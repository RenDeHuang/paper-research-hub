package database

import (
	"context"
	"strings"
	"testing"
)

func TestContentObservationAndVersionTruthSchemaIsExplicitAndImmutable(
	t *testing.T,
) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	requiredTables := []string{
		"ingestion_raw_observations",
		"work_channel_event_assertions",
		"work_channel_event_decisions",
		"work_version_number_assertions",
		"work_version_number_assessments",
		"work_version_kind_assessments",
		"work_version_kind_relation_decisions",
	}
	for _, table := range requiredTables {
		var relation *string
		if err := pool.QueryRow(ctx, `
			SELECT to_regclass('public.' || $1)::text
		`, table).Scan(&relation); err != nil {
			t.Fatalf("query table %q: %v", table, err)
		}
		if relation == nil {
			t.Fatalf("required content truth table %q is missing", table)
		}
	}

	var observedAtDefault *string
	if err := pool.QueryRow(ctx, `
		SELECT column_default
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'ingestion_raw_observations'
		  AND column_name = 'observed_at'
	`).Scan(&observedAtDefault); err != nil {
		t.Fatalf("query ingestion_raw_observations.observed_at: %v", err)
	}
	if observedAtDefault != nil {
		t.Fatalf(
			"ingestion_raw_observations.observed_at default = %q, want explicit connector boundary input",
			*observedAtDefault,
		)
	}

	requiredTriggers := map[string]string{
		"ingestion_raw_observations_immutable":           "ingestion_raw_observations",
		"work_channel_event_assertions_immutable":        "work_channel_event_assertions",
		"work_channel_event_decisions_immutable":         "work_channel_event_decisions",
		"work_version_number_assertions_immutable":       "work_version_number_assertions",
		"work_version_number_assessments_immutable":      "work_version_number_assessments",
		"work_version_kind_assessments_immutable":        "work_version_kind_assessments",
		"work_version_kind_relation_decisions_immutable": "work_version_kind_relation_decisions",
	}
	for trigger, table := range requiredTriggers {
		var count int
		if err := pool.QueryRow(ctx, `
			SELECT count(*)
			FROM pg_trigger
			WHERE tgname = $1
			  AND tgrelid = $2::regclass
			  AND NOT tgisinternal
		`, trigger, table).Scan(&count); err != nil {
			t.Fatalf("query trigger %q: %v", trigger, err)
		}
		if count != 1 {
			t.Fatalf("trigger %q count = %d, want 1", trigger, count)
		}
	}
}

func TestContentTruthSchemaEncodesNullableVersionAndGraphEvidence(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := testContext(t)

	requiredConstraints := []string{
		"ingestion_raw_observations_raw_job_fkey",
		"ingestion_raw_observations_page_fkey",
		"ingestion_raw_observations_coordinates_key",
		"work_channel_event_assertions_channel_kind_check",
		"work_channel_event_decisions_shape_check",
		"work_version_number_assertions_number_check",
		"work_version_number_assessments_state_check",
		"work_version_number_assessments_shape_check",
		"work_version_number_assessments_checked_revisions_check",
		"work_version_kind_assessments_kind_check",
		"work_version_kind_assessments_channel_decision_fkey",
		"work_version_kind_relation_decisions_relation_fkey",
	}
	rows, err := pool.Query(ctx, `
		SELECT conname
		FROM pg_constraint
		WHERE conname = ANY($1::text[])
	`, requiredConstraints)
	if err != nil {
		t.Fatalf("query content truth constraints: %v", err)
	}
	defer rows.Close()
	found := make(map[string]bool, len(requiredConstraints))
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan content truth constraint: %v", err)
		}
		found[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate content truth constraints: %v", err)
	}
	for _, name := range requiredConstraints {
		if !found[name] {
			t.Errorf("content truth constraint %q is missing", name)
		}
	}

	var viewDefinition string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_viewdef('current_work_first_observed_at'::regclass, true)
	`).Scan(&viewDefinition); err != nil {
		t.Fatalf("query current_work_first_observed_at view: %v", err)
	}
	normalized := strings.Join(strings.Fields(viewDefinition), " ")
	if !strings.Contains(normalized, "min(observation.observed_at)") ||
		strings.Contains(normalized, "source_records.retrieved_at") ||
		strings.Contains(normalized, "ingestion_raw_events.created_at") {
		t.Fatalf(
			"current_work_first_observed_at definition = %q, want MIN(raw observations observed_at) only",
			normalized,
		)
	}
}

func TestContentTruthHistoryRowsRejectMutation(t *testing.T) {
	pool := openMigratedTestPool(t)
	ctx := context.Background()

	var triggerFunctionCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_trigger AS trigger
		JOIN pg_proc AS procedure ON procedure.oid = trigger.tgfoid
		WHERE trigger.tgname IN (
			'ingestion_raw_observations_immutable',
			'work_channel_event_assertions_immutable',
			'work_channel_event_decisions_immutable',
			'work_version_number_assertions_immutable',
			'work_version_number_assessments_immutable',
			'work_version_kind_assessments_immutable',
			'work_version_kind_relation_decisions_immutable'
		)
		  AND procedure.proname = 'reject_immutable_row'
		  AND NOT trigger.tgisinternal
	`).Scan(&triggerFunctionCount); err != nil {
		t.Fatalf("query immutable content truth trigger functions: %v", err)
	}
	if triggerFunctionCount != 7 {
		t.Fatalf(
			"immutable content truth triggers using reject_immutable_row = %d, want 7",
			triggerFunctionCount,
		)
	}
}
