# Venue metric fixtures

`jcr-q1.example.csv` is a fully synthetic schema and policy fixture. Every
title, category, identifier, metric, and source label is invented for this
repository. The `0000-*` ISSNs are checksum-valid synthetic identifiers used
only by tests; they are not claims about registered publications.

Production JCR data must come from a user-authorized CSV export supplied
explicitly through `JCR_IMPORT_PATH`. The importer never substitutes OpenAlex
`2yr_mean_citedness`, title similarity, or another inferred citation metric
for JIF.

## Required columns

Every file must contain these case-sensitive columns:

```text
edition_year,metric_year,category,quartile,jif,jif_rank,
category_journal_count,jif_percentile,issn,eissn,issn_l,source,status
```

`title` is optional alias evidence. It is never sent to the venue identity
repository and cannot resolve a Venue. Extra controlled-source columns may be
carried by an authorized export, but Venue matching remains exact
ISSN-L/print-ISSN/eISSN matching.

All data rows must have the same width as the header. A Registry v2 `known`
row requires edition year, a nonnegative decimal JIF, positive rank and
category journal count, rank not exceeding count, an exact JIF percentile in
`[0, 100]`, and one of `Q1` through `Q4`. An `unknown` row leaves every metric
evidence field blank while retaining metric year, category, all three ISSN
roles, source, and status.

Legacy fixtures without the four Registry v2 columns remain readable only for
deterministic replay. They are stored as `legacy/v1` evidence and cannot
satisfy the current `journal-all-q1/v2` admission policy.

## Persistence contract

The Go importer reads through a `JCRRepository` and writes one atomic
`JCRImport` through a `JCRSink`. Migration `000003_jcr_import_receipts`
extends the existing Venue schema with immutable import receipts and
receipt-to-alias provenance; `PostgresJCRStore` persists against that schema
without introducing a parallel database model. Persistence implementations
must:

- resolve exactly one pre-existing Venue from the three role-labelled ISSNs;
- append `(venue_id, metric_year, category)` snapshots without overwriting
  historical years;
- treat normalized identical rows and identical file SHA-256 values as
  idempotent;
- reject conflicting rows explicitly;
- atomically record source, input SHA-256, import timestamp, row counts,
  metric receipt links, and imported alias evidence.

The PostgreSQL store requires an explicit `SourceLicense` configuration. The
fixture and schema make no implicit claim about production JCR licensing.

## Decimal representation

JIF and JIF percentile use arbitrary-precision decimal values represented as a
canonical digit coefficient plus decimal scale. They are retained as evidence;
only an exact `Q1` Quartile admits a journal.

## Covered synthetic outcomes

The fixture has five rows covering:

- a high-JIF Q2 category that is rejected by itself;
- a second category for the same journal that is Q1;
- a multi-category journal admitted only by its Q1 category;
- an explicit `unknown` metric row;
- a known `9.999`, Q2 row that evaluates to `rejected`;
- a checksum-valid known row with exact decimal JIF `0`.

## PubMed journal coverage registry

`journal-pubmed-registry` generates the schema-v1 PubMed coverage registry from
exactly three explicitly supplied source CSV files. The production run requires
their combined logical row count, as parsed by `LoadSourceFiles`, to equal
`2032`; it does not derive counts from physical lines.

Required flags:

```text
--input <path>       repeat exactly three times, in source order
--cache-dir <path>   Crossref journal catalog cache
--output <path>      destination CSV
--report <path>      destination JSON report
```

Required environment:

```text
NCBI_TOOL
NCBI_EMAIL
```

`NCBI_EMAIL` must be a bare email address. `NCBI_API_KEY` is optional.
`CROSSREF_CONTACT_EMAIL` is optional and otherwise reuses `NCBI_EMAIL`.
The standalone command does not read or require `DATABASE_URL`.

The CSV has this exact field order:

```text
domain
source_order
source_journal_name
impact_factor
jcr_value
cass_value
issn_l
print_issn
eissn
all_issns
crossref_publisher
resolution_status
pubmed_supported
pubmed_record_count
pubmed_checked_at
source_url
verification_status
```

Only uniquely resolved Crossref rows with at least one valid ISSN are queried.
All known ISSNs for one journal are sent in one exact PubMed OR query, using one
shared bounded client and sequential request order. A positive count is `yes`,
a successful zero count is `no`, and a request or protocol failure is recorded
as explicit `unknown` in the report. Ambiguous and unresolved rows are not
queried.

The command fully encodes and reconciles the CSV and typed JSON report in
memory before publishing. Each artifact is written to a temporary file in its
own target directory, synced, closed, published with an atomic no-replace
hard-link, and then the temporary link is unlinked. A controlled failure
removes temporary files and any newly published final from that run. The two
separate hard-link publishes are not a cross-crash atomic transaction.

This repository stage intentionally does not include the live 2032-row output
or report; those data artifacts are generated in the dedicated data-production
task.
