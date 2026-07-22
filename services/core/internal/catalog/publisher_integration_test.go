package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/abstractanalysis"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/contenttruth"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/scope"
)

func TestPublisherBuildsAndAtomicallyPublishesNormalizedCurrentState(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-visible",
		canonicalKey:      "doi:10.1000/publisher-visible",
		title:             "Evidence-Grounded Agents",
		topicNames:        []string{"Agent Systems"},
		methodNames:       []string{"Causal Inference"},
		withCode:          true,
		withData:          true,
		withBenchmark:     true,
		paperType:         "research_article",
		jcrDecision:       "accepted",
		jcrMatchedRules:   `["jif_at_least_10"]`,
		jcrEvidence:       `{"venue_type":"journal","jif":12.4,"quartiles":["Q1"]}`,
		publishedAt:       time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	publisher := mustPublisher(t, pool)
	generatedAt := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	input := catalogCurationInputAt(generatedAt)
	preparePublisherAcceptedCuration(t, pool, input, fixture)

	generation, err := publisher.PublishCurrent(context.Background(), input)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	if generation.ID == uuid.Nil ||
		generation.GeneratedAt != generatedAt ||
		generation.FormulaVersion != input.FormulaVersion {
		t.Fatalf("published generation = %#v, want requested immutable generation", generation)
	}
	if len(generation.SourceRevision) != 64 {
		t.Fatalf("source revision = %q, want SHA-256", generation.SourceRevision)
	}

	repository := mustRepository(t, pool)
	page, err := repository.Papers(context.Background(), PaperListQuery{Limit: 20})
	if err != nil {
		t.Fatalf("Papers() after publish error = %v", err)
	}
	assertPaperIDs(t, page.Items, fixture.workID)

	detail, err := repository.Paper(context.Background(), fixture.workID)
	if err != nil {
		t.Fatalf("Paper() after publish error = %v", err)
	}
	assertNestedJSONValue(t, detail.Payload, []string{"type", "state"}, "known")
	assertNestedJSONValue(
		t,
		detail.Payload,
		[]string{"type", "value"},
		"research_article",
	)
	assertNestedJSONValue(t, detail.Payload, []string{"has_code", "value"}, true)
	assertNestedJSONValue(t, detail.Payload, []string{"has_data", "value"}, true)
	assertNestedJSONValue(t, detail.Payload, []string{"has_benchmark", "value"}, true)
	assertNestedJSONValue(t, detail.Payload, []string{"curation", "state"}, "known")
	assertNestedJSONValue(
		t,
		detail.Payload,
		[]string{"curation", "value", "decision"},
		"accepted",
	)
	assertJSONArrayLength(t, detail.Payload, "source_provenance", 1)

	topics, err := repository.Topics(context.Background(), PageQuery{Limit: 20})
	if err != nil {
		t.Fatalf("Topics() after publish error = %v", err)
	}
	if len(topics.Items) != 1 {
		t.Fatalf("topic count = %d, want 1", len(topics.Items))
	}
	assertJSONField(t, topics.Items[0], "slug", "agent-systems")

	methods, err := repository.Methods(context.Background(), PageQuery{Limit: 20})
	if err != nil {
		t.Fatalf("Methods() after publish error = %v", err)
	}
	if len(methods.Items) != 1 {
		t.Fatalf("method count = %d, want 1", len(methods.Items))
	}
	assertJSONField(t, methods.Items[0], "slug", "causal-inference")

	stats, err := repository.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() after publish error = %v", err)
	}
	assertNestedJSONValue(t, stats.Payload, []string{"papers_total", "value"}, float64(1))
	assertNestedJSONValue(t, stats.Payload, []string{"with_code_ratio", "value"}, float64(1))
}

func TestPublisherFactsModePublishesWithoutAnalysisRunsAndPersistsVisibility(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-facts-visibility",
		canonicalKey:      "doi:10.1000/publisher-facts-visibility",
		title:             "Facts-only visibility assessment",
		paperType:         "research_article",
		publishedAt:       time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
	input.Mode = PublishFacts
	input.AnalysisCutoff = time.Time{}
	input.ClassifierVersion = ""
	input.AbstractRouteRevision = ""
	input.CitationSource = ""
	input.CitationAnalysisRunID = uuid.Nil
	input.TrendAnalysisRunID = uuid.Nil
	input.JournalAnalysisRunID = uuid.Nil
	input.OpportunityAnalysisRunID = uuid.Nil
	preparePublisherAcceptedCurationWithoutBiomedicalRuns(
		t,
		pool,
		input,
		fixture,
	)
	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	); err != nil {
		t.Fatalf("PublishCurrent(facts) error = %v", err)
	}

	paper, err := mustRepository(t, pool).Paper(
		context.Background(),
		fixture.workID,
	)
	if err != nil {
		t.Fatalf("Paper(facts) error = %v", err)
	}
	assertNestedJSONValue(t, paper.Payload, []string{"publicly_visible"}, true)
	assertNestedJSONValue(t, paper.Payload, []string{"analysis_ready"}, false)
	assertNestedJSONValue(
		t,
		paper.Payload,
		[]string{"citation_analysis_evidence", "state"},
		"missing",
	)
	assertNestedJSONValue(
		t,
		paper.Payload,
		[]string{"trend_score", "state"},
		"missing",
	)

	var (
		publiclyVisible bool
		analysisReady   bool
		reasons         []string
		evaluatedAt     time.Time
	)
	if err := pool.QueryRow(context.Background(), `
		SELECT publicly_visible, analysis_ready, reasons, evaluated_at
		FROM work_visibility_assessments
		WHERE work_id = $1
		  AND policy_version = $2
		  AND evaluated_at = $3
	`, fixture.workID, VisibilityPolicyVersion, input.GeneratedAt).Scan(
		&publiclyVisible,
		&analysisReady,
		&reasons,
		&evaluatedAt,
	); err != nil {
		t.Fatalf("query facts visibility assessment: %v", err)
	}
	if !publiclyVisible || analysisReady || len(reasons) != 0 ||
		!evaluatedAt.Equal(input.GeneratedAt) {
		t.Fatalf(
			"facts visibility assessment = public %v analysis %v reasons %v evaluated %s",
			publiclyVisible,
			analysisReady,
			reasons,
			evaluatedAt,
		)
	}

	var genericAnalysisRuns, abstractRouteRuns int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM analysis_runs),
			(SELECT count(*) FROM abstract_route_analysis_runs)
	`).Scan(&genericAnalysisRuns, &abstractRouteRuns); err != nil {
		t.Fatalf("count facts-only analysis rows: %v", err)
	}
	if genericAnalysisRuns != 0 || abstractRouteRuns != 0 {
		t.Fatalf(
			"facts-only analysis rows = generic %d abstract-route %d, want zero",
			genericAnalysisRuns,
			abstractRouteRuns,
		)
	}
}

func TestPublisherFactsModePublishesJournalWithoutJCRSubjectOrCurationEvidence(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "crossref:publisher-facts-no-jcr",
		logicalSource:     "crossref",
		canonicalKey:      "doi:10.1000/publisher-facts-no-jcr",
		title:             "Facts publication without JCR dependencies",
		paperType:         "research_article",
		publishedAt:       time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
	)
	input.Mode = PublishFacts
	input.AnalysisCutoff = time.Time{}
	input.ClassifierVersion = ""
	input.AbstractRouteRevision = ""
	input.JCRMetricYear = 0
	input.VenuePolicyName = ""
	input.VenuePolicyVersion = 0
	input.EligibilityPolicyVersion = ""
	input.SubjectVersion = ""
	input.JCRImportReceipt = uuid.Nil
	input.CitationSource = ""
	input.CitationAnalysisRunID = uuid.Nil
	input.TrendAnalysisRunID = uuid.Nil
	input.JournalAnalysisRunID = uuid.Nil
	input.OpportunityAnalysisRunID = uuid.Nil

	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	); err != nil {
		t.Fatalf("PublishCurrent(facts without JCR evidence) error = %v", err)
	}

	paper, err := mustRepository(t, pool).Paper(
		context.Background(),
		fixture.workID,
	)
	if err != nil {
		t.Fatalf("Paper(facts without JCR evidence) error = %v", err)
	}
	assertNestedJSONValue(t, paper.Payload, []string{"publicly_visible"}, true)
	assertNestedJSONValue(t, paper.Payload, []string{"analysis_ready"}, false)
	assertNestedJSONValue(
		t,
		paper.Payload,
		[]string{"curation", "state"},
		"missing",
	)
	assertNestedJSONValue(
		t,
		paper.Payload,
		[]string{"jcr_assessment", "state"},
		"missing",
	)
}

func TestPublisherFactsModeIgnoresUnrelatedPendingSourceState(t *testing.T) {
	pool := openCatalogTestPool(t)
	included := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "crossref:publisher-facts-pending-control",
		logicalSource:     "crossref",
		canonicalKey:      "doi:10.1000/publisher-facts-pending-control",
		title:             "Facts pending control",
		paperType:         "preprint",
		publishedAt:       time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
		contentChannel:    scope.ContentChannelPreprint,
		lifecycleState:    scope.LifecycleStatePreprintActive,
	})
	pending := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:            "openalex:publisher-facts-unrelated-pending",
		canonicalKey:        "doi:10.1000/publisher-facts-unrelated-pending",
		title:               "Unrelated pending enrichment",
		scopeStatus:         "pending",
		includeWorkLink:     false,
		includeWorkID:       false,
		includeNormalized:   true,
		noJCRAssessment:     true,
		withoutOfficialLink: true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
	)
	input.Mode = PublishFacts
	input.AnalysisCutoff = time.Time{}
	input.ClassifierVersion = ""
	input.AbstractRouteRevision = ""
	input.JCRMetricYear = 0
	input.VenuePolicyName = ""
	input.VenuePolicyVersion = 0
	input.EligibilityPolicyVersion = ""
	input.SubjectVersion = ""
	input.JCRImportReceipt = uuid.Nil
	input.CitationSource = ""
	input.CitationAnalysisRunID = uuid.Nil
	input.TrendAnalysisRunID = uuid.Nil
	input.JournalAnalysisRunID = uuid.Nil
	input.OpportunityAnalysisRunID = uuid.Nil

	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	); err != nil {
		t.Fatalf(
			"PublishCurrent(facts with unrelated pending source) error = %v",
			err,
		)
	}
	page, err := mustRepository(t, pool).Papers(
		context.Background(),
		PaperListQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Papers(facts with unrelated pending source) error = %v", err)
	}
	assertPaperIDs(t, page.Items, included.workID)
	if _, err := mustRepository(t, pool).Paper(
		context.Background(),
		pending.workID,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Paper(unrelated pending Work) error = %v, want ErrNotFound", err)
	}
}

func TestPublisherFactsModeDoesNotReadTaxonomyAnalysisRows(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "crossref:publisher-facts-taxonomy-not-ready",
		logicalSource:     "crossref",
		canonicalKey:      "doi:10.1000/publisher-facts-taxonomy-not-ready",
		title:             "Facts taxonomy not ready",
		topicNames:        []string{"Broken classifier topic"},
		paperType:         "preprint",
		publishedAt:       time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
		contentChannel:    scope.ContentChannelPreprint,
		lifecycleState:    scope.LifecycleStatePreprintActive,
	})
	if _, err := pool.Exec(context.Background(), `
		UPDATE topics
		SET description = ''
		WHERE id = (
			SELECT topic_id
			FROM work_topics
			WHERE work_id = $1
		)
	`, fixture.workID); err != nil {
		t.Fatalf("corrupt unrelated taxonomy analysis row: %v", err)
	}
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
	)
	input.Mode = PublishFacts
	input.AnalysisCutoff = time.Time{}
	input.ClassifierVersion = ""
	input.AbstractRouteRevision = ""
	input.JCRMetricYear = 0
	input.VenuePolicyName = ""
	input.VenuePolicyVersion = 0
	input.EligibilityPolicyVersion = ""
	input.SubjectVersion = ""
	input.JCRImportReceipt = uuid.Nil
	input.CitationSource = ""
	input.CitationAnalysisRunID = uuid.Nil
	input.TrendAnalysisRunID = uuid.Nil
	input.JournalAnalysisRunID = uuid.Nil
	input.OpportunityAnalysisRunID = uuid.Nil

	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	); err != nil {
		t.Fatalf("PublishCurrent(facts with bad taxonomy analysis) error = %v", err)
	}
	paper, err := mustRepository(t, pool).Paper(
		context.Background(),
		fixture.workID,
	)
	if err != nil {
		t.Fatalf("Paper(facts with bad taxonomy analysis) error = %v", err)
	}
	assertNestedJSONValue(t, paper.Payload, []string{"topics"}, []any{})
	assertNestedJSONValue(t, paper.Payload, []string{"methods"}, []any{})
	assertNestedJSONValue(
		t,
		paper.Payload,
		[]string{"topics_state"},
		"not_ready",
	)
	assertNestedJSONValue(
		t,
		paper.Payload,
		[]string{"methods_state"},
		"not_ready",
	)
	assertNestedJSONValue(t, paper.Payload, []string{"analysis_ready"}, false)
}

func TestPublisherFactsModePersistsInactiveWorkVisibilityAndExcludesIt(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	active := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "crossref:publisher-facts-active-control",
		logicalSource:     "crossref",
		canonicalKey:      "doi:10.1000/publisher-facts-active-control",
		title:             "Active facts control",
		paperType:         "preprint",
		publishedAt:       time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
		contentChannel:    scope.ContentChannelPreprint,
		lifecycleState:    scope.LifecycleStatePreprintActive,
	})
	inactive := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "crossref:publisher-facts-inactive",
		logicalSource:     "crossref",
		canonicalKey:      "doi:10.1000/publisher-facts-inactive",
		title:             "Inactive facts work",
		paperType:         "preprint",
		publishedAt:       time.Date(2026, time.July, 18, 7, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
		contentChannel:    scope.ContentChannelPreprint,
		lifecycleState:    scope.LifecycleStatePreprintActive,
	})
	if _, err := pool.Exec(context.Background(), `
		UPDATE works
		SET status = 'retracted'
		WHERE id = $1
	`, inactive.workID); err != nil {
		t.Fatalf("mark facts Work inactive: %v", err)
	}

	input := catalogCurationInputAt(
		time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
	)
	input.Mode = PublishFacts
	input.AnalysisCutoff = time.Time{}
	input.ClassifierVersion = ""
	input.AbstractRouteRevision = ""
	input.CitationSource = ""
	input.CitationAnalysisRunID = uuid.Nil
	input.TrendAnalysisRunID = uuid.Nil
	input.JournalAnalysisRunID = uuid.Nil
	input.OpportunityAnalysisRunID = uuid.Nil
	preparePublisherFactsReferences(t, pool, input)

	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	); err != nil {
		t.Fatalf("PublishCurrent(facts with inactive Work) error = %v", err)
	}
	page, err := mustRepository(t, pool).Papers(
		context.Background(),
		PaperListQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Papers(facts with inactive Work) error = %v", err)
	}
	assertPaperIDs(t, page.Items, active.workID)

	var publiclyVisible, analysisReady bool
	var reasons []string
	if err := pool.QueryRow(context.Background(), `
		SELECT publicly_visible, analysis_ready, reasons
		FROM work_visibility_assessments
		WHERE work_id = $1
		  AND policy_version = $2
		  AND evaluated_at = $3
	`, inactive.workID, VisibilityPolicyVersion, input.GeneratedAt).Scan(
		&publiclyVisible,
		&analysisReady,
		&reasons,
	); err != nil {
		t.Fatalf("query inactive Work visibility: %v", err)
	}
	if publiclyVisible || analysisReady ||
		!slices.Contains(reasons, "work_inactive") {
		t.Fatalf(
			"inactive Work visibility = public %v analysis %v reasons %v",
			publiclyVisible,
			analysisReady,
			reasons,
		)
	}
}

func TestPublisherFactsModeFiltersUsingRestoredVisibilityState(t *testing.T) {
	pool := openCatalogTestPool(t)
	control := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "crossref:publisher-facts-restored-control",
		logicalSource:     "crossref",
		canonicalKey:      "doi:10.1000/publisher-facts-restored-control",
		title:             "Restored visibility control",
		paperType:         "preprint",
		publishedAt:       time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
		contentChannel:    scope.ContentChannelPreprint,
		lifecycleState:    scope.LifecycleStatePreprintActive,
	})
	restoredHidden := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "crossref:publisher-facts-restored-hidden",
		logicalSource:     "crossref",
		canonicalKey:      "doi:10.1000/publisher-facts-restored-hidden",
		title:             "Restored hidden visibility",
		paperType:         "preprint",
		publishedAt:       time.Date(2026, time.July, 18, 7, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
		contentChannel:    scope.ContentChannelPreprint,
		lifecycleState:    scope.LifecycleStatePreprintActive,
	})
	if _, err := pool.Exec(context.Background(), fmt.Sprintf(`
		CREATE FUNCTION rewrite_test_visibility_state()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.work_id = '%s'::uuid THEN
				NEW.publicly_visible = false;
				NEW.analysis_ready = false;
				NEW.reasons = ARRAY['work_inactive']::text[];
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER rewrite_test_visibility_state
		BEFORE INSERT ON work_visibility_assessments
		FOR EACH ROW
		EXECUTE FUNCTION rewrite_test_visibility_state()
	`, restoredHidden.workID)); err != nil {
		t.Fatalf("install exact visibility persistence trigger: %v", err)
	}
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
	)
	input.Mode = PublishFacts
	input.AnalysisCutoff = time.Time{}
	input.ClassifierVersion = ""
	input.AbstractRouteRevision = ""
	input.JCRMetricYear = 0
	input.VenuePolicyName = ""
	input.VenuePolicyVersion = 0
	input.EligibilityPolicyVersion = ""
	input.SubjectVersion = ""
	input.JCRImportReceipt = uuid.Nil
	input.CitationSource = ""
	input.CitationAnalysisRunID = uuid.Nil
	input.TrendAnalysisRunID = uuid.Nil
	input.JournalAnalysisRunID = uuid.Nil
	input.OpportunityAnalysisRunID = uuid.Nil

	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	); err != nil {
		t.Fatalf("PublishCurrent(facts exact visibility state) error = %v", err)
	}
	page, err := mustRepository(t, pool).Papers(
		context.Background(),
		PaperListQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Papers(facts exact visibility state) error = %v", err)
	}
	assertPaperIDs(t, page.Items, control.workID)

	var publiclyVisible, analysisReady bool
	var reasons []string
	if err := pool.QueryRow(context.Background(), `
		SELECT publicly_visible, analysis_ready, reasons
		FROM work_visibility_assessments
		WHERE work_id = $1
		  AND policy_version = $2
		  AND evaluated_at = $3
	`, restoredHidden.workID, VisibilityPolicyVersion, input.GeneratedAt).Scan(
		&publiclyVisible,
		&analysisReady,
		&reasons,
	); err != nil {
		t.Fatalf("query exact restored visibility state: %v", err)
	}
	if publiclyVisible || analysisReady ||
		!slices.Equal(reasons, []string{"work_inactive"}) {
		t.Fatalf(
			"restored visibility state = public %v analysis %v reasons %v",
			publiclyVisible,
			analysisReady,
			reasons,
		)
	}
}

func TestPublisherFactsModePublishesEveryAcceptedChannel(t *testing.T) {
	tests := []struct {
		name        string
		channel     scope.ContentChannel
		lifecycle   scope.LifecycleState
		withJCRGate bool
		linkRole    string
	}{
		{
			name:        "journal published",
			channel:     scope.ContentChannelJournalPublished,
			lifecycle:   scope.LifecycleStatePublished,
			withJCRGate: true,
		},
		{
			name:        "accepted early",
			channel:     scope.ContentChannelAcceptedEarly,
			lifecycle:   scope.LifecycleStateAcceptedEarly,
			withJCRGate: true,
		},
		{
			name:      "preprint",
			channel:   scope.ContentChannelPreprint,
			lifecycle: scope.LifecycleStatePreprintActive,
		},
		{
			name:      "preprint DOI URL",
			channel:   scope.ContentChannelPreprint,
			lifecycle: scope.LifecycleStatePreprintActive,
			linkRole:  "doi_url",
		},
		{
			name:      "conference proceeding",
			channel:   scope.ContentChannelConferenceProceeding,
			lifecycle: scope.LifecycleStateConferencePublished,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			pool := openCatalogTestPool(t)
			slug := strings.ToLower(strings.ReplaceAll(test.name, " ", "-"))
			fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
				eventKey:          "openalex:publisher-channel-" + slug,
				canonicalKey:      "doi:10.1000/publisher-channel-" + slug,
				title:             "Publisher channel " + test.name,
				paperType:         "research_article",
				publishedAt:       time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
				sourceTime:        time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
				scopeStatus:       "included",
				includeWorkLink:   true,
				includeWorkID:     true,
				includeNormalized: true,
				noJCRAssessment:   true,
				contentChannel:    test.channel,
				lifecycleState:    test.lifecycle,
				officialLinkRole:  test.linkRole,
			})
			input := catalogCurationInput()
			input.Mode = PublishFacts
			input.AnalysisCutoff = time.Time{}
			input.ClassifierVersion = ""
			input.AbstractRouteRevision = ""
			input.CitationSource = ""
			input.CitationAnalysisRunID = uuid.Nil
			input.TrendAnalysisRunID = uuid.Nil
			input.JournalAnalysisRunID = uuid.Nil
			input.OpportunityAnalysisRunID = uuid.Nil
			if test.withJCRGate {
				preparePublisherAcceptedCurationWithoutBiomedicalRuns(
					t,
					pool,
					input,
					fixture,
				)
			} else {
				preparePublisherFactsReferences(t, pool, input)
			}

			if _, err := mustPublisher(t, pool).PublishCurrent(
				context.Background(),
				input,
			); err != nil {
				t.Fatalf("PublishCurrent(%s facts) error = %v", test.name, err)
			}
			paper, err := mustRepository(t, pool).Paper(
				context.Background(),
				fixture.workID,
			)
			if err != nil {
				t.Fatalf("Paper(%s facts) error = %v", test.name, err)
			}
			assertNestedJSONValue(
				t,
				paper.Payload,
				[]string{"official_link", "content_channel"},
				string(test.channel),
			)
			assertNestedJSONValue(
				t,
				paper.Payload,
				[]string{"publicly_visible"},
				true,
			)
		})
	}
}

func TestPublisherAnalysisModePublishesOnlyWithExactSucceededAbstractRouteRevision(
	t *testing.T,
) {
	t.Run("exact current revision is analysis ready", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:                "pubmed:publisher-analysis-ready",
			logicalSource:           "pubmed",
			canonicalKey:            "doi:10.1000/publisher-analysis-ready",
			title:                   "Exact abstract route readiness",
			topicNames:              []string{"Agent Systems"},
			methodNames:             []string{"Causal Inference"},
			paperType:               "research_article",
			publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
			scopeStatus:             "included",
			includeWorkLink:         true,
			includeWorkID:           true,
			includeNormalized:       true,
			normalizedPayloadSchema: "normalized-record/v4",
			noJCRAssessment:         true,
		})
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				printState:      "missing",
				electronicState: "missing",
				aheadState:      "missing",
				acceptedState:   "missing",
			},
		)
		input := catalogCurationInput()
		preparePublisherAcceptedCuration(t, pool, input, fixture)

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf("PublishCurrent(analysis exact revision) error = %v", err)
		}
		paper, err := mustRepository(t, pool).Paper(
			context.Background(),
			fixture.workID,
		)
		if err != nil {
			t.Fatalf("Paper(analysis exact revision) error = %v", err)
		}
		assertNestedJSONValue(
			t,
			paper.Payload,
			[]string{"analysis_ready"},
			true,
		)

		var (
			analysisReady  bool
			analysisCutoff time.Time
			reasons        []string
		)
		if err := pool.QueryRow(context.Background(), `
			SELECT analysis_ready, analysis_cutoff, reasons
			FROM work_visibility_assessments
			WHERE work_id = $1
			  AND policy_version = $2
			  AND evaluated_at = $3
		`, fixture.workID, VisibilityPolicyVersion, input.GeneratedAt).Scan(
			&analysisReady,
			&analysisCutoff,
			&reasons,
		); err != nil {
			t.Fatalf("query analysis visibility assessment: %v", err)
		}
		if !analysisReady ||
			!analysisCutoff.Equal(input.AnalysisCutoff) ||
			len(reasons) != 0 {
			t.Fatalf(
				"analysis assessment = ready %v cutoff %s reasons %v",
				analysisReady,
				analysisCutoff,
				reasons,
			)
		}
	})

	t.Run("stale classifier projection is not analysis ready", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:                "openalex:publisher-analysis-stale-classifier",
			canonicalKey:            "doi:10.1000/publisher-analysis-stale-classifier",
			title:                   "Stale classifier projection",
			topicNames:              []string{"Agent Systems"},
			methodNames:             []string{"Causal Inference"},
			paperType:               "research_article",
			publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
			scopeStatus:             "included",
			includeWorkLink:         true,
			includeWorkID:           true,
			includeNormalized:       true,
			projectionPolicyVersion: "projection/stale-source-mapping/v1",
			noJCRAssessment:         true,
		})
		input := catalogCurationInput()
		preparePublisherAcceptedCuration(t, pool, input, fixture)

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf("PublishCurrent(stale classifier projection) error = %v", err)
		}
		paper, err := mustRepository(t, pool).Paper(
			context.Background(),
			fixture.workID,
		)
		if err != nil {
			t.Fatalf("Paper(stale classifier projection) error = %v", err)
		}
		assertNestedJSONValue(
			t,
			paper.Payload,
			[]string{"analysis_ready"},
			false,
		)
		var reasons []string
		if err := pool.QueryRow(context.Background(), `
			SELECT reasons
			FROM work_visibility_assessments
			WHERE work_id = $1
			  AND policy_version = $2
			  AND evaluated_at = $3
		`, fixture.workID, VisibilityPolicyVersion, input.GeneratedAt).Scan(
			&reasons,
		); err != nil {
			t.Fatalf("query stale classifier visibility assessment: %v", err)
		}
		if !slices.Contains(reasons, string(VisibilityReasonTaxonomyMissing)) {
			t.Fatalf(
				"stale classifier visibility reasons = %v, want %s",
				reasons,
				VisibilityReasonTaxonomyMissing,
			)
		}
	})

	t.Run("mismatched revision is rejected", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:                "pubmed:publisher-analysis-revision-mismatch",
			logicalSource:           "pubmed",
			canonicalKey:            "doi:10.1000/publisher-analysis-revision-mismatch",
			title:                   "Mismatched abstract route revision",
			topicNames:              []string{"Agent Systems"},
			methodNames:             []string{"Causal Inference"},
			paperType:               "research_article",
			publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
			scopeStatus:             "included",
			includeWorkLink:         true,
			includeWorkID:           true,
			includeNormalized:       true,
			normalizedPayloadSchema: "normalized-record/v4",
			noJCRAssessment:         true,
		})
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				printState:      "missing",
				electronicState: "missing",
				aheadState:      "missing",
				acceptedState:   "missing",
			},
		)
		input := catalogCurationInput()
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		input.AbstractRouteRevision = strings.Repeat("b", 64)

		_, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		)
		if err == nil ||
			!strings.Contains(err.Error(), "abstract route revision") {
			t.Fatalf(
				"PublishCurrent(mismatched abstract route revision) error = %v",
				err,
			)
		}
		assertNoCatalogWrites(t, pool)
	})

	t.Run("missing exact run publishes facts-only", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:                "pubmed:publisher-analysis-run-missing",
			logicalSource:           "pubmed",
			canonicalKey:            "doi:10.1000/publisher-analysis-run-missing",
			title:                   "Missing abstract route run",
			topicNames:              []string{"Agent Systems"},
			methodNames:             []string{"Causal Inference"},
			paperType:               "research_article",
			publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
			scopeStatus:             "included",
			includeWorkLink:         true,
			includeWorkID:           true,
			includeNormalized:       true,
			normalizedPayloadSchema: "normalized-record/v4",
			noJCRAssessment:         true,
		})
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				printState:      "missing",
				electronicState: "missing",
				aheadState:      "missing",
				acceptedState:   "missing",
			},
		)
		input := catalogCurationInput()
		preparePublisherAcceptedCurationReferences(t, pool, input, fixture)
		insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf(
				"PublishCurrent(missing abstract route run) error = %v",
				err,
			)
		}
		paper, err := mustRepository(t, pool).Paper(
			context.Background(),
			fixture.workID,
		)
		if err != nil {
			t.Fatalf("Paper(missing abstract route run) error = %v", err)
		}
		assertNestedJSONValue(
			t,
			paper.Payload,
			[]string{"publicly_visible"},
			true,
		)
		assertNestedJSONValue(
			t,
			paper.Payload,
			[]string{"analysis_ready"},
			false,
		)
		var reasons []string
		if err := pool.QueryRow(context.Background(), `
			SELECT reasons
			FROM work_visibility_assessments
			WHERE work_id = $1
			  AND policy_version = $2
			  AND evaluated_at = $3
		`, fixture.workID, VisibilityPolicyVersion, input.GeneratedAt).Scan(
			&reasons,
		); err != nil {
			t.Fatalf("query missing abstract route visibility: %v", err)
		}
		if !slices.Contains(
			reasons,
			string(VisibilityReasonAbstractRouteMissing),
		) {
			t.Fatalf(
				"missing abstract route visibility reasons = %v, want %s",
				reasons,
				VisibilityReasonAbstractRouteMissing,
			)
		}
	})
}

func TestPublisherAnalysisModeOutputsRestoredAnalysisReadiness(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:                "pubmed:publisher-analysis-restored-state",
		logicalSource:           "pubmed",
		canonicalKey:            "doi:10.1000/publisher-analysis-restored-state",
		title:                   "Restored analysis readiness",
		topicNames:              []string{"Agent Systems"},
		methodNames:             []string{"Causal Inference"},
		paperType:               "research_article",
		publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
		scopeStatus:             "included",
		includeWorkLink:         true,
		includeWorkID:           true,
		includeNormalized:       true,
		normalizedPayloadSchema: "normalized-record/v4",
		noJCRAssessment:         true,
	})
	insertPublisherPublicationState(
		t,
		pool,
		fixture,
		publisherPublicationStateFixture{
			printState:      "missing",
			electronicState: "missing",
			aheadState:      "missing",
			acceptedState:   "missing",
		},
	)
	input := catalogCurationInput()
	preparePublisherAcceptedCuration(t, pool, input, fixture)
	if _, err := pool.Exec(context.Background(), fmt.Sprintf(`
		CREATE FUNCTION rewrite_test_analysis_readiness()
		RETURNS trigger
		LANGUAGE plpgsql
		AS $$
		BEGIN
			IF NEW.work_id = '%s'::uuid AND NEW.analysis_ready THEN
				NEW.analysis_ready = false;
				NEW.reasons = ARRAY['required_taxonomy_missing']::text[];
			END IF;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER rewrite_test_analysis_readiness
		BEFORE INSERT ON work_visibility_assessments
		FOR EACH ROW
		EXECUTE FUNCTION rewrite_test_analysis_readiness()
	`, fixture.workID)); err != nil {
		t.Fatalf("install exact analysis readiness trigger: %v", err)
	}

	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	); err != nil {
		t.Fatalf("PublishCurrent(restored analysis readiness) error = %v", err)
	}
	paper, err := mustRepository(t, pool).Paper(
		context.Background(),
		fixture.workID,
	)
	if err != nil {
		t.Fatalf("Paper(restored analysis readiness) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		paper.Payload,
		[]string{"analysis_ready"},
		false,
	)
	assertNestedJSONValue(
		t,
		paper.Payload,
		[]string{"topics_state"},
		"not_ready",
	)
	assertNestedJSONValue(
		t,
		paper.Payload,
		[]string{"methods_state"},
		"not_ready",
	)

	var visibilityReady, snapshotReady bool
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(
				SELECT analysis_ready
				FROM work_visibility_assessments
				WHERE work_id = $1
				  AND policy_version = $2
				  AND evaluated_at = $3
			),
			(
				SELECT analysis_ready
				FROM catalog_analysis_work_snapshots
				WHERE work_id = $1
			)
	`, fixture.workID, VisibilityPolicyVersion, input.GeneratedAt).Scan(
		&visibilityReady,
		&snapshotReady,
	); err != nil {
		t.Fatalf("query restored analysis readiness states: %v", err)
	}
	if visibilityReady || snapshotReady {
		t.Fatalf(
			"restored analysis readiness = visibility %v snapshot %v, want false/false",
			visibilityReady,
			snapshotReady,
		)
	}
}

func TestPublisherAnalysisCutoffRejectsLaterAnalysisInputTimes(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:                "pubmed:publisher-analysis-cutoff-inputs",
		logicalSource:           "pubmed",
		canonicalKey:            "doi:10.1000/publisher-analysis-cutoff-inputs",
		title:                   "Analysis inputs after cutoff",
		topicNames:              []string{"Agent Systems"},
		methodNames:             []string{"Causal Inference"},
		paperType:               "research_article",
		publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
		scopeStatus:             "included",
		includeWorkLink:         true,
		includeWorkID:           true,
		includeNormalized:       true,
		normalizedPayloadSchema: "normalized-record/v4",
		noJCRAssessment:         true,
	})
	insertPublisherPublicationState(
		t,
		pool,
		fixture,
		publisherPublicationStateFixture{
			printState:      "missing",
			electronicState: "missing",
			aheadState:      "missing",
			acceptedState:   "missing",
		},
	)
	input := catalogCurationInput()
	preparePublisherAcceptedCuration(t, pool, input, fixture)
	input.AnalysisCutoff = input.AnalysisCutoff.Add(-5 * time.Minute)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if err == nil || !strings.Contains(err.Error(), "analysis cutoff") {
		t.Fatalf(
			"PublishCurrent(analysis inputs after cutoff) error = %v, want analysis cutoff rejection",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherAnalysisSelectsExactPerWorkEvidenceBeforeCohort(t *testing.T) {
	t.Run("selects distinct abstract route runs for each ready Work", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		input := catalogCurationInput()
		eventAt := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
		first := insertPublisherAnalysisWork(
			t,
			pool,
			"publisher-per-work-first",
			eventAt,
		)
		second := insertPublisherAnalysisWork(
			t,
			pool,
			"publisher-per-work-second",
			eventAt,
		)
		preparePublisherAnalysisReferencesForCohort(
			t,
			pool,
			input,
			[]publisherWorkFixture{first, second},
			[]publisherWorkFixture{first, second},
		)
		firstRunID := uuid.MustParse(
			"00000000-0000-0000-0000-000000000705",
		)
		secondRunID := uuid.MustParse(
			"00000000-0000-0000-0000-000000000706",
		)
		insertCatalogSucceededAbstractRouteRunWithID(
			t,
			pool,
			input,
			first,
			firstRunID,
		)
		insertCatalogSucceededAbstractRouteRunWithID(
			t,
			pool,
			input,
			second,
			secondRunID,
		)

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf("PublishCurrent(per-Work analysis selection) error = %v", err)
		}
		for _, fixture := range []publisherWorkFixture{first, second} {
			paper, err := mustRepository(t, pool).Paper(
				context.Background(),
				fixture.workID,
			)
			if err != nil {
				t.Fatalf("Paper(%s) error = %v", fixture.workID, err)
			}
			assertNestedJSONValue(
				t,
				paper.Payload,
				[]string{"analysis_ready"},
				true,
			)
		}

		rows, err := pool.Query(context.Background(), `
			SELECT work_id, abstract_route_run_id
			FROM catalog_analysis_work_snapshots
			ORDER BY work_id
		`)
		if err != nil {
			t.Fatalf("query per-Work analysis snapshot: %v", err)
		}
		defer rows.Close()
		selected := make(map[uuid.UUID]uuid.UUID)
		for rows.Next() {
			var workID, runID uuid.UUID
			if err := rows.Scan(&workID, &runID); err != nil {
				t.Fatalf("scan per-Work analysis snapshot: %v", err)
			}
			selected[workID] = runID
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate per-Work analysis snapshot: %v", err)
		}
		if selected[first.workID] != firstRunID ||
			selected[second.workID] != secondRunID {
			t.Fatalf(
				"selected abstract runs = %#v, want first %s second %s",
				selected,
				firstRunID,
				secondRunID,
			)
		}
		for _, fixture := range []publisherWorkFixture{first, second} {
			var (
				projectionAssertionID uuid.UUID
				normalizedAssertionID uuid.UUID
				sourceRecordID        uuid.UUID
				classifierVersion     string
				classifierPolicy      string
				topicAssertions       int
				methodAssertions      int
			)
			if err := pool.QueryRow(context.Background(), `
				SELECT
					work_snapshot.projection_assertion_id,
					work_snapshot.normalized_assertion_id,
					work_snapshot.source_record_id,
					analysis_snapshot.classifier_version,
					analysis_snapshot.classifier_policy_version,
					(
						SELECT count(*)
						FROM catalog_analysis_work_topic_assertions AS topic
						WHERE topic.work_snapshot_id = work_snapshot.id
					),
					(
						SELECT count(*)
						FROM catalog_analysis_work_method_assertions AS method
						WHERE method.work_snapshot_id = work_snapshot.id
					)
				FROM catalog_analysis_work_snapshots AS work_snapshot
				JOIN catalog_analysis_snapshots AS analysis_snapshot
				  ON analysis_snapshot.id =
				     work_snapshot.analysis_snapshot_id
				WHERE work_snapshot.work_id = $1
			`, fixture.workID).Scan(
				&projectionAssertionID,
				&normalizedAssertionID,
				&sourceRecordID,
				&classifierVersion,
				&classifierPolicy,
				&topicAssertions,
				&methodAssertions,
			); err != nil {
				t.Fatalf(
					"query exact classifier snapshot for Work %s: %v",
					fixture.workID,
					err,
				)
			}
			if projectionAssertionID != fixture.projectionAssertionID ||
				normalizedAssertionID != fixture.normalizedAssertionID ||
				sourceRecordID != fixture.sourceRecord ||
				classifierVersion != CatalogClassifierVersion ||
				classifierPolicy != catalogClassifierAssertionPolicyVersion ||
				topicAssertions != 1 ||
				methodAssertions != 1 {
				t.Fatalf(
					"classifier snapshot for Work %s = projection %s normalized %s source %s version %q policy %q topics %d methods %d",
					fixture.workID,
					projectionAssertionID,
					normalizedAssertionID,
					sourceRecordID,
					classifierVersion,
					classifierPolicy,
					topicAssertions,
					methodAssertions,
				)
			}
		}
	})

	t.Run("excludes a Work whose canonical event follows cutoff", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		input := catalogCurationInputAt(
			time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
		)
		input.AnalysisCutoff = time.Date(
			2026,
			time.July,
			18,
			0,
			0,
			0,
			0,
			time.UTC,
		)
		ready := insertPublisherAnalysisWork(
			t,
			pool,
			"publisher-cohort-ready",
			input.AnalysisCutoff,
		)
		late := insertPublisherAnalysisWork(
			t,
			pool,
			"publisher-cohort-late",
			input.AnalysisCutoff.Add(time.Hour),
		)
		preparePublisherAnalysisReferencesForCohort(
			t,
			pool,
			input,
			[]publisherWorkFixture{ready, late},
			[]publisherWorkFixture{ready},
		)
		insertCatalogSucceededAbstractRouteRunWithID(
			t,
			pool,
			input,
			ready,
			uuid.MustParse("00000000-0000-0000-0000-000000000705"),
		)
		insertCatalogSucceededAbstractRouteRunWithID(
			t,
			pool,
			input,
			late,
			uuid.MustParse("00000000-0000-0000-0000-000000000707"),
		)

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf("PublishCurrent(analysis-ready cohort) error = %v", err)
		}
		readyPaper, err := mustRepository(t, pool).Paper(
			context.Background(),
			ready.workID,
		)
		if err != nil {
			t.Fatalf("Paper(ready) error = %v", err)
		}
		assertNestedJSONValue(
			t,
			readyPaper.Payload,
			[]string{"analysis_ready"},
			true,
		)
		latePaper, err := mustRepository(t, pool).Paper(
			context.Background(),
			late.workID,
		)
		if err != nil {
			t.Fatalf("Paper(late) error = %v", err)
		}
		assertNestedJSONValue(
			t,
			latePaper.Payload,
			[]string{"analysis_ready"},
			false,
		)
		var eventAt time.Time
		var analysisReady bool
		var reasons []string
		if err := pool.QueryRow(context.Background(), `
			SELECT canonical_channel_event_at, analysis_ready, reasons
			FROM catalog_analysis_work_snapshots
			WHERE work_id = $1
		`, late.workID).Scan(&eventAt, &analysisReady, &reasons); err != nil {
			t.Fatalf("query late per-Work analysis snapshot: %v", err)
		}
		if !eventAt.Equal(input.AnalysisCutoff.Add(time.Hour)) ||
			analysisReady ||
			!slices.Contains(
				reasons,
				string(VisibilityReasonCanonicalChannelEventAfterCutoff),
			) {
			t.Fatalf(
				"late snapshot = event %s ready %v reasons %v",
				eventAt,
				analysisReady,
				reasons,
			)
		}
	})

}

func TestPublisherRequiresActiveVerifiedHTTPSOfficialURL(t *testing.T) {
	t.Run("publishes only the URL-backed Work and preserves the exact DB link", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		input := catalogCurationInput()
		dbURL := "https://publisher.example.test/articles/exact-db-link?view=full"
		withURL := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "openalex:publisher-official-url-backed",
			canonicalKey:      "doi:10.1000/catalog-must-not-construct-this-url",
			title:             "Official URL backed publication",
			paperType:         "research_article",
			publishedAt:       time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			sourceTime:        time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
			officialURL:       dbURL,
			noJCRAssessment:   true,
		})
		withoutURL := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:            "openalex:publisher-official-url-missing",
			canonicalKey:        "doi:10.1000/catalog-official-url-missing",
			title:               "Publication without official URL",
			paperType:           "research_article",
			publishedAt:         time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
			sourceTime:          time.Date(2026, time.July, 17, 9, 30, 0, 0, time.UTC),
			scopeStatus:         "included",
			includeWorkLink:     true,
			includeWorkID:       true,
			includeNormalized:   true,
			withoutOfficialLink: true,
			noJCRAssessment:     true,
		})
		preparePublisherAcceptedCurationForPublishedCohort(
			t,
			pool,
			input,
			[]publisherWorkFixture{withURL, withoutURL},
			[]publisherWorkFixture{withURL},
		)

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf("PublishCurrent() error = %v", err)
		}
		repository := mustRepository(t, pool)
		page, err := repository.Papers(
			context.Background(),
			PaperListQuery{Limit: 20},
		)
		if err != nil {
			t.Fatalf("Papers() error = %v", err)
		}
		assertPaperIDs(t, page.Items, withURL.workID)
		if _, err := repository.Paper(
			context.Background(),
			withoutURL.workID,
		); !errors.Is(err, ErrNotFound) {
			t.Fatalf(
				"Paper(without official URL) error = %v, want ErrNotFound",
				err,
			)
		}

		paper, err := repository.Paper(context.Background(), withURL.workID)
		if err != nil {
			t.Fatalf("Paper(with official URL) error = %v", err)
		}
		assertNestedJSONValue(t, paper.Payload, []string{"publicly_visible"}, true)
		assertNestedJSONValue(t, paper.Payload, []string{"analysis_ready"}, false)
		for path, want := range map[string]any{
			"url":              dbURL,
			"verification_id":  withURL.officialVerificationID.String(),
			"link_role":        "official_article",
			"content_channel":  "journal_published",
			"verified_at":      withURL.officialVerifiedAt.Format(time.RFC3339Nano),
			"expires_at":       withURL.officialExpiresAt.Format(time.RFC3339Nano),
			"verifier_version": "publisher-fixture-verifier/v1",
			"policy_version":   "official-url/v1",
		} {
			assertNestedJSONValue(
				t,
				paper.Payload,
				[]string{"official_link", path},
				want,
			)
		}
		if strings.Contains(string(paper.Payload), "https://doi.org/") {
			t.Fatalf(
				"Paper payload constructed a DOI URL instead of publishing the exact DB URL: %s",
				paper.Payload,
			)
		}
	})

	t.Run("keeps valid v1 when newer same-role v2 is expired", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		input := catalogCurationInput()
		dbURL := "https://publisher.example.test/articles/versioned-official-link"
		fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "openalex:publisher-official-url-version-fallback",
			canonicalKey:      "doi:10.1000/catalog-official-url-version-fallback",
			title:             "Official URL version fallback",
			paperType:         "research_article",
			publishedAt:       time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
			sourceTime:        time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
			officialURL:       dbURL,
			noJCRAssessment:   true,
		})
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		insertPublisherExpiredOfficialLinkVersion(
			t,
			pool,
			fixture,
			time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC),
			time.Date(2026, time.July, 17, 11, 0, 0, 0, time.UTC),
		)

		if _, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		); err != nil {
			t.Fatalf("PublishCurrent() error = %v", err)
		}
		paper, err := mustRepository(t, pool).Paper(
			context.Background(),
			fixture.workID,
		)
		if err != nil {
			t.Fatalf("Paper() error = %v", err)
		}
		assertNestedJSONValue(
			t,
			paper.Payload,
			[]string{"official_link", "verification_id"},
			fixture.officialVerificationID.String(),
		)
		assertNestedJSONValue(
			t,
			paper.Payload,
			[]string{"official_link", "url"},
			dbURL,
		)
	})

	for _, test := range []struct {
		name    string
		options publisherWorkOptions
	}{
		{
			name: "missing link",
			options: publisherWorkOptions{
				withoutOfficialLink: true,
			},
		},
		{
			name: "expired link",
			options: publisherWorkOptions{
				officialLinkExpiresAt: time.Date(
					2026,
					time.July,
					17,
					10,
					0,
					0,
					0,
					time.UTC,
				),
			},
		},
		{
			name: "link projected after generation cutoff",
			options: publisherWorkOptions{
				officialLinkProjectedAt: time.Date(
					2026,
					time.July,
					17,
					13,
					0,
					0,
					0,
					time.UTC,
				),
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			pool := openCatalogTestPool(t)
			options := test.options
			testSlug := strings.ToLower(
				strings.ReplaceAll(test.name, " ", "-"),
			)
			options.eventKey = "openalex:publisher-url-gate-" + testSlug
			options.canonicalKey = "doi:10.1000/publisher-url-gate-" +
				testSlug
			options.title = "Publisher URL gate " + test.name
			options.paperType = "research_article"
			options.publishedAt = time.Date(
				2026,
				time.July,
				17,
				8,
				0,
				0,
				0,
				time.UTC,
			)
			options.sourceTime = time.Date(
				2026,
				time.July,
				17,
				9,
				0,
				0,
				0,
				time.UTC,
			)
			options.scopeStatus = "included"
			options.includeWorkLink = true
			options.includeWorkID = true
			options.includeNormalized = true
			options.noJCRAssessment = true
			fixture := insertPublisherVisibleWork(t, pool, options)
			input := catalogCurationInput()
			preparePublisherAcceptedCuration(t, pool, input, fixture)

			_, err := mustPublisher(t, pool).PublishCurrent(
				context.Background(),
				input,
			)
			if !errors.Is(err, ErrEmptyDomain) {
				t.Fatalf(
					"PublishCurrent(%s) error = %v, want ErrEmptyDomain",
					test.name,
					err,
				)
			}
			assertNoCatalogWrites(t, pool)
		})
	}
}

func TestPublisherRejectsPublicationEventProvenanceMismatch(t *testing.T) {
	t.Run("publication state does not match exact work winner", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		fixture := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"winner-mismatch",
			"doi:10.1000/publication-winner-mismatch",
		)
		input := catalogCurationInputAt(
			time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
		)
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		insertPublisherBiomedicalProjection(t, pool, fixture)
		alternateProjectionID := insertPublisherAlternateProjectionAssertion(
			t,
			pool,
			fixture,
			"projection/publication-decoy-v2",
		)
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				projectionAssertionID: alternateProjectionID,
				printState:            "missing",
				electronicState:       "missing",
				aheadState:            "missing",
				acceptedState:         "missing",
			},
		)

		_, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		)
		if !errors.Is(err, ErrCatalogNotReady) ||
			!strings.Contains(err.Error(), "publication state") {
			t.Fatalf(
				"PublishCurrent(publication winner mismatch) error = %v, want ErrCatalogNotReady",
				err,
			)
		}
		assertNoCatalogWrites(t, pool)
	})

	t.Run("known state lacks exact immutable event assertion", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		fixture := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"event-provenance-missing",
			"doi:10.1000/publication-event-provenance-missing",
			publisherPublicationHistoryEntry(
				"accepted", 2026, time.July, 17, "day",
				"/PubmedArticle/PubmedData/History/PubMedPubDate[1]", 1,
			),
		)
		input := catalogCurationInputAt(
			time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
		)
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		insertPublisherBiomedicalProjection(t, pool, fixture)
		acceptedDate := publicationUpdateDate(2026, time.July, 17)
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedDate:      &acceptedDate,
				acceptedState:     "known",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
			},
		)

		_, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		)
		if !errors.Is(err, ErrCatalogNotReady) ||
			!strings.Contains(err.Error(), "publication event") {
			t.Fatalf(
				"PublishCurrent(missing exact publication event) error = %v, want ErrCatalogNotReady",
				err,
			)
		}
		assertNoCatalogWrites(t, pool)
	})

	t.Run("event assertion publication model differs from exact state", func(t *testing.T) {
		pool := openCatalogTestPool(t)
		fixture := insertPublisherPublicationUpdateWork(
			t,
			pool,
			"event-model-mismatch",
			"doi:10.1000/publication-event-model-mismatch",
			publisherPublicationHistoryEntry(
				"ppublish", 2026, time.July, 17, "day",
				"/PubmedArticle/JournalIssue/PubDate", 1,
			),
		)
		input := catalogCurationInputAt(
			time.Date(2026, time.July, 18, 12, 0, 0, 0, time.UTC),
		)
		preparePublisherAcceptedCuration(t, pool, input, fixture)
		insertPublisherBiomedicalProjection(t, pool, fixture)
		printDate := publicationUpdateDate(2026, time.July, 17)
		insertPublisherPublicationState(
			t,
			pool,
			fixture,
			publisherPublicationStateFixture{
				printDate:         &printDate,
				printState:        "known",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
				events: []publisherPublicationEventFixture{{
					kind:       "print_published",
					date:       &printDate,
					precision:  "day",
					statusRaw:  "ppublish",
					modelRaw:   stringPointer("Decoy"),
					sourcePath: "/PubmedArticle/JournalIssue/PubDate",
					ordinal:    1,
				}},
			},
		)

		_, err := mustPublisher(t, pool).PublishCurrent(
			context.Background(),
			input,
		)
		if !errors.Is(err, ErrCatalogNotReady) ||
			!strings.Contains(err.Error(), "publication event assertion") {
			t.Fatalf(
				"PublishCurrent(publication event model mismatch) error = %v, want ErrCatalogNotReady",
				err,
			)
		}
		assertNoCatalogWrites(t, pool)
	})
}

func TestPublisherRejectsPublicationAssertionSetMismatchWithNormalizedHistory(
	t *testing.T,
) {
	normalizedDate := publisherPublicationDateFixture{
		Year:      2026,
		Month:     int(time.July),
		Day:       17,
		Precision: "day",
	}
	persistedDate := publicationUpdateDate(2026, time.July, 17)
	baseHistory := publisherPublicationHistoryFixture{
		Status:     "ppublish",
		Date:       normalizedDate,
		SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
		Ordinal:    1,
	}
	baseEvent := publisherPublicationEventFixture{
		kind:       "print_published",
		date:       &persistedDate,
		precision:  "day",
		statusRaw:  "ppublish",
		modelRaw:   stringPointer("Electronic"),
		sourcePath: baseHistory.SourcePath,
		ordinal:    1,
	}
	baseState := publisherPublicationStateFixture{
		printDate:         &persistedDate,
		printState:        "known",
		electronicState:   "missing",
		aheadState:        "missing",
		acceptedState:     "missing",
		publicationModel:  stringPointer("Electronic"),
		publicationStatus: stringPointer("epublish"),
	}

	tests := []struct {
		name              string
		history           []publisherPublicationHistoryFixture
		events            []publisherPublicationEventFixture
		state             publisherPublicationStateFixture
		publicationStatus string
	}{
		{
			name:    "event kind",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.kind = "accepted"
				return event
			}()},
			state: func() publisherPublicationStateFixture {
				state := baseState
				state.printDate = nil
				state.printState = "missing"
				state.acceptedDate = &persistedDate
				state.acceptedState = "known"
				return state
			}(),
			publicationStatus: "epublish",
		},
		{
			name:    "event date",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				changed := publicationUpdateDate(2026, time.July, 18)
				event.date = &changed
				event.sourceDate = `{"year":2026,"month":7,"day":17,"precision":"day"}`
				return event
			}()},
			state: func() publisherPublicationStateFixture {
				state := baseState
				changed := publicationUpdateDate(2026, time.July, 18)
				state.printDate = &changed
				return state
			}(),
			publicationStatus: "epublish",
		},
		{
			name:    "date precision",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.date = nil
				event.precision = "month"
				event.sourceDate = `{"year":2026,"month":7,"day":17,"precision":"day"}`
				return event
			}()},
			state: func() publisherPublicationStateFixture {
				state := baseState
				state.printDate = nil
				state.printState = "missing"
				return state
			}(),
			publicationStatus: "epublish",
		},
		{
			name:    "source date",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.sourceDate = `{"year":2026,"month":7,"day":18,"precision":"day"}`
				return event
			}()},
			state:             baseState,
			publicationStatus: "epublish",
		},
		{
			name:    "status",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.statusRaw = "accepted"
				return event
			}()},
			state:             baseState,
			publicationStatus: "epublish",
		},
		{
			name:    "source path",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.sourcePath = "/PubmedArticle/Decoy/PubMedPubDate[1]"
				return event
			}()},
			state:             baseState,
			publicationStatus: "epublish",
		},
		{
			name:    "ordinal",
			history: []publisherPublicationHistoryFixture{baseHistory},
			events: []publisherPublicationEventFixture{func() publisherPublicationEventFixture {
				event := baseEvent
				event.ordinal = 2
				return event
			}()},
			state:             baseState,
			publicationStatus: "epublish",
		},
		{
			name: "missing partial assertion",
			history: []publisherPublicationHistoryFixture{{
				Status: "aheadofprint",
				Date: publisherPublicationDateFixture{
					Year:      2026,
					Month:     int(time.July),
					Precision: "month",
				},
				SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				Ordinal:    1,
			}},
			state: publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
			},
			publicationStatus: "epublish",
		},
		{
			name:    "extra assertion for empty history",
			history: []publisherPublicationHistoryFixture{},
			events: []publisherPublicationEventFixture{{
				kind:       "print_published",
				precision:  "month",
				sourceDate: `{"year":2026,"month":7,"precision":"month"}`,
				statusRaw:  "ppublish",
				modelRaw:   stringPointer("Electronic"),
				sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				ordinal:    1,
			}},
			state: publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
			},
			publicationStatus: "epublish",
		},
		{
			name: "unknown status does not generate assertion",
			history: []publisherPublicationHistoryFixture{{
				Status: "received",
				Date: publisherPublicationDateFixture{
					Year:      2026,
					Month:     int(time.July),
					Precision: "month",
				},
				SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				Ordinal:    1,
			}},
			events: []publisherPublicationEventFixture{{
				kind:       "accepted",
				precision:  "month",
				sourceDate: `{"year":2026,"month":7,"precision":"month"}`,
				statusRaw:  "received",
				modelRaw:   stringPointer("Electronic"),
				sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				ordinal:    1,
			}},
			state: publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("epublish"),
			},
			publicationStatus: "epublish",
		},
		{
			name: "epublish requires top level epublish or ppublish",
			history: []publisherPublicationHistoryFixture{{
				Status: "epublish",
				Date: publisherPublicationDateFixture{
					Year:      2026,
					Month:     int(time.July),
					Precision: "month",
				},
				SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				Ordinal:    1,
			}},
			events: []publisherPublicationEventFixture{{
				kind:       "electronic_published",
				precision:  "month",
				sourceDate: `{"year":2026,"month":7,"precision":"month"}`,
				statusRaw:  "epublish",
				modelRaw:   stringPointer("Electronic"),
				sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				ordinal:    1,
			}},
			state: publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("accepted"),
			},
			publicationStatus: "accepted",
		},
		{
			name: "epublish is recognized for top level ppublish",
			history: []publisherPublicationHistoryFixture{{
				Status: "epublish",
				Date: publisherPublicationDateFixture{
					Year:      2026,
					Month:     int(time.July),
					Precision: "month",
				},
				SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
				Ordinal:    1,
			}},
			state: publisherPublicationStateFixture{
				printState:        "missing",
				electronicState:   "missing",
				aheadState:        "missing",
				acceptedState:     "missing",
				publicationModel:  stringPointer("Electronic"),
				publicationStatus: stringPointer("ppublish"),
			},
			publicationStatus: "ppublish",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := openCatalogTestPool(t)
			fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
				eventKey:                "pubmed:publication-history-mismatch-" + strings.ReplaceAll(test.name, " ", "-"),
				logicalSource:           "pubmed",
				canonicalKey:            "doi:10.1000/publication-history-mismatch-" + strings.ReplaceAll(test.name, " ", "-"),
				title:                   "Publication history mismatch " + test.name,
				paperType:               "research_article",
				publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
				sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
				scopeStatus:             "included",
				includeWorkLink:         true,
				includeWorkID:           true,
				includeNormalized:       true,
				normalizedPayloadSchema: "normalized-record/v4",
				publicationModel:        "Electronic",
				publicationStatus:       test.publicationStatus,
				publicationHistory:      test.history,
				noJCRAssessment:         true,
			})
			input := catalogCurationInput()
			preparePublisherAcceptedCuration(t, pool, input, fixture)
			insertPublisherBiomedicalProjection(t, pool, fixture)
			state := test.state
			state.events = test.events
			insertPublisherPublicationState(t, pool, fixture, state)

			_, err := mustPublisher(t, pool).PublishCurrent(
				context.Background(),
				input,
			)
			if !errors.Is(err, ErrCatalogNotReady) ||
				!strings.Contains(err.Error(), "publication event assertion") {
				t.Fatalf(
					"PublishCurrent(%s) error = %v, want publication assertion set ErrCatalogNotReady",
					test.name,
					err,
				)
			}
			assertNoCatalogWrites(t, pool)
		})
	}
}

func TestPublisherPreloadsPublicationEvidenceForMultipleWorks(t *testing.T) {
	pool := openCatalogTestPool(t)
	eventDate := publicationUpdateDate(2026, time.July, 17)
	event := publisherPublicationEventFixture{
		kind:       "print_published",
		date:       &eventDate,
		precision:  "day",
		statusRaw:  "ppublish",
		modelRaw:   stringPointer("Electronic"),
		sourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
		ordinal:    1,
	}
	v3 := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:                "pubmed:catalog-bulk-publication-v3",
		logicalSource:           "pubmed",
		canonicalKey:            "doi:10.1000/catalog-bulk-publication-v3",
		title:                   "Bulk publication evidence v3",
		paperType:               "research_article",
		publishedAt:             eventDate,
		sourceTime:              eventDate.Add(time.Hour),
		scopeStatus:             "included",
		includeWorkLink:         true,
		includeWorkID:           true,
		includeNormalized:       true,
		normalizedPayloadSchema: "normalized-record/v4",
		publicationModel:        "Electronic",
		publicationStatus:       "epublish",
		publicationHistory: publisherPublicationHistoryFromEvents(
			t,
			[]publisherPublicationEventFixture{event},
		),
		noJCRAssessment: true,
	})
	v2 := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:catalog-bulk-publication-v2",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/catalog-bulk-publication-v2",
		title:             "Bulk publication evidence v2",
		paperType:         "research_article",
		publishedAt:       eventDate.Add(-time.Hour),
		sourceTime:        eventDate.Add(2 * time.Hour),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInput()
	preparePublisherAcceptedCuration(t, pool, input, v3, v2)
	insertPublisherBiomedicalProjection(t, pool, v3)
	insertPublisherBiomedicalProjection(t, pool, v2)
	insertPublisherPublicationState(
		t,
		pool,
		v3,
		publisherPublicationStateFixture{
			printDate:         &eventDate,
			printState:        "known",
			electronicState:   "missing",
			aheadState:        "missing",
			acceptedState:     "missing",
			publicationModel:  stringPointer("Electronic"),
			publicationStatus: stringPointer("epublish"),
			events:            []publisherPublicationEventFixture{event},
		},
	)

	counter := &publicationEvidenceQueryCounter{}
	publisher, err := NewPublisher(publicationEvidenceCountingDatabase{
		database: pool,
		counter:  counter,
	})
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}
	if _, err := publisher.PublishCurrent(context.Background(), input); err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	if got := counter.count.Load(); got != 2 {
		t.Fatalf(
			"publication evidence SQL statements = %d, want exactly 2 bulk preload statements for two Works",
			got,
		)
	}
}

func TestPublisherRejectsV3WinnerWithoutPublicationState(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:                "pubmed:catalog-v3-no-publication-state",
		logicalSource:           "pubmed",
		canonicalKey:            "doi:10.1000/catalog-v3-no-publication-state",
		title:                   "V3 winner without publication state",
		paperType:               "research_article",
		publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
		scopeStatus:             "included",
		includeWorkLink:         true,
		includeWorkID:           true,
		includeNormalized:       true,
		normalizedPayloadSchema: "normalized-record/v4",
		publicationModel:        "Electronic",
		publicationStatus:       "epublish",
		noJCRAssessment:         true,
	})
	input := catalogCurationInput()
	preparePublisherAcceptedCuration(t, pool, input, fixture)
	insertPublisherBiomedicalProjection(t, pool, fixture)

	_, err := mustPublisher(t, pool).PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "publication state") {
		t.Fatalf(
			"PublishCurrent(v3 winner without publication state) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRejectsV1WinnerWithoutPublicationState(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:                "pubmed:catalog-v1-no-publication-state",
		logicalSource:           "pubmed",
		canonicalKey:            "doi:10.1000/catalog-v1-no-publication-state",
		title:                   "V1 winner without publication state",
		paperType:               "research_article",
		publishedAt:             time.Date(2026, time.July, 17, 8, 0, 0, 0, time.UTC),
		sourceTime:              time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
		scopeStatus:             "included",
		includeWorkLink:         true,
		includeWorkID:           true,
		includeNormalized:       true,
		normalizedPayloadSchema: "normalized-record/v1",
		noJCRAssessment:         true,
	})
	input := catalogCurationInput()
	preparePublisherAcceptedCuration(t, pool, input, fixture)
	insertPublisherBiomedicalProjection(t, pool, fixture)

	_, err := mustPublisher(t, pool).PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "publication state") {
		t.Fatalf(
			"PublishCurrent(v1 winner without publication state) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherPersistsBiomedicalCoverageMarkerBeforePublication(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-biomedical-coverage-marker",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-biomedical-coverage-marker",
		title:             "Biomedical coverage marker",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCuration(t, pool, input, fixture)

	generation, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	var markers int
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*)
		FROM public_catalog_biomedical_coverage
		WHERE generation_id = $1
	`, generation.ID).Scan(&markers); err != nil {
		t.Fatalf("query biomedical coverage marker: %v", err)
	}
	if markers != 1 {
		t.Fatalf(
			"biomedical coverage markers = %d, want exactly one for the published generation",
			markers,
		)
	}
}

func TestPublisherUsesOnlyTheExplicitCitationSource(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-citation-source",
		canonicalKey:      "doi:10.1000/publisher-citation-source",
		title:             "Source-specific citation evidence",
		publishedAt:       time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 9, 0, 0, 0, time.UTC),
	)
	input.CitationAnalysisRunID = uuid.New()
	preparePublisherAcceptedCurationReferences(t, pool, input, fixture)
	insertPublisherCitationSnapshot(t, pool, fixture, 42)
	insertCatalogCitationAnalysisRun(
		t,
		pool,
		input,
		fixture.workID,
	)
	insertCatalogSucceededAbstractRouteRun(t, pool, input, fixture)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO metric_snapshots (
			work_id,
			metric_name,
			metric_value,
			observed_at,
			source
		) VALUES (
			$1,
			'citation_count',
			999,
			'2026-07-17T08:00:00Z',
			'legacy-cross-source'
		)
	`, fixture.workID); err != nil {
		t.Fatalf("insert legacy ambiguous citation metric: %v", err)
	}

	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	); err != nil {
		t.Fatalf("PublishCurrent(openalex) error = %v", err)
	}
	document, err := mustRepository(t, pool).Paper(
		context.Background(),
		fixture.workID,
	)
	if err != nil {
		t.Fatalf("Paper(openalex citation source) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_source", "state"},
		"known",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_source", "value"},
		"openalex",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_count", "value"},
		float64(42),
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_snapshots", "value", "0", "source"},
		"openalex",
	)

	crossrefInput := input
	crossrefInput.CitationSource = "crossref"
	crossrefInput.GeneratedAt = input.GeneratedAt.Add(time.Hour)
	crossrefInput.CitationAnalysisRunID = uuid.New()
	crossrefInput.TrendAnalysisRunID = uuid.New()
	crossrefInput.JournalAnalysisRunID = uuid.New()
	crossrefInput.OpportunityAnalysisRunID = uuid.New()
	insertCatalogCitationAnalysisRun(
		t,
		pool,
		crossrefInput,
		fixture.workID,
	)
	insertCatalogBiomedicalAnalysisRuns(
		t,
		pool,
		crossrefInput,
		catalogBiomedicalCohortRevisionForFixtures(
			t,
			pool,
			crossrefInput,
			[]publisherWorkFixture{fixture},
		),
		crossrefInput.TrendAnalysisRunID,
	)
	if _, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		crossrefInput,
	); err != nil {
		t.Fatalf("PublishCurrent(crossref) error = %v", err)
	}
	crossrefDocument, err := mustRepository(t, pool).Paper(
		context.Background(),
		fixture.workID,
	)
	if err != nil {
		t.Fatalf("Paper(crossref citation source) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		crossrefDocument.Payload,
		[]string{"citation_source", "value"},
		"crossref",
	)
	assertNestedJSONValue(
		t,
		crossrefDocument.Payload,
		[]string{"citation_count", "state"},
		"missing",
	)
	assertNestedJSONValue(
		t,
		crossrefDocument.Payload,
		[]string{"citation_snapshots", "state"},
		"missing",
	)
}

func TestPublisherRejectsCitationVelocityWindowThatDiffersFromAnalysisRun(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:citation-window-mismatch",
		canonicalKey:      "doi:10.1000/citation-window-mismatch",
		title:             "Citation window mismatch",
		publishedAt:       time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.June, 15, 10, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	insertPublisherCitationSnapshot(t, pool, fixture, 10)
	insertPublisherAdditionalCitationSnapshot(
		t,
		pool,
		fixture,
		time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC),
		42,
	)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCurationReferences(t, pool, input, fixture)
	insertCatalogCitationAnalysisRunWithOptions(
		t,
		pool,
		input,
		catalogCitationAnalysisFixtureOptions{
			runVelocityWindowDays:          31,
			materializedVelocityWindowDays: 30,
			runMinimumCohortSize:           20,
		},
		fixture.workID,
	)
	insertCatalogSucceededAbstractRouteRun(t, pool, input, fixture)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "citation trend metadata conflicts") {
		t.Fatalf(
			"PublishCurrent(citation velocity window mismatch) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRejectsCitationPercentileMinimumThatDiffersFromAnalysisRun(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:citation-cohort-minimum-mismatch",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/citation-cohort-minimum-mismatch",
		title:             "Citation cohort minimum mismatch",
		paperType:         "research_article",
		publishedAt:       time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	insertPublisherBiomedicalProjection(t, pool, fixture)
	insertPublisherAdditionalCitationSnapshot(
		t,
		pool,
		fixture,
		time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC),
		42,
	)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCurationReferences(t, pool, input, fixture)
	materializedMinimum := 21
	insertCatalogCitationAnalysisRunWithOptions(
		t,
		pool,
		input,
		catalogCitationAnalysisFixtureOptions{
			runVelocityWindowDays:          30,
			materializedVelocityWindowDays: 30,
			runMinimumCohortSize:           20,
			materializedMinimumCohortSize:  &materializedMinimum,
		},
		fixture.workID,
	)
	insertCatalogSucceededAbstractRouteRun(t, pool, input, fixture)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "invalid materialized citation percentile evidence") {
		t.Fatalf(
			"PublishCurrent(citation cohort minimum mismatch) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherPublishesOnlyPositiveCitationMomentumFromBoundAnalysisRun(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	generatedAt := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	baselineAt := time.Date(2026, time.June, 15, 10, 0, 0, 0, time.UTC)
	currentAt := time.Date(2026, time.July, 17, 10, 0, 0, 0, time.UTC)
	fixtures := []publisherWorkFixture{
		insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "openalex:citation-momentum-fast",
			canonicalKey:      "doi:10.1000/citation-momentum-fast",
			title:             "Fast citation momentum",
			publishedAt:       time.Date(2026, time.July, 16, 8, 0, 0, 0, time.UTC),
			sourceTime:        baselineAt,
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
		}),
		insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "openalex:citation-momentum-slow",
			canonicalKey:      "doi:10.1000/citation-momentum-slow",
			title:             "Slow citation momentum",
			publishedAt:       time.Date(2026, time.July, 16, 7, 0, 0, 0, time.UTC),
			sourceTime:        baselineAt,
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
		}),
		insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          "openalex:citation-momentum-flat",
			canonicalKey:      "doi:10.1000/citation-momentum-flat",
			title:             "Flat citation momentum",
			publishedAt:       time.Date(2026, time.July, 16, 6, 0, 0, 0, time.UTC),
			sourceTime:        baselineAt,
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
		}),
	}
	for index, counts := range [][2]int64{
		{10, 42},
		{20, 30},
		{50, 50},
	} {
		insertPublisherCitationSnapshot(t, pool, fixtures[index], counts[0])
		insertPublisherAdditionalCitationSnapshot(
			t,
			pool,
			fixtures[index],
			currentAt,
			counts[1],
		)
	}

	input := catalogCurationInputAt(generatedAt)
	preparePublisherAcceptedCuration(t, pool, input, fixtures...)
	generation, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}

	repository := mustRepository(t, pool)
	trends, err := repository.Trends(
		context.Background(),
		TrendKindPapers,
		TrendQuery{WindowDays: 30, Limit: 20},
	)
	if err != nil {
		t.Fatalf("Trends(papers) error = %v", err)
	}
	if trends.Generation.ID != generation.ID {
		t.Fatalf(
			"Trends() generation = %s, want %s",
			trends.Generation.ID,
			generation.ID,
		)
	}
	if len(trends.Items) != 2 {
		t.Fatalf(
			"positive citation momentum count = %d, want 2; items=%s",
			len(trends.Items),
			trends.Items,
		)
	}
	assertNestedJSONValue(
		t,
		trends.Items[0],
		[]string{"paper", "id"},
		fixtures[0].workID.String(),
	)
	assertNestedJSONValue(t, trends.Items[0], []string{"rank"}, float64(1))
	assertNestedJSONValue(t, trends.Items[0], []string{"score"}, float64(1))
	assertNestedJSONValue(
		t,
		trends.Items[0],
		[]string{"ranking", "formula_version"},
		"citation-intelligence/v1",
	)
	assertNestedJSONValue(
		t,
		trends.Items[0],
		[]string{"ranking", "window_days"},
		float64(30),
	)
	assertNestedJSONValue(
		t,
		trends.Items[1],
		[]string{"paper", "id"},
		fixtures[1].workID.String(),
	)
	assertNestedJSONValue(t, trends.Items[1], []string{"score"}, float64(0.5))

	fast, err := repository.Paper(context.Background(), fixtures[0].workID)
	if err != nil {
		t.Fatalf("Paper(fast) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		fast.Payload,
		[]string{"trend_score", "state"},
		"known",
	)
	assertNestedJSONValue(
		t,
		fast.Payload,
		[]string{"trend_score", "value"},
		float64(1),
	)
	flat, err := repository.Paper(context.Background(), fixtures[2].workID)
	if err != nil {
		t.Fatalf("Paper(flat) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		flat.Payload,
		[]string{"trend_score", "state"},
		"missing",
	)

	home, err := repository.Home(context.Background())
	if err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "analysis", "formula_version", "value"},
		"citation-intelligence/v1",
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "analysis", "sources", "value", "0"},
		"openalex",
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "analysis", "coverage_ratio", "value"},
		float64(1),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "items", "0", "citation_delta", "value"},
		float64(32),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "items", "0", "cohort_percentile", "value"},
		float64(1),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"citation_momentum", "items", "1", "citation_delta", "value"},
		float64(10),
	)
}

func TestPublisherPublishesBiomedicalPayloadFromBoundProjection(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-biomedical",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-biomedical",
		title:             "Prospective oncology cohort",
		paperType:         "research_article",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCuration(t, pool, input, fixture)
	insertPublisherBiomedicalProjection(t, pool, fixture)

	generation, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	repository := mustRepository(t, pool)
	document, err := repository.Paper(
		context.Background(),
		fixture.workID,
	)
	if err != nil {
		t.Fatalf("Paper() error = %v", err)
	}

	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"journal", "title"},
		"Publisher Journal pubmed:publisher-biomedical",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"subjects", "0", "slug"},
		"oncology",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "state"},
		"known",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "descriptor_ui"},
		"D008175",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "label"},
		"Lung Neoplasms",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "is_major_topic"},
		true,
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "source_path"},
		"/PubmedArticle/MeshHeading[1]/DescriptorName",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "qualifiers", "0", "qualifier_ui"},
		"Q000401",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "qualifiers", "0", "label"},
		"therapy",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "qualifiers", "0", "is_major_topic"},
		false,
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"mesh_headings", "value", "0", "qualifiers", "0", "source_path"},
		"/PubmedArticle/MeshHeading[1]/QualifierName[1]",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"publication_types", "0"},
		"Randomized Controlled Trial",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"publication_types_state", "state"},
		"known",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"jcr_assessment", "state"},
		"known",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"jcr_assessment", "value", "decision"},
		"accepted",
	)
	for _, field := range []string{
		"citation_snapshots",
		"citation_percentile",
		"article_usage",
		"open_fulltext",
	} {
		assertNestedJSONValue(
			t,
			document.Payload,
			[]string{field, "state"},
			"missing",
		)
	}
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_velocity", "state"},
		"insufficient_evidence",
	)
	assertNestedJSONValue(
		t,
		document.Payload,
		[]string{"citation_velocity", "reason"},
		"citation velocity requires exact same-source boundary snapshots",
	)

	home, err := repository.Home(context.Background())
	if err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	if home.Generation.ID != generation.ID {
		t.Fatalf(
			"Home() generation = %s, want %s",
			home.Generation.ID,
			generation.ID,
		)
	}
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"catalog_generation"},
		generation.ID.String(),
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"latest_papers", "items", "0", "title"},
		"Prospective oncology cohort",
	)
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"coverage", "mesh_coverage_ratio", "value"},
		float64(1),
	)

	subjects, err := repository.Subjects(
		context.Background(),
		PageQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Subjects() error = %v", err)
	}
	if subjects.Generation.ID != generation.ID {
		t.Fatalf(
			"Subjects() generation = %s, want %s",
			subjects.Generation.ID,
			generation.ID,
		)
	}
	if len(subjects.Items) != 1 {
		t.Fatalf("Subjects() items = %d, want 1", len(subjects.Items))
	}
	assertNestedJSONValue(
		t,
		subjects.Items[0],
		[]string{"slug"},
		"oncology",
	)
	subject, err := repository.Subject(
		context.Background(),
		"oncology",
		PageQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Subject(oncology) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		subject.Payload,
		[]string{"mesh_distribution", "items", "0", "label"},
		"Lung Neoplasms",
	)
	assertNestedJSONValue(
		t,
		subject.Payload,
		[]string{"publication_type_distribution", "items", "0", "label"},
		"Randomized Controlled Trial",
	)

	journals, err := repository.Journals(
		context.Background(),
		PageQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Journals() error = %v", err)
	}
	if journals.Generation.ID != generation.ID {
		t.Fatalf(
			"Journals() generation = %s, want %s",
			journals.Generation.ID,
			generation.ID,
		)
	}
	if len(journals.Items) != 1 {
		t.Fatalf("Journals() items = %d, want 1", len(journals.Items))
	}
	assertNestedJSONValue(
		t,
		journals.Items[0],
		[]string{"jif", "value"},
		float64(12),
	)
	assertNestedJSONValue(
		t,
		journals.Items[0],
		[]string{"categories", "0", "name"},
		"Oncology",
	)
	journalSlug := "journal-" + strings.ReplaceAll(
		catalogWorkVenueID(t, pool, fixture.workID).String(),
		"-",
		"",
	)
	journal, err := repository.Journal(
		context.Background(),
		journalSlug,
		PageQuery{Limit: 20},
	)
	if err != nil {
		t.Fatalf("Journal(%s) error = %v", journalSlug, err)
	}
	assertNestedJSONValue(
		t,
		journal.Payload,
		[]string{"curation", "decision"},
		"accepted",
	)
	assertNestedJSONValue(
		t,
		journal.Payload,
		[]string{"recent_papers", "items", "0", "title"},
		"Prospective oncology cohort",
	)

	searchPage, err := repository.Papers(
		context.Background(),
		PaperListQuery{
			Query: "Lung Neoplasms",
			Sort:  PaperSortRelevance,
			Limit: 20,
		},
	)
	if err != nil {
		t.Fatalf("Papers(MeSH search) error = %v", err)
	}
	assertPaperIDs(t, searchPage.Items, fixture.workID)
}

func TestPublisherRejectsInconsistentExactJIFAcrossJCRCategories(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-inconsistent-jif",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-inconsistent-jif",
		title:             "Inconsistent JIF evidence",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE venues
		SET venue_type = 'journal',
		    issn_l = $2
		WHERE id = $1
	`, venueID, deterministicCatalogISSN(1400)); err != nil {
		t.Fatalf("prepare inconsistent-JIF Venue: %v", err)
	}
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	insertCatalogJCRBundle(
		t,
		pool,
		input,
		subjectRuleID,
		[]catalogJCRVenueFixture{{
			venueID:     venueID,
			index:       1400,
			withSubject: true,
			extraMetrics: []catalogJCRMetricFixture{{
				category: "Immunology",
				jif:      "15",
				quartile: "Q2",
			}},
		}},
	)
	assessCatalogVenuePolicy(t, pool, input)
	assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)
	insertCatalogBiomedicalAnalysisRuns(
		t,
		pool,
		input,
		catalogBiomedicalCohortRevisionForFixtures(
			t,
			pool,
			input,
			[]publisherWorkFixture{fixture},
		),
		input.TrendAnalysisRunID,
	)
	insertCatalogSucceededAbstractRouteRun(t, pool, input, fixture)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "inconsistent exact JIF") {
		t.Fatalf(
			"PublishCurrent(inconsistent exact JIF) error = %v, want ErrCatalogNotReady",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRejectsForgedAcceptedAssessmentForQ2HighJIF(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-forged-accepted",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-forged-accepted",
		title:             "Forged accepted JCR evidence",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE venues
		SET venue_type = 'journal',
		    issn_l = $2
		WHERE id = $1
	`, venueID, deterministicCatalogISSN(1450)); err != nil {
		t.Fatalf("prepare forged-assessment Venue: %v", err)
	}
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	insertCatalogJCRBundle(
		t,
		pool,
		input,
		subjectRuleID,
		[]catalogJCRVenueFixture{{
			venueID:     venueID,
			index:       1450,
			withSubject: true,
			primaryMetric: &catalogJCRMetricFixture{
				category: "Oncology",
				jif:      "20",
				quartile: "Q2",
			},
		}},
	)
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
	assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "does not satisfy JCR Q1") {
		t.Fatalf(
			"PublishCurrent(forged accepted assessment) error = %v, want exact JCR admission rejection",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRejectsAcceptedAssessmentWhoseEvidenceDiffersFromExactReceipt(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-mismatched-assessment-evidence",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-mismatched-assessment-evidence",
		title:             "Mismatched accepted JCR evidence",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE venues
		SET venue_type = 'journal',
		    issn_l = $2
		WHERE id = $1
	`, venueID, deterministicCatalogISSN(1460)); err != nil {
		t.Fatalf("prepare mismatched-assessment Venue: %v", err)
	}
	_, subjectRuleID := insertCatalogSubjectVersion(t, pool, input.SubjectVersion)
	insertCatalogJCRBundle(
		t,
		pool,
		input,
		subjectRuleID,
		[]catalogJCRVenueFixture{{
			venueID:     venueID,
			index:       1460,
			withSubject: true,
		}},
	)
	insertCatalogVenueAssessmentPayload(
		t,
		pool,
		input,
		venueID,
		input.VenuePolicyName,
		input.VenuePolicyVersion,
		input.JCRMetricYear,
		"accepted",
		`["jcr_q1"]`,
		fmt.Sprintf(`{
			"jcr_import_receipt_id":%q,
			"policy_version":"journal-all-q1/v2",
			"metric_year":%d,
			"venue_type":"journal",
			"categories":[{
				"category":"Oncology",
				"jif":"99",
				"quartile":"Q4",
				"status":"known",
				"source_name":"authorized-jcr"
			}]
		}`, input.JCRImportReceipt.String(), input.JCRMetricYear),
	)
	assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "assessment evidence does not match exact JCR receipt") {
		t.Fatalf(
			"PublishCurrent(mismatched accepted evidence) error = %v, want exact receipt mismatch",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherRejectsEligibilitySubjectEvidenceFromAnotherJCRReceipt(
	t *testing.T,
) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "pubmed:publisher-cross-receipt-subject",
		logicalSource:     "pubmed",
		canonicalKey:      "doi:10.1000/publisher-cross-receipt-subject",
		title:             "Cross-receipt Subject evidence",
		publishedAt:       time.Date(2026, time.July, 17, 6, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 17, 7, 0, 0, 0, time.UTC),
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		noJCRAssessment:   true,
	})
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC),
	)
	venueID := catalogWorkVenueID(t, pool, fixture.workID)
	if _, err := pool.Exec(context.Background(), `
		UPDATE venues
		SET venue_type = 'journal',
		    issn_l = $2
		WHERE id = $1
	`, venueID, deterministicCatalogISSN(1470)); err != nil {
		t.Fatalf("prepare cross-receipt Venue: %v", err)
	}
	_, subjectRules := insertCatalogSubjectVersionCategories(
		t,
		pool,
		input.SubjectVersion,
		map[string]string{
			"immunology": "Immunology",
			"oncology":   "Oncology",
		},
	)
	primaryMetricID := uuid.MustParse(
		"00000000-0000-4000-8000-000000000101",
	)
	primaryLinkID := uuid.MustParse(
		"00000000-0000-4000-8000-000000000102",
	)
	insertCatalogJCRBundle(
		t,
		pool,
		input,
		subjectRules["Oncology"],
		[]catalogJCRVenueFixture{{
			venueID:         venueID,
			index:           1470,
			withSubject:     true,
			primaryMetricID: &primaryMetricID,
			subjectLinkID:   &primaryLinkID,
		}},
	)
	insertCatalogCrossReceiptSubjectEvidence(
		t,
		pool,
		venueID,
		input.JCRMetricYear,
		subjectRules["Immunology"],
	)
	assessCatalogVenuePolicy(t, pool, input)
	assessCatalogBiomedicalEligibility(t, pool, fixture.workID, input)
	insertCatalogCitationAnalysisRun(t, pool, input, fixture.workID)
	insertCatalogBiomedicalAnalysisRuns(
		t,
		pool,
		input,
		catalogBiomedicalCohortRevisionForFixtures(
			t,
			pool,
			input,
			[]publisherWorkFixture{fixture},
		),
		input.TrendAnalysisRunID,
	)
	insertCatalogSucceededAbstractRouteRun(t, pool, input, fixture)

	_, err := mustPublisher(t, pool).PublishCurrent(
		context.Background(),
		input,
	)
	if !errors.Is(err, ErrCatalogNotReady) ||
		!strings.Contains(err.Error(), "Subject evidence does not belong to JCR receipt") {
		t.Fatalf(
			"PublishCurrent(cross-receipt Subject) error = %v, want exact receipt rejection",
			err,
		)
	}
	assertNoCatalogWrites(t, pool)
}

func insertCatalogCrossReceiptSubjectEvidence(
	t *testing.T,
	pool *pgxpool.Pool,
	venueID uuid.UUID,
	metricYear int,
	subjectRuleID uuid.UUID,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cross-receipt Subject fixture: %v", err)
	}
	defer tx.Rollback(context.Background())

	receiptID := uuid.MustParse("ffffffff-ffff-4fff-8fff-fffffffffff1")
	metricID := uuid.MustParse("ffffffff-ffff-4fff-8fff-fffffffffff2")
	linkID := uuid.MustParse("ffffffff-ffff-4fff-8fff-fffffffffff3")
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
			'authorized-jcr-secondary',
			$3,
			1,
			1,
			0
		)
	`,
		receiptID,
		strings.Repeat("c", 64),
		time.Date(2026, time.July, 17, 8, 31, 0, 0, time.UTC),
	); err != nil {
		t.Fatalf("insert secondary JCR receipt: %v", err)
	}
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
			'Immunology',
			12,
			'Q1',
			'known',
			'authorized-jcr-secondary',
			'institution-authorized',
			$4,
			$5
		)
	`,
		metricID,
		venueID,
		metricYear,
		time.Date(2026, time.July, 17, 8, 31, 0, 0, time.UTC),
		receiptID,
	); err != nil {
		t.Fatalf("insert secondary JCR metric: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO jcr_import_receipt_metrics (
			import_receipt_id,
			metric_snapshot_id
		) VALUES ($1, $2)
	`, receiptID, metricID); err != nil {
		t.Fatalf("link secondary JCR receipt metric: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO journal_subject_metrics (
			id,
			venue_metric_snapshot_id,
			subject_rule_id,
			jcr_category
		) VALUES ($1, $2, $3, 'Immunology')
	`, linkID, metricID, subjectRuleID); err != nil {
		t.Fatalf("insert secondary Subject metric link: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit cross-receipt Subject fixture: %v", err)
	}
}

func TestPublisherLoadsNormalizedPayloadByBoundAssertionID(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-normalized-binding",
		canonicalKey:      "doi:10.1000/publisher-normalized-binding",
		title:             "Bound normalized payload",
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
	})
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO ingestion_normalized_records (
			raw_event_id, source_record_uuid, normalization_policy_version,
			payload_schema_version, normalized_payload
		) VALUES (
			$1, $2, 'normalize/v1', 'normalized-record/v4',
			'{
				"source":"openalex",
				"source_record_id":"openalex:publisher-normalized-binding",
				"canonical_key":"doi:10.1000/publisher-normalized-binding",
				"title":"Unbound decoy payload"
			}'
		)
	`, fixture.rawEventID, fixture.sourceRecord); err != nil {
		t.Fatalf("insert unbound normalized assertion: %v", err)
	}

	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin normalized binding read: %v", err)
	}
	defer tx.Rollback(context.Background())
	states, err := loadCurrentSourceStates(context.Background(), tx)
	if err != nil {
		t.Fatalf("loadCurrentSourceStates() error = %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("current source states = %d, want one bound assertion", len(states))
	}
	payload, ok := states[0].normalizedPayload.(map[string]any)
	if !ok || payload["title"] != "Bound normalized payload" {
		t.Fatalf(
			"bound normalized payload = %#v, want the source-state assertion",
			states[0].normalizedPayload,
		)
	}
}

func TestPublisherPreservesKnownUnknownAndMissingWithoutInventingValues(t *testing.T) {
	pool := openCatalogTestPool(t)
	known := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-known",
		canonicalKey:      "doi:10.1000/publisher-known",
		title:             "Known Curation",
		paperType:         "review",
		jcrDecision:       "accepted",
		jcrMatchedRules:   `["jcr_q1"]`,
		jcrEvidence:       `{"venue_type":"journal","quartiles":["Q1"]}`,
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		publishedAt:       time.Date(2026, time.July, 14, 8, 0, 0, 0, time.UTC),
		sourceTime:        time.Date(2026, time.July, 14, 9, 0, 0, 0, time.UTC),
	})
	unknown := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-unknown",
		canonicalKey:      "doi:10.1000/publisher-unknown",
		title:             "Unknown Curation",
		jcrDecision:       "unknown",
		jcrMatchedRules:   `[]`,
		jcrEvidence:       `{"venue_type":"journal","reason":"metric_unknown"}`,
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC),
	})
	missing := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-missing",
		canonicalKey:      "doi:10.1000/publisher-missing",
		title:             "Missing Curation",
		noJCRAssessment:   true,
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 14, 11, 0, 0, 0, time.UTC),
	})

	publisher := mustPublisher(t, pool)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	)
	factsInput := input
	factsInput.Mode = PublishFacts
	preparePublisherAcceptedCurationWithoutBiomedicalRuns(
		t,
		pool,
		factsInput,
		known,
		unknown,
		missing,
	)
	ensurePublisherClassifierAssertions(t, pool, known, unknown, missing)
	insertCatalogCitationAnalysisRun(
		t,
		pool,
		input,
		known.workID,
		unknown.workID,
		missing.workID,
	)
	cohortRevision := catalogBiomedicalCohortRevisionForFixtures(
		t,
		pool,
		input,
		[]publisherWorkFixture{known},
	)
	insertCatalogBiomedicalAnalysisRuns(
		t,
		pool,
		input,
		cohortRevision,
		input.TrendAnalysisRunID,
	)
	insertCatalogSucceededAbstractRouteRun(t, pool, input, known)
	if _, err := publisher.PublishCurrent(context.Background(), input); err != nil {
		t.Fatalf("PublishCurrent() error = %v", err)
	}
	repository := mustRepository(t, pool)

	for _, test := range []struct {
		name string
		id   uuid.UUID
	}{
		{"known", known.workID},
		{"unknown", unknown.workID},
		{"missing", missing.workID},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, err := repository.Paper(context.Background(), test.id)
			if err != nil {
				t.Fatalf("Paper(%s) error = %v", test.id, err)
			}
			assertNestedJSONValue(
				t,
				document.Payload,
				[]string{"curation", "state"},
				"known",
			)
		})
	}

	missingDocument, err := repository.Paper(context.Background(), missing.workID)
	if err != nil {
		t.Fatalf("Paper(missing evidence) error = %v", err)
	}
	assertNestedJSONValue(
		t,
		missingDocument.Payload,
		[]string{"published_at", "state"},
		"missing",
	)
	assertNestedJSONValue(
		t,
		missingDocument.Payload,
		[]string{"has_code", "state"},
		"unknown",
	)
}

func TestPublisherReturnsEmptyDomainWithoutWritingGeneration(t *testing.T) {
	pool := openCatalogTestPool(t)
	insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-excluded",
		canonicalKey:      "doi:10.1000/publisher-excluded",
		title:             "Excluded Work",
		scopeStatus:       "excluded",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
	})
	publisher := mustPublisher(t, pool)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherEmptyCurationReferences(t, pool, input)

	_, err := publisher.PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrEmptyDomain) {
		t.Fatalf("PublishCurrent() error = %v, want ErrEmptyDomain", err)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherReturnsNotReadyForPendingOrBrokenIncludedStateWithoutWrites(t *testing.T) {
	tests := []struct {
		name    string
		options publisherWorkOptions
	}{
		{
			name: "pending current source state",
			options: publisherWorkOptions{
				eventKey:          "openalex:publisher-pending",
				canonicalKey:      "doi:10.1000/publisher-pending",
				title:             "Pending Work",
				scopeStatus:       "pending",
				includeWorkLink:   true,
				includeWorkID:     true,
				includeNormalized: true,
				sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "included state without work ID",
			options: publisherWorkOptions{
				eventKey:          "openalex:publisher-no-work",
				canonicalKey:      "doi:10.1000/publisher-no-work",
				title:             "No Work Link",
				scopeStatus:       "included",
				includeWorkLink:   false,
				includeWorkID:     false,
				includeNormalized: true,
				sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pool := openCatalogTestPool(t)
			insertPublisherVisibleWork(t, pool, test.options)
			publisher := mustPublisher(t, pool)
			input := catalogCurationInputAt(
				time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
			)
			preparePublisherEmptyCurationReferences(t, pool, input)

			_, err := publisher.PublishCurrent(context.Background(), input)
			if !errors.Is(err, ErrCatalogNotReady) {
				t.Fatalf("PublishCurrent() error = %v, want ErrCatalogNotReady", err)
			}
			assertNoCatalogWrites(t, pool)
		})
	}
}

func TestPublisherRejectsDeterministicSlugCollisionsWithoutPartialPublication(t *testing.T) {
	pool := openCatalogTestPool(t)
	var fixtures []publisherWorkFixture
	for index, topic := range []string{"Agent Systems", "Agent-Systems"} {
		fixtures = append(fixtures, insertPublisherVisibleWork(t, pool, publisherWorkOptions{
			eventKey:          fmt.Sprintf("openalex:publisher-collision-%d", index),
			canonicalKey:      fmt.Sprintf("doi:10.1000/publisher-collision-%d", index),
			title:             fmt.Sprintf("Collision Work %d", index),
			topicNames:        []string{topic},
			scopeStatus:       "included",
			includeWorkLink:   true,
			includeWorkID:     true,
			includeNormalized: true,
			sourceTime:        time.Date(2026, time.July, 15, 9+index, 0, 0, 0, time.UTC),
		}))
	}
	publisher := mustPublisher(t, pool)
	input := catalogCurationInputAt(
		time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCuration(t, pool, input, fixtures...)

	_, err := publisher.PublishCurrent(context.Background(), input)
	if !errors.Is(err, ErrCatalogNotReady) {
		t.Fatalf("PublishCurrent() error = %v, want ErrCatalogNotReady", err)
	}
	assertNoCatalogWrites(t, pool)
}

func TestPublisherReusesOnlyIdenticalImmutableGeneration(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:          "openalex:publisher-retry",
		canonicalKey:      "doi:10.1000/publisher-retry",
		title:             "Retry Work",
		scopeStatus:       "included",
		includeWorkLink:   true,
		includeWorkID:     true,
		includeNormalized: true,
		sourceTime:        time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC),
	})
	publisher := mustPublisher(t, pool)
	firstInput := catalogCurationInputAt(
		time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC),
	)
	preparePublisherAcceptedCuration(t, pool, firstInput, fixture)

	first, err := publisher.PublishCurrent(context.Background(), firstInput)
	if err != nil {
		t.Fatalf("PublishCurrent(first) error = %v", err)
	}
	second, err := publisher.PublishCurrent(context.Background(), firstInput)
	if err != nil {
		t.Fatalf("PublishCurrent(second) error = %v", err)
	}
	if second.ID != first.ID || second.SourceRevision != first.SourceRevision {
		t.Fatalf("retry generation = %#v, want original %#v", second, first)
	}

	var generations, publications int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM public_catalog_generations),
			(SELECT count(*) FROM public_catalog_publications)
	`).Scan(&generations, &publications); err != nil {
		t.Fatalf("count idempotent publication rows: %v", err)
	}
	if generations != 1 || publications != 1 {
		t.Fatalf(
			"idempotent publication rows = generations %d publications %d, want 1/1",
			generations,
			publications,
		)
	}

	changedInput := firstInput
	changedInput.GeneratedAt = time.Date(2026, time.July, 15, 13, 0, 0, 0, time.UTC)
	changed, err := publisher.PublishCurrent(context.Background(), changedInput)
	if err != nil {
		t.Fatalf("PublishCurrent(changed generated_at) error = %v", err)
	}
	if changed.ID == first.ID || changed.SourceRevision == first.SourceRevision {
		t.Fatalf(
			"changed generated_at generation = %#v, want a new immutable snapshot after %#v",
			changed,
			first,
		)
	}

	home, err := mustRepository(t, pool).Home(context.Background())
	if err != nil {
		t.Fatalf("Home(after changed generated_at) error = %v", err)
	}
	if home.Generation.ID != changed.ID {
		t.Fatalf(
			"Home() generation = %s, want changed generation %s",
			home.Generation.ID,
			changed.ID,
		)
	}
	assertNestedJSONValue(
		t,
		home.Payload,
		[]string{"generated_at"},
		changedInput.GeneratedAt.Format(time.RFC3339Nano),
	)

	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM public_catalog_generations),
			(SELECT count(*) FROM public_catalog_publications)
	`).Scan(&generations, &publications); err != nil {
		t.Fatalf("count changed publication rows: %v", err)
	}
	if generations != 2 || publications != 2 {
		t.Fatalf(
			"changed publication rows = generations %d publications %d, want 2/2",
			generations,
			publications,
		)
	}
}

type publisherWorkOptions struct {
	eventKey                   string
	logicalSource              string
	canonicalKey               string
	title                      string
	topicNames                 []string
	methodNames                []string
	withCode                   bool
	withData                   bool
	withBenchmark              bool
	paperType                  string
	jcrDecision                string
	jcrMatchedRules            string
	jcrEvidence                string
	noJCRAssessment            bool
	publishedAt                time.Time
	sourceTime                 time.Time
	scopeStatus                string
	includeWorkLink            bool
	includeWorkID              bool
	includeNormalized          bool
	normalizedPayloadSchema    string
	includePublicationMetadata bool
	publicationModel           string
	publicationStatus          string
	publicationHistory         []publisherPublicationHistoryFixture
	withoutOfficialLink        bool
	officialURL                string
	officialLinkProjectedAt    time.Time
	officialLinkExpiresAt      time.Time
	officialLinkRole           string
	contentChannel             scope.ContentChannel
	lifecycleState             scope.LifecycleState
	venueType                  string
	projectionPolicyVersion    string
}

type publisherPublicationDateFixture struct {
	Year      int    `json:"year"`
	Month     int    `json:"month,omitempty"`
	Day       int    `json:"day,omitempty"`
	Precision string `json:"precision"`
}

type publisherPublicationHistoryFixture struct {
	Status     string                          `json:"status"`
	Date       publisherPublicationDateFixture `json:"date"`
	SourcePath string                          `json:"source_path"`
	Ordinal    int                             `json:"ordinal"`
}

func publisherPublicationHistoryEntry(
	status string,
	year int,
	month time.Month,
	day int,
	precision string,
	sourcePath string,
	ordinal int,
) publisherPublicationHistoryFixture {
	return publisherPublicationHistoryFixture{
		Status: status,
		Date: publisherPublicationDateFixture{
			Year:      year,
			Month:     int(month),
			Day:       day,
			Precision: precision,
		},
		SourcePath: sourcePath,
		Ordinal:    ordinal,
	}
}

type publisherWorkFixture struct {
	workID                 uuid.UUID
	sourceRecord           uuid.UUID
	rawEventID             uuid.UUID
	normalizedAssertionID  uuid.UUID
	projectionAssertionID  uuid.UUID
	sourceName             string
	sourceRecordExternalID string
	parserVersion          string
	sourceTime             time.Time
	title                  string
	abstract               string
	officialVerificationID uuid.UUID
	officialVerifiedAt     time.Time
	officialExpiresAt      time.Time
}

func insertPublisherAnalysisWork(
	t *testing.T,
	pool *pgxpool.Pool,
	slug string,
	eventAt time.Time,
) publisherWorkFixture {
	t.Helper()

	event := publisherPublicationEventFixture{
		kind:       "electronic_published",
		date:       visibilityTimePointer(eventAt),
		precision:  "day",
		statusRaw:  "epublish",
		modelRaw:   visibilityStringPointer("electronic"),
		sourcePath: "$.publication_history[0]",
		ordinal:    1,
	}
	fixture := insertPublisherVisibleWork(t, pool, publisherWorkOptions{
		eventKey:                   "pubmed:" + slug,
		logicalSource:              "pubmed",
		canonicalKey:               "doi:10.1000/" + slug,
		title:                      "Analysis Work " + slug,
		topicNames:                 []string{"Topic " + slug},
		methodNames:                []string{"Method " + slug},
		paperType:                  "research_article",
		publishedAt:                eventAt,
		sourceTime:                 time.Date(2026, time.July, 16, 9, 0, 0, 0, time.UTC),
		scopeStatus:                "included",
		includeWorkLink:            true,
		includeWorkID:              true,
		includeNormalized:          true,
		normalizedPayloadSchema:    "normalized-record/v4",
		includePublicationMetadata: true,
		publicationModel:           "electronic",
		publicationStatus:          "epublish",
		publicationHistory: publisherPublicationHistoryFromEvents(
			t,
			[]publisherPublicationEventFixture{event},
		),
		noJCRAssessment: true,
	})
	insertPublisherPublicationState(
		t,
		pool,
		fixture,
		publisherPublicationStateFixture{
			printState:       "missing",
			electronicDate:   visibilityTimePointer(eventAt),
			electronicState:  "known",
			aheadState:       "missing",
			acceptedState:    "missing",
			publicationModel: visibilityStringPointer("electronic"),
			publicationStatus: visibilityStringPointer(
				"epublish",
			),
			events: []publisherPublicationEventFixture{event},
		},
	)
	return fixture
}

func preparePublisherAnalysisReferencesForCohort(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	publicFixtures []publisherWorkFixture,
	cohortFixtures []publisherWorkFixture,
) {
	t.Helper()

	factsInput := input
	factsInput.Mode = PublishFacts
	preparePublisherAcceptedCurationWithoutBiomedicalRuns(
		t,
		pool,
		factsInput,
		publicFixtures...,
	)
	cohortRevision := catalogBiomedicalCohortRevisionForFixtures(
		t,
		pool,
		input,
		cohortFixtures,
	)
	insertCatalogBiomedicalPublicationFixtures(
		t,
		pool,
		input,
		cohortRevision,
		cohortRevision,
		input.TrendAnalysisRunID,
		cohortFixtures[0],
	)
	workIDs := make([]uuid.UUID, 0, len(publicFixtures))
	for _, fixture := range publicFixtures {
		workIDs = append(workIDs, fixture.workID)
	}
	insertCatalogCitationAnalysisRun(t, pool, input, workIDs...)
}

func visibilityStringPointer(value string) *string {
	return &value
}

func insertPublisherVisibleWork(
	t *testing.T,
	pool *pgxpool.Pool,
	options publisherWorkOptions,
) publisherWorkFixture {
	t.Helper()

	if options.eventKey == "" {
		options.eventKey = "openalex:" + uuid.NewString()
	}
	if options.logicalSource == "" {
		options.logicalSource = "openalex"
	}
	if options.canonicalKey == "" {
		options.canonicalKey = "openalex:W" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if options.title == "" {
		options.title = "Publisher Work"
	}
	if options.sourceTime.IsZero() {
		options.sourceTime = time.Date(2026, time.July, 15, 9, 0, 0, 0, time.UTC)
	}
	if options.scopeStatus == "" {
		options.scopeStatus = "included"
	}
	if options.normalizedPayloadSchema == "" {
		options.normalizedPayloadSchema = "normalized-record/v2"
	}
	if options.projectionPolicyVersion == "" {
		options.projectionPolicyVersion =
			catalogClassifierProjectionPolicyVersion
	}
	if options.contentChannel == "" {
		options.contentChannel = scope.ContentChannelJournalPublished
	}
	if options.lifecycleState == "" {
		switch options.contentChannel {
		case scope.ContentChannelJournalPublished:
			options.lifecycleState = scope.LifecycleStatePublished
		case scope.ContentChannelAcceptedEarly:
			options.lifecycleState = scope.LifecycleStateAcceptedEarly
		case scope.ContentChannelPreprint:
			options.lifecycleState = scope.LifecycleStatePreprintActive
		case scope.ContentChannelConferenceProceeding:
			options.lifecycleState = scope.LifecycleStateConferencePublished
		}
	}
	if options.venueType == "" {
		switch options.contentChannel {
		case scope.ContentChannelPreprint:
			options.venueType = "preprint"
		case scope.ContentChannelConferenceProceeding:
			options.venueType = "conference"
		default:
			options.venueType = "journal"
		}
	}

	fixture := publisherWorkFixture{
		workID:                 uuid.New(),
		sourceRecord:           uuid.New(),
		rawEventID:             uuid.New(),
		sourceName:             options.logicalSource,
		sourceRecordExternalID: options.eventKey,
		parserVersion:          "publisher-parser/v1",
		sourceTime:             options.sourceTime,
		title:                  options.title,
		abstract:               "Source-backed abstract",
	}
	jobID := uuid.New()
	venueID := uuid.New()
	ctx := context.Background()
	hash := sha256.Sum256([]byte(options.eventKey))
	contentHash := hex.EncodeToString(hash[:])

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin publisher fixture transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_jobs (
			id, source, job_type, idempotency_key, status, payload, max_attempts,
			batch_key, stage
		) VALUES (
			$1, $3, 'sync', $2, 'succeeded', '{}', 1, $2, 'project'
		)
	`, jobID, "publisher:"+options.eventKey, options.logicalSource); err != nil {
		t.Fatalf("insert publisher ingestion job: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_raw_events (
			id, job_id, logical_source, event_key, event_kind, source_record_id,
			source_time, tie_break_key, position, content_hash, raw_format, raw_payload
		) VALUES (
			$1, $2, $8, $3, 'upsert', $4, $5, 'publisher-fixture', 1,
			$6, 'json', $7
		)
	`,
		fixture.rawEventID,
		jobID,
		options.eventKey,
		options.eventKey,
		options.sourceTime,
		contentHash,
		[]byte(`{"fixture":"publisher"}`),
		options.logicalSource,
	); err != nil {
		t.Fatalf("insert publisher raw event: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_records (
			id, source, source_record_id, source_identity, source_time,
			content_hash, raw_payload
		) VALUES (
			$1, $6, $2, $3::jsonb, $4, $5, '{"fixture":"publisher"}'
		)
	`,
		fixture.sourceRecord,
		options.eventKey,
		fmt.Sprintf(`{"canonical_key":%q}`, options.canonicalKey),
		options.sourceTime,
		contentHash,
		options.logicalSource,
	); err != nil {
		t.Fatalf("insert publisher source record: %v", err)
	}
	if options.includeNormalized {
		normalizedPayloadMap := map[string]any{
			"source":           options.logicalSource,
			"source_record_id": options.eventKey,
			"canonical_key":    options.canonicalKey,
			"title":            options.title,
		}
		if options.normalizedPayloadSchema == "normalized-record/v4" {
			normalizedPayloadMap["parser_version"] = fixture.parserVersion
			normalizedPayloadMap["abstract"] = fixture.abstract
		}
		if options.includePublicationMetadata || options.publicationModel != "" {
			normalizedPayloadMap["publication_model"] = options.publicationModel
		}
		if options.includePublicationMetadata || options.publicationStatus != "" {
			normalizedPayloadMap["publication_status"] = options.publicationStatus
		}
		if options.normalizedPayloadSchema == "normalized-record/v4" {
			history := options.publicationHistory
			if history == nil {
				history = []publisherPublicationHistoryFixture{}
			}
			normalizedPayloadMap["publication_history"] = history
		}
		normalizedPayload, err := json.Marshal(normalizedPayloadMap)
		if err != nil {
			t.Fatalf("encode publisher normalized payload: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO ingestion_normalized_records (
				raw_event_id, source_record_uuid, normalization_policy_version,
				payload_schema_version, normalized_payload
			) VALUES ($1, $2, 'normalize/v1', $3, $4::jsonb)
			RETURNING id
		`,
			fixture.rawEventID,
			fixture.sourceRecord,
			options.normalizedPayloadSchema,
			normalizedPayload,
		).Scan(
			&fixture.normalizedAssertionID,
		); err != nil {
			t.Fatalf("insert publisher normalized record: %v", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO venues (
			id, venue_type, display_title, source_scheme, source_identifier
		) VALUES ($1, $2, $3, 'openalex', $4)
	`,
		venueID,
		options.venueType,
		"Publisher Journal "+options.eventKey,
		"V"+strings.ReplaceAll(fixture.workID.String(), "-", ""),
	); err != nil {
		t.Fatalf("insert publisher venue: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO works (
			id, canonical_key, status, title, abstract, published_at, venue_id
		) VALUES (
			$1, $2, 'active', $3, 'Source-backed abstract',
			NULLIF($4, '0001-01-01 00:00:00+00'::timestamptz), $5
		)
	`,
		fixture.workID,
		options.canonicalKey,
		options.title,
		options.publishedAt,
		venueID,
	); err != nil {
		t.Fatalf("insert publisher work: %v", err)
	}
	if options.includeWorkLink {
		if _, err := tx.Exec(ctx, `
			INSERT INTO source_record_works (source_record_id, work_id)
			VALUES ($1, $2)
		`, fixture.sourceRecord, fixture.workID); err != nil {
			t.Fatalf("insert publisher source/work association: %v", err)
		}
	}
	if options.includeWorkLink &&
		options.includeWorkID &&
		options.includeNormalized {
		journalPolicyVersion := any(nil)
		channelRegistryVersion := any(nil)
		switch options.contentChannel {
		case scope.ContentChannelJournalPublished,
			scope.ContentChannelAcceptedEarly:
			journalPolicyVersion = scope.JournalAllQ1PolicyVersion
		case scope.ContentChannelPreprint:
			channelRegistryVersion = scope.PreprintRegistryVersion
		case scope.ContentChannelConferenceProceeding:
			channelRegistryVersion = scope.ConferenceRegistryVersion
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_lifecycle_states (
				work_id,
				channel,
				lifecycle_state,
				source_record_id,
				source_path,
				policy_version,
				evidence,
				decided_at
			) VALUES (
				$1,
				$2,
				$3,
				$4,
				'$.publication_status',
				'lifecycle-projection/v1',
				'{"assertion_ids":["publisher-fixture"]}'::jsonb,
				$5
			)
		`,
			fixture.workID,
			options.contentChannel,
			options.lifecycleState,
			fixture.sourceRecord,
			options.sourceTime,
		); err != nil {
			t.Fatalf("insert publisher lifecycle state: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_channel_admission_decisions (
				work_id,
				channel,
				decision,
				reason,
				source_record_id,
				source_path,
				admission_policy_version,
				domain_registry_version,
				journal_policy_version,
				channel_registry_version,
				evidence,
				decided_at
			) VALUES (
				$1,
				$3,
				'accepted',
				'eligible',
				$2,
				'$.venue_assessment',
				'channel-admission/v1',
				'research-domains-jcr-subjects/v2',
				$4,
				$5,
				'{"source_paths":["$.venue_assessment"]}'::jsonb,
				$6
			)
		`,
			fixture.workID,
			fixture.sourceRecord,
			options.contentChannel,
			journalPolicyVersion,
			channelRegistryVersion,
			options.sourceTime,
		); err != nil {
			t.Fatalf("insert publisher admission decision: %v", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_source_states (
			logical_source, event_key, raw_event_id, source_record_uuid, work_id,
			source_time, tie_break_key, position, scope_status,
			scope_policy_version, projection_policy_version,
			normalized_assertion_id, is_deleted
		) VALUES (
			$8, $1, $2, $3, NULLIF($4, '')::uuid, $5,
			'publisher-fixture', 1, $6, 'scope/v1', $9,
			NULLIF($7, '')::uuid, false
		)
	`,
		options.eventKey,
		fixture.rawEventID,
		fixture.sourceRecord,
		optionalUUID(options.includeWorkID, fixture.workID),
		options.sourceTime,
		options.scopeStatus,
		optionalUUID(options.includeNormalized, fixture.normalizedAssertionID),
		options.logicalSource,
		options.projectionPolicyVersion,
	); err != nil {
		t.Fatalf("insert publisher source state: %v", err)
	}
	if options.includeWorkLink &&
		options.includeWorkID &&
		options.includeNormalized {
		if err := tx.QueryRow(ctx, `
			INSERT INTO ingestion_projection_assertions (
				raw_event_id,
				normalized_assertion_id,
				source_record_uuid,
				work_id,
				job_id,
				scope_policy_version,
				projection_policy_version,
				record_payload
			) VALUES (
				$1, $2, $3, $4, $5,
				'scope/v1', $6, '{"fixture":"publisher-winner"}'
			)
			RETURNING id
		`,
			fixture.rawEventID,
			fixture.normalizedAssertionID,
			fixture.sourceRecord,
			fixture.workID,
			jobID,
			options.projectionPolicyVersion,
		).Scan(&fixture.projectionAssertionID); err != nil {
			t.Fatalf("insert publisher projection winner assertion: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_projection_states (
				work_id,
				raw_event_id,
				normalized_assertion_id,
				source_record_uuid,
				source_time,
				tie_break_key,
				position,
				scope_policy_version,
				projection_policy_version
			)
			SELECT
				$1,
				raw_event_id,
				normalized_assertion_id,
				source_record_uuid,
				source_time,
				tie_break_key,
				position,
				scope_policy_version,
				projection_policy_version
			FROM ingestion_source_states
			WHERE work_id = $1
		`, fixture.workID); err != nil {
			t.Fatalf("insert publisher projection winner state: %v", err)
		}
		if !options.publishedAt.IsZero() {
			eventKind := contenttruth.ChannelEventOfficialOnline
			switch options.contentChannel {
			case scope.ContentChannelAcceptedEarly:
				eventKind = contenttruth.ChannelEventAccepted
			case scope.ContentChannelPreprint:
				eventKind = contenttruth.ChannelEventPreprintPosted
			case scope.ContentChannelConferenceProceeding:
				eventKind =
					contenttruth.ChannelEventProceedingPublished
			}
			assertionID := uuid.New()
			const sourcePath = "$.published_at"
			if _, err := tx.Exec(ctx, `
				INSERT INTO work_channel_event_assertions (
					id,
					projection_assertion_id,
					normalized_assertion_id,
					source_record_id,
					work_id,
					channel,
					event_kind,
					event_at,
					source_path,
					asserted_at
				) VALUES (
					$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
				)
			`,
				assertionID,
				fixture.projectionAssertionID,
				fixture.normalizedAssertionID,
				fixture.sourceRecord,
				fixture.workID,
				options.contentChannel,
				eventKind,
				options.publishedAt.UTC(),
				sourcePath,
				options.sourceTime.UTC(),
			); err != nil {
				t.Fatalf(
					"insert publisher canonical channel event assertion: %v",
					err,
				)
			}
		}
	}

	if options.includeWorkLink {
		assertions := map[string]map[string]any{
			"published_at": {
				"state": stateForTime(options.publishedAt),
				"value": optionalTimeValue(options.publishedAt),
			},
			"abstract": {
				"state": "known",
				"value": "Source-backed abstract",
			},
			"code_urls": {
				"state": boolState(options.withCode),
				"value": optionalStringArray(
					options.withCode,
					"https://github.com/example/project",
				),
			},
		}
		if options.paperType != "" {
			assertions["paper_type"] = map[string]any{
				"state": "known",
				"value": options.paperType,
			}
		}
		for name, value := range assertions {
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("encode publisher assertion %s: %v", name, err)
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO field_assertions (
					work_id, source_record_id, field_name, asserted_value, asserted_at
				) VALUES ($1, $2, $3, $4, $5)
			`,
				fixture.workID,
				fixture.sourceRecord,
				name,
				encoded,
				options.sourceTime,
			); err != nil {
				t.Fatalf("insert publisher assertion %s: %v", name, err)
			}
		}
	}

	authorID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO authors (id, display_name) VALUES ($1, 'Ada Lovelace')
	`, authorID); err != nil {
		t.Fatalf("insert publisher author: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_authors (work_id, author_id, author_position)
		VALUES ($1, $2, 1)
	`, fixture.workID, authorID); err != nil {
		t.Fatalf("associate publisher author: %v", err)
	}
	for _, name := range options.topicNames {
		topicID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO topics (id, name) VALUES ($1, $2)
		`, topicID, name); err != nil {
			t.Fatalf("insert publisher topic %q: %v", name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_topics (work_id, topic_id, source_record_id)
			VALUES ($1, $2, $3)
		`, fixture.workID, topicID, fixture.sourceRecord); err != nil {
			t.Fatalf("associate publisher topic %q: %v", name, err)
		}
	}
	for _, name := range options.methodNames {
		methodID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO methods (id, name) VALUES ($1, $2)
		`, methodID, name); err != nil {
			t.Fatalf("insert publisher method %q: %v", name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_methods (work_id, method_id, source_record_id)
			VALUES ($1, $2, $3)
		`, fixture.workID, methodID, fixture.sourceRecord); err != nil {
			t.Fatalf("associate publisher method %q: %v", name, err)
		}
	}
	if options.withCode {
		repositoryID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO code_repositories (
				id, canonical_url, host, owner_name, repository_name
			) VALUES ($1, $2, 'github.com', 'example', $3)
		`,
			repositoryID,
			"https://github.com/example/"+fixture.workID.String(),
			fixture.workID.String(),
		); err != nil {
			t.Fatalf("insert publisher code repository: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_code_repositories (work_id, repository_id, relation_type)
			VALUES ($1, $2, 'official')
		`, fixture.workID, repositoryID); err != nil {
			t.Fatalf("associate publisher code repository: %v", err)
		}
	}
	if options.withData {
		datasetID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO datasets (id, name) VALUES ($1, $2)
		`, datasetID, "Dataset "+fixture.workID.String()); err != nil {
			t.Fatalf("insert publisher dataset: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_datasets (work_id, dataset_id, relation_type)
			VALUES ($1, $2, 'used')
		`, fixture.workID, datasetID); err != nil {
			t.Fatalf("associate publisher dataset: %v", err)
		}
	}
	if options.withBenchmark {
		benchmarkID := uuid.New()
		if _, err := tx.Exec(ctx, `
			INSERT INTO benchmarks (id, name) VALUES ($1, $2)
		`, benchmarkID, "Benchmark "+fixture.workID.String()); err != nil {
			t.Fatalf("insert publisher benchmark: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_benchmarks (work_id, benchmark_id, relation_type)
			VALUES ($1, $2, 'evaluated')
		`, fixture.workID, benchmarkID); err != nil {
			t.Fatalf("associate publisher benchmark: %v", err)
		}
	}

	if !options.noJCRAssessment {
		policyID := uuid.New()
		decision := options.jcrDecision
		if decision == "" {
			decision = "accepted"
		}
		matchedRules := options.jcrMatchedRules
		if matchedRules == "" {
			matchedRules = `["jcr_q1"]`
		}
		evidence := options.jcrEvidence
		if evidence == "" {
			evidence = `{"venue_type":"journal","quartiles":["Q1"]}`
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO venue_policy_versions (
				id, policy_name, version_number, definition, effective_at
			) VALUES ($1, $2, 1, '{"kind":"publisher-fixture"}', $3)
		`,
			policyID,
			"publisher-policy-"+fixture.workID.String(),
			options.sourceTime,
		); err != nil {
			t.Fatalf("insert publisher JCR policy: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO venue_policy_assessments (
				venue_id, policy_version_id, metric_year, decision,
				matched_rules, evidence, assessed_at
			) VALUES ($1, $2, 2026, $3, $4::jsonb, $5::jsonb, $6)
		`,
			venueID,
			policyID,
			decision,
			matchedRules,
			evidence,
			options.sourceTime,
		); err != nil {
			t.Fatalf("insert publisher JCR assessment: %v", err)
		}
	}

	if !options.withoutOfficialLink {
		insertPublisherOfficialLinkFixture(
			t,
			ctx,
			tx,
			jobID,
			options,
			&fixture,
		)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit publisher fixture: %v", err)
	}
	return fixture
}

func ensurePublisherClassifierAssertions(
	t *testing.T,
	pool *pgxpool.Pool,
	fixtures ...publisherWorkFixture,
) {
	t.Helper()
	ctx := context.Background()
	for _, fixture := range fixtures {
		var projectionID, normalizedID, sourceRecordID uuid.UUID
		if err := pool.QueryRow(ctx, `
			SELECT
				projection.id,
				state.normalized_assertion_id,
				state.source_record_uuid
			FROM work_projection_states AS state
			JOIN ingestion_projection_assertions AS projection
			  ON projection.work_id = state.work_id
			 AND projection.raw_event_id = state.raw_event_id
			 AND projection.normalized_assertion_id =
			     state.normalized_assertion_id
			 AND projection.source_record_uuid =
			     state.source_record_uuid
			 AND projection.scope_policy_version =
			     state.scope_policy_version
			 AND projection.projection_policy_version =
			     state.projection_policy_version
			WHERE state.work_id = $1
		`, fixture.workID).Scan(
			&projectionID,
			&normalizedID,
			&sourceRecordID,
		); err != nil {
			t.Fatalf(
				"query Work %s exact classifier source: %v",
				fixture.workID,
				err,
			)
		}
		for _, taxonomy := range []struct {
			table       string
			registry    string
			foreignKey  string
			fixtureName string
		}{
			{
				table:       "work_topics",
				registry:    "topics",
				foreignKey:  "topic_id",
				fixtureName: "Classifier Topic " + fixture.workID.String(),
			},
			{
				table:       "work_methods",
				registry:    "methods",
				foreignKey:  "method_id",
				fixtureName: "Classifier Method " + fixture.workID.String(),
			},
		} {
			var exists bool
			if err := pool.QueryRow(ctx, fmt.Sprintf(`
				SELECT EXISTS (
					SELECT 1
					FROM %s
					WHERE work_id = $1
					  AND source_record_id = $2
				)
			`, taxonomy.table), fixture.workID, sourceRecordID).Scan(
				&exists,
			); err != nil {
				t.Fatalf(
					"query Work %s exact %s assertion: %v",
					fixture.workID,
					taxonomy.table,
					err,
				)
			}
			if exists {
				continue
			}
			var taxonomyID uuid.UUID
			if err := pool.QueryRow(ctx, fmt.Sprintf(`
				INSERT INTO %s (name)
				VALUES ($1)
				RETURNING id
			`, taxonomy.registry), taxonomy.fixtureName).Scan(
				&taxonomyID,
			); err != nil {
				t.Fatalf(
					"insert Work %s classifier fixture %s: %v",
					fixture.workID,
					taxonomy.registry,
					err,
				)
			}
			if _, err := pool.Exec(ctx, fmt.Sprintf(`
				INSERT INTO %s (
					work_id,
					%s,
					source_record_id
				) VALUES ($1, $2, $3)
			`, taxonomy.table, taxonomy.foreignKey),
				fixture.workID,
				taxonomyID,
				sourceRecordID,
			); err != nil {
				t.Fatalf(
					"bind Work %s classifier fixture %s to projection %s normalized %s: %v",
					fixture.workID,
					taxonomy.table,
					projectionID,
					normalizedID,
					err,
				)
			}
		}
	}
}

func insertCatalogSucceededAbstractRouteRun(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	fixture publisherWorkFixture,
) string {
	t.Helper()

	return insertCatalogSucceededAbstractRouteRunWithID(
		t,
		pool,
		input,
		fixture,
		uuid.New(),
	)
}

func insertCatalogSucceededAbstractRouteRunWithID(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	fixture publisherWorkFixture,
	runID uuid.UUID,
) string {
	t.Helper()

	if fixture.projectionAssertionID == uuid.Nil ||
		fixture.normalizedAssertionID == uuid.Nil ||
		fixture.sourceRecord == uuid.Nil ||
		fixture.workID == uuid.Nil {
		t.Fatal("abstract route fixture requires an exact projection revision")
	}
	output := make(map[string]any, len(abstractanalysis.FieldNames()))
	for _, field := range abstractanalysis.FieldNames() {
		output[field] = map[string]any{
			"state":    "not_reported",
			"value":    "",
			"evidence": []string{},
		}
	}
	outputJSON, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("encode abstract route output fixture: %v", err)
	}
	schemaJSON := abstractanalysis.SchemaJSON()
	schemaSHA := sha256.Sum256(schemaJSON)
	const promptText = "publisher abstract route fixture"
	titleSHA := sha256.Sum256([]byte(fixture.title))
	abstractSHA := sha256.Sum256([]byte(fixture.abstract))
	promptSHA := sha256.Sum256([]byte(promptText))
	inputJSON, err := json.Marshal(struct {
		PromptVersion string `json:"prompt_version"`
		PromptText    string `json:"prompt_text"`
		SchemaVersion string `json:"schema_version"`
		APIMode       string `json:"api_mode"`
		Title         string `json:"title"`
		Abstract      string `json:"abstract"`
	}{
		PromptVersion: abstractanalysis.PromptVersion,
		PromptText:    promptText,
		SchemaVersion: abstractanalysis.SchemaVersion,
		APIMode:       "responses",
		Title:         fixture.title,
		Abstract:      fixture.abstract,
	})
	if err != nil {
		t.Fatalf("encode abstract route input fixture: %v", err)
	}
	inputSHA := sha256.Sum256(inputJSON)
	startedAt := input.GeneratedAt.Add(-10 * time.Minute)
	completedAt := startedAt.Add(time.Minute)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO abstract_route_analysis_runs (
			id,
			work_id,
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			source_name,
			source_record_external_id,
			parser_version,
			source_time,
			model_provider,
			api_mode,
			requested_model,
			actual_model,
			prompt_version,
			prompt_text,
			prompt_sha256,
			schema_name,
			schema_version,
			schema_json,
			schema_sha256,
			input_title,
			title_sha256,
			input_abstract,
			abstract_sha256,
			input_sha256,
			response_id,
			usage,
			input_tokens,
			output_tokens,
			total_tokens,
			output_payload,
			status,
			started_at,
			lease_expires_at,
			completed_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9,
			'openai-compatible', 'responses', 'publisher-model',
			'publisher-model-2026-07-19',
			$10, $11, $12,
			$13, $14, $15::jsonb, $16,
			$17, $18,
			$19, $20, $21,
			'resp_publisher_abstract_route',
			'{"input_tokens":10,"output_tokens":20,"total_tokens":30}'::jsonb,
			10, 20, 30, $22::jsonb, 'succeeded', $23, $24, $25
		)
	`,
		runID,
		fixture.workID,
		fixture.projectionAssertionID,
		fixture.normalizedAssertionID,
		fixture.sourceRecord,
		fixture.sourceName,
		fixture.sourceRecordExternalID,
		fixture.parserVersion,
		fixture.sourceTime,
		abstractanalysis.PromptVersion,
		promptText,
		hex.EncodeToString(promptSHA[:]),
		abstractanalysis.SchemaName,
		abstractanalysis.SchemaVersion,
		schemaJSON,
		hex.EncodeToString(schemaSHA[:]),
		fixture.title,
		hex.EncodeToString(titleSHA[:]),
		fixture.abstract,
		hex.EncodeToString(abstractSHA[:]),
		hex.EncodeToString(inputSHA[:]),
		outputJSON,
		startedAt,
		startedAt.Add(10*time.Minute),
		completedAt,
	); err != nil {
		t.Fatalf("insert succeeded abstract route run: %v", err)
	}
	return CatalogAbstractRouteRevision
}

func insertPublisherOfficialLinkFixture(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	jobID uuid.UUID,
	options publisherWorkOptions,
	fixture *publisherWorkFixture,
) {
	t.Helper()

	officialURL := options.officialURL
	if officialURL == "" {
		officialURL = "https://publisher.example.test/articles/" +
			fixture.workID.String()
	}
	checkedAt := options.sourceTime
	projectedAt := options.officialLinkProjectedAt
	if projectedAt.IsZero() {
		projectedAt = checkedAt
	}
	expiresAt := options.officialLinkExpiresAt
	if expiresAt.IsZero() {
		expiresAt = checkedAt.Add(365 * 24 * time.Hour)
	}
	identifierScheme := "doi"
	identifierValue := "10.1000/publisher-fixture-" +
		fixture.workID.String()
	if scheme, value, found := strings.Cut(
		options.canonicalKey,
		":",
	); found && scheme == "doi" && value != "" {
		identifierValue = value
	}

	rawEventID := uuid.New()
	sourceRecordID := uuid.New()
	normalizedAssertionID := uuid.New()
	projectionAssertionID := uuid.New()
	candidateID := uuid.New()
	verificationID := uuid.New()
	urlEventKey := options.eventKey + ":official-url"
	sourcePath := "$.URL"
	parserVersion := "publisher-fixture-url-parser/v1"
	contentChannel := string(options.contentChannel)
	linkRole := "official_article"
	switch options.contentChannel {
	case scope.ContentChannelPreprint:
		linkRole = "official_preprint"
	case scope.ContentChannelConferenceProceeding:
		linkRole = "official_proceeding"
	}
	if options.officialLinkRole != "" {
		linkRole = options.officialLinkRole
	}
	hash := sha256.Sum256([]byte(urlEventKey + "\x00" + officialURL))
	contentHash := hex.EncodeToString(hash[:])
	normalizedPayload, err := json.Marshal(map[string]any{
		"source":              options.logicalSource,
		"source_record_id":    urlEventKey,
		"canonical_key":       options.canonicalKey,
		"title":               options.title,
		"parser_version":      parserVersion,
		"publication_history": []publisherPublicationHistoryFixture{},
		"url_candidates": []map[string]any{{
			"url":               officialURL,
			"source_path":       sourcePath,
			"content_channel":   contentChannel,
			"link_role":         linkRole,
			"parser_version":    parserVersion,
			"identifier_scheme": identifierScheme,
			"identifier_value":  identifierValue,
		}},
	})
	if err != nil {
		t.Fatalf("encode publisher official URL normalized payload: %v", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_raw_events (
			id, job_id, logical_source, event_key, event_kind, source_record_id,
			source_time, tie_break_key, position, content_hash, raw_format, raw_payload
		) VALUES (
			$1, $2, $3, $4, 'upsert', $4, $5,
			'publisher-official-url-fixture', 2, $6, 'json', $7
		)
	`,
		rawEventID,
		jobID,
		options.logicalSource,
		urlEventKey,
		options.sourceTime,
		contentHash,
		normalizedPayload,
	); err != nil {
		t.Fatalf("insert publisher official URL raw event: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_records (
			id, source, source_record_id, source_identity, source_time,
			content_hash, raw_payload
		) VALUES (
			$1, $2, $3, $4::jsonb, $5, $6, $7::jsonb
		)
	`,
		sourceRecordID,
		options.logicalSource,
		urlEventKey,
		fmt.Sprintf(`{"canonical_key":%q}`, options.canonicalKey),
		options.sourceTime,
		contentHash,
		normalizedPayload,
	); err != nil {
		t.Fatalf("insert publisher official URL source record: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_normalized_records (
			id, raw_event_id, source_record_uuid, normalization_policy_version,
			payload_schema_version, normalized_payload
		) VALUES ($1, $2, $3, 'normalize/v1', 'normalized-record/v4', $4::jsonb)
	`,
		normalizedAssertionID,
		rawEventID,
		sourceRecordID,
		normalizedPayload,
	); err != nil {
		t.Fatalf("insert publisher official URL normalized record: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, sourceRecordID, fixture.workID); err != nil {
		t.Fatalf("associate publisher official URL source/work: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_projection_assertions (
			id, raw_event_id, normalized_assertion_id, source_record_uuid,
			work_id, job_id, scope_policy_version, projection_policy_version,
			record_payload
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			'scope/v1', 'projection/v1', '{"fixture":"publisher-official-url"}'
		)
	`,
		projectionAssertionID,
		rawEventID,
		normalizedAssertionID,
		sourceRecordID,
		fixture.workID,
		jobID,
	); err != nil {
		t.Fatalf("insert publisher official URL projection assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_url_candidates (
			id, work_id, projection_assertion_id, normalized_assertion_id,
			source_record_id, source_path, content_channel, link_role,
			candidate_url, parser_version, identifier_scheme, identifier_value,
			asserted_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
		)
	`,
		candidateID,
		fixture.workID,
		projectionAssertionID,
		normalizedAssertionID,
		sourceRecordID,
		sourcePath,
		contentChannel,
		linkRole,
		officialURL,
		parserVersion,
		identifierScheme,
		identifierValue,
		options.sourceTime,
	); err != nil {
		t.Fatalf("insert publisher official URL candidate: %v", err)
	}
	parsedOfficialURL, err := url.Parse(officialURL)
	if err != nil || parsedOfficialURL.Hostname() == "" {
		t.Fatalf("parse publisher official URL %q: %v", officialURL, err)
	}
	importPublisherOfficialURLHostRegistryFixture(t, ctx, tx)
	var registeredHost bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM official_url_host_registry AS registry
			JOIN official_url_registry_versions AS version
			  ON version.id = registry.registry_version_id
			 AND version.policy_version = registry.policy_version
			WHERE version.policy_version = 'official-url/v1'
			  AND version.sealed_at IS NOT NULL
			  AND registry.content_channel = $1
			  AND registry.link_role = $2
			  AND registry.hostname = $3
		)
	`, contentChannel, linkRole, parsedOfficialURL.Hostname()).Scan(
		&registeredHost,
	); err != nil {
		t.Fatalf("query publisher official URL host Registry: %v", err)
	}
	if !registeredHost {
		t.Fatalf(
			"publisher official URL host Registry lacks %s/%s/%s",
			contentChannel,
			linkRole,
			parsedOfficialURL.Hostname(),
		)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_url_verifications (
			id, candidate_id, source_url, final_url, redirect_chain, http_status,
			expected_identifier_scheme, expected_identifier_value,
			observed_identifiers, identifier_evidence, response_metadata,
			matched_identifier_scheme,
			matched_identifier_value, identifier_match, verification_state,
			checked_at, expires_at, verifier_version, policy_version
		) VALUES (
			$1, $2, $3, $3, $4::jsonb, 200, $5, $6, $7::jsonb,
			$8::jsonb, $9::jsonb, $5, $6, true, 'verified', $10, $11,
			'publisher-fixture-verifier/v1', 'official-url/v1'
		)
	`,
		verificationID,
		candidateID,
		officialURL,
		fmt.Sprintf(
			`[{"url":%q,"status_code":200,"metadata":{"content_type":"text/html","etag":"","last_modified":""}}]`,
			officialURL,
		),
		identifierScheme,
		identifierValue,
		fmt.Sprintf(
			`[{"scheme":%q,"value":%q}]`,
			identifierScheme,
			identifierValue,
		),
		fmt.Sprintf(
			`[{"identifier":{"scheme":%q,"value":%q},"source_kind":"html_meta","source_path":"meta[name=\"citation_doi\"]@content","raw_value":%q}]`,
			identifierScheme,
			identifierValue,
			identifierValue,
		),
		`{"content_type":"text/html","etag":"","last_modified":""}`,
		checkedAt,
		expiresAt,
	); err != nil {
		t.Fatalf("insert publisher official URL verification: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO current_work_official_links (
			work_id, content_channel, link_role, verification_id, official_url,
			projection_version, projected_at, expires_at
		) VALUES ($1, $2, $3, $4, $5, 1, $6, $7)
	`,
		fixture.workID,
		contentChannel,
		linkRole,
		verificationID,
		officialURL,
		projectedAt,
		expiresAt,
	); err != nil {
		t.Fatalf("insert publisher current official URL link: %v", err)
	}
	fixture.officialVerificationID = verificationID
	fixture.officialVerifiedAt = checkedAt.UTC()
	fixture.officialExpiresAt = expiresAt.UTC()
}

func importPublisherOfficialURLHostRegistryFixture(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
) {
	t.Helper()

	var registryVersionID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id
		FROM official_url_registry_versions
		WHERE policy_version = 'official-url/v1'
		  AND sealed_at IS NOT NULL
	`).Scan(&registryVersionID)
	if err == nil {
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("query publisher official URL Registry receipt: %v", err)
	}

	importedAt := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	fileSHA256 := fmt.Sprintf(
		"%x",
		sha256.Sum256(
			[]byte("publisher-integration-test/official-url-hosts/v1"),
		),
	)
	const hostCount = 8
	if err := tx.QueryRow(ctx, `
		INSERT INTO official_url_registry_versions (
			registry_name,
			policy_version,
			file_sha256,
			host_count,
			imported_at
		) VALUES (
			'official-url-hosts',
			'official-url/v1',
			$1,
			$2,
			$3
		)
		RETURNING id
	`, fileSHA256, hostCount, importedAt).Scan(&registryVersionID); err != nil {
		t.Fatalf("insert publisher official URL Registry receipt: %v", err)
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
		) VALUES
			($1, 'official-url/v1', 'journal_published', 'official_article',
				'publisher.example.test', 'catalog_test_fixture',
				'publisher-integration-test/v1', $2),
			($1, 'official-url/v1', 'journal_published', 'doi_url',
				'publisher.example.test', 'catalog_test_fixture',
				'publisher-integration-test/v1', $2),
			($1, 'official-url/v1', 'accepted_early', 'official_article',
				'publisher.example.test', 'catalog_test_fixture',
				'publisher-integration-test/v1', $2),
			($1, 'official-url/v1', 'accepted_early', 'doi_url',
				'publisher.example.test', 'catalog_test_fixture',
				'publisher-integration-test/v1', $2),
			($1, 'official-url/v1', 'preprint', 'official_preprint',
				'publisher.example.test', 'catalog_test_fixture',
				'publisher-integration-test/v1', $2),
			($1, 'official-url/v1', 'preprint', 'doi_url',
				'publisher.example.test', 'catalog_test_fixture',
				'publisher-integration-test/v1', $2),
			($1, 'official-url/v1', 'conference_proceeding',
				'official_proceeding', 'publisher.example.test',
				'catalog_test_fixture', 'publisher-integration-test/v1', $2),
			($1, 'official-url/v1', 'conference_proceeding', 'doi_url',
				'publisher.example.test', 'catalog_test_fixture',
				'publisher-integration-test/v1', $2)
	`, registryVersionID, importedAt); err != nil {
		t.Fatalf("insert publisher official URL Registry hosts: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE official_url_registry_versions
		SET sealed_at = $2
		WHERE id = $1
	`, registryVersionID, importedAt); err != nil {
		t.Fatalf("seal publisher official URL Registry receipt: %v", err)
	}
}

func insertPublisherExpiredOfficialLinkVersion(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
	checkedAt time.Time,
	expiresAt time.Time,
) {
	t.Helper()

	ctx := context.Background()
	verificationID := uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO work_url_verifications (
			id, candidate_id, source_url, final_url, redirect_chain, http_status,
			expected_identifier_scheme, expected_identifier_value,
			observed_identifiers, identifier_evidence, response_metadata,
			matched_identifier_scheme, matched_identifier_value,
			identifier_match, verification_state, checked_at, expires_at,
			verifier_version, policy_version, failure_code
		)
		SELECT
			$1, candidate_id, source_url, final_url, redirect_chain, http_status,
			expected_identifier_scheme, expected_identifier_value,
			observed_identifiers, identifier_evidence, response_metadata,
			matched_identifier_scheme, matched_identifier_value,
			identifier_match, verification_state, $2, $3,
			verifier_version, policy_version, failure_code
		FROM work_url_verifications
		WHERE id = $4
	`, verificationID, checkedAt, expiresAt, fixture.officialVerificationID); err != nil {
		t.Fatalf("insert expired publisher official URL verification: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO current_work_official_links (
			work_id, content_channel, link_role, verification_id, official_url,
			projection_version, projected_at, expires_at
		)
		SELECT
			work_id, content_channel, link_role, $1, official_url,
			2, $2, $3
		FROM current_work_official_links
		WHERE verification_id = $4
	`, verificationID, checkedAt, expiresAt, fixture.officialVerificationID); err != nil {
		t.Fatalf("insert expired publisher current official URL link: %v", err)
	}
}

func preparePublisherFactsReferences(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
) {
	t.Helper()

	_, subjectRuleID := insertCatalogSubjectVersion(
		t,
		pool,
		input.SubjectVersion,
	)
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, nil)
}

func preparePublisherAcceptedCurationForPublishedCohort(
	t *testing.T,
	pool *pgxpool.Pool,
	input PublishInput,
	acceptedFixtures []publisherWorkFixture,
	publishedFixtures []publisherWorkFixture,
) {
	t.Helper()

	_, subjectRuleID := insertCatalogSubjectVersion(
		t,
		pool,
		input.SubjectVersion,
	)
	jcrFixtures := make(
		[]catalogJCRVenueFixture,
		0,
		len(acceptedFixtures),
	)
	for index, fixture := range acceptedFixtures {
		venueID := catalogWorkVenueID(t, pool, fixture.workID)
		if _, err := pool.Exec(context.Background(), `
			UPDATE venues
			SET venue_type = 'journal',
			    issn_l = $2
			WHERE id = $1
		`, venueID, deterministicCatalogISSN(2000+index)); err != nil {
			t.Fatalf("prepare publisher URL gate Venue curation: %v", err)
		}
		jcrFixtures = append(jcrFixtures, catalogJCRVenueFixture{
			venueID:     venueID,
			index:       2000 + index,
			withSubject: true,
		})
	}
	insertCatalogJCRBundle(t, pool, input, subjectRuleID, jcrFixtures)
	assessCatalogVenuePolicy(t, pool, input)
	for _, fixture := range acceptedFixtures {
		assessCatalogBiomedicalEligibility(
			t,
			pool,
			fixture.workID,
			input,
		)
	}
	cohortRevision := catalogBiomedicalCohortRevisionForFixtures(
		t,
		pool,
		input,
		publishedFixtures,
	)
	if len(publishedFixtures) == 0 {
		t.Fatal("publisher URL gate fixture requires a published cohort")
	}
	insertCatalogBiomedicalPublicationFixtures(
		t,
		pool,
		input,
		cohortRevision,
		cohortRevision,
		input.TrendAnalysisRunID,
		publishedFixtures[0],
	)
	workIDs := make([]uuid.UUID, 0, len(publishedFixtures))
	for _, fixture := range publishedFixtures {
		workIDs = append(workIDs, fixture.workID)
	}
	insertCatalogCitationAnalysisRun(t, pool, input, workIDs...)
	if input.Mode == PublishAnalysis {
		insertCatalogSucceededAbstractRouteRun(
			t,
			pool,
			input,
			publishedFixtures[0],
		)
	}
}

type publisherPublicationEventFixture struct {
	kind       string
	date       *time.Time
	precision  string
	sourceDate string
	statusRaw  string
	modelRaw   *string
	sourcePath string
	ordinal    int
}

func publisherPublicationHistoryFromEvents(
	t *testing.T,
	events []publisherPublicationEventFixture,
) []publisherPublicationHistoryFixture {
	t.Helper()
	history := make([]publisherPublicationHistoryFixture, 0, len(events))
	for _, event := range events {
		var date publisherPublicationDateFixture
		if event.sourceDate != "" {
			if err := json.Unmarshal([]byte(event.sourceDate), &date); err != nil {
				t.Fatalf(
					"decode publication history source date ordinal %d: %v",
					event.ordinal,
					err,
				)
			}
		} else {
			if event.date == nil {
				t.Fatalf(
					"publication history event ordinal %d lacks a source date",
					event.ordinal,
				)
			}
			date = publisherPublicationDateFixture{
				Year:      event.date.Year(),
				Month:     int(event.date.Month()),
				Day:       event.date.Day(),
				Precision: event.precision,
			}
		}
		history = append(history, publisherPublicationHistoryFixture{
			Status:     event.statusRaw,
			Date:       date,
			SourcePath: event.sourcePath,
			Ordinal:    event.ordinal,
		})
	}
	return history
}

type publisherPublicationStateFixture struct {
	projectionAssertionID uuid.UUID
	printDate             *time.Time
	printState            string
	electronicDate        *time.Time
	electronicState       string
	aheadDate             *time.Time
	aheadState            string
	acceptedDate          *time.Time
	acceptedState         string
	publicationModel      *string
	publicationStatus     *string
	events                []publisherPublicationEventFixture
}

type publicationEvidenceQueryCounter struct {
	count atomic.Int64
}

func (counter *publicationEvidenceQueryCounter) record(sql string) {
	if strings.Contains(sql, "work_publication_states") ||
		strings.Contains(sql, "work_publication_event_assertions") {
		counter.count.Add(1)
	}
}

type publicationEvidenceCountingDatabase struct {
	database *pgxpool.Pool
	counter  *publicationEvidenceQueryCounter
}

func (database publicationEvidenceCountingDatabase) BeginTx(
	ctx context.Context,
	options pgx.TxOptions,
) (pgx.Tx, error) {
	tx, err := database.database.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return publicationEvidenceCountingTx{
		Tx:      tx,
		counter: database.counter,
	}, nil
}

type publicationEvidenceCountingTx struct {
	pgx.Tx
	counter *publicationEvidenceQueryCounter
}

func (tx publicationEvidenceCountingTx) Query(
	ctx context.Context,
	sql string,
	args ...any,
) (pgx.Rows, error) {
	tx.counter.record(sql)
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx publicationEvidenceCountingTx) QueryRow(
	ctx context.Context,
	sql string,
	args ...any,
) pgx.Row {
	tx.counter.record(sql)
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func insertPublisherPublicationState(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
	state publisherPublicationStateFixture,
) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	if state.projectionAssertionID == uuid.Nil {
		state.projectionAssertionID = publisherProjectionAssertionID(t, pool, fixture.workID)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin publisher publication state fixture: %v", err)
	}
	defer tx.Rollback(context.Background())

	for _, event := range state.events {
		sourceDate := event.sourceDate
		if sourceDate == "" {
			if event.date == nil {
				t.Fatalf("publication event %s lacks source date", event.kind)
			}
			encoded, err := json.Marshal(map[string]any{
				"year":      event.date.Year(),
				"month":     int(event.date.Month()),
				"day":       event.date.Day(),
				"precision": event.precision,
			})
			if err != nil {
				t.Fatalf("encode publication event source date: %v", err)
			}
			sourceDate = string(encoded)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO work_publication_event_assertions (
				projection_assertion_id,
				normalized_assertion_id,
				source_record_id,
				work_id,
				event_kind,
				event_date,
				date_precision,
				source_date,
				status_raw,
				publication_model_raw,
				source_path,
				ordinal
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8::jsonb, $9, $10, $11, $12
			)
		`,
			state.projectionAssertionID,
			fixture.normalizedAssertionID,
			fixture.sourceRecord,
			fixture.workID,
			event.kind,
			event.date,
			event.precision,
			sourceDate,
			event.statusRaw,
			event.modelRaw,
			event.sourcePath,
			event.ordinal,
		); err != nil {
			t.Fatalf("insert publisher publication event %s: %v", event.kind, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_publication_states (
			work_id,
			projection_assertion_id,
			normalized_assertion_id,
			source_record_id,
			print_published_on,
			print_published_state,
			electronic_published_on,
			electronic_published_state,
			ahead_of_print_on,
			ahead_of_print_state,
			accepted_on,
			accepted_state,
			publication_model_raw,
			publication_status_raw
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$8, $9, $10, $11, $12, $13, $14
		)
	`,
		fixture.workID,
		state.projectionAssertionID,
		fixture.normalizedAssertionID,
		fixture.sourceRecord,
		state.printDate,
		state.printState,
		state.electronicDate,
		state.electronicState,
		state.aheadDate,
		state.aheadState,
		state.acceptedDate,
		state.acceptedState,
		state.publicationModel,
		state.publicationStatus,
	); err != nil {
		t.Fatalf("insert publisher publication state: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit publisher publication state fixture: %v", err)
	}
	return state.projectionAssertionID
}

func publisherProjectionAssertionID(
	t *testing.T,
	pool *pgxpool.Pool,
	workID uuid.UUID,
) uuid.UUID {
	t.Helper()
	var projectionAssertionID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		SELECT assertion.id
		FROM work_projection_states AS state
		JOIN ingestion_projection_assertions AS assertion
		  ON assertion.work_id = state.work_id
		 AND assertion.raw_event_id = state.raw_event_id
		 AND assertion.normalized_assertion_id = state.normalized_assertion_id
		 AND assertion.source_record_uuid = state.source_record_uuid
		 AND assertion.scope_policy_version = state.scope_policy_version
		 AND assertion.projection_policy_version = state.projection_policy_version
		WHERE state.work_id = $1
	`, workID).Scan(&projectionAssertionID); err != nil {
		t.Fatalf("query publisher projection assertion: %v", err)
	}
	return projectionAssertionID
}

func insertPublisherAlternateProjectionAssertion(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
	projectionPolicyVersion string,
) uuid.UUID {
	t.Helper()
	var jobID uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		SELECT job_id
		FROM ingestion_raw_events
		WHERE id = $1
	`, fixture.rawEventID).Scan(&jobID); err != nil {
		t.Fatalf("query alternate publication projection job: %v", err)
	}
	projectionAssertionID := uuid.New()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO ingestion_projection_assertions (
			id,
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			work_id,
			job_id,
			scope_policy_version,
			projection_policy_version,
			record_payload
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			'scope/v1', $7, '{"fixture":"publication-decoy"}'
		)
	`,
		projectionAssertionID,
		fixture.rawEventID,
		fixture.normalizedAssertionID,
		fixture.sourceRecord,
		fixture.workID,
		jobID,
		projectionPolicyVersion,
	); err != nil {
		t.Fatalf("insert alternate publication projection assertion: %v", err)
	}
	return projectionAssertionID
}

func mustPublisher(t *testing.T, pool *pgxpool.Pool) *Publisher {
	t.Helper()

	publisher, err := NewPublisher(pool)
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}
	return publisher
}

func assertNoCatalogWrites(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()

	var generations, publications, current int
	if err := pool.QueryRow(context.Background(), `
		SELECT
			(SELECT count(*) FROM public_catalog_generations),
			(SELECT count(*) FROM public_catalog_publications),
			(SELECT count(*) FROM public_catalog_current)
	`).Scan(&generations, &publications, &current); err != nil {
		t.Fatalf("count public catalog writes: %v", err)
	}
	if generations != 0 || publications != 0 || current != 0 {
		t.Fatalf(
			"catalog writes = generations %d publications %d current %d, want 0/0/0",
			generations,
			publications,
			current,
		)
	}
}

func assertNestedJSONValue(
	t *testing.T,
	payload json.RawMessage,
	path []string,
	want any,
) {
	t.Helper()

	var current any
	if err := json.Unmarshal(payload, &current); err != nil {
		t.Fatalf("decode JSON payload: %v", err)
	}
	for _, segment := range path {
		if object, ok := current.(map[string]any); ok {
			current, ok = object[segment]
			if !ok {
				t.Fatalf("JSON path %v is missing segment %q in %s", path, segment, payload)
			}
			continue
		}
		items, ok := current.([]any)
		if !ok {
			t.Fatalf("JSON path %v reached non-container %#v", path, current)
		}
		index := -1
		if _, err := fmt.Sscan(segment, &index); err != nil ||
			index < 0 ||
			index >= len(items) {
			t.Fatalf("JSON path %v has invalid array index %q", path, segment)
		}
		current = items[index]
	}
	if fmt.Sprint(current) != fmt.Sprint(want) {
		t.Fatalf("JSON path %v = %#v, want %#v; payload=%s", path, current, want, payload)
	}
}

func insertPublisherBiomedicalProjection(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin publisher biomedical fixture: %v", err)
	}
	defer tx.Rollback(context.Background())

	var jobID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT job_id
		FROM ingestion_raw_events
		WHERE id = $1
	`, fixture.rawEventID).Scan(&jobID); err != nil {
		t.Fatalf("query publisher biomedical job: %v", err)
	}
	var projectionAssertionID uuid.UUID
	if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO ingestion_projection_assertions (
				id,
				raw_event_id,
				normalized_assertion_id,
				source_record_uuid,
				work_id,
				job_id,
				scope_policy_version,
				projection_policy_version,
				record_payload
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				'scope/v1', $7, '{"fixture":"biomedical"}'
			)
			ON CONFLICT (
				normalized_assertion_id,
				scope_policy_version,
				projection_policy_version
			) DO NOTHING
			RETURNING id
		)
		SELECT id FROM inserted
		UNION ALL
		SELECT id
		FROM ingestion_projection_assertions
		WHERE normalized_assertion_id = $3
		  AND scope_policy_version = 'scope/v1'
		  AND projection_policy_version = $7
		LIMIT 1
	`,
		uuid.New(),
		fixture.rawEventID,
		fixture.normalizedAssertionID,
		fixture.sourceRecord,
		fixture.workID,
		jobID,
		catalogClassifierProjectionPolicyVersion,
	).Scan(&projectionAssertionID); err != nil {
		t.Fatalf("insert publisher biomedical projection assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_projection_states (
			work_id,
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			source_time,
			tie_break_key,
			position,
			scope_policy_version,
			projection_policy_version
		)
		SELECT
			$1,
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			source_time,
			tie_break_key,
			position,
			scope_policy_version,
			projection_policy_version
		FROM ingestion_source_states
		WHERE work_id = $1
		ON CONFLICT (work_id) DO NOTHING
	`, fixture.workID); err != nil {
		t.Fatalf("insert publisher biomedical winner state: %v", err)
	}

	var descriptorID uuid.UUID
	var qualifierID uuid.UUID
	var publicationTypeID uuid.UUID
	headingID := uuid.New()
	if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO mesh_descriptors (id, descriptor_ui)
			VALUES ($1, 'D008175')
			ON CONFLICT (descriptor_ui) DO NOTHING
			RETURNING id
		)
		SELECT id FROM inserted
		UNION ALL
		SELECT id FROM mesh_descriptors WHERE descriptor_ui = 'D008175'
		LIMIT 1
	`, uuid.New()).Scan(&descriptorID); err != nil {
		t.Fatalf("insert publisher MeSH descriptor: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO mesh_qualifiers (id, qualifier_ui)
			VALUES ($1, 'Q000401')
			ON CONFLICT (qualifier_ui) DO NOTHING
			RETURNING id
		)
		SELECT id FROM inserted
		UNION ALL
		SELECT id FROM mesh_qualifiers WHERE qualifier_ui = 'Q000401'
		LIMIT 1
	`, uuid.New()).Scan(&qualifierID); err != nil {
		t.Fatalf("insert publisher MeSH qualifier: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO publication_types (id, publication_type_ui)
			VALUES ($1, 'D016449')
			ON CONFLICT (publication_type_ui) DO NOTHING
			RETURNING id
		)
		SELECT id FROM inserted
		UNION ALL
		SELECT id
		FROM publication_types
		WHERE publication_type_ui = 'D016449'
		LIMIT 1
	`, uuid.New()).Scan(&publicationTypeID); err != nil {
		t.Fatalf("insert publisher Publication Type: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_mesh_headings (
			id,
			projection_assertion_id,
			source_record_id,
			work_id,
			descriptor_id,
			source_path,
			descriptor_label,
			is_major_topic
		) VALUES (
			$1, $2, $3, $4, $5,
			'/PubmedArticle/MeshHeading[1]/DescriptorName',
			'Lung Neoplasms',
			true
		)
	`,
		headingID,
		projectionAssertionID,
		fixture.sourceRecord,
		fixture.workID,
		descriptorID,
	); err != nil {
		t.Fatalf("insert publisher MeSH heading: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_mesh_qualifiers (
			projection_assertion_id,
			source_record_id,
			work_id,
			work_mesh_heading_id,
			qualifier_id,
			source_path,
			qualifier_label,
			is_major_topic
		) VALUES (
			$1, $2, $3, $4, $5,
			'/PubmedArticle/MeshHeading[1]/QualifierName[1]',
			'therapy',
			false
		)
	`,
		projectionAssertionID,
		fixture.sourceRecord,
		fixture.workID,
		headingID,
		qualifierID,
	); err != nil {
		t.Fatalf("insert publisher MeSH qualifier assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO work_publication_types (
			projection_assertion_id,
			source_record_id,
			work_id,
			publication_type_id,
			source_path,
			publication_type_label
		) VALUES (
			$1, $2, $3, $4,
			'/PubmedArticle/PublicationType[1]',
			'Randomized Controlled Trial'
		)
	`,
		projectionAssertionID,
		fixture.sourceRecord,
		fixture.workID,
		publicationTypeID,
	); err != nil {
		t.Fatalf("insert publisher Publication Type assertion: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit publisher biomedical fixture: %v", err)
	}
}

func insertPublisherCitationSnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
	count int64,
) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin publisher citation fixture: %v", err)
	}
	defer tx.Rollback(context.Background())

	var jobID uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT job_id
		FROM ingestion_raw_events
		WHERE id = $1
	`, fixture.rawEventID).Scan(&jobID); err != nil {
		t.Fatalf("query publisher citation job: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_projection_assertions (
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			work_id,
			job_id,
			scope_policy_version,
			projection_policy_version,
			record_payload
		) VALUES (
			$1, $2, $3, $4, $5,
			'scope/v1', 'projection/v1', '{"fixture":"citation"}'
		)
		ON CONFLICT (
			normalized_assertion_id,
			scope_policy_version,
			projection_policy_version
		) DO NOTHING
	`, fixture.rawEventID, fixture.normalizedAssertionID, fixture.sourceRecord, fixture.workID, jobID); err != nil {
		t.Fatalf("insert publisher citation projection assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO citation_snapshots (
			work_id,
			source,
			observed_at,
			count,
			source_record_id,
			ingestion_job_id,
			retrieved_at,
			coverage,
			definition_version,
			dataset_version
		)
		SELECT
			$1,
			source,
			source_time,
			$2,
			id,
			$3,
			retrieved_at,
			1,
			'openalex-cited-by-count/v1',
			'openalex-source-record/' || id::text
		FROM source_records
		WHERE id = $4
	`, fixture.workID, count, jobID, fixture.sourceRecord); err != nil {
		t.Fatalf("insert publisher citation snapshot: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit publisher citation fixture: %v", err)
	}
}

func insertPublisherAdditionalCitationSnapshot(
	t *testing.T,
	pool *pgxpool.Pool,
	fixture publisherWorkFixture,
	observedAt time.Time,
	count int64,
) {
	t.Helper()
	ctx := context.Background()
	sourceRecordID := uuid.New()
	rawEventID := uuid.New()
	jobID := uuid.New()
	eventKey := "openalex:citation:" + uuid.NewString()
	sum := sha256.Sum256([]byte(eventKey))
	contentHash := hex.EncodeToString(sum[:])
	retrievedAt := observedAt.Add(time.Hour)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin additional publisher citation fixture: %v", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_jobs (
			id, source, job_type, idempotency_key, status, payload, max_attempts,
			batch_key, stage
		) VALUES (
			$1, 'openalex', 'sync', $2, 'succeeded', '{}', 1, $2, 'project'
		)
	`, jobID, "publisher:"+eventKey); err != nil {
		t.Fatalf("insert additional citation ingestion job: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_raw_events (
			id, job_id, logical_source, event_key, event_kind, source_record_id,
			source_time, tie_break_key, position, content_hash, raw_format, raw_payload
		) VALUES (
			$1, $2, 'openalex', $3, 'upsert', $3, $4,
			'publisher-citation-fixture', 1, $5, 'json', $6
		)
	`, rawEventID, jobID, eventKey, observedAt, contentHash, []byte(`{"fixture":"citation"}`)); err != nil {
		t.Fatalf("insert additional citation raw event: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_records (
			id, source, source_record_id, source_identity, source_time,
			content_hash, raw_payload, retrieved_at
		) VALUES (
			$1, 'openalex', $2, $3::jsonb, $4, $5,
			'{"fixture":"citation"}', $6
		)
	`,
		sourceRecordID,
		eventKey,
		fmt.Sprintf(`{"canonical_key":%q}`, fixture.workID.String()),
		observedAt,
		contentHash,
		retrievedAt,
	); err != nil {
		t.Fatalf("insert additional citation source record: %v", err)
	}
	var normalizedAssertionID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO ingestion_normalized_records (
			raw_event_id,
			source_record_uuid,
			normalization_policy_version,
			payload_schema_version,
			normalized_payload
		) VALUES (
			$1, $2, 'normalize/v1', 'normalized-record/v2',
			'{"fixture":"citation"}'
		)
		RETURNING id
	`, rawEventID, sourceRecordID).Scan(&normalizedAssertionID); err != nil {
		t.Fatalf("insert additional citation normalized record: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO source_record_works (source_record_id, work_id)
		VALUES ($1, $2)
	`, sourceRecordID, fixture.workID); err != nil {
		t.Fatalf("associate additional citation source record: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_projection_assertions (
			raw_event_id,
			normalized_assertion_id,
			source_record_uuid,
			work_id,
			job_id,
			scope_policy_version,
			projection_policy_version,
			record_payload
		) VALUES (
			$1, $2, $3, $4, $5,
			'scope/v1', 'projection/v1', '{"fixture":"citation"}'
		)
	`,
		rawEventID,
		normalizedAssertionID,
		sourceRecordID,
		fixture.workID,
		jobID,
	); err != nil {
		t.Fatalf("insert additional citation projection assertion: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO citation_snapshots (
			work_id,
			source,
			observed_at,
			count,
			source_record_id,
			ingestion_job_id,
			retrieved_at,
			coverage,
			definition_version,
			dataset_version
		) VALUES (
			$1, 'openalex', $2, $3, $4, $5, $6, 1,
			'openalex-cited-by-count/v1',
			$7
		)
	`,
		fixture.workID,
		observedAt,
		count,
		sourceRecordID,
		jobID,
		retrievedAt,
		"openalex-source-record/"+sourceRecordID.String(),
	); err != nil {
		t.Fatalf("insert additional publisher citation snapshot: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit additional publisher citation fixture: %v", err)
	}
}

func assertJSONArrayLength(
	t *testing.T,
	payload json.RawMessage,
	field string,
	want int,
) {
	t.Helper()

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode JSON payload: %v", err)
	}
	value, ok := decoded[field].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want DataValue object", field, decoded[field])
	}
	items, ok := value["value"].([]any)
	if !ok {
		t.Fatalf("%s.value = %#v, want array", field, value["value"])
	}
	if len(items) != want {
		t.Fatalf("%s.value length = %d, want %d", field, len(items), want)
	}
}

func optionalUUID(include bool, value uuid.UUID) string {
	if include {
		return value.String()
	}
	return ""
}

func stateForTime(value time.Time) string {
	if value.IsZero() {
		return "missing"
	}
	return "known"
}

func optionalTimeValue(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.Format(time.RFC3339Nano)
}

func boolState(value bool) string {
	if value {
		return "known"
	}
	return "unknown"
}

func optionalStringArray(include bool, value string) []string {
	if !include {
		return nil
	}
	return []string{value}
}
