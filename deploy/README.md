# Local deployment contract

`deploy/` defines the operator-facing local release boundary. The root
`docker-compose.yml` remains the canonical definition for PostgreSQL, migrate,
API, Worker, and Web commands. `deploy/compose/compose.local.yml` is an overlay
only; it must not copy those commands.

## Prerequisites

- Docker with the Compose plugin
- POSIX `sh`
- `curl`, `awk`, `grep`, and standard Unix utilities

No API key, authorized JCR export, or paper replay input belongs in Git. Copy
the examples to ignored files and replace every angle-bracket placeholder:

```sh
cp deploy/env/local.env.example deploy/env/.env.local
cp deploy/env/pipeline.env.example deploy/env/.env.pipeline
```

Use an absolute path when selecting the local environment file:

```sh
LOCAL_ENV_FILE="$PWD/deploy/env/.env.local" make local-up
```

`local-up` resets the dedicated Compose project's volumes before startup so
that `local-verify-empty` can prove the truthful empty-Catalog boundary. Set
`COMPOSE_PROJECT_NAME` explicitly if a different isolated project name is
required.

## Empty deployment

```sh
make local-up
make local-verify-empty
```

The empty verifier requires healthy API and Web services and requires
`GET /api/v1/home` to return `503 catalog_not_published`. It does not treat
fixture data or a synthetic Catalog as an acceptable empty state.

## Release manifest

The first manifest version is intentionally restricted to plain, top-level
`KEY: value` lines. Blank lines and full-line comments are accepted. Indented
content, lists, maps, quoted values, inline comments, duplicate keys, and
unknown keys are rejected. The only accepted keys are:

```text
MANIFEST_VERSION: 1
JCR_IMPORT_PATH: /absolute/path/to/authorized-jcr.csv
REPLAY_INPUT_PATH: /absolute/path/to/fixed-paper-input.ndjson
```

Both input paths must be absolute paths to existing regular files. The JCR
file must be an operator-authorized export; repository synthetic examples are
not release inputs.

Copy `deploy/manifests/replay.example.yaml` outside the repository or to an
ignored local file, replace its paths, and invoke:

```sh
make local-release MANIFEST=/absolute/path/to/replay.yaml
```

Task 0 provides only the validation and orchestration boundary. After a valid
manifest is accepted, `local-release` deliberately exits non-zero with
`pipeline stage not implemented`. It must not report a release until the
remaining Registry, replay, assessment, analysis, and publication stages are
implemented.

## Release verification

```sh
make local-verify-release
```

`local-verify-release` accepts only an HTTP `200` Catalog response with a
non-empty `X-Catalog-Generation` header. In particular, an empty deployment's
HTTP `503` is a hard failure.

For isolated verification or tests, inject both origins without contacting
the Compose services:

```sh
API_URL=http://api.test.invalid \
WEB_URL=http://web.test.invalid \
sh deploy/scripts/verify-release.sh
```

The SQL assertion in `deploy/sql/verify-release.sql` verifies that PostgreSQL
has a current generation backed by a publication row. It is kept separate
from HTTP verification until the complete release orchestrator is available.

## Contract verification

```sh
sh deploy/tests/deploy_contract_test.sh
make compose-config
```

The contract test validates file presence, POSIX shell syntax, Compose
merging, strict manifest rejection, placeholder-only environment examples,
and published-versus-unpublished HTTP behavior without using real services.
