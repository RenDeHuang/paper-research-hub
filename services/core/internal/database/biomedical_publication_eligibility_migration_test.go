package database

import (
	"strings"
	"testing"
)

func TestBiomedicalPublicationEligibilityMigrationCreatesImmutableEvidenceBoundDecisions(
	t *testing.T,
) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	expectedColumns := []string{
		"id",
		"work_id",
		"policy_version",
		"metric_year",
		"subject_version_id",
		"decision",
		"venue_id",
		"journal_subject_metric_id",
		"evidence",
		"assessed_at",
		"created_at",
	}
	for _, column := range expectedColumns {
		var nullable string
		if err := pool.QueryRow(ctx, `
			SELECT is_nullable
			FROM information_schema.columns
			WHERE table_schema = 'public'
			  AND table_name = 'biomedical_publication_eligibility_decisions'
			  AND column_name = $1
		`, column).Scan(&nullable); err != nil {
			t.Fatalf("eligibility column %s metadata: %v", column, err)
		}
		if column != "venue_id" &&
			column != "journal_subject_metric_id" &&
			nullable != "NO" {
			t.Fatalf("eligibility column %s nullable = %q, want NO", column, nullable)
		}
	}

	var uniqueDefinition string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conname =
			'biomedical_publication_eligibility_decisions_identity_key'
	`).Scan(&uniqueDefinition); err != nil {
		t.Fatalf("query eligibility identity constraint: %v", err)
	}
	for _, column := range []string{
		"work_id",
		"policy_version",
		"metric_year",
		"subject_version_id",
	} {
		if !strings.Contains(uniqueDefinition, column) {
			t.Fatalf(
				"eligibility identity constraint = %q, want %s",
				uniqueDefinition,
				column,
			)
		}
	}

	for _, trigger := range []string{
		"biomedical_publication_eligibility_decisions_semantics",
		"biomedical_publication_eligibility_decisions_immutable",
	} {
		var enabled string
		if err := pool.QueryRow(ctx, `
			SELECT tgenabled::text
			FROM pg_trigger
			WHERE tgrelid =
				'biomedical_publication_eligibility_decisions'::regclass
			  AND tgname = $1
			  AND NOT tgisinternal
		`, trigger).Scan(&enabled); err != nil {
			t.Fatalf("query eligibility trigger %s: %v", trigger, err)
		}
		if enabled != "O" {
			t.Fatalf("eligibility trigger %s enabled = %q, want O", trigger, enabled)
		}
	}
}
