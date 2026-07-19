# Journal API Registry Enrichment Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Deterministically enrich the 2032 source journal rows with ISSNs, Crossref identity, OpenAlex Source identity, PubMed coverage, and an auditable API acquisition strategy.

**Architecture:** A Go command reads the three source CSVs, consumes a locally cached Crossref journal catalog, resolves titles without fuzzy matching, optionally probes OpenAlex and PubMed with bounded network policies, and writes a stable CSV plus a machine-readable validation report. Raw network captures remain outside Git; the committed outputs contain provenance hashes and explicit unresolved states.

**Tech Stack:** Go 1.26, standard `encoding/csv` and `encoding/json`, `golang.org/x/text/unicode/norm`, Crossref REST, OpenAlex Sources API, PubMed E-utilities.

---

### Task 1: Add the registry schema and deterministic title normalizer

**Files:**
- Create: `services/core/internal/venueenrich/model.go`
- Create: `services/core/internal/venueenrich/model_test.go`
- Create: `services/core/internal/venueenrich/normalize.go`
- Create: `services/core/internal/venueenrich/normalize_test.go`

**Step 1: Write failing tests**

Cover:

- source rows preserve domain, order, title and source URL;
- output rows reject invalid ISSNs and invalid match states;
- NFKC, case folding and whitespace folding are deterministic;
- punctuation is preserved;
- hyphen replacement, punctuation deletion and approximate titles do not match.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/venueenrich -run 'TestNormalize|TestRegistryRow' -count=1
```

Expected: FAIL because the package does not exist.

**Step 3: Implement the minimal types and normalizer**

Use `norm.NFKC.String`, Unicode lower/case folding and `unicode.IsSpace`. Do not use edit distance,
token similarity or punctuation stripping.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/venueenrich -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/venueenrich
git commit -m "feat(venue): add deterministic journal enrichment model"
```

### Task 2: Load source CSVs and preserve all 2032 rows

**Files:**
- Create: `services/core/internal/venueenrich/input.go`
- Create: `services/core/internal/venueenrich/input_test.go`
- Create: `services/core/internal/venueenrich/testdata/source.csv`

**Step 1: Write failing tests**

Test exact headers, multiline JCR values, numeric source order, duplicate source keys, invalid rows,
and stable concatenation of multiple input files.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/venueenrich -run TestLoadSource -count=1
```

Expected: FAIL.

**Step 3: Implement strict CSV loading**

Reject missing fields and duplicate `(domain, source_order)` keys. Preserve journal names byte-for-byte
after trimming only field boundaries.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/venueenrich -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/venueenrich
git commit -m "feat(venue): load journal candidate CSVs"
```

### Task 3: Fetch and cache the complete Crossref journal catalog

**Files:**
- Create: `services/core/internal/venueenrich/crossref_catalog.go`
- Create: `services/core/internal/venueenrich/crossref_catalog_test.go`
- Create: `services/core/internal/venueenrich/testdata/crossref-journals-page-1.json`
- Create: `services/core/internal/venueenrich/testdata/crossref-journals-page-2.json`

**Step 1: Write failing HTTP tests**

Verify:

- `rows=1000` and `cursor=*`;
- every page uses the returned `next-cursor`;
- repeated cursors fail;
- malformed items fail explicitly;
- rate limit, timeout and maximum response size are bounded;
- raw page bytes, page SHA-256 and final catalog SHA-256 are recorded;
- an interrupted fetch can resume only from a verified cache manifest.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/venueenrich -run TestCrossrefCatalog -count=1
```

Expected: FAIL.

**Step 3: Implement catalog fetching**

Reuse `internal/source/httpclient`. Write JSONL and a manifest to a caller-provided cache directory.
Never commit the 168k-record raw catalog.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/venueenrich -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/venueenrich
git commit -m "feat(venue): cache Crossref journal catalog"
```

### Task 4: Resolve unique Crossref journal identities

**Files:**
- Create: `services/core/internal/venueenrich/match.go`
- Create: `services/core/internal/venueenrich/match_test.go`

**Step 1: Write failing matching tests**

Cover:

- one normalized title and one ISSN set resolves;
- duplicate catalog rows with the same ISSN set collapse;
- same title with different ISSN sets is ambiguous;
- punctuation differences remain unresolved;
- no match remains unresolved;
- input order is preserved.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/venueenrich -run TestMatch -count=1
```

Expected: FAIL.

**Step 3: Implement deterministic matching**

Index the cached catalog by the approved normalized title. Do not issue Crossref fuzzy search queries
and do not auto-resolve ambiguous titles.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/venueenrich -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/venueenrich
git commit -m "feat(venue): resolve exact Crossref journal identities"
```

### Task 5: Add OpenAlex ISSN corroboration and PubMed coverage probes

**Files:**
- Create: `services/core/internal/venueenrich/openalex.go`
- Create: `services/core/internal/venueenrich/openalex_test.go`
- Create: `services/core/internal/venueenrich/pubmed.go`
- Create: `services/core/internal/venueenrich/pubmed_test.go`

**Step 1: Write failing HTTP tests**

Verify:

- OpenAlex uses only `/sources/issn:{ISSN}`;
- conflicting Source IDs make a row ambiguous;
- OpenAlex network failure produces `unknown`, not `no`;
- PubMed combines known ISSNs with an exact OR query;
- PubMed uses `retmax=0`, `tool`, `email`, optional API key and bounded rate;
- successful zero count is `no`; transport failure is `unknown`.

**Step 2: Run RED**

```bash
go -C services/core test ./internal/venueenrich -run 'TestOpenAlex|TestPubMed' -count=1
```

Expected: FAIL.

**Step 3: Implement bounded probes**

Reuse the shared HTTP policy. Persist probe response hashes in the report. Never log API keys.

**Step 4: Run GREEN**

```bash
go -C services/core test ./internal/venueenrich -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/internal/venueenrich
git commit -m "feat(venue): probe journal API coverage"
```

### Task 6: Add the enrichment command and stable outputs

**Files:**
- Create: `services/core/cmd/journal-registry-enrich/main.go`
- Create: `services/core/cmd/journal-registry-enrich/main_test.go`
- Create: `data/venues/journal-api-registry.v1.csv`
- Create: `data/venues/journal-api-registry.v1.report.json`
- Modify: `data/venues/README.md`

**Step 1: Write failing command tests**

Test:

- exactly three input files are accepted;
- cache directory, output CSV and report paths are explicit;
- live probes can be disabled for deterministic replay;
- output field order is fixed;
- output contains exactly the input row count;
- report totals reconcile with the CSV;
- any partial run exits nonzero and leaves no final output files.

**Step 2: Run RED**

```bash
go -C services/core test ./cmd/journal-registry-enrich -count=1
```

Expected: FAIL.

**Step 3: Implement the command**

Write temporary files and atomically rename only after all required stages succeed.

**Step 4: Run GREEN**

```bash
go -C services/core test ./cmd/journal-registry-enrich ./internal/venueenrich -count=1
```

Expected: PASS.

**Step 5: Commit**

```bash
git add services/core/cmd/journal-registry-enrich data/venues
git commit -m "feat(venue): generate journal API registry"
```

### Task 7: Run the live 2032-row enrichment and verify the artifact

**Files:**
- Modify: `data/venues/journal-api-registry.v1.csv`
- Modify: `data/venues/journal-api-registry.v1.report.json`

**Step 1: Run the live command**

```bash
go -C services/core run ./cmd/journal-registry-enrich \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-medicine-wechat.csv \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-biology-wechat.csv \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-computer-science-wechat.csv \
  --cache-dir /Users/huangrende/Documents/论文自媒体/期刊名单/enrichment-cache \
  --output data/venues/journal-api-registry.v1.csv \
  --report data/venues/journal-api-registry.v1.report.json
```

Expected: exit 0 with 2032 rows and no silent probe failures.

**Step 2: Replay from cache**

```bash
go -C services/core run ./cmd/journal-registry-enrich \
  --offline \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-medicine-wechat.csv \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-biology-wechat.csv \
  --input /Users/huangrende/Documents/论文自媒体/期刊名单/2026-jcr-q1-computer-science-wechat.csv \
  --cache-dir /Users/huangrende/Documents/论文自媒体/期刊名单/enrichment-cache \
  --output /tmp/journal-api-registry-replay.csv \
  --report /tmp/journal-api-registry-replay.report.json
```

Expected: CSV and report hashes equal the live outputs except for explicitly excluded observation timestamps.

**Step 3: Run verification**

```bash
go -C services/core test ./internal/venueenrich ./cmd/journal-registry-enrich -count=1
go -C services/core test ./internal/source/crossref ./internal/source/pubmed ./internal/source/openalex -count=1
git diff --check
```

Expected: PASS.

**Step 4: Review unresolved and ambiguous rows**

Do not manually promote them. Report exact counts and evidence gaps.

**Step 5: Commit**

```bash
git add data/venues/journal-api-registry.v1.csv \
  data/venues/journal-api-registry.v1.report.json
git commit -m "data(venue): add 2032-row journal API registry"
```

