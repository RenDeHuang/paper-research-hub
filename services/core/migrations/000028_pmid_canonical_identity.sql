CREATE OR REPLACE FUNCTION normalize_paper_identifier(
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
                    IF arxiv_archive !~ '^[a-z][a-z0-9-]*\.[a-z][a-z0-9-]*$' THEN
                        RETURN NULL;
                    END IF;
                    arxiv_subject_class := split_part(arxiv_archive, '.', 2);
                    arxiv_archive := split_part(arxiv_archive, '.', 1);
                    IF arxiv_subject_class !~ '^[a-z][a-z0-9-]*$'
                        OR arxiv_archive <> ALL (
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
            IF normalized !~ '^[1-9][0-9]*$' THEN
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

CREATE OR REPLACE FUNCTION normalize_work_canonical_key(canonical_key text)
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
    IF identifier_scheme NOT IN (
        'doi', 'pmid', 'arxiv', 'openreview', 's2', 'openalex'
    ) THEN
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

ALTER TABLE works
    DROP CONSTRAINT works_canonical_key_check;

ALTER TABLE works
    ADD CONSTRAINT works_canonical_key_check
    CHECK (
        normalize_work_canonical_key(canonical_key) IS NOT NULL
        AND canonical_key = normalize_work_canonical_key(canonical_key)
    )
    NOT VALID;

ALTER TABLE works
    VALIDATE CONSTRAINT works_canonical_key_check;
