package urlverify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/database"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

func TestPostgresStoreImportsExactCurrentV4CandidatesAndVersionsOfficialLinks(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	ctx := context.Background()
	sourceURL := "https://resolver.example.test/10.1000/url-store"
	payload := normalizedURLPayload(
		"crossref/crossref-work-v4",
		[]map[string]any{{
			"url":               sourceURL,
			"source_path":       "$.message.URL",
			"content_channel":   "journal_published",
			"link_role":         "official_article",
			"parser_version":    "crossref/crossref-work-v4",
			"identifier_scheme": "doi",
			"identifier_value":  "10.1000/url-store",
		}},
	)
	fixture := insertCurrentURLProjectionFixture(
		t,
		pool,
		"main",
		"normalized-record/v4",
		payload,
	)
	reference := CurrentProjectionRef{
		WorkID:                fixture.WorkID,
		NormalizedAssertionID: fixture.NormalizedAssertionID,
	}

	firstImport, err := store.ImportCurrentProjectionCandidates(ctx, reference)
	if err != nil {
		t.Fatalf("ImportCurrentProjectionCandidates(first) error = %v", err)
	}
	secondImport, err := store.ImportCurrentProjectionCandidates(ctx, reference)
	if err != nil {
		t.Fatalf("ImportCurrentProjectionCandidates(replay) error = %v", err)
	}
	if len(firstImport) != 1 ||
		len(secondImport) != 1 ||
		firstImport[0].ID == "" ||
		secondImport[0].ID != firstImport[0].ID {
		t.Fatalf("candidate imports = %#v / %#v", firstImport, secondImport)
	}
	candidate := firstImport[0]
	if candidate.WorkID != fixture.WorkID ||
		candidate.SourceRecordID != fixture.SourceRecordID ||
		candidate.ProjectionAssertionID != fixture.ProjectionAssertionID ||
		candidate.NormalizedAssertionID != fixture.NormalizedAssertionID ||
		candidate.SourcePath != "$.message.URL" ||
		candidate.URL != sourceURL ||
		candidate.ParserVersion != "crossref/crossref-work-v4" ||
		candidate.Identifier !=
			(StableIdentifier{Scheme: "doi", Value: "10.1000/url-store"}) {
		t.Fatalf("imported candidate = %#v", candidate)
	}
	loadedCandidate, found, err := store.FindCandidate(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("FindCandidate() error = %v", err)
	}
	if !found || !reflect.DeepEqual(loadedCandidate, candidate) {
		t.Fatalf("FindCandidate() = %#v, %t, want %#v", loadedCandidate, found, candidate)
	}
	var candidateCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM work_url_candidates
		WHERE normalized_assertion_id = $1
		  AND source_path = $2
		  AND candidate_url = $3
	`, fixture.NormalizedAssertionID, candidate.SourcePath, candidate.URL).Scan(
		&candidateCount,
	); err != nil {
		t.Fatalf("count exact candidate identity: %v", err)
	}
	if candidateCount != 1 {
		t.Fatalf("exact candidate count = %d, want 1", candidateCount)
	}

	other := insertCurrentURLProjectionFixture(
		t,
		pool,
		"other",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               "https://guessed.example.test/from-title",
				"source_path":       "$.message.URL",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/other",
			}},
		),
	)
	if _, err := store.ImportCurrentProjectionCandidates(
		ctx,
		CurrentProjectionRef{
			WorkID:                fixture.WorkID,
			NormalizedAssertionID: other.NormalizedAssertionID,
		},
	); !errors.Is(err, ErrCurrentProjectionNotFound) {
		t.Fatalf("stale/mismatched current projection error = %v", err)
	}

	invalid := insertCurrentURLProjectionFixture(
		t,
		pool,
		"invalid",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               "https://publisher.example.test/invalid",
				"source_path":       "",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/invalid",
			}},
		),
	)
	if _, err := store.ImportCurrentProjectionCandidates(
		ctx,
		CurrentProjectionRef{
			WorkID:                invalid.WorkID,
			NormalizedAssertionID: invalid.NormalizedAssertionID,
		},
	); !errors.Is(err, ErrInvalidNormalizedURLCandidate) {
		t.Fatalf("invalid normalized candidate error = %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM work_url_candidates WHERE work_id = $1
	`, invalid.WorkID).Scan(&candidateCount); err != nil {
		t.Fatalf("count invalid Work candidates: %v", err)
	}
	if candidateCount != 0 {
		t.Fatalf("invalid candidate import count = %d, want 0", candidateCount)
	}

	legacy := insertCurrentURLProjectionFixture(
		t,
		pool,
		"legacy",
		"normalized-record/v3",
		payload,
	)
	if _, err := store.ImportCurrentProjectionCandidates(
		ctx,
		CurrentProjectionRef{
			WorkID:                legacy.WorkID,
			NormalizedAssertionID: legacy.NormalizedAssertionID,
		},
	); !errors.Is(err, ErrCurrentProjectionNotV4) {
		t.Fatalf("legacy normalized projection error = %v", err)
	}

	firstInput := verificationInputForCandidate(
		candidate,
		fixture.SourceTime.Add(time.Hour),
		"https://publisher.example.test/article",
	)
	firstVerification, err := Verify(firstInput)
	if err != nil {
		t.Fatalf("Verify(first) error = %v", err)
	}
	firstVerification, err = store.PersistVerification(ctx, firstVerification)
	if err != nil {
		t.Fatalf("PersistVerification(first) error = %v", err)
	}
	loadedVerification, found, err := store.FindVerification(
		ctx,
		firstVerification.ID,
	)
	if err != nil {
		t.Fatalf("FindVerification() error = %v", err)
	}
	if !found || !reflect.DeepEqual(loadedVerification, firstVerification) {
		t.Fatalf(
			"FindVerification() = %#v, %t, want %#v",
			loadedVerification,
			found,
			firstVerification,
		)
	}
	firstLink, err := store.ProjectCurrent(
		ctx,
		firstVerification.ID,
		firstInput.CheckedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("ProjectCurrent(first) error = %v", err)
	}
	if firstLink.ProjectionVersion != 1 ||
		firstLink.URL != firstVerification.FinalURL {
		t.Fatalf("first current link = %#v", firstLink)
	}

	secondInput := verificationInputForCandidate(
		candidate,
		firstInput.CheckedAt.Add(2*time.Hour),
		"https://publisher.example.test/article-v2",
	)
	secondVerification, err := Verify(secondInput)
	if err != nil {
		t.Fatalf("Verify(second) error = %v", err)
	}
	secondVerification, err = store.PersistVerification(
		ctx,
		secondVerification,
	)
	if err != nil {
		t.Fatalf("PersistVerification(second) error = %v", err)
	}
	secondLink, err := store.ProjectCurrent(
		ctx,
		secondVerification.ID,
		secondInput.CheckedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("ProjectCurrent(second) error = %v", err)
	}
	if secondLink.ProjectionVersion != 2 ||
		secondLink.VerificationID != secondVerification.ID {
		t.Fatalf("second current link = %#v", secondLink)
	}
	current, found, err := store.Current(
		ctx,
		fixture.WorkID,
		candidate.LinkRole,
		secondInput.CheckedAt.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if !found || !reflect.DeepEqual(current, secondLink) {
		t.Fatalf("Current() = %#v, %t, want %#v", current, found, secondLink)
	}

	var verificationCount, projectionCount int
	if err := pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM work_url_verifications WHERE candidate_id = $1),
			(
				SELECT count(*)
				FROM current_work_official_links
				WHERE work_id = $2 AND link_role = $3
			)
	`, candidate.ID, fixture.WorkID, candidate.LinkRole).Scan(
		&verificationCount,
		&projectionCount,
	); err != nil {
		t.Fatalf("count URL history: %v", err)
	}
	if verificationCount != 2 || projectionCount != 2 {
		t.Fatalf(
			"history counts = verifications %d projections %d",
			verificationCount,
			projectionCount,
		)
	}

	failedInput := verificationInputForCandidate(
		candidate,
		secondInput.CheckedAt.Add(2*time.Hour),
		"https://publisher.example.test/mismatch",
	)
	failedInput.Observation.IdentifierEvidence = testIdentifierEvidence(
		StableIdentifier{
			Scheme: "doi",
			Value:  "10.1000/different",
		},
	)
	failedVerification, err := Verify(failedInput)
	if err != nil {
		t.Fatalf("Verify(failed) error = %v", err)
	}
	failedVerification, err = store.PersistVerification(
		ctx,
		failedVerification,
	)
	if err != nil {
		t.Fatalf("PersistVerification(failed) error = %v", err)
	}
	if _, err := store.ProjectCurrent(
		ctx,
		failedVerification.ID,
		failedInput.CheckedAt,
	); !errors.Is(err, ErrVerificationNotVerified) {
		t.Fatalf("project failed verification error = %v", err)
	}

	expiringInput := verificationInputForCandidate(
		candidate,
		failedInput.CheckedAt.Add(2*time.Hour),
		"https://publisher.example.test/expiring",
	)
	expiringInput.ExpiresAt = expiringInput.CheckedAt.Add(time.Hour)
	expiringVerification, err := Verify(expiringInput)
	if err != nil {
		t.Fatalf("Verify(expiring) error = %v", err)
	}
	expiringVerification, err = store.PersistVerification(
		ctx,
		expiringVerification,
	)
	if err != nil {
		t.Fatalf("PersistVerification(expiring) error = %v", err)
	}
	if _, err := store.ProjectCurrent(
		ctx,
		expiringVerification.ID,
		expiringInput.ExpiresAt,
	); !errors.Is(err, ErrVerificationExpired) {
		t.Fatalf("project expired verification error = %v", err)
	}

	assertURLVerifyImmutable(
		t,
		pool,
		"UPDATE work_url_candidates SET source_path = '$.mutated' WHERE id = $1",
		candidate.ID,
	)
	assertURLVerifyImmutable(
		t,
		pool,
		"UPDATE work_url_verifications SET final_url = source_url WHERE id = $1",
		firstVerification.ID,
	)
	assertURLVerifyImmutable(
		t,
		pool,
		"UPDATE current_work_official_links SET official_url = 'https://mutated.example' WHERE id = $1",
		firstLink.ID,
	)
}

func TestPostgresStoreImportsValidCandidatesFromMixedMalformedProjection(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	ctx := context.Background()
	goodURL := "https://publisher.example.test/mixed-good"
	fixture := insertCurrentURLProjectionFixture(
		t,
		pool,
		"mixed-candidates",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{
				{
					"url":               goodURL,
					"source_path":       "$.message.URL",
					"content_channel":   "journal_published",
					"link_role":         "official_article",
					"parser_version":    "crossref/crossref-work-v4",
					"identifier_scheme": "doi",
					"identifier_value":  "10.1000/mixed-candidates",
				},
				{
					"url":               "https://publisher.example.test/mixed-bad",
					"source_path":       "",
					"content_channel":   "journal_published",
					"link_role":         "official_article",
					"parser_version":    "crossref/crossref-work-v4",
					"identifier_scheme": "doi",
					"identifier_value":  "10.1000/mixed-candidates",
				},
			},
		),
	)

	imported, err := store.ImportCurrentProjectionCandidates(
		ctx,
		CurrentProjectionRef{
			WorkID:                fixture.WorkID,
			NormalizedAssertionID: fixture.NormalizedAssertionID,
		},
	)
	if !errors.Is(err, ErrInvalidNormalizedURLCandidate) {
		t.Fatalf("mixed normalized candidate error = %v", err)
	}
	if len(imported) != 1 || imported[0].URL != goodURL {
		t.Fatalf("mixed normalized candidate imports = %#v", imported)
	}

	var (
		candidateCount int
		persistedURL   string
	)
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(candidate_url)
		FROM work_url_candidates
		WHERE normalized_assertion_id = $1
	`, fixture.NormalizedAssertionID).Scan(
		&candidateCount,
		&persistedURL,
	); err != nil {
		t.Fatalf("read mixed normalized candidate imports: %v", err)
	}
	if candidateCount != 1 || persistedURL != goodURL {
		t.Fatalf(
			"persisted mixed candidates = %d / %q, want 1 / %q",
			candidateCount,
			persistedURL,
			goodURL,
		)
	}
}

func TestPostgresStoreCurrentFallsBackToOlderActiveProjection(t *testing.T) {
	store, candidate, fixture := openURLVerifyStoreFixture(t, "current-fallback")
	ctx := context.Background()

	olderInput := verificationInputForCandidate(
		candidate,
		fixture.SourceTime.Add(time.Hour),
		"https://publisher.example.test/older-active",
	)
	olderInput.ExpiresAt = olderInput.CheckedAt.Add(24 * time.Hour)
	olderVerification, err := Verify(olderInput)
	if err != nil {
		t.Fatalf("Verify(older) error = %v", err)
	}
	olderVerification, err = store.PersistVerification(ctx, olderVerification)
	if err != nil {
		t.Fatalf("PersistVerification(older) error = %v", err)
	}
	olderLink, err := store.ProjectCurrent(
		ctx,
		olderVerification.ID,
		olderInput.CheckedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("ProjectCurrent(older) error = %v", err)
	}

	newerInput := verificationInputForCandidate(
		candidate,
		olderInput.CheckedAt.Add(2*time.Hour),
		"https://publisher.example.test/newer-expired",
	)
	newerInput.ExpiresAt = newerInput.CheckedAt.Add(time.Hour)
	newerVerification, err := Verify(newerInput)
	if err != nil {
		t.Fatalf("Verify(newer) error = %v", err)
	}
	newerVerification, err = store.PersistVerification(ctx, newerVerification)
	if err != nil {
		t.Fatalf("PersistVerification(newer) error = %v", err)
	}
	if _, err := store.ProjectCurrent(
		ctx,
		newerVerification.ID,
		newerInput.CheckedAt.Add(time.Minute),
	); err != nil {
		t.Fatalf("ProjectCurrent(newer) error = %v", err)
	}

	at := newerInput.ExpiresAt.Add(time.Minute)
	current, found, err := store.Current(
		ctx,
		candidate.WorkID,
		candidate.LinkRole,
		at,
	)
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if !found || current.ID != olderLink.ID {
		t.Fatalf(
			"Current() = %#v, %t, want older active projection %#v",
			current,
			found,
			olderLink,
		)
	}
}

func TestPostgresStoreRejectsOlderVerificationAsNewCurrentVersion(t *testing.T) {
	store, candidate, fixture := openURLVerifyStoreFixture(t, "older-current")
	ctx := context.Background()

	newerInput := verificationInputForCandidate(
		candidate,
		fixture.SourceTime.Add(3*time.Hour),
		"https://publisher.example.test/newer-check",
	)
	newerVerification, err := Verify(newerInput)
	if err != nil {
		t.Fatalf("Verify(newer) error = %v", err)
	}
	newerVerification, err = store.PersistVerification(ctx, newerVerification)
	if err != nil {
		t.Fatalf("PersistVerification(newer) error = %v", err)
	}
	if _, err := store.ProjectCurrent(
		ctx,
		newerVerification.ID,
		newerInput.CheckedAt.Add(time.Minute),
	); err != nil {
		t.Fatalf("ProjectCurrent(newer) error = %v", err)
	}

	olderInput := verificationInputForCandidate(
		candidate,
		fixture.SourceTime.Add(2*time.Hour),
		"https://publisher.example.test/older-check",
	)
	olderVerification, err := Verify(olderInput)
	if err != nil {
		t.Fatalf("Verify(older) error = %v", err)
	}
	olderVerification, err = store.PersistVerification(ctx, olderVerification)
	if err != nil {
		t.Fatalf("PersistVerification(older) error = %v", err)
	}
	if link, err := store.ProjectCurrent(
		ctx,
		olderVerification.ID,
		newerInput.CheckedAt.Add(2*time.Minute),
	); err == nil {
		t.Fatalf("ProjectCurrent(older) = %#v, want rejection", link)
	}
}

func TestPostgresStoreRejectsForgedVerifiedAssertion(t *testing.T) {
	store, candidate, fixture := openURLVerifyStoreFixture(t, "forged-verified")
	ctx := context.Background()
	input := verificationInputForCandidate(
		candidate,
		fixture.SourceTime.Add(time.Hour),
		"https://publisher.example.test/forged",
	)
	matched := candidate.Identifier
	forged := Verification{
		CandidateID:         candidate.ID,
		WorkID:              candidate.WorkID,
		Channel:             candidate.Channel,
		LinkRole:            candidate.LinkRole,
		SourceURL:           candidate.URL,
		FinalURL:            input.Observation.FinalURL,
		RedirectChain:       input.Observation.RedirectChain,
		HTTPStatus:          input.Observation.HTTPStatus,
		ExpectedIdentifier:  candidate.Identifier,
		ObservedIdentifiers: []StableIdentifier{candidate.Identifier},
		IdentifierEvidence:  testIdentifierEvidence(candidate.Identifier),
		MatchedIdentifier:   &matched,
		IdentifierMatch:     true,
		State:               VerificationStateVerified,
		CheckedAt:           input.CheckedAt,
		ExpiresAt:           input.ExpiresAt,
		VerifierVersion:     input.VerifierVersion,
		PolicyVersion:       input.Policy.Version,
	}
	if persisted, err := store.PersistVerification(ctx, forged); err == nil {
		t.Fatalf(
			"PersistVerification(forged) = %#v, want integrity rejection",
			persisted,
		)
	}
}

func TestPostgresOfficialURLSchemaRejectsUnprojectableAndMalformedEvidence(
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
		"schema-bounds",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{
				{
					"url":               "https://publisher.example.test/schema",
					"source_path":       "$.message.URL",
					"content_channel":   "journal_published",
					"link_role":         "official_article",
					"parser_version":    "crossref/crossref-work-v4",
					"identifier_scheme": "doi",
					"identifier_value":  "10.1000/schema-bounds",
				},
				{
					"url":               "https://publisher.example.test/schema/supplement",
					"source_path":       "$.message.link[0].URL",
					"content_channel":   "journal_published",
					"link_role":         "auxiliary",
					"parser_version":    "crossref/crossref-work-v4",
					"identifier_scheme": "doi",
					"identifier_value":  "10.1000/schema-bounds",
				},
			},
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
	var official, auxiliary Candidate
	for _, candidate := range candidates {
		switch candidate.LinkRole {
		case LinkRoleOfficialArticle:
			official = candidate
		case LinkRoleAuxiliary:
			auxiliary = candidate
		}
	}

	_, err = insertDirectURLVerification(
		pool,
		auxiliary,
		"https://publisher.example.test/schema/supplement/final",
		directRedirectChain(
			auxiliary.URL,
			"https://publisher.example.test/schema/supplement/final",
		),
		VerificationStateVerified,
		FailureNone,
	)
	assertURLVerifyConstraintViolation(t, err)

	oversized := make([]RedirectHop, 12)
	for index := range oversized {
		oversized[index] = RedirectHop{
			URL:        official.URL,
			StatusCode: http.StatusFound,
			Location:   "https://publisher.example.test/schema/final",
		}
	}
	oversized[11] = RedirectHop{
		URL:        "https://publisher.example.test/schema/final",
		StatusCode: http.StatusOK,
	}
	_, err = insertDirectURLVerification(
		pool,
		official,
		"https://publisher.example.test/schema/final",
		oversized,
		VerificationStateVerified,
		FailureNone,
	)
	assertURLVerifyConstraintViolation(t, err)

	_, err = insertDirectURLVerification(
		pool,
		official,
		"https://publisher.example.test/schema/failed",
		directRedirectChain(
			official.URL,
			"https://publisher.example.test/schema/failed",
		),
		VerificationStateFailed,
		FailureCode("unknown_failure"),
	)
	assertURLVerifyConstraintViolation(t, err)

	loopbackFinal := "http://127.0.0.1:8080/schema"
	_, err = insertDirectURLVerification(
		pool,
		official,
		loopbackFinal,
		directRedirectChain(official.URL, loopbackFinal),
		VerificationStateVerified,
		FailureNone,
	)
	assertURLVerifyConstraintViolation(t, err)
}

type urlProjectionFixture struct {
	WorkID                string
	SourceRecordID        string
	NormalizedAssertionID string
	ProjectionAssertionID string
	SourceTime            time.Time
}

func openURLVerifyStoreFixture(
	t *testing.T,
	suffix string,
) (*PostgresStore, Candidate, urlProjectionFixture) {
	t.Helper()
	pool := openURLVerifyTestPool(t)
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
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/" + suffix,
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

func insertCurrentURLProjectionFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	suffix string,
	schemaVersion string,
	payload json.RawMessage,
) urlProjectionFixture {
	t.Helper()
	ctx := context.Background()
	sourceTime := time.Date(2026, time.July, 18, 6, 0, 0, 0, time.UTC)
	fixture := urlProjectionFixture{SourceTime: sourceTime}
	var jobID, rawEventID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO works (canonical_key, status, title)
		VALUES ($1, 'active', $2)
		RETURNING id::text
	`, "doi:10.1000/url-"+suffix, "URL fixture "+suffix).Scan(
		&fixture.WorkID,
	); err != nil {
		t.Fatalf("insert URL fixture Work: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_jobs (
			source, job_type, idempotency_key, status, payload,
			attempts, max_attempts, started_at, finished_at,
			batch_key, stage
		) VALUES (
			'crossref', 'sync', $1, 'succeeded', '{}',
			1, 1, $2, $2, $1, 'project'
		)
		RETURNING id::text
	`, "url-job-"+suffix, sourceTime).Scan(&jobID); err != nil {
		t.Fatalf("insert URL fixture job: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_raw_events (
			job_id, logical_source, event_key, event_kind,
			source_record_id, source_time, tie_break_key, position,
			content_hash, raw_format, raw_payload
		) VALUES (
			$1, 'crossref', $2, 'upsert',
			$2, $3, $2, 1,
			$4, 'json', convert_to('{}', 'UTF8')
		)
		RETURNING id::text
	`, jobID, "url-record-"+suffix, sourceTime, repeatedSHA(suffix)).Scan(
		&rawEventID,
	); err != nil {
		t.Fatalf("insert URL fixture raw event: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO source_records (
			source, source_record_id, source_identity,
			source_time, content_hash, raw_payload
		) VALUES (
			'crossref', $1, jsonb_build_object('id', $1::text),
			$2, $3, jsonb_build_object('id', $1::text)
		)
		RETURNING id::text
	`, "url-record-"+suffix, sourceTime, "source-"+suffix).Scan(
		&fixture.SourceRecordID,
	); err != nil {
		t.Fatalf("insert URL fixture source record: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, fixture.SourceRecordID, fixture.WorkID); err != nil {
		t.Fatalf("link URL fixture source record: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id, source_record_uuid,
			normalization_policy_version, payload_schema_version,
			normalized_payload
		) VALUES (
			$1, $2, 'normalization/url-v1', $3, $4
		)
		RETURNING id::text
	`, rawEventID, fixture.SourceRecordID, schemaVersion, payload).Scan(
		&fixture.NormalizedAssertionID,
	); err != nil {
		t.Fatalf("insert URL fixture normalized assertion: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_projection_assertions (
			raw_event_id, source_record_uuid, work_id, job_id,
			scope_policy_version, projection_policy_version,
			record_payload, normalized_assertion_id
		) VALUES (
			$1, $2, $3, $4,
			'scope/url-v1', 'projection/url-v1',
			'{"url_fixture":true}', $5
		)
		RETURNING id::text
	`,
		rawEventID,
		fixture.SourceRecordID,
		fixture.WorkID,
		jobID,
		fixture.NormalizedAssertionID,
	).Scan(&fixture.ProjectionAssertionID); err != nil {
		t.Fatalf("insert URL fixture projection assertion: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_projection_states (
			work_id, normalized_assertion_id, raw_event_id,
			source_record_uuid, source_time, tie_break_key, position,
			scope_policy_version, projection_policy_version
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, 1,
			'scope/url-v1', 'projection/url-v1'
		)
	`,
		fixture.WorkID,
		fixture.NormalizedAssertionID,
		rawEventID,
		fixture.SourceRecordID,
		sourceTime,
		"url-tie-"+suffix,
	); err != nil {
		t.Fatalf("insert URL fixture current projection: %v", err)
	}
	return fixture
}

func insertURLProjectionRevisionForWork(
	t *testing.T,
	pool *pgxpool.Pool,
	previous urlProjectionFixture,
	suffix string,
	payload json.RawMessage,
) urlProjectionFixture {
	t.Helper()
	ctx := context.Background()
	sourceTime := previous.SourceTime.Add(time.Hour)
	fixture := previous
	var jobID, rawEventID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_jobs (
			source, job_type, idempotency_key, status, payload,
			attempts, max_attempts, started_at, finished_at,
			batch_key, stage
		) VALUES (
			'crossref', 'sync', $1, 'succeeded', '{}',
			1, 1, $2, $2, $1, 'project'
		)
		RETURNING id::text
	`, "url-job-"+suffix, sourceTime).Scan(&jobID); err != nil {
		t.Fatalf("insert URL revision job: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_raw_events (
			job_id, logical_source, event_key, event_kind,
			source_record_id, source_time, tie_break_key, position,
			content_hash, raw_format, raw_payload
		) VALUES (
			$1, 'crossref', $2, 'upsert',
			$2, $3, $2, 1,
			$4, 'json', convert_to('{}', 'UTF8')
		)
		RETURNING id::text
	`, jobID, "url-record-"+suffix, sourceTime, repeatedSHA(suffix)).Scan(
		&rawEventID,
	); err != nil {
		t.Fatalf("insert URL revision raw event: %v", err)
	}
	var sourceRecordID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO source_records (
			source, source_record_id, source_identity,
			source_time, content_hash, raw_payload
		) VALUES (
			'crossref', $1, jsonb_build_object('id', $1::text),
			$2, $3, jsonb_build_object('id', $1::text)
		)
		RETURNING id::text
	`, "url-record-"+suffix, sourceTime, "source-"+suffix).Scan(
		&sourceRecordID,
	); err != nil {
		t.Fatalf("insert URL revision source record: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, sourceRecordID, previous.WorkID); err != nil {
		t.Fatalf("link URL revision source record: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id, source_record_uuid,
			normalization_policy_version, payload_schema_version,
			normalized_payload
		) VALUES (
			$1, $2, 'normalization/url-v1', 'normalized-record/v4', $3
		)
		RETURNING id::text
	`, rawEventID, sourceRecordID, payload).Scan(
		&fixture.NormalizedAssertionID,
	); err != nil {
		t.Fatalf("insert URL revision normalized assertion: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO ingestion_projection_assertions (
			raw_event_id, source_record_uuid, work_id, job_id,
			scope_policy_version, projection_policy_version,
			record_payload, normalized_assertion_id
		) VALUES (
			$1, $2, $3, $4,
			'scope/url-v1', 'projection/url-v1',
			'{"url_fixture":true}', $5
		)
		RETURNING id::text
	`,
		rawEventID,
		sourceRecordID,
		previous.WorkID,
		jobID,
		fixture.NormalizedAssertionID,
	).Scan(&fixture.ProjectionAssertionID); err != nil {
		t.Fatalf("insert URL revision projection assertion: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE work_projection_states
		SET normalized_assertion_id = $2,
			raw_event_id = $3,
			source_record_uuid = $4,
			source_time = $5,
			tie_break_key = $6
		WHERE work_id = $1
	`, previous.WorkID, fixture.NormalizedAssertionID, rawEventID,
		sourceRecordID, sourceTime, "url-tie-"+suffix); err != nil {
		t.Fatalf("update URL current projection state: %v", err)
	}
	fixture.SourceRecordID = sourceRecordID
	fixture.SourceTime = sourceTime
	return fixture
}

func normalizedURLPayload(
	parserVersion string,
	candidates []map[string]any,
) json.RawMessage {
	payload, err := json.Marshal(map[string]any{
		"source":           "crossref",
		"source_record_id": "url-record",
		"parser_version":   parserVersion,
		"title":            "URL candidate fixture",
		"url_candidates":   candidates,
	})
	if err != nil {
		panic(err)
	}
	return payload
}

func verificationInputForCandidate(
	candidate Candidate,
	checkedAt time.Time,
	finalURL string,
) VerificationInput {
	return VerificationInput{
		Candidate:          candidate,
		ExpectedIdentifier: candidate.Identifier,
		Observation: HTTPObservation{
			FinalURL:   finalURL,
			HTTPStatus: 200,
			RedirectChain: []RedirectHop{
				{
					URL:        candidate.URL,
					StatusCode: 302,
					Location:   finalURL,
				},
				{
					URL:        finalURL,
					StatusCode: 200,
				},
			},
			IdentifierEvidence: testIdentifierEvidence(
				candidate.Identifier,
			),
		},
		CheckedAt:       checkedAt,
		ExpiresAt:       checkedAt.Add(24 * time.Hour),
		EvaluatedAt:     checkedAt,
		VerifierVersion: "official-url-verifier/v1",
		Policy: Policy{
			Version:      "official-url/v1",
			MaxRedirects: 3,
		},
	}
}

func repeatedSHA(value string) string {
	alphabet := "0123456789abcdef"
	character := alphabet[len(value)%len(alphabet)]
	return strings.Repeat(string(character), 64)
}

func directRedirectChain(sourceURL string, finalURL string) []RedirectHop {
	return []RedirectHop{
		{
			URL:        sourceURL,
			StatusCode: http.StatusFound,
			Location:   finalURL,
		},
		{
			URL:        finalURL,
			StatusCode: http.StatusOK,
		},
	}
}

func insertDirectURLVerification(
	pool *pgxpool.Pool,
	candidate Candidate,
	finalURL string,
	redirectChain []RedirectHop,
	state VerificationState,
	failureCode FailureCode,
) (string, error) {
	redirectJSON, _ := json.Marshal(redirectChain)
	observedJSON, _ := json.Marshal([]StableIdentifier{
		candidate.Identifier,
	})
	evidenceJSON, _ := json.Marshal(
		testIdentifierEvidence(candidate.Identifier),
	)
	metadataJSON, _ := json.Marshal(ResponseMetadata{})
	matchedScheme := any(candidate.Identifier.Scheme)
	matchedValue := any(candidate.Identifier.Value)
	identifierMatch := true
	failure := any(nil)
	if state == VerificationStateFailed {
		matchedScheme = nil
		matchedValue = nil
		identifierMatch = false
		failure = string(failureCode)
	}
	var verificationID string
	err := pool.QueryRow(context.Background(), `
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
			$10, $11, $12, $13, $14, $15, $16, $17, $18
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
		matchedScheme,
		matchedValue,
		identifierMatch,
		state,
		candidate.AssertedAt.Add(time.Hour),
		candidate.AssertedAt.Add(25*time.Hour),
		"direct-schema-test/v1",
		"official-url/v1",
		failure,
	).Scan(&verificationID)
	if err != nil {
		return "", err
	}
	return verificationID, nil
}

func assertURLVerifyConstraintViolation(t *testing.T, err error) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "23514" {
		t.Fatalf("constraint violation error = %v", err)
	}
}

func assertURLVerifyImmutable(
	t *testing.T,
	pool *pgxpool.Pool,
	statement string,
	id string,
) {
	t.Helper()
	_, err := pool.Exec(context.Background(), statement, id)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) ||
		postgresError.Code != "55000" {
		t.Fatalf("immutable mutation error = %v", err)
	}
}

func openURLVerifyTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	container, err := postgres.Run(
		ctx,
		"postgres:18-alpine",
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("url_verify_test"),
		postgres.WithPassword("url-verify-test-password"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start PostgreSQL Testcontainer: %v", err)
	}
	t.Cleanup(func() {
		if cleanupErr := testcontainers.TerminateContainer(container); cleanupErr != nil {
			t.Errorf("terminate PostgreSQL Testcontainer: %v", cleanupErr)
		}
	})
	databaseURL, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("resolve PostgreSQL test URL: %v", err)
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse PostgreSQL test URL: %v", err)
	}
	parsed.Path = "/postgres"
	pool, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatalf("open PostgreSQL test pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := database.Up(ctx, pool); err != nil {
		t.Fatalf("migrate PostgreSQL test database: %v", err)
	}
	importOfficialURLHostRegistry(
		t,
		pool,
		"official-url/v1",
		[]officialURLHostRegistryFixture{
			{
				channel:  scope.ContentChannelJournalPublished,
				role:     LinkRoleOfficialArticle,
				hostname: "resolver.example.test",
			},
			{
				channel:  scope.ContentChannelJournalPublished,
				role:     LinkRoleOfficialArticle,
				hostname: "publisher.example.test",
			},
			{
				channel:  scope.ContentChannelJournalPublished,
				role:     LinkRoleDOIResolver,
				hostname: "doi.org",
			},
			{
				channel:  scope.ContentChannelJournalPublished,
				role:     LinkRoleDOIResolver,
				hostname: "publisher.example.test",
			},
		},
	)
	return pool
}
