# Daily Publication Intelligence Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a trustworthy daily biomedical publication dashboard that separates formal publication, acceptance, and ahead-of-print events and exposes them through one generation-bound Home API.

**Architecture:** Extend the immutable PubMed normalization pipeline to preserve publication status/history evidence in `normalized-record/v3`, persist append-only event assertions, and maintain a replaceable current state projection. The Catalog publisher reads only provenance-bound current states for already eligible biomedical Works, publishes one immutable Home snapshot, and Nuxt renders a compact content-first dashboard.

**Tech Stack:** Go 1.26, PostgreSQL 18, pgx, PubMed XML, OpenAPI 3.1, Nuxt 4, Vue 3, TypeScript, Vitest, Playwright, Docker Compose.

---

## Execution rules

- Use `@superpowers:test-driven-development` for every behavior change.
- Run every new test once in RED before production implementation.
- Use `@systematic-debugging` for unexpected failures.
- Use `@verification-before-completion` before commits and completion claims.
- Do not infer publication state from titles, generic Work lifecycle status, retrieval time, or cross-source field stitching.
- Do not modify existing normalized v1/v2 assertions.
- Do not publish synthetic papers, citation counts, download counts, JCR values, or trend values.
- Public Works must still pass the existing `JCR Q1 OR exact JIF >= 10` and biomedical Subject gate.

## Task 1: Add publication event schema

**Files:**

- Create: `services/core/migrations/000018_publication_event_assertions.sql`
- Modify: `services/core/internal/database/migrate_test.go`

**Step 1: Write the failing migration tests**

Add tests requiring:

- `work_publication_event_assertions` is append-only;
- every assertion binds one projection assertion, normalized assertion, source record, and Work;
- event kinds are restricted to `print_published`, `electronic_published`, `ahead_of_print`, and `accepted`;
- date precision is controlled;
- raw source date and XML path are preserved;
- `work_publication_states` stores `known`, `missing`, or `conflict` for each event;
- the current state table is updateable and has no immutable trigger;
- cross-Work or cross-source provenance is rejected.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/database -run 'TestPublicationEventAssertionSchema|TestEmbeddedMigrationsPreservePriorChecksumsAndIncludeCurrentCatalogMigrations' -count=1
```

Expected: FAIL because migration 18 and its tables do not exist.

**Step 3: Implement migration 18**

Use composite foreign keys back to:

```text
ingestion_projection_assertions
ingestion_normalized_records
source_records
source_record_works
works
```

Add indexes for:

```text
(event_kind, event_date DESC, work_id)
(work_id, projection_assertion_id)
(source_record_id, ordinal)
```

Do not backfill publication meaning from `works.published_at`.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/database -run 'TestPublicationEventAssertionSchema|TestMigrationFromEmptyDatabaseCreatesExpectedSchema|TestEmbeddedMigrationsPreservePriorChecksumsAndIncludeCurrentCatalogMigrations|TestMigrationUpgradesAppliedInitialSchemaWithoutChecksumMismatch' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/migrations/000018_publication_event_assertions.sql services/core/internal/database/migrate_test.go
git commit -m "feat(core): add publication event evidence schema"
```

## Task 2: Parse explicit PubMed publication events

**Files:**

- Modify: `services/core/internal/source/source.go`
- Modify: `services/core/internal/source/pubmed/parse.go`
- Modify: `services/core/internal/source/pubmed/parse_test.go`
- Add fixtures under: `services/core/internal/source/pubmed/testdata/`

**Step 1: Write failing parser tests**

Cover:

1. `Article/@PubModel`;
2. `PubmedData/PublicationStatus`;
3. ordered History entries with `PubStatus`, date precision, and source path;
4. accepted, ahead-of-print, epublish, and ppublish;
5. unknown PubStatus values preserved as raw history evidence but not converted to a public event;
6. partial dates retained with year/month precision;
7. `ArticleDate Electronic` alone does not create an ahead-of-print event.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/source/pubmed -run 'TestParsePublication' -count=1
```

Expected: FAIL because the source model does not contain publication model, status, or history.

**Step 3: Implement minimal source model and parser**

Add explicit source types:

```go
type PublicationHistoryEntry struct {
    Status     string
    Date       SourceDate
    SourcePath string
    Ordinal    int
}
```

Add to `source.Record`:

```go
PublicationModel   string
PublicationStatus  string
PublicationHistory []PublicationHistoryEntry
```

Deep-copy the new collection in all clone paths.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/source/pubmed ./internal/source -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/source services/core/internal/source/pubmed
git commit -m "feat(pubmed): parse publication history evidence"
```

## Task 3: Persist normalized-record/v3 and publication projections

**Files:**

- Modify: `services/core/internal/ingestion/postgres_repository.go`
- Modify: `services/core/internal/ingestion/postgres_repository_test.go`
- Modify: `services/core/internal/ingestion/repository.go`
- Modify: `services/core/internal/ingestion/envelope_test.go`

**Step 1: Write failing ingestion tests**

Require:

- normalized payload schema is `normalized-record/v3`;
- publication model, status, and ordered History survive normalization;
- existing v2 assertion remains unchanged;
- replaying the same raw event creates/reuses v3 without mutating v2;
- every recognized PubMed event creates an immutable assertion with exact source path;
- partial dates are persisted but not marked `known` for daily publication;
- conflicting day dates produce a `conflict` state;
- one source assertion cannot borrow a status/date from another source;
- an older non-winning projection keeps its assertions but cannot replace current state;
- projection replay is idempotent.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/ingestion -run 'TestPostgresRepositoryPersistsPublicationEvents|TestPostgresRepositoryReplaysPublicationSchemaV3' -count=1
```

Expected: FAIL because the v3 payload and publication event projection do not exist.

**Step 3: Implement v3 normalization**

Extend `persistedRecordPayload` with:

```go
PublicationModel   string                           `json:"publication_model,omitempty"`
PublicationStatus  string                           `json:"publication_status,omitempty"`
PublicationHistory []source.PublicationHistoryEntry `json:"publication_history"`
```

Set:

```go
normalizedPayloadSchemaVersion = "normalized-record/v3"
```

Do not update prior normalized rows.

**Step 4: Implement deterministic assertion and state projection**

After the immutable projection assertion exists:

1. derive recognized publication event assertions from the normalized PubMed payload;
2. insert each assertion idempotently;
3. compute `known/missing/conflict` only within that one projection assertion;
4. update `work_publication_states` only when the same projection becomes the current Work winner;
5. never use Crossref dates or `works.published_at` to fill PubMed event states.

**Step 5: Run GREEN**

```bash
go -C services/core test ./internal/ingestion -count=1
go -C services/core test -race -p 1 ./internal/ingestion -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/internal/ingestion
git commit -m "feat(ingestion): project publication event evidence"
```

## Task 4: Publish generation-bound Home publication updates

**Files:**

- Modify: `services/core/internal/catalog/publisher.go`
- Modify: `services/core/internal/catalog/biomedical_snapshot.go`
- Modify: `services/core/internal/catalog/biomedical_publication_test.go`
- Modify: `services/core/internal/catalog/publisher_integration_test.go`
- Modify: `services/core/internal/catalog/home_test.go`

**Step 1: Write failing Catalog tests**

Require:

- `publication_updates.calendar_date` is the Catalog generation date;
- timezone is explicit and stable;
- formal publication uses only known ppublish/eligible epublish day events;
- accepted and ahead-of-print collections use their own events;
- conflict, missing, partial date, rejected JCR, non-biomedical, retracted, withdrawn, and excluded Works do not appear;
- event provenance points to the same source assertion as the published Work revision;
- sorting is event date descending then canonical key;
- empty collections are real empty arrays with analysis metadata, not fixtures.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/catalog -run 'TestPublisherPublishesDailyPublicationUpdates|TestPublisherRejectsPublicationEventProvenanceMismatch' -count=1
```

Expected: FAIL because Home has no `publication_updates`.

**Step 3: Extend the publisher model**

Load the exact current publication state while building each `publishedPaper`. Reject Catalog publication if a claimed known state has no matching immutable event assertion or mismatched provenance.

**Step 4: Build Home collections**

Add:

```text
formal_publications_today
recent_acceptances
recent_online_first
```

Use calendar windows derived from `PublishInput.GeneratedAt` and the declared timezone. Do not use `time.Now()` inside snapshot generation.

**Step 5: Run GREEN**

```bash
go -C services/core test ./internal/catalog -count=1
go -C services/core test -race -p 1 ./internal/catalog -count=1
```

Expected: PASS.

**Step 6: Commit**

```bash
git add services/core/internal/catalog
git commit -m "feat(catalog): publish daily publication updates"
```

## Task 5: Lock the Home OpenAPI contract

**Files:**

- Modify: `contracts/openapi.yaml`
- Modify: `apps/web/tests/openapi.test.ts`
- Regenerate: `apps/web/app/types/openapi.generated.ts`
- Modify if required: `apps/web/app/types/biomedical.ts`
- Modify: `services/core/internal/httpapi/catalog_integration_test.go`

**Step 1: Write failing contract tests**

Require:

- `HomeResponse.publication_updates`;
- exact event-kind enum;
- ISO date rather than date-time for event dates;
- required provenance fields;
- collection `analysis`, `items`, and `pagination`;
- existing `PaperAnalysisCollection.pagination` runtime/contract mismatch is removed.

**Step 2: Run RED**

```bash
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
go -C services/core test ./internal/httpapi -run 'TestCatalogHome' -count=1
```

Expected: FAIL because the OpenAPI contract does not describe publication updates and pagination is inconsistent.

**Step 3: Update OpenAPI and generate types**

```bash
pnpm --dir apps/web generate:api-types
```

Do not hand-edit generated TypeScript.

**Step 4: Run GREEN**

```bash
pnpm --dir apps/web check:api-types
pnpm --dir apps/web exec vitest run tests/openapi.test.ts --config vitest.config.ts
go -C services/core test ./internal/httpapi -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add contracts/openapi.yaml apps/web/app/types apps/web/tests/openapi.test.ts services/core/internal/httpapi/catalog_integration_test.go
git commit -m "feat(api): add daily publication home contract"
```

## Task 6: Rebuild the Nuxt Home as a daily intelligence dashboard

**Files:**

- Modify: `apps/web/app/pages/index.vue`
- Modify: `apps/web/app/layouts/default.vue`
- Modify: `apps/web/app/components/AppHeader.vue`
- Modify: `apps/web/app/components/BiomedicalDailyBrief.vue`
- Modify: `apps/web/app/components/SyncStatus.vue`
- Modify: `apps/web/app/components/AppFooter.vue`
- Create: `apps/web/app/components/PublicationUpdateList.vue`
- Create: `apps/web/app/components/PublicationEventCard.vue`
- Modify: `apps/web/app/assets/css/main.css`
- Modify: `apps/web/tests/home.test.ts`
- Modify: `apps/web/tests/components.test.ts`
- Modify: `apps/web/tests/visual-foundation.contract.test.ts`
- Modify/add Playwright tests under: `apps/web/e2e/`

**Step 1: Write failing UI tests**

Require:

- navigation order is `今日、论文、学科、期刊、趋势`;
- research opportunities are absent from primary navigation;
- Header has one Search utility linking to `/papers#papers-q`;
- global `DiscoveryRail` is not rendered;
- Home has no hero marketing paragraph or search form;
- publication sections render from `publication_updates`;
- empty collections do not create example papers;
- desktop and mobile DOM order matches the approved information architecture.

**Step 2: Run RED**

```bash
pnpm --dir apps/web exec vitest run tests/home.test.ts tests/components.test.ts tests/visual-foundation.contract.test.ts --config vitest.config.ts
```

Expected: FAIL because the current Home is search/hero-led and the layout includes `DiscoveryRail`.

**Step 3: Implement the compact dashboard**

Desktop:

- left 8 columns: formal publications today;
- right 4 columns: recent acceptances and online first;
- lower sections: seven-day trends, journal activity, subject activity.

Mobile:

- one column in the approved order.

Keep only compact data status. Do not surface raw generation IDs in the main visual hierarchy.

**Step 4: Run GREEN**

```bash
pnpm --dir apps/web test
pnpm --dir apps/web typecheck
pnpm --dir apps/web lint
pnpm --dir apps/web build
```

Expected: PASS.

**Step 5: Browser verification**

Run Playwright and `agent-browser`/gstack browse at:

```text
1440x1000
768x1024
390x844
```

Verify no horizontal overflow, no console errors, correct focus order, and correct empty/error states.

**Step 6: Commit**

```bash
git add apps/web
git commit -m "feat(web): make home a daily publication dashboard"
```

## Task 7: Full verification, documentation, review, and push

**Files:**

- Modify if needed: `README.md`
- Modify if needed: `.env.example`
- Modify if needed: `docker-compose.yml`
- Modify if needed: `docker-compose.test.yml`

**Step 1: Run backend verification**

```bash
go -C services/core test ./... -count=1
go -C services/core test -race -p 1 ./... -count=1
go -C services/core build ./...
```

Expected: PASS.

**Step 2: Run frontend verification**

```bash
pnpm --dir apps/web check:api-types
pnpm --dir apps/web test
pnpm --dir apps/web typecheck
pnpm --dir apps/web lint
pnpm --dir apps/web build
pnpm --dir apps/web test:e2e
```

Expected: PASS.

**Step 3: Run Compose smoke**

```bash
docker compose config --quiet
docker compose -f docker-compose.test.yml up --build --abort-on-container-exit --exit-code-from test
```

Expected: PASS.

**Step 4: Review**

- Run a specification-compliance review;
- run a separate code-quality review;
- resolve all findings;
- rerun affected tests;
- run `git diff --check`.

**Step 5: Commit and push**

```bash
git add README.md .env.example docker-compose.yml docker-compose.test.yml
git commit -m "docs: document daily publication intelligence"
git push origin codex/go-nuxt-replatform
```

