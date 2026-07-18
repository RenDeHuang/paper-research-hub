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
