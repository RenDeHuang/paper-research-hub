# Research domain and legacy biomedical Subject registries

`research-domains-jcr-subjects.v2.csv` is the current public-scope Registry.
Its only top-level domain identities are:

```text
medicine
biology
computer_science
```

The exact header is:

```text
registry_name,registry_version,domain,display_label,jcr_category,article_level_required
```

Each JCR Category mapping is an explicit row. The same exact Category may map
to more than one domain only when every mapping is present as a separate CSV
row. Import and lookup do not trim, case-fold, spell-correct, alias, prefix
match, or approximately match Category values.

`article_level_required=true` prevents a journal-level Category from assigning
an individual Work to a domain. In particular, multidisciplinary journal
articles require a later article-level domain assertion before they may enter
domain trends.

The Registry import records its exact bytes as SHA-256 in `domain_versions`,
with immutable `research_domains` and `domain_category_rules` children.
Identical bytes for the same version are idempotent; different bytes for an
existing version are rejected.

Preprints and conferences use the independent versioned Registries:

```text
data/sources/preprint-sources.v1.csv
data/venues/conference-venues.v1.csv
```

They never use JCR evidence.

## Legacy biomedical Registry

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

This legacy Registry remains readable for migration replay. It is not a new
public top-level domain. Neither Registry contains JIF, Quartile, or synthetic
Venue metrics, and synthetic JCR fixtures must never be used to publish a
production Catalog generation.
