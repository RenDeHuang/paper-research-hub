# medpaperhub Biomedical Intelligence Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Convert the current generic paper portal into `medpaperhub`, a biomedical literature and journal intelligence product that publicly exposes only journal papers accepted by the versioned rule `JCR Q1 OR JIF >= 10`.

**Architecture:** Preserve the decoupled Nuxt Web, Go API/Worker/Migrate, and PostgreSQL architecture. Extend the immutable ingestion pipeline with biomedical semantics, source-specific citation snapshots, authorized JCR assessment, deterministic statistical analysis, and an `accepted` Catalog publication gate. Public pages continue to read immutable Catalog generations only.

**Tech Stack:** Go 1.26, PostgreSQL 18, pgx, `net/http`, Nuxt 4, Vue 3, TypeScript, OpenAPI 3.1, Vitest, Playwright, Docker Compose.

---

## Execution rules

- Use `@superpowers:test-driven-development` for every behavior change.
- Use `@systematic-debugging` for any failing test not explained by the new assertion.
- Use `@verification-before-completion` before every commit and before the final report.
- Do not publish production Catalog generations from synthetic JCR fixtures.
- Do not use title similarity to admit a Venue into the public Catalog.
- Do not substitute SJR, CiteScore, OpenAlex citedness, full-text availability, saves, or mentions for JIF or downloads.
- Keep `missing`, `unknown`, zero, rejected, and not-applicable states distinct.
- Commit after each task. Do not combine unrelated tasks.

## Phase 1: Lock contracts and biomedical semantics

### Task 1: Add API discovery and lock the medpaperhub public contract

**Files:**

- Modify: `contracts/openapi.yaml`
- Modify: `services/core/internal/httpapi/server.go`
- Modify: `services/core/internal/httpapi/server_test.go`
- Modify: `apps/web/server/routes/health.get.ts`
- Modify: `apps/web/server/routes/health.ts`
- Modify: `apps/web/tests/health.test.ts`
- Modify: `apps/web/tests/openapi.test.ts`
- Modify: `docker-compose.yml`
- Modify: `docker-compose.test.yml`
- Modify: `README.md`

**Step 1: Write failing Go tests for `GET /`**

Add a test that requires:

```json
{
  "service": "medpaperhub-api",
  "status": "ok",
  "api_version": "v1",
  "health": "/health"
}
```

The test must also require:

- `GET /` returns `200 application/json`;
- `POST /` returns the existing RFC 9457-style `405` problem;
- unknown `/not-a-route` remains `404`;
- no redirect to `localhost:3000`.
- API health identifies `medpaperhub-api`;
- Web health identifies `medpaperhub-web`.

**Step 2: Run the focused test and confirm RED**

Run:

```bash
go -C services/core test ./internal/httpapi -run 'TestRootDiscovery' -count=1
```

Expected: FAIL because `/` is not registered.

**Step 3: Implement the root discovery handler**

Register before the `/api` fallback:

```go
mux.HandleFunc("/", getOnly(discovery))
```

The handler must reject non-root paths explicitly:

```go
if request.URL.Path != "/" {
    http.NotFound(writer, request)
    return
}
```

Do not include a mutable frontend URL in the payload.

Keep Go module names, binary names, Docker image names, Compose project names, and database names unchanged in this task. They are internal infrastructure identities, not public brand copy.

**Step 4: Extend OpenAPI**

Add only the implemented `GET /` discovery operation in this task. Tasks 16 and 17 add biomedical resource paths and generated Nuxt types atomically with their running implementations. Do not publish contracts for routes that still return 404, and do not return placeholder payloads.

**Step 5: Verify**

Run:

```bash
go -C services/core test ./internal/httpapi -count=1
pnpm --dir apps/web exec vitest run tests/health.test.ts --config vitest.e2e.config.ts
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
docker compose config --quiet
git diff --check
```

Expected: PASS.

**Step 6: Commit**

```bash
git add contracts/openapi.yaml services/core/internal/httpapi apps/web/server apps/web/tests/health.test.ts apps/web/tests/openapi.test.ts docker-compose.yml docker-compose.test.yml README.md
git commit -m "feat(api): add medpaperhub discovery contract"
```

### Task 2: Add normalized MeSH, Publication Type, and Subject schema

**Files:**

- Create: `services/core/migrations/000011_biomedical_semantics.sql`
- Modify: `services/core/internal/database/migrate_test.go`

**Step 1: Write failing migration assertions**

Require these immutable or controlled tables:

```text
mesh_descriptors
mesh_qualifiers
publication_types
work_mesh_headings
work_mesh_qualifiers
work_publication_types
subject_import_receipts
subject_versions
subjects
biomedical_subject_rules
journal_subject_metrics
```

Required identity rules:

- MeSH descriptor primary identity: `descriptor_ui`;
- qualifier primary identity: `qualifier_ui`;
- Publication Type primary identity: `publication_type_ui`;
- canonical MeSH and Publication Type identity rows do not use a mutable display name as identity;
- source-backed work assertions include `source_record_id` and preserve the source label;
- `work_mesh_qualifiers` references the exact `work_mesh_headings` assertion to which the qualifier belongs;
- the same UI and a different label in a later source record creates a separate immutable assertion instead of overwriting prior evidence;
- PubMed article XML does not populate or synthesize MeSH Tree Numbers;
- `subject_import_receipts` records exact source, registry version, SHA-256, row counts, and import time;
- `subject_import_receipts` is unique by `(source, registry_version)` and by `file_sha256`;
- `subject_versions.subject_import_receipt_id` is a required unique foreign key to the receipt that created it;
- Subject rules include a version and an exact authorized JCR Category;
- `journal_subject_metrics` references an existing `venue_metric_snapshots` row and an exact Subject rule instead of copying or approximating JIF/Quartile;
- `journal_subject_metrics` uses composite foreign keys with one stored `jcr_category` value so the database proves that the Venue metric Category and Subject rule Category are exactly equal;
- no name-only uniqueness is used as the canonical identifier.

**Step 2: Run migration test and confirm RED**

Run:

```bash
go -C services/core test ./internal/database -run 'TestBiomedicalSemanticSchema' -count=1
```

Expected: FAIL because migration `000011` does not exist.

**Step 3: Implement migration**

Use foreign keys back to:

```text
works
source_records
source_record_works
```

Add database constraints that prevent:

- blank UI values;
- blank source labels;
- a source record linking semantics to a different Work;
- duplicate descriptor, qualifier, Publication Type, or Subject assertions.

Use the existing source-backed ownership pattern:

```text
source_record_id -> source_records(id) ON DELETE RESTRICT
(source_record_id, work_id) -> source_record_works(source_record_id, work_id)
    DEFERRABLE INITIALLY DEFERRED
```

Extend `ingestion_projection_assertions` with a composite unique key over `(id, source_record_uuid, work_id)`. Every source-backed semantic assertion must retain:

```text
projection_assertion_id
source_record_id
work_id
source_path
source label and semantic flags
```

Use a composite foreign key from `(projection_assertion_id, source_record_id, work_id)` to the matching ingestion projection assertion. This binds source path, source snapshot, scope policy, and projection policy without copying mutable provenance text into each row.

Require this receipt chain:

```text
subject_import_receipts
  -> subject_versions
  -> subjects
  -> biomedical_subject_rules
```

Add immutable triggers only to append-only import receipts, versioned taxonomy rows, and source-backed semantic assertions. Do not add an immutable trigger to a replaceable current-state table. Task 2 does not create a second mutable projection cache.

Current semantics must first use the complete winner tuple stored in `work_projection_states`:

```text
raw_event_id
source_record_uuid
work_id
scope_policy_version
projection_policy_version
```

Join that tuple to one `ingestion_projection_assertions.id`, then select semantic rows only by the matching `projection_assertion_id`. Selecting by `source_record_uuid` alone is forbidden because one raw/source record may be projected under multiple policy versions.

**Step 4: Verify empty and upgraded databases**

Run:

```bash
go -C services/core test ./internal/database -run 'TestBiomedicalSemanticSchema|TestMigrationFromEmptyDatabaseCreatesExpectedSchema|TestEmbeddedMigrationsPreservePriorChecksumsAndIncludeCurrentCatalogMigrations|TestMigrationUpgradesAppliedInitialSchemaWithoutChecksumMismatch' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/migrations services/core/internal/database/migrate_test.go
git commit -m "feat(core): add biomedical semantic schema"
```

### Task 3: Preserve PubMed biomedical fields through normalization

**Files:**

- Modify: `services/core/internal/ingestion/postgres_repository.go`
- Modify: `services/core/internal/ingestion/postgres_repository_test.go`
- Modify: `services/core/internal/ingestion/envelope_test.go`
- Modify: `services/core/internal/source/pubmed/parse_test.go`

**Step 1: Write a failing persistence test**

Build a PubMed record with:

- structured abstract sections;
- MeSH Descriptor and Qualifier UIs;
- Major Topic flags;
- Publication Types such as `Journal Article`, `Randomized Controlled Trial`, and `Meta-Analysis`;
- comments/corrections relation;
- PMID, PMCID, DOI.

Assert that after `Normalize` and `Project`:

- `ingestion_normalized_records.normalized_payload` contains all fields;
- the semantic tables contain source-backed rows;
- rerunning the same raw event is idempotent;
- the same UI with a different source label in a later source record preserves both immutable assertions;
- an older source event that does not win still preserves its semantic assertions, while a current-semantics query returns only the `work_projection_states` winner;
- the same raw/source record projected under two policy-version tuples preserves both assertion sets, while the current query returns only the tuple referenced by `work_projection_states`;
- no MeSH Tree Number is synthesized from PubMed article XML.

**Step 2: Run focused tests and confirm RED**

Run:

```bash
go -C services/core test ./internal/ingestion -run 'TestPostgresRepositoryPersistsPubMedBiomedicalSemantics' -count=1
```

Expected: FAIL because `persistedRecordPayload` currently drops these fields.

**Step 3: Extend the normalized payload**

Add explicit fields instead of serializing the entire source model:

```go
AbstractSections []source.AbstractSection `json:"abstract_sections"`
MeSHHeadings     []source.MeSHHeading     `json:"mesh_headings"`
PublicationTypes []source.PublicationType `json:"publication_types"`
Relations        []source.Relation        `json:"relations"`
Keywords         []source.Keyword         `json:"keywords"`
```

Deep-copy all nested collections.

**Step 4: Add deterministic projection**

Add projection functions that:

- upsert controlled MeSH and Publication Type identities;
- insert immutable source-backed Work assertions immediately after the matching `ingestion_projection_assertions` insert/replay and before `upsertSourceState`;
- persist deterministic PubMed XML `source_path` values and the matching `projection_assertion_id`;
- write assertions for every successfully projected source record, including records that do not become the Work winner;
- never insert or delete semantic assertions from `applyWinningProjection` or `applyRecordProjection`;
- select current paper semantics by joining the complete `work_projection_states` winner tuple to one `ingestion_projection_assertions.id`, then matching semantic assertions on `projection_assertion_id`;
- leave Subject membership to the exact Venue metric Category-to-Subject relation imported in Task 4.

Do not infer a Subject from title, abstract, journal title, or Category-name similarity. MeSH remains paper-level semantic evidence and does not replace the JCR Subject relation.

Do not parse, infer, or fabricate MeSH Tree Numbers from PubMed article XML. A future Tree Number feature requires a separate authorized NLM MeSH vocabulary import with an explicit vocabulary version and receipt.

Do not compress Publication Type strings into the existing generic `paper_type` field with ad-hoc mappings. If a normalized high-level study type is required, add a separate explicit, versioned mapping table and tests.

**Step 5: Verify**

Run:

```bash
go -C services/core test ./internal/source/pubmed ./internal/ingestion -count=1
go -C services/core test -race -p 1 ./internal/ingestion -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/internal/ingestion services/core/internal/source/pubmed
git commit -m "feat(pubmed): persist biomedical semantics"
```

### Task 4: Import a versioned biomedical JCR Subject registry

**Files:**

- Create: `data/subjects/biomedical-jcr-subjects.v1.csv`
- Create: `data/subjects/README.md`
- Create: `services/core/internal/biomed/taxonomy.go`
- Create: `services/core/internal/biomed/taxonomy_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Modify: `services/core/cmd/worker/main_test.go`
- Modify: `services/core/internal/venue/postgres_store.go`
- Modify: `services/core/internal/venue/postgres_store_test.go`

**Step 1: Write failing parser and command tests**

Add a worker command:

```text
paper-hub-worker import subjects --file /imports/biomedical-jcr-subjects.v1.csv
```

Require:

- explicit version;
- exact CSV headers;
- unique slug;
- exact authorized JCR Category name;
- no duplicate category rule within one Subject version;
- atomic `subject_import_receipts` row with source, version, file SHA-256, row counts, and import time;
- an auditable foreign-key path from receipt to Subject version, Subject rows, and Category rules;
- the same source/version with a different SHA-256 fails atomically;
- idempotent repeat import.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/biomed ./internal/venue ./cmd/worker -run 'Subject|Reconcile' -count=1
```

Expected: FAIL because package and command do not exist.

**Step 3: Implement the first Subject version**

The CSV is an explicit biomedical allowlist over exact JCR Category values from the authorized export. It may include categories such as Oncology, Immunology, Neurosciences, Genetics & Heredity, Cell Biology, Biochemistry & Molecular Biology, Pharmacology & Pharmacy, and Medical Informatics only when their exact source value has been reviewed and recorded.

Do not create a custom parent/child hierarchy in v1. Record the source, version, display label, stable slug, and exact Category match for every Subject.

Implement one idempotent exact-link reconciliation operation. Call it from both the Subject importer and the authorized JCR importer so import order is interchangeable:

```sql
INSERT INTO journal_subject_metrics (...)
SELECT ...
FROM venue_metric_snapshots AS metric
JOIN biomedical_subject_rules AS rule
  ON metric.category = rule.jcr_category
ON CONFLICT DO NOTHING
```

`journal_subject_metrics` must retain the metric snapshot ID, Subject rule ID, and one exact `jcr_category`. Composite foreign keys must prove that this Category equals both referenced rows. Do not use title matching, case-folded similarity, aliases, or partial Category matching.

Test both import orders:

```text
Subject -> JCR
JCR -> Subject
```

Both must create the same links. Category values that differ only by case, spacing, prefix, or substring must create no link.

**Step 4: Verify**

Run:

```bash
go -C services/core test ./internal/biomed ./internal/venue ./cmd/worker -count=1
git diff --check
```

Expected: PASS.

**Step 5: Commit**

```bash
git add data/subjects services/core/internal/biomed services/core/internal/venue services/core/cmd/worker
git commit -m "feat(core): add versioned biomedical JCR subjects"
```

## Phase 2: Enforce authorized journal curation

### Task 5: Generate Venue policy assessments after authorized JCR import

**Files:**

- Create: `services/core/internal/venue/assessment_service.go`
- Create: `services/core/internal/venue/assessment_service_test.go`
- Modify: `services/core/internal/venue/postgres_store.go`
- Modify: `services/core/internal/venue/postgres_store_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Modify: `services/core/cmd/worker/main_test.go`
- Modify: `services/core/internal/config/config.go`
- Modify: `services/core/internal/config/config_test.go`

**Step 1: Write failing assessment service tests**

Require deterministic decisions for:

```text
JCR Q1                    -> accepted, matched rule any_q1
JIF 10.0                  -> accepted, matched rule jif_gte_10
JIF 9.999 and Q2          -> rejected
missing metric evidence   -> unknown
non-journal Venue         -> not_applicable
```

Require one immutable assessment per:

```text
(venue_id, policy_version_id, metric_year)
```

and a source receipt reference in evidence.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/venue -run 'AssessmentService' -count=1
```

Expected: FAIL because the production service is absent.

**Step 3: Implement the service**

Use the existing exact-decimal `JournalPolicy.Evaluate`. The service must:

- enumerate matched and unmatched journal Venues for one JCR import receipt/year;
- read every category row;
- produce exactly one assessment per Venue;
- preserve all category evidence;
- write assessments in a serializable transaction;
- make a repeated run idempotent;
- reject a policy version/year mismatch.

**Step 4: Add an explicit Worker command**

Register:

```text
paper-hub-worker assess venues \
  --metric-year 2025 \
  --policy-version journal-jif-or-q1/v1 \
  --assessed-at 2026-07-17T00:00:00Z \
  --jcr-receipt <uuid>
```

`--assessed-at` is mandatory RFC3339Nano input and cannot fall back to the current time. Do not silently assess during API startup.

**Step 5: Verify**

Run:

```bash
go -C services/core test ./internal/venue ./cmd/worker ./internal/config -count=1
go -C services/core test -race -p 1 ./internal/venue -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/internal/venue services/core/cmd/worker services/core/internal/config
git commit -m "feat(venues): materialize JCR policy assessments"
```

### Task 6: Add biomedical scope policy without keyword guessing

**Files:**

- Create: `services/core/internal/biomed/scope.go`
- Create: `services/core/internal/biomed/scope_test.go`
- Modify: `services/core/internal/ingestion/policy.go`
- Modify: `services/core/internal/ingestion/policy_test.go`
- Modify: `services/core/cmd/worker/main.go`

**Step 1: Write failing policy tests**

The public biomedical eligibility input must be supported by one controlled fact:

- the verified Venue has at least one exact JCR Category in the approved biomedical Subject registry for the declared metric year.

Tests must reject:

- title-only keyword matches;
- journal title-only matches;
- MeSH-only eligibility without a biomedical JCR Subject;
- unversioned category names;
- records without controlled evidence.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/biomed ./internal/ingestion -run 'BiomedicalScope' -count=1
```

Expected: FAIL because only controlled canonical identity is currently evaluated.

**Step 3: Implement a separate biomedical publication policy**

Do not overload ingestion identity scope. Keep:

```text
ingestion scope = valid canonical record
public biomedical eligibility = controlled biomedical evidence
```

Persist the public eligibility decision and version separately so future taxonomy changes can be reassessed without changing immutable source records.

**Step 4: Verify**

Run:

```bash
go -C services/core test ./internal/biomed ./internal/ingestion -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/biomed services/core/internal/ingestion services/core/cmd/worker
git commit -m "feat(core): add biomedical publication eligibility"
```

### Task 7: Make `accepted` JCR assessment a Catalog publication invariant

**Files:**

- Modify: `services/core/internal/catalog/publisher.go`
- Modify: `services/core/internal/catalog/publisher_integration_test.go`
- Modify: `services/core/internal/catalog/model.go`
- Modify: `services/core/migrations/000007_public_catalog.sql` only if a clean-install correction is required
- Create: `services/core/migrations/000012_catalog_curation_gate.sql` when persisted generation metadata changes

**Step 1: Write failing integration tests**

Create current source states for papers whose Venue assessment is:

```text
accepted
rejected
unknown
not_applicable
missing
accepted for the wrong metric year
accepted by a stale policy version
```

Require:

- only the matching `accepted` paper is published;
- Work lifecycle is exactly `active`;
- Venue type is exactly `journal`;
- Venue linkage is backed by strict ISSN evidence;
- a generation with zero accepted papers returns `ErrEmptyDomain`;
- generation metadata includes JCR metric year and policy version;
- source revision changes when assessment evidence changes;
- rejected/missing records cannot be fetched by paper ID.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/catalog -run 'TestPublisherRequiresAcceptedVenueAssessment' -count=1
```

Expected: FAIL because `buildCurrentSnapshot` currently admits all `scope_status=included` Works.

**Step 3: Implement the invariant**

Filter before adding a Work to `visible`. Do not select “latest assessment”. Load the assessment constrained by the requested:

```text
metric_year
policy_name
policy_version
```

Extend `PublishInput` with explicit curation inputs:

```go
type PublishInput struct {
    FormulaVersion       string
    GeneratedAt          time.Time
    JCRMetricYear        int
    VenuePolicyName      string
    VenuePolicyVersion   int
    SubjectVersion       string
    JCRImportReceipt     uuid.UUID
}
```

Do not infer any of these values from current time or “latest row”.

**Step 4: Extend Worker flags**

Require:

```text
--jcr-metric-year
--venue-policy-name
--venue-policy-version
--subject-version
--jcr-import-receipt
```

All are mandatory and trimmed.

**Step 5: Verify**

Run:

```bash
go -C services/core test ./internal/catalog ./cmd/worker -count=1
go -C services/core test -race -p 1 ./internal/catalog -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/internal/catalog services/core/cmd/worker services/core/migrations
git commit -m "feat(catalog): enforce accepted biomedical journal gate"
```

## Phase 3: Citation and full-text evidence

### Task 8: Add source-specific citation snapshot schema

**Files:**

- Create: `services/core/migrations/000013_citation_evidence.sql`
- Modify: `services/core/internal/database/migrate_test.go`
- Create: `services/core/internal/citation/model.go`
- Create: `services/core/internal/citation/model_test.go`
- Create: `services/core/internal/citation/postgres_store.go`
- Create: `services/core/internal/citation/postgres_store_test.go`

**Step 1: Write failing migration and model tests**

Require:

```text
citation_snapshots
citation_edges
reference_edges
```

Snapshot uniqueness:

```text
(work_id, source, observed_at)
```

Required fields:

```text
count
source_record_id
ingestion_job_id
retrieved_at
coverage
definition_version
dataset_version
```

Counts must be nonnegative. Edges must preserve citing and cited identifiers plus source provenance.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/database ./internal/citation -run 'Citation' -count=1
```

Expected: FAIL.

**Step 3: Implement exact domain types and store**

Use integer counts and UTC timestamps. Do not store computed velocity in the source snapshot table.

**Step 4: Verify**

Run:

```bash
go -C services/core test ./internal/citation ./internal/database -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/migrations services/core/internal/database services/core/internal/citation
git commit -m "feat(citations): add source-specific evidence snapshots"
```

### Task 9: Implement the Europe PMC connector

**Files:**

- Create: `services/core/internal/source/europepmc/client.go`
- Create: `services/core/internal/source/europepmc/client_test.go`
- Create: `services/core/internal/source/europepmc/parse.go`
- Create: `services/core/internal/source/europepmc/parse_test.go`
- Create: `services/core/internal/source/europepmc/testdata/search.json`
- Create: `services/core/internal/source/europepmc/testdata/citations.json`
- Modify: `services/core/internal/source/source.go`
- Modify: `services/core/cmd/worker/main.go`
- Modify: `services/core/cmd/worker/main_test.go`
- Modify: `services/core/internal/config/config.go`
- Modify: `services/core/internal/config/config_test.go`
- Modify: `.env.example`

**Step 1: Write failing parser tests**

Test:

- PMID/PMCID/DOI identity;
- citation count;
- cited-by and reference identifiers;
- open-access flags and full-text links;
- missing versus zero counts;
- bounded payload size;
- duplicate JSON key rejection;
- unknown fields preserved only in raw payload.

**Step 2: Write failing client tests**

Require:

- HTTPS only;
- configured contact identity where supported;
- request timeout;
- bounded retries honoring `Retry-After`;
- bounded pages/results;
- no secret in URLs or logs;
- deterministic cursor/page order.

**Step 3: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/source/europepmc ./cmd/worker ./internal/config -count=1
```

Expected: FAIL because the connector does not exist.

**Step 4: Implement source and Worker command**

Register:

```text
paper-hub-worker sync europepmc \
  --query <query> \
  --from-date YYYY-MM-DD \
  --to-date YYYY-MM-DD \
  --max-results N
```

Project bibliographic fields as source assertions and persist citation evidence through `internal/citation`.

**Step 5: Verify**

Run:

```bash
go -C services/core test ./internal/source/europepmc ./internal/citation ./cmd/worker -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/internal/source/europepmc services/core/internal/source/source.go services/core/internal/citation services/core/cmd/worker services/core/internal/config .env.example
git commit -m "feat(sources): add Europe PMC citation enrichment"
```

### Task 10: Convert OpenAlex and Crossref citation counts into snapshots

**Files:**

- Modify: `services/core/internal/source/openalex/parse.go`
- Modify: `services/core/internal/source/openalex/parse_test.go`
- Modify: `services/core/internal/source/crossref/parse.go`
- Modify: `services/core/internal/source/crossref/parse_test.go`
- Modify: `services/core/internal/ingestion/service.go`
- Modify: `services/core/internal/ingestion/service_test.go`
- Modify: `services/core/internal/citation/postgres_store.go`

**Step 1: Write failing multi-source tests**

Require one Work with independent snapshots:

```text
openalex   42
europepmc  37
crossref   11
```

No source may overwrite another. A newer snapshot from the same source must append a row.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/ingestion ./internal/citation ./internal/source/openalex ./internal/source/crossref -run 'CitationSnapshot' -count=1
```

Expected: FAIL because current projection writes a generic `citation_count` metric.

**Step 3: Implement source-aware side effects**

Move citation persistence behind an explicit source-aware interface. `observed_at` and `retrieved_at` must come from the actual metric observation and retrieval event, not from the publication source revision time. Preserve the generic assertion only for backward-compatible reads until the Catalog switches to the new snapshot query.

**Step 4: Verify**

Run:

```bash
go -C services/core test ./internal/ingestion ./internal/citation ./internal/source/openalex ./internal/source/crossref -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/ingestion services/core/internal/citation services/core/internal/source/openalex services/core/internal/source/crossref
git commit -m "feat(citations): preserve source-specific counts"
```

### Task 11: Add licensed PMC OA full-text ingestion

**Files:**

- Create: `services/core/internal/source/pmc/client.go`
- Create: `services/core/internal/source/pmc/client_test.go`
- Create: `services/core/internal/source/pmc/license.go`
- Create: `services/core/internal/source/pmc/license_test.go`
- Create: `services/core/internal/source/pmc/parse.go`
- Create: `services/core/internal/source/pmc/parse_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Modify: `services/core/cmd/worker/main_test.go`
- Modify: `services/core/internal/ingestion/postgres_repository.go`
- Modify: `services/core/internal/ingestion/postgres_repository_test.go`

**Step 1: Write failing license boundary tests**

Require:

- explicit reusable license creates a `fulltext_asset`;
- missing or incompatible license does not create a public asset;
- raw JATS and derived extraction remain separate;
- checksum, byte size, media type, source URL, retrieved time, and license are recorded;
- no PDF/HTML scraping fallback.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/source/pmc ./internal/ingestion -count=1
```

Expected: FAIL.

**Step 3: Implement explicit command**

Register:

```text
paper-hub-worker sync pmc --pmcid <PMCID>
```

Batch expansion can be added only after the exact-PMCID path is verified.

**Step 4: Verify**

Run:

```bash
go -C services/core test ./internal/source/pmc ./internal/ingestion ./cmd/worker -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/source/pmc services/core/internal/ingestion services/core/cmd/worker
git commit -m "feat(sources): add licensed PMC full text"
```

## Phase 4: Deterministic biomedical analysis

### Task 12: Materialize citation velocity and cohort percentiles

**Files:**

- Create: `services/core/internal/analysis/citations.go`
- Create: `services/core/internal/analysis/citations_test.go`
- Create: `services/core/internal/analysis/postgres.go`
- Create: `services/core/internal/analysis/postgres_test.go`
- Modify: `services/core/internal/ranking/formula.go`
- Modify: `services/core/internal/ranking/formula_test.go`
- Modify: `services/core/cmd/worker/main.go`

**Step 1: Write failing formula tests**

Require:

- at least two ordered snapshots from the same source;
- exact elapsed time;
- no division by zero;
- no mixing sources;
- cohort key includes Subject, publication year, and Publication Type;
- percentile requires an explicit cohort and minimum observation count;
- insufficient evidence returns a typed result, not zero.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/analysis ./internal/ranking -run 'Citation' -count=1
```

Expected: FAIL because the materialization package is absent.

**Step 3: Implement immutable analysis runs**

Register:

```text
paper-hub-worker analyze citations \
  --as-of RFC3339Nano \
  --source openalex \
  --formula-version citation-intelligence/v1 \
  --subject-version biomedical-jcr-subjects/v1
```

Persist inputs, cohort definition, outputs, missing signals, and source revision in `analysis_runs` plus ranking snapshots.

**Step 4: Verify**

Run:

```bash
go -C services/core test ./internal/analysis ./internal/ranking ./cmd/worker -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/analysis services/core/internal/ranking services/core/cmd/worker
git commit -m "feat(analysis): compute biomedical citation intelligence"
```

### Task 13: Implement publication trend models

**Files:**

- Create: `services/core/internal/analysis/trends.go`
- Create: `services/core/internal/analysis/trends_test.go`
- Modify: `services/core/internal/analysis/postgres.go`
- Modify: `services/core/cmd/worker/main.go`

**Step 1: Write failing statistical contract tests**

Test fixed datasets for:

- Poisson rate ratio with exact exposure windows;
- Negative Binomial selection only when predeclared dispersion criteria are met;
- confidence interval;
- minimum paper count;
- independent journal/team count;
- Benjamini-Hochberg correction;
- `insufficient_evidence` for sparse cohorts;
- no model switching based on desired significance.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/analysis -run 'Trend' -count=1
```

Expected: FAIL.

**Step 3: Implement versioned analysis**

Add:

```text
paper-hub-worker analyze trends \
  --as-of RFC3339Nano \
  --recent-window-days 56 \
  --baseline-window-days 364 \
  --formula-version biomedical-trends/v1
```

Generate paper, Subject, MeSH, Publication Type, method, and journal trend rows.

**Step 4: Verify**

Run:

```bash
go -C services/core test ./internal/analysis ./cmd/worker -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/analysis services/core/cmd/worker
git commit -m "feat(analysis): add biomedical trend models"
```

### Task 14: Implement journal editorial-pattern analysis and evidence gaps

**Files:**

- Create: `services/core/migrations/000014_journal_analysis.sql`
- Create: `services/core/internal/analysis/journals.go`
- Create: `services/core/internal/analysis/journals_test.go`
- Create: `services/core/internal/analysis/opportunities.go`
- Create: `services/core/internal/analysis/opportunities_test.go`
- Modify: `services/core/internal/analysis/postgres.go`
- Modify: `services/core/cmd/worker/main.go`

**Step 1: Write failing journal enrichment tests**

For a fixed journal/category cohort require:

- odds ratio or rate ratio;
- confidence interval;
- support paper count;
- field baseline count;
- multiple-testing-adjusted value;
- no result below minimum support;
- wording payload uses `editorial_pattern`, not `preference`.

**Step 2: Write failing opportunity tests**

Only allow predefined evidence-gap rules:

```text
rapid_growth_low_rct_share
single_center_external_validation_gap
high_citation_low_open_data
observational_dominance
emerging_method_low_independent_team_count
```

Each result must contain:

- trigger rule version;
- supporting Work IDs;
- estimate and uncertainty;
- coverage;
- limitations;
- status.

**Step 3: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/analysis -run 'Journal|Opportunity' -count=1
```

Expected: FAIL.

**Step 4: Implement and persist immutable outputs**

Add:

```text
journal_pattern_snapshots
research_opportunity_snapshots
```

The Catalog publisher copies accepted analysis outputs into its immutable generation; it does not recompute them.

**Step 5: Verify**

Run:

```bash
go -C services/core test ./internal/analysis ./internal/database ./cmd/worker -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/migrations services/core/internal/analysis services/core/cmd/worker
git commit -m "feat(analysis): add journal patterns and evidence gaps"
```

## Phase 5: Public API and Nuxt product

### Task 15: Publish biomedical semantics and analysis into Catalog

**Files:**

- Modify: `services/core/internal/catalog/publisher.go`
- Modify: `services/core/internal/catalog/publisher_integration_test.go`
- Modify: `services/core/internal/catalog/model.go`
- Modify: `services/core/migrations/000012_catalog_curation_gate.sql`
- Modify: `contracts/openapi.yaml`

**Step 1: Write failing Catalog payload tests**

Require Paper summary/detail to expose:

```text
subjects
mesh_headings
publication_types
journal
jcr_assessment
citation_snapshots
citation_velocity
citation_percentile
open_fulltext
```

Require Catalog trend and opportunity rows to originate from explicitly selected completed immutable analysis runs with matching source revision.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/catalog -run 'Biomedical|Analysis' -count=1
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
```

Expected: FAIL.

**Step 3: Extend publication**

Include source-specific citation values. Select one configured display source only for the list sort value, while preserving all sources in detail payload.

Extend Catalog publish input with explicit citation-analysis, trend-analysis, and journal-pattern run IDs. Reject missing, incomplete, failed, stale, or source-revision-mismatched runs. Do not select the newest analysis row implicitly.

Do not create a download value when no authoritative usage snapshot exists.

**Step 4: Verify**

Run:

```bash
go -C services/core test ./internal/catalog -count=1
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/catalog services/core/migrations contracts/openapi.yaml
git commit -m "feat(catalog): publish biomedical intelligence"
```

### Task 16: Add home, Subject, and journal API repositories

**Files:**

- Create: `services/core/internal/catalog/home.go`
- Create: `services/core/internal/catalog/home_test.go`
- Create: `services/core/internal/catalog/subjects.go`
- Create: `services/core/internal/catalog/subjects_test.go`
- Create: `services/core/internal/catalog/journals.go`
- Create: `services/core/internal/catalog/journals_test.go`
- Modify: `services/core/internal/httpapi/catalog.go`
- Modify: `services/core/internal/httpapi/catalog_integration_test.go`
- Modify: `services/core/internal/catalog/analysis.go`
- Modify: `contracts/openapi.yaml`

**Step 1: Write failing repository and HTTP tests**

Cover:

```text
GET /api/v1/home
GET /api/v1/subjects
GET /api/v1/subjects/{slug}
GET /api/v1/journals
GET /api/v1/journals/{slug}
GET /api/v1/trends/subjects
GET /api/v1/trends/journals
GET /api/v1/trends/mesh
```

Require:

- one generation-bound Home response containing scope, coverage, latest papers, citation momentum, Subject momentum, active journals, and evidence gaps;
- generation-bound cursor;
- exact filters;
- stable sort;
- unknown slug returns 404;
- non-GET returns 405;
- generation header;
- no access to rejected journal records.

**Step 2: Run and confirm RED**

Run:

```bash
go -C services/core test ./internal/catalog ./internal/httpapi -run 'Subject|Journal|MeshTrend' -count=1
```

Expected: FAIL.

**Step 3: Implement repositories and handlers**

Reuse cursor and problem-details infrastructure. Do not query mutable ingestion tables from public HTTP handlers.

**Step 4: Verify**

Run:

```bash
go -C services/core test ./internal/catalog ./internal/httpapi -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/catalog services/core/internal/httpapi contracts/openapi.yaml
git commit -m "feat(api): add biomedical home Subject and journal discovery"
```

### Task 17: Generate Nuxt API types from OpenAPI

**Files:**

- Modify: `apps/web/package.json`
- Modify: `pnpm-lock.yaml`
- Create: `apps/web/scripts/generate-openapi-types.mjs`
- Create: `apps/web/app/types/openapi.generated.ts`
- Modify: `apps/web/app/types/catalog.ts`
- Modify: `apps/web/tests/openapi.test.ts`
- Modify: `.github/workflows/ci.yml`
- Modify: `Makefile`

**Step 1: Write a failing generation-consistency test**

Require:

- generated TypeScript is derived from `contracts/openapi.yaml`;
- generation is deterministic;
- `CatalogValue`, pagination, paper, Subject, journal, trend, and opportunity response shapes come from generated types;
- handwritten UI-only view models remain separate;
- generated files cannot contain `any`.

**Step 2: Run and confirm RED**

Run:

```bash
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
```

Expected: FAIL because API response types are handwritten and currently drift from OpenAPI.

**Step 3: Add a pinned OpenAPI TypeScript generator**

Add the generator as a locked development dependency. The script must read:

```text
../../contracts/openapi.yaml
```

and write only:

```text
app/types/openapi.generated.ts
```

Do not edit the generated file manually.

**Step 4: Replace permissive handwritten API response types**

Keep aliases such as:

```ts
export type PaperSummary =
  components["schemas"]["PaperSummary"]
```

UI presentation models may remain handwritten, but adapters must accept generated API types as input.

**Step 5: Add CI drift verification**

Add:

```bash
make generate-api-types
git diff --exit-code
```

before Nuxt typecheck.

**Step 6: Verify**

Run:

```bash
make generate-api-types
git diff --exit-code
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
pnpm --dir apps/web typecheck
```

Expected: PASS.

**Step 7: Commit**

```bash
git add apps/web/package.json pnpm-lock.yaml apps/web/scripts apps/web/app/types apps/web/tests/openapi.test.ts .github/workflows/ci.yml Makefile
git commit -m "chore(web): generate API types from OpenAPI"
```

### Task 18: Rename and restructure the Nuxt shell as medpaperhub

**Files:**

- Modify: `apps/web/app/app.vue`
- Modify: `apps/web/app/layouts/default.vue`
- Modify: `apps/web/app/components/AppHeader.vue`
- Modify: `apps/web/app/components/AppFooter.vue`
- Modify: `apps/web/app/components/DiscoveryRail.vue`
- Modify: `apps/web/app/assets/css/main.css`
- Modify: `apps/web/nuxt.config.ts`
- Modify: `apps/web/tests/replatform.test.ts`
- Modify: `apps/web/tests/components.test.ts`
- Modify: `apps/web/tests/visual-foundation.contract.test.ts`
- Modify: `docs/design/visual-rationale.md`

**Step 1: Write failing brand and navigation tests**

Require:

- visible brand exactly `medpaperhub`;
- document title contains `medpaperhub`;
- first-level navigation: 首页、学科、期刊、论文、趋势、研究机会;
- no `Paper Research Hub` string;
- no AI Agent-specific copy;
- 375/768/1024/1440 layouts remain overflow-free;
- keyboard targets remain at least 44×44.

**Step 2: Run and confirm RED**

Run:

```bash
pnpm --dir apps/web exec vitest run tests/replatform.test.ts tests/components.test.ts tests/visual-foundation.contract.test.ts --config vitest.config.ts
```

Expected: FAIL because the old brand and navigation remain.

**Step 3: Implement the shell**

Keep current design tokens unless a token demonstrably conflicts with the biomedical information hierarchy. Do not redesign unrelated controls.

**Step 4: Verify**

Run:

```bash
pnpm --dir apps/web lint
pnpm --dir apps/web typecheck
pnpm --dir apps/web exec vitest run tests/replatform.test.ts tests/components.test.ts tests/visual-foundation.contract.test.ts --config vitest.config.ts
```

Expected: PASS.

**Step 5: Commit**

```bash
git add apps/web docs/design/visual-rationale.md
git commit -m "feat(web): rebrand the portal as medpaperhub"
```

### Task 19: Build the biomedical home page

**Files:**

- Modify: `apps/web/app/pages/index.vue`
- Create: `apps/web/app/components/BiomedicalDailyBrief.vue`
- Create: `apps/web/app/components/SubjectMomentumGrid.vue`
- Create: `apps/web/app/components/JournalActivityList.vue`
- Create: `apps/web/app/components/CitationMomentumList.vue`
- Create: `apps/web/tests/biomedical-home.test.ts`
- Modify: `apps/web/app/types/catalog.ts`
- Modify: `apps/web/app/utils/catalogApi.ts`

**Step 1: Write failing home tests**

Require sections:

```text
今日新增精选论文
学科趋势
引用增长
活跃期刊
热门疾病/靶点/方法
研究机会
数据覆盖
```

Every metric card must display:

- time window;
- generation or update time;
- sample size/coverage;
- missing state when unavailable.

The home page must consume one `GET /api/v1/home` response. It must not issue independent stats, paper, Subject, journal, and trend requests that could cross Catalog generations.

**Step 2: Run and confirm RED**

Run:

```bash
pnpm --dir apps/web exec vitest run tests/biomedical-home.test.ts --config vitest.config.ts
```

Expected: FAIL.

**Step 3: Implement using real API calls**

Do not add fallback fixtures. `503 catalog_not_published` remains a truthful empty state.

**Step 4: Verify**

Run:

```bash
pnpm --dir apps/web exec vitest run tests/biomedical-home.test.ts tests/home.test.ts --config vitest.config.ts
pnpm --dir apps/web typecheck
```

Expected: PASS.

**Step 5: Commit**

```bash
git add apps/web
git commit -m "feat(web): add biomedical intelligence home"
```

### Task 20: Build Subject and journal pages

**Files:**

- Create: `apps/web/app/pages/subjects/index.vue`
- Create: `apps/web/app/pages/subjects/[slug].vue`
- Create: `apps/web/app/pages/journals/index.vue`
- Create: `apps/web/app/pages/journals/[slug].vue`
- Create: `apps/web/app/components/JournalCurationEvidence.vue`
- Create: `apps/web/app/components/EditorialPatternTable.vue`
- Create: `apps/web/tests/subjects.test.ts`
- Create: `apps/web/tests/journals.test.ts`
- Modify: `apps/web/app/types/catalog.ts`
- Modify: `apps/web/app/utils/catalogApi.ts`
- Modify: `apps/web/app/utils/routeLabel.ts`

**Step 1: Write failing Subject tests**

Require:

- recent papers;
- active journals;
- MeSH/Publication Type distributions;
- trend estimates and uncertainty;
- evidence gaps;
- explicit taxonomy version.

**Step 2: Write failing journal tests**

Require:

- title, Publisher, ISSNs;
- JCR year, JIF, all categories/Quartiles;
- accepted matched rules;
- recent papers;
- editorial-pattern estimates with baseline and support;
- no “journal likes” causal wording.

**Step 3: Run and confirm RED**

Run:

```bash
pnpm --dir apps/web exec vitest run tests/subjects.test.ts tests/journals.test.ts --config vitest.config.ts
```

Expected: FAIL.

**Step 4: Implement**

Use URL-addressable filters and generation-bound pagination. No client-only hidden filtering of rejected records.

**Step 5: Verify**

Run:

```bash
pnpm --dir apps/web exec vitest run tests/subjects.test.ts tests/journals.test.ts --config vitest.config.ts
pnpm --dir apps/web typecheck
```

Expected: PASS.

**Step 6: Commit**

```bash
git add apps/web
git commit -m "feat(web): add Subject and journal intelligence"
```

### Task 21: Upgrade paper, trend, and opportunity pages

**Files:**

- Modify: `apps/web/app/pages/papers/index.vue`
- Modify: `apps/web/app/pages/papers/[id].vue`
- Modify: `apps/web/app/pages/trends.vue`
- Modify: `apps/web/app/pages/opportunities.vue`
- Modify: `apps/web/app/components/CatalogPaperCard.vue`
- Create: `apps/web/app/components/MedicalEvidencePanel.vue`
- Create: `apps/web/app/components/CitationEvidenceChart.vue`
- Create: `apps/web/app/components/TrendEstimate.vue`
- Modify: `apps/web/tests/public-pages.contract.test.ts`
- Create: `apps/web/tests/medical-paper-detail.test.ts`
- Create: `apps/web/tests/biomedical-trends.test.ts`

**Step 1: Write failing detail tests**

Require:

- MeSH;
- Publication Type;
- Subject;
- JCR curation evidence;
- source-specific citation counts;
- velocity and cohort percentile;
- open-full-text license;
- explicit missing downloads.

**Step 2: Write failing trend tests**

Require:

- recent and baseline windows;
- estimate;
- confidence interval;
- sample size;
- adjusted significance;
- insufficient-evidence state.

**Step 3: Run and confirm RED**

Run:

```bash
pnpm --dir apps/web exec vitest run tests/medical-paper-detail.test.ts tests/biomedical-trends.test.ts --config vitest.config.ts
```

Expected: FAIL.

**Step 4: Implement**

Remove generic AI Agent filters. Add biomedical filters:

```text
subject
journal
mesh
publication_type
published_from/to
citation_source
minimum citation percentile
open_fulltext
```

JCR acceptance remains non-bypassable and is not exposed as an “all papers” switch.

**Step 5: Verify**

Run:

```bash
pnpm --dir apps/web test
pnpm --dir apps/web typecheck
pnpm --dir apps/web build
```

Expected: PASS.

**Step 6: Commit**

```bash
git add apps/web
git commit -m "feat(web): expose biomedical paper and trend evidence"
```

## Phase 6: Deployment and end-to-end verification

### Task 22: Add explicit scheduled job documentation and Compose commands

**Files:**

- Modify: `Makefile`
- Modify: `docker-compose.yml`
- Modify: `.env.example`
- Modify: `README.md`
- Create: `docs/deployment/biomedical-pipeline.md`
- Modify: `.github/workflows/ci.yml`
- Modify: `.github/workflows/container.yml`

**Step 1: Write failing command/help tests**

Require Make targets for explicit one-shot operations:

```text
import-subjects
sync-pubmed
sync-crossref
sync-europepmc
sync-openalex
sync-pmc
import-jcr
assess-venues
analyze-citations
analyze-trends
analyze-journals
publish-catalog
```

Targets must fail when mandatory deterministic arguments are absent.

**Step 2: Run and confirm RED**

Run:

```bash
make help
docker compose config --quiet
```

Expected: commands are absent before implementation.

**Step 3: Implement deployment contract**

Document the production order and required Secrets. Keep Web, API, Worker, and Migrate separate. No cron runs inside the API container.

**Step 4: Verify**

Run:

```bash
make compose-config
make smoke
```

Expected: PASS.

**Step 5: Commit**

```bash
git add Makefile docker-compose.yml .env.example README.md docs/deployment .github
git commit -m "chore(deploy): define medpaperhub data jobs"
```

### Task 23: Verify a complete biomedical Catalog from a clean database

**Files:**

- Create: `artifacts/verification/medpaperhub-pipeline.json`
- Create: `artifacts/verification/medpaperhub-browser.json`
- Modify: `.gitignore`
- Modify: `README.md`

**Step 1: Start a clean isolated database**

Use a dedicated Compose project and volume. Do not reuse the current 33-paper AI Agent validation database.

Run:

```bash
docker compose -p medpaperhub-verification \
  -f docker-compose.yml \
  up -d --build postgres migrate
docker compose -p medpaperhub-verification \
  -f docker-compose.yml \
  wait migrate
```

Expected: migration exits `0`.

**Step 2: Import the versioned biomedical Subject registry**

Run the exact command documented by Task 4.

Expected: one immutable import receipt and the expected Subject/rule counts.

**Step 3: Run narrow live source syncs**

Use a declared biomedical query and bounded date window. Capture:

- fetched count;
- projected count;
- excluded count;
- failed count;
- source time range;
- job IDs.

Do not store credentials or raw copyrighted full text in artifacts.

**Step 4: Import authorized JCR CSV**

This step requires a user-authorized production CSV mounted read-only. If the file is unavailable:

- do not use the synthetic fixture;
- mark live production verification blocked;
- continue only with integration tests.

Expected: receipt checksum, metric year, row count, Venue match count.

**Step 5: Assess Venues**

Expected:

```text
accepted > 0
rejected >= 0
unknown >= 0
not_applicable >= 0
```

Verify a known negative Venue remains absent from public publication.

**Step 6: Run citation and trend analysis**

Require completed immutable analysis runs. Verify at least one result with sufficient evidence and one `insufficient_evidence` result.

**Step 7: Publish Catalog**

Pass every explicit version and timestamp flag. Verify:

```text
catalog_generations = 1
catalog_papers > 0
catalog_papers = accepted biomedical visible Works only
```

**Step 8: Verify API**

Run:

```bash
curl -fsS http://localhost:8080/
curl -fsS http://localhost:8080/health
curl -fsS http://localhost:8080/api/v1/stats
curl -fsS 'http://localhost:8080/api/v1/papers?limit=20'
curl -fsS 'http://localhost:8080/api/v1/subjects?limit=20'
curl -fsS 'http://localhost:8080/api/v1/journals?limit=20'
curl -fsS 'http://localhost:8080/api/v1/trends/journals?window_days=30'
```

Expected: `200`, request ID, Catalog generation header, and no rejected paper.

**Step 9: Verify browser flows**

Using `@browse`, check:

```text
/
/subjects
/subjects/{known-slug}
/journals
/journals/{known-slug}
/papers
/papers/{known-id}
/trends
/opportunities
```

Verify:

- brand is `medpaperhub`;
- no AI Agent sample text;
- no mock values;
- no console errors;
- mobile and desktop layouts;
- JCR evidence is visible;
- missing downloads display as missing;
- trend uncertainty is visible.

**Step 10: Run complete verification**

Run:

```bash
unformatted="$(gofmt -l services/core)"; test -z "${unformatted}"
go -C services/core vet ./...
go -C services/core test ./...
go -C services/core test -race -p 1 ./... -count=1
go -C services/core build ./...
make generate
git diff --exit-code
make lint-web
make typecheck
make test-web
make build-web
pnpm --dir apps/web test:e2e
make smoke
git status --short
```

Expected: all commands exit `0`; only intended verification artifacts remain.

**Step 11: Commit**

```bash
git add artifacts/verification .gitignore README.md
git commit -m "test: verify medpaperhub biomedical portal"
```

## Final acceptance gate

Do not call the product Demo complete until all statements below are true:

1. The UI brand is `medpaperhub`.
2. The public Catalog contains only biomedical journal papers.
3. Every public paper has a matching `accepted` Venue assessment.
4. `accepted` is exactly `JCR Q1 OR JIF >= 10` for an explicit metric year.
5. PubMed MeSH and Publication Type survive parsing, normalization, projection, Catalog publication, API, and UI rendering.
6. Citation counts retain source and observation time.
7. Citation velocity uses at least two same-source snapshots.
8. Trend estimates expose window, baseline, support, uncertainty, and formula version.
9. Journal pages use statistical editorial-pattern language, not causal preference claims.
10. Downloads are shown only from an authoritative article-level source; otherwise they are missing.
11. Research opportunities are rule-backed and list supporting evidence and limitations.
12. Web and API remain independently deployable.
13. A clean-database end-to-end run publishes a real immutable Catalog generation.
14. Full local verification, remote CI, container smoke, and browser QA pass.
