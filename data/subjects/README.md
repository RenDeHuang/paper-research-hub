# Biomedical JCR Subject registry

`biomedical-jcr-subjects.v1.csv` is the immutable first version of the
medpaperhub biomedical Subject allowlist. Each row records one stable Subject
slug and one reviewed, exact JCR Category value.

## Exact CSV contract

The header, order, and case are fixed:

```text
source,registry_version,slug,display_label,jcr_category
```

Every row must repeat the same nonblank `source` and explicit
`registry_version`. Slugs are unique lowercase hyphenated identifiers.
`jcr_category` is unique within the version and must be copied exactly from the
authorized JCR Category export: the importer does not trim, case-fold, alias,
prefix-match, or substring-match Category values.

Changing any byte requires publishing a new registry version and file. The
worker records the raw file SHA-256, source, version, Subject count, rule count,
and import time in one atomic receipt chain:

```text
subject_import_receipts
  -> subject_versions
  -> subjects
  -> biomedical_subject_rules
```

Import with:

```bash
paper-hub-worker import subjects \
  --file /imports/biomedical-jcr-subjects.v1.csv
```

Re-importing the identical file is idempotent. Reusing the same source/version
with different bytes is rejected atomically.

The registry contains Category rules only. It does not contain JIF, Quartile,
or synthetic Venue metrics, and synthetic JCR fixtures must never be used to
publish a production Catalog generation.
