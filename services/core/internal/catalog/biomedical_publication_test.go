package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/analysis"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPublisherPublishesExactBiomedicalAnalysisSnapshots(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:catalog-biomedical-analysis-publication",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/catalog-biomedical-analysis-publication",
		title:             "Catalog biomedical analysis publication",
		methodNames:       []string{"Causal Inference"},
		paperType:         "research_article",
		publishedAt:       time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
	cohortRevision := preparePublisherAcceptedCurationWithoutBiomedicalRuns(
		t,
		pool,
		input,
		fixture,
	)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)
	insertCatalogBiomedicalPublicationFixtures(
		t,
		pool,
		input,
		cohortRevision,
		cohortRevision,
		input.TrendAnalysisRunID,
		fixture,
	)

	generation, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}

	home, err := mustRepository(t, pool).Home(context.Background())
	if err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"subject_momentum", "analysis", "analysis_run_id"},
		input.TrendAnalysisRunID.String(),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"subject_momentum", "analysis", "cohort_revision"},
		cohortRevision,
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"subject_momentum", "items", "0", "estimate", "value"},
		float64(7.25),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"entity_momentum", "items", "0", "label"},
		"Causal Inference",
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"research_opportunities", "analysis", "analysis_run_id"},
		input.OpportunityAnalysisRunID.String(),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"research_opportunities", "analysis", "window_days", "state"},
		"missing",
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"research_opportunities", "items", "0", "estimates", "0", "value"},
		float64(0.81),
	)

	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	journal, err := mustRepository(t, pool).Journal(
		context.Background(),
		"journal-"+strings.ReplaceAll(venueID.String(), "-", ""),
		PageQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Journal() error = %v", err)
	}
	assertNestedJSONValue(
		t,
		journal.Payload,
		[]string{"editorial_patterns", "analysis", "analysis_run_id"},
		input.JournalAnalysisRunID.String(),
	)
	assertNestedJSONValue(
		t,
		journal.Payload,
		[]string{"editorial_patterns", "items", "0", "estimate", "value"},
		float64(4.5),
	)

	opportunities, err := mustRepository(t, pool).ResearchOpportunities(
		context.Background(),
		OpportunityQuery{Status: "insufficient_evidence", Limit: 20},
	)
	if err != nil {
		t.Fatalf("ResearchOpportunities() error = %v", err)
	}
	if len(opportunities.Items) != 1 {
		t.Fatalf(
			"ResearchOpportunities() item count = %d, want 1",
			len(opportunities.Items),
		)
	}
	assertNestedJSONValue(
		t,
		opportunities.Items[0],
		[]string{"analysis_run_id"},
		input.OpportunityAnalysisRunID.String(),
	)
	assertNestedJSONValue(
		t,
		opportunities.Items[0],
		[]string{"supporting_work_ids", "0"},
		fixture.workID.String(),
	)

	var metadata []byte
	if err := pool.QueryRow(context.Background(), `
		SELECT metadata
		FROM public_catalog_generations
		WHERE id = $1
	`, generation.ID).Scan(&metadata); err != nil {
		t.Fatalf("query published generation metadata: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatalf("decode published generation metadata: %v", err)
	}
	for key, want := range map[string]any{
		"trend_analysis_run_id":       input.TrendAnalysisRunID.String(),
		"journal_analysis_run_id":     input.JournalAnalysisRunID.String(),
		"opportunity_analysis_run_id": input.OpportunityAnalysisRunID.String(),
		"opportunities":               float64(1),
	} {
		if decoded[key] != want {
			t.Fatalf("generation metadata[%q] = %#v, want %#v", key, decoded[key], want)
		}
	}
}

func TestPublisherRejectsBiomedicalSnapshotOutsideValidatedCohort(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:catalog-biomedical-analysis-cohort-mismatch",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/catalog-biomedical-analysis-cohort-mismatch",
		title:             "Catalog biomedical analysis cohort mismatch",
		publishedAt:       time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
	cohortRevision := preparePublisherAcceptedCurationWithoutBiomedicalRuns(
		t,
		pool,
		input,
		fixture,
	)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)
	insertCatalogBiomedicalPublicationFixtures(
		t,
		pool,
		input,
		cohortRevision,
		strings.Repeat("f", 64),
		input.TrendAnalysisRunID,
		fixture,
	)

	_, err := mustPublisher(t, pool).PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "cohort revision") {
		t.Fatalf(
			"PublishCurrent(snapshot cohort mismatch) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestSnapshotRevisionIncludesOpportunityRows(t *testing.T) {
	input := catalogCurationInput()
	firstOpportunityID := uuid.MustParse(
		"00000000-0000-0000-0000-000000000801",
	)
	secondOpportunityID := uuid.MustParse(
		"00000000-0000-0000-0000-000000000802",
	)
	first := catalogSnapshot{
		opportunities: []publishedResearchOpportunity{{
			ID:      firstOpportunityID,
			Status:  "insufficient_evidence",
			Ordinal: 1,
			Payload: json.RawMessage(`{"id":"00000000-0000-0000-0000-000000000801"}`),
		}},
	}
	second := first
	second.opportunities = []publishedResearchOpportunity{{
		ID:      secondOpportunityID,
		Status:  "insufficient_evidence",
		Ordinal: 1,
		Payload: json.RawMessage(`{"id":"00000000-0000-0000-0000-000000000802"}`),
	}}

	firstRevision, err := snapshotRevision(input, first)
	if err != nil {
		t.Fatalf("snapshotRevision(first) error = %v", err)
	}
	secondRevision, err := snapshotRevision(input, second)
	if err != nil {
		t.Fatalf("snapshotRevision(second) error = %v", err)
	}
	if firstRevision == secondRevision {
		t.Fatalf(
			"snapshot revisions are equal for distinct opportunity rows: %s",
			firstRevision,
		)
	}
}

func preparePublisherAcceptedCurationWithoutBiomedicalRuns(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	fixtures ...publisherWorkFixture,
) string {
	t.Helper()
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	venueIDs := make([]uuid.UUID, 0, len(fixtures))
	jcrFixtures := make([]catalogJCRVenueFixture, 0, len(fixtures))
	for index, fixture := range fixtures {
		venueID := catalogWorkVenueID(t, pool, fixture.workID)
		if _, err := pool.Exec(context.Background(), `
			UPDATE venues
			SET venue_type = 'journal',
			    issn_l = $2
			WHERE id = $1
		`, venueID, deterministicCatalogISSN(index+900)); err != nil {
			t.Fatalf("prepare biomedical publication Journal identity: %v", err)
		}
		venueIDs = append(venueIDs, venueID)
		jcrFixtures = append(jcrFixtures, catalogJCRVenueFixture{
			venueID:     venueID,
			index:       index,
			withSubject: true,
		})
	}
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, jcrFixtures)
	assessCatalogVenuePolicy(t, pool, input)
	for _, fixture := range fixtures {
		assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	}
	return catalogBiomedicalCohortRevisionForFixtures(t, pool, input, fixtures)
}

func insertCatalogBiomedicalPublicationFixtures(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	runCohortRevision string,
	snapshotCohortRevision string,
	opportunityTrendRunID uuid.UUID,
	fixture publisherWorkFixture,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin biomedical publication fixtures: %v", err)
	}
	defer tx.Rollback(context.Background())

	var (
		subjectVersionID uuid.UUID
		subjectID        uuid.UUID
		subjectSlug      string
		venueID          uuid.UUID
	)
	if err := tx.QueryRow(ctx, `
		SELECT
			eligibility.subject_version_id,
			rule.subject_id,
			subject.slug,
			eligibility.venue_id
		FROM biomedical_publication_eligibility_decisions AS eligibility
		JOIN journal_subject_metrics AS link
		  ON link.id = eligibility.journal_subject_metric_id
		JOIN biomedical_subject_rules AS rule
		  ON rule.id = link.subject_rule_id
		JOIN subjects AS subject
		  ON subject.id = rule.subject_id
		 AND subject.subject_version_id = eligibility.subject_version_id
		WHERE eligibility.work_id = $1
		  AND eligibility.decision = 'accepted'
	`, fixture.workID).Scan(
		&subjectVersionID,
		&subjectID,
		&subjectSlug,
		&venueID,
	); err != nil {
		t.Fatalf("query biomedical publication fixture identities: %v", err)
	}

	asOf := input.GeneratedAt.Add(-3 * time.Minute).UTC()
	startedAt := input.GeneratedAt.Add(-2 * time.Minute).UTC()
	completedAt := input.GeneratedAt.Add(-time.Minute).UTC()
	common := map[string]any{
		"as_of":                      asOf.Format(time.RFC3339Nano),
		"subject_version":            input.SubjectVersion,
		"eligibility_policy_version": input.EligibilityPolicyVersion,
		"jcr_metric_year":            input.JCRMetricYear,
		"jcr_import_receipt":         input.JCRImportReceipt,
		"venue_policy_name":          input.VenuePolicyName,
		"venue_policy_version":       input.VenuePolicyVersion,
		"cohort_revision":            runCohortRevision,
	}
	type runFixture struct {
		id              uuid.UUID
		analysisType    string
		formulaVersion  string
		additionalInput map[string]any
	}
	runs := []runFixture{
		{
			id:             input.TrendAnalysisRunID,
			analysisType:   "publication_trends",
			formulaVersion: analysis.PublicationTrendFormulaVersion,
			additionalInput: map[string]any{
				"recent_window_days":                56,
				"baseline_window_days":              364,
				"minimum_paper_count":               20,
				"minimum_independent_journal_count": 3,
				"minimum_independent_team_count":    3,
				"model_selection_rule":              "fixed_poisson",
				"dispersion_threshold":              0,
			},
		},
		{
			id:             input.JournalAnalysisRunID,
			analysisType:   "journal_editorial_patterns",
			formulaVersion: analysis.JournalPatternFormulaVersion,
			additionalInput: map[string]any{
				"window_days":                  364,
				"minimum_support_count":        10,
				"minimum_field_baseline_count": 40,
			},
		},
		{
			id:             input.OpportunityAnalysisRunID,
			analysisType:   "research_opportunities",
			formulaVersion: analysis.OpportunityFormulaVersion,
			additionalInput: map[string]any{
				"rule_set_version":         analysis.OpportunityRuleSetVersion,
				"citation_analysis_run_id": input.CitationAnalysisRunID,
				"trend_analysis_run_id":    opportunityTrendRunID,
				"journal_analysis_run_id":  input.JournalAnalysisRunID,
			},
		},
	}
	for _, run := range runs {
		payload := make(map[string]any, len(common)+len(run.additionalInput)+1)
		for key, value := range common {
			payload[key] = value
		}
		payload["formula_version"] = run.formulaVersion
		for key, value := range run.additionalInput {
			payload[key] = value
		}
		rawInput, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode %s run input: %v", run.analysisType, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO analysis_runs (
				id,
				analysis_type,
				model_provider,
				model_name,
				prompt_version,
				status,
				input_payload,
				started_at
			) VALUES (
				$1, $2, 'internal', 'deterministic', $3, 'running',
				$4::jsonb, $5
			)
		`,
			run.id,
			run.analysisType,
			run.formulaVersion,
			rawInput,
			startedAt,
		); err != nil {
			t.Fatalf("insert running %s analysis run: %v", run.analysisType, err)
		}
	}

	baselineStart := asOf.Add(-364 * 24 * time.Hour)
	recentStart := asOf.Add(-56 * 24 * time.Hour)
	trendFixtures := []struct {
		entityType string
		entityID   string
		rateRatio  string
	}{
		{entityType: "subject", entityID: subjectSlug, rateRatio: "7.25"},
	}
	var methodName string
	err = tx.QueryRow(ctx, `
		SELECT method.name
		FROM work_methods AS link
		JOIN methods AS method
		  ON method.id = link.method_id
		WHERE link.work_id = $1
		ORDER BY method.name
		LIMIT 1
	`, fixture.workID).Scan(&methodName)
	if err == nil {
		trendFixtures = append(trendFixtures, struct {
			entityType string
			entityID   string
			rateRatio  string
		}{
			entityType: "method",
			entityID:   methodName,
			rateRatio:  "3.75",
		})
	} else if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("query biomedical publication fixture Method: %v", err)
	}
	for _, trend := range trendFixtures {
		if _, err := tx.Exec(ctx, `
			INSERT INTO publication_trend_snapshots (
				analysis_run_id,
				entity_type,
				entity_id,
				state,
				model,
				model_selection_rule,
				dispersion_threshold,
				predeclared_dispersion_alpha,
				recent_window_start,
				recent_window_end,
				recent_paper_count,
				recent_rate_per_day,
				baseline_window_start,
				baseline_window_end,
				baseline_paper_count,
				baseline_rate_per_day,
				independent_journal_count,
				independent_team_count,
				rate_ratio,
				confidence_level,
				confidence_interval_lower,
				confidence_interval_upper,
				p_value,
				adjusted_p_value,
				cohort_revision,
				formula_version,
				payload,
				evidence,
				generated_at
			) VALUES (
				$1, $2, $3, 'sufficient_evidence', 'poisson',
				'fixed_poisson', 0, 0, $4, $5, 24, 0.5,
				$6, $4, 40, 0.13, 3, 4, $7::numeric, 0.95,
				1.2, 8.5, 0.01, 0.02, $8, $9,
				'{"fixture":"persisted_trend"}',
				'{"fixture":"persisted_trend_evidence"}',
				$10
			)
		`,
			input.TrendAnalysisRunID,
			trend.entityType,
			trend.entityID,
			recentStart,
			asOf,
			baselineStart,
			trend.rateRatio,
			snapshotCohortRevision,
			analysis.PublicationTrendFormulaVersion,
			startedAt,
		); err != nil {
			t.Fatalf("insert %s publication trend snapshot: %v", trend.entityType, err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO journal_pattern_snapshots (
			analysis_run_id,
			pattern_key,
			journal_id,
			subject_version_id,
			subject_id,
			pattern_kind,
			feature_type,
			feature_value,
			state,
			measure,
			journal_feature_paper_count,
			journal_paper_count,
			field_feature_paper_count,
			field_baseline_paper_count,
			journal_exposure,
			field_baseline_exposure,
			covered_paper_count,
			eligible_paper_count,
			coverage,
			effect_value,
			confidence_level,
			confidence_interval_lower,
			confidence_interval_upper,
			p_value,
			adjusted_p_value,
			cohort_revision,
			formula_version,
			payload,
			evidence,
			generated_at
		) VALUES (
			$1, 'method:causal-inference', $2, $3, $4,
			'editorial_pattern', 'method', 'Causal Inference',
			'sufficient_evidence', 'odds_ratio',
			12, 20, 20, 50, NULL, NULL, 20, 20, 1,
			4.5, 0.95, 1.25, 6.5, 0.01, 0.02, $5, $6,
			'{"fixture":"persisted_journal_pattern"}',
			'{"fixture":"persisted_journal_pattern_evidence"}',
			$7
		)
	`,
		input.JournalAnalysisRunID,
		venueID,
		subjectVersionID,
		subjectID,
		snapshotCohortRevision,
		analysis.JournalPatternFormulaVersion,
		startedAt,
	); err != nil {
		t.Fatalf("insert journal pattern snapshot: %v", err)
	}

	opportunityID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO research_opportunity_snapshots (
			id,
			analysis_run_id,
			rule,
			rule_version,
			entity_type,
			entity_id,
			state,
			supporting_work_count,
			covered_paper_count,
			eligible_paper_count,
			coverage,
			confidence_level,
			primary_metric,
			primary_estimate,
			primary_confidence_interval_lower,
			primary_confidence_interval_upper,
			limitations,
			cohort_revision,
			payload,
			evidence,
			generated_at
		) VALUES (
			$1, $2, 'observational_dominance', $3,
			'subject', $4, 'insufficient_evidence',
			1, 1, 2, 0.5, 0.95,
			'observational_share', 0.81, 0.71, 0.91,
			ARRAY['requires_external_validation'], $5,
			'{"fixture":"persisted_opportunity"}',
			'{"fixture":"persisted_opportunity_evidence"}',
			$6
		)
	`,
		opportunityID,
		input.OpportunityAnalysisRunID,
		analysis.OpportunityRuleSetVersion,
		subjectSlug,
		snapshotCohortRevision,
		startedAt,
	); err != nil {
		t.Fatalf("insert research opportunity snapshot: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO research_opportunity_supporting_works (
			analysis_run_id,
			research_opportunity_snapshot_id,
			work_id,
			ordinal
		) VALUES ($1, $2, $3, 1)
	`,
		input.OpportunityAnalysisRunID,
		opportunityID,
		fixture.workID,
	); err != nil {
		t.Fatalf("insert research opportunity supporting Work: %v", err)
	}

	for _, completed := range []struct {
		id            uuid.UUID
		snapshotCount int
	}{
		{id: input.TrendAnalysisRunID, snapshotCount: len(trendFixtures)},
		{id: input.JournalAnalysisRunID, snapshotCount: 1},
		{id: input.OpportunityAnalysisRunID, snapshotCount: 1},
	} {
		rawOutput, err := json.Marshal(map[string]any{
			"cohort_revision": runCohortRevision,
			"snapshot_count":  completed.snapshotCount,
		})
		if err != nil {
			t.Fatalf("encode completed biomedical run output: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE analysis_runs
			SET status = 'succeeded',
			    output_payload = $2::jsonb,
			    completed_at = $3
			WHERE id = $1
		`, completed.id, rawOutput, completedAt); err != nil {
			t.Fatalf("complete biomedical analysis run %s: %v", completed.id, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit biomedical publication fixtures: %v", err)
	}
}
