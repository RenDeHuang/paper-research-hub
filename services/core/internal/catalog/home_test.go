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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBiomedicalCatalogPublicationRequiresCompleteGenerationCoverage(t *testing.T) {
	pool := openCatalogTestPool(t)
	ctx := context.Background()

	missingSnapshotGeneration := insertBiomedicalGenerationBase(
		t,
		pool,
		"biomedical-missing-snapshots",
	)
	_, err := pool.Exec(ctx, `
		INSERT INTO public_catalog_publications (generation_id, published_at)
		VALUES ($1, now())
	`, missingSnapshotGeneration)
	assertCatalogConstraint(
		t,
		err,
		"public_catalog_publications_requires_biomedical_snapshots",
	)

	emptyGeneration := insertBiomedicalGenerationBase(
		t,
		pool,
		"biomedical-empty-catalogs",
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO public_catalog_home (generation_id, payload)
		VALUES ($1, '{"catalog_generation":"empty"}')
	`, emptyGeneration); err != nil {
		t.Fatalf("insert empty-generation Home snapshot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public_catalog_biomedical_manifest (
			generation_id,
			subject_count,
			subject_list_payload,
			journal_count,
			journal_list_payload
		) VALUES (
			$1,
			0,
			'{"analysis":{"coverage_ratio":{"state":"known","value":1},"generated_at":{"state":"known","value":"2026-07-17T08:30:00Z"},"missing_signals":[],"sample_size":{"state":"known","value":0},"sources":{"state":"known","value":[]},"window_days":{"state":"known","value":30}},"jcr_metric_year":2025,"taxonomy_version":"biomedical-jcr-subjects/v1"}',
			0,
			'{"analysis":{"coverage_ratio":{"state":"known","value":1},"generated_at":{"state":"known","value":"2026-07-17T08:30:00Z"},"missing_signals":[],"sample_size":{"state":"known","value":0},"sources":{"state":"known","value":[]},"window_days":{"state":"known","value":30}},"jcr_metric_year":2025,"taxonomy_version":"biomedical-jcr-subjects/v1"}'
		)
	`, emptyGeneration); err != nil {
		t.Fatalf("insert empty-generation biomedical manifest: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public_catalog_publications (generation_id, published_at)
		VALUES ($1, now())
	`, emptyGeneration); err != nil {
		t.Fatalf("publish generation with explicit zero-row coverage: %v", err)
	}

	countMismatchGeneration := insertBiomedicalGenerationBase(
		t,
		pool,
		"biomedical-count-mismatch",
	)
	if _, err := pool.Exec(ctx, `
		INSERT INTO public_catalog_home (generation_id, payload)
		VALUES ($1, '{"catalog_generation":"count-mismatch"}')
	`, countMismatchGeneration); err != nil {
		t.Fatalf("insert count-mismatch Home snapshot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public_catalog_biomedical_manifest (
			generation_id,
			subject_count,
			subject_list_payload,
			journal_count,
			journal_list_payload
		) VALUES ($1, 1, '{}', 0, '{}')
	`, countMismatchGeneration); err != nil {
		t.Fatalf("insert count-mismatch biomedical manifest: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO public_catalog_publications (generation_id, published_at)
		VALUES ($1, now())
	`, countMismatchGeneration)
	assertCatalogConstraint(
		t,
		err,
		"public_catalog_publications_biomedical_coverage_matches",
	)
}

func TestHomeReadsStoredPayloadFromCurrentPublishedGeneration(t *testing.T) {
	pool := openCatalogTestPool(t)
	first := insertBiomedicalCatalogFixture(
		t,
		pool,
		biomedicalFixtureOptions{
			sourceRevision: "biomedical-home-generation-1",
			homeMarker:     "first-generation",
		},
	)
	second := insertBiomedicalCatalogFixture(
		t,
		pool,
		biomedicalFixtureOptions{
			sourceRevision: "biomedical-home-generation-2",
			homeMarker:     "current-generation",
		},
	)
	repository := mustRepository(t, pool)

	document, err := repository.Home(context.Background())
	if err != nil {
		t.Fatalf("Home() error = %v", err)
	}
	if document.Generation.ID != second.generationID {
		t.Fatalf(
			"Home() generation = %s, want current %s (not stale %s)",
			document.Generation.ID,
			second.generationID,
			first.generationID,
		)
	}
	assertJSONField(t, document.Payload, "fixture_marker", "current-generation")
}

func TestHomeRequiresPublishedBiomedicalSnapshot(t *testing.T) {
	pool := openCatalogTestPool(t)
	repository := mustRepository(t, pool)

	_, err := repository.Home(context.Background())
	if !errors.Is(err, ErrCatalogNotPublished) {
		t.Fatalf("Home() error = %v, want ErrCatalogNotPublished", err)
	}
}

func TestPublishedBiomedicalSnapshotsAreImmutable(t *testing.T) {
	pool := openCatalogTestPool(t)
	fixture := insertBiomedicalCatalogFixture(
		t,
		pool,
		biomedicalFixtureOptions{
			sourceRevision: "biomedical-immutable",
			homeMarker:     "immutable",
		},
	)
	ctx := context.Background()

	for _, test := range []struct {
		name  string
		query string
	}{
		{
			name: "home update",
			query: `UPDATE public_catalog_home
				SET payload = '{"changed":true}'
				WHERE generation_id = $1`,
		},
		{
			name: "subject delete",
			query: `DELETE FROM public_catalog_subjects
				WHERE generation_id = $1`,
		},
		{
			name: "journal insert",
			query: `INSERT INTO public_catalog_journals (
					generation_id,
					journal_id,
					slug,
					paper_count,
					summary_payload,
					detail_payload
				) VALUES (
					$1,
					gen_random_uuid(),
					'late-journal',
					0,
					'{}',
					'{}'
				)`,
		},
		{
			name: "manifest update",
			query: `UPDATE public_catalog_biomedical_manifest
				SET subject_count = subject_count + 1
				WHERE generation_id = $1`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, test.query, fixture.generationID); err == nil {
				t.Fatalf("%s succeeded against a published generation", test.name)
			}
		})
	}
}

type biomedicalFixtureOptions struct {
	sourceRevision string
	homeMarker     string
}

type biomedicalCatalogFixture struct {
	generationID uuid.UUID
	subjectID    uuid.UUID
	journalID    uuid.UUID
}

func insertBiomedicalCatalogFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	options biomedicalFixtureOptions,
) biomedicalCatalogFixture {
	t.Helper()

	generationID := insertBiomedicalGenerationBase(
		t,
		pool,
		options.sourceRevision,
	)
	fixture := biomedicalCatalogFixture{
		generationID: generationID,
		subjectID:    uuid.New(),
		journalID:    uuid.New(),
	}
	generatedAt := time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin biomedical Catalog fixture: %v", err)
	}
	defer tx.Rollback(context.Background())

	homePayload := fmt.Sprintf(`{
		"active_journals":{"analysis":%s,"items":[]},
		"catalog_generation":%q,
		"citation_momentum":{"analysis":%s,"items":[]},
		"coverage":{"analysis":%s,"citation_coverage_ratio":{"state":"known","value":1},"jcr_metric_year":2025,"mesh_coverage_ratio":{"state":"known","value":1},"publication_type_coverage_ratio":{"state":"known","value":1},"taxonomy_version":"biomedical-jcr-subjects/v1"},
		"entity_momentum":{"analysis":%s,"items":[]},
		"evidence_gaps":[],
		"fixture_marker":%q,
		"generated_at":"2026-07-17T08:30:00Z",
		"latest_papers":{"analysis":%s,"items":[]},
		"research_opportunities":{"analysis":%s,"items":[]},
		"scope":{"jcr_metric_year":2025,"taxonomy_version":"biomedical-jcr-subjects/v1"},
		"subject_momentum":{"analysis":%s,"items":[]}
	}`,
		biomedicalAnalysisJSON(0),
		generationID,
		biomedicalAnalysisJSON(0),
		biomedicalAnalysisJSON(0),
		biomedicalAnalysisJSON(0),
		options.homeMarker,
		biomedicalAnalysisJSON(0),
		biomedicalAnalysisJSON(0),
		biomedicalAnalysisJSON(0),
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_home (generation_id, payload)
		VALUES ($1, $2::jsonb)
	`, generationID, homePayload); err != nil {
		t.Fatalf("insert biomedical Home snapshot: %v", err)
	}

	subjectListPayload := fmt.Sprintf(`{
		"analysis":%s,
		"jcr_metric_year":2025,
		"taxonomy_version":"biomedical-jcr-subjects/v1"
	}`, biomedicalAnalysisJSON(3))
	journalListPayload := fmt.Sprintf(`{
		"analysis":%s,
		"jcr_metric_year":2025,
		"taxonomy_version":"biomedical-jcr-subjects/v1"
	}`, biomedicalAnalysisJSON(3))
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_biomedical_manifest (
			generation_id,
			subject_count,
			subject_list_payload,
			journal_count,
			journal_list_payload
		) VALUES ($1, 3, $2::jsonb, 3, $3::jsonb)
	`, generationID, subjectListPayload, journalListPayload); err != nil {
		t.Fatalf("insert biomedical Catalog manifest: %v", err)
	}

	subjectRows := []struct {
		id         uuid.UUID
		slug       string
		name       string
		paperCount int
	}{
		{uuid.New(), "cardiology", "Cardiology", 20},
		{fixture.subjectID, "oncology", "Oncology", 20},
		{uuid.New(), "neurology", "Neurology", 5},
	}
	for _, row := range subjectRows {
		summary := fmt.Sprintf(`{
			"description":{"state":"known","value":"%s snapshot"},
			"id":%q,
			"jcr_metric_year":2025,
			"journal_count":{"state":"known","value":2},
			"name":%q,
			"paper_count":{"state":"known","value":%d},
			"slug":%q,
			"taxonomy_version":"biomedical-jcr-subjects/v1"
		}`, row.name, row.id, row.name, row.paperCount, row.slug)
		detail := summary
		if row.slug == "oncology" {
			detail = biomedicalSubjectDetailJSON(row.id)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO public_catalog_subjects (
				generation_id,
				subject_id,
				slug,
				paper_count,
				summary_payload,
				detail_payload
			) VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb)
		`, generationID, row.id, row.slug, row.paperCount, summary, detail); err != nil {
			t.Fatalf("insert biomedical Subject %q: %v", row.slug, err)
		}
	}

	journalRows := []struct {
		id         uuid.UUID
		slug       string
		title      string
		paperCount int
	}{
		{uuid.New(), "annals-of-biomedicine", "Annals of Biomedicine", 20},
		{fixture.journalID, "journal-of-clinical-oncology", "Journal of Clinical Oncology", 20},
		{uuid.New(), "neurology-reports", "Neurology Reports", 5},
	}
	for _, row := range journalRows {
		summary := fmt.Sprintf(`{
			"id":%q,
			"jcr_metric_year":2025,
			"jif":{"state":"known","value":12.5},
			"paper_count":{"state":"known","value":%d},
			"slug":%q,
			"taxonomy_version":"biomedical-jcr-subjects/v1",
			"title":%q
		}`, row.id, row.paperCount, row.slug, row.title)
		detail := summary
		if row.slug == "journal-of-clinical-oncology" {
			detail = biomedicalJournalDetailJSON(row.id)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO public_catalog_journals (
				generation_id,
				journal_id,
				slug,
				paper_count,
				summary_payload,
				detail_payload
			) VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb)
		`, generationID, row.id, row.slug, row.paperCount, summary, detail); err != nil {
			t.Fatalf("insert biomedical Journal %q: %v", row.slug, err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_publications (generation_id, published_at)
		VALUES ($1, $2)
	`, generationID, generatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("publish biomedical Catalog fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_current (singleton, generation_id)
		VALUES (true, $1)
		ON CONFLICT (singleton) DO UPDATE
		SET generation_id = EXCLUDED.generation_id
	`, generationID); err != nil {
		t.Fatalf("set current biomedical Catalog generation: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit biomedical Catalog fixture: %v", err)
	}
	return fixture
}

func insertBiomedicalGenerationBase(
	t *testing.T,
	pool *pgxpool.Pool,
	sourceRevision string,
) uuid.UUID {
	t.Helper()

	generationID := uuid.New()
	generatedAt := time.Date(2026, time.July, 17, 8, 30, 0, 0, time.UTC)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO public_catalog_generations (
			id,
			source_revision,
			formula_version,
			generated_at,
			metadata
		) VALUES (
			$1,
			$2,
			'public-catalog/biomedical-v1',
			$3,
			'{"catalog_profile":"biomedical"}'
		)
	`, generationID, sourceRevision, generatedAt); err != nil {
		t.Fatalf("insert biomedical Catalog generation: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public_catalog_stats (generation_id, payload)
		VALUES ($1, '{"generated_at":"2026-07-17T08:30:00Z"}')
	`, generationID); err != nil {
		t.Fatalf("insert biomedical Catalog stats: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public_catalog_biomedical_coverage (generation_id)
		VALUES ($1)
	`, generationID); err != nil {
		t.Fatalf("declare biomedical Catalog snapshot coverage: %v", err)
	}
	return generationID
}

func biomedicalAnalysisJSON(sampleSize int) string {
	return fmt.Sprintf(`{
		"coverage_ratio":{"state":"known","value":1},
		"generated_at":{"state":"known","value":"2026-07-17T08:30:00Z"},
		"missing_signals":[],
		"sample_size":{"state":"known","value":%d},
		"sources":{"state":"known","value":["test-fixture"]},
		"window_days":{"state":"known","value":30}
	}`, sampleSize)
}

func biomedicalSubjectDetailJSON(subjectID uuid.UUID) string {
	return fmt.Sprintf(`{
		"active_journals":{"analysis":%s,"items":[]},
		"description":{"state":"known","value":"Oncology snapshot"},
		"evidence_gaps":[],
		"id":%q,
		"jcr_metric_year":2025,
		"journal_count":{"state":"known","value":2},
		"mesh_distribution":{"analysis":%s,"items":[]},
		"name":"Oncology",
		"paper_count":{"state":"known","value":20},
		"publication_type_distribution":{"analysis":%s,"items":[]},
		"recent_papers":{
			"analysis":%s,
			"items":[
				{"has_benchmark":{"state":"known","value":false},"has_code":{"state":"known","value":false},"has_data":{"state":"known","value":true},"id":"00000000-0000-0000-0000-000000000101","published_at":{"state":"known","value":"2026-07-17T08:00:00Z"},"status":"active","title":"Oncology Paper A","type":{"state":"known","value":"research_article"}},
				{"has_benchmark":{"state":"known","value":false},"has_code":{"state":"known","value":false},"has_data":{"state":"known","value":true},"id":"00000000-0000-0000-0000-000000000102","published_at":{"state":"known","value":"2026-07-17T07:00:00Z"},"status":"active","title":"Oncology Paper B","type":{"state":"known","value":"research_article"}},
				{"has_benchmark":{"state":"known","value":false},"has_code":{"state":"known","value":false},"has_data":{"state":"known","value":true},"id":"00000000-0000-0000-0000-000000000103","published_at":{"state":"known","value":"2026-07-17T06:00:00Z"},"status":"active","title":"Oncology Paper C","type":{"state":"known","value":"research_article"}}
			],
			"pagination":{"has_more":false,"limit":3,"next_cursor":null,"total":3}
		},
		"slug":"oncology",
		"taxonomy_version":"biomedical-jcr-subjects/v1",
		"trend_estimates":{"analysis":%s,"items":[]}
	}`,
		biomedicalAnalysisJSON(0),
		subjectID,
		biomedicalAnalysisJSON(0),
		biomedicalAnalysisJSON(0),
		biomedicalAnalysisJSON(3),
		biomedicalAnalysisJSON(0),
	)
}

func biomedicalJournalDetailJSON(journalID uuid.UUID) string {
	return fmt.Sprintf(`{
		"curation":{"assessed_at":"2026-07-17T08:00:00Z","decision":"accepted","evidence":[],"matched_rules":["jcr_q1"],"metric_year":2025,"policy_name":"biomedical-journal-admission","policy_version":1},
		"editorial_patterns":{"analysis":%s,"items":[]},
		"evidence_gaps":[],
		"id":%q,
		"jcr_metric_year":2025,
		"jif":{"state":"known","value":45.3},
		"recent_papers":{
			"analysis":%s,
			"items":[
				{"has_benchmark":{"state":"known","value":false},"has_code":{"state":"known","value":false},"has_data":{"state":"known","value":true},"id":"00000000-0000-0000-0000-000000000201","published_at":{"state":"known","value":"2026-07-17T08:00:00Z"},"status":"active","title":"Journal Paper A","type":{"state":"known","value":"research_article"}},
				{"has_benchmark":{"state":"known","value":false},"has_code":{"state":"known","value":false},"has_data":{"state":"known","value":true},"id":"00000000-0000-0000-0000-000000000202","published_at":{"state":"known","value":"2026-07-17T07:00:00Z"},"status":"active","title":"Journal Paper B","type":{"state":"known","value":"research_article"}}
			],
			"pagination":{"has_more":false,"limit":2,"next_cursor":null,"total":2}
		},
		"slug":"journal-of-clinical-oncology",
		"taxonomy_version":"biomedical-jcr-subjects/v1",
		"title":"Journal of Clinical Oncology"
	}`,
		biomedicalAnalysisJSON(0),
		journalID,
		biomedicalAnalysisJSON(2),
	)
}

func assertCatalogConstraint(t *testing.T, err error, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("operation succeeded, want PostgreSQL constraint %s", constraint)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want PostgreSQL constraint %s", err, constraint)
	}
	if pgErr.ConstraintName != constraint &&
		!strings.Contains(pgErr.Message, constraint) {
		t.Fatalf(
			"PostgreSQL error constraint = %q message=%q, want %s",
			pgErr.ConstraintName,
			pgErr.Message,
			constraint,
		)
	}
}

func decodePayloadObject(t *testing.T, payload json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode payload object: %v", err)
	}
	return decoded
}
