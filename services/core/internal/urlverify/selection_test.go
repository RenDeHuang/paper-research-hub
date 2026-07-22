package urlverify

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPostgresStoreSelectsOnlyCurrentUnprojectedV4Candidates(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	ctx := context.Background()
	fixture := insertCurrentURLProjectionFixture(
		t,
		pool,
		"selection",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{
				{
					"url":               "https://publisher.example.test/selection",
					"source_path":       "$.message.URL",
					"content_channel":   "journal_published",
					"link_role":         "official_article",
					"parser_version":    "crossref/crossref-work-v4",
					"identifier_scheme": "doi",
					"identifier_value":  "10.1000/selection",
				},
				{
					"url":               "https://publisher.example.test/selection/supplement",
					"source_path":       "$.message.link[0].URL",
					"content_channel":   "journal_published",
					"link_role":         "auxiliary",
					"parser_version":    "crossref/crossref-work-v4",
					"identifier_scheme": "doi",
					"identifier_value":  "10.1000/selection",
				},
			},
		),
	)
	insertCurrentURLProjectionFixture(
		t,
		pool,
		"selection-empty",
		"normalized-record/v4",
		normalizedURLPayload("crossref/crossref-work-v4", []map[string]any{}),
	)
	insertCurrentURLProjectionFixture(
		t,
		pool,
		"selection-legacy",
		"normalized-record/v3",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               "https://publisher.example.test/legacy",
				"source_path":       "$.message.URL",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/legacy",
			}},
		),
	)

	references, err := store.ListCurrentProjectionRefs(ctx, 100)
	if err != nil {
		t.Fatalf("ListCurrentProjectionRefs() error = %v", err)
	}
	if len(references) != 1 ||
		references[0] != (CurrentProjectionRef{
			WorkID:                fixture.WorkID,
			NormalizedAssertionID: fixture.NormalizedAssertionID,
		}) {
		t.Fatalf("current projection refs = %#v", references)
	}
	imported, err := store.ImportCurrentProjectionCandidates(
		ctx,
		references[0],
	)
	if err != nil {
		t.Fatalf("ImportCurrentProjectionCandidates() error = %v", err)
	}
	if len(imported) != 2 {
		t.Fatalf("imported candidates = %#v", imported)
	}

	at := fixture.SourceTime.Add(2 * time.Hour)
	candidates, err := store.ListVerificationCandidates(
		ctx,
		CandidateSelection{
			PolicyVersion: "official-url/v1",
			At:            at,
			Limit:         100,
		},
	)
	if err != nil {
		t.Fatalf("ListVerificationCandidates() error = %v", err)
	}
	if len(candidates) != 1 ||
		candidates[0].LinkRole != LinkRoleOfficialArticle ||
		candidates[0].URL != "https://publisher.example.test/selection" {
		t.Fatalf("verification candidates = %#v", candidates)
	}

	input := verificationInputForCandidate(
		candidates[0],
		at,
		"https://publisher.example.test/selection/final",
	)
	verification, err := Verify(input)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	verification, err = store.PersistVerification(ctx, verification)
	if err != nil {
		t.Fatalf("PersistVerification() error = %v", err)
	}
	if _, err := store.ProjectCurrent(
		ctx,
		verification.ID,
		at.Add(time.Minute),
	); err != nil {
		t.Fatalf("ProjectCurrent() error = %v", err)
	}

	candidates, err = store.ListVerificationCandidates(
		ctx,
		CandidateSelection{
			PolicyVersion: "official-url/v1",
			At:            at.Add(2 * time.Minute),
			Limit:         100,
		},
	)
	if err != nil {
		t.Fatalf("ListVerificationCandidates(projected) error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("already projected candidates = %#v", candidates)
	}
	candidates, err = store.ListVerificationCandidates(
		ctx,
		CandidateSelection{
			PolicyVersion: "official-url/v2",
			At:            at.Add(2 * time.Minute),
			Limit:         100,
		},
	)
	if err != nil {
		t.Fatalf("ListVerificationCandidates(other policy) error = %v", err)
	}
	if len(candidates) != 1 || candidates[0].ID != imported[0].ID {
		t.Fatalf("other-policy candidates = %#v", candidates)
	}

	if _, err := pool.Exec(ctx, `
		DELETE FROM work_projection_states WHERE work_id = $1
	`, fixture.WorkID); err != nil {
		t.Fatalf("remove current projection state: %v", err)
	}
	candidates, err = store.ListVerificationCandidates(
		ctx,
		CandidateSelection{
			PolicyVersion: "official-url/v2",
			At:            at.Add(2 * time.Minute),
			Limit:         100,
		},
	)
	if err != nil {
		t.Fatalf("ListVerificationCandidates(stale) error = %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("stale projection candidates = %#v", candidates)
	}
}

func TestPostgresStoreListCurrentProjectionRefsAdvancesPastCompleteImport(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	ctx := context.Background()
	first := insertCurrentURLProjectionFixture(
		t,
		pool,
		"refs-first",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               "https://publisher.example.test/refs-first",
				"source_path":       "$.message.URL",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/refs-first",
			}},
		),
	)
	second := insertCurrentURLProjectionFixture(
		t,
		pool,
		"refs-second",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               "https://publisher.example.test/refs-second",
				"source_path":       "$.message.URL",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/refs-second",
			}},
		),
	)

	firstPage, err := store.ListCurrentProjectionRefs(ctx, 1)
	if err != nil {
		t.Fatalf("ListCurrentProjectionRefs(first) error = %v", err)
	}
	if len(firstPage) != 1 {
		t.Fatalf("first projection refs = %#v", firstPage)
	}
	if _, err := store.ImportCurrentProjectionCandidates(
		ctx,
		firstPage[0],
	); err != nil {
		t.Fatalf("ImportCurrentProjectionCandidates(first) error = %v", err)
	}

	secondPage, err := store.ListCurrentProjectionRefs(ctx, 1)
	if err != nil {
		t.Fatalf("ListCurrentProjectionRefs(second) error = %v", err)
	}
	if len(secondPage) != 1 || secondPage[0] == firstPage[0] {
		t.Fatalf(
			"second projection refs = %#v, first = %#v",
			secondPage,
			firstPage,
		)
	}
	wantSecond := CurrentProjectionRef{
		WorkID:                second.WorkID,
		NormalizedAssertionID: second.NormalizedAssertionID,
	}
	wantFirst := CurrentProjectionRef{
		WorkID:                first.WorkID,
		NormalizedAssertionID: first.NormalizedAssertionID,
	}
	if secondPage[0] != wantFirst && secondPage[0] != wantSecond {
		t.Fatalf(
			"second projection ref = %#v, want one of %#v / %#v",
			secondPage[0],
			wantFirst,
			wantSecond,
		)
	}
	if firstPage[0] != wantFirst && firstPage[0] != wantSecond {
		t.Fatalf("first projection ref = %#v", firstPage[0])
	}
	if secondPage[0] == firstPage[0] {
		t.Fatal("complete first projection was returned again")
	}
}

func TestPostgresStoreListCurrentProjectionRefsRequeuesUpdatedRevision(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	ctx := context.Background()
	initial := insertCurrentURLProjectionFixture(
		t,
		pool,
		"refs-revision-initial",
		"normalized-record/v4",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               "https://publisher.example.test/revision-initial",
				"source_path":       "$.message.URL",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/revision-initial",
			}},
		),
	)
	initialRef := CurrentProjectionRef{
		WorkID:                initial.WorkID,
		NormalizedAssertionID: initial.NormalizedAssertionID,
	}
	if _, err := store.ImportCurrentProjectionCandidates(ctx, initialRef); err != nil {
		t.Fatalf("ImportCurrentProjectionCandidates(initial) error = %v", err)
	}
	updated := insertURLProjectionRevisionForWork(
		t,
		pool,
		initial,
		"refs-revision-updated",
		normalizedURLPayload(
			"crossref/crossref-work-v4",
			[]map[string]any{{
				"url":               "https://publisher.example.test/revision-updated",
				"source_path":       "$.message.URL",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/revision-updated",
			}},
		),
	)

	refs, err := store.ListCurrentProjectionRefs(ctx, 1)
	if err != nil {
		t.Fatalf("ListCurrentProjectionRefs(updated) error = %v", err)
	}
	want := CurrentProjectionRef{
		WorkID:                initial.WorkID,
		NormalizedAssertionID: updated.NormalizedAssertionID,
	}
	if len(refs) != 1 || refs[0] != want {
		t.Fatalf("updated projection refs = %#v, want %#v", refs, want)
	}
}

func TestPostgresStoreRejectedCandidateReceiptAdvancesLimitAndRequeuesRevision(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	ctx := context.Background()
	mixedPayload := normalizedURLPayload(
		"crossref/crossref-work-v4",
		[]map[string]any{
			{
				"url":               "https://publisher.example.test/receipt-good",
				"source_path":       "$.message.URL",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/receipt-mixed",
			},
			{
				"url": "https://publisher.example.test/" +
					"receipt-bad?view=%zz",
				"source_path":       "$.message.URLMalformed",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/receipt-mixed",
			},
			{
				"url": "https://publisher.example.test/" +
					"receipt-incomplete?view=%",
				"source_path":       "$.message.URLIncomplete",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "crossref/crossref-work-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/receipt-mixed",
			},
		},
	)
	mixed := insertCurrentURLProjectionFixture(
		t,
		pool,
		"receipt-mixed",
		"normalized-record/v4",
		mixedPayload,
	)
	laterPayload := normalizedURLPayload(
		"crossref/crossref-work-v4",
		[]map[string]any{{
			"url":               "https://publisher.example.test/receipt-later",
			"source_path":       "$.message.URL",
			"content_channel":   "journal_published",
			"link_role":         "official_article",
			"parser_version":    "crossref/crossref-work-v4",
			"identifier_scheme": "doi",
			"identifier_value":  "10.1000/receipt-later",
		}},
	)
	laterInitial := insertCurrentURLProjectionFixture(
		t,
		pool,
		"receipt-later-initial",
		"normalized-record/v4",
		laterPayload,
	)
	later := insertURLProjectionRevisionForWork(
		t,
		pool,
		laterInitial,
		"receipt-later-current",
		laterPayload,
	)

	imported, err := store.ImportCurrentProjectionCandidates(
		ctx,
		CurrentProjectionRef{
			WorkID:                mixed.WorkID,
			NormalizedAssertionID: mixed.NormalizedAssertionID,
		},
	)
	if !errors.Is(err, ErrInvalidNormalizedURLCandidate) ||
		len(imported) != 1 {
		t.Fatalf("mixed import = %#v, %v", imported, err)
	}

	refs, err := store.ListCurrentProjectionRefs(ctx, 1)
	if err != nil {
		t.Fatalf("ListCurrentProjectionRefs(after rejection) error = %v", err)
	}
	wantLater := CurrentProjectionRef{
		WorkID:                later.WorkID,
		NormalizedAssertionID: later.NormalizedAssertionID,
	}
	if len(refs) != 1 || refs[0] != wantLater {
		t.Fatalf(
			"refs after rejection = %#v, want later %#v",
			refs,
			wantLater,
		)
	}

	rows, err := pool.Query(ctx, `
		SELECT
			rejection.id::text,
			rejection.work_id::text,
			rejection.projection_assertion_id::text,
			rejection.normalized_assertion_id::text,
			rejection.source_record_id::text,
			rejection.candidate_ordinal,
			rejection.candidate_payload_hash,
			rejection.error_code,
			jsonb_array_element(
				normalized.normalized_payload->'url_candidates',
				rejection.candidate_ordinal
			)::text
		FROM work_url_candidate_import_rejections AS rejection
		JOIN ingestion_normalized_records AS normalized
		  ON normalized.id = rejection.normalized_assertion_id
		WHERE rejection.normalized_assertion_id = $1
		ORDER BY rejection.candidate_ordinal
	`, mixed.NormalizedAssertionID)
	if err != nil {
		t.Fatalf("query candidate rejection receipts: %v", err)
	}
	defer rows.Close()
	receiptIDs := make([]string, 0, 2)
	for rows.Next() {
		var (
			receiptID             string
			receiptWorkID         string
			receiptProjectionID   string
			receiptNormalizedID   string
			receiptSourceRecordID string
			receiptOrdinal        int
			payloadHash           string
			errorCode             string
			candidatePayload      string
		)
		if err := rows.Scan(
			&receiptID,
			&receiptWorkID,
			&receiptProjectionID,
			&receiptNormalizedID,
			&receiptSourceRecordID,
			&receiptOrdinal,
			&payloadHash,
			&errorCode,
			&candidatePayload,
		); err != nil {
			t.Fatalf("scan candidate rejection receipt: %v", err)
		}
		wantHash := sha256.Sum256([]byte(candidatePayload))
		if receiptWorkID != mixed.WorkID ||
			receiptProjectionID != mixed.ProjectionAssertionID ||
			receiptNormalizedID != mixed.NormalizedAssertionID ||
			receiptSourceRecordID != mixed.SourceRecordID ||
			(receiptOrdinal != 1 && receiptOrdinal != 2) ||
			payloadHash != fmt.Sprintf("%x", wantHash) ||
			errorCode != "invalid_candidate" {
			t.Fatalf(
				"candidate rejection receipt = work %s projection %s normalized %s source %s ordinal %d hash %q code %q payload %q",
				receiptWorkID,
				receiptProjectionID,
				receiptNormalizedID,
				receiptSourceRecordID,
				receiptOrdinal,
				payloadHash,
				errorCode,
				candidatePayload,
			)
		}
		receiptIDs = append(receiptIDs, receiptID)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate candidate rejection receipts: %v", err)
	}
	if len(receiptIDs) != 2 {
		t.Fatalf("candidate rejection receipt IDs = %#v, want two", receiptIDs)
	}
	for _, receiptID := range receiptIDs {
		assertURLVerifyImmutable(
			t,
			pool,
			"UPDATE work_url_candidate_import_rejections SET error_code = 'invalid_json_shape' WHERE id = $1",
			receiptID,
		)
		assertURLVerifyImmutable(
			t,
			pool,
			"DELETE FROM work_url_candidate_import_rejections WHERE id = $1",
			receiptID,
		)
	}

	if _, err := store.ImportCurrentProjectionCandidates(
		ctx,
		wantLater,
	); err != nil {
		t.Fatalf("ImportCurrentProjectionCandidates(later) error = %v", err)
	}
	updated := insertURLProjectionRevisionForWork(
		t,
		pool,
		mixed,
		"receipt-mixed-updated",
		mixedPayload,
	)
	refs, err = store.ListCurrentProjectionRefs(ctx, 1)
	if err != nil {
		t.Fatalf("ListCurrentProjectionRefs(updated) error = %v", err)
	}
	wantUpdated := CurrentProjectionRef{
		WorkID:                updated.WorkID,
		NormalizedAssertionID: updated.NormalizedAssertionID,
	}
	if len(refs) != 1 || refs[0] != wantUpdated {
		t.Fatalf(
			"updated rejection refs = %#v, want %#v",
			refs,
			wantUpdated,
		)
	}
}

func TestPostgresStoreFailedVerificationCooldownAdvancesFixedLimit(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	ctx := context.Background()
	fixtures := []urlProjectionFixture{
		insertCurrentURLProjectionFixture(
			t,
			pool,
			"cooldown-first",
			"normalized-record/v4",
			normalizedURLPayload(
				"crossref/crossref-work-v4",
				[]map[string]any{{
					"url":               "https://publisher.example.test/cooldown-first",
					"source_path":       "$.message.URL",
					"content_channel":   "journal_published",
					"link_role":         "official_article",
					"parser_version":    "crossref/crossref-work-v4",
					"identifier_scheme": "doi",
					"identifier_value":  "10.1000/cooldown-first",
				}},
			),
		),
		insertCurrentURLProjectionFixture(
			t,
			pool,
			"cooldown-second",
			"normalized-record/v4",
			normalizedURLPayload(
				"crossref/crossref-work-v4",
				[]map[string]any{{
					"url":               "https://publisher.example.test/cooldown-second",
					"source_path":       "$.message.URL",
					"content_channel":   "journal_published",
					"link_role":         "official_article",
					"parser_version":    "crossref/crossref-work-v4",
					"identifier_scheme": "doi",
					"identifier_value":  "10.1000/cooldown-second",
				}},
			),
		),
	}
	for _, fixture := range fixtures {
		if _, err := store.ImportCurrentProjectionCandidates(
			ctx,
			CurrentProjectionRef{
				WorkID:                fixture.WorkID,
				NormalizedAssertionID: fixture.NormalizedAssertionID,
			},
		); err != nil {
			t.Fatalf("ImportCurrentProjectionCandidates() error = %v", err)
		}
	}

	at := fixtures[0].SourceTime.Add(2 * time.Hour)
	selection := CandidateSelection{
		PolicyVersion: "official-url/v1",
		At:            at,
		Limit:         1,
	}
	firstPage, err := store.ListVerificationCandidates(ctx, selection)
	if err != nil {
		t.Fatalf("ListVerificationCandidates(first) error = %v", err)
	}
	if len(firstPage) != 1 {
		t.Fatalf("first verification candidates = %#v", firstPage)
	}
	first := firstPage[0]
	failedInput := verificationInputForCandidate(
		first,
		at,
		first.URL+"/final",
	)
	different := StableIdentifier{
		Scheme: "doi",
		Value:  "10.1000/cooldown-different",
	}
	failedInput.Observation.IdentifierEvidence = testIdentifierEvidence(
		different,
	)
	failed, err := Verify(failedInput)
	if err != nil {
		t.Fatalf("Verify(failed) error = %v", err)
	}
	if failed.State != VerificationStateFailed ||
		failed.FailureCode != FailureIdentifierMismatch {
		t.Fatalf("failed verification = %#v", failed)
	}
	failed, err = store.PersistVerification(ctx, failed)
	if err != nil {
		t.Fatalf("PersistVerification(failed) error = %v", err)
	}

	selection.At = at.Add(time.Minute)
	secondPage, err := store.ListVerificationCandidates(ctx, selection)
	if err != nil {
		t.Fatalf("ListVerificationCandidates(cooldown) error = %v", err)
	}
	if len(secondPage) != 1 || secondPage[0].ID == first.ID {
		t.Fatalf(
			"cooldown verification candidates = %#v, first = %#v",
			secondPage,
			first,
		)
	}

	selection.At = failed.ExpiresAt
	selection.Limit = 100
	afterCooldown, err := store.ListVerificationCandidates(ctx, selection)
	if err != nil {
		t.Fatalf("ListVerificationCandidates(expired cooldown) error = %v", err)
	}
	foundFirst := false
	for _, candidate := range afterCooldown {
		if candidate.ID == first.ID {
			foundFirst = true
		}
	}
	if !foundFirst {
		t.Fatalf(
			"candidate %s was not reselected after cooldown: %#v",
			first.ID,
			afterCooldown,
		)
	}
}

func TestPostgresStorePersistsObservedRedirectFailuresForCooldown(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	ctx := context.Background()

	loopServer := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path == "/a" {
			http.Redirect(writer, request, "/b", http.StatusFound)
			return
		}
		http.Redirect(writer, request, "/a", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(loopServer.Close)
	limitServer := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(writer, request, "/next", http.StatusFound)
	}))
	t.Cleanup(limitServer.Close)
	invalidLocationServer := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set(
			"Location",
			"https://user:secret@example.test/final",
		)
		writer.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(invalidLocationServer.Close)

	tests := []struct {
		name         string
		suffix       string
		server       *httptest.Server
		path         string
		maxRedirects int
		wantFailure  FailureCode
	}{
		{
			name:         "redirect loop",
			suffix:       "redirect-loop",
			server:       loopServer,
			path:         "/a",
			maxRedirects: MaximumRedirects,
			wantFailure:  FailureRedirectLoop,
		},
		{
			name:         "redirect limit",
			suffix:       "redirect-limit",
			server:       limitServer,
			path:         "/start",
			maxRedirects: 1,
			wantFailure:  FailureRedirectLimitExceeded,
		},
		{
			name:         "invalid redirect location",
			suffix:       "invalid-redirect-location",
			server:       invalidLocationServer,
			path:         "/start",
			maxRedirects: MaximumRedirects,
			wantFailure:  FailureInvalidRedirectChain,
		},
	}

	checkedAt := time.Date(2026, time.July, 19, 5, 0, 0, 0, time.UTC)
	persistedIDs := make([]string, 0, len(tests))
	for index, test := range tests {
		candidateURL := logicalTestServerURL(
			t,
			test.server,
			test.suffix+".example.test",
			test.path,
		)
		fixture := insertCurrentURLProjectionFixture(
			t,
			pool,
			"observed-redirect-failure-"+test.suffix,
			"normalized-record/v4",
			normalizedURLPayload(
				"crossref/crossref-work-v4",
				[]map[string]any{{
					"url":               candidateURL,
					"source_path":       "$.message.URL",
					"content_channel":   "journal_published",
					"link_role":         "official_article",
					"parser_version":    "crossref/crossref-work-v4",
					"identifier_scheme": "doi",
					"identifier_value": "10.1000/observed-redirect-" +
						string(rune('a'+index)),
				}},
			),
		)
		imported, err := store.ImportCurrentProjectionCandidates(
			ctx,
			CurrentProjectionRef{
				WorkID:                fixture.WorkID,
				NormalizedAssertionID: fixture.NormalizedAssertionID,
			},
		)
		if err != nil || len(imported) != 1 {
			t.Fatalf(
				"ImportCurrentProjectionCandidates(%s) = %#v, %v",
				test.name,
				imported,
				err,
			)
		}
		candidate := imported[0]
		observation := observeHTTP(
			t,
			test.server,
			candidate,
			test.maxRedirects,
		)
		verification, err := Verify(VerificationInput{
			Candidate:          candidate,
			ExpectedIdentifier: candidate.Identifier,
			Observation:        observation,
			CheckedAt:          checkedAt,
			ExpiresAt:          checkedAt.Add(24 * time.Hour),
			EvaluatedAt:        checkedAt,
			VerifierVersion:    "official-url-verifier/v1",
			Policy: Policy{
				Version:      "official-url/v1",
				MaxRedirects: test.maxRedirects,
			},
		})
		if err != nil {
			t.Fatalf("Verify(%s) error = %v", test.name, err)
		}
		if verification.State != VerificationStateFailed ||
			verification.FailureCode != test.wantFailure {
			t.Fatalf(
				"verification(%s) = %#v, want failure %q",
				test.name,
				verification,
				test.wantFailure,
			)
		}
		persisted, err := store.PersistVerification(ctx, verification)
		if err != nil {
			t.Fatalf("PersistVerification(%s) error = %v", test.name, err)
		}
		persistedIDs = append(persistedIDs, persisted.CandidateID)
	}

	selected, err := store.ListVerificationCandidates(
		ctx,
		CandidateSelection{
			PolicyVersion: "official-url/v1",
			At:            checkedAt.Add(time.Minute),
			Limit:         100,
		},
	)
	if err != nil {
		t.Fatalf("ListVerificationCandidates(cooldown) error = %v", err)
	}
	for _, candidate := range selected {
		for _, persistedID := range persistedIDs {
			if candidate.ID == persistedID {
				t.Fatalf(
					"failed candidate %s bypassed cooldown: %#v",
					persistedID,
					selected,
				)
			}
		}
	}
}

func TestPostgresStorePersistsObserverFailureReceiptForCooldown(
	t *testing.T,
) {
	pool := openURLVerifyTestPool(t)
	store, err := NewPostgresStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	ctx := context.Background()
	fixture := insertCurrentURLProjectionFixture(
		t,
		pool,
		"observer-network-failure",
		"normalized-record/v4",
		normalizedURLPayload(
			"official/normalized-v4",
			[]map[string]any{{
				"url":               "https://network.example.test/article",
				"source_path":       "$.official_url",
				"content_channel":   "journal_published",
				"link_role":         "official_article",
				"parser_version":    "official/normalized-v4",
				"identifier_scheme": "doi",
				"identifier_value":  "10.1000/network-failure",
			}},
		),
	)
	imported, err := store.ImportCurrentProjectionCandidates(
		ctx,
		CurrentProjectionRef{
			WorkID:                fixture.WorkID,
			NormalizedAssertionID: fixture.NormalizedAssertionID,
		},
	)
	if err != nil || len(imported) != 1 {
		t.Fatalf("import network candidate = %#v, %v", imported, err)
	}
	candidate := imported[0]
	checkedAt := fixture.SourceTime.Add(time.Hour)
	verification, err := Verify(VerificationInput{
		Candidate:          candidate,
		ExpectedIdentifier: candidate.Identifier,
		Observation: HTTPObservation{
			FinalURL:    candidate.URL,
			FailureCode: FailureNetwork,
		},
		CheckedAt:       checkedAt,
		ExpiresAt:       checkedAt.Add(24 * time.Hour),
		EvaluatedAt:     checkedAt,
		VerifierVersion: "official-url-verifier/v1",
		Policy: Policy{
			Version:      "official-url/v1",
			MaxRedirects: MaximumRedirects,
		},
	})
	if err != nil {
		t.Fatalf("Verify(network failure) error = %v", err)
	}
	persisted, err := store.PersistVerification(ctx, verification)
	if err != nil {
		t.Fatalf("PersistVerification(network failure) error = %v", err)
	}
	if persisted.State != VerificationStateFailed ||
		persisted.FailureCode != FailureNetwork ||
		persisted.HTTPStatus != 0 ||
		len(persisted.RedirectChain) != 0 {
		t.Fatalf("persisted network failure = %#v", persisted)
	}

	selected, err := store.ListVerificationCandidates(
		ctx,
		CandidateSelection{
			PolicyVersion: "official-url/v1",
			At:            checkedAt.Add(time.Minute),
			Limit:         100,
		},
	)
	if err != nil {
		t.Fatalf("ListVerificationCandidates(cooldown) error = %v", err)
	}
	for _, selectedCandidate := range selected {
		if selectedCandidate.ID == candidate.ID {
			t.Fatalf(
				"network failure candidate bypassed cooldown: %#v",
				selected,
			)
		}
	}
}

func TestCandidateSelectionRejectsInvalidLimitsAndPolicy(t *testing.T) {
	t.Parallel()

	for _, selection := range []CandidateSelection{
		{PolicyVersion: "", At: time.Now(), Limit: 1},
		{PolicyVersion: " official-url/v1", At: time.Now(), Limit: 1},
		{PolicyVersion: "official-url/v1", Limit: 1},
		{PolicyVersion: "official-url/v1", At: time.Now(), Limit: 0},
		{PolicyVersion: "official-url/v1", At: time.Now(), Limit: 1001},
	} {
		if err := selection.Validate(); err == nil {
			t.Fatalf("CandidateSelection.Validate(%#v) error = nil", selection)
		}
	}
	if err := (CandidateSelection{
		PolicyVersion: "official-url/v1",
		At:            time.Now(),
		Limit:         1,
	}).Validate(); err != nil {
		t.Fatalf("valid CandidateSelection error = %v", err)
	}
	if err := validateSelectionLimit(0); !errors.Is(
		err,
		ErrSelectionLimit,
	) {
		t.Fatalf("validateSelectionLimit(0) error = %v", err)
	}
}
