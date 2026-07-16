# Venue metric fixtures

`jcr-q1.example.csv` is a **fully synthetic** schema fixture. Its journal
titles, identifiers, ISSNs, categories, JIF values, and quartiles are invented
for automated tests and documentation; they are not copied from JCR or any
other commercial dataset.

Production JCR data must come from a user-authorized CSV export supplied
explicitly through `JCR_IMPORT_PATH`. The application never substitutes
OpenAlex citation metrics, title similarity, or another inferred metric when
that path is absent.

The fixture demonstrates:

- exact ISSN-L/ISSN/eISSN and controlled-source matching;
- multiple Q1 categories for one venue and metric year;
- a nonnegative JIF greater than or equal to 10;
- an explicit `unknown` metric row with no JIF or quartile.

The production importer is intentionally outside Task 3.
