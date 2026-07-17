package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublisherRequiresAcceptedBiomedicalJCRAssessment(t *testing.T) {
	pool := openCatalogTestPool(t)
	input := catalogCurationInput()
	fixtures := []struct {
		name           string
		decision       string
		policyName     string
		policyVersion  int
		metricYear     int
		lifecycle      string
		venueType      string
		withISSN       bool
		withAssessment bool
		withSubject    bool
	}{
		{
			name:           "accepted",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "rejected",
			decision:       "rejected",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "unknown",
			decision:       "unknown",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:          "missing assessment",
			lifecycle:     "active",
			venueType:     "journal",
			withISSN:      true,
			withSubject:   true,
			metricYear:    input.JCRMetricYear,
			policyName:    input.VenuePolicyName,
			policyVersion: input.VenuePolicyVersion,
		},
		{
			name:           "wrong metric year",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear - 1,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "stale policy",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion + 1,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "missing exact subject",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
		},
		{
			name:           "missing ISSN",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "journal",
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "non journal",
			decision:       "not_applicable",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "active",
			venueType:      "preprint",
			withAssessment: true,
			withSubject:    true,
		},
		{
			name:           "inactive work",
			decision:       "accepted",
			policyName:     input.VenuePolicyName,
			policyVersion:  input.VenuePolicyVersion,
			metricYear:     input.JCRMetricYear,
			lifecycle:      "withdrawn",
			venueType:      "journal",
			withISSN:       true,
			withAssessment: true,
			withSubject:    true,
		},
	}

	subjectVersionID, subjectRuleID := insertCatalogSubjectVersion(
		t,
		pool,
		input.SubjectVersion,
	)
	_ = subjectVersionID

	workIDs := make(map[string]uuid.UUID, len(fixtures))
	type preparedFixture struct {
		spec    int
		venueID uuid.UUID
	}
	prepared := make([]preparedFixture, 0, len(fixtures))
	for index, spec := range fixtures {
		fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "pubmed:curation-" + strings.ReplaceAll(spec.name, " ", "-"),
			canonicalKey:      fmt.Sprintf("doi:10.1000/curation-%02d", index),
			title:             "Curation " + spec.name,
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
			noJCRAssessment:   true,
			sourceTime: time.Date(
				2026,
				time.July,
				17,
				10,
				index,
				0,
				0,
				time.UTC,
			),
		})
		workIDs[spec.name] = fixture.workID
		venueID := catalogWorkVenueID(t, pool, fixture.workID)
		if _, err := pool.Exec(context.Background(), `
			UPDATE works
			SET status = $2
			WHERE id = $1
		`, fixture.workID, spec.lifecycle); err != nil {
			t.Fatalf("set %s lifecycle: %v", spec.name, err)
		}
		var issn any
		if spec.withISSN {
			issn = deterministicCatalogISSN(index + 1)
		}
		if _, err := pool.Exec(context.Background(), `
			UPDATE venues
			SET venue_type = $2,
			    issn_l = $3
			WHERE id = $1
		`, venueID, spec.venueType, issn); err != nil {
			t.Fatalf("set %s Venue evidence: %v", spec.name, err)
		}
		prepared = append(prepared, preparedFixture{spec: index, venueID: venueID})
	}
	jcrFixtures := make([]catalogJCRVenueFixture, 0, len(prepared))
	for _, item := range prepared {
		jcrFixtures = append(jcrFixtures, catalogJCRVenueFixture{
			venueID:     item.venueID,
			index:       item.spec,
			withSubject: fixtures[item.spec].withSubject,
		})
	}
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, jcrFixtures)
	for _, item := range prepared {
		spec := fixtures[item.spec]
		if spec.withAssessment {
			insertCatalogVenueAssessment(
				t,
				pool,
				input,
				item.venueID,
				spec.policyName,
				spec.policyVersion,
				spec.metricYear,
				spec.decision,
			)
		}
	}

	publisher := mustPublisher(t, pool)
	generation, err := publisher.PublishCurrent(context.Background(), input)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	repository := mustRepository(t, pool)
	page, err := repository.Papers(context.Background(), PaperListQuery{Limit: 50})
	if err != nil {
		t.Fatalf("Papers() error = %v", err)
	}
	assertPaperIDs(t, page.Items, workIDs["accepted"])

	for name, workID := range workIDs {
		if name == "accepted" {
			continue
		}
		if _, err := repository.Paper(context.Background(), workID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Paper(%s) error = %v, want ErrNotFound", name, err)
		}
	}

	var metadata []byte
	if err := pool.QueryRow(context.Background(), `
		SELECT metadata
		FROM public_catalog_generations
		WHERE id = $1
	`, generation.ID).Scan(&metadata); err != nil {
		t.Fatalf("query Catalog generation metadata: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatalf("decode Catalog generation metadata: %v", err)
	}
	for key, want := range map[string]any{
		"jcr_metric_year":      float64(input.JCRMetricYear),
		"venue_policy_name":    input.VenuePolicyName,
		"venue_policy_version": float64(input.VenuePolicyVersion),
		"subject_version":      input.SubjectVersion,
		"jcr_import_receipt":   input.JCRImportReceipt.String(),
	} {
		if decoded[key] != want {
			t.Fatalf("generation metadata[%q] = %#v, want %#v", key, decoded[key], want)
		}
	}
}

func TestPublisherReturnsEmptyDomainWhenNoWorkPassesCurationGate(t *testing.T) {
	pool := openCatalogTestPool(t)
	input := catalogCurationInput()
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:curation-empty",
		canonicalKey:      "doi:10.1000/curation-empty",
		title:             "Rejected biomedical paper",
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE venues SET issn_l = $2 WHERE id = $1
	`, venueID, deterministicCatalogISSN(100)); err != nil {
		t.Fatalf("set rejected Venue ISSN: %v", err)
	}
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, []catalogJCRVenueFixture{{
		venueID:     venueID,
		index:       100,
		withSubject: true,
	}})
	insertCatalogVenueAssessment(
		t,
		pool,
		input,
		venueID,
		input.VenuePolicyName,
		input.VenuePolicyVersion,
		input.JCRMetricYear,
		"rejected",
	)

	_, err := mustPublisher(t, pool).PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrEmptyDomain) {
		t.Fatalf("PublishCurrent() error = %v, want ErrEmptyDomain", err)
	}
	assertNoCatalogWrites(t, pool)
}

func catalogCurationInput() PublishInput {
	return catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
}

func catalogCurationInputAt(generatedAt time.Time) PublishInput {
	return PublishInput{
		FormulaVersion:     "public-catalog/biomedical-v1",
		GeneratedAt:        generatedAt,
		JCRMetricYear:      2025,
		VenuePolicyName:    "journal-jif-or-q1",
		VenuePolicyVersion: 1,
		SubjectVersion:     "biomedical-jcr-subjects/v1",
		JCRImportReceipt:   uuid.MustParse("00000000-0000-0000-0000-000000000501"),
	}
}

func preparePublisherAcceptedCuration(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	fixtures ...publisherWorkFixture,
) {
	t.Helper()
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	jcrFixtures := make([]catalogJCRVenueFixture, 0, len(fixtures))
	venueIDs := make([]uuid.UUID, 0, len(fixtures))
	for index, fixture := range fixtures {
		venueID := catalogWorkVenueID(t, pool, fixture.workID)
		if _, err := pool.Exec(context.Background(), `
			UPDATE venues
			SET venue_type = 'journal',
			    issn_l = $2
			WHERE id = $1
		`, venueID, deterministicCatalogISSN(1000+index)); err != nil {
			t.Fatalf("prepare publisher Venue curation: %v", err)
		}
		venueIDs = append(venueIDs, venueID)
		jcrFixtures = append(jcrFixtures, catalogJCRVenueFixture{
			venueID:     venueID,
			index:       1000 + index,
			withSubject: true,
		})
	}
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, jcrFixtures)
	for _, venueID := range venueIDs {
		insertCatalogVenueAssessment(
			t,
			pool,
			input,
			venueID,
			input.VenuePolicyName,
			input.VenuePolicyVersion,
			input.JCRMetricYear,
			"accepted",
		)
	}
}

func preparePublisherEmptyCurationReferences(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
) {
	t.Helper()
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, nil)
}

func insertCatalogSubjectVersion(
	t *testing.T,
	pool *pgxpool.Pool,
	version string,
) (uuid.UUID, uuid.UUID) {
	t.Helper()
	receiptID := uuid.New()
	versionID := uuid.New()
	subjectID := uuid.New()
	ruleID := uuid.New()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Catalog Subject fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `
		INSERT INTO subject_import_receipts (
			id,
			source,
			registry_version,
			file_sha256,
			subject_count,
			rule_count,
			imported_at
		) VALUES (
			$1,
			'medpaperhub-reviewed-jcr-category-allowlist',
			$2,
			$3,
			1,
			1,
			$4
		)
	`,
		receiptID,
		version,
		strings.Repeat("a", 64),
		time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("insert Catalog Subject receipt: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO subject_versions (
			id,
			subject_import_receipt_id,
			version_key
		) VALUES ($1, $2, $3)
	`, versionID, receiptID, version); err != nil {
		t.Fatalf("insert Catalog Subject version: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO subjects (
			id,
			subject_version_id,
			slug,
			display_label
		) VALUES ($1, $2, 'oncology', 'Oncology')
	`, subjectID, versionID); err != nil {
		t.Fatalf("insert Catalog Subject: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO biomedical_subject_rules (
			id,
			subject_version_id,
			subject_id,
			jcr_category
		) VALUES ($1, $2, $3, 'Oncology')
	`,
		ruleID,
		versionID,
		subjectID,
	); err != nil {
		t.Fatalf("insert Catalog Subject rule: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit Catalog Subject fixture: %v", err)
	}
	return versionID, ruleID
}

type catalogJCRVenueFixture struct {
	venueID     uuid.UUID
	index       int
	withSubject bool
}

func insertCatalogJCRBundle(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	subjectRuleID uuid.UUID,
	fixtures []catalogJCRVenueFixture,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin Catalog JCR fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipts (
			id,
			file_sha256,
			source,
			imported_at,
			input_rows,
			inserted_rows,
			unchanged_rows
		) VALUES (
			$1,
			$2,
			'authorized-jcr',
			$3,
			$4,
			$4,
			0
		)
	`,
		input.JCRImportReceipt,
		strings.Repeat("b", 64),
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
		len(fixtures),
	); err != nil {
		t.Fatalf("insert Catalog JCR receipt: %v", err)
	}
	for _, fixture := range fixtures {
		metricID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO venue_metric_snapshots (
				id,
				venue_id,
				metric_year,
				category,
				jif,
				quartile,
				metric_status,
				source_name,
				source_license,
				captured_at,
				jcr_import_receipt_id
			) VALUES (
				$1,
				$2,
				$3,
				'Oncology',
				12,
				'Q1',
				'known',
				'authorized-jcr',
				'institution-authorized',
				$4,
				$5
			)
		`,
			metricID,
			fixture.venueID,
			input.JCRMetricYear,
			input.GeneratedAt.Add(time.Duration(fixture.index)*time.Second),
			input.JCRImportReceipt,
		); err != nil {
			t.Fatalf("insert Catalog JCR metric: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO jcr_import_receipt_metrics (
				import_receipt_id,
				metric_snapshot_id
			) VALUES ($1, $2)
		`, input.JCRImportReceipt, metricID); err != nil {
			t.Fatalf("link Catalog JCR receipt metric: %v", err)
		}
		if fixture.withSubject {
			if _, err := tx.Exec(ctx, `
				INSERT INTO journal_subject_metrics (
					venue_metric_snapshot_id,
					subject_rule_id,
					jcr_category
				) VALUES ($1, $2, 'Oncology')
			`, metricID, subjectRuleID); err != nil {
				t.Fatalf("link Catalog exact Subject: %v", err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit Catalog JCR fixture: %v", err)
	}
}

func insertCatalogVenueAssessment(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	venueID uuid.UUID,
	policyName string,
	policyVersion int,
	metricYear int,
	decision string,
) {
	t.Helper()
	policyID := uuid.New()
	matchedRules := `[]`
	reason := `"no_policy_rule_matched"`
	if decision == "accepted" {
		matchedRules = `["jcr_q1"]`
		reason = `null`
	}
	if decision == "unknown" {
		reason = `"missing_or_unknown_metric_evidence"`
	}
	if decision == "not_applicable" {
		reason = `"venue_type_not_journal"`
	}
	evidence := fmt.Sprintf(
		`{
			"jcr_import_receipt_id":%q,
			"policy_version":%q,
			"metric_year":%d,
			"venue_type":%q,
			"reason":%s,
			"categories":[{"category":"Oncology","jif":"12","quartile":"Q1","status":"known","source_name":"authorized-jcr"}]
		}`,
		input.JCRImportReceipt.String(),
		fmt.Sprintf("%s/v%d", policyName, policyVersion),
		metricYear,
		catalogVenueType(t, pool, venueID),
		reason,
	)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO venue_policy_versions (
			id,
			policy_name,
			version_number,
			definition,
			effective_at
		) VALUES ($1, $2, $3, '{"kind":"curation-gate-test"}', $4)
		ON CONFLICT (policy_name, version_number) DO NOTHING
	`,
		policyID,
		policyName,
		policyVersion,
		input.GeneratedAt.Add(-time.Hour),
	); err != nil {
		t.Fatalf("insert Catalog Venue policy: %v", err)
	}
	if err := pool.QueryRow(context.Background(), `
		SELECT id
		FROM venue_policy_versions
		WHERE policy_name = $1
		  AND version_number = $2
	`, policyName, policyVersion).Scan(&policyID); err != nil {
		t.Fatalf("query Catalog Venue policy: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO venue_policy_assessments (
			venue_id,
			policy_version_id,
			metric_year,
			decision,
			matched_rules,
			evidence,
			assessed_at
		) VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7)
	`,
		venueID,
		policyID,
		metricYear,
		decision,
		matchedRules,
		evidence,
		input.GeneratedAt.Add(-time.Hour),
	); err != nil {
		t.Fatalf("insert Catalog Venue assessment: %v", err)
	}
}

func catalogWorkVenueID(
	t *testing.T,
	pool *pgxpool.Pool,
	workID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var venueID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		SELECT venue_id FROM works WHERE id = $1
	`, workID).Scan(&venueID); err != nil {
		t.Fatalf("query Work Venue: %v", err)
	}
	return venueID
}

func catalogVenueType(
	t *testing.T,
	pool *pgxpool.Pool,
	venueID uuid.UUID,
) string {
	t.Helper()
	var venueType string
	if err := pool.QueryRow(context.Background(), `
		SELECT venue_type FROM venues WHERE id = $1
	`, venueID).Scan(&venueType); err != nil {
		t.Fatalf("query Venue type: %v", err)
	}
	return venueType
}

func deterministicCatalogISSN(value int) string {
	digits := fmt.Sprintf("%07d", value%10_000_000)
	sum := 0
	for index := 0; index < 7; index++ {
		sum += int(digits[index]-'0') * (8 - index)
	}
	check := (11 - sum%11) % 11
	checkDigit := byte('0' + check)
	if check == 10 {
		checkDigit = 'X'
	}
	return digits[:4] + "-" + digits[4:] + string(checkDigit)
}
