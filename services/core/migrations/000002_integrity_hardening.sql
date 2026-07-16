CREATE FUNCTION normalize_paper_identifier(
    identifier_scheme text,
    identifier_value text
)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
    normalized text;
    arxiv_archive text;
    arxiv_date_part text;
    arxiv_date_code integer;
    arxiv_month integer;
    arxiv_sequence text;
    arxiv_subject_class text;
    arxiv_version text;
    arxiv_year integer;
BEGIN
    IF identifier_scheme IS NULL OR identifier_value IS NULL THEN
        RETURN NULL;
    END IF;

    normalized := btrim(identifier_value);

    CASE identifier_scheme
        WHEN 'doi' THEN
            normalized := lower(normalized);
            IF normalized !~ '^10\.[0-9]{4,9}/[^[:space:]]+$' THEN
                RETURN NULL;
            END IF;
        WHEN 'arxiv' THEN
            normalized := lower(normalized);
            IF normalized ~ 'v[0-9]+$' THEN
                arxiv_version := substring(normalized FROM 'v([0-9]+)$');
                IF left(arxiv_version, 1) = '0' THEN
                    RETURN NULL;
                END IF;
                normalized := regexp_replace(normalized, 'v[0-9]+$', '');
            END IF;

            IF normalized ~ '^[0-9]{4}\.[0-9]+$' THEN
                arxiv_date_code := substring(normalized FROM 1 FOR 4)::integer;
                arxiv_month := substring(normalized FROM 3 FOR 2)::integer;
                arxiv_sequence := split_part(normalized, '.', 2);
                IF arxiv_month < 1 OR arxiv_month > 12
                    OR arxiv_date_code < 704
                    OR arxiv_sequence ~ '^0+$'
                    OR (arxiv_date_code <= 1412 AND length(arxiv_sequence) <> 4)
                    OR (arxiv_date_code >= 1501 AND length(arxiv_sequence) <> 5)
                THEN
                    RETURN NULL;
                END IF;
            ELSIF normalized ~ '^[a-z][a-z0-9.-]*/[0-9]{7}$' THEN
                arxiv_archive := split_part(normalized, '/', 1);
                arxiv_date_part := split_part(normalized, '/', 2);
                arxiv_year := substring(arxiv_date_part FROM 1 FOR 2)::integer;
                arxiv_month := substring(arxiv_date_part FROM 3 FOR 2)::integer;
                arxiv_sequence := substring(arxiv_date_part FROM 5 FOR 3);

                IF arxiv_month < 1 OR arxiv_month > 12
                    OR arxiv_sequence ~ '^0+$'
                    OR (
                        arxiv_year >= 91
                        AND arxiv_year = 91
                        AND arxiv_month < 8
                    )
                    OR (
                        arxiv_year <= 7
                        AND arxiv_year = 7
                        AND arxiv_month > 3
                    )
                    OR (arxiv_year > 7 AND arxiv_year < 91)
                THEN
                    RETURN NULL;
                END IF;

                IF strpos(arxiv_archive, '.') > 0 THEN
                    arxiv_subject_class := split_part(arxiv_archive, '.', 2);
                    IF split_part(arxiv_archive, '.', 3) <> ''
                        OR arxiv_subject_class !~ '^[a-z][a-z0-9-]*$'
                        OR split_part(arxiv_archive, '.', 1) <> ALL (
                            ARRAY['astro-ph', 'cond-mat', 'cs', 'math', 'nlin', 'physics', 'q-bio']
                        )
                    THEN
                        RETURN NULL;
                    END IF;
                    arxiv_archive := split_part(arxiv_archive, '.', 1);
                ELSIF arxiv_archive <> ALL (
                    ARRAY[
                        'acc-phys', 'adap-org', 'alg-geom', 'ao-sci', 'astro-ph',
                        'atom-ph', 'bayes-an', 'chao-dyn', 'chem-ph', 'cmp-lg',
                        'comp-gas', 'cond-mat', 'cs', 'dg-ga', 'funct-an', 'gr-qc',
                        'hep-ex', 'hep-lat', 'hep-ph', 'hep-th', 'math', 'math-ph',
                        'mtrl-th', 'nlin', 'nucl-ex', 'nucl-th', 'patt-sol',
                        'physics', 'plasm-ph', 'q-alg', 'q-bio', 'quant-ph',
                        'solv-int', 'supr-con'
                    ]
                ) THEN
                    RETURN NULL;
                END IF;

                normalized := arxiv_archive || '/' || arxiv_date_part;
            ELSE
                RETURN NULL;
            END IF;
        WHEN 'openreview' THEN
            IF normalized !~ '^[A-Za-z0-9][A-Za-z0-9._~-]{0,254}$' THEN
                RETURN NULL;
            END IF;
        WHEN 's2' THEN
            normalized := lower(normalized);
            IF normalized !~ '^[0-9a-f]{40}$' THEN
                RETURN NULL;
            END IF;
        WHEN 'openalex' THEN
            normalized := upper(normalized);
            IF normalized !~ '^W[0-9]+$' THEN
                RETURN NULL;
            END IF;
        WHEN 'pmid' THEN
            IF normalized !~ '^[0-9]+$' THEN
                RETURN NULL;
            END IF;
        WHEN 'pmcid' THEN
            normalized := upper(normalized);
            IF normalized !~ '^PMC[0-9]+$' THEN
                RETURN NULL;
            END IF;
        ELSE
            RETURN NULL;
    END CASE;

    RETURN normalized;
END;
$$;

CREATE FUNCTION normalize_work_canonical_key(canonical_key text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
AS $$
DECLARE
    separator_position integer;
    identifier_scheme text;
    identifier_value text;
    normalized_value text;
BEGIN
    IF canonical_key IS NULL THEN
        RETURN NULL;
    END IF;

    separator_position := strpos(canonical_key, ':');
    IF separator_position <= 1 THEN
        RETURN NULL;
    END IF;

    identifier_scheme := left(canonical_key, separator_position - 1);
    IF identifier_scheme NOT IN ('doi', 'arxiv', 'openreview', 's2', 'openalex') THEN
        RETURN NULL;
    END IF;

    identifier_value := substring(canonical_key FROM separator_position + 1);
    normalized_value := normalize_paper_identifier(identifier_scheme, identifier_value);
    IF normalized_value IS NULL THEN
        RETURN NULL;
    END IF;

    RETURN identifier_scheme || ':' || normalized_value;
END;
$$;

CREATE TEMP TABLE work_merge_destinations
ON COMMIT DROP
AS
WITH normalized_works AS (
    SELECT
        id AS work_id,
        created_at,
        normalize_work_canonical_key(canonical_key) AS normalized_identity
    FROM works
),
ranked_works AS (
    SELECT
        work_id,
        normalized_identity,
        CASE
            WHEN normalized_identity IS NULL THEN work_id
            ELSE first_value(work_id) OVER (
                PARTITION BY normalized_identity
                ORDER BY created_at, work_id
            )
        END AS target_work_id
    FROM normalized_works
)
SELECT work_id, target_work_id, normalized_identity
FROM ranked_works;

CREATE UNIQUE INDEX work_merge_destinations_work_id_key
    ON work_merge_destinations(work_id);

DO $$
DECLARE
    conflict_identity text;
    conflict_version_label text;
    conflict_rows text;
BEGIN
    SELECT
        destination.normalized_identity,
        version.version_label,
        string_agg(version.id::text, ', ' ORDER BY version.id)
    INTO conflict_identity, conflict_version_label, conflict_rows
    FROM paper_versions AS version
    JOIN work_merge_destinations AS destination
      ON destination.work_id = version.work_id
    GROUP BY
        destination.target_work_id,
        destination.normalized_identity,
        version.version_label
    HAVING count(*) > 1
    ORDER BY destination.normalized_identity, version.version_label
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'cannot reconcile normalized identity %: paper_versions label % has conflicting rows %',
            conflict_identity,
            conflict_version_label,
            conflict_rows
            USING ERRCODE = '23505', TABLE = 'paper_versions';
    END IF;
END;
$$;

DO $$
DECLARE
    conflict_scheme text;
    conflict_value text;
    conflict_works text;
BEGIN
    SELECT
        identifier.scheme,
        normalize_paper_identifier(identifier.scheme, identifier.normalized_value),
        string_agg(
            DISTINCT destination.target_work_id::text,
            ', '
            ORDER BY destination.target_work_id::text
        )
    INTO conflict_scheme, conflict_value, conflict_works
    FROM external_identifiers AS identifier
    JOIN work_merge_destinations AS destination
      ON destination.work_id = identifier.work_id
    WHERE normalize_paper_identifier(identifier.scheme, identifier.normalized_value) IS NOT NULL
    GROUP BY
        identifier.scheme,
        normalize_paper_identifier(identifier.scheme, identifier.normalized_value)
    HAVING count(DISTINCT destination.target_work_id) > 1
    ORDER BY identifier.scheme, normalize_paper_identifier(identifier.scheme, identifier.normalized_value)
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'cannot reconcile normalized external identity %:% across Works %',
            conflict_scheme,
            conflict_value,
            conflict_works
            USING ERRCODE = '23505', TABLE = 'external_identifiers';
    END IF;
END;
$$;

DO $$
DECLARE
    conflict_identity text;
    conflict_key text;
BEGIN
    SELECT
        destination.normalized_identity,
        author.author_id::text
    INTO conflict_identity, conflict_key
    FROM work_authors AS author
    JOIN work_merge_destinations AS destination
      ON destination.work_id = author.work_id
    GROUP BY destination.target_work_id, destination.normalized_identity, author.author_id
    HAVING count(*) > 1
       AND count(DISTINCT ROW(
           author.institution_id,
           author.author_position,
           author.is_corresponding
       )) > 1
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'cannot reconcile normalized identity %: work_authors author % has conflicting attributes',
            conflict_identity,
            conflict_key
            USING ERRCODE = '23505', TABLE = 'work_authors';
    END IF;

    SELECT
        destination.normalized_identity,
        author.author_position::text
    INTO conflict_identity, conflict_key
    FROM work_authors AS author
    JOIN work_merge_destinations AS destination
      ON destination.work_id = author.work_id
    GROUP BY destination.target_work_id, destination.normalized_identity, author.author_position
    HAVING count(DISTINCT author.author_id) > 1
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'cannot reconcile normalized identity %: work_authors position % has multiple authors',
            conflict_identity,
            conflict_key
            USING ERRCODE = '23505', TABLE = 'work_authors';
    END IF;
END;
$$;

DO $$
DECLARE
    conflict_identity text;
    conflict_topic uuid;
BEGIN
    SELECT
        destination.normalized_identity,
        topic.topic_id
    INTO conflict_identity, conflict_topic
    FROM work_topics AS topic
    JOIN work_merge_destinations AS destination
      ON destination.work_id = topic.work_id
    GROUP BY destination.target_work_id, destination.normalized_identity, topic.topic_id
    HAVING count(*) > 1
       AND count(DISTINCT ROW(topic.confidence, topic.source_record_id)) > 1
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'cannot reconcile normalized identity %: work_topics topic % has conflicting provenance',
            conflict_identity,
            conflict_topic
            USING ERRCODE = '23505', TABLE = 'work_topics';
    END IF;
END;
$$;

DO $$
DECLARE
    conflict_identity text;
    conflict_method uuid;
BEGIN
    SELECT
        destination.normalized_identity,
        method.method_id
    INTO conflict_identity, conflict_method
    FROM work_methods AS method
    JOIN work_merge_destinations AS destination
      ON destination.work_id = method.work_id
    GROUP BY destination.target_work_id, destination.normalized_identity, method.method_id
    HAVING count(*) > 1
       AND count(DISTINCT ROW(method.confidence, method.source_record_id)) > 1
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'cannot reconcile normalized identity %: work_methods method % has conflicting provenance',
            conflict_identity,
            conflict_method
            USING ERRCODE = '23505', TABLE = 'work_methods';
    END IF;
END;
$$;

DO $$
DECLARE
    conflict_identity text;
    conflict_metric text;
    conflict_observed_at timestamptz;
BEGIN
    SELECT
        destination.normalized_identity,
        metric.metric_name,
        metric.observed_at
    INTO conflict_identity, conflict_metric, conflict_observed_at
    FROM metric_snapshots AS metric
    JOIN work_merge_destinations AS destination
      ON destination.work_id = metric.work_id
    GROUP BY
        destination.target_work_id,
        destination.normalized_identity,
        metric.metric_name,
        metric.observed_at
    HAVING count(*) > 1
       AND count(DISTINCT ROW(metric.metric_value, metric.source, metric.metadata)) > 1
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'cannot reconcile normalized identity %: metric_snapshots % at % has conflicting values',
            conflict_identity,
            conflict_metric,
            conflict_observed_at
            USING ERRCODE = '23505', TABLE = 'metric_snapshots';
    END IF;
END;
$$;

DO $$
DECLARE
    conflict_identity text;
    conflict_hash text;
BEGIN
    SELECT
        destination.normalized_identity,
        asset.content_hash
    INTO conflict_identity, conflict_hash
    FROM fulltext_assets AS asset
    JOIN work_merge_destinations AS destination
      ON destination.work_id = asset.work_id
    GROUP BY destination.target_work_id, destination.normalized_identity, asset.content_hash
    HAVING count(*) > 1
       AND count(DISTINCT ROW(
           asset.source_record_id,
           asset.source_url,
           asset.license,
           asset.retrieved_at,
           asset.reusable_status,
           asset.publication_status,
           asset.storage_key,
           asset.metadata
       )) > 1
    LIMIT 1;

    IF FOUND THEN
        RAISE EXCEPTION
            'cannot reconcile normalized identity %: fulltext_assets hash % has conflicting metadata',
            conflict_identity,
            conflict_hash
            USING ERRCODE = '23505', TABLE = 'fulltext_assets';
    END IF;
END;
$$;

ALTER TABLE paper_versions
    DROP CONSTRAINT paper_versions_source_record_work_fkey;

ALTER TABLE external_identifiers
    DROP CONSTRAINT external_identifiers_source_record_work_fkey;

ALTER TABLE field_assertions
    DROP CONSTRAINT field_assertions_source_record_work_fkey;

ALTER TABLE work_topics
    DROP CONSTRAINT work_topics_source_record_work_fkey;

ALTER TABLE work_methods
    DROP CONSTRAINT work_methods_source_record_work_fkey;

ALTER TABLE fulltext_assets
    DROP CONSTRAINT fulltext_assets_source_record_work_fkey;

WITH ranked_identifiers AS (
    SELECT
        identifier.id,
        row_number() OVER (
            PARTITION BY
                destination.target_work_id,
                identifier.scheme,
                normalize_paper_identifier(identifier.scheme, identifier.normalized_value)
            ORDER BY identifier.created_at, identifier.id
        ) AS duplicate_rank
    FROM external_identifiers AS identifier
    JOIN work_merge_destinations AS destination
      ON destination.work_id = identifier.work_id
    WHERE normalize_paper_identifier(identifier.scheme, identifier.normalized_value) IS NOT NULL
)
DELETE FROM external_identifiers AS identifier
USING ranked_identifiers AS ranked
WHERE identifier.id = ranked.id
  AND ranked.duplicate_rank > 1;

WITH ranked_authors AS (
    SELECT
        author.id,
        row_number() OVER (
            PARTITION BY destination.target_work_id, author.author_id
            ORDER BY author.created_at, author.id
        ) AS duplicate_rank
    FROM work_authors AS author
    JOIN work_merge_destinations AS destination
      ON destination.work_id = author.work_id
)
DELETE FROM work_authors AS author
USING ranked_authors AS ranked
WHERE author.id = ranked.id
  AND ranked.duplicate_rank > 1;

WITH ranked_topics AS (
    SELECT
        topic.id,
        row_number() OVER (
            PARTITION BY destination.target_work_id, topic.topic_id
            ORDER BY topic.created_at, topic.id
        ) AS duplicate_rank
    FROM work_topics AS topic
    JOIN work_merge_destinations AS destination
      ON destination.work_id = topic.work_id
)
DELETE FROM work_topics AS topic
USING ranked_topics AS ranked
WHERE topic.id = ranked.id
  AND ranked.duplicate_rank > 1;

WITH ranked_methods AS (
    SELECT
        method.id,
        row_number() OVER (
            PARTITION BY destination.target_work_id, method.method_id
            ORDER BY method.created_at, method.id
        ) AS duplicate_rank
    FROM work_methods AS method
    JOIN work_merge_destinations AS destination
      ON destination.work_id = method.work_id
)
DELETE FROM work_methods AS method
USING ranked_methods AS ranked
WHERE method.id = ranked.id
  AND ranked.duplicate_rank > 1;

WITH ranked_datasets AS (
    SELECT
        relation.id,
        row_number() OVER (
            PARTITION BY
                destination.target_work_id,
                relation.dataset_id,
                relation.relation_type
            ORDER BY relation.created_at, relation.id
        ) AS duplicate_rank
    FROM work_datasets AS relation
    JOIN work_merge_destinations AS destination
      ON destination.work_id = relation.work_id
)
DELETE FROM work_datasets AS relation
USING ranked_datasets AS ranked
WHERE relation.id = ranked.id
  AND ranked.duplicate_rank > 1;

WITH ranked_benchmarks AS (
    SELECT
        relation.id,
        row_number() OVER (
            PARTITION BY
                destination.target_work_id,
                relation.benchmark_id,
                relation.relation_type
            ORDER BY relation.created_at, relation.id
        ) AS duplicate_rank
    FROM work_benchmarks AS relation
    JOIN work_merge_destinations AS destination
      ON destination.work_id = relation.work_id
)
DELETE FROM work_benchmarks AS relation
USING ranked_benchmarks AS ranked
WHERE relation.id = ranked.id
  AND ranked.duplicate_rank > 1;

WITH ranked_models AS (
    SELECT
        relation.id,
        row_number() OVER (
            PARTITION BY
                destination.target_work_id,
                relation.model_id,
                relation.relation_type
            ORDER BY relation.created_at, relation.id
        ) AS duplicate_rank
    FROM work_models AS relation
    JOIN work_merge_destinations AS destination
      ON destination.work_id = relation.work_id
)
DELETE FROM work_models AS relation
USING ranked_models AS ranked
WHERE relation.id = ranked.id
  AND ranked.duplicate_rank > 1;

WITH ranked_repositories AS (
    SELECT
        relation.id,
        row_number() OVER (
            PARTITION BY
                destination.target_work_id,
                relation.repository_id,
                relation.relation_type
            ORDER BY relation.created_at, relation.id
        ) AS duplicate_rank
    FROM work_code_repositories AS relation
    JOIN work_merge_destinations AS destination
      ON destination.work_id = relation.work_id
)
DELETE FROM work_code_repositories AS relation
USING ranked_repositories AS ranked
WHERE relation.id = ranked.id
  AND ranked.duplicate_rank > 1;

WITH ranked_metrics AS (
    SELECT
        metric.id,
        row_number() OVER (
            PARTITION BY
                destination.target_work_id,
                metric.metric_name,
                metric.observed_at
            ORDER BY metric.created_at, metric.id
        ) AS duplicate_rank
    FROM metric_snapshots AS metric
    JOIN work_merge_destinations AS destination
      ON destination.work_id = metric.work_id
)
DELETE FROM metric_snapshots AS metric
USING ranked_metrics AS ranked
WHERE metric.id = ranked.id
  AND ranked.duplicate_rank > 1;

WITH ranked_assets AS (
    SELECT
        asset.id,
        row_number() OVER (
            PARTITION BY destination.target_work_id, asset.content_hash
            ORDER BY asset.created_at, asset.id
        ) AS duplicate_rank
    FROM fulltext_assets AS asset
    JOIN work_merge_destinations AS destination
      ON destination.work_id = asset.work_id
)
DELETE FROM fulltext_assets AS asset
USING ranked_assets AS ranked
WHERE asset.id = ranked.id
  AND ranked.duplicate_rank > 1;

UPDATE source_records AS source
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE source.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE paper_versions AS version
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE version.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE external_identifiers AS identifier
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE identifier.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE field_assertions AS assertion
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE assertion.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE work_authors AS author
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE author.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE work_topics AS topic
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE topic.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE work_methods AS method
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE method.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE work_datasets AS relation
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE relation.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE work_benchmarks AS relation
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE relation.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE work_models AS relation
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE relation.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE work_code_repositories AS relation
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE relation.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE metric_snapshots AS metric
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE metric.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE ranking_snapshots AS ranking
SET subject_work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE ranking.subject_work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE fulltext_assets AS asset
SET work_id = destination.target_work_id
FROM work_merge_destinations AS destination
WHERE asset.work_id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

DELETE FROM works AS work
USING work_merge_destinations AS destination
WHERE work.id = destination.work_id
  AND destination.work_id <> destination.target_work_id;

UPDATE works
SET canonical_key = normalize_work_canonical_key(canonical_key)
WHERE normalize_work_canonical_key(canonical_key) IS NOT NULL
  AND canonical_key IS DISTINCT FROM normalize_work_canonical_key(canonical_key);

UPDATE external_identifiers
SET normalized_value = normalize_paper_identifier(scheme, normalized_value)
WHERE normalize_paper_identifier(scheme, normalized_value) IS NOT NULL
  AND normalized_value IS DISTINCT FROM normalize_paper_identifier(scheme, normalized_value);

ALTER TABLE works
    DROP CONSTRAINT works_canonical_key_check;

ALTER TABLE works
    ADD CONSTRAINT works_canonical_key_check
    CHECK (
        normalize_work_canonical_key(canonical_key) IS NOT NULL
        AND canonical_key = normalize_work_canonical_key(canonical_key)
    );

ALTER TABLE external_identifiers
    DROP CONSTRAINT external_identifiers_normalized_value_check;

ALTER TABLE external_identifiers
    ADD CONSTRAINT external_identifiers_normalized_value_check
    CHECK (
        normalize_paper_identifier(scheme, normalized_value) IS NOT NULL
        AND normalized_value = normalize_paper_identifier(scheme, normalized_value)
    );

CREATE FUNCTION enforce_work_status_monotonic()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status <> 'active' AND NEW.status IS DISTINCT FROM OLD.status THEN
        RAISE EXCEPTION 'terminal work status % cannot transition to %', OLD.status, NEW.status
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'works_status_monotonic';
    END IF;
    RETURN NEW;
END;
$$;

COMMENT ON FUNCTION enforce_work_status_monotonic() IS
    'Enforces terminal status monotonicity only; repository compare-and-swap is intentionally deferred to Task 7.';

CREATE TRIGGER works_status_monotonic
BEFORE UPDATE OF status ON works
FOR EACH ROW
EXECUTE FUNCTION enforce_work_status_monotonic();

CREATE FUNCTION resolve_venue_by_issn(
    requested_issn_l text,
    requested_issn text,
    requested_eissn text
)
RETURNS uuid
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    matched_venue_ids uuid[];
BEGIN
    SELECT array_agg(matches.id ORDER BY matches.id)
    INTO matched_venue_ids
    FROM (
        SELECT DISTINCT venue.id
        FROM venues AS venue
        CROSS JOIN LATERAL (
            VALUES (requested_issn_l), (requested_issn), (requested_eissn)
        ) AS requested(identifier)
        WHERE requested.identifier IS NOT NULL
          AND requested.identifier <> ''
          AND requested.identifier IN (venue.issn_l, venue.issn, venue.eissn)
    ) AS matches;

    CASE COALESCE(cardinality(matched_venue_ids), 0)
        WHEN 0 THEN
            RAISE EXCEPTION 'no venue matches exact ISSN identifiers'
                USING ERRCODE = 'P0002';
        WHEN 1 THEN
            RETURN matched_venue_ids[1];
        ELSE
            RAISE EXCEPTION 'multiple venues match exact ISSN identifiers'
                USING ERRCODE = 'P0003';
    END CASE;
END;
$$;

CREATE TABLE source_record_works (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_record_id uuid NOT NULL,
    work_id uuid NOT NULL,
    linked_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT source_record_works_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT source_record_works_work_id_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE CASCADE,
    CONSTRAINT source_record_works_source_record_id_key
        UNIQUE (source_record_id),
    CONSTRAINT source_record_works_source_record_id_work_id_key
        UNIQUE (source_record_id, work_id)
);

CREATE INDEX idx_source_record_works_work_id ON source_record_works(work_id);

INSERT INTO source_record_works (source_record_id, work_id)
SELECT id, work_id
FROM source_records
WHERE work_id IS NOT NULL;

ALTER TABLE paper_versions
    ADD CONSTRAINT paper_versions_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT paper_versions_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE external_identifiers
    ADD CONSTRAINT external_identifiers_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT external_identifiers_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE field_assertions
    ADD CONSTRAINT field_assertions_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT field_assertions_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE work_topics
    ADD CONSTRAINT work_topics_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT work_topics_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE work_methods
    ADD CONSTRAINT work_methods_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT work_methods_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE fulltext_assets
    ADD CONSTRAINT fulltext_assets_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT fulltext_assets_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

DROP TRIGGER source_records_raw_immutable ON source_records;
DROP FUNCTION enforce_source_record_raw_immutability();

ALTER TABLE source_records
    DROP CONSTRAINT source_records_work_id_fkey,
    DROP CONSTRAINT source_records_id_work_id_key;

DROP INDEX idx_source_records_work_id;

ALTER TABLE source_records
    DROP COLUMN work_id;

CREATE TRIGGER source_records_immutable
BEFORE UPDATE OR DELETE ON source_records
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
