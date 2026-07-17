package venue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestPostgresAssessmentStorePersistsImmutableReceiptBackedDecisions(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	insertSyntheticFixtureVenues(t, pool)
	ctx := venueTestContext(t)
	const (
		missingVenueID  = "00000000-0000-0000-0000-000000000104"
		preprintVenueID = "00000000-0000-0000-0000-000000000105"
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO venues (id, venue_type, display_title, issn_l)
		VALUES
			($1, 'journal', 'Registry Missing', '0000-0132'),
			($2, 'preprint', 'Registry Preprint', NULL)
	`, missingVenueID, preprintVenueID); err != nil {
		t.Fatalf("insert missing/non-journal Venues: %v", err)
	}

	jcrStore := mustNewPostgresJCRStore(t, pool)
	receipt, err := jcrStore.PersistJCRImport(
		ctx,
		mustJCRImport(
			t,
			strings.Repeat("5", 64),
			2026,
			[]MetricSnapshot{
				mustMetricSnapshot(
					t,
					fixtureVenueAlpha,
					2025,
					"Oncology",
					"4.5",
					QuartileQ1,
					MetricStatusKnown,
					"synthetic-jcr-fixture",
				),
				mustMetricSnapshot(
					t,
					fixtureVenueUnknown,
					2025,
					"Immunology",
					"10",
					QuartileQ2,
					MetricStatusKnown,
					"synthetic-jcr-fixture",
				),
				mustMetricSnapshot(
					t,
					fixtureVenueSubthreshold,
					2025,
					"Neurosciences",
					"9.999",
					QuartileQ2,
					MetricStatusKnown,
					"synthetic-jcr-fixture",
				),
			},
			nil,
		),
	)
	if err != nil {
		t.Fatalf("PersistJCRImport() error = %v", err)
	}
	var receiptID string
	if err := pool.QueryRow(ctx, `
		SELECT id::text
		FROM jcr_import_receipts
		WHERE file_sha256 = $1
	`, receipt.FileSHA256()).Scan(&receiptID); err != nil {
		t.Fatalf("query JCR receipt ID: %v", err)
	}

	store, err := NewPostgresAssessmentStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresAssessmentStore() error = %v", err)
	}
	service, err := NewAssessmentService(store)
	if err != nil {
		t.Fatalf("NewAssessmentService() error = %v", err)
	}
	input := AssessmentInput{
		JCRImportReceiptID: receiptID,
		MetricYear:         2025,
		PolicyVersion:      JournalJIFOrQ1PolicyVersion,
		AssessedAt:         time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
	}
	first, err := service.Assess(context.Background(), input)
	if err != nil {
		t.Fatalf("Assess(first) error = %v", err)
	}
	second, err := service.Assess(context.Background(), input)
	if err != nil {
		t.Fatalf("Assess(idempotent) error = %v", err)
	}
	if first != second ||
		first.Total != 5 ||
		first.Accepted != 2 ||
		first.Rejected != 1 ||
		first.Unknown != 1 ||
		first.NotApplicable != 1 {
		t.Fatalf("assessment summaries = first %#v second %#v", first, second)
	}

	rows, err := pool.Query(ctx, `
		SELECT
			assessment.venue_id::text,
			assessment.decision,
			assessment.matched_rules,
			assessment.evidence,
			assessment.assessed_at,
			policy.policy_name,
			policy.version_number
		FROM venue_policy_assessments AS assessment
		JOIN venue_policy_versions AS policy
		  ON policy.id = assessment.policy_version_id
		ORDER BY assessment.venue_id
	`)
	if err != nil {
		t.Fatalf("query persisted assessments: %v", err)
	}
	defer rows.Close()
	type persistedAssessment struct {
		venueID      string
		decision     string
		matchedRules []string
		evidence     map[string]any
		assessedAt   time.Time
		policyName   string
		policyNumber int
	}
	var got []persistedAssessment
	for rows.Next() {
		var (
			item                         persistedAssessment
			rawMatchedRules, rawEvidence []byte
		)
		if err := rows.Scan(
			&item.venueID,
			&item.decision,
			&rawMatchedRules,
			&rawEvidence,
			&item.assessedAt,
			&item.policyName,
			&item.policyNumber,
		); err != nil {
			t.Fatalf("scan persisted assessment: %v", err)
		}
		if err := json.Unmarshal(rawMatchedRules, &item.matchedRules); err != nil {
			t.Fatalf("decode matched rules: %v", err)
		}
		if err := json.Unmarshal(rawEvidence, &item.evidence); err != nil {
			t.Fatalf("decode evidence: %v", err)
		}
		got = append(got, item)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate persisted assessments: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("persisted assessments = %d, want 5", len(got))
	}
	wantDecisions := []string{
		"accepted",
		"accepted",
		"rejected",
		"unknown",
		"not_applicable",
	}
	for index, item := range got {
		if item.decision != wantDecisions[index] ||
			item.policyName != "journal-jif-or-q1" ||
			item.policyNumber != 1 ||
			!item.assessedAt.Equal(input.AssessedAt) ||
			item.evidence["jcr_import_receipt_id"] != receiptID ||
			item.evidence["policy_version"] != JournalJIFOrQ1PolicyVersion {
			t.Fatalf("persisted assessment %d = %#v", index, item)
		}
	}
	if got[0].evidence["venue_type"] != "journal" ||
		got[4].evidence["venue_type"] != "preprint" ||
		got[4].evidence["reason"] != "venue_type_not_journal" {
		t.Fatalf("venue evidence = accepted %#v not-applicable %#v", got[0], got[4])
	}
}

func TestPostgresAssessmentStoreRejectsReceiptMetricYearMismatch(t *testing.T) {
	pool := openMigratedVenueTestPool(t)
	venueID := insertSingleStoreVenue(t, pool)
	store := mustNewPostgresJCRStore(t, pool)
	receipt, err := store.PersistJCRImport(
		venueTestContext(t),
		mustJCRImport(
			t,
			strings.Repeat("6", 64),
			2026,
			[]MetricSnapshot{
				mustMetricSnapshot(
					t,
					venueID,
					2024,
					"Oncology",
					"12",
					QuartileQ1,
					MetricStatusKnown,
					"synthetic-jcr-fixture",
				),
			},
			nil,
		),
	)
	if err != nil {
		t.Fatalf("PersistJCRImport() error = %v", err)
	}
	var receiptID string
	if err := pool.QueryRow(venueTestContext(t), `
		SELECT id::text FROM jcr_import_receipts WHERE file_sha256 = $1
	`, receipt.FileSHA256()).Scan(&receiptID); err != nil {
		t.Fatalf("query JCR receipt ID: %v", err)
	}
	assessmentStore, err := NewPostgresAssessmentStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresAssessmentStore() error = %v", err)
	}
	service, err := NewAssessmentService(assessmentStore)
	if err != nil {
		t.Fatalf("NewAssessmentService() error = %v", err)
	}
	_, err = service.Assess(context.Background(), AssessmentInput{
		JCRImportReceiptID: receiptID,
		MetricYear:         2025,
		PolicyVersion:      JournalJIFOrQ1PolicyVersion,
		AssessedAt:         time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "metric year") {
		t.Fatalf("Assess(wrong metric year) error = %v", err)
	}
	var assessments int
	if err := pool.QueryRow(
		venueTestContext(t),
		"SELECT count(*) FROM venue_policy_assessments",
	).Scan(&assessments); err != nil {
		t.Fatalf("count Venue assessments: %v", err)
	}
	if assessments != 0 {
		t.Fatalf("Venue assessments = %d, want zero", assessments)
	}
}
