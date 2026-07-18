# medpaperhub Three-Source Intelligence Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the legacy biomedical search/catalog truth with a three-domain, Crossref-first publication intelligence pipeline that publishes only papers with verified official URLs and separates real-time visibility from analysis readiness.

**Architecture:** Preserve the decoupled Go API/Worker/Migrate, Nuxt Web, PostgreSQL, and immutable Catalog boundaries. Add durable connector watermarks, Crossref created/update hot ingestion, verified URL assertions, `publicly_visible` and `analysis_ready` states, PubMed/OpenAlex asynchronous enrichment, and generation-bound APIs for daily publications, journal patterns, weekly/monthly trends, and technology signals.

**Tech Stack:** Go 1.26, PostgreSQL 18, Nuxt 4, Vue 3, TypeScript, OpenAPI, Vitest, Playwright, Docker Compose, GitHub Actions.

---

## Execution rules

- Work in `/Users/huangrende/Documents/论文自媒体/论文聚合门户/.worktrees/go-nuxt-replatform`.
- Use TDD for every behavior change.
- Do not reuse `indexed` as a Crossref discovery or revision timestamp.
- Do not publish a Work without a persisted, active, verified official URL assertion.
- Do not require PubMed, OpenAlex, citation, trend, journal, or opportunity analysis to publish real-time facts.
- Do not let a `publicly_visible` Work enter analysis until it independently satisfies `analysis_ready`.
- Do not infer missing JCR, URL, publication state, domain, classification, or download metrics.
- Keep source facts and model/analysis assertions separate and versioned.
- Run `git diff --check` before every commit.

## Delivery order

The critical path is:

```text
JCR Registry v2
→ durable watermarks
→ Crossref hot discovery
→ verified URL gate
→ public/analysis state split
→ API and Web daily feed
→ PubMed/OpenAlex enrichment
→ classification and analysis products
→ production scheduling and replay verification
```

---

### Task 1: Add JCR Registry v2 and remove the legacy `Q1 OR JIF >= 10` rule

**Files:**
- Create: `services/core/migrations/000019_jcr_registry_v2.sql`
- Modify: `services/core/internal/venue/model.go`
- Modify: `services/core/internal/venue/jcr_import.go`
- Modify: `services/core/internal/venue/postgres_store.go`
- Modify: `services/core/internal/venue/policy.go`
- Modify: `services/core/internal/venue/assessment_service.go`
- Modify: `services/core/internal/catalog/publisher.go`
- Modify: `services/core/cmd/worker/assessment_command_test.go`
- Modify: `services/core/internal/venue/jcr_import_test.go`
- Modify: `services/core/internal/venue/policy_test.go`
- Modify: `services/core/internal/venue/postgres_store_test.go`
- Modify: `services/core/internal/database/migrate_test.go`
- Modify: `data/venues/README.md`
- Modify: `data/venues/jcr-q1.example.csv`

**Step 1: Write failing migration and domain tests**

Add tests requiring these fields on every known JCR category metric:

```go
type CategoryMetric struct {
    EditionYear         int
    MetricYear          int
    Category            string
    Quartile            Quartile
    JIF                 decimal.Decimal
    JIFRank             int
    CategoryJournalCount int
    JIFPercentile       decimal.Decimal
}
```

Test the policy boundary:

```go
func TestUpperHalfQ1Policy(t *testing.T) {
    tests := []struct {
        name       string
        quartile   Quartile
        percentile string
        want       Decision
    }{
        {"boundary accepted", QuartileQ1, "87.5", Accepted},
        {"below boundary rejected", QuartileQ1, "87.49", Rejected},
        {"q2 rejected", QuartileQ2, "99", Rejected},
    }
    // ...
}
```

Also test rank fallback with `CategoryJournalCount=101`: rank 13 accepted, rank 14 rejected.

**Step 2: Run focused tests and confirm failure**

Run:

```bash
go -C services/core test ./internal/venue ./internal/database \
  -run 'TestUpperHalfQ1Policy|TestJCRRegistryV2|TestMigration' -count=1
```

Expected: FAIL because the schema and policy fields do not exist.

**Step 3: Implement migration, importer, and policy**

The migration must:

- preserve prior immutable receipts;
- add edition/rank/count/percentile to the metric assertion model;
- reject invalid known metrics;
- prevent percentile outside `[0, 100]`;
- prevent rank greater than category journal count;
- preserve exact decimal percentile values;
- add policy version `journal-upper-half-q1/v2`.

Use percentile when present. Use rank/count only when the authorized row explicitly lacks percentile.

**Step 4: Update Catalog evidence**

Every accepted Venue evidence block must include:

```json
{
  "edition_year": 2026,
  "metric_year": 2025,
  "category": "Oncology",
  "quartile": "Q1",
  "jif_rank": 12,
  "category_journal_count": 120,
  "jif_percentile": "90.0",
  "policy_version": "journal-upper-half-q1/v2",
  "receipt_id": "..."
}
```

**Step 5: Run all Venue and migration tests**

Run:

```bash
go -C services/core test ./internal/venue ./internal/database ./internal/catalog -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations/000019_jcr_registry_v2.sql \
  services/core/internal/venue services/core/internal/catalog/publisher.go \
  services/core/cmd/worker/assessment_command_test.go \
  services/core/internal/database/migrate_test.go data/venues
git commit -m "feat(venue): add upper-half Q1 registry"
```

---

### Task 2: Replace the biomedical-only Subject registry with three reviewed domains

**Files:**
- Create: `data/subjects/research-domains-jcr-subjects.v2.csv`
- Create: `services/core/internal/scope/domain.go`
- Create: `services/core/internal/scope/domain_test.go`
- Create: `services/core/internal/scope/postgres_domain.go`
- Create: `services/core/internal/scope/postgres_domain_test.go`
- Create: `services/core/migrations/000020_research_domains.sql`
- Modify: `services/core/internal/biomed/taxonomy.go`
- Modify: `services/core/internal/biomed/taxonomy_test.go`
- Modify: `services/core/internal/biomed/scope.go`
- Modify: `services/core/internal/biomed/postgres_scope.go`
- Modify: `services/core/cmd/worker/biomedical_eligibility_command_test.go`
- Modify: `data/subjects/README.md`

**Step 1: Write failing exact-registry tests**

Test:

- the only top-level domains are `medicine`, `biology`, `computer-science`;
- a JCR Category is linked by exact authorized source value;
- one Category can map to more than one domain only when the CSV explicitly records both rows;
- unknown or spelling-normalized categories are rejected;
- a multidisciplinary journal does not enter a domain trend without article-level assignment.

**Step 2: Run focused tests**

```bash
go -C services/core test ./internal/scope ./internal/biomed ./cmd/worker \
  -run 'TestResearchDomain|TestExactDomainRegistry|TestEligibility' -count=1
```

Expected: FAIL because `internal/scope` and registry v2 do not exist.

**Step 3: Implement the domain registry**

Persist:

```text
domain_versions
research_domains
domain_category_rules
journal_domain_metrics
```

Do not infer a domain from title, abstract, or approximate Category text.

**Step 4: Preserve compatibility only at internal boundaries**

Keep old biomedical tables readable for migration/replay, but stop exposing “biomedical” as the public top-level scope. New Catalog generations must bind the v2 domain version.

**Step 5: Run scope and Catalog tests**

```bash
go -C services/core test ./internal/scope ./internal/biomed ./internal/catalog ./cmd/worker -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add data/subjects services/core/migrations/000020_research_domains.sql \
  services/core/internal/scope services/core/internal/biomed \
  services/core/cmd/worker/biomedical_eligibility_command_test.go
git commit -m "feat(scope): add medicine biology and computer science domains"
```

---

### Task 3: Implement durable connector runs and atomic watermarks

**Files:**
- Create: `services/core/migrations/000021_connector_watermarks.sql`
- Create: `services/core/internal/ingestion/checkpoint.go`
- Create: `services/core/internal/ingestion/checkpoint_test.go`
- Create: `services/core/internal/ingestion/postgres_checkpoint.go`
- Create: `services/core/internal/ingestion/postgres_checkpoint_test.go`
- Modify: `services/core/internal/ingestion/repository.go`
- Modify: `services/core/internal/ingestion/postgres_repository.go`
- Modify: `services/core/internal/ingestion/service.go`
- Modify: `services/core/internal/ingestion/service_test.go`
- Modify: `services/core/internal/database/migrate_test.go`

**Step 1: Write failing state-machine tests**

Define:

```go
type StreamKey struct {
    Source string
    Stream string
}

type Watermark struct {
    SourceTime  time.Time
    TieBreakKey string
    Revision    int64
}
```

Test:

- `created` and `updated` streams advance independently;
- a run can transition `planned -> running -> succeeded|failed`;
- only `succeeded` can CAS-advance the committed watermark;
- HTTP, parse, pagination, persistence, or context failure leaves the committed watermark unchanged;
- immutable source records written before failure remain reusable;
- restart from the previous watermark converges without duplicate Works.

**Step 2: Run tests and confirm failure**

```bash
go -C services/core test ./internal/ingestion ./internal/database \
  -run 'TestConnectorRun|TestWatermark|TestCheckpoint' -count=1
```

Expected: FAIL.

**Step 3: Implement schema and PostgreSQL store**

Persist:

```text
connector_runs
connector_run_windows
connector_watermarks
connector_run_failures
```

The successful run transition and watermark CAS must occur in one transaction.

**Step 4: Add deterministic overlap support**

Store the committed watermark exactly. Compute overlap as an explicit command parameter and save it in the run record. Do not mutate the committed watermark backward.

**Step 5: Run ingestion tests**

```bash
go -C services/core test ./internal/ingestion ./internal/database -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations/000021_connector_watermarks.sql \
  services/core/internal/ingestion services/core/internal/database/migrate_test.go
git commit -m "feat(ingestion): add durable connector watermarks"
```

---

### Task 4: Convert Crossref to created/update hot discovery

**Files:**
- Modify: `services/core/internal/source/crossref/client.go`
- Modify: `services/core/internal/source/crossref/client_test.go`
- Modify: `services/core/internal/source/crossref/json.go`
- Modify: `services/core/internal/source/crossref/parse.go`
- Modify: `services/core/internal/source/crossref/parse_test.go`
- Modify: `services/core/internal/source/source.go`
- Modify: `services/core/internal/source/source_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Modify: `services/core/cmd/worker/main_test.go`
- Create: `services/core/cmd/worker/crossref_incremental_command_test.go`
- Modify: `services/core/internal/ingestion/postgres_repository.go`
- Modify: `services/core/internal/ingestion/postgres_repository_test.go`

**Step 1: Write failing Crossref query tests**

Replace:

```go
IndexedDateWindow DateWindow
```

with:

```go
type Stream string

const (
    StreamCreated Stream = "created"
    StreamUpdated Stream = "updated"
)

type Query struct {
    Stream     Stream
    DateWindow DateWindow
    DOIs       []string
    ISSNs      []string
    MaxResults int
}
```

Require generated filters:

```text
from-created-date / until-created-date
from-update-date / until-update-date
```

Add a regression fixture where only `indexed` changes on a 2019 DOI.

**Step 2: Run focused tests**

```bash
go -C services/core test ./internal/source/crossref ./cmd/worker \
  -run 'TestCreatedStream|TestUpdatedStream|TestIndexedDoesNotCreateRevision' -count=1
```

Expected: FAIL.

**Step 3: Preserve correct Crossref timestamps**

Persist separate source facts:

```text
crossref_created_at
crossref_deposited_at
crossref_indexed_at
official_online_date
official_print_date
official_accepted_date
first_observed_at
```

`indexed` must never populate generic `UpdatedAt` or determine “today newly discovered”.

**Step 4: Add an incremental Worker command**

Implement:

```bash
paper-hub-worker sync crossref \
  --stream created \
  --overlap 10m \
  --max-results 100000
```

and the equivalent `--stream updated`.

The command must drain every cursor page before committing the watermark.

**Step 5: Add failure and replay tests**

Test page N failure, parser failure, repository failure, and process cancellation. Confirm the watermark stays fixed and a subsequent run yields the same final source revision as a no-failure run.

Run:

```bash
go -C services/core test ./internal/source/crossref ./internal/ingestion ./cmd/worker -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/internal/source services/core/internal/ingestion \
  services/core/cmd/worker
git commit -m "feat(crossref): add created and update discovery streams"
```

---

### Task 5: Add immutable URL assertions and the official URL verifier

**Files:**
- Create: `services/core/migrations/000022_work_url_assertions.sql`
- Create: `services/core/internal/urlverify/model.go`
- Create: `services/core/internal/urlverify/model_test.go`
- Create: `services/core/internal/urlverify/verifier.go`
- Create: `services/core/internal/urlverify/verifier_test.go`
- Create: `services/core/internal/urlverify/postgres_store.go`
- Create: `services/core/internal/urlverify/postgres_store_test.go`
- Modify: `services/core/internal/source/source.go`
- Modify: `services/core/internal/source/crossref/parse.go`
- Modify: `services/core/internal/source/pubmed/parse.go`
- Modify: `services/core/internal/source/openalex/parse.go`
- Modify: `services/core/internal/config/config.go`
- Modify: `services/core/internal/config/config_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Create: `services/core/cmd/worker/url_verification_command_test.go`

**Step 1: Write failing URL model tests**

Model:

```go
type Assertion struct {
    WorkID             uuid.UUID
    SourceRecordID     uuid.UUID
    Kind               string
    CandidateURL       string
    FinalURL           string
    RedirectChain      []Redirect
    HTTPStatus         int
    IdentifierKind     string
    IdentifierValue    string
    IdentifierMatched  bool
    State              string
    VerifiedAt         time.Time
    VerifierVersion    string
}
```

Allowed public state is only `verified`. Test rejection of:

- non-HTTPS final URL;
- redirect loop;
- DOI mismatch;
- missing stable article identifier;
- guessed URL with no source path;
- expired assertion under an explicit URL policy.

**Step 2: Run focused tests**

```bash
go -C services/core test ./internal/urlverify ./internal/config ./cmd/worker \
  -run 'TestURL|TestVerifier' -count=1
```

Expected: FAIL.

**Step 3: Implement deterministic verification**

The verifier must:

- use the existing hardened HTTP client policy;
- follow a bounded redirect chain;
- save every hop;
- accept only HTTP(S) source candidates;
- inspect structured DOI/article identifiers;
- save response metadata and verifier version;
- never generate a candidate URL from title, journal, volume, issue, pages, or DOI patterns.

**Step 4: Persist assertions and add Worker command**

Implement:

```bash
paper-hub-worker verify urls \
  --policy-version official-url/v1 \
  --limit 500
```

Repeated verification writes a new immutable assertion and updates a current projection; it does not edit history.

**Step 5: Run source, URL, and integration tests**

```bash
go -C services/core test ./internal/urlverify ./internal/source/... ./cmd/worker -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations/000022_work_url_assertions.sql \
  services/core/internal/urlverify services/core/internal/source \
  services/core/internal/config services/core/cmd/worker
git commit -m "feat(url): verify official article links"
```

---

### Task 6: Split real-time public visibility from analysis readiness

**Files:**
- Create: `services/core/migrations/000023_visibility_readiness.sql`
- Create: `services/core/internal/catalog/visibility.go`
- Create: `services/core/internal/catalog/visibility_test.go`
- Modify: `services/core/internal/catalog/model.go`
- Modify: `services/core/internal/catalog/publisher.go`
- Modify: `services/core/internal/catalog/curation_gate_test.go`
- Modify: `services/core/internal/catalog/publisher_integration_test.go`
- Modify: `services/core/internal/catalog/biomedical_snapshot.go`
- Modify: `services/core/internal/catalog/biomedical_snapshot_test.go`
- Modify: `services/core/cmd/worker/catalog_curation_command_test.go`

**Step 1: Write failing visibility tests**

Define:

```go
type VisibilityState struct {
    WorkID          uuid.UUID
    PubliclyVisible bool
    AnalysisReady   bool
    AnalysisCutoff  *time.Time
    Reasons         []string
    PolicyVersion   string
}
```

Test:

- eligible Venue + stable identity + active verified URL + active lifecycle is publicly visible;
- missing PubMed/OpenAlex does not block public visibility;
- missing required classification or source conflict blocks analysis readiness;
- cold analysis failure does not revoke an otherwise valid public fact;
- retraction/withdrawal revokes public visibility in the next generation.

**Step 2: Run focused tests**

```bash
go -C services/core test ./internal/catalog \
  -run 'TestPubliclyVisible|TestAnalysisReady|TestFactCatalogWithoutAnalysis' -count=1
```

Expected: FAIL.

**Step 3: Change `PublishInput`**

Replace mandatory analysis run IDs with an explicit mode:

```go
type PublishMode string

const (
    PublishFacts    PublishMode = "facts"
    PublishAnalysis PublishMode = "analysis"
)
```

Facts mode must not require citation, trend, journal, or opportunity runs. Analysis mode must bind cutoff, taxonomy/classifier versions, and exact analysis runs.

**Step 4: Add the URL gate inside the serializable publish transaction**

The final query that selects public Works must require a current verified URL assertion. Frontend filtering is not acceptable.

**Step 5: Run Catalog tests**

```bash
go -C services/core test ./internal/catalog ./cmd/worker -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations/000023_visibility_readiness.sql \
  services/core/internal/catalog services/core/cmd/worker
git commit -m "feat(catalog): separate public facts from analysis readiness"
```

---

### Task 7: Publish the new URL, discovery-time, and readiness API contract

**Files:**
- Modify: `contracts/openapi.yaml`
- Modify: `services/core/internal/catalog/home_contract.go`
- Modify: `services/core/internal/catalog/home_contract_test.go`
- Modify: `services/core/internal/catalog/home.go`
- Modify: `services/core/internal/catalog/papers.go`
- Modify: `services/core/internal/catalog/journals.go`
- Modify: `services/core/internal/catalog/repository.go`
- Modify: `services/core/internal/httpapi/catalog.go`
- Modify: `services/core/internal/httpapi/catalog_integration_test.go`
- Modify: `services/core/internal/httpapi/catalog_postgres_test.go`
- Regenerate: `apps/web/app/types/openapi.generated.ts`
- Modify: `apps/web/tests/openapi.test.ts`

**Step 1: Write failing OpenAPI contract tests**

Add required:

```yaml
OfficialLink:
  type: object
  required: [url, kind, verified_at, verifier_version]

PaperSummary:
  required:
    - official_link
    - first_observed_at
    - publicly_visible
    - analysis_ready
```

Add `today_newly_discovered` to `HomeResponse`.

Model analysis collections as a discriminated union:

```yaml
oneOf:
  - required: [state, analysis, items]
    properties:
      state: { const: analysis_ready }
  - required: [state, reason]
    properties:
      state: { const: analysis_not_ready }
```

**Step 2: Run the contract test**

```bash
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
```

Expected: FAIL.

**Step 3: Implement API payloads**

Facts must expose:

- official publication date when known;
- `first_observed_at`;
- `url_verified_at`;
- official link;
- source-specific provenance;
- public/analysis states.

Do not label `first_observed_at` as publication time.

**Step 4: Regenerate types**

```bash
pnpm --dir apps/web generate:api-types
pnpm --dir apps/web check:api-types
```

Expected: generated file changes once, then check passes.

**Step 5: Run Go and OpenAPI tests**

```bash
go -C services/core test ./internal/catalog ./internal/httpapi -count=1
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
```

Expected: PASS.

**Step 6: Commit**

```bash
git add contracts/openapi.yaml services/core/internal/catalog \
  services/core/internal/httpapi apps/web/app/types/openapi.generated.ts \
  apps/web/tests/openapi.test.ts
git commit -m "feat(api): expose verified links and readiness"
```

---

### Task 8: Wire PubMed hourly enrichment and Daily Update catch-up

**Files:**
- Create: `services/core/internal/source/pubmed/postgres_bulk.go`
- Create: `services/core/internal/source/pubmed/postgres_bulk_test.go`
- Modify: `services/core/internal/source/pubmed/bulk.go`
- Modify: `services/core/internal/source/pubmed/bulk_test.go`
- Modify: `services/core/internal/source/pubmed/client.go`
- Modify: `services/core/internal/source/pubmed/client_test.go`
- Modify: `services/core/internal/source/pubmed/query.go`
- Modify: `services/core/internal/source/pubmed/query_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Create: `services/core/cmd/worker/pubmed_incremental_command_test.go`
- Create: `services/core/cmd/worker/pubmed_bulk_command_test.go`
- Modify: `services/core/internal/config/config.go`
- Modify: `services/core/internal/config/config_test.go`

**Step 1: Write failing command and checkpoint tests**

Require:

```bash
paper-hub-worker sync pubmed --stream hourly --overlap 2h
paper-hub-worker import pubmed-daily --file /imports/pubmed/update.xml.gz
```

Test ESearch, EFetch, XML parse, database, and delete-event failures. No failure can advance the hourly or file checkpoint.

**Step 2: Run focused tests**

```bash
go -C services/core test ./internal/source/pubmed ./cmd/worker \
  -run 'TestPubMedHourly|TestPubMedDaily|TestPubMedCheckpoint' -count=1
```

Expected: FAIL.

**Step 3: Implement PostgreSQL Bulk Sink and Checkpoint**

Connect the existing bulk parser to the immutable ingestion repository. File SHA256, filename, sequence, source date, and delete events must be persisted.

**Step 4: Implement hourly EDAT ingestion**

Use a source window and local exact ISSN/domain filtering. Keep accepted/ahead-of-print/epublish/ppublish assertions tied to the same PubMed source record.

**Step 5: Run PubMed tests**

```bash
go -C services/core test ./internal/source/pubmed ./internal/ingestion ./cmd/worker -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/internal/source/pubmed services/core/internal/config \
  services/core/cmd/worker
git commit -m "feat(pubmed): add hourly and daily enrichment"
```

---

### Task 9: Make OpenAlex a DOI-driven enrichment pipeline

**Files:**
- Create: `services/core/migrations/000024_enrichment_outbox.sql`
- Create: `services/core/internal/enrichment/outbox.go`
- Create: `services/core/internal/enrichment/outbox_test.go`
- Create: `services/core/internal/enrichment/postgres_outbox.go`
- Create: `services/core/internal/enrichment/postgres_outbox_test.go`
- Modify: `services/core/internal/source/openalex/client.go`
- Modify: `services/core/internal/source/openalex/client_test.go`
- Modify: `services/core/internal/source/openalex/parse.go`
- Modify: `services/core/internal/source/openalex/parse_test.go`
- Modify: `services/core/internal/ingestion/postgres_repository.go`
- Modify: `services/core/cmd/worker/main.go`
- Create: `services/core/cmd/worker/openalex_enrichment_command_test.go`

**Step 1: Write failing outbox tests**

Test:

- a newly committed DOI creates one `openalex_work_enrich` event;
- repeated Crossref/PubMed projections do not duplicate the event;
- OpenAlex cannot create the first public Work;
- retry and dead-letter state are persisted;
- repeated successful consumption produces identical source assertions;
- citation snapshots retain `source=openalex` and `observed_at`.

**Step 2: Run focused tests**

```bash
go -C services/core test ./internal/enrichment ./internal/source/openalex ./cmd/worker \
  -run 'TestOpenAlexEnrichment|TestOutbox' -count=1
```

Expected: FAIL.

**Step 3: Implement transactional outbox**

Create the outbox event in the same transaction that first commits a stable DOI projection.

**Step 4: Restrict the Worker path**

Implement:

```bash
paper-hub-worker enrich openalex --limit 500
paper-hub-worker snapshot citations --source openalex --observed-at ...
```

Do not use broad OpenAlex queries as the real-time discovery source.

**Step 5: Run tests**

```bash
go -C services/core test ./internal/enrichment ./internal/source/openalex \
  ./internal/ingestion ./cmd/worker -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations/000024_enrichment_outbox.sql \
  services/core/internal/enrichment services/core/internal/source/openalex \
  services/core/internal/ingestion services/core/cmd/worker
git commit -m "feat(openalex): add DOI-driven enrichment"
```

---

### Task 10: Implement versioned five-axis classification and article-level domains

**Files:**
- Create: `services/core/migrations/000025_work_classifications.sql`
- Create: `services/core/internal/classification/model.go`
- Create: `services/core/internal/classification/model_test.go`
- Create: `services/core/internal/classification/taxonomy.go`
- Create: `services/core/internal/classification/taxonomy_test.go`
- Create: `services/core/internal/classification/source_rules.go`
- Create: `services/core/internal/classification/source_rules_test.go`
- Create: `services/core/internal/classification/postgres_store.go`
- Create: `services/core/internal/classification/postgres_store_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Create: `services/core/cmd/worker/classification_command_test.go`
- Create: `data/taxonomy/content-types.v1.csv`
- Create: `data/taxonomy/research-modes.v1.csv`
- Create: `data/taxonomy/research-purposes.v1.csv`
- Create: `data/taxonomy/study-designs.v1.csv`
- Create: `data/taxonomy/techniques.v1.csv`

**Step 1: Write failing taxonomy tests**

Require five independent axes:

```text
content_type
research_mode
research_purpose
study_design
technique
```

Every assertion must save taxonomy version, classifier version, evidence source/path, confidence, and classification time.

**Step 2: Write source-fact mapping tests**

First implementation uses only exact, reviewed source mappings:

- PubMed Publication Type and MeSH;
- explicit publisher/Crossref structured type;
- OpenAlex topic/domain assertions as OpenAlex-sourced evidence.

Unknown remains `insufficient_evidence`. Do not add title-keyword heuristics.

**Step 3: Run focused tests**

```bash
go -C services/core test ./internal/classification ./cmd/worker \
  -run 'TestTaxonomy|TestSourceClassification|TestArticleDomain' -count=1
```

Expected: FAIL.

**Step 4: Implement persistence and Worker command**

```bash
paper-hub-worker classify works \
  --taxonomy-version research-taxonomy/v1 \
  --classifier-version structured-source-mapping/v1 \
  --limit 1000
```

Multidisciplinary journal articles require an article-level domain assertion before entering domain analysis.

**Step 5: Run tests**

```bash
go -C services/core test ./internal/classification ./internal/catalog ./cmd/worker -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations/000025_work_classifications.sql \
  services/core/internal/classification services/core/cmd/worker data/taxonomy
git commit -m "feat(classification): add versioned research taxonomy"
```

---

### Task 11: Build weekly/monthly trends, 12/24-month journal patterns, and technology signals

**Files:**
- Create: `services/core/internal/analysis/technologies.go`
- Create: `services/core/internal/analysis/technologies_test.go`
- Create: `services/core/internal/analysis/postgres_technologies.go`
- Create: `services/core/internal/analysis/postgres_technologies_test.go`
- Create: `services/core/migrations/000026_technology_analysis.sql`
- Modify: `services/core/internal/analysis/trends.go`
- Modify: `services/core/internal/analysis/trends_test.go`
- Modify: `services/core/internal/analysis/journals.go`
- Modify: `services/core/internal/analysis/journals_test.go`
- Modify: `services/core/internal/analysis/postgres_trends.go`
- Modify: `services/core/internal/analysis/postgres_journals.go`
- Modify: `services/core/internal/catalog/analysis.go`
- Modify: `services/core/internal/catalog/biomedical_analysis.go`
- Modify: `services/core/cmd/worker/main.go`
- Modify: `services/core/cmd/worker/biomedical_analysis_command_test.go`
- Modify: `contracts/openapi.yaml`

**Step 1: Write failing cohort-boundary tests**

Trends must support only:

```text
period=week
period=month
```

Journal patterns must support:

```text
window_months=12
window_months=24
```

Every cohort must require `analysis_ready=true` and `published_at <= analysis_cutoff`.

**Step 2: Write technology-axis tests**

Persist and expose separate:

```text
Novelty
Momentum
Diffusion
Maturity
```

No combined hidden score. Insufficient abstract/structured evidence yields `insufficient_evidence` for Novelty and Maturity.

**Step 3: Run focused tests**

```bash
go -C services/core test ./internal/analysis ./internal/catalog ./cmd/worker \
  -run 'TestWeeklyMonthly|TestJournalWindow|TestTechnology' -count=1
```

Expected: FAIL.

**Step 4: Implement analysis snapshots**

Keep existing Poisson/Negative Binomial, confidence interval, coverage, effect-size, and FDR primitives. Add explicit period/window/cutoff/taxonomy/classifier fields to each snapshot.

**Step 5: Update OpenAPI and run tests**

```bash
go -C services/core test ./internal/analysis ./internal/catalog ./cmd/worker -count=1
pnpm --dir apps/web generate:api-types
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations/000026_technology_analysis.sql \
  services/core/internal/analysis services/core/internal/catalog \
  services/core/cmd/worker contracts/openapi.yaml \
  apps/web/app/types/openapi.generated.ts
git commit -m "feat(analysis): add trend journal and technology windows"
```

---

### Task 12: Rebuild the Nuxt experience around daily intelligence

**Required skills before implementation:** `@ui-ux-pro-max`; use `@imagegen` only if a raster visual asset is actually required.

**Files:**
- Modify: `design-system/paper-research-hub/MASTER.md`
- Modify: `design-system/paper-research-hub/pages/home.md`
- Modify: `design-system/paper-research-hub/pages/papers.md`
- Modify: `design-system/paper-research-hub/pages/trends.md`
- Create: `design-system/paper-research-hub/pages/journals.md`
- Create: `design-system/paper-research-hub/pages/technologies.md`
- Modify: `apps/web/app/pages/index.vue`
- Modify: `apps/web/app/pages/papers/[id].vue`
- Modify: `apps/web/app/pages/journals/[slug].vue`
- Modify: `apps/web/app/pages/trends.vue`
- Create: `apps/web/app/pages/technologies/index.vue`
- Create: `apps/web/app/pages/technologies/[slug].vue`
- Modify: `apps/web/app/components/AppHeader.vue`
- Modify: `apps/web/app/components/AppFooter.vue`
- Modify: `apps/web/app/components/PaperCard.vue`
- Modify: `apps/web/app/components/CatalogPaperCard.vue`
- Modify: `apps/web/app/components/PublicationEventCard.vue`
- Modify: `apps/web/app/components/PublicationUpdateList.vue`
- Modify: `apps/web/app/components/EditorialPatternTable.vue`
- Create: `apps/web/app/components/AnalysisReadiness.vue`
- Create: `apps/web/app/components/TechnologySignalGrid.vue`
- Modify: `apps/web/app/utils/catalogApi.ts`
- Modify: `apps/web/app/utils/catalogPresentation.ts`
- Modify: `apps/web/tests/components.test.ts`
- Modify: `apps/web/tests/home.test.ts`
- Modify: `apps/web/tests/biomedical-home.test.ts`
- Modify: `apps/web/tests/biomedical-trends.test.ts`
- Modify: `apps/web/tests/journals.test.ts`
- Modify: `apps/web/tests/paper-detail.test.ts`
- Modify: `apps/web/tests/public-pages.contract.test.ts`
- Modify: `apps/web/e2e/mock-catalog-server.ts`
- Modify: `apps/web/e2e/replatform.spec.ts`

**Step 1: Write failing component and page tests**

Require:

- homepage sections “今日正式发表” and “今日新发现”;
- official external link on every paper card and detail page;
- no home hero search box;
- analysis modules render only for `analysis_ready`;
- `period=week|month` is shareable and restored on navigation;
- `window_months=12|24` is shareable;
- technologies display four independent axes;
- “公开目录尚未发布” replaces “等待首次同步” for 503 Catalog state.

**Step 2: Run focused Web tests**

```bash
pnpm --dir apps/web exec vitest run \
  tests/components.test.ts tests/home.test.ts tests/biomedical-home.test.ts \
  tests/biomedical-trends.test.ts tests/journals.test.ts \
  tests/paper-detail.test.ts tests/public-pages.contract.test.ts \
  --config vitest.config.ts
```

Expected: FAIL.

**Step 3: Update the design system first**

Freeze:

- search as header utility;
- concise card-first layout;
- official URL as a primary action;
- visible separation of publication date and first-observed time;
- no promotional explanation blocks;
- no fake zero-state charts;
- readiness state instead of partial analysis.

**Step 4: Implement pages and components**

External links must use the persisted API URL. If opening a new window, use:

```html
rel="noopener noreferrer"
```

The Web must not build DOI or publisher URLs.

**Step 5: Run Web verification**

```bash
pnpm --dir apps/web lint
pnpm --dir apps/web typecheck
pnpm --dir apps/web test
pnpm --dir apps/web build
pnpm --dir apps/web test:e2e
```

Expected: PASS.

**Step 6: Commit**

```bash
git add design-system/paper-research-hub apps/web
git commit -m "feat(web): focus medpaperhub on daily intelligence"
```

---

### Task 13: Add production-neutral scheduling, health, metrics, and runbooks

**Files:**
- Create: `deploy/systemd/medpaperhub-crossref-created.service`
- Create: `deploy/systemd/medpaperhub-crossref-created.timer`
- Create: `deploy/systemd/medpaperhub-crossref-updated.service`
- Create: `deploy/systemd/medpaperhub-crossref-updated.timer`
- Create: `deploy/systemd/medpaperhub-pubmed-hourly.service`
- Create: `deploy/systemd/medpaperhub-pubmed-hourly.timer`
- Create: `deploy/systemd/medpaperhub-pubmed-daily.service`
- Create: `deploy/systemd/medpaperhub-pubmed-daily.timer`
- Create: `deploy/systemd/medpaperhub-openalex-enrich.service`
- Create: `deploy/systemd/medpaperhub-openalex-enrich.timer`
- Create: `deploy/systemd/medpaperhub-catalog-facts.service`
- Create: `deploy/systemd/medpaperhub-catalog-facts.timer`
- Create: `deploy/systemd/medpaperhub-analysis.service`
- Create: `deploy/systemd/medpaperhub-analysis.timer`
- Create: `docs/deployment/three-source-operations.md`
- Create: `docs/deployment/restore-replay.md`
- Modify: `services/core/internal/httpapi/server.go`
- Modify: `services/core/internal/httpapi/server_test.go`
- Create: `services/core/internal/observability/metrics.go`
- Create: `services/core/internal/observability/metrics_test.go`
- Modify: `docker-compose.yml`
- Modify: `docker-compose.test.yml`
- Modify: `.env.example`
- Modify: `docs/deployment/verify-local.sh`
- Modify: `.github/workflows/ci.yml`

**Step 1: Write failing health and metrics tests**

Require:

```text
GET /live
GET /ready
GET /metrics
```

`/ready` must check database connectivity, migration compatibility, and current facts Catalog state. Metrics must include:

```text
connector_watermark_age_seconds
connector_run_duration_seconds
connector_failures_total
source_lag_seconds
url_verification_lag_seconds
catalog_generation_age_seconds
analysis_ready_ratio
```

**Step 2: Run focused tests**

```bash
go -C services/core test ./internal/httpapi ./internal/observability \
  -run 'TestLive|TestReady|TestMetrics' -count=1
```

Expected: FAIL.

**Step 3: Implement endpoints and metrics**

Do not include credentials, raw URLs with tokens, or source payloads in metrics.

**Step 4: Add default single-VPS timers**

Default cadence:

```text
Crossref created          every 2 minutes
Crossref updated          every 2 minutes
facts Catalog             every 5 minutes
PubMed hourly             every 1 hour
OpenAlex enrichment       every 6 hours
analysis snapshots        daily
PubMed Daily Update       after file arrival
```

Timer services must call one-shot Worker commands. The Worker remains scheduler-neutral and can later be run by Kubernetes CronJobs without code changes.

**Step 5: Verify deployment configuration**

```bash
docker compose config --quiet
docker compose -f docker-compose.yml -f docker-compose.test.yml config --quiet
bash docs/deployment/verify-local.sh
```

Expected: PASS.

**Step 6: Commit**

```bash
git add deploy/systemd docs/deployment services/core/internal/httpapi \
  services/core/internal/observability docker-compose.yml docker-compose.test.yml \
  .env.example .github/workflows/ci.yml
git commit -m "ops: schedule and observe three-source pipeline"
```

---

### Task 14: Remove stale product truth and prove clean replay end to end

**Files:**
- Modify: `README.md`
- Modify: `docs/deployment/biomedical-pipeline.md`
- Modify: `docs/deployment/service-boundaries.md`
- Modify: `apps/web/app/components/SyncStatus.vue`
- Modify: `apps/web/app/components/AppFooter.vue`
- Modify: `apps/web/app/pages/journals/index.vue`
- Modify: all tests that assert the legacy domain or Venue policy
- Create: `docs/deployment/verify-three-source-demo.sh`
- Create: `services/core/internal/catalog/three_source_replay_integration_test.go`
- Modify: `Makefile`

**Step 1: Add a failing legacy-truth guard**

Create a test/script that rejects public documentation and UI strings containing:

```text
Q1 OR JIF >= 10
Q1 或 JIF >= 10
仅医学与生物学
等待首次同步
```

Allow historical design documents only through an explicit archived-path allowlist.

**Step 2: Add the end-to-end replay test**

Test:

```text
empty database
→ migrate
→ import authorized-format JCR v2 fixture
→ import three-domain registry
→ Crossref created/update fixtures
→ verify official URL fixture
→ publish facts Catalog
→ assert Home contains today's formal/newly discovered items
→ PubMed and OpenAlex enrichment
→ classification
→ analysis-ready snapshot
→ publish analysis Catalog
→ assert trends/journal/technology products
→ replay all inputs
→ assert identical source revision and no duplicate Work
```

**Step 3: Run the replay test and confirm initial failure**

```bash
go -C services/core test ./internal/catalog \
  -run TestThreeSourceCleanReplay -count=1
```

Expected: FAIL until all prior tasks are integrated.

**Step 4: Update all operational documentation**

Document required external inputs:

- authorized JCR v2 CSV;
- `CROSSREF_CONTACT_EMAIL`;
- `NCBI_TOOL`, `NCBI_EMAIL`, optional `NCBI_API_KEY`;
- `OPENALEX_API_KEY`;
- PostgreSQL backup/restore path;
- production Web/API domains and CORS allowlist.

Do not commit credentials or an unauthorized JCR export.

**Step 5: Run complete verification**

```bash
git diff --check
gofmt -w $(find services/core -type f -name '*.go')
go -C services/core vet ./...
go -C services/core test ./...
go -C services/core test -race ./...
pnpm --dir apps/web generate:api-types
pnpm --dir apps/web check:api-types
pnpm --dir apps/web lint
pnpm --dir apps/web typecheck
pnpm --dir apps/web test
pnpm --dir apps/web build
pnpm --dir apps/web test:e2e
docker compose config --quiet
docker compose -f docker-compose.yml -f docker-compose.test.yml config --quiet
bash docs/deployment/verify-three-source-demo.sh
git status --short
```

Expected:

- every command passes;
- no generated diff remains;
- replay is deterministic;
- every public paper has a verified official URL;
- facts can publish without analysis;
- only analysis-ready papers enter analytical products;
- working tree is clean.

**Step 6: Commit**

```bash
git add README.md docs apps/web services/core Makefile
git commit -m "docs: finalize three-source publication intelligence"
```

---

## Final acceptance matrix

| Product question | Required evidence | Pass condition |
|---|---|---|
| 今天发了什么？ | official publication assertion, first-observed time, verified URL | facts Catalog publishes within 5 minutes of Crossref visibility |
| 今天新发现了什么？ | `first_observed_at`, Crossref provenance | no use of Crossref `indexed` as discovery truth |
| 杂志近期发什么？ | analysis-ready 12/24-month cohort | six classification dimensions with support, coverage, effect size, CI and FDR |
| 周/月趋势是什么？ | week/month cohort and cutoff | paper/team/journal/diffusion/citation metrics use only analysis-ready Works |
| 什么技术在变热？ | versioned technique assertions | Novelty, Momentum, Diffusion and Maturity remain separate |
| 能否跳转原文？ | active URL verification assertion | 100% of public papers expose the persisted official URL |
| 抓取是否正确？ | immutable raw records, typed watermarks, deterministic replay | failure never advances watermarks and replay produces no duplicates |
| 前后端是否解耦？ | deployment boundaries | Nuxt has no DB/source credentials; Go API never runs ingestion; Worker remains one-shot |

## External inputs still required before production data can be published

These are operational inputs, not unresolved product logic:

1. A user-authorized JCR v2 export containing Category rank/count or JIF Percentile.
2. Production Web/API domains and the final deployment host.
3. Production PostgreSQL credentials and backup destination.
4. Crossref/NCBI contact configuration and an OpenAlex API key.

Synthetic fixtures remain test-only and must never publish a production Catalog.
