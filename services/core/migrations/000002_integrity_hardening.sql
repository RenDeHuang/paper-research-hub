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
    arxiv_date_part text;
    arxiv_month integer;
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
            normalized := regexp_replace(normalized, 'v[0-9]+$', '');
            IF normalized ~ '^[0-9]{4}\.[0-9]{4,5}$' THEN
                arxiv_month := substring(normalized FROM 3 FOR 2)::integer;
            ELSIF normalized ~ '^[a-z][a-z0-9.-]*/[0-9]{7}$' THEN
                arxiv_date_part := split_part(normalized, '/', 2);
                arxiv_month := substring(arxiv_date_part FROM 3 FOR 2)::integer;
            ELSE
                RETURN NULL;
            END IF;
            IF arxiv_month < 1 OR arxiv_month > 12 THEN
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
    DROP CONSTRAINT paper_versions_source_record_work_fkey,
    ADD CONSTRAINT paper_versions_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT paper_versions_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE external_identifiers
    DROP CONSTRAINT external_identifiers_source_record_work_fkey,
    ADD CONSTRAINT external_identifiers_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT external_identifiers_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE field_assertions
    DROP CONSTRAINT field_assertions_source_record_work_fkey,
    ADD CONSTRAINT field_assertions_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT field_assertions_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE work_topics
    DROP CONSTRAINT work_topics_source_record_work_fkey,
    ADD CONSTRAINT work_topics_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT work_topics_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE work_methods
    DROP CONSTRAINT work_methods_source_record_work_fkey,
    ADD CONSTRAINT work_methods_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT work_methods_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE fulltext_assets
    DROP CONSTRAINT fulltext_assets_source_record_work_fkey,
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
