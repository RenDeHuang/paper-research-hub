# Paper Research Hub Go + Nuxt Replatform Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the existing FastAPI and Next.js implementation with a maintainable Go modular monolith, a Nuxt 4 public portal, PostgreSQL-backed ingestion and rankings, independent deployment artifacts, and a public GitHub repository.

**Architecture:** One Go module builds separate API, worker, and migration binaries while sharing domain, repository, ingestion, ranking, configuration, and telemetry packages. Nuxt consumes only the versioned OpenAPI contract. PostgreSQL is the sole canonical store; source records, normalized entities, analysis runs, and ranking snapshots remain reproducible and auditable.

**Tech Stack:** Go 1.26, PostgreSQL 18, pgx, SQL migrations, OpenAPI 3.1, Nuxt 4, Vue 3, TypeScript, pnpm, Vitest, Nuxt Test Utils, Playwright, Docker Compose, GitHub Actions.

---

### Task 1: Remove the legacy runtimes and establish the new repository baseline

**Files:**
- Delete: `services/api/**`
- Delete: `apps/web/**`
- Delete: `artifacts/**`
- Delete: `design-system/paper-research-hub/**`
- Delete: `docs/plans/2026-07-15-paper-research-hub-design.md`
- Delete: `docs/plans/2026-07-15-paper-research-hub-implementation.md`
- Delete: `docs/plans/2026-07-16-paper-api-quality-hardening.md`
- Replace: `.gitignore`
- Replace: `.env.example`
- Replace: `Makefile`
- Replace: `package.json`
- Replace: `pnpm-workspace.yaml`
- Delete: `pnpm-lock.yaml`
- Create: `services/core/go.mod`
- Create: `services/core/cmd/api/main.go`
- Create: `services/core/cmd/worker/main.go`
- Create: `services/core/cmd/migrate/main.go`
- Create: `apps/web/package.json`
- Create: `apps/web/nuxt.config.ts`
- Create: `apps/web/tsconfig.json`
- Create: `apps/web/app/app.vue`

**Step 1: Record the clean baseline**

Run:

```bash
git status --short
git ls-files | sort
```

Expected: only the approved design and implementation plan are new relative to the legacy implementation.

**Step 2: Remove tracked legacy files through Git**

Use `git rm` for tracked files. Do not use history rewriting and do not delete `.git`.

Expected after removal:

```bash
! git grep -nE 'fastapi|sqlalchemy|alembic|pytest|next/(server|navigation)|next\\.config' -- \
  ':!docs/plans/2026-07-16-go-nuxt-replatform-design.md' \
  ':!docs/plans/2026-07-16-go-nuxt-replatform-implementation.md'
```

**Step 3: Create the minimum Go module**

`services/core/go.mod`:

```go
module github.com/RenDeHuang/paper-research-hub/services/core

go 1.26
```

Each command initially exits explicitly:

```go
package main

import "log"

func main() {
	log.Fatal("not implemented")
}
```

**Step 4: Create the minimum Nuxt workspace**

`apps/web/package.json`:

```json
{
  "name": "@paper-hub/web",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "nuxt dev",
    "build": "nuxt build",
    "preview": "nuxt preview",
    "lint": "eslint .",
    "typecheck": "nuxt typecheck",
    "test": "vitest run",
    "test:e2e": "playwright test",
    "postinstall": "nuxt prepare"
  },
  "dependencies": {
    "nuxt": "^4.0.0",
    "vue": "^3.5.0",
    "vue-router": "^4.5.0"
  },
  "devDependencies": {
    "@nuxt/test-utils": "^3.19.0",
    "@playwright/test": "^1.54.0",
    "@vue/test-utils": "^2.4.0",
    "typescript": "^5.8.0",
    "vitest": "^3.2.0",
    "vue-tsc": "^2.2.0"
  }
}
```

Install dependencies and commit the generated `pnpm-lock.yaml`.

**Step 5: Verify the legacy stacks are gone**

Run:

```bash
find services apps -type f | sort
go env GOMOD
pnpm --dir apps/web exec nuxt --version
```

Expected: no Python, Alembic, Next.js, `.next`, or `uv` project files remain.

**Step 6: Commit**

```bash
git add -A
git commit -m "chore: replace legacy runtimes with Go and Nuxt baseline"
```

### Task 2: Define the OpenAPI contract and health endpoints

**Files:**
- Create: `contracts/openapi.yaml`
- Create: `services/core/internal/httpapi/problem.go`
- Create: `services/core/internal/httpapi/server.go`
- Create: `services/core/internal/httpapi/server_test.go`
- Modify: `services/core/cmd/api/main.go`
- Create: `apps/web/tests/health.test.ts`
- Create: `apps/web/server/routes/health.get.ts`
- Modify: `apps/web/nuxt.config.ts`

**Step 1: Write the failing Go health test**

```go
func TestHealthReportsServiceIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	NewServer(Dependencies{}).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"status":"ok","service":"paper-hub-api"}`, rec.Body.String())
}
```

**Step 2: Run it to verify RED**

```bash
cd services/core
go test ./internal/httpapi -run TestHealthReportsServiceIdentity -v
```

Expected: FAIL because `NewServer` does not exist.

**Step 3: Define the contract**

`contracts/openapi.yaml` must include:

```yaml
openapi: 3.1.0
info:
  title: Paper Research Hub API
  version: 1.0.0
paths:
  /health:
    get:
      operationId: getHealth
      responses:
        "200":
          description: Process health
  /api/v1/papers:
    get:
      operationId: listPapers
  /api/v1/papers/{id}:
    get:
      operationId: getPaper
  /api/v1/stats:
    get:
      operationId: getStats
  /api/v1/topics:
    get:
      operationId: listTopics
  /api/v1/topics/{slug}:
    get:
      operationId: getTopic
  /api/v1/methods:
    get:
      operationId: listMethods
  /api/v1/methods/{slug}:
    get:
      operationId: getMethod
  /api/v1/trends/papers:
    get:
      operationId: listPaperTrends
  /api/v1/trends/topics:
    get:
      operationId: listTopicTrends
  /api/v1/trends/methods:
    get:
      operationId: listMethodTrends
  /api/v1/research-opportunities:
    get:
      operationId: listResearchOpportunities
```

Add complete parameter, response, pagination, ranking metadata, and RFC 9457 schemas before generating clients.

**Step 4: Implement the health endpoint and Problem Details writer**

The server must:

- use a standard `http.Handler`;
- emit JSON with `Content-Type: application/json`;
- attach a request ID;
- return RFC 9457 JSON for unmatched `/api` routes.

**Step 5: Write and pass the Nuxt health test**

```ts
import handler from "../server/routes/health.get"

it("reports the web service identity", async () => {
  expect(await handler({} as never)).toEqual({
    status: "ok",
    service: "paper-hub-web",
  })
})
```

Run:

```bash
go test ./... -v
pnpm --dir apps/web test
pnpm --dir apps/web typecheck
```

Expected: PASS.

**Step 6: Commit**

```bash
git add contracts services/core apps/web
git commit -m "feat: define service contracts and health endpoints"
```

### Task 3: Add strict configuration and PostgreSQL lifecycle

**Files:**
- Create: `services/core/internal/config/config.go`
- Create: `services/core/internal/config/config_test.go`
- Create: `services/core/internal/database/pool.go`
- Create: `services/core/internal/database/pool_test.go`
- Create: `services/core/internal/database/migrate.go`
- Create: `services/core/internal/database/migrate_test.go`
- Create: `services/core/migrations/000001_initial.sql`
- Modify: `services/core/cmd/migrate/main.go`
- Modify: `.env.example`

**Step 1: Write failing configuration tests**

Cover:

- `DATABASE_URL` is required.
- only PostgreSQL URLs are accepted;
- `OPENALEX_CONTACT_EMAIL` is required for ingestion;
- secrets are redacted from formatted configuration;
- invalid durations and worker limits fail at startup.

Example:

```go
func TestLoadRejectsNonPostgresDatabase(t *testing.T) {
	t.Setenv("DATABASE_URL", "sqlite:///paper.db")
	_, err := Load()
	require.ErrorContains(t, err, "DATABASE_URL must use postgres")
}
```

**Step 2: Run RED**

```bash
cd services/core
go test ./internal/config ./internal/database -v
```

**Step 3: Implement strict configuration**

Use explicit structs:

```go
type Config struct {
	Environment string
	HTTP        HTTPConfig
	Database    DatabaseConfig
	OpenAlex    OpenAlexConfig
}
```

Do not supply production secrets or silent defaults for required values.

**Step 4: Create the initial schema**

`000001_initial.sql` must create:

```text
works
paper_versions
source_records
external_identifiers
field_assertions
authors
work_authors
institutions
topics
work_topics
methods
work_methods
datasets
work_datasets
benchmarks
work_benchmarks
models
work_models
code_repositories
work_code_repositories
metric_snapshots
ranking_snapshots
ingestion_cursors
ingestion_jobs
analysis_runs
```

Add database constraints for:

- approved canonical key prefixes;
- immutable raw source snapshots;
- valid paper status;
- nonnegative metrics;
- exactly one ranking subject;
- source and projection ownership;
- unique normalized external identifiers.

**Step 5: Test migrations against real PostgreSQL**

The test must:

1. start PostgreSQL 18 with Testcontainers;
2. apply migrations from an empty database;
3. verify expected tables and constraints;
4. apply the migration command a second time;
5. verify no schema drift or duplicate effects.

Run:

```bash
go test ./internal/database -run Migration -v
```

Expected: PASS with Docker available; fail explicitly if the test environment cannot start PostgreSQL.

**Step 6: Commit**

```bash
git add .env.example services/core
git commit -m "feat: add strict configuration and PostgreSQL schema"
```

### Task 4: Implement canonical paper identities and domain rules

**Files:**
- Create: `services/core/internal/paper/identifier.go`
- Create: `services/core/internal/paper/identifier_test.go`
- Create: `services/core/internal/paper/model.go`
- Create: `services/core/internal/paper/model_test.go`
- Create: `services/core/internal/paper/status.go`
- Create: `services/core/internal/paper/status_test.go`

**Step 1: Write failing table tests**

Port the verified identity cases:

```go
func TestNormalizeDOI(t *testing.T) {
	cases := map[string]string{
		" https://doi.org/10.1000/ABC ": "10.1000/abc",
		"doi: 10.1000/ABC":              "10.1000/abc",
	}
	for raw, expected := range cases {
		actual, err := NormalizeDOI(raw)
		require.NoError(t, err)
		require.Equal(t, expected, actual)
	}
}

func TestSimilarTitlesNeverCreateIdentity(t *testing.T) {
	_, ok := CanonicalIdentity(Identifiers{}, "Planning Agents with Tool Use")
	require.False(t, ok)
}
```

Also cover:

- arXiv version suffix removal;
- OpenAlex URL normalization;
- Semantic Scholar ID length and lowercase;
- OpenReview forum case preservation;
- invalid primary alias cannot hide a valid secondary alias;
- arbitrary `canonical_key` input is not identity evidence;
- identity precedence is DOI, arXiv, OpenReview, Semantic Scholar, OpenAlex.

**Step 2: Run RED**

```bash
go test ./internal/paper -v
```

**Step 3: Implement value objects**

Use typed values rather than unvalidated strings:

```go
type Identifier struct {
	Scheme Scheme
	Value  string
}

type WorkStatus string
```

All constructors validate; invalid instances cannot be created through public package functions.

**Step 4: Run GREEN and fuzz tests**

```bash
go test ./internal/paper -v
go test ./internal/paper -fuzz FuzzNormalizeDOI -fuzztime 10s
```

Expected: all deterministic tests pass and the fuzz target finds no panic.

**Step 5: Commit**

```bash
git add services/core/internal/paper
git commit -m "feat: implement canonical paper domain rules"
```

### Task 5: Implement the OpenAlex connector with bounded network behavior

**Files:**
- Create: `services/core/internal/source/source.go`
- Create: `services/core/internal/source/openalex/client.go`
- Create: `services/core/internal/source/openalex/client_test.go`
- Create: `services/core/internal/source/openalex/parse.go`
- Create: `services/core/internal/source/openalex/parse_test.go`
- Create: `services/core/internal/source/openalex/testdata/works.json`

**Step 1: Write failing HTTP client tests**

Use `httptest.Server` to verify:

- contact email and optional API key are sent correctly;
- cursor pagination is used;
- `max_results` is a hard limit;
- timeout is explicit;
- API keys never appear in errors;
- 429 respects bounded `Retry-After`;
- retryable 5xx uses bounded exponential backoff;
- nonretryable 4xx is not retried;
- malformed JSON is an explicit connector error.

**Step 2: Write failing parser tests**

Verify mapping of:

- identifiers;
- authors and institutions;
- publication and update dates;
- abstract reconstruction;
- topics;
- citation count;
- OA and license;
- retraction state;
- code repository URLs;
- exact raw content hash;
- scope decision and reason.

The raw content hash is SHA-256 over deterministically sorted compact JSON, not
over re-serialized domain objects.

**Step 3: Run RED**

```bash
go test ./internal/source/openalex -v
```

**Step 4: Implement the connector**

The connector API:

```go
type Client interface {
	Fetch(ctx context.Context, query Query) iter.Seq2[Record, error]
}
```

No error may be converted into an empty successful result.

**Step 5: Run GREEN**

```bash
go test ./internal/source/openalex -v
go test -race ./internal/source/openalex
```

**Step 6: Commit**

```bash
git add services/core/internal/source
git commit -m "feat: add strict OpenAlex connector"
```

### Task 6: Implement idempotent and concurrent-safe ingestion

**Files:**
- Create: `services/core/internal/ingestion/service.go`
- Create: `services/core/internal/ingestion/service_test.go`
- Create: `services/core/internal/ingestion/repository.go`
- Create: `services/core/internal/ingestion/postgres_repository.go`
- Create: `services/core/internal/ingestion/postgres_repository_test.go`
- Create: `services/core/internal/ingestion/job.go`
- Create: `services/core/internal/ingestion/job_test.go`
- Modify: `services/core/cmd/worker/main.go`

**Step 1: Write failing PostgreSQL tests**

Cover:

- same source hash is idempotent;
- new hash creates an immutable source snapshot without duplicating the work;
- older snapshots never overwrite newer projections;
- exact latest snapshot replaces taxonomy associations;
- excluded snapshots remain auditable;
- scope rule version changes reuse raw snapshots;
- controlled identifiers merge works; similar titles do not;
- stronger identifiers upgrade canonical identity;
- canonical conflicts fail explicitly;
- concurrent identical inserts create one source snapshot and one work;
- concurrent sources cannot regress projections;
- deleting a work preserves raw source records;
- a later-page failure preserves raw records already durably confirmed;
- job summaries report actual committed raw, projected, excluded, and failed
  counts instead of zeroing earlier durable work.

**Step 2: Run RED**

```bash
go test ./internal/ingestion -v
```

**Step 3: Implement transaction boundaries**

Required rules:

- PostgreSQL advisory lock guards a logical ingestion batch.
- unique constraints resolve races; application code handles expected conflicts.
- raw source records are append-only.
- projection selection uses source timestamp plus a deterministic tie-break key.
- public visibility aggregates current decisions across logical sources.
- raw persistence, normalization, projection, and ranking are separately
  restartable stages recorded in `ingestion_jobs`; a downstream failure cannot
  roll back an already confirmed raw source snapshot.

**Step 4: Implement worker commands**

```text
paper-hub-worker sync openalex --query "LLM agent" --max-results 100
paper-hub-worker snapshot metrics
paper-hub-worker rank --window 30d
```

Invalid flags, missing contact email, or over-limit requests exit nonzero before database mutation.

**Step 5: Run GREEN and race tests**

```bash
go test -race ./internal/ingestion -v
go test ./cmd/worker/... -v
```

**Step 6: Commit**

```bash
git add services/core/internal/ingestion services/core/cmd/worker
git commit -m "feat: ingest papers with immutable provenance"
```

### Task 7: Implement search, filters, pagination, and paper detail APIs

**Files:**
- Create: `services/core/internal/paper/repository.go`
- Create: `services/core/internal/paper/postgres_repository.go`
- Create: `services/core/internal/paper/postgres_repository_test.go`
- Create: `services/core/internal/httpapi/papers.go`
- Create: `services/core/internal/httpapi/papers_test.go`
- Modify: `services/core/internal/httpapi/server.go`
- Modify: `contracts/openapi.yaml`
- Create: `services/core/migrations/000002_search_indexes.sql`

**Step 1: Write failing repository tests**

Cover search by:

- normalized title;
- author;
- DOI;
- arXiv ID;
- OpenAlex ID.

Cover combined filters:

- date;
- paper type;
- topic;
- method;
- code/data/benchmark presence;
- publication status;
- source.

**Step 2: Write failing pagination tests**

The cursor must encode:

```text
snapshot revision
sort name
sort value
canonical key
filter hash
```

Verify:

- stable ties use canonical key;
- concurrent inserts do not enter an existing snapshot;
- any observable update invalidates an incompatible cursor;
- blank queries and mismatched cursors return stable 422 Problem Details;
- list, total, and facets share one repeatable-read transaction.

**Step 3: Write failing detail tests**

Paper detail must return:

- versions and identifiers;
- authors and institutions;
- topics, methods, datasets, benchmarks and models;
- repositories;
- source and field provenance;
- metrics;
- current source scope decisions;
- license and update times.

The same public visibility predicate and read-only transaction boundary must be
used by `/api/v1/stats`, `/api/v1/topics`, `/api/v1/topics/{slug}`,
`/api/v1/methods`, and `/api/v1/methods/{slug}`.

**Step 4: Implement indexes and repositories**

Use PostgreSQL full-text search and trigram indexes. Every list query must be bounded and must not issue per-item queries.

**Step 5: Implement handlers and run GREEN**

```bash
go test -race ./internal/paper ./internal/httpapi -v
```

Expected: all repository and handler tests pass.

**Step 6: Commit**

```bash
git add contracts services/core
git commit -m "feat: expose paper discovery APIs"
```

### Task 8: Implement transparent trends and research-opportunity analysis

**Files:**
- Create: `services/core/internal/ranking/formula.go`
- Create: `services/core/internal/ranking/formula_test.go`
- Create: `services/core/internal/ranking/service.go`
- Create: `services/core/internal/ranking/service_test.go`
- Create: `services/core/internal/ranking/postgres_repository.go`
- Create: `services/core/internal/ranking/postgres_repository_test.go`
- Create: `services/core/internal/researchmap/opportunity.go`
- Create: `services/core/internal/researchmap/opportunity_test.go`
- Create: `services/core/internal/httpapi/trends.go`
- Create: `services/core/internal/httpapi/trends_test.go`
- Create: `services/core/internal/httpapi/opportunities.go`
- Create: `services/core/internal/httpapi/opportunities_test.go`
- Modify: `contracts/openapi.yaml`

**Step 1: Write failing ranking formula tests**

Keep formulas separate:

- citation velocity requires two boundary snapshots;
- code growth requires two snapshots;
- topic growth compares current and baseline windows;
- method adoption is share delta, not topic count growth;
- citation percentiles use topic, month, and paper-type cohorts;
- missing signals are reported, not replaced with zero;
- retracted, withdrawn, rejected, superseded, or currently excluded works are not ranked.

**Step 2: Write failing opportunity tests**

Every opportunity result must contain:

```go
type Opportunity struct {
	Status              Status
	Topic               TopicSummary
	EvidencePaperIDs    []uuid.UUID
	GrowthWindow        Window
	GrowthScore         decimal.Decimal
	CompetitionDensity  decimal.Decimal
	DataAvailability    decimal.Decimal
	Reproducibility     decimal.Decimal
	Limitations         []string
	FormulaVersion      string
	GeneratedAt         time.Time
}
```

Allowed statuses:

```text
worth_pursuing
proceed_with_caution
not_recommended_now
insufficient_evidence
```

Insufficient evidence must never be promoted to a positive or negative recommendation.

**Step 3: Run RED**

```bash
go test ./internal/ranking ./internal/researchmap ./internal/httpapi -v
```

**Step 4: Implement formulas, snapshot persistence, and APIs**

Responses must expose:

- formula version;
- window;
- generated time;
- data coverage;
- missing signals;
- confidence;
- evidence IDs.

**Step 5: Run GREEN**

```bash
go test -race ./internal/ranking ./internal/researchmap ./internal/httpapi -v
```

**Step 6: Commit**

```bash
git add contracts services/core
git commit -m "feat: add transparent trends and research opportunities"
```

### Task 9: Generate the new UI/UX system and repository-owned visual assets

**Files:**
- Create: `design-system/paper-research-hub/MASTER.md`
- Create: `design-system/paper-research-hub/pages/home.md`
- Create: `design-system/paper-research-hub/pages/papers.md`
- Create: `design-system/paper-research-hub/pages/trends.md`
- Create: `design-system/paper-research-hub/pages/opportunities.md`
- Create: `apps/web/public/images/paper-network-hero.webp`
- Create: `apps/web/public/images/research-map-empty.webp`
- Create: `docs/design/visual-rationale.md`

**Step 1: Run the UI/UX design-system workflow**

Read and follow:

```text
/Users/huangrende/Library/Application Support/OPL/runtime/current/skills/ui-ux-pro-max/SKILL.md
```

Generate a new system using the skill's `--design-system` workflow. Requirements:

- academic intelligence portal;
- AgentSkillsHub-style discoverability without brand imitation;
- high-density cards, trend charts, evidence bars, and matrix views;
- restrained navy/teal/coral palette;
- strong Chinese and English typography;
- WCAG AA;
- 375/768/1024/1440 layouts;
- no decorative chart junk.

**Step 2: Generate visual assets**

Read and follow:

```text
/Users/huangrende/.codex/skills/.system/imagegen/SKILL.md
```

Generate:

- a subtle paper-network hero visual;
- a research-map empty-state visual.

Do not embed text into generated raster images. Copy accepted assets into `apps/web/public/images`.

**Step 3: Review generated assets**

Open the files and verify:

- no illegible text;
- no unrelated UI chrome;
- enough negative space for overlays;
- light and dark backgrounds remain legible;
- exported dimensions and compression are appropriate.

**Step 4: Document the decisions and commit**

```bash
git add design-system apps/web/public/images docs/design
git commit -m "design: establish the paper intelligence visual system"
```

### Task 10: Build the Nuxt application shell and home dashboard

**Files:**
- Create: `apps/web/app/assets/css/main.css`
- Create: `apps/web/app/layouts/default.vue`
- Create: `apps/web/app/components/AppHeader.vue`
- Create: `apps/web/app/components/AppFooter.vue`
- Create: `apps/web/app/components/SearchCommand.vue`
- Create: `apps/web/app/components/PaperCard.vue`
- Create: `apps/web/app/components/TrendSparkline.vue`
- Create: `apps/web/app/components/TopicHeatmap.vue`
- Create: `apps/web/app/components/OpportunityMatrix.vue`
- Create: `apps/web/app/composables/usePaperApi.ts`
- Create: `apps/web/app/composables/usePaperApi.test.ts`
- Create: `apps/web/app/pages/index.vue`
- Create: `apps/web/tests/home.test.ts`
- Modify: `apps/web/nuxt.config.ts`

**Step 1: Generate TypeScript types from OpenAPI**

Create a deterministic script:

```json
"generate:api": "openapi-typescript ../../contracts/openapi.yaml -o app/generated/api.ts"
```

The generated file is committed. CI later verifies regeneration creates no diff.

**Step 2: Write failing API client tests**

Verify:

- server-side requests use private `apiBase`;
- browser-side requests use `public.apiBase`;
- query parameters are serialized once;
- non-2xx Problem Details become typed errors;
- no mock data is used when the real endpoint fails.

**Step 3: Write failing home-page tests**

Verify the SSR page renders:

- global search;
- latest papers;
- paper trends;
- topic trends;
- method trends;
- research-opportunity matrix;
- explicit loading, empty, and error states.

**Step 4: Implement the shell and home dashboard**

Follow the new design-system documents exactly. Use semantic HTML, visible keyboard focus, reduced-motion support, and SVG icons from one icon family.

**Step 5: Run GREEN**

```bash
pnpm --dir apps/web generate:api
pnpm --dir apps/web test
pnpm --dir apps/web typecheck
pnpm --dir apps/web build
```

**Step 6: Commit**

```bash
git add apps/web
git commit -m "feat: build the Nuxt paper discovery dashboard"
```

### Task 11: Build paper, trend, topic, method, and opportunity pages

**Files:**
- Create: `apps/web/app/pages/papers/index.vue`
- Create: `apps/web/app/pages/papers/[id].vue`
- Create: `apps/web/app/pages/trends.vue`
- Create: `apps/web/app/pages/opportunities.vue`
- Create: `apps/web/app/pages/topics/[slug].vue`
- Create: `apps/web/app/pages/methods/[slug].vue`
- Create: `apps/web/app/components/PaperFilters.vue`
- Create: `apps/web/app/components/RankingTable.vue`
- Create: `apps/web/app/components/EvidencePanel.vue`
- Create: `apps/web/app/components/SourceProvenance.vue`
- Create: `apps/web/tests/papers.test.ts`
- Create: `apps/web/tests/trends.test.ts`
- Create: `apps/web/tests/opportunities.test.ts`
- Create: `apps/web/e2e/public-portal.spec.ts`

**Step 1: Write failing page tests**

Cover:

- filters are represented in the URL;
- browser back/forward restores filters;
- cursor pagination preserves the snapshot;
- detail pages show source and license provenance;
- trend windows are separate;
- opportunity status always shows formula, evidence, limitations, and confidence;
- SSR metadata contains title, description, canonical URL, Open Graph, and scholarly structured data.

**Step 2: Implement pages and states**

Do not use fallback fixture data in production components. Empty API results, invalid cursors, network failures, and missing papers each have a distinct rendered state.

**Step 3: Add Playwright flows**

Test at 375 and 1440 widths:

1. open home;
2. search for an agent paper;
3. apply topic and code filters;
4. open a paper detail;
5. inspect provenance;
6. open trends;
7. inspect an opportunity's evidence.

**Step 4: Verify**

```bash
pnpm --dir apps/web test
pnpm --dir apps/web typecheck
pnpm --dir apps/web build
pnpm --dir apps/web test:e2e
```

**Step 5: Commit**

```bash
git add apps/web
git commit -m "feat: add paper discovery and trend exploration pages"
```

### Task 12: Add Docker, local orchestration, and production deployment boundaries

**Files:**
- Create: `deploy/docker/api.Dockerfile`
- Create: `deploy/docker/web.Dockerfile`
- Create: `.dockerignore`
- Create: `deploy/compose/docker-compose.yml`
- Create: `deploy/compose/docker-compose.test.yml`
- Create: `deploy/production/README.md`
- Create: `deploy/production/api.env.example`
- Create: `deploy/production/web.env.example`
- Modify: `Makefile`
- Modify: `.env.example`

**Step 1: Write container smoke checks**

The test Compose stack must verify:

```text
postgres healthy
migration exits 0
api ready
worker healthy
web ready
web can call api
```

**Step 2: Build one Go image with three commands**

The Go Dockerfile produces:

```text
/app/paper-hub-api
/app/paper-hub-worker
/app/paper-hub-migrate
```

Use a nonroot runtime user and a minimal runtime image.

**Step 3: Build the Nuxt image**

Build the production Node server output, run as nonroot, and expose only the Nuxt port. The image must not contain source caches or development dependencies.

**Step 4: Implement Make targets**

Required:

```text
make install
make generate
make test
make test-race
make build
make compose-up
make compose-down
make smoke
make sync-openalex
```

**Step 5: Verify from a clean container build**

```bash
docker compose -f deploy/compose/docker-compose.test.yml build --no-cache
docker compose -f deploy/compose/docker-compose.test.yml up \
  --abort-on-container-exit --exit-code-from smoke
```

Expected: smoke exits 0.

**Step 6: Commit**

```bash
git add deploy Makefile .env.example
git commit -m "chore: add decoupled container deployment"
```

### Task 13: Add CI, documentation, licensing, and repository metadata

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.github/workflows/container.yml`
- Create: `.github/workflows/publish.yml`
- Create: `.github/dependabot.yml`
- Create: `README.md`
- Create: `CONTRIBUTING.md`
- Create: `LICENSE`
- Create: `SECURITY.md`
- Create: `docs/architecture.md`
- Create: `docs/data-sources.md`
- Create: `docs/ranking-methodology.md`
- Create: `docs/deployment.md`

**Step 1: Define CI jobs**

`ci.yml`:

- Go format, vet, unit, PostgreSQL integration, and race tests;
- OpenAPI validation;
- generated-client drift check;
- Nuxt tests, typecheck, build;
- Playwright smoke test.

`container.yml`:

- build API/Worker/Migrate image;
- build Web image;
- run Compose smoke test;
- no registry push until repository secrets are configured.

`publish.yml`:

- runs only for a version tag or GitHub Release;
- publishes separate Core and Web images to GHCR;
- uses `GITHUB_TOKEN` with `packages: write`;
- emits provenance and SBOM attestations;
- never publishes unverified pull-request images.

**Step 2: Write operational documentation**

The README must explain:

- product purpose;
- architecture diagram;
- prerequisites;
- one-command local startup;
- live OpenAlex sync;
- test commands;
- repository layout;
- deployment boundaries;
- roadmap.

Data-source and ranking documents must expose provenance and formula semantics.

**Step 3: Validate repository metadata**

```bash
git grep -nE 'FastAPI|Alembic|Next\\.js|uvicorn|pytest' -- \
  ':!docs/plans/2026-07-16-go-nuxt-replatform-design.md' \
  ':!docs/plans/2026-07-16-go-nuxt-replatform-implementation.md'
```

Expected: no runtime or active-document references.

**Step 4: Run local CI-equivalent checks**

```bash
make generate
git diff --exit-code
make test
make test-race
make build
make smoke
```

Expected: all commands exit 0.

**Step 5: Commit**

```bash
git add .github README.md CONTRIBUTING.md LICENSE SECURITY.md docs
git commit -m "docs: prepare the paper research hub for open source"
```

### Task 14: Verify live ingestion and public user flows

**Files:**
- Create: `artifacts/verification/openalex-sync.json`
- Create: `artifacts/verification/portal-smoke.json`
- Modify: `.gitignore`

**Step 1: Start from an empty database**

```bash
make compose-up
make migrate
```

Expected: the new Go schema is created from `000001` and `000002`.

**Step 2: Run a narrow live OpenAlex sync**

```bash
OPENALEX_CONTACT_EMAIL="$OPENALEX_CONTACT_EMAIL" \
make sync-openalex QUERY="LLM agent benchmark" MAX_RESULTS=20
```

Expected:

- nonzero fetched count;
- committed inserted/excluded counts;
- no secret values in logs;
- a machine-readable summary written to `artifacts/verification/openalex-sync.json`.

**Step 3: Verify API and rendered pages**

Check:

```text
/health
/api/v1/papers
/api/v1/trends/papers
/
/papers
/trends
/opportunities
```

Capture only decisive status, count, and console-error information in `portal-smoke.json`.

**Step 4: Run complete verification**

```bash
go test -race ./...
pnpm --dir apps/web test
pnpm --dir apps/web typecheck
pnpm --dir apps/web build
pnpm --dir apps/web test:e2e
make smoke
git status --short
```

Expected: all tests pass and only intended verification artifacts are added.

**Step 5: Commit**

```bash
git add artifacts/verification .gitignore
git commit -m "test: verify live paper ingestion and portal flows"
```

### Task 15: Create and publish the GitHub repository

**Files:**
- No source changes expected.

**Step 1: Confirm authentication and repository availability**

```bash
gh auth status
gh repo view RenDeHuang/paper-research-hub
```

Expected: authenticated as `RenDeHuang`; repository does not already conflict, or its state is explicitly reviewed before changing it.

**Step 2: Create the public repository**

```bash
gh repo create RenDeHuang/paper-research-hub \
  --public \
  --description "A public paper intelligence portal for AI agents, trends, methods, and research opportunities." \
  --source .
```

Expected: repository created and `origin` configured.

**Step 3: Publish the verified commit as `main`**

```bash
git push -u origin HEAD:main
gh repo edit RenDeHuang/paper-research-hub \
  --default-branch main \
  --add-topic academic-research \
  --add-topic ai-agents \
  --add-topic literature-discovery \
  --add-topic golang \
  --add-topic nuxt
```

Do not force-push.

**Step 4: Verify remote state**

```bash
gh repo view RenDeHuang/paper-research-hub \
  --json nameWithOwner,visibility,defaultBranchRef,url
git ls-remote --heads origin
```

Expected:

- visibility is `PUBLIC`;
- default branch is `main`;
- remote `main` points at the verified local commit.

**Step 5: Report the published URL and exact verification results**

The completion report must include:

- repository URL;
- final commit;
- test totals;
- container smoke result;
- live ingestion count;
- remaining product limitations.
