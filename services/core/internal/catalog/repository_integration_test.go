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

var catalogCursorSecret = []byte("catalog-test-cursor-secret-with-32-bytes")

func TestRepositoryRequiresPublishedCatalog(t *testing.T) {
	pool := openCatalogTestPool(t)
	repository := mustRepository(t, pool)

	_, err := repository.Stats(context.Background())
	if !errors.Is(err, ErrCatalogNotPublished) {
		t.Fatalf("Stats() error = %v, want ErrCatalogNotPublished", err)
	}
}

func TestPublishedCatalogGenerationIsImmutable(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertCatalogFixture(t, pool, fixtureOptions{})
	ctx := context.Background()

	rejectedWrites := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name:  "generation update",
			query: `UPDATE public_catalog_generations SET source_revision = 'changed' WHERE id = $1`,
			args:  []any{fixture.generationID},
		},
		{
			name:  "generation delete",
			query: `DELETE FROM public_catalog_generations WHERE id = $1`,
			args:  []any{fixture.generationID},
		},
		{
			name:  "published stats update",
			query: `UPDATE public_catalog_stats SET payload = '{"changed":true}' WHERE generation_id = $1`,
			args:  []any{fixture.generationID},
		},
		{
			name: "published paper insert",
			query: `
				INSERT INTO public_catalog_papers (
					generation_id, paper_id, canonical_key, title, search_text,
					published_at_state, paper_type_state, lifecycle_status,
					has_code_state, has_data_state, has_benchmark_state,
					citation_count_state, trend_score_state,
					summary_payload, detail_payload
				) VALUES (
					$1, gen_random_uuid(), 'openalex:W999999', 'Late mutation', 'Late mutation',
					'missing', 'missing', 'active',
					'missing', 'missing', 'missing', 'missing', 'missing',
					'{"id":"00000000-0000-0000-0000-000000000999","title":"Late mutation"}',
					'{"id":"00000000-0000-0000-0000-000000000999","title":"Late mutation"}'
				)
			`,
			args: []any{fixture.generationID},
		},
	}

	for _, test := range rejectedWrites {
		t.Run(test.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, test.query, test.args...); err == nil {
				t.Fatalf("%s succeeded against an immutable published generation", test.name)
			}
		})
	}
}

func TestRepositoryReadsStatsAndDetailsFromCurrentGeneration(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertCatalogFixture(t, pool, fixtureOptions{})
	repository := mustRepository(t, pool)
	ctx := context.Background()

	stats, err := repository.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.Generation.ID != fixture.generationID {
		t.Fatalf(
			"Stats generation = %s, want %s",
			stats.Generation.ID,
			fixture.generationID,
		)
	}
	assertJSONField(t, stats.Payload, "papers_total", float64(3))

	paper, err := repository.Paper(ctx, fixture.knownPaperID)
	if err != nil {
		t.Fatalf("Paper() error = %v", err)
	}
	assertJSONField(t, paper.Payload, "title", "Causal Agents")

	topic, err := repository.Topic(ctx, "agents")
	if err != nil {
		t.Fatalf("Topic() error = %v", err)
	}
	assertJSONField(t, topic.Payload, "slug", "agents")

	method, err := repository.Method(ctx, "causal-inference")
	if err != nil {
		t.Fatalf("Method() error = %v", err)
	}
	assertJSONField(t, method.Payload, "slug", "causal-inference")
}

func TestRepositoryHidesAbsentAndNonPublishedDetailsBehindSameNotFound(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertCatalogFixture(t, pool, fixtureOptions{})
	repository := mustRepository(t, pool)
	ctx := context.Background()

	for _, paperID := range []uuid.UUID{fixture.hiddenPaperID, uuid.New()} {
		_, err := repository.Paper(ctx, paperID)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Paper(%s) error = %v, want ErrNotFound", paperID, err)
		}
	}
	for _, slug := range []string{"private-topic", "does-not-exist"} {
		_, err := repository.Topic(ctx, slug)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("Topic(%q) error = %v, want ErrNotFound", slug, err)
		}
	}
}

func TestPaperListSearchFiltersAndKnownUnknownMissingSemantics(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertCatalogFixture(t, pool, fixtureOptions{})
	repository := mustRepository(t, pool)
	ctx := context.Background()

	search, err := repository.Papers(ctx, PaperListQuery{
		Query: "causal agent",
		Sort:  PaperSortRelevance,
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("Papers(search) error = %v", err)
	}
	assertPaperIDs(t, search.Items, fixture.knownPaperID)

	hasCode := true
	knownTrue, err := repository.Papers(ctx, PaperListQuery{
		HasCode: &hasCode,
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("Papers(has_code=true) error = %v", err)
	}
	assertPaperIDs(t, knownTrue.Items, fixture.knownPaperID)

	hasCode = false
	knownFalse, err := repository.Papers(ctx, PaperListQuery{
		HasCode: &hasCode,
		Limit:   20,
	})
	if err != nil {
		t.Fatalf("Papers(has_code=false) error = %v", err)
	}
	assertPaperIDs(t, knownFalse.Items, fixture.knownFalsePaperID)

	combined, err := repository.Papers(ctx, PaperListQuery{
		Topic:     "agents",
		Method:    "causal-inference",
		PaperType: "research_article",
		Status:    "active",
		Source:    "openalex",
		PublishedFrom: timePointer(
			time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		),
		PublishedTo: timePointer(
			time.Date(2026, time.July, 31, 23, 59, 59, 0, time.UTC),
		),
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("Papers(combined filters) error = %v", err)
	}
	assertPaperIDs(t, combined.Items, fixture.knownPaperID)

	if combined.Pagination.Total != 1 {
		t.Fatalf("filtered total = %d, want 1", combined.Pagination.Total)
	}
	if len(combined.Facets.PaperTypes) == 0 ||
		len(combined.Facets.Topics) == 0 ||
		len(combined.Facets.Methods) == 0 ||
		len(combined.Facets.Statuses) == 0 ||
		len(combined.Facets.Sources) == 0 {
		t.Fatalf("filtered facets are incomplete: %#v", combined.Facets)
	}
}

func TestPaperCursorIsSignedAndBoundToGenerationFilterAndSort(t *testing.T) {
	pool := openCatalogTestPool(t)
	firstFixture := insertCatalogFixture(t, pool, fixtureOptions{})
	repository := mustRepository(t, pool)
	ctx := context.Background()

	firstPage, err := repository.Papers(ctx, PaperListQuery{
		Topic: "agents",
		Sort:  PaperSortPublishedAtDesc,
		Limit: 1,
	})
	if err != nil {
		t.Fatalf("Papers(first page) error = %v", err)
	}
	if !firstPage.Pagination.HasMore || firstPage.Pagination.NextCursor == "" {
		t.Fatalf("first page pagination = %#v, want signed continuation", firstPage.Pagination)
	}

	secondPage, err := repository.Papers(ctx, PaperListQuery{
		Topic:  "agents",
		Sort:   PaperSortPublishedAtDesc,
		Limit:  1,
		Cursor: firstPage.Pagination.NextCursor,
	})
	if err != nil {
		t.Fatalf("Papers(second page) error = %v", err)
	}
	assertPaperIDs(t, secondPage.Items, firstFixture.knownFalsePaperID)

	cursorParts := strings.Split(firstPage.Pagination.NextCursor, ".")
	if len(cursorParts) != 2 || cursorParts[1] == "" {
		t.Fatalf("cursor = %q, want payload.signature", firstPage.Pagination.NextCursor)
	}
	replacement := byte('A')
	if cursorParts[1][0] == replacement {
		replacement = 'B'
	}
	cursorParts[1] = string(replacement) + cursorParts[1][1:]
	tampered := strings.Join(cursorParts, ".")
	_, err = repository.Papers(ctx, PaperListQuery{
		Topic:  "agents",
		Sort:   PaperSortPublishedAtDesc,
		Limit:  1,
		Cursor: tampered,
	})
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("Papers(tampered cursor) error = %v, want ErrInvalidCursor", err)
	}

	_, err = repository.Papers(ctx, PaperListQuery{
		Topic:  "different-filter",
		Sort:   PaperSortPublishedAtDesc,
		Limit:  1,
		Cursor: firstPage.Pagination.NextCursor,
	})
	if !errors.Is(err, ErrCursorConflict) {
		t.Fatalf("Papers(filter mismatch) error = %v, want ErrCursorConflict", err)
	}

	insertCatalogFixture(t, pool, fixtureOptions{
		generationTime: time.Date(2026, time.July, 16, 4, 0, 0, 0, time.UTC),
		sourceRevision: "revision-2",
	})
	_, err = repository.Papers(ctx, PaperListQuery{
		Topic:  "agents",
		Sort:   PaperSortPublishedAtDesc,
		Limit:  1,
		Cursor: firstPage.Pagination.NextCursor,
	})
	if !errors.Is(err, ErrCursorConflict) {
		t.Fatalf("Papers(generation mismatch) error = %v, want ErrCursorConflict", err)
	}
}

func TestTaxonomyTrendAndOpportunityQueriesUseBoundedCursors(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertCatalogFixture(t, pool, fixtureOptions{})
	repository := mustRepository(t, pool)
	ctx := context.Background()

	topics, err := repository.Topics(ctx, PageQuery{Limit: 1})
	if err != nil {
		t.Fatalf("Topics() error = %v", err)
	}
	if len(topics.Items) != 1 || topics.Pagination.NextCursor == "" {
		t.Fatalf("Topics() = %#v, want first bounded page with cursor", topics)
	}

	methods, err := repository.Methods(ctx, PageQuery{Limit: 20})
	if err != nil {
		t.Fatalf("Methods() error = %v", err)
	}
	if len(methods.Items) != 2 {
		t.Fatalf("Methods() item count = %d, want 2", len(methods.Items))
	}

	trends, err := repository.Trends(ctx, TrendKindPapers, TrendQuery{
		WindowDays: 30,
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("Trends(papers) error = %v", err)
	}
	if trends.WindowDays != 30 || len(trends.Items) != 1 ||
		trends.Pagination.NextCursor == "" {
		t.Fatalf("Trends(papers) = %#v, want bounded 30-day page", trends)
	}
	assertJSONField(t, trends.Items[0], "subject_id", fixture.knownPaperID.String())

	opportunities, err := repository.ResearchOpportunities(ctx, OpportunityQuery{
		Status: "insufficient_evidence",
		Limit:  20,
	})
	if err != nil {
		t.Fatalf("ResearchOpportunities() error = %v", err)
	}
	if len(opportunities.Items) != 1 {
		t.Fatalf(
			"ResearchOpportunities() item count = %d, want 1",
			len(opportunities.Items),
		)
	}
	assertJSONField(t, opportunities.Items[0], "status", "insufficient_evidence")
}

func TestCompletedAnalysisRunCannotBeMutated(t *testing.T) {
	pool := openCatalogTestPool(t)
	ctx := context.Background()
	var runID uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO analysis_runs (
			analysis_type,
			model_provider,
			model_name,
			prompt_version,
			status,
			input_payload,
			output_payload,
			started_at,
			completed_at
		) VALUES (
			'public_catalog',
			'internal',
			'deterministic-formula',
			'v1',
			'succeeded',
			'{"source_revision":"revision-1"}',
			'{"result":"complete"}',
			now() - interval '1 minute',
			now()
		)
		RETURNING id
	`).Scan(&runID)
	if err != nil {
		t.Fatalf("insert completed analysis run: %v", err)
	}

	for _, query := range []string{
		`UPDATE analysis_runs SET output_payload = '{"result":"changed"}' WHERE id = $1`,
		`DELETE FROM analysis_runs WHERE id = $1`,
	} {
		if _, err := pool.Exec(ctx, query, runID); err == nil {
			t.Fatalf("completed analysis run mutation succeeded: %s", query)
		}
	}
}

type fixtureOptions struct {
	generationTime time.Time
	sourceRevision string
}

type catalogFixture struct {
	generationID      uuid.UUID
	knownPaperID      uuid.UUID
	knownFalsePaperID uuid.UUID
	hiddenPaperID     uuid.UUID
}

func insertCatalogFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	options fixtureOptions,
) catalogFixture {
	t.Helper()

	generatedAt := options.generationTime
	if generatedAt.IsZero() {
		generatedAt = time.Date(2026, time.July, 16, 3, 0, 0, 0, time.UTC)
	}
	sourceRevision := options.sourceRevision
	if sourceRevision == "" {
		sourceRevision = "revision-1"
	}

	fixture := catalogFixture{
		generationID:      uuid.New(),
		knownPaperID:      uuid.New(),
		knownFalsePaperID: uuid.New(),
		hiddenPaperID:     uuid.New(),
	}
	topicAgentsID := uuid.New()
	topicSafetyID := uuid.New()
	methodCausalID := uuid.New()
	methodRAGID := uuid.New()
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin catalog fixture transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_generations (
			id, source_revision, formula_version, generated_at, metadata
		) VALUES ($1, $2, 'public-catalog/v1', $3, '{"scope":"demo"}')
	`, fixture.generationID, sourceRevision, generatedAt); err != nil {
		t.Fatalf("insert catalog generation: %v", err)
	}

	statsPayload := fmt.Sprintf(
		`{
			"generated_at":%q,
			"papers_total":3,
			"papers_last_7_days":{"state":"known","value":2},
			"topics_total":{"state":"known","value":2},
			"methods_total":{"state":"known","value":2},
			"with_code_ratio":{"state":"known","value":0.5},
			"with_data_ratio":{"state":"unknown"},
			"with_benchmark_ratio":{"state":"missing"}
		}`,
		generatedAt.Format(time.RFC3339Nano),
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_stats (generation_id, payload)
		VALUES ($1, $2::jsonb)
	`, fixture.generationID, statsPayload); err != nil {
		t.Fatalf("insert catalog stats: %v", err)
	}

	taxonomyRows := []struct {
		table      string
		id         uuid.UUID
		slug       string
		name       string
		paperCount int
	}{
		{"public_catalog_topics", topicAgentsID, "agents", "Agents", 2},
		{"public_catalog_topics", topicSafetyID, "safety", "Safety", 1},
		{"public_catalog_methods", methodCausalID, "causal-inference", "Causal Inference", 2},
		{"public_catalog_methods", methodRAGID, "rag", "RAG", 1},
	}
	for _, row := range taxonomyRows {
		payload := fmt.Sprintf(
			`{"id":%q,"slug":%q,"name":%q,"description":{"state":"known","value":"Demo"},"paper_count":%d}`,
			row.id,
			row.slug,
			row.name,
			row.paperCount,
		)
		query := fmt.Sprintf(`
			INSERT INTO %s (
				generation_id, taxonomy_id, slug, name, paper_count,
				summary_payload, detail_payload
			) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $6::jsonb)
		`, row.table)
		if _, err := tx.Exec(
			ctx,
			query,
			fixture.generationID,
			row.id,
			row.slug,
			row.name,
			row.paperCount,
			payload,
		); err != nil {
			t.Fatalf("insert %s row %q: %v", row.table, row.slug, err)
		}
	}

	type paperRow struct {
		id                 uuid.UUID
		canonicalKey       string
		title              string
		searchText         string
		publishedState     string
		publishedAt        *time.Time
		paperTypeState     string
		paperType          *string
		hasCodeState       string
		hasCode            *bool
		hasDataState       string
		hasData            *bool
		hasBenchmarkState  string
		hasBenchmark       *bool
		citationCountState string
		citationCount      *int64
		trendScoreState    string
		trendScore         *string
		topicSlugs         []string
		methodSlugs        []string
		sourceNames        []string
	}
	researchArticle := "research_article"
	preprint := "preprint"
	yes := true
	no := false
	citations42 := int64(42)
	citations3 := int64(3)
	score090 := "0.90"
	score020 := "0.20"
	publishedKnown := time.Date(2026, time.July, 15, 12, 0, 0, 0, time.UTC)
	publishedKnownFalse := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	papers := []paperRow{
		{
			id:                 fixture.knownPaperID,
			canonicalKey:       "openalex:W100001",
			title:              "Causal Agents",
			searchText:         "Causal Agents Ada Lovelace openalex W100001",
			publishedState:     "known",
			publishedAt:        &publishedKnown,
			paperTypeState:     "known",
			paperType:          &researchArticle,
			hasCodeState:       "known",
			hasCode:            &yes,
			hasDataState:       "unknown",
			hasBenchmarkState:  "missing",
			citationCountState: "known",
			citationCount:      &citations42,
			trendScoreState:    "known",
			trendScore:         &score090,
			topicSlugs:         []string{"agents"},
			methodSlugs:        []string{"causal-inference"},
			sourceNames:        []string{"openalex", "crossref"},
		},
		{
			id:                 fixture.knownFalsePaperID,
			canonicalKey:       "openalex:W100002",
			title:              "Agent Retrieval Baseline",
			searchText:         "Agent Retrieval Baseline Grace Hopper openalex W100002",
			publishedState:     "known",
			publishedAt:        &publishedKnownFalse,
			paperTypeState:     "known",
			paperType:          &preprint,
			hasCodeState:       "known",
			hasCode:            &no,
			hasDataState:       "known",
			hasData:            &no,
			hasBenchmarkState:  "known",
			hasBenchmark:       &no,
			citationCountState: "known",
			citationCount:      &citations3,
			trendScoreState:    "known",
			trendScore:         &score020,
			topicSlugs:         []string{"agents"},
			methodSlugs:        []string{"rag"},
			sourceNames:        []string{"openalex"},
		},
		{
			id:                 uuid.New(),
			canonicalKey:       "pubmed:12345678",
			title:              "Unresolved Safety Evidence",
			searchText:         "Unresolved Safety Evidence pubmed 12345678",
			publishedState:     "missing",
			paperTypeState:     "missing",
			hasCodeState:       "unknown",
			hasDataState:       "missing",
			hasBenchmarkState:  "known",
			hasBenchmark:       &no,
			citationCountState: "unknown",
			trendScoreState:    "missing",
			topicSlugs:         []string{"safety"},
			methodSlugs:        []string{"causal-inference"},
			sourceNames:        []string{"pubmed"},
		},
	}
	for _, row := range papers {
		summaryPayload := paperPayload(row)
		if _, err := tx.Exec(ctx, `
			INSERT INTO public_catalog_papers (
				generation_id,
				paper_id,
				canonical_key,
				title,
				search_text,
				published_at_state,
				published_at,
				paper_type_state,
				paper_type,
				lifecycle_status,
				topic_slugs,
				method_slugs,
				source_names,
				has_code_state,
				has_code_value,
				has_data_state,
				has_data_value,
				has_benchmark_state,
				has_benchmark_value,
				citation_count_state,
				citation_count_value,
				trend_score_state,
				trend_score_value,
				summary_payload,
				detail_payload
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8, $9, 'active',
				$10, $11, $12, $13, $14, $15, $16, $17, $18,
				$19, $20, $21, $22, $23::jsonb, $23::jsonb
			)
		`,
			fixture.generationID,
			row.id,
			row.canonicalKey,
			row.title,
			row.searchText,
			row.publishedState,
			row.publishedAt,
			row.paperTypeState,
			row.paperType,
			row.topicSlugs,
			row.methodSlugs,
			row.sourceNames,
			row.hasCodeState,
			row.hasCode,
			row.hasDataState,
			row.hasData,
			row.hasBenchmarkState,
			row.hasBenchmark,
			row.citationCountState,
			row.citationCount,
			row.trendScoreState,
			row.trendScore,
			summaryPayload,
		); err != nil {
			t.Fatalf("insert catalog paper %q: %v", row.title, err)
		}
	}

	for _, trend := range []struct {
		kind      string
		rank      int
		subjectID uuid.UUID
		score     string
	}{
		{"papers", 1, fixture.knownPaperID, "0.90"},
		{"papers", 2, fixture.knownFalsePaperID, "0.20"},
		{"topics", 1, topicAgentsID, "0.80"},
		{"methods", 1, methodCausalID, "0.70"},
	} {
		payload := fmt.Sprintf(
			`{"rank":%d,"score":%s,"subject_id":%q,"ranking":{"coverage":{"state":"known","value":1},"missing_signals":[]}}`,
			trend.rank,
			trend.score,
			trend.subjectID,
		)
		if _, err := tx.Exec(ctx, `
			INSERT INTO public_catalog_trends (
				generation_id, trend_kind, window_days, rank, subject_id, score, payload
			) VALUES ($1, $2, 30, $3, $4, $5, $6::jsonb)
		`,
			fixture.generationID,
			trend.kind,
			trend.rank,
			trend.subjectID,
			trend.score,
			payload,
		); err != nil {
			t.Fatalf("insert %s trend rank %d: %v", trend.kind, trend.rank, err)
		}
	}

	for ordinal, opportunity := range []struct {
		status string
		title  string
	}{
		{"worth_pursuing", "Causal agent evaluation"},
		{"insufficient_evidence", "Long-horizon agent safety"},
	} {
		opportunityID := uuid.New()
		payload := fmt.Sprintf(
			`{
				"id":%q,
				"title":%q,
				"status":%q,
				"evidence_ids":[%q],
				"growth_score":{"state":"known","value":0.6},
				"competition_density":{"state":"unknown"},
				"data_availability":{"state":"missing"},
				"reproducibility":{"state":"known","value":0.5},
				"missing_signals":["competition_density","data_availability"],
				"formula_version":"opportunity/v1",
				"generated_at":%q
			}`,
			opportunityID,
			opportunity.title,
			opportunity.status,
			fixture.knownPaperID,
			generatedAt.Format(time.RFC3339Nano),
		)
		if _, err := tx.Exec(ctx, `
			INSERT INTO public_catalog_research_opportunities (
				generation_id, opportunity_id, status, ordinal, payload
			) VALUES ($1, $2, $3, $4, $5::jsonb)
		`,
			fixture.generationID,
			opportunityID,
			opportunity.status,
			ordinal+1,
			payload,
		); err != nil {
			t.Fatalf("insert research opportunity %q: %v", opportunity.title, err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_publications (generation_id, published_at)
		VALUES ($1, $2)
	`, fixture.generationID, generatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("publish catalog generation: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_current (singleton, generation_id)
		VALUES (true, $1)
		ON CONFLICT (singleton) DO UPDATE
		SET generation_id = EXCLUDED.generation_id
	`, fixture.generationID); err != nil {
		t.Fatalf("set current catalog generation: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit catalog fixture: %v", err)
	}
	return fixture
}

func paperPayload(row struct {
	id                 uuid.UUID
	canonicalKey       string
	title              string
	searchText         string
	publishedState     string
	publishedAt        *time.Time
	paperTypeState     string
	paperType          *string
	hasCodeState       string
	hasCode            *bool
	hasDataState       string
	hasData            *bool
	hasBenchmarkState  string
	hasBenchmark       *bool
	citationCountState string
	citationCount      *int64
	trendScoreState    string
	trendScore         *string
	topicSlugs         []string
	methodSlugs        []string
	sourceNames        []string
}) string {
	payload := map[string]any{
		"id":                row.id,
		"title":             row.title,
		"status":            "active",
		"published_at":      dataValue(row.publishedState, row.publishedAt),
		"type":              dataValue(row.paperTypeState, row.paperType),
		"has_code":          dataValue(row.hasCodeState, row.hasCode),
		"has_data":          dataValue(row.hasDataState, row.hasData),
		"has_benchmark":     dataValue(row.hasBenchmarkState, row.hasBenchmark),
		"citation_count":    dataValue(row.citationCountState, row.citationCount),
		"trend_score":       dataValue(row.trendScoreState, row.trendScore),
		"topics":            row.topicSlugs,
		"methods":           row.methodSlugs,
		"source_provenance": row.sourceNames,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func dataValue(state string, value any) map[string]any {
	result := map[string]any{"state": state}
	if state == "known" {
		switch typed := value.(type) {
		case *time.Time:
			result["value"] = typed.Format(time.RFC3339Nano)
		case *string:
			result["value"] = *typed
		case *bool:
			result["value"] = *typed
		case *int64:
			result["value"] = *typed
		default:
			result["value"] = value
		}
	}
	return result
}

func mustRepository(t *testing.T, pool *pgxpool.Pool) *Repository {
	t.Helper()

	repository, err := NewRepository(pool, catalogCursorSecret)
	if err != nil {
		t.Fatalf("NewRepository() error = %v", err)
	}
	return repository
}

func assertPaperIDs(t *testing.T, items []json.RawMessage, want ...uuid.UUID) {
	t.Helper()

	got := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		var payload struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal(item, &payload); err != nil {
			t.Fatalf("decode paper payload: %v", err)
		}
		got = append(got, payload.ID)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("paper IDs = %v, want %v", got, want)
	}
}

func assertJSONField(t *testing.T, payload json.RawMessage, field string, want any) {
	t.Helper()

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode JSON payload: %v", err)
	}
	if fmt.Sprint(decoded[field]) != fmt.Sprint(want) {
		t.Fatalf("%s = %#v, want %#v; payload=%s", field, decoded[field], want, payload)
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}

func TestNewRepositoryRejectsWeakCursorSecrets(t *testing.T) {
	pool := openCatalogTestPool(t)

	for _, secret := range [][]byte{nil, []byte("short"), []byte(strings.Repeat("a", 31))} {
		if _, err := NewRepository(pool, secret); err == nil {
			t.Fatalf("NewRepository accepted %d-byte cursor secret", len(secret))
		}
	}
}
