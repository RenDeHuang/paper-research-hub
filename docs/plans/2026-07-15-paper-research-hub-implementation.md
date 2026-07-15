# Paper Research Hub MVP Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build and deploy a database-backed public AI Agent / LLM paper directory with live OpenAlex ingestion, deterministic version handling, search/filter APIs, transparent trend rankings, and a responsive AgentSkillsHub-style frontend.

**Architecture:** A monorepo contains a Next.js public portal and a FastAPI service. PostgreSQL is the canonical store; ingestion writes immutable source records, normalized works, external identifiers, topics, methods, code links, and metric snapshots. The web app consumes the API and renders SEO-friendly directory, ranking, topic, method, and paper-detail pages.

**Tech Stack:** Next.js, TypeScript, Tailwind CSS, Vitest, Testing Library, Playwright, FastAPI, Pydantic, SQLAlchemy 2, Alembic, PostgreSQL, pytest, httpx, Docker Compose.

---

### Task 1: Scaffold the monorepo and health contracts

**Files:**
- Create: `pnpm-workspace.yaml`
- Create: `package.json`
- Create: `apps/web/package.json`
- Create: `apps/web/next.config.ts`
- Create: `apps/web/tsconfig.json`
- Create: `apps/web/src/app/layout.tsx`
- Create: `apps/web/src/app/page.tsx`
- Create: `apps/web/src/app/globals.css`
- Create: `apps/web/src/app/api/health/route.ts`
- Create: `apps/web/src/app/api/health/route.test.ts`
- Create: `services/api/pyproject.toml`
- Create: `services/api/src/paper_hub/__init__.py`
- Create: `services/api/src/paper_hub/main.py`
- Create: `services/api/tests/test_health.py`
- Create: `.env.example`
- Create: `Makefile`

**Step 1: Write failing health tests**

Backend:

```python
from fastapi.testclient import TestClient
from paper_hub.main import app

def test_health_reports_service_identity():
    response = TestClient(app).get("/health")
    assert response.status_code == 200
    assert response.json() == {"status": "ok", "service": "paper-hub-api"}
```

Frontend:

```typescript
import { GET } from "./route";

it("returns the web service health contract", async () => {
  const response = await GET();
  expect(await response.json()).toEqual({
    status: "ok",
    service: "paper-hub-web",
  });
});
```

**Step 2: Run tests and verify RED**

Run:

```bash
cd services/api && uv run pytest tests/test_health.py -v
pnpm --dir apps/web test src/app/api/health/route.test.ts
```

Expected: both fail because application modules are missing.

**Step 3: Implement minimal health endpoints and project configuration**

Create the FastAPI app and Next.js route with the exact JSON contracts above. Configure scripts:

```json
{
  "scripts": {
    "dev": "next dev",
    "build": "next build",
    "test": "vitest run",
    "lint": "eslint ."
  }
}
```

**Step 4: Run tests and builds**

```bash
cd services/api && uv run pytest -v
pnpm --dir apps/web test
pnpm --dir apps/web build
```

Expected: all commands exit 0.

**Step 5: Commit**

```bash
git add .
git commit -m "chore: scaffold paper hub services"
```

### Task 2: Implement canonical paper models and deterministic normalization

**Files:**
- Create: `services/api/src/paper_hub/config.py`
- Create: `services/api/src/paper_hub/db.py`
- Create: `services/api/src/paper_hub/models.py`
- Create: `services/api/src/paper_hub/schemas.py`
- Create: `services/api/src/paper_hub/normalization.py`
- Create: `services/api/tests/test_normalization.py`
- Create: `services/api/alembic.ini`
- Create: `services/api/alembic/env.py`
- Create: `services/api/alembic/versions/0001_initial_schema.py`

**Step 1: Write failing normalization tests**

Test:

- DOI normalization removes URL prefixes and lowercases.
- arXiv version IDs map to one base identifier.
- identical DOI records generate the same canonical key.
- records with only similar titles do not generate the same canonical key.
- source field assertions preserve source and license.

Example:

```python
def test_arxiv_versions_share_a_canonical_key():
    first = canonical_identity({"arxiv_id": "2401.01234v1"})
    second = canonical_identity({"arxiv_id": "2401.01234v3"})
    assert first == second == "arxiv:2401.01234"
```

**Step 2: Verify RED**

```bash
cd services/api
uv run pytest tests/test_normalization.py -v
```

Expected: import or assertion failure because normalization is missing.

**Step 3: Implement schema and deterministic normalization**

Create SQLAlchemy models for:

```text
Work
PaperVersion
SourceRecord
ExternalIdentifier
FieldAssertion
Topic
Method
Dataset
Benchmark
CodeRepository
MetricSnapshot
RankingSnapshot
```

No title-similarity auto-merge is permitted.

**Step 4: Verify GREEN and migration**

```bash
cd services/api
uv run pytest tests/test_normalization.py -v
uv run alembic upgrade head
```

Expected: tests pass and PostgreSQL schema is created.

**Step 5: Commit**

```bash
git add services/api
git commit -m "feat: add canonical paper data model"
```

### Task 3: Add OpenAlex ingestion with immutable provenance

**Files:**
- Create: `services/api/src/paper_hub/connectors/__init__.py`
- Create: `services/api/src/paper_hub/connectors/base.py`
- Create: `services/api/src/paper_hub/connectors/openalex.py`
- Create: `services/api/src/paper_hub/ingestion.py`
- Create: `services/api/src/paper_hub/cli.py`
- Create: `services/api/tests/fixtures/openalex_works.json`
- Create: `services/api/tests/test_openalex_connector.py`
- Create: `services/api/tests/test_ingestion.py`

**Step 1: Write failing connector and idempotency tests**

Test:

- API response maps external IDs, authors, dates, OA/license and citation count.
- raw response receives SHA-256 hash and retrieval timestamp.
- importing the same source record twice creates one source record and one work.
- a later source update creates a new immutable source assertion without duplicating the work.
- records outside the configured Agent/LLM scope are excluded with an explicit reason.

**Step 2: Verify RED**

```bash
cd services/api
uv run pytest tests/test_openalex_connector.py tests/test_ingestion.py -v
```

**Step 3: Implement connector and CLI**

CLI:

```bash
uv run paper-hub sync-openalex \
  --query "LLM agent benchmark" \
  --from-date 2026-01-01 \
  --max-results 100
```

The connector must:

- set a stable User-Agent and contact email;
- support an optional OpenAlex API key;
- obey response errors instead of silently falling back;
- save exact raw records;
- use external identifiers for canonical identity;
- record scope-match evidence.

**Step 4: Verify GREEN and run a live narrow sync**

```bash
cd services/api
uv run pytest tests/test_openalex_connector.py tests/test_ingestion.py -v
uv run paper-hub sync-openalex --query "LLM agent benchmark" --max-results 20
```

Expected: tests pass and the live command reports inserted, updated, excluded and failed counts.

**Step 5: Commit**

```bash
git add services/api
git commit -m "feat: ingest OpenAlex papers with provenance"
```

### Task 4: Implement search, filters, detail, and transparent rankings API

**Files:**
- Create: `services/api/src/paper_hub/repositories.py`
- Create: `services/api/src/paper_hub/rankings.py`
- Create: `services/api/src/paper_hub/api/__init__.py`
- Create: `services/api/src/paper_hub/api/papers.py`
- Create: `services/api/src/paper_hub/api/trends.py`
- Modify: `services/api/src/paper_hub/main.py`
- Create: `services/api/tests/test_rankings.py`
- Create: `services/api/tests/test_papers_api.py`
- Create: `services/api/tests/test_trends_api.py`

**Step 1: Write failing API and ranking tests**

Cover:

- search by title, author, DOI and arXiv ID;
- combined type/topic/code/date filters;
- paginated stable ordering;
- paper detail includes versions, identifiers, sources and license;
- `latest`, `citation_velocity`, `code_growth`, `topic_growth`, `method_adoption`;
- ranking response includes formula version, window, generated time and missing signals;
- withdrawn/retracted works are excluded from rankings.

**Step 2: Verify RED**

```bash
cd services/api
uv run pytest tests/test_rankings.py tests/test_papers_api.py tests/test_trends_api.py -v
```

**Step 3: Implement minimal repositories and routes**

Routes:

```text
GET /api/v1/papers
GET /api/v1/papers/{slug}
GET /api/v1/topics
GET /api/v1/methods
GET /api/v1/trends/papers
GET /api/v1/trends/topics
GET /api/v1/trends/methods
GET /api/v1/stats
```

Keep popularity, citation growth and quality separate.

**Step 4: Verify GREEN**

```bash
cd services/api
uv run pytest -v
```

**Step 5: Commit**

```bash
git add services/api
git commit -m "feat: expose paper search and trend APIs"
```

### Task 5: Build the directory design system and frontend data layer

**Files:**
- Create: `apps/web/src/lib/api.ts`
- Create: `apps/web/src/lib/types.ts`
- Create: `apps/web/src/lib/format.ts`
- Create: `apps/web/src/components/site-header.tsx`
- Create: `apps/web/src/components/search-box.tsx`
- Create: `apps/web/src/components/stat-card.tsx`
- Create: `apps/web/src/components/paper-card.tsx`
- Create: `apps/web/src/components/trend-list.tsx`
- Create: `apps/web/src/components/filter-bar.tsx`
- Create: `apps/web/src/components/theme-toggle.tsx`
- Create: `apps/web/src/components/components.test.tsx`
- Modify: `apps/web/src/app/globals.css`
- Modify: `apps/web/src/app/layout.tsx`

**Step 1: Write failing component tests**

Cover:

- semantic headings and navigation;
- search label and submit behavior;
- paper card title, authors, date, type, code/data badges;
- trend window and formula disclosure;
- keyboard focus and theme toggle accessible names;
- no structural Emoji icons.

**Step 2: Verify RED**

```bash
pnpm --dir apps/web test src/components/components.test.tsx
```

**Step 3: Implement components using the persisted design system**

Read:

```text
design-system/paper-research-hub/MASTER.md
```

Required UI:

- directory-style hero search;
- institutional navy and research accent tokens;
- Atkinson Hyperlegible body and Crimson Pro headings;
- 44px interaction targets;
- visible focus;
- light/dark themes;
- responsive density;
- reduced-motion support.

**Step 4: Verify GREEN**

```bash
pnpm --dir apps/web test
pnpm --dir apps/web build
```

**Step 5: Commit**

```bash
git add apps/web design-system
git commit -m "feat: add paper directory component system"
```

### Task 6: Implement public portal pages

**Files:**
- Modify: `apps/web/src/app/page.tsx`
- Create: `apps/web/src/app/papers/page.tsx`
- Create: `apps/web/src/app/papers/[slug]/page.tsx`
- Create: `apps/web/src/app/trends/page.tsx`
- Create: `apps/web/src/app/topics/[slug]/page.tsx`
- Create: `apps/web/src/app/methods/[slug]/page.tsx`
- Create: `apps/web/src/app/loading.tsx`
- Create: `apps/web/src/app/error.tsx`
- Create: `apps/web/src/app/not-found.tsx`
- Create: `apps/web/src/app/sitemap.ts`
- Create: `apps/web/src/app/rss.xml/route.ts`
- Create: `apps/web/src/app/pages.test.tsx`

**Step 1: Write failing page tests**

Cover:

- homepage renders stats, latest, trending papers, topics and methods;
- URL query parameters preserve filters;
- paper detail renders provenance and version history;
- trends page exposes separate rankings;
- empty and error states provide recovery actions;
- JSON-LD uses `ScholarlyArticle`.

**Step 2: Verify RED**

```bash
pnpm --dir apps/web test src/app/pages.test.tsx
```

**Step 3: Implement pages against the FastAPI contract**

Do not hardcode paper cards. All content comes from the API.

**Step 4: Verify GREEN**

```bash
pnpm --dir apps/web test
pnpm --dir apps/web build
```

**Step 5: Commit**

```bash
git add apps/web
git commit -m "feat: build public paper trend portal"
```

### Task 7: Add deployment, operations, and data-governance configuration

**Files:**
- Create: `docker-compose.yml`
- Create: `services/api/Dockerfile`
- Create: `apps/web/Dockerfile`
- Create: `infra/cloud-run/api-service.yaml`
- Create: `infra/cloud-run/sync-job.yaml`
- Create: `infra/cloud-run/snapshot-job.yaml`
- Create: `scripts/dev.sh`
- Create: `scripts/sync.sh`
- Create: `docs/deployment.md`
- Create: `docs/data-governance.md`
- Create: `README.md`
- Create: `.github/workflows/ci.yml`

**Step 1: Write failing configuration checks**

Create a script or test that verifies:

- all required services exist in Compose;
- healthchecks are configured;
- environment variables are documented;
- sync jobs do not run inside the web process;
- PDF storage is disabled unless license is approved.

**Step 2: Verify RED**

```bash
make verify-config
```

**Step 3: Implement deployment configuration**

Local:

```bash
docker compose up --build
```

Production:

- Next.js frontend on Cloudflare/Vercel or container;
- FastAPI API on Cloud Run;
- sync and snapshot jobs as separate Cloud Run Jobs;
- managed PostgreSQL;
- R2/S3 for raw records and approved-license documents.

**Step 4: Verify GREEN**

```bash
make verify-config
docker compose config
docker compose build
```

**Step 5: Commit**

```bash
git add .
git commit -m "ops: add reproducible deployment configuration"
```

### Task 8: End-to-end, accessibility, and responsive verification

**Files:**
- Create: `apps/web/playwright.config.ts`
- Create: `apps/web/e2e/portal.spec.ts`
- Create: `apps/web/e2e/accessibility.spec.ts`
- Create: `scripts/verify.sh`
- Modify: `.github/workflows/ci.yml`

**Step 1: Write failing E2E tests**

Cover:

- homepage loads from a clean database after sync;
- search returns matching papers;
- filters update URL and results;
- paper detail opens;
- trend windows switch;
- keyboard navigation works;
- 375, 768, 1024 and 1440 widths have no horizontal overflow;
- reduced-motion mode remains usable.

**Step 2: Verify RED**

```bash
pnpm --dir apps/web exec playwright test
```

**Step 3: Implement only changes required by failing E2E tests**

Do not add unrelated features.

**Step 4: Run full verification**

```bash
./scripts/verify.sh
```

Expected:

- Python tests pass;
- frontend unit tests pass;
- lint passes;
- production builds pass;
- Playwright passes;
- Docker configuration validates.

**Step 5: Final review and commit**

```bash
git add .
git commit -m "test: verify paper hub end to end"
```

