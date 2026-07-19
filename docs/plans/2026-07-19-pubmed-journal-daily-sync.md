# PubMed Journal Daily Sync Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Generate a 2032-row PubMed coverage registry, backfill three years of covered journal papers, and provide one idempotent daily three-day-overlap sync command.

**Architecture:** Reuse the existing PubMed ESearch/EFetch client, parser, ingestion service, PostgreSQL projections, and worker command. Add PMID canonical identity fallback, explicit PubMed date modes and coverage counting, a deterministic journal-window planner, and one registry-level orchestration command. The existing exact Crossref resolver is reused only to obtain ISSNs; no additional source platforms are added.

**Tech Stack:** Go 1.26, PostgreSQL, NCBI E-utilities, existing `internal/source/pubmed`, `internal/ingestion`, `cmd/worker`, `internal/venueenrich`.

---

### Task 1: Allow DOI-less PubMed records to use PMID identity

**Files:**
- Modify: `services/core/internal/paper/identifier.go`
- Modify: `services/core/internal/paper/identifier_test.go`
- Modify: `services/core/internal/source/pubmed/parse.go`
- Modify: `services/core/internal/source/pubmed/parse_test.go`
- Create: `services/core/migrations/000028_pmid_canonical_identity.sql`
- Modify: `services/core/internal/database/migrate_test.go`

**Step 1: Write failing tests**

Cover:

- canonical PMID syntax is digits only and normalized without whitespace;
- DOI remains preferred when DOI and PMID both exist;
- PMID is selected when a PubMed record has no DOI;
- malformed PMID cannot become a canonical identity;
- the database accepts `pmid:<value>` in `works.canonical_key`.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/paper ./internal/source/pubmed -run 'Test.*PMID|TestParse.*WithoutDOI' -count=1
```

Expected: FAIL because `paper.SchemePMID` and PubMed PMID fallback do not exist.

**Step 3: Implement the minimal identity change**

Add `SchemePMID`, strict PMID normalization, and place PMID after DOI in canonical priority. In the PubMed parser, always construct the PMID identifier and use it only when DOI identity is absent. Add a migration replacing the `works_canonical_key_check` constraint with one that includes `pmid`.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/paper ./internal/source/pubmed -count=1
go -C services/core test ./internal/database -run TestMigrate -count=1
```

Expected: package tests pass; database migration test may require the existing PostgreSQL test environment.

**Step 5: Commit**

```bash
git add services/core/internal/paper \
  services/core/internal/source/pubmed \
  services/core/migrations/000028_pmid_canonical_identity.sql \
  services/core/internal/database/migrate_test.go
git commit -m "feat(pubmed): retain DOI-less records by PMID"
```

### Task 2: Add explicit PubMed date modes and exact coverage counts

**Files:**
- Modify: `services/core/internal/source/pubmed/query.go`
- Modify: `services/core/internal/source/pubmed/query_test.go`
- Modify: `services/core/internal/source/pubmed/client.go`
- Modify: `services/core/internal/source/pubmed/client_test.go`

**Step 1: Write failing tests**

Cover:

- `publication`, `entrez`, and `modification` date modes encode as `pdat`, `edat`, and `mdat`;
- unsupported or empty date mode fails;
- exact ISSN coverage count uses ESearch with `retmax=0`, no history server, and no date window;
- count greater than zero, zero, malformed count, network failure, response overflow, duplicate JSON keys, and trailing JSON values;
- API key remains redacted and the existing 3/10 requests-per-second policy remains intact.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/source/pubmed -run 'TestSearchQuery.*DateType|TestClientCountCoverage' -count=1
```

Expected: FAIL because date modes and coverage count do not exist.

**Step 3: Implement minimal APIs**

Add:

```go
type DateType string

const (
    DateTypePublication  DateType = "publication"
    DateTypeEntrez       DateType = "entrez"
    DateTypeModification DateType = "modification"
)

type CoverageQuery struct {
    JournalISSNs []string
}

type CoverageResult struct {
    Count          int64
    ResponseSHA256 string
}
```

`SearchQuery` receives an explicit `DateType`. `Client.CountCoverage` performs one exact ESearch count request and returns a verified count plus response hash.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/source/pubmed -count=1
go -C services/core test -race ./internal/source/pubmed -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/source/pubmed
git commit -m "feat(pubmed): add coverage and date-mode queries"
```

### Task 3: Generate the 2032-row PubMed journal registry

**Files:**
- Create: `services/core/internal/venueenrich/pubmed.go`
- Create: `services/core/internal/venueenrich/pubmed_test.go`
- Create: `services/core/cmd/journal-pubmed-registry/main.go`
- Create: `services/core/cmd/journal-pubmed-registry/main_test.go`
- Create: `data/venues/journal-pubmed-registry.v1.csv`
- Create: `data/venues/journal-pubmed-registry.v1.report.json`
- Modify: `data/venues/README.md`

**Step 1: Write failing tests**

Cover:

- only resolved rows with valid ISSNs are probed;
- all known ISSNs for one journal form one exact OR query;
- positive count is `yes`, zero is `no`, request failure is `unknown`;
- ambiguous and unresolved rows are preserved without requests;
- source row order is unchanged;
- CSV header and report schema are fixed;
- output row count and report totals reconcile;
- temporary output is renamed only after both files are complete.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/venueenrich ./cmd/journal-pubmed-registry -run 'TestPubMed|TestJournalPubMedRegistry' -count=1
```

Expected: FAIL because the registry probe and command do not exist.

**Step 3: Implement the registry command**

Inputs:

- exactly three source CSV files;
- existing Crossref catalog cache directory;
- explicit output CSV and report paths;
- NCBI tool, email, optional API key from configuration/environment.

The command loads source rows, reuses `MatchCrossrefCatalog`, probes PubMed coverage sequentially under the shared HTTP rate policy, and writes stable CSV/report artifacts.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/venueenrich ./cmd/journal-pubmed-registry -count=1
go -C services/core test -race ./internal/venueenrich ./cmd/journal-pubmed-registry -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/venueenrich/pubmed.go \
  services/core/internal/venueenrich/pubmed_test.go \
  services/core/cmd/journal-pubmed-registry \
  data/venues
git commit -m "feat(pubmed): generate journal coverage registry"
```

### Task 4: Plan deterministic backfill and daily windows

**Files:**
- Create: `services/core/internal/pubmedsync/planner.go`
- Create: `services/core/internal/pubmedsync/planner_test.go`
- Create: `services/core/internal/pubmedsync/registry.go`
- Create: `services/core/internal/pubmedsync/registry_test.go`

**Step 1: Write failing tests**

Cover:

- registry loader accepts only the fixed registry header;
- only `resolved + pubmed_supported=yes` rows become sync journals;
- duplicate journal identities with the same ISSN set collapse deterministically;
- conflicting identities fail;
- backfill range is exactly 2023-07-19 through 2026-07-19;
- a range below 10,000 records remains one window;
- a range at or above 10,000 splits into natural years, then natural months;
- a month still at or above 10,000 fails explicitly;
- daily mode emits one EDAT and one MDAT window covering run day and previous two days.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/pubmedsync -count=1
```

Expected: FAIL because the package does not exist.

**Step 3: Implement the planner**

Use a count dependency:

```go
type Counter interface {
    Count(context.Context, Journal, pubmed.DateType, DateWindow) (int64, error)
}
```

The planner returns immutable `SyncWindow` values with journal identity, date type, inclusive dates, and deterministic keys.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/pubmedsync -count=1
go -C services/core test -race ./internal/pubmedsync -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/pubmedsync
git commit -m "feat(pubmed): plan journal sync windows"
```

### Task 5: Add registry-level PubMed orchestration

**Files:**
- Create: `services/core/internal/pubmedsync/service.go`
- Create: `services/core/internal/pubmedsync/service_test.go`
- Modify: `services/core/cmd/worker/main.go`
- Modify: `services/core/cmd/worker/main_test.go`

**Step 1: Write failing tests**

Cover:

- `sync pubmed-journals --mode backfill` requires registry path and uses the fixed three-year range;
- `sync pubmed-journals --mode daily` derives the three-day window from an explicit run date;
- each journal/window invokes the existing PubMed ESearch/EFetch and ingestion flow;
- idempotency key includes journal ISSN set, date type, and inclusive dates;
- a failed journal is reported while later journals continue;
- any failure causes nonzero command exit after the full report is emitted;
- repeated successful execution reuses/upserts existing PMID records.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/pubmedsync ./cmd/worker -run 'Test.*PubMedJournals' -count=1
```

Expected: FAIL because the orchestration mode does not exist.

**Step 3: Implement the service and worker command**

Add command forms:

```bash
paper-hub-worker sync pubmed-journals \
  --mode backfill \
  --registry /path/to/journal-pubmed-registry.v1.csv

paper-hub-worker sync pubmed-journals \
  --mode daily \
  --run-date 2026-07-19 \
  --lookback-days 3 \
  --registry /path/to/journal-pubmed-registry.v1.csv
```

The orchestrator processes windows sequentially. It uses the existing configured PubMed rate limit and ingestion repositories; it does not add an internal scheduler.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/pubmedsync ./cmd/worker -count=1
go -C services/core test -race ./internal/pubmedsync ./cmd/worker -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/pubmedsync services/core/cmd/worker
git commit -m "feat(pubmed): orchestrate journal backfill and daily sync"
```

### Task 6: Document daily deployment and verify the full first phase

**Files:**
- Create: `docs/runbooks/pubmed-journal-daily-sync.md`
- Modify: `data/venues/journal-pubmed-registry.v1.csv`
- Modify: `data/venues/journal-pubmed-registry.v1.report.json`

**Step 1: Generate the live registry**

```bash
go -C services/core run ./cmd/journal-pubmed-registry \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-medicine-wechat.csv \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-biology-wechat.csv \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-computer-science-wechat.csv \
  --cache-dir /Users/huangrende/Documents/论文自媒体/期刊名单/enrichment-cache \
  --output data/venues/journal-pubmed-registry.v1.csv \
  --report data/venues/journal-pubmed-registry.v1.report.json
```

Expected: exit 0, exactly 2032 data rows, explicit status for every row.

**Step 2: Run the three-year backfill**

```bash
go -C services/core run ./cmd/worker -- sync pubmed-journals \
  --mode backfill \
  --registry data/venues/journal-pubmed-registry.v1.csv
```

Expected: all supported journals are attempted; failures are itemized.

**Step 3: Replay one daily run twice**

```bash
go -C services/core run ./cmd/worker -- sync pubmed-journals \
  --mode daily \
  --run-date 2026-07-19 \
  --lookback-days 3 \
  --registry data/venues/journal-pubmed-registry.v1.csv
```

Expected: second run creates no duplicate PMID work.

**Step 4: Write the runbook**

Document environment variables, the daily command, example cron/Scheduler invocation, overlap semantics, report interpretation, and retry procedure.

**Step 5: Run final verification**

```bash
go -C services/core test ./internal/paper ./internal/source/pubmed \
  ./internal/venueenrich ./internal/pubmedsync ./cmd/journal-pubmed-registry \
  ./cmd/worker -count=1
go -C services/core test -race ./internal/source/pubmed \
  ./internal/venueenrich ./internal/pubmedsync -count=1
go -C services/core vet ./internal/paper ./internal/source/pubmed \
  ./internal/venueenrich ./internal/pubmedsync ./cmd/journal-pubmed-registry \
  ./cmd/worker
git diff --check
```

Expected: PASS except any pre-existing PostgreSQL testcontainer environment block, which must be reported exactly.

**Step 6: Commit**

```bash
git add docs/runbooks/pubmed-journal-daily-sync.md \
  data/venues/journal-pubmed-registry.v1.csv \
  data/venues/journal-pubmed-registry.v1.report.json
git commit -m "data(pubmed): add journal coverage and daily sync runbook"
```

