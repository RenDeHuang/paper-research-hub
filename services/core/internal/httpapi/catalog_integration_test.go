package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/catalog"
)

var httpAPICursorSecret = []byte("http-api-catalog-cursor-secret-32-bytes")

func TestPublicCatalogGETRoutesReadPublishedGeneration(t *testing.T) {
	pool := openHTTPAPITestPool(t)
	fixture := insertHTTPAPICatalogFixture(t, pool)
	handler := newCatalogTestHandler(t, pool)

	for _, target := range []string{
		"/api/v1/stats",
		"/api/v1/papers?limit=1",
		"/api/v1/papers/" + fixture.paperID.String(),
		"/api/v1/topics?limit=1",
		"/api/v1/topics/agents",
		"/api/v1/methods?limit=1",
		"/api/v1/methods/causal-inference",
		"/api/v1/trends/papers?window_days=30&limit=1",
		"/api/v1/trends/topics?window_days=30&limit=1",
		"/api/v1/trends/methods?window_days=30&limit=1",
		"/api/v1/research-opportunities?limit=1",
	} {
		t.Run(target, func(t *testing.T) {
			response := serveWithHandler(handler, http.MethodGet, target)
			if response.Code != http.StatusOK {
				t.Fatalf("GET %s status = %d, want 200; body=%s", target, response.Code, response.Body)
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
				t.Fatalf("GET %s Content-Type = %q, want application/json", target, contentType)
			}
			if generation := response.Header().Get("X-Catalog-Generation"); generation != fixture.generationID.String() {
				t.Fatalf(
					"GET %s X-Catalog-Generation = %q, want %s",
					target,
					generation,
					fixture.generationID,
				)
			}

			var body any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("GET %s response is not JSON: %v", target, err)
			}
		})
	}
}

func TestPublicCatalogRoutesReturn503BeforePublication(t *testing.T) {
	pool := openHTTPAPITestPool(t)
	handler := newCatalogTestHandler(t, pool)

	for _, target := range []string{
		"/api/v1/stats",
		"/api/v1/papers",
		"/api/v1/papers/" + uuid.NewString(),
		"/api/v1/topics",
		"/api/v1/topics/agents",
		"/api/v1/methods",
		"/api/v1/methods/causal-inference",
		"/api/v1/trends/papers",
		"/api/v1/trends/topics",
		"/api/v1/trends/methods",
		"/api/v1/research-opportunities",
	} {
		t.Run(target, func(t *testing.T) {
			response := serveWithHandler(handler, http.MethodGet, target)
			assertProblem(t, response, http.StatusServiceUnavailable, "catalog_not_published")
		})
	}
}

func TestKnownPublicCatalogPathsRejectNonGETWithProblemDetails(t *testing.T) {
	pool := openHTTPAPITestPool(t)
	handler := newCatalogTestHandler(t, pool)

	for _, target := range []string{
		"/api/v1/stats",
		"/api/v1/papers",
		"/api/v1/papers/" + uuid.NewString(),
		"/api/v1/topics",
		"/api/v1/topics/agents",
		"/api/v1/methods",
		"/api/v1/methods/causal-inference",
		"/api/v1/trends/papers",
		"/api/v1/trends/topics",
		"/api/v1/trends/methods",
		"/api/v1/research-opportunities",
	} {
		t.Run(target, func(t *testing.T) {
			response := serveWithHandler(handler, http.MethodPost, target)
			assertProblem(t, response, http.StatusMethodNotAllowed, "method_not_allowed")
			if allow := response.Header().Get("Allow"); allow != http.MethodGet {
				t.Fatalf("POST %s Allow = %q, want GET", target, allow)
			}
		})
	}
}

func TestPaperDetailUsesIdentical404ForInvisibleMissingAndMalformedIDs(t *testing.T) {
	pool := openHTTPAPITestPool(t)
	fixture := insertHTTPAPICatalogFixture(t, pool)
	handler := newCatalogTestHandler(t, pool)

	targets := []string{
		"/api/v1/papers/" + fixture.hiddenPaperID.String(),
		"/api/v1/papers/" + uuid.NewString(),
		"/api/v1/papers/not-a-uuid",
	}
	var first problemDetails
	for index, target := range targets {
		response := serveWithHandler(handler, http.MethodGet, target)
		problem := assertProblem(t, response, http.StatusNotFound, "resource_not_found")
		if index == 0 {
			first = problem
			continue
		}
		if problem.Title != first.Title || problem.Detail != first.Detail || problem.Code != first.Code {
			t.Fatalf(
				"GET %s problem = %#v, want same public 404 semantics as %#v",
				target,
				problem,
				first,
			)
		}
	}
}

func TestPaperListMapsCursorValidationAndContextConflicts(t *testing.T) {
	pool := openHTTPAPITestPool(t)
	insertHTTPAPICatalogFixture(t, pool)
	handler := newCatalogTestHandler(t, pool)

	first := serveWithHandler(
		handler,
		http.MethodGet,
		"/api/v1/papers?topic=agents&limit=1",
	)
	if first.Code != http.StatusOK {
		t.Fatalf("first page status = %d, want 200; body=%s", first.Code, first.Body)
	}
	var page struct {
		Pagination struct {
			NextCursor *string `json:"next_cursor"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode first page: %v", err)
	}
	if page.Pagination.NextCursor == nil || *page.Pagination.NextCursor == "" {
		t.Fatal("first page did not return a continuation cursor")
	}

	mismatch := serveWithHandler(
		handler,
		http.MethodGet,
		"/api/v1/papers?topic=safety&limit=1&cursor="+
			urlQueryEscape(*page.Pagination.NextCursor),
	)
	assertProblem(t, mismatch, http.StatusConflict, "cursor_context_mismatch")

	cursor := *page.Pagination.NextCursor
	tampered := cursor[:len(cursor)-1] + "A"
	invalid := serveWithHandler(
		handler,
		http.MethodGet,
		"/api/v1/papers?topic=agents&limit=1&cursor="+urlQueryEscape(tampered),
	)
	assertProblem(t, invalid, http.StatusUnprocessableEntity, "invalid_cursor")
}

func TestPublicCatalogQueryValidationIsStrict(t *testing.T) {
	pool := openHTTPAPITestPool(t)
	insertHTTPAPICatalogFixture(t, pool)
	handler := newCatalogTestHandler(t, pool)

	for _, target := range []string{
		"/api/v1/papers?q=%20%20",
		"/api/v1/papers?limit=0",
		"/api/v1/papers?has_code=1",
		"/api/v1/papers?unknown_parameter=value",
		"/api/v1/topics?limit=1&limit=2",
		"/api/v1/trends/papers?window_days=0",
		"/api/v1/research-opportunities?status=unknown",
	} {
		t.Run(target, func(t *testing.T) {
			response := serveWithHandler(handler, http.MethodGet, target)
			assertProblem(t, response, http.StatusUnprocessableEntity, "validation_error")
		})
	}
}

func TestUnknownAPIRouteKeepsDistinctStableCode(t *testing.T) {
	pool := openHTTPAPITestPool(t)
	handler := newCatalogTestHandler(t, pool)

	response := serveWithHandler(handler, http.MethodGet, "/api/v1/trends/unknown")
	assertProblem(t, response, http.StatusNotFound, "route_not_found")
}

type httpAPICatalogFixture struct {
	generationID  uuid.UUID
	paperID       uuid.UUID
	hiddenPaperID uuid.UUID
}

func insertHTTPAPICatalogFixture(
	t *testing.T,
	pool *pgxpool.Pool,
) httpAPICatalogFixture {
	t.Helper()

	fixture := httpAPICatalogFixture{
		generationID:  uuid.New(),
		paperID:       uuid.New(),
		hiddenPaperID: uuid.New(),
	}
	secondPaperID := uuid.New()
	topicID := uuid.New()
	methodID := uuid.New()
	generatedAt := time.Date(2026, time.July, 16, 5, 0, 0, 0, time.UTC)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin HTTP API fixture transaction: %v", err)
	}
	defer tx.Rollback(context.Background())

	if _, err := tx.Exec(ctx, `
		INSERT INTO works (id, canonical_key, status, title)
		VALUES ($1, 'openalex:W800001', 'active', 'Invisible base work')
	`, fixture.hiddenPaperID); err != nil {
		t.Fatalf("insert invisible base work: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_generations (
			id, source_revision, formula_version, generated_at
		) VALUES ($1, 'http-api-fixture', 'public-catalog/v1', $2)
	`, fixture.generationID, generatedAt); err != nil {
		t.Fatalf("insert HTTP API catalog generation: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_stats (generation_id, payload)
		VALUES (
			$1,
			'{
				"generated_at":"2026-07-16T05:00:00Z",
				"papers_total":{"state":"known","value":2},
				"papers_last_7_days":{"state":"known","value":2},
				"topics_total":{"state":"known","value":1},
				"methods_total":{"state":"known","value":1},
				"with_code_ratio":{"state":"known","value":0.5},
				"with_data_ratio":{"state":"unknown"},
				"with_benchmark_ratio":{"state":"missing"}
			}'
		)
	`, fixture.generationID); err != nil {
		t.Fatalf("insert HTTP API stats: %v", err)
	}

	topicPayload := fmt.Sprintf(
		`{"id":%q,"slug":"agents","name":"Agents","description":{"state":"known","value":"Agent systems"},"paper_count":2}`,
		topicID,
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_topics (
			generation_id, taxonomy_id, slug, name, paper_count,
			summary_payload, detail_payload
		) VALUES ($1, $2, 'agents', 'Agents', 2, $3::jsonb, $3::jsonb)
	`, fixture.generationID, topicID, topicPayload); err != nil {
		t.Fatalf("insert HTTP API topic: %v", err)
	}
	methodPayload := fmt.Sprintf(
		`{"id":%q,"slug":"causal-inference","name":"Causal Inference","description":{"state":"known","value":"Causal methods"},"paper_count":2}`,
		methodID,
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_methods (
			generation_id, taxonomy_id, slug, name, paper_count,
			summary_payload, detail_payload
		) VALUES ($1, $2, 'causal-inference', 'Causal Inference', 2, $3::jsonb, $3::jsonb)
	`, fixture.generationID, methodID, methodPayload); err != nil {
		t.Fatalf("insert HTTP API method: %v", err)
	}

	for index, paperID := range []uuid.UUID{fixture.paperID, secondPaperID} {
		publishedAt := generatedAt.Add(-time.Duration(index) * time.Hour)
		payload := fmt.Sprintf(
			`{
				"id":%q,
				"title":%q,
				"published_at":{"state":"known","value":%q},
				"type":{"state":"known","value":"research_article"},
				"status":"active",
				"has_code":{"state":"known","value":%t},
				"has_data":{"state":"unknown"},
				"has_benchmark":{"state":"missing"}
			}`,
			paperID,
			fmt.Sprintf("Agent Paper %d", index+1),
			publishedAt.Format(time.RFC3339Nano),
			index == 0,
		)
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
				has_benchmark_state,
				citation_count_state,
				trend_score_state,
				summary_payload,
				detail_payload
			) VALUES (
				$1, $2, $3, $4, $4, 'known', $5, 'known', 'research_article',
				'active', '{agents}', '{causal-inference}', '{openalex}',
				'known', $6, 'unknown', 'missing', 'unknown', 'missing',
				$7::jsonb, $7::jsonb
			)
		`,
			fixture.generationID,
			paperID,
			fmt.Sprintf("openalex:W90000%d", index+1),
			fmt.Sprintf("Agent Paper %d", index+1),
			publishedAt,
			index == 0,
			payload,
		); err != nil {
			t.Fatalf("insert HTTP API paper %d: %v", index+1, err)
		}
	}

	for _, trend := range []struct {
		kind      string
		subjectID uuid.UUID
	}{
		{"papers", fixture.paperID},
		{"topics", topicID},
		{"methods", methodID},
	} {
		payload := fmt.Sprintf(
			`{"rank":1,"score":0.9,"subject_id":%q,"ranking":{"missing_signals":[]}}`,
			trend.subjectID,
		)
		if _, err := tx.Exec(ctx, `
			INSERT INTO public_catalog_trends (
				generation_id, trend_kind, window_days, rank, subject_id, score, payload
			) VALUES ($1, $2, 30, 1, $3, 0.9, $4::jsonb)
		`, fixture.generationID, trend.kind, trend.subjectID, payload); err != nil {
			t.Fatalf("insert HTTP API %s trend: %v", trend.kind, err)
		}
	}

	opportunityID := uuid.New()
	opportunityPayload := fmt.Sprintf(
		`{
			"id":%q,
			"title":"Agent Evaluation",
			"status":"worth_pursuing",
			"evidence_ids":[%q],
			"growth_score":{"state":"known","value":0.8},
			"competition_density":{"state":"known","value":0.3},
			"data_availability":{"state":"unknown"},
			"reproducibility":{"state":"missing"},
			"missing_signals":["data_availability","reproducibility"],
			"formula_version":"opportunity/v1",
			"generated_at":"2026-07-16T05:00:00Z"
		}`,
		opportunityID,
		fixture.paperID,
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_research_opportunities (
			generation_id, opportunity_id, status, ordinal, payload
		) VALUES ($1, $2, 'worth_pursuing', 1, $3::jsonb)
	`, fixture.generationID, opportunityID, opportunityPayload); err != nil {
		t.Fatalf("insert HTTP API research opportunity: %v", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_publications (generation_id, published_at)
		VALUES ($1, $2)
	`, fixture.generationID, generatedAt.Add(time.Minute)); err != nil {
		t.Fatalf("publish HTTP API catalog fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO public_catalog_current (generation_id)
		VALUES ($1)
	`, fixture.generationID); err != nil {
		t.Fatalf("set HTTP API current catalog fixture: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit HTTP API fixture: %v", err)
	}
	return fixture
}

func newCatalogTestHandler(t *testing.T, pool *pgxpool.Pool) http.Handler {
	t.Helper()

	repository, err := catalog.NewRepository(pool, httpAPICursorSecret)
	if err != nil {
		t.Fatalf("catalog.NewRepository() error = %v", err)
	}
	return NewServer(Dependencies{Catalog: repository})
}

func serveWithHandler(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertProblem(
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
	code string,
) problemDetails {
	t.Helper()

	if response.Code != status {
		t.Fatalf(
			"status = %d, want %d; body=%s",
			response.Code,
			status,
			response.Body,
		)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", contentType)
	}

	var problem problemDetails
	decoder := json.NewDecoder(response.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&problem); err != nil {
		t.Fatalf("decode Problem Details: %v", err)
	}
	if problem.Code != code {
		t.Fatalf("problem code = %q, want %q; problem=%#v", problem.Code, code, problem)
	}
	if problem.Status != status {
		t.Fatalf("problem status = %d, want %d", problem.Status, status)
	}
	if problem.RequestID == "" || problem.RequestID != response.Header().Get("X-Request-ID") {
		t.Fatalf(
			"problem request_id = %q, response X-Request-ID = %q",
			problem.RequestID,
			response.Header().Get("X-Request-ID"),
		)
	}
	return problem
}

func urlQueryEscape(value string) string {
	replacer := strings.NewReplacer(
		"%", "%25",
		"+", "%2B",
		"/", "%2F",
		"=", "%3D",
	)
	return replacer.Replace(value)
}
