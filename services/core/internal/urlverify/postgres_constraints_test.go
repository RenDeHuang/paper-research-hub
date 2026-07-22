package urlverify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresOfficialURLVerificationRequiresControlledWriterRole(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	_, candidate, _ := insertURLVerifyStoreFixture(
		t,
		pool,
		"controlled-writer",
	)
	finalURL := "https://publisher.example.test/controlled-writer/final"
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin runtime role transaction: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE paper_hub_worker"); err != nil {
		t.Fatalf("set runtime role: %v", err)
	}
	if _, _, err := insertDirectVerifiedURLVerificationWithQuerier(
		tx,
		candidate,
		finalURL,
		directRedirectChain(candidate.URL, finalURL),
		[]map[string]any{stableIdentifierJSON(candidate.Identifier)},
		[]map[string]any{identifierEvidenceJSON(
			candidate.Identifier,
			IdentifierSourceHTMLMeta,
			`meta[name="citation_doi"]@content`,
			candidate.Identifier.Value,
		)},
	); !isPostgresErrorCode(err, "42501") {
		t.Fatalf(
			"runtime direct verified INSERT error = %v, want insufficient_privilege",
			err,
		)
	}
}

func TestPostgresOfficialURLHostRegistrySealsExactVersion(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	ctx := context.Background()
	const (
		policyVersion = "official-url/test-sealed-v1"
		fileSHA256    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin host Registry import: %v", err)
	}
	var registryVersionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO official_url_registry_versions (
			registry_name,
			policy_version,
			file_sha256,
			host_count,
			imported_at
		) VALUES (
			'official-url-hosts',
			$1,
			$2,
			1,
			TIMESTAMPTZ '2026-07-19 00:00:00+00'
		)
		RETURNING id::text
	`, policyVersion, fileSHA256).Scan(&registryVersionID); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("insert host Registry receipt: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO official_url_host_registry (
			registry_version_id,
			policy_version,
			content_channel,
			link_role,
			hostname,
			registry_source,
			source_reference,
			registered_at
		) VALUES (
			$1,
			$2,
			'journal_published',
			'official_article',
			'sealed.example.test',
			'test_fixture',
			'sealed-host-registry/v1',
			TIMESTAMPTZ '2026-07-19 00:00:00+00'
		)
	`, registryVersionID, policyVersion); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("insert host Registry entry: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE official_url_registry_versions
		SET sealed_at = TIMESTAMPTZ '2026-07-19 00:00:00+00'
		WHERE id = $1
	`, registryVersionID); err != nil {
		_ = tx.Rollback(context.Background())
		t.Fatalf("seal host Registry receipt: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit sealed host Registry: %v", err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO official_url_host_registry (
			registry_version_id,
			policy_version,
			content_channel,
			link_role,
			hostname,
			registry_source,
			source_reference,
			registered_at
		) VALUES (
			$1,
			$2,
			'journal_published',
			'official_article',
			'late.example.test',
			'test_fixture',
			'late-host-registry/v1',
			TIMESTAMPTZ '2026-07-19 00:00:01+00'
		)
	`, registryVersionID, policyVersion)
	if !isPostgresErrorCode(err, "55000") {
		t.Fatalf(
			"append to sealed host Registry error = %v, want object_not_in_prerequisite_state",
			err,
		)
	}
}

func isPostgresErrorCode(err error, code string) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == code
}

func TestPostgresOfficialURLSchemaRejectsURLsOutsideGoAcceptedSubset(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	ctx := context.Background()

	for _, rawURL := range []string{
		"https:///missing-host",
		"https://user:password@publisher.example.test/article",
		"https://publisher.example.test/article#fragment",
		"https://publisher.example.test/article/%zz",
		"https://publisher.example.test/article/%",
	} {
		var accepted bool
		if err := pool.QueryRow(ctx, `
			SELECT official_url_is_accepted($1, false)
		`, rawURL).Scan(&accepted); err != nil {
			t.Fatalf("validate official URL %q: %v", rawURL, err)
		}
		if accepted {
			t.Fatalf("official_url_is_accepted(%q) = true", rawURL)
		}
	}

	for index, rawURL := range []string{
		"https:///missing-host",
		"https://user:password@publisher.example.test/article",
		"https://publisher.example.test/article#fragment",
		"https://publisher.example.test/article/%zz",
		"https://publisher.example.test/article/%",
	} {
		suffix := "invalid-candidate-" + string(rune('a'+index))
		fixture := insertCurrentURLProjectionFixture(
			t,
			pool,
			suffix,
			"normalized-record/v4",
			normalizedURLPayload(
				"crossref/crossref-work-v4",
				[]map[string]any{{
					"url":               rawURL,
					"source_path":       "$.message.URL",
					"content_channel":   "journal_published",
					"link_role":         "official_article",
					"parser_version":    "crossref/crossref-work-v4",
					"identifier_scheme": "doi",
					"identifier_value":  "10.1000/" + suffix,
				}},
			),
		)
		_, err := pool.Exec(ctx, `
			INSERT INTO work_url_candidates (
				work_id,
				projection_assertion_id,
				normalized_assertion_id,
				source_record_id,
				source_path,
				content_channel,
				link_role,
				candidate_url,
				parser_version,
				identifier_scheme,
				identifier_value,
				asserted_at
			) VALUES (
				$1, $2, $3, $4, '$.message.URL',
				'journal_published', 'official_article', $5,
				'crossref/crossref-work-v4', 'doi', $6, $7
			)
		`,
			fixture.WorkID,
			fixture.ProjectionAssertionID,
			fixture.NormalizedAssertionID,
			fixture.SourceRecordID,
			rawURL,
			"10.1000/"+suffix,
			fixture.SourceTime,
		)
		assertURLVerifyConstraintViolation(t, err)
	}

	httpFixture := insertCurrentURLProjectionFixture(
		t,
		pool,
		"http-source-candidate",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               "http://publisher.example.test/article",
				"source_path":       "$.message.URL",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/http-source-candidate",
			}},
		),
	)
	_, err := pool.Exec(ctx, `
		INSERT INTO work_url_candidates (
			work_id,
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			source_path,
			content_channel,
			link_role,
			candidate_url,
			parser_version,
			identifier_scheme,
			identifier_value,
			asserted_at
		) VALUES (
			$1, $2, $3, $4, '$.message.URL',
			'journal_published', 'official_article',
			'http://publisher.example.test/article',
			'crossref/crossref-work-v4', 'doi',
			'10.1000/http-source-candidate', $5
		)
	`,
		httpFixture.WorkID,
		httpFixture.ProjectionAssertionID,
		httpFixture.NormalizedAssertionID,
		httpFixture.SourceRecordID,
		httpFixture.SourceTime,
	)
	assertURLVerifyConstraintViolation(t, err)

	store, candidate, fixture := insertURLVerifyStoreFixture(
		t,
		pool,
		"invalid-verification-urls",
	)
	_ = store
	for _, finalURL := range []string{
		"https:///missing-host",
		"https://user:password@publisher.example.test/article",
		"https://publisher.example.test/article#fragment",
		"https://publisher.example.test/article/%zz",
		"https://publisher.example.test/article/%",
	} {
		_, _, err := insertDirectVerifiedURLVerification(
			pool,
			candidate,
			finalURL,
			directRedirectChain(candidate.URL, finalURL),
			[]map[string]any{stableIdentifierJSON(candidate.Identifier)},
			[]map[string]any{identifierEvidenceJSON(
				candidate.Identifier,
				IdentifierSourceHTMLMeta,
				`meta[name="citation_doi"]@content`,
				candidate.Identifier.Value,
			)},
		)
		assertURLVerifyConstraintViolation(t, err)
	}

	finalURL := "https://publisher.example.test/invalid-verification-urls/final"
	for _, intermediateURL := range []string{
		"http://publisher.example.test/intermediate",
		"https://user:password@publisher.example.test/intermediate",
		"https://publisher.example.test/intermediate#fragment",
		"https://publisher.example.test/intermediate/%zz",
		"https://publisher.example.test/intermediate/%",
	} {
		chain := []RedirectHop{
			{
				URL:        candidate.URL,
				StatusCode: http.StatusFound,
				Location:   intermediateURL,
			},
			{
				URL:        intermediateURL,
				StatusCode: http.StatusFound,
				Location:   finalURL,
			},
			{
				URL:        finalURL,
				StatusCode: http.StatusOK,
			},
		}
		_, _, err := insertDirectVerifiedURLVerification(
			pool,
			candidate,
			finalURL,
			chain,
			[]map[string]any{stableIdentifierJSON(candidate.Identifier)},
			[]map[string]any{identifierEvidenceJSON(
				candidate.Identifier,
				IdentifierSourceHTMLMeta,
				`meta[name="citation_doi"]@content`,
				candidate.Identifier.Value,
			)},
		)
		assertURLVerifyConstraintViolation(t, err)
	}

	var count int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM work_url_verifications
		WHERE candidate_id = $1
	`, candidate.ID).Scan(&count); err != nil {
		t.Fatalf("count invalid URL verifications: %v", err)
	}
	if count != 0 {
		t.Fatalf(
			"invalid URL verification count = %d for fixture %#v",
			count,
			fixture,
		)
	}
}

func TestPostgresOfficialURLSchemaAcceptsValidPercentEncoding(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	ctx := context.Background()
	sourceURL := "https://resolver.example.test/articles%2Fsource?view=%2Ffull"
	var accepted bool
	if err := pool.QueryRow(ctx, `
		SELECT official_url_is_accepted($1, false)
	`, sourceURL).Scan(&accepted); err != nil {
		t.Fatalf("validate percent-encoded official URL: %v", err)
	}
	if !accepted {
		t.Fatalf("official_url_is_accepted(%q) = false", sourceURL)
	}

	fixture := insertCurrentURLProjectionFixture(
		t,
		pool,
		"valid-percent-encoding",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               sourceURL,
				"source_path":       "$.message.URL",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/valid-percent-encoding",
			}},
		),
	)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	candidates, err := store.ImportCurrentProjectionCandidates(
		ctx,
		CurrentProjectionRef{
			WorkID:                fixture.WorkID,
			NormalizedAssertionID: fixture.NormalizedAssertionID,
		},
	)
	if err != nil || len(candidates) != 1 {
		t.Fatalf(
			"ImportCurrentProjectionCandidates() = %#v, %v",
			candidates,
			err,
		)
	}
	candidate := candidates[0]
	intermediateURL := "https://publisher.example.test/redirect%2Fhop"
	finalURL := "https://publisher.example.test/article%2Ffinal?view=%2Ffull"
	chain := []RedirectHop{
		{
			URL:        candidate.URL,
			StatusCode: http.StatusFound,
			Location:   intermediateURL,
		},
		{
			URL:        intermediateURL,
			StatusCode: http.StatusTemporaryRedirect,
			Location:   finalURL,
		},
		{
			URL:        finalURL,
			StatusCode: http.StatusOK,
		},
	}
	if _, _, err := insertDirectVerifiedURLVerification(
		pool,
		candidate,
		finalURL,
		chain,
		[]map[string]any{stableIdentifierJSON(candidate.Identifier)},
		[]map[string]any{identifierEvidenceJSON(
			candidate.Identifier,
			IdentifierSourceHTMLMeta,
			`meta[name="citation_doi"]@content`,
			candidate.Identifier.Value,
		)},
	); err != nil {
		t.Fatalf("insert valid percent-encoded verification: %v", err)
	}
}

func TestPostgresOfficialURLSchemaRejectsForgedIdentifierEvidence(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, candidate, fixture := insertURLVerifyStoreFixture(
		t,
		pool,
		"forged-evidence-sql",
	)
	finalURL := "https://publisher.example.test/forged-evidence-sql/final"
	validIdentifier := stableIdentifierJSON(candidate.Identifier)

	tests := []struct {
		name     string
		observed any
		evidence any
		project  bool
	}{
		{
			name:     "title source",
			observed: []map[string]any{validIdentifier},
			evidence: []map[string]any{identifierEvidenceJSON(
				candidate.Identifier,
				"title",
				"title",
				candidate.Identifier.Value,
			)},
			project: true,
		},
		{
			name:     "scheme conflicts with path",
			observed: []map[string]any{validIdentifier},
			evidence: []map[string]any{identifierEvidenceJSON(
				candidate.Identifier,
				IdentifierSourceHTMLMeta,
				`meta[name="citation_pmid"]@content`,
				candidate.Identifier.Value,
			)},
		},
		{
			name:     "empty raw value",
			observed: []map[string]any{validIdentifier},
			evidence: []map[string]any{identifierEvidenceJSON(
				candidate.Identifier,
				IdentifierSourceHTMLMeta,
				`meta[name="citation_doi"]@content`,
				"",
			)},
		},
		{
			name:     "extra evidence field",
			observed: []map[string]any{validIdentifier},
			evidence: []map[string]any{func() map[string]any {
				value := identifierEvidenceJSON(
					candidate.Identifier,
					IdentifierSourceHTMLMeta,
					`meta[name="citation_doi"]@content`,
					candidate.Identifier.Value,
				)
				value["title"] = "forged"
				return value
			}()},
		},
		{
			name: "extra identifier field",
			observed: []map[string]any{{
				"scheme": "doi",
				"value":  candidate.Identifier.Value,
				"title":  "forged",
			}},
			evidence: []map[string]any{{
				"identifier": map[string]any{
					"scheme": "doi",
					"value":  candidate.Identifier.Value,
					"title":  "forged",
				},
				"source_kind": IdentifierSourceHTMLMeta,
				"source_path": `meta[name="citation_doi"]@content`,
				"raw_value":   candidate.Identifier.Value,
			}},
		},
		{
			name:     "non object evidence",
			observed: []map[string]any{validIdentifier},
			evidence: []any{"forged"},
		},
		{
			name:     "non object observed identifier",
			observed: []any{"forged"},
			evidence: []map[string]any{identifierEvidenceJSON(
				candidate.Identifier,
				IdentifierSourceHTMLMeta,
				`meta[name="citation_doi"]@content`,
				candidate.Identifier.Value,
			)},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			verificationID, checkedAt, err :=
				insertDirectVerifiedURLVerification(
					pool,
					candidate,
					finalURL,
					directRedirectChain(candidate.URL, finalURL),
					test.observed,
					test.evidence,
				)
			if err == nil {
				if test.project {
					if link, projectErr := store.ProjectCurrent(
						context.Background(),
						verificationID,
						checkedAt.Add(time.Minute),
					); projectErr == nil {
						t.Fatalf(
							"forged verification projected current link %#v",
							link,
						)
					}
				}
				t.Fatalf(
					"insert forged identifier evidence returned verification %s",
					verificationID,
				)
			}
			assertURLVerifyConstraintViolation(t, err)
		})
	}
	_, _, nulErr := insertDirectVerifiedURLVerification(
		pool,
		candidate,
		finalURL,
		directRedirectChain(candidate.URL, finalURL),
		[]map[string]any{validIdentifier},
		[]map[string]any{identifierEvidenceJSON(
			candidate.Identifier,
			IdentifierSourceHTMLMeta,
			`meta[name="citation_doi"]@content`,
			"\x00",
		)},
	)
	if nulErr == nil {
		t.Fatal("insert identifier evidence with NUL raw_value error = nil")
	}

	var verificationCount, projectionCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM work_url_verifications
			 WHERE candidate_id = $1),
			(SELECT count(*) FROM current_work_official_links
			 WHERE work_id = $2)
	`, candidate.ID, fixture.WorkID).Scan(
		&verificationCount,
		&projectionCount,
	); err != nil {
		t.Fatalf("count forged official URL rows: %v", err)
	}
	if verificationCount != 0 || projectionCount != 0 {
		t.Fatalf(
			"forged rows persisted: verifications=%d projections=%d",
			verificationCount,
			projectionCount,
		)
	}
}

func TestPostgresOfficialURLSchemaRejectsForgedRedirectTransitions(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, candidate, fixture := insertURLVerifyStoreFixture(
		t,
		pool,
		"forged-redirect-transitions",
	)
	finalURL := "https://publisher.example.test/redirect-transitions/final"
	observed := []map[string]any{stableIdentifierJSON(candidate.Identifier)}
	evidence := []map[string]any{identifierEvidenceJSON(
		candidate.Identifier,
		IdentifierSourceHTMLMeta,
		`meta[name="citation_doi"]@content`,
		candidate.Identifier.Value,
	)}
	tests := []struct {
		name  string
		chain []RedirectHop
	}{
		{
			name: "malformed location",
			chain: []RedirectHop{
				{
					URL:        candidate.URL,
					StatusCode: http.StatusFound,
					Location:   "https://publisher.example.test/%zz",
				},
				{
					URL:        finalURL,
					StatusCode: http.StatusOK,
				},
			},
		},
		{
			name: "location credentials",
			chain: []RedirectHop{
				{
					URL:        candidate.URL,
					StatusCode: http.StatusFound,
					Location: "https://user:password@" +
						"publisher.example.test/redirect-transitions/final",
				},
				{
					URL:        finalURL,
					StatusCode: http.StatusOK,
				},
			},
		},
		{
			name: "location fragment",
			chain: []RedirectHop{
				{
					URL:        candidate.URL,
					StatusCode: http.StatusFound,
					Location:   "/redirect-transitions/final#fragment",
				},
				{
					URL:        finalURL,
					StatusCode: http.StatusOK,
				},
			},
		},
		{
			name: "non redirect intermediate",
			chain: []RedirectHop{
				{
					URL:        candidate.URL,
					StatusCode: http.StatusOK,
					Location:   finalURL,
				},
				{
					URL:        finalURL,
					StatusCode: http.StatusOK,
				},
			},
		},
		{
			name: "discontinuous chain",
			chain: []RedirectHop{
				{
					URL:        candidate.URL,
					StatusCode: http.StatusTemporaryRedirect,
					Location:   "/redirect-transitions/expected",
				},
				{
					URL:        finalURL,
					StatusCode: http.StatusOK,
				},
			},
		},
		{
			name: "double slash discontinuity",
			chain: []RedirectHop{
				{
					URL:        candidate.URL,
					StatusCode: http.StatusTemporaryRedirect,
					Location:   "/redirect-transitions//intermediate",
				},
				{
					URL: "https://resolver.example.test/" +
						"redirect-transitions/intermediate",
					StatusCode: http.StatusFound,
					Location:   finalURL,
				},
				{
					URL:        finalURL,
					StatusCode: http.StatusOK,
				},
			},
		},
		{
			name: "terminal location",
			chain: []RedirectHop{
				{
					URL:        candidate.URL,
					StatusCode: http.StatusFound,
					Location:   finalURL,
				},
				{
					URL:        finalURL,
					StatusCode: http.StatusOK,
					Location:   "/unexpected-next",
				},
			},
		},
		{
			name: "canonical URL loop",
			chain: []RedirectHop{
				{
					URL:        candidate.URL,
					StatusCode: http.StatusFound,
					Location: "https://resolver.example.test/" +
						"redirect-transitions/intermediate",
				},
				{
					URL: "https://resolver.example.test/" +
						"redirect-transitions/intermediate",
					StatusCode: http.StatusTemporaryRedirect,
					Location:   candidate.URL,
				},
				{
					URL:        candidate.URL,
					StatusCode: http.StatusSeeOther,
					Location:   finalURL,
				},
				{
					URL:        finalURL,
					StatusCode: http.StatusOK,
				},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			verificationID, checkedAt, err :=
				insertDirectVerifiedURLVerification(
					pool,
					candidate,
					finalURL,
					test.chain,
					observed,
					evidence,
				)
			if err == nil {
				if link, projectErr := store.ProjectCurrent(
					context.Background(),
					verificationID,
					checkedAt.Add(time.Minute),
				); projectErr == nil {
					t.Fatalf(
						"forged redirect verification projected current link %#v",
						link,
					)
				}
				t.Fatalf(
					"insert forged redirect chain returned verification %s",
					verificationID,
				)
			}
			assertURLVerifyConstraintViolation(t, err)
		})
	}

	var verificationCount, projectionCount int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM work_url_verifications
			 WHERE candidate_id = $1),
			(SELECT count(*) FROM current_work_official_links
			 WHERE work_id = $2)
	`, candidate.ID, fixture.WorkID).Scan(
		&verificationCount,
		&projectionCount,
	); err != nil {
		t.Fatalf("count forged redirect rows: %v", err)
	}
	if verificationCount != 0 || projectionCount != 0 {
		t.Fatalf(
			"forged redirect rows persisted: verifications=%d projections=%d",
			verificationCount,
			projectionCount,
		)
	}
}

func TestPostgresOfficialURLSchemaAcceptsRFCRelativeRedirectLocation(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	tests := []struct {
		name     string
		suffix   string
		location string
	}{
		{
			name:     "relative reference",
			suffix:   "relative-redirect-location",
			location: "../resolved/./final",
		},
		{
			name:     "absolute reference removes dot segments",
			suffix:   "absolute-redirect-location",
			location: "https://resolver.example.test/a/../resolved/final",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			store, candidate, _ := insertURLVerifyStoreFixture(
				t,
				pool,
				test.suffix,
			)
			finalURL := "https://resolver.example.test/resolved/final"
			verificationID, checkedAt, err :=
				insertDirectVerifiedURLVerification(
					pool,
					candidate,
					finalURL,
					[]RedirectHop{
						{
							URL:        candidate.URL,
							StatusCode: http.StatusTemporaryRedirect,
							Location:   test.location,
						},
						{
							URL:        finalURL,
							StatusCode: http.StatusOK,
						},
					},
					[]map[string]any{
						stableIdentifierJSON(candidate.Identifier),
					},
					[]map[string]any{identifierEvidenceJSON(
						candidate.Identifier,
						IdentifierSourceHTMLMeta,
						`meta[name="citation_doi"]@content`,
						candidate.Identifier.Value,
					)},
				)
			if err != nil {
				t.Fatalf("insert RFC redirect verification: %v", err)
			}
			if _, err := store.ProjectCurrent(
				context.Background(),
				verificationID,
				checkedAt.Add(time.Minute),
			); err != nil {
				t.Fatalf("ProjectCurrent(RFC redirect) error = %v", err)
			}
		})
	}
}

func TestPostgresOfficialURLSchemaRejectsVerifiedHostAbsentRegistry(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	fixture := insertCurrentURLProjectionFixture(
		t,
		pool,
		"unregistered-official-host",
		"normalized-record/v4",
		normalizedURLPayload(
			"official/normalized-v4",
			[]map[string]any{{
				"url": "https://unregistered.example.test/" +
					"article",
				"source_path":       "$.official_url",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "official/normalized-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/unregistered-host",
			}},
		),
	)
	candidates, err := store.ImportCurrentProjectionCandidates(
		context.Background(),
		CurrentProjectionRef{
			WorkID:                fixture.WorkID,
			NormalizedAssertionID: fixture.NormalizedAssertionID,
		},
	)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("import unregistered candidate = %#v, %v", candidates, err)
	}
	candidate := candidates[0]
	verificationID, checkedAt, err :=
		insertDirectVerifiedURLVerification(
			pool,
			candidate,
			candidate.URL,
			[]RedirectHop{{
				URL:        candidate.URL,
				StatusCode: http.StatusOK,
			}},
			[]map[string]any{stableIdentifierJSON(candidate.Identifier)},
			[]map[string]any{identifierEvidenceJSON(
				candidate.Identifier,
				IdentifierSourceHTMLMeta,
				`meta[name="citation_doi"]@content`,
				candidate.Identifier.Value,
			)},
		)
	if err == nil {
		if link, projectErr := store.ProjectCurrent(
			context.Background(),
			verificationID,
			checkedAt.Add(time.Minute),
		); projectErr == nil {
			t.Fatalf(
				"unregistered verified host projected %#v",
				link,
			)
		}
		t.Fatalf(
			"unregistered verified host inserted verification %s",
			verificationID,
		)
	}
	assertURLVerifyConstraintViolation(t, err)
}

func TestPostgresOfficialURLSchemaBindsIdentifierEvidenceRawValue(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	tests := []struct {
		name        string
		identifier  StableIdentifier
		sourcePath  string
		validRaw    string
		mismatchRaw string
	}{
		{
			name: "citation DOI",
			identifier: StableIdentifier{
				Scheme: "doi",
				Value:  "10.1000/sql-raw-doi",
			},
			sourcePath:  `meta[name="citation_doi"]@content`,
			validRaw:    " DOI:10.1000/SQL-RAW-DOI ",
			mismatchRaw: "10.1000/sql-raw-other",
		},
		{
			name: "citation PMID",
			identifier: StableIdentifier{
				Scheme: "pmid",
				Value:  "123456",
			},
			sourcePath:  `meta[name="citation_pmid"]@content`,
			validRaw:    " 123456 ",
			mismatchRaw: "654321",
		},
		{
			name: "citation PMCID",
			identifier: StableIdentifier{
				Scheme: "pmcid",
				Value:  "PMC123456",
			},
			sourcePath:  `meta[name="citation_pmcid"]@content`,
			validRaw:    " pmc123456 ",
			mismatchRaw: "PMC654321",
		},
		{
			name: "citation arXiv",
			identifier: StableIdentifier{
				Scheme: "arxiv",
				Value:  "2401.01234",
			},
			sourcePath:  `meta[name="citation_arxiv_id"]@content`,
			validRaw:    " arXiv:2401.01234V2 ",
			mismatchRaw: "arXiv:2401.54321",
		},
		{
			name: "DC DOI",
			identifier: StableIdentifier{
				Scheme: "doi",
				Value:  "10.1000/sql-raw-dc",
			},
			sourcePath:  `meta[name="dc.identifier"]@content`,
			validRaw:    "https://doi.org/10.1000/SQL-RAW-DC",
			mismatchRaw: "doi:10.1000/sql-raw-other",
		},
	}
	for index, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			suffix := "raw-evidence-" + string(rune('a'+index))
			store, candidate, _ := insertURLVerifyStoreFixtureWithIdentifier(
				t,
				pool,
				suffix,
				test.identifier,
			)
			finalURL := "https://publisher.example.test/" + suffix + "/final"
			observed := []map[string]any{
				stableIdentifierJSON(candidate.Identifier),
			}
			validEvidence := []map[string]any{identifierEvidenceJSON(
				candidate.Identifier,
				IdentifierSourceHTMLMeta,
				test.sourcePath,
				test.validRaw,
			)}
			if _, _, err := insertDirectVerifiedURLVerification(
				pool,
				candidate,
				finalURL,
				directRedirectChain(candidate.URL, finalURL),
				observed,
				validEvidence,
			); err != nil {
				t.Fatalf("insert valid raw identifier evidence: %v", err)
			}

			mismatchEvidence := []map[string]any{identifierEvidenceJSON(
				candidate.Identifier,
				IdentifierSourceHTMLMeta,
				test.sourcePath,
				test.mismatchRaw,
			)}
			verificationID, checkedAt, err :=
				insertDirectVerifiedURLVerification(
					pool,
					candidate,
					finalURL,
					directRedirectChain(candidate.URL, finalURL),
					observed,
					mismatchEvidence,
				)
			if err == nil {
				if link, projectErr := store.ProjectCurrent(
					context.Background(),
					verificationID,
					checkedAt.Add(time.Minute),
				); projectErr == nil {
					t.Fatalf(
						"raw mismatch verification projected current link %#v",
						link,
					)
				}
				t.Fatalf(
					"insert raw mismatch evidence returned verification %s",
					verificationID,
				)
			}
			assertURLVerifyConstraintViolation(t, err)
		})
	}
}

func insertDirectVerifiedURLVerification(
	pool *pgxpool.Pool,
	candidate Candidate,
	finalURL string,
	redirectChain []RedirectHop,
	observed any,
	evidence any,
) (string, time.Time, error) {
	return insertDirectVerifiedURLVerificationWithQuerier(
		pool,
		candidate,
		finalURL,
		redirectChain,
		observed,
		evidence,
	)
}

type verificationQueryRower interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func insertDirectVerifiedURLVerificationWithQuerier(
	querier verificationQueryRower,
	candidate Candidate,
	finalURL string,
	redirectChain []RedirectHop,
	observed any,
	evidence any,
) (string, time.Time, error) {
	redirectJSON, _ := json.Marshal(redirectChain)
	observedJSON, _ := json.Marshal(observed)
	evidenceJSON, _ := json.Marshal(evidence)
	metadataJSON, _ := json.Marshal(ResponseMetadata{})
	checkedAt := candidate.AssertedAt.Add(time.Hour)
	var verificationID string
	err := querier.QueryRow(context.Background(), `
		INSERT INTO work_url_verifications (
			candidate_id,
			source_url,
			final_url,
			redirect_chain,
			http_status,
			expected_identifier_scheme,
			expected_identifier_value,
			observed_identifiers,
			identifier_evidence,
			response_metadata,
			matched_identifier_scheme,
			matched_identifier_value,
			identifier_match,
			verification_state,
			checked_at,
			expires_at,
			verifier_version,
			policy_version,
			failure_code
		) VALUES (
			$1, $2, $3, $4, 200, $5, $6, $7, $8, $9,
			$5, $6, true, 'verified', $10, $11,
			'direct-forged-test/v1', 'official-url/v1', NULL
		)
		RETURNING id::text
	`,
		candidate.ID,
		candidate.URL,
		finalURL,
		redirectJSON,
		candidate.Identifier.Scheme,
		candidate.Identifier.Value,
		observedJSON,
		evidenceJSON,
		metadataJSON,
		checkedAt,
		checkedAt.Add(24*time.Hour),
	).Scan(&verificationID)
	return verificationID, checkedAt, err
}

func insertURLVerifyStoreFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	suffix string,
) (*PostgresStore, Candidate, urlProjectionFixture) {
	return insertURLVerifyStoreFixtureWithIdentifier(
		t,
		pool,
		suffix,
		StableIdentifier{
			Scheme: "doi",
			Value:  "10.1000/" + suffix,
		},
	)
}

func insertURLVerifyStoreFixtureWithIdentifier(
	t *testing.T,
	pool *pgxpool.Pool,
	suffix string,
	identifier StableIdentifier,
) (*PostgresStore, Candidate, urlProjectionFixture) {
	t.Helper()
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	sourceURL := "https://resolver.example.test/10.1000/" + suffix
	fixture := insertCurrentURLProjectionFixture(
		t,
		pool,
		suffix,
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               sourceURL,
				"source_path":       "$.message.URL",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": identifier.Scheme,
				"identifier_value":  identifier.Value,
			}},
		),
	)
	candidates, err := store.ImportCurrentProjectionCandidates(
		context.Background(),
		CurrentProjectionRef{
			WorkID:                fixture.WorkID,
			NormalizedAssertionID: fixture.NormalizedAssertionID,
		},
	)
	if err != nil {
		t.Fatalf("ImportCurrentProjectionCandidates() error = %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(candidates))
	}
	return store, candidates[0], fixture
}

func stableIdentifierJSON(identifier StableIdentifier) map[string]any {
	return map[string]any{
		"scheme": identifier.Scheme,
		"value":  identifier.Value,
	}
}

func identifierEvidenceJSON(
	identifier StableIdentifier,
	sourceKind string,
	sourcePath string,
	rawValue string,
) map[string]any {
	return map[string]any{
		"identifier":  stableIdentifierJSON(identifier),
		"source_kind": sourceKind,
		"source_path": sourcePath,
		"raw_value":   rawValue,
	}
}
