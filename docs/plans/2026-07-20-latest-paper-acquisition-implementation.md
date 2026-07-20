# Latest Paper Acquisition Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace per-journal PubMed daily probing with deterministic batched daily acquisition over every resolved journal ISSN, while preserving the existing backfill path.

**Architecture:** Registry parsing continues to validate every status and identifier, but daily eligibility depends only on a resolved exact ISSN identity. The daily planner packs whole journal identities into stable batches of at most 2048 unique ISSN terms, emits EDAT and MDAT windows, and runs each window through PubMed ESearch POST plus EFetch. Backfill remains journal-scoped and unchanged.

**Tech Stack:** Go 1.26, NCBI E-utilities, existing ingestion service, PostgreSQL, Go tests/race/vet.

---

### Task 1: Add a resolved-only daily Registry view

**Files:**
- Modify: `services/core/internal/pubmedsync/registry.go`
- Modify: `services/core/internal/pubmedsync/registry_test.go`

**Step 1: Write the failing test**

Keep the existing yes-only backfill test. Add a daily-view test that supplies three resolved rows:

```go
resolved + pubmed yes
resolved + pubmed no
resolved + pubmed unknown
```

Assert that:

- existing `LoadRegistry` still returns only `resolved + pubmed yes`;
- new `LoadResolvedRegistry` returns all three resolved rows;
- ambiguous and unresolved rows remain excluded from both.

Keep the existing status/count contradiction tests unchanged.

**Step 2: Run the focused test and verify RED**

```bash
go -C services/core test ./internal/pubmedsync \
  -run 'TestLoadResolvedRegistryReturnsAllResolvedRowsRegardlessOfHistoricalPubMedCoverage' \
  -count=1
```

Expected: FAIL because `LoadResolvedRegistry` does not exist.

**Step 3: Implement the minimal eligibility change**

Refactor the internal parser to accept an explicit view predicate while retaining every validation.
Expose:

```go
func LoadRegistry(source io.Reader) ([]Journal, error)
func LoadResolvedRegistry(source io.Reader) ([]Journal, error)
```

`LoadRegistry` keeps its current `resolved + pubmed_supported=yes` behavior for backfill.
`LoadResolvedRegistry` accepts every resolved row for daily mode. Do not weaken ISSN
validation, status/count consistency, exact-set folding, or intersection conflicts.

**Step 4: Run registry tests and verify GREEN**

```bash
go -C services/core test ./internal/pubmedsync \
  -run 'TestLoadRegistry|TestJournal' -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/pubmedsync/registry.go \
  services/core/internal/pubmedsync/registry_test.go
git commit -m "fix(pubmed): expose resolved daily registry"
```

### Task 2: Send PubMed ESearch through POST

**Files:**
- Modify: `services/core/internal/source/pubmed/client.go`
- Modify: `services/core/internal/source/pubmed/client_test.go`

**Step 1: Write the failing transport test**

Add a client test whose server asserts:

```text
method = POST
Content-Type = application/x-www-form-urlencoded
query string contains only tool/email/api_key identity
form body contains db, term, datetype, mindate, maxdate, usehistory
```

Return a valid ESearch history payload and assert `Search` succeeds.

**Step 2: Run the focused test and verify RED**

```bash
go -C services/core test ./internal/source/pubmed \
  -run 'TestSearchUsesFormPOSTForLongJournalQueries' -count=1
```

Expected: FAIL because `Search` currently sends GET.

**Step 3: Implement the minimal POST helper**

Add a request helper that keeps the small NCBI identity parameters in the URL so the
existing HTTP policy can extract and redact sensitive query values, while posting the
potentially large search form:

```go
identity := url.Values{}
client.addIdentity(identity)
body := strings.NewReader(searchValues.Encode())
request, err := http.NewRequestWithContext(
    ctx,
    http.MethodPost,
    endpointWithIdentity.String(),
    body,
)
request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
```

Use it only from `Search`. Keep `CountCoverage` on the existing GET path. Do not duplicate
identity parameters in the form body. Ensure the request body is replayable for the existing
retry policy.

**Step 4: Verify retry and redaction behavior**

Run:

```bash
go -C services/core test ./internal/source/pubmed \
  -run 'TestSearch|TestClient' -count=1
```

Expected: PASS, including 429/5xx retry and API-key redaction tests.

**Step 5: Commit**

```bash
git add services/core/internal/source/pubmed/client.go \
  services/core/internal/source/pubmed/client_test.go
git commit -m "feat(pubmed): post history searches"
```

### Task 3: Add deterministic whole-journal batching

**Files:**
- Create: `services/core/internal/pubmedsync/daily.go`
- Create: `services/core/internal/pubmedsync/daily_test.go`
- Modify: `services/core/internal/pubmedsync/planner.go`

**Step 1: Write failing batch-planner tests**

Cover these exact behaviors:

1. Journals remain in Registry order.
2. Every ISSN belonging to one journal stays in one batch.
3. A batch contains at most 2048 unique ISSNs.
4. Exact duplicate journal identities are already folded by `LoadRegistry`.
5. A single journal with more than 2048 unique ISSNs fails explicitly.
6. Repeated planning produces byte-identical batch keys and window keys.
7. Each batch emits EDAT then MDAT for `[D-2,D]`.

Use generated valid ISSNs rather than invalid placeholder strings.

**Step 2: Run the new tests and verify RED**

```bash
go -C services/core test ./internal/pubmedsync \
  -run 'TestPlanDailyBatches|TestBuildDailyJournalBatches' -count=1
```

Expected: FAIL because the batch types and planner do not exist.

**Step 3: Implement minimal batch types**

Add:

```go
const MaxDailyISSNTerms = 2048

type DailyJournalBatch struct {
    key      string
    journals []Journal
    issns    []string
}

type DailyBatchWindow struct {
    batch      DailyJournalBatch
    dateType   pubmed.DateType
    dateWindow pubmed.DateWindow
    key        string
}
```

Pack journals greedily in stable Registry order. Before appending a journal, count only
new unique ISSNs. Flush the current batch when the next whole journal would exceed 2048.

Generalize the existing window-key helper to hash an exact ISSN slice while preserving
the current payload version:

```json
{
  "version": 1,
  "issns": ["..."],
  "date_type": "entrez",
  "from": "2026-07-18",
  "to": "2026-07-20"
}
```

The existing singleton journal wrapper must produce byte-identical backfill keys.

**Step 4: Run the planner tests and verify GREEN**

```bash
go -C services/core test ./internal/pubmedsync \
  -run 'TestPlanDailyBatches|TestBuildDailyJournalBatches|TestPlanBackfill' \
  -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/pubmedsync/daily.go \
  services/core/internal/pubmedsync/daily_test.go \
  services/core/internal/pubmedsync/planner.go
git commit -m "feat(pubmed): batch daily journal searches"
```

### Task 4: Run daily ingestion once per batch window

**Files:**
- Modify: `services/core/internal/pubmedsync/service.go`
- Modify: `services/core/internal/pubmedsync/service_test.go`

**Step 1: Write the failing service test**

Build a Registry that produces two batches. Run daily mode and assert:

```text
eligible_journals = total resolved journals
journals_planned = total resolved journals
windows_planned = 4
Search calls = 4
order = batch1 EDAT, batch1 MDAT, batch2 EDAT, batch2 MDAT
each query contains the complete batch ISSN set
```

Assert backfill still performs its existing count/search sequence per journal.

**Step 2: Run the focused test and verify RED**

```bash
go -C services/core test ./internal/pubmedsync \
  -run 'TestServiceDailyBatchesResolvedJournals' -count=1
```

Expected: FAIL because the service currently loops over journals.

**Step 3: Add a daily-specific service path**

At the start of `Service.Run`, select the Registry view explicitly:

```go
switch request.Mode {
case ModeBackfill:
    journals, err = LoadRegistry(bytes.NewReader(registryBytes))
case ModeDaily:
    journals, err = LoadResolvedRegistry(bytes.NewReader(registryBytes))
}
```

Keep the existing journal loop for backfill.

`runDaily` must:

1. initialize one `JournalReport` per resolved journal;
2. plan deterministic batches;
3. run two windows per batch;
4. mark every journal in a failed batch explicitly failed;
5. preserve summary totals from each ingestion window;
6. return non-zero error after processing remaining independent batches.

Refactor `runWindow` into a generic helper accepting exact ISSNs, date type, date window,
window key, and job payload. Daily job payloads contain:

```text
batch_key
batch_index
journal_count
journal_keys
issns
date_type
from_date
to_date
run_date
```

Backfill payloads remain unchanged.

**Step 4: Verify service tests**

```bash
go -C services/core test ./internal/pubmedsync -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/pubmedsync/service.go \
  services/core/internal/pubmedsync/service_test.go
git commit -m "feat(pubmed): ingest daily batch windows"
```

### Task 5: Enforce returned-record ISSN scope

**Files:**
- Modify: `services/core/internal/pubmedsync/service.go`
- Modify: `services/core/internal/pubmedsync/service_test.go`

**Step 1: Write failing record-scope tests**

Create fetched records for:

1. Venue ISSN intersects the requested batch: accepted.
2. Venue ISSN-L intersects: accepted.
3. Venue is missing: explicit error.
4. Venue has only unrelated ISSNs: explicit error.

The service must not silently discard an unexpected PubMed result.

**Step 2: Run and verify RED**

```bash
go -C services/core test ./internal/pubmedsync \
  -run 'TestServiceRejectsFetchedRecordOutsideRequestedISSNs' -count=1
```

Expected: FAIL because fetched records currently flow directly to ingestion.

**Step 3: Add exact record filtering**

Wrap the PubMed `source.ClientSequence` before `recordEvents`. Build an exact set from the
requested ISSNs and require intersection with:

```text
record.Venue.ISSNL
record.Venue.ISSN
record.Venue.ISSNDetails[].Value
```

Yield a descriptive error containing PMID and the requested window key when the assertion
is absent or disjoint.

**Step 4: Verify GREEN**

```bash
go -C services/core test ./internal/pubmedsync -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/pubmedsync/service.go \
  services/core/internal/pubmedsync/service_test.go
git commit -m "fix(pubmed): verify fetched journal identity"
```

### Task 6: Remove coverage audit from the daily critical path

**Files:**
- Modify: `services/core/cmd/journal-pubmed-registry/main.go`
- Modify: `services/core/cmd/journal-pubmed-registry/main_test.go`
- Modify: `docs/runbooks/pubmed-journal-daily-sync.md`
- Modify: `data/venues/README.md`

**Step 1: Write the failing CLI test**

Add an explicit optional flag:

```text
--audit-pubmed-coverage
```

Assert the default command:

- does not require NCBI configuration;
- does not construct a PubMed counter;
- writes resolved rows with `pubmed_supported=unknown`;
- records zero attempted probes.

Assert the explicit audit flag preserves the existing coverage behavior.

**Step 2: Run and verify RED**

```bash
go -C services/core test ./cmd/journal-pubmed-registry \
  -run 'TestJournalPubMedRegistrySkipsCoverageAuditByDefault' -count=1
```

Expected: FAIL because coverage probing is currently mandatory.

**Step 3: Implement the explicit audit switch**

Add `AuditPubMedCoverage bool` to `registryCommand`. Without the flag, call a deterministic
overlay that validates rows and emits unknown, unattempted receipts without network access.
With the flag, retain `ProbePubMedCoverage`.

Do not delete the coverage audit capability and do not silently reinterpret old yes/no data.

**Step 4: Verify command tests**

```bash
go -C services/core test ./cmd/journal-pubmed-registry \
  ./internal/venueenrich -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/cmd/journal-pubmed-registry \
  docs/runbooks/pubmed-journal-daily-sync.md \
  data/venues/README.md
git commit -m "fix(pubmed): make coverage audit optional"
```

### Task 7: Regenerate Registry and run full verification

**Files:**
- Regenerate: `data/venues/journal-pubmed-registry.v1.csv`
- Regenerate: `data/venues/journal-pubmed-registry.v1.report.json`

**Step 1: Run all focused tests**

```bash
go -C services/core test \
  ./internal/source/httpclient \
  ./internal/source/pubmed \
  ./internal/venueenrich \
  ./internal/pubmedsync \
  ./cmd/journal-pubmed-registry \
  ./cmd/worker -count=1
```

Expected: PASS.

**Step 2: Run race and vet**

```bash
go -C services/core test -race \
  ./internal/source/httpclient \
  ./internal/source/pubmed \
  ./internal/venueenrich \
  ./internal/pubmedsync -count=1

go -C services/core vet \
  ./internal/source/httpclient \
  ./internal/source/pubmed \
  ./internal/venueenrich \
  ./internal/pubmedsync \
  ./cmd/journal-pubmed-registry \
  ./cmd/worker
```

Expected: PASS.

**Step 3: Regenerate without PubMed coverage audit**

Use the three authorized input paths and the existing Crossref cache. Do not pass
`--audit-pubmed-coverage`.

Expected:

```text
rows=2032
resolved=1674
ambiguous=44
unresolved=314
PubMed unknown=2032
attempted probes=0
```

**Step 4: Run a real read-only PubMed batch search**

For `run-date=2026-07-20`, POST both planned EDAT batches with `retmax=0`. Verify:

- both requests return valid `count`, `WebEnv`, and `QueryKey`;
- no batch exceeds 2048 unique ISSNs;
- no Boolean-operator error occurs.

Do not run the three-year backfill in this task.

**Step 5: Run final hygiene checks**

```bash
git diff --check
git status --short
```

Review every generated artifact and commit only after counts and hashes are consistent.
