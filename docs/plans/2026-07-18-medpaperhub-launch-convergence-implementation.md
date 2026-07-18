# medpaperhub Launch Convergence Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Deliver a locally deployable medpaperhub that ingests real multi-source papers, publishes only records with verified official URLs, extracts evidence-bound research routes from abstracts through a strict OpenAI-compatible Responses API schema, computes trends and journal profiles, and explains journal fit for a user abstract.

**Architecture:** Keep the decoupled Go API/Worker/Migrate, Nuxt Web, PostgreSQL, OpenAPI, and immutable Catalog boundaries. Execute nine business tasks as vertical release slices; database, deduplication, watermarks, recovery, APIs, UI, tests, and deployment are completion criteria inside those slices rather than separate product milestones.

**Tech Stack:** Go 1.26, PostgreSQL 18, Nuxt 4, Vue 3, TypeScript, OpenAPI 3.1, OpenAI-compatible Responses API, strict JSON Schema, Vitest, Playwright, Docker Compose, shell deployment scripts, GitHub Actions.

---

## Execution rules

- Work only in `/Users/huangrende/Documents/论文自媒体/论文聚合门户/.worktrees/go-nuxt-replatform`.
- Use TDD for every behavior change.
- Run `git diff --check` before every commit.
- Keep real API keys, JCR exports, and replay inputs out of Git.
- Do not use synthetic JCR or fake papers for a release Catalog.
- Do not add fallback model providers, free-form text parsing, regex repair, guessed URLs, title similarity merging, or hidden aggregate scores.
- Every new source connector must persist raw records before normalization.
- A connector watermark advances only after the entire claimed batch commits.
- A public Work must have an active verified official URL.
- `publicly_visible` and `analysis_ready` remain separate.
- Every OpenAI analysis output must pass the strict schema and evidence-substring validation.
- Stop expanding a task when its listed business acceptance passes; move non-blocking additions to the post-launch backlog.

## Release gates

```text
Gate A: daily facts
Registry → source discovery → dedupe → verified URL → Facts Catalog → daily Nuxt feed

Gate B: research intelligence
Abstract route analysis → readiness → weekly/monthly trends → 12/24-month journal profiles

Gate C: journal fit
User abstract → same structured schema → explainable journal-profile comparison

Gate D: local release
Clean database → fixed inputs → publish → API/Web 200 → replay without duplicates
```

---

### Task 0: Create the deployment contract before feature expansion

**Business task:** Task 9 foundation.

**Files:**
- Create: `deploy/README.md`
- Create: `deploy/compose/compose.local.yml`
- Create: `deploy/env/local.env.example`
- Create: `deploy/env/pipeline.env.example`
- Create: `deploy/manifests/replay.example.yaml`
- Create: `deploy/scripts/lib.sh`
- Create: `deploy/scripts/up-empty.sh`
- Create: `deploy/scripts/verify-empty.sh`
- Create: `deploy/scripts/local-release.sh`
- Create: `deploy/scripts/verify-release.sh`
- Create: `deploy/sql/verify-release.sql`
- Create: `deploy/tests/deploy_contract_test.sh`
- Modify: `Makefile`
- Modify: `.github/workflows/container.yml`

**Step 1: Write the failing deployment contract test**

The test must require:

- all listed files exist;
- shell scripts pass `sh -n`;
- `compose.local.yml` can be merged with the root Compose model;
- real release verification rejects Catalog `503`;
- `local-release.sh` rejects a missing manifest and missing absolute input paths;
- no committed file contains an API key value.

Run:

```bash
sh deploy/tests/deploy_contract_test.sh
```

Expected: FAIL because `deploy/` does not exist.

**Step 2: Add the minimum deployment skeleton**

Keep the root `docker-compose.yml` as the canonical service definition. The local deployment
overlay may add profiles and mounts but must not duplicate API, Web, Worker, Migrate, or database
commands.

`local-release.sh` is initially a strict orchestrator skeleton. It must fail with an explicit
“pipeline stage not implemented” status for unimplemented stages rather than reporting success.

**Step 3: Add Make targets**

```text
local-up
local-verify-empty
local-release
local-verify-release
```

`local-up` may accept the empty Catalog contract. `local-verify-release` may only accept a
published Catalog.

**Step 4: Run verification**

```bash
sh deploy/tests/deploy_contract_test.sh
make compose-config
git diff --check
```

Expected: PASS.

**Step 5: Commit**

```bash
git add deploy Makefile .github/workflows/container.yml
git commit -m "ops: add local release deployment contract"
```

---

### Task 1: Finish the three-domain and four-channel Registry

**Business task:** Task 1 completion.

**Files:**
- Create: `services/core/migrations/000020_content_channel_registries.sql`
- Create: `services/core/internal/registry/model.go`
- Create: `services/core/internal/registry/model_test.go`
- Create: `services/core/internal/registry/import.go`
- Create: `services/core/internal/registry/import_test.go`
- Create: `services/core/internal/registry/postgres_store.go`
- Create: `services/core/internal/registry/postgres_store_test.go`
- Create: `services/core/cmd/worker/registry_import_command_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Create: `data/registries/domains.v1.csv`
- Create: `data/registries/preprint-sources.v1.csv`
- Create: `data/registries/conference-sources.v1.csv`
- Modify: `contracts/openapi.yaml`

**Step 1: Write failing Registry tests**

Require exactly:

```text
domains:
  medicine
  biology
  computer_science

channels:
  journal_published
  accepted_early
  preprint
  conference_proceeding
```

Journal channels use JCR `journal-all-q1/v2`. Preprint and conference channels require exact,
versioned source Registry matches and must reject JCR evidence as their eligibility decision.

**Step 2: Run the failing tests**

```bash
go -C services/core test ./internal/registry ./internal/database ./cmd/worker \
  -run 'TestDomainRegistry|TestChannelRegistry|TestRegistryImport' -count=1
```

Expected: FAIL.

**Step 3: Implement immutable Registry imports**

Persist:

- registry name and version;
- file SHA-256;
- import receipt;
- exact source identifiers;
- active lifecycle;
- channel;
- allowed domains.

Re-importing identical bytes is idempotent. Reusing the same Registry version with different bytes
must fail.

**Step 4: Run verification**

```bash
go -C services/core test ./internal/registry ./internal/venue ./internal/database ./cmd/worker -count=1
go -C services/core test ./... -count=1
git diff --check
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/migrations/000020_content_channel_registries.sql \
  services/core/internal/registry services/core/cmd/worker \
  data/registries contracts/openapi.yaml
git commit -m "feat(registry): add domains and content channels"
```

---

### Task 2: Add lossless records, durable connector runs, and Crossref hot discovery

**Business task:** Task 2, first slice.

**Files:**
- Create: `services/core/migrations/000021_connector_runs.sql`
- Modify: `services/core/internal/ingestion/envelope.go`
- Modify: `services/core/internal/ingestion/envelope_test.go`
- Create: `services/core/internal/ingestion/connector_run.go`
- Create: `services/core/internal/ingestion/connector_run_test.go`
- Create: `services/core/internal/ingestion/postgres_connector_run.go`
- Create: `services/core/internal/ingestion/postgres_connector_run_test.go`
- Modify: `services/core/internal/source/crossref/client.go`
- Modify: `services/core/internal/source/crossref/client_test.go`
- Modify: `services/core/internal/source/crossref/parse.go`
- Modify: `services/core/internal/source/crossref/parse_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Create: `services/core/cmd/worker/crossref_hot_discovery_command_test.go`

**Step 1: Write failing normalized-record/v4 tests**

Require field-level assertions for:

- title and abstract;
- identifiers;
- authors and affiliations;
- venue;
- lifecycle and channel event dates;
- source URLs and relations;
- source path and parser version.

No whole-record “latest source wins” operation is allowed.

**Step 2: Write failing connector-run tests**

Require:

- typed stream and watermark;
- run status and claimed interval;
- page receipts;
- compare-and-swap watermark advancement;
- no advancement on fetch, parse, persistence, or projection failure;
- idempotent replay of an identical source revision.

**Step 3: Run failing tests**

```bash
go -C services/core test ./internal/ingestion ./internal/source/crossref ./cmd/worker \
  -run 'TestNormalizedRecordV4|TestConnectorRun|TestCrossrefCreatedUpdated' -count=1
```

Expected: FAIL.

**Step 4: Implement Crossref created and updated streams**

Add explicit Worker commands:

```text
sync crossref-created
sync crossref-updated
```

Use Crossref `created` for hot discovery and `updated` for revisions. Persist `indexed` as source
evidence only; it must never become first discovery time.

**Step 5: Run verification**

```bash
go -C services/core test ./internal/ingestion ./internal/source/crossref ./cmd/worker -count=1
go -C services/core test ./... -count=1
git diff --check
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations/000021_connector_runs.sql \
  services/core/internal/ingestion services/core/internal/source/crossref \
  services/core/cmd/worker
git commit -m "feat(ingestion): add durable Crossref discovery"
```

---

### Task 3: Add the minimum preprint and conference source set

**Business task:** Task 2, second slice.

**Files:**
- Create: `services/core/internal/source/biorxiv/client.go`
- Create: `services/core/internal/source/biorxiv/client_test.go`
- Create: `services/core/internal/source/biorxiv/parse.go`
- Create: `services/core/internal/source/biorxiv/parse_test.go`
- Create: `services/core/internal/source/medrxiv/client.go`
- Create: `services/core/internal/source/medrxiv/client_test.go`
- Create: `services/core/internal/source/medrxiv/parse.go`
- Create: `services/core/internal/source/medrxiv/parse_test.go`
- Create: `services/core/internal/source/arxiv/client.go`
- Create: `services/core/internal/source/arxiv/client_test.go`
- Create: `services/core/internal/source/arxiv/parse.go`
- Create: `services/core/internal/source/arxiv/parse_test.go`
- Create: `services/core/internal/source/ieee/client.go`
- Create: `services/core/internal/source/ieee/client_test.go`
- Create: `services/core/internal/source/ieee/parse.go`
- Create: `services/core/internal/source/ieee/parse_test.go`
- Create: `services/core/internal/source/acm/feed.go`
- Create: `services/core/internal/source/acm/feed_test.go`
- Modify: `services/core/internal/config/config.go`
- Modify: `services/core/internal/config/config_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Create: `services/core/cmd/worker/multi_source_command_test.go`
- Modify: `.env.example`

**Step 1: Write failing source identity tests**

Require separate source identities, credentials, parser versions, and watermarks for bioRxiv and
medRxiv. Require arXiv version, submitted/updated dates, categories, DOI relation, and official
abstract URL. Require IEEE/ACM conference records to bind an allowed conference Registry entry.

**Step 2: Write failing client replay tests**

Use fixed HTTP fixtures and require:

- pagination;
- rate-limit handling;
- no watermark advancement on a failed page;
- identical raw checksums and projections after replay;
- no generic ACM HTML crawler.

**Step 3: Run failing tests**

```bash
go -C services/core test \
  ./internal/source/biorxiv ./internal/source/medrxiv \
  ./internal/source/arxiv ./internal/source/ieee ./internal/source/acm \
  ./internal/config ./cmd/worker -count=1
```

Expected: FAIL.

**Step 4: Implement source commands**

```text
sync biorxiv
sync medrxiv
sync arxiv
sync ieee-xplore
sync acm-feed
```

Each command must use the common connector-run transaction and normalized-record/v4 projection.

**Step 5: Run verification**

```bash
go -C services/core test ./internal/source/... ./internal/ingestion ./cmd/worker -count=1
go -C services/core test ./... -count=1
git diff --check
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/internal/source services/core/internal/config \
  services/core/cmd/worker .env.example
git commit -m "feat(source): add preprint and conference discovery"
```

---

### Task 4: Add Work Family, verified official URLs, and the Facts Catalog

**Business tasks:** Task 3 and Task 4.

**Files:**
- Create: `services/core/migrations/000022_work_families.sql`
- Create: `services/core/migrations/000023_official_urls.sql`
- Create: `services/core/migrations/000024_visibility_states.sql`
- Create: `services/core/internal/workfamily/model.go`
- Create: `services/core/internal/workfamily/model_test.go`
- Create: `services/core/internal/workfamily/postgres_store.go`
- Create: `services/core/internal/workfamily/postgres_store_test.go`
- Create: `services/core/internal/urlverify/model.go`
- Create: `services/core/internal/urlverify/model_test.go`
- Create: `services/core/internal/urlverify/verifier.go`
- Create: `services/core/internal/urlverify/verifier_test.go`
- Create: `services/core/internal/urlverify/postgres_store.go`
- Create: `services/core/internal/urlverify/postgres_store_test.go`
- Modify: `services/core/internal/catalog/publisher.go`
- Modify: `services/core/internal/catalog/publisher_integration_test.go`
- Modify: `services/core/internal/catalog/home_contract.go`
- Modify: `services/core/internal/httpapi/catalog.go`
- Modify: `contracts/openapi.yaml`
- Modify: `apps/web/app/pages/index.vue`
- Modify: `apps/web/app/components/PublicationEventCard.vue`
- Modify: `apps/web/tests/biomedical-home.test.ts`

**Step 1: Write failing Work Family tests**

Require:

- every Work belongs to one active family;
- singleton family when no relation exists;
- only explicit source relations or stable shared identifiers can link versions;
- no title/author similarity merge;
- Version of Record is canonical while preprint history remains visible.

**Step 2: Write failing URL tests**

Model:

```text
url_candidate
→ immutable verification
→ active current official link
```

Require HTTPS, bounded redirects, stable identifier match, verification version, checked time,
expiry, and reason. The Web may not build a DOI or publisher URL.

**Step 3: Write failing Facts Catalog tests**

Require:

- `publicly_visible` independent of `analysis_ready`;
- active verified official URL as a publication gate;
- formal, preprint, conference, Accepted/Early, and newly observed collections;
- `first_observed_at` distinct from publication/posted/conference time;
- Facts Catalog publish without OpenAI, PubMed, OpenAlex, citation, trend, or journal analysis.

**Step 4: Run failing tests**

```bash
go -C services/core test ./internal/workfamily ./internal/urlverify \
  ./internal/catalog ./internal/httpapi -count=1
pnpm --dir apps/web exec vitest run tests/biomedical-home.test.ts \
  --config vitest.config.ts
```

Expected: FAIL.

**Step 5: Implement and verify**

```bash
go -C services/core test ./internal/workfamily ./internal/urlverify \
  ./internal/catalog ./internal/httpapi -count=1
pnpm --dir apps/web test
pnpm --dir apps/web typecheck
git diff --check
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations/000022_work_families.sql \
  services/core/migrations/000023_official_urls.sql \
  services/core/migrations/000024_visibility_states.sql \
  services/core/internal/workfamily services/core/internal/urlverify \
  services/core/internal/catalog services/core/internal/httpapi \
  contracts/openapi.yaml apps/web
git commit -m "feat(catalog): publish verified multi-channel facts"
```

**Gate A acceptance**

From a clean test database and fixed multi-source fixtures:

- at least one formal article, preprint, conference paper, and Accepted/Early record is public;
- every public item has a verified official URL;
- replay creates no duplicate Work;
- `/api/v1/home` returns `200` with `X-Catalog-Generation`;
- Nuxt renders the five daily facts collections.

---

### Task 5: Add strict OpenAI-compatible abstract research-route analysis

**Business task:** Task 5.

**Files:**
- Create: `services/core/migrations/000025_abstract_route_analysis.sql`
- Create: `services/core/internal/abstractanalysis/schema.go`
- Create: `services/core/internal/abstractanalysis/schema_test.go`
- Create: `services/core/internal/abstractanalysis/evidence.go`
- Create: `services/core/internal/abstractanalysis/evidence_test.go`
- Create: `services/core/internal/abstractanalysis/service.go`
- Create: `services/core/internal/abstractanalysis/service_test.go`
- Create: `services/core/internal/abstractanalysis/postgres_store.go`
- Create: `services/core/internal/abstractanalysis/postgres_store_test.go`
- Create: `services/core/internal/openairesponses/client.go`
- Create: `services/core/internal/openairesponses/client_test.go`
- Create: `services/core/internal/openairesponses/types.go`
- Modify: `services/core/internal/config/config.go`
- Modify: `services/core/internal/config/config_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Create: `services/core/cmd/worker/abstract_analysis_command_test.go`
- Modify: `.env.example`

**Step 1: Write failing configuration tests**

Add:

```go
const RoleAbstractAnalysis Role = "abstract-analysis"
```

Require non-empty, trimmed:

```text
OPENAI_BASE_URL
OPENAI_API_KEY
OPENAI_MODEL
```

Validate `OPENAI_BASE_URL` as an explicit HTTP(S) origin/base path without credentials. Redacted
configuration must expose only whether the key is configured, never the key itself.

**Step 2: Write failing strict schema tests**

Define `abstract-route/v1` with the fields frozen in the design. Every field is:

```json
{
  "state": "supported | not_reported",
  "value": "...",
  "evidence": ["exact abstract substring"]
}
```

The root schema must set:

```text
type=object
additionalProperties=false
required=[all fields]
strict=true
```

`not_reported` requires an empty value and empty evidence. `supported` requires a non-empty value
and at least one evidence string.

**Step 3: Write failing HTTP client tests**

Use `httptest.Server` and verify the request:

- targets `${OPENAI_BASE_URL}/responses` according to normalized base-path rules;
- sends Bearer authentication without logging it;
- sends the configured model;
- uses Responses API input messages;
- uses strict JSON Schema structured output;
- records response ID and usage metadata;
- rejects non-2xx, refusal, incomplete output, invalid JSON, extra fields, and schema mismatch;
- does not retry schema-invalid output with free-form text.

**Step 4: Write failing evidence validation tests**

Normalize Unicode and whitespace deterministically, then require every evidence item to exist in
the normalized input abstract. Reject the whole result when any evidence is not source-backed.

**Step 5: Run failing tests**

```bash
go -C services/core test ./internal/config ./internal/openairesponses \
  ./internal/abstractanalysis ./cmd/worker \
  -run 'TestOpenAI|TestAbstractRoute|TestEvidence' -count=1
```

Expected: FAIL.

**Step 6: Implement the minimal client and service**

Worker command:

```text
analyze abstract-routes
  --prompt-version abstract-route-prompt/v1
  --schema-version abstract-route/v1
  --analysis-cutoff <RFC3339Nano>
  --limit <N>
```

Persist:

- Work and exact Work revision;
- model provider and configured model name;
- prompt/schema version;
- title and abstract input SHA-256;
- source assertion references;
- response ID;
- validated structured output;
- timing, usage, status, and failure code.

Never persist the API key or request Authorization header.

**Step 7: Run verification**

```bash
go -C services/core test ./internal/config ./internal/openairesponses \
  ./internal/abstractanalysis ./cmd/worker -count=1
go -C services/core test ./... -count=1
git diff --check
```

Expected: PASS.

**Step 8: Commit**

```bash
git add services/core/migrations/000025_abstract_route_analysis.sql \
  services/core/internal/abstractanalysis services/core/internal/openairesponses \
  services/core/internal/config services/core/cmd/worker .env.example
git commit -m "feat(analysis): extract evidence-bound abstract routes"
```

---

### Task 6: Add PubMed/OpenAlex enrichment and analysis readiness

**Business tasks:** Task 5 enrichment and Task 6 readiness.

**Files:**
- Create: `services/core/migrations/000026_enrichment_outbox.sql`
- Create: `services/core/migrations/000027_analysis_readiness.sql`
- Modify: `services/core/internal/source/pubmed/bulk.go`
- Modify: `services/core/internal/source/pubmed/bulk_test.go`
- Modify: `services/core/internal/source/openalex/client.go`
- Modify: `services/core/internal/source/openalex/client_test.go`
- Create: `services/core/internal/enrichment/outbox.go`
- Create: `services/core/internal/enrichment/outbox_test.go`
- Create: `services/core/internal/enrichment/postgres_outbox.go`
- Create: `services/core/internal/enrichment/postgres_outbox_test.go`
- Create: `services/core/internal/readiness/service.go`
- Create: `services/core/internal/readiness/service_test.go`
- Create: `services/core/internal/readiness/postgres_store.go`
- Create: `services/core/internal/readiness/postgres_store_test.go`
- Modify: `services/core/cmd/worker/main.go`

**Step 1: Write failing enrichment tests**

Require:

- PubMed hourly windows and Daily Update replay use durable watermarks;
- publication history remains source-record consistent;
- stable DOI creates one OpenAlex enrichment event;
- replay does not duplicate the event;
- enrichment failure does not revoke Facts Catalog visibility.

**Step 2: Write failing readiness tests**

`analysis_ready=true` requires:

- public Work revision at or before cutoff;
- article-level domain;
- required structured taxonomy fields;
- succeeded abstract-route analysis for the same revision;
- no decisive source conflict;
- active verified official URL.

Unknown or failed enhancement must be explicit; it must not become numeric zero.

**Step 3: Run failing tests**

```bash
go -C services/core test ./internal/enrichment ./internal/readiness \
  ./internal/source/pubmed ./internal/source/openalex ./cmd/worker -count=1
```

Expected: FAIL.

**Step 4: Implement and verify**

```bash
go -C services/core test ./internal/enrichment ./internal/readiness \
  ./internal/source/pubmed ./internal/source/openalex ./cmd/worker -count=1
go -C services/core test ./... -count=1
git diff --check
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/migrations/000026_enrichment_outbox.sql \
  services/core/migrations/000027_analysis_readiness.sql \
  services/core/internal/enrichment services/core/internal/readiness \
  services/core/internal/source/pubmed services/core/internal/source/openalex \
  services/core/cmd/worker
git commit -m "feat(enrichment): add evidence readiness pipeline"
```

---

### Task 7: Build weekly/monthly trends and 12/24-month journal profiles

**Business task:** Task 6.

**Files:**
- Create: `services/core/migrations/000028_research_intelligence.sql`
- Modify: `services/core/internal/analysis/trends.go`
- Modify: `services/core/internal/analysis/trends_test.go`
- Modify: `services/core/internal/analysis/journals.go`
- Modify: `services/core/internal/analysis/journals_test.go`
- Create: `services/core/internal/analysis/technologies.go`
- Create: `services/core/internal/analysis/technologies_test.go`
- Create: `services/core/internal/analysis/postgres_technologies.go`
- Create: `services/core/internal/analysis/postgres_technologies_test.go`
- Modify: `services/core/internal/catalog/biomedical_analysis.go`
- Modify: `services/core/internal/catalog/publisher.go`
- Modify: `services/core/cmd/worker/main.go`
- Modify: `contracts/openapi.yaml`

**Step 1: Write failing cohort tests**

Require:

```text
trend period = week | month
journal window_months = 12 | 24
counting_unit = work_family
```

Only analysis-ready Works before cutoff enter the cohort.

**Step 2: Write failing output tests**

Trends expose separate volume, change, support, coverage, effect size, interval, and FDR values.
Technology exposes independent Novelty, Momentum, Diffusion, and Maturity states. Journal profiles
expose distributions for route fields and recent growth/decline. No hidden combined score.

**Step 3: Run failing tests**

```bash
go -C services/core test ./internal/analysis ./internal/catalog ./cmd/worker \
  -run 'TestWeeklyMonthly|TestJournalWindow|TestTechnology' -count=1
```

Expected: FAIL.

**Step 4: Implement immutable analysis snapshots**

Bind each snapshot to:

- analysis cutoff;
- Facts Catalog generation;
- domain/channel Registry versions;
- JCR receipt/policy;
- abstract prompt/schema/model version;
- cohort and counting unit.

**Step 5: Run verification**

```bash
go -C services/core test ./internal/analysis ./internal/catalog ./cmd/worker -count=1
pnpm --dir apps/web generate:api-types
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
git diff --check
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations/000028_research_intelligence.sql \
  services/core/internal/analysis services/core/internal/catalog \
  services/core/cmd/worker contracts/openapi.yaml \
  apps/web/app/types/openapi.generated.ts
git commit -m "feat(intelligence): add trends and journal profiles"
```

**Gate B acceptance**

- at least one fixed paper has a validated abstract route;
- non-ready records never enter analysis;
- weekly/monthly and 12/24-month results are reproducible;
- preprint plus Version of Record counts once by default;
- API exposes explicit insufficient-evidence states.

---

### Task 8: Add explainable journal-fit analysis

**Business task:** Task 7.

**Files:**
- Create: `services/core/migrations/000029_journal_fit.sql`
- Create: `services/core/internal/journalfit/model.go`
- Create: `services/core/internal/journalfit/model_test.go`
- Create: `services/core/internal/journalfit/service.go`
- Create: `services/core/internal/journalfit/service_test.go`
- Create: `services/core/internal/journalfit/postgres_store.go`
- Create: `services/core/internal/journalfit/postgres_store_test.go`
- Modify: `services/core/internal/abstractanalysis/service.go`
- Modify: `services/core/internal/httpapi/catalog.go`
- Modify: `services/core/internal/httpapi/catalog_integration_test.go`
- Modify: `contracts/openapi.yaml`

**Step 1: Write failing journal-fit tests**

Input:

```text
title
abstract
domain
window_months = 12 | 24
```

Use the same abstract-route prompt/schema/model version as indexed papers. Compare independent
dimensions:

- topic/problem;
- purpose;
- research mode;
- study design;
- technical route;
- article/content type.

Each recommendation must include supporting profile evidence and explicit mismatch reasons. Do not
return acceptance probability or one hidden score.

**Step 2: Write failing API tests**

Add:

```text
POST /api/v1/journal-fit
GET  /api/v1/journal-fit/{id}
```

Require bounded input size, request ID, immutable analysis identity, no API key exposure, and
problem+json errors.

**Step 3: Run failing tests**

```bash
go -C services/core test ./internal/journalfit ./internal/httpapi -count=1
```

Expected: FAIL.

**Step 4: Implement and verify**

```bash
go -C services/core test ./internal/journalfit ./internal/abstractanalysis \
  ./internal/httpapi -count=1
go -C services/core test ./... -count=1
pnpm --dir apps/web generate:api-types
git diff --check
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/migrations/000029_journal_fit.sql \
  services/core/internal/journalfit services/core/internal/abstractanalysis \
  services/core/internal/httpapi contracts/openapi.yaml \
  apps/web/app/types/openapi.generated.ts
git commit -m "feat(journal-fit): compare abstracts with journal profiles"
```

---

### Task 9: Finish the daily-intelligence Nuxt product

**Business task:** Task 8.

**Required skills before implementation:** `@ui-ux-pro-max`; use `@imagegen` only if a raster asset is required.

**Files:**
- Modify: `design-system/paper-research-hub/MASTER.md`
- Modify: `design-system/paper-research-hub/pages/home.md`
- Modify: `design-system/paper-research-hub/pages/papers.md`
- Modify: `design-system/paper-research-hub/pages/trends.md`
- Create: `design-system/paper-research-hub/pages/journal-fit.md`
- Modify: `apps/web/app/pages/index.vue`
- Modify: `apps/web/app/pages/papers/index.vue`
- Modify: `apps/web/app/pages/papers/[id].vue`
- Modify: `apps/web/app/pages/journals/[slug].vue`
- Modify: `apps/web/app/pages/trends.vue`
- Create: `apps/web/app/pages/journal-fit.vue`
- Create: `apps/web/app/components/ResearchRoute.vue`
- Create: `apps/web/app/components/VersionTimeline.vue`
- Create: `apps/web/app/components/ContentChannelTabs.vue`
- Create: `apps/web/app/components/JournalFitResult.vue`
- Modify: `apps/web/app/components/AppHeader.vue`
- Modify: `apps/web/app/components/CatalogPaperCard.vue`
- Modify: `apps/web/app/utils/catalogApi.ts`
- Modify: `apps/web/tests/home.test.ts`
- Modify: `apps/web/tests/paper-detail.test.ts`
- Modify: `apps/web/tests/journals.test.ts`
- Modify: `apps/web/tests/biomedical-trends.test.ts`
- Create: `apps/web/tests/journal-fit.test.ts`
- Modify: `apps/web/e2e/replatform.spec.ts`

**Step 1: Write failing page tests**

Require:

- no homepage hero search box;
- concise five-stream daily feed;
- official URL as primary paper action;
- explicit preprint and conference labels;
- research route with `not_reported` states;
- Work Family timeline;
- week/month and 12/24 query state preserved in URL;
- journal profile distributions;
- journal-fit form and explainable results;
- no fake zero charts or explanatory marketing blocks.

**Step 2: Run failing tests**

```bash
pnpm --dir apps/web exec vitest run \
  tests/home.test.ts tests/paper-detail.test.ts tests/journals.test.ts \
  tests/biomedical-trends.test.ts tests/journal-fit.test.ts \
  --config vitest.config.ts
```

Expected: FAIL.

**Step 3: Update the design system, then implement**

Use the existing visual system and approved concise card-first direction. Do not add a raster
asset unless the design requires one.

**Step 4: Run full Web verification**

```bash
pnpm --dir apps/web lint
pnpm --dir apps/web typecheck
pnpm --dir apps/web test
pnpm --dir apps/web build
pnpm --dir apps/web test:e2e
git diff --check
```

Expected: PASS.

**Step 5: Commit**

```bash
git add design-system/paper-research-hub apps/web
git commit -m "feat(web): deliver daily research intelligence portal"
```

**Gate C acceptance**

- user can read daily facts without searching;
- user can inspect abstract route and official source;
- user can inspect journal 12/24-month profile;
- user can submit an abstract and receive dimension-by-dimension journal fit.

---

### Task 10: Complete strict local release, readiness, replay, and documentation

**Business task:** Task 9 completion.

**Files:**
- Modify: `deploy/scripts/local-release.sh`
- Modify: `deploy/scripts/verify-release.sh`
- Create: `deploy/scripts/replay-fixed.sh`
- Create: `deploy/scripts/backup.sh`
- Create: `deploy/scripts/restore.sh`
- Create: `deploy/scripts/rollback.sh`
- Modify: `deploy/sql/verify-release.sql`
- Modify: `deploy/manifests/replay.example.yaml`
- Create: `deploy/manifests/schema.json`
- Modify: `deploy/README.md`
- Create: `services/core/internal/observability/metrics.go`
- Create: `services/core/internal/observability/metrics_test.go`
- Modify: `services/core/internal/httpapi/server.go`
- Modify: `services/core/internal/httpapi/server_test.go`
- Modify: `docs/deployment/verify-local.sh`
- Modify: `docker-compose.yml`
- Modify: `docker-compose.test.yml`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/container.yml`
- Modify: `README.md`

**Step 1: Write failing readiness and release tests**

Require:

```text
GET /live
GET /ready
GET /metrics
```

`/ready` must verify database connectivity, migration compatibility, and the expected Catalog
publication state. `verify-release.sh` must reject:

- HTTP 503;
- zero public papers;
- any public paper without verified official URL;
- no succeeded abstract-route run;
- no trend or journal profile snapshots;
- a generation mismatch.

**Step 2: Write failing replay tests**

Manifest must pin:

- file paths and SHA-256;
- Registry versions;
- JCR license identity;
- source windows or fixed raw bundles;
- analysis cutoff;
- prompt/schema/model version;
- formula versions.

`replay-fixed.sh` may not call mutable online APIs. Online collection is named `bootstrap-live`,
not deterministic replay.

**Step 3: Run failing tests**

```bash
go -C services/core test ./internal/httpapi ./internal/observability \
  -run 'TestLive|TestReady|TestMetrics' -count=1
sh deploy/tests/deploy_contract_test.sh
```

Expected: FAIL.

**Step 4: Implement the complete release orchestrator**

`make local-release MANIFEST=/absolute/path/to/replay.yaml` must execute:

```text
validate manifest and secrets
→ start PostgreSQL
→ migrate
→ import Registries
→ import authorized JCR
→ replay fixed raw inputs
→ assess channels and venues
→ build Work Families
→ verify official URLs
→ publish Facts Catalog
→ run abstract-route analysis
→ run enrichment/readiness
→ build trends and journal profiles
→ publish Analysis Catalog
→ start API/Web
→ verify release
→ replay the same inputs
→ verify no duplicates
```

Print only non-secret result identifiers and local URLs.

**Step 5: Run complete verification**

```bash
gofmt -w services/core
go -C services/core vet ./...
go -C services/core test ./...
go -C services/core test -race ./...
pnpm --dir apps/web lint
pnpm --dir apps/web typecheck
pnpm --dir apps/web test
pnpm --dir apps/web build
pnpm --dir apps/web test:e2e
make compose-config
make smoke
sh deploy/tests/deploy_contract_test.sh
git diff --check
```

Expected: PASS.

With authorized fixed inputs:

```bash
make local-release MANIFEST=/absolute/path/to/replay.yaml
make local-verify-release
```

Expected:

- Web `http://localhost:3000` is reachable;
- API `http://localhost:8080` is reachable;
- `/api/v1/home` returns 200 and `X-Catalog-Generation`;
- daily facts, research routes, trends, journal profiles, and journal fit are usable;
- second replay creates no duplicates.

**Step 6: Remove stale product truth**

Search production code and public docs for:

```text
AI Agent-only scope
biomedical-only scope
JIF >= 10
JIF percentile as eligibility
waiting for first sync
search-first homepage
browser-built DOI URLs
```

Delete or update every stale production claim. Test fixtures may retain legacy strings only when a
test explicitly proves rejection.

**Step 7: Commit**

```bash
git add deploy services/core/internal/observability \
  services/core/internal/httpapi docs/deployment docker-compose.yml \
  docker-compose.test.yml .github README.md
git commit -m "ops: complete reproducible local release"
```

**Gate D acceptance**

The project is complete only when a new operator can follow `deploy/README.md`, provide authorized
inputs and configured secrets, run one command from a clean machine, and obtain the same validated
business result without manual database edits or undocumented commands.

---

## Post-launch backlog

These items do not block the local release:

- DataCite connector;
- Europe PMC connector;
- additional publisher APIs;
- Kubernetes;
- high availability;
- production domain and TLS automation;
- per-source systemd units;
- full-text research-route analysis;
- user accounts, saved libraries, alerts, and collaborative features.

They must not be pulled into the active plan unless a listed release gate cannot pass without them.
