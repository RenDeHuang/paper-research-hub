CREATE FUNCTION official_url_is_accepted(
    value text,
    https_only boolean
)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    remainder text;
    authority text;
BEGIN
    IF value = ''
        OR value <> btrim(value)
        OR value ~ '[[:space:][:cntrl:]]'
        OR position('#' IN value) > 0
        OR position(
            '%' IN regexp_replace(
                value,
                '%[0-9A-Fa-f]{2}',
                '',
                'g'
            )
        ) > 0
    THEN
        RETURN false;
    END IF;

    IF left(value, 8) = 'https://' THEN
        remainder := substr(value, 9);
    ELSIF NOT https_only AND left(value, 7) = 'http://' THEN
        remainder := substr(value, 8);
    ELSE
        RETURN false;
    END IF;

    authority := split_part(
        split_part(remainder, '/', 1),
        '?',
        1
    );
    IF authority = '' OR position('@' IN authority) > 0 THEN
        RETURN false;
    END IF;

    RETURN
        authority ~
            '^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]+)?$'
        OR authority ~
            '^\[[0-9A-Fa-f:.]+\](:[0-9]+)?$';
END;
$$;

CREATE FUNCTION official_url_canonical(value text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    scheme text;
    remainder text;
    authority text;
    suffix text;
BEGIN
    IF NOT official_url_is_accepted(value, false) THEN
        RETURN NULL;
    END IF;
    IF left(value, 8) = 'https://' THEN
        scheme := 'https';
        remainder := substr(value, 9);
    ELSE
        scheme := 'http';
        remainder := substr(value, 8);
    END IF;
    authority := split_part(
        split_part(remainder, '/', 1),
        '?',
        1
    );
    suffix := substr(remainder, length(authority) + 1);
    authority := lower(authority);
    IF scheme = 'http' AND authority ~ ':80$' THEN
        authority := left(authority, length(authority) - 3);
    ELSIF scheme = 'https' AND authority ~ ':443$' THEN
        authority := left(authority, length(authority) - 4);
    END IF;
    RETURN scheme || '://' || authority || suffix;
END;
$$;

CREATE FUNCTION official_url_has_valid_percent_encoding(value text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT position(
        '%' IN regexp_replace(
            value,
            '%[0-9A-Fa-f]{2}',
            '',
            'g'
        )
    ) = 0
$$;

CREATE FUNCTION official_url_remove_dot_segments(path text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    segment text;
    input_buffer text := path;
    output_buffer text := '';
    next_slash integer;
BEGIN
    WHILE input_buffer <> ''
    LOOP
        IF left(input_buffer, 3) = '../' THEN
            input_buffer := substr(input_buffer, 4);
        ELSIF left(input_buffer, 2) = './' THEN
            input_buffer := substr(input_buffer, 3);
        ELSIF left(input_buffer, 3) = '/./' THEN
            input_buffer := '/' || substr(input_buffer, 4);
        ELSIF input_buffer = '/.' THEN
            input_buffer := '/';
        ELSIF left(input_buffer, 4) = '/../' THEN
            input_buffer := '/' || substr(input_buffer, 5);
            output_buffer := regexp_replace(
                output_buffer,
                '/[^/]*$',
                ''
            );
        ELSIF input_buffer = '/..' THEN
            input_buffer := '/';
            output_buffer := regexp_replace(
                output_buffer,
                '/[^/]*$',
                ''
            );
        ELSIF input_buffer IN ('.', '..') THEN
            input_buffer := '';
        ELSE
            IF left(input_buffer, 1) = '/' THEN
                next_slash := position(
                    '/' IN substr(input_buffer, 2)
                );
                IF next_slash = 0 THEN
                    segment := input_buffer;
                    input_buffer := '';
                ELSE
                    segment := left(input_buffer, next_slash);
                    input_buffer := substr(
                        input_buffer,
                        next_slash + 1
                    );
                END IF;
            ELSE
                next_slash := position('/' IN input_buffer);
                IF next_slash = 0 THEN
                    segment := input_buffer;
                    input_buffer := '';
                ELSE
                    segment := left(input_buffer, next_slash - 1);
                    input_buffer := substr(
                        input_buffer,
                        next_slash
                    );
                END IF;
            END IF;
            output_buffer := output_buffer || segment;
        END IF;
    END LOOP;
    RETURN output_buffer;
END;
$$;

CREATE FUNCTION official_url_normalize_absolute_reference(
    value text
)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    scheme text;
    remainder text;
    authority text;
    suffix text;
    path text;
    query text := '';
    question_position integer;
    normalized text;
BEGIN
    IF NOT official_url_is_accepted(value, false) THEN
        RETURN NULL;
    END IF;
    IF left(value, 8) = 'https://' THEN
        scheme := 'https';
        remainder := substr(value, 9);
    ELSE
        scheme := 'http';
        remainder := substr(value, 8);
    END IF;
    authority := split_part(
        split_part(remainder, '/', 1),
        '?',
        1
    );
    suffix := substr(remainder, length(authority) + 1);
    question_position := position('?' IN suffix);
    IF question_position > 0 THEN
        path := left(suffix, question_position - 1);
        query := substr(suffix, question_position);
    ELSE
        path := suffix;
    END IF;
    normalized := scheme || '://' || authority ||
        official_url_remove_dot_segments(path) || query;
    IF official_url_is_accepted(normalized, false) THEN
        RETURN normalized;
    END IF;
    RETURN NULL;
END;
$$;

CREATE FUNCTION official_url_resolve_location(
    base_url text,
    location text
)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    scheme text;
    base_remainder text;
    authority text;
    base_suffix text;
    base_path text;
    base_directory text;
    location_path text;
    location_query text := '';
    question_position integer;
    resolved text;
BEGIN
    IF NOT official_url_is_accepted(base_url, false)
        OR location = ''
        OR location <> btrim(location)
        OR location ~ '[[:space:][:cntrl:]]'
        OR position('#' IN location) > 0
        OR NOT official_url_has_valid_percent_encoding(location)
    THEN
        RETURN NULL;
    END IF;

    IF left(base_url, 8) = 'https://' THEN
        scheme := 'https';
        base_remainder := substr(base_url, 9);
    ELSE
        scheme := 'http';
        base_remainder := substr(base_url, 8);
    END IF;
    authority := split_part(
        split_part(base_remainder, '/', 1),
        '?',
        1
    );
    base_suffix := substr(base_remainder, length(authority) + 1);
    base_path := split_part(base_suffix, '?', 1);

    IF left(location, 2) = '//' THEN
        RETURN official_url_normalize_absolute_reference(
            scheme || ':' || location
        );
    END IF;
    IF location ~ '^[A-Za-z][A-Za-z0-9+.-]*:' THEN
        RETURN official_url_normalize_absolute_reference(location);
    END IF;

    question_position := position('?' IN location);
    IF question_position > 0 THEN
        location_path := left(location, question_position - 1);
        location_query := substr(location, question_position);
    ELSE
        location_path := location;
    END IF;

    IF location_path = '' THEN
        resolved := scheme || '://' || authority ||
            base_path || location_query;
    ELSIF left(location_path, 1) = '/' THEN
        resolved := scheme || '://' || authority ||
            official_url_remove_dot_segments(location_path) ||
            location_query;
    ELSE
        IF base_path = '' THEN
            base_directory := '/';
        ELSE
            base_directory := regexp_replace(
                base_path,
                '[^/]*$',
                ''
            );
        END IF;
        resolved := scheme || '://' || authority ||
            official_url_remove_dot_segments(
                base_directory || location_path
            ) || location_query;
    END IF;

    RETURN official_url_normalize_absolute_reference(resolved);
END;
$$;

CREATE FUNCTION official_url_redirect_chain_is_valid(
    chain jsonb,
    source_url text,
    final_url text,
    final_status integer
)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    hop jsonb;
    ordinal bigint;
    hop_status integer;
    chain_length integer;
    location text;
    resolved_location text;
    next_url text;
    hop_canonical text;
    seen_canonical text[] := ARRAY[]::text[];
BEGIN
    IF jsonb_typeof(chain) <> 'array' THEN
        RETURN false;
    END IF;
    chain_length := jsonb_array_length(chain);
    IF chain_length < 1
        OR chain_length > 11
        OR final_status < 100
        OR final_status > 599
    THEN
        RETURN false;
    END IF;

    FOR hop, ordinal IN
        SELECT value, ordinality
        FROM jsonb_array_elements(chain) WITH ORDINALITY
    LOOP
        IF jsonb_typeof(hop) <> 'object'
            OR jsonb_typeof(hop->'url') <> 'string'
            OR NOT official_url_is_accepted(hop->>'url', true)
            OR jsonb_typeof(hop->'status_code') <> 'number'
            OR (hop->>'status_code') !~ '^[0-9]{3}$'
        THEN
            RETURN false;
        END IF;

        hop_status := (hop->>'status_code')::integer;
        IF hop_status < 100 OR hop_status > 599 THEN
            RETURN false;
        END IF;
        hop_canonical := official_url_canonical(hop->>'url');
        IF hop_canonical IS NULL
            OR hop_canonical = ANY(seen_canonical)
        THEN
            RETURN false;
        END IF;
        seen_canonical := array_append(
            seen_canonical,
            hop_canonical
        );
        IF ordinal = 1
            AND hop_canonical <> official_url_canonical(source_url)
        THEN
            RETURN false;
        END IF;
        IF ordinal < chain_length THEN
            IF hop_status NOT IN (301, 302, 303, 307, 308)
                OR jsonb_typeof(hop->'location') <> 'string'
            THEN
                RETURN false;
            END IF;
            location := hop->>'location';
            IF location = '' OR location <> btrim(location) THEN
                RETURN false;
            END IF;
            resolved_location := official_url_resolve_location(
                hop->>'url',
                location
            );
            next_url := chain->(ordinal::integer)->>'url';
            IF resolved_location IS NULL
                OR NOT official_url_is_accepted(
                    resolved_location,
                    true
                )
                OR official_url_canonical(resolved_location) IS NULL
                OR official_url_canonical(next_url) IS NULL
                OR NOT official_url_is_accepted(next_url, true)
                OR official_url_canonical(resolved_location) <>
                    official_url_canonical(next_url)
            THEN
                RETURN false;
            END IF;
        ELSE
            IF hop_status IN (301, 302, 303, 307, 308)
                OR COALESCE(hop->>'location', '') <> ''
                OR official_url_canonical(hop->>'url') <>
                    official_url_canonical(final_url)
                OR hop_status <> final_status
            THEN
                RETURN false;
            END IF;
        END IF;
    END LOOP;

    RETURN true;
END;
$$;

CREATE FUNCTION official_url_redirect_chain_has_basic_shape(
    chain jsonb,
    source_url text,
    final_url text,
    final_status integer
)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    hop jsonb;
    ordinal bigint;
    hop_status integer;
    chain_length integer;
BEGIN
    IF jsonb_typeof(chain) <> 'array' THEN
        RETURN false;
    END IF;
    chain_length := jsonb_array_length(chain);
    IF chain_length < 1
        OR chain_length > 11
        OR final_status < 100
        OR final_status > 599
    THEN
        RETURN false;
    END IF;

    FOR hop, ordinal IN
        SELECT value, ordinality
        FROM jsonb_array_elements(chain) WITH ORDINALITY
    LOOP
        IF jsonb_typeof(hop) <> 'object'
            OR jsonb_typeof(hop->'url') <> 'string'
            OR NOT official_url_is_accepted(hop->>'url', false)
            OR jsonb_typeof(hop->'status_code') <> 'number'
            OR (hop->>'status_code') !~ '^[0-9]{3}$'
            OR jsonb_typeof(hop->'metadata') <> 'object'
            OR (
                hop ? 'location'
                AND jsonb_typeof(hop->'location') <> 'string'
            )
        THEN
            RETURN false;
        END IF;

        hop_status := (hop->>'status_code')::integer;
        IF hop_status < 100 OR hop_status > 599 THEN
            RETURN false;
        END IF;
        IF ordinal = 1
            AND official_url_canonical(hop->>'url') <>
                official_url_canonical(source_url)
        THEN
            RETURN false;
        END IF;
        IF ordinal = chain_length
            AND (
                official_url_canonical(hop->>'url') <>
                    official_url_canonical(final_url)
                OR hop_status <> final_status
            )
        THEN
            RETURN false;
        END IF;
    END LOOP;

    RETURN true;
END;
$$;

CREATE FUNCTION official_url_trim_identifier_space(value text)
RETURNS text
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
    SELECT regexp_replace(
        value,
        '^[[:space:]]+|[[:space:]]+$',
        '',
        'g'
    )
$$;

CREATE FUNCTION official_url_normalize_doi(value text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    normalized text;
BEGIN
    normalized := official_url_trim_identifier_space(value);
    normalized := regexp_replace(
        normalized,
        '^https?://(dx\.)?doi\.org/',
        '',
        'i'
    );
    normalized := regexp_replace(
        normalized,
        '^doi[[:space:]]*:',
        '',
        'i'
    );
    normalized := lower(
        official_url_trim_identifier_space(normalized)
    );
    IF normalized !~
        '^10\.[0-9]{4,9}/[^[:space:][:cntrl:]]+$'
    THEN
        RETURN NULL;
    END IF;
    RETURN normalized;
END;
$$;

CREATE FUNCTION official_url_normalize_arxiv(value text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    normalized text;
    version text;
    parts text[];
    archive text;
    archive_root text;
    subject_class text;
    year_number integer;
    month_number integer;
    date_code integer;
BEGIN
    normalized := official_url_trim_identifier_space(value);
    normalized := regexp_replace(
        normalized,
        '^https?://arxiv\.org/(abs|pdf)/',
        '',
        'i'
    );
    normalized := regexp_replace(
        normalized,
        '^arxiv[[:space:]]*:',
        '',
        'i'
    );
    normalized := official_url_trim_identifier_space(normalized);
    IF right(lower(normalized), 4) = '.pdf' THEN
        normalized := left(normalized, length(normalized) - 4);
    END IF;
    normalized := lower(normalized);

    parts := regexp_match(normalized, 'v([0-9]+)$');
    IF parts IS NOT NULL THEN
        version := parts[1];
        IF left(version, 1) = '0' THEN
            RETURN NULL;
        END IF;
        normalized := regexp_replace(normalized, 'v[0-9]+$', '');
    END IF;

    parts := regexp_match(
        normalized,
        '^([0-9]{2})([0-9]{2})\.([0-9]+)$'
    );
    IF parts IS NOT NULL THEN
        month_number := parts[2]::integer;
        date_code := (parts[1] || parts[2])::integer;
        IF month_number < 1
            OR month_number > 12
            OR date_code < 704
            OR (
                date_code <= 1412
                AND length(parts[3]) <> 4
            )
            OR (
                date_code >= 1501
                AND length(parts[3]) <> 5
            )
            OR parts[3] ~ '^0+$'
        THEN
            RETURN NULL;
        END IF;
        RETURN normalized;
    END IF;

    parts := regexp_match(
        normalized,
        '^([a-z][a-z0-9.-]*)/([0-9]{2})([0-9]{2})([0-9]{3})$'
    );
    IF parts IS NULL THEN
        RETURN NULL;
    END IF;
    archive := parts[1];
    year_number := parts[2]::integer;
    month_number := parts[3]::integer;
    IF month_number < 1
        OR month_number > 12
        OR parts[4] ~ '^0+$'
        OR (
            year_number >= 91
            AND NOT (
                year_number > 91
                OR month_number >= 8
            )
        )
        OR (
            year_number <= 7
            AND NOT (
                year_number < 7
                OR month_number <= 3
            )
        )
        OR (year_number > 7 AND year_number < 91)
    THEN
        RETURN NULL;
    END IF;

    IF position('.' IN archive) > 0 THEN
        archive_root := split_part(archive, '.', 1);
        subject_class := substr(
            archive,
            length(archive_root) + 2
        );
        IF position('.' IN subject_class) > 0
            OR subject_class !~ '^[a-z][a-z0-9-]*$'
            OR archive_root NOT IN (
                'astro-ph',
                'cond-mat',
                'cs',
                'math',
                'nlin',
                'physics',
                'q-bio'
            )
        THEN
            RETURN NULL;
        END IF;
        archive := archive_root;
    ELSIF archive NOT IN (
        'acc-phys',
        'adap-org',
        'alg-geom',
        'ao-sci',
        'astro-ph',
        'atom-ph',
        'bayes-an',
        'chao-dyn',
        'chem-ph',
        'cmp-lg',
        'comp-gas',
        'cond-mat',
        'cs',
        'dg-ga',
        'funct-an',
        'gr-qc',
        'hep-ex',
        'hep-lat',
        'hep-ph',
        'hep-th',
        'math',
        'math-ph',
        'mtrl-th',
        'nlin',
        'nucl-ex',
        'nucl-th',
        'patt-sol',
        'physics',
        'plasm-ph',
        'q-alg',
        'q-bio',
        'quant-ph',
        'solv-int',
        'supr-con'
    ) THEN
        RETURN NULL;
    END IF;

    RETURN archive || '/' || parts[2] || parts[3] || parts[4];
END;
$$;

CREATE FUNCTION official_url_identifier_from_evidence_raw(
    source_path text,
    raw_value text
)
RETURNS jsonb
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    normalized text;
    trimmed text;
    lower_trimmed text;
    scheme text;
BEGIN
    CASE source_path
        WHEN 'meta[name="citation_doi"]@content',
             'meta[name="dc.identifier.doi"]@content',
             'meta[name="prism.doi"]@content'
        THEN
            scheme := 'doi';
            normalized := official_url_normalize_doi(raw_value);
        WHEN 'meta[name="citation_pmid"]@content' THEN
            scheme := 'pmid';
            normalized := official_url_trim_identifier_space(raw_value);
            IF normalized !~ '^[0-9]+$' THEN
                normalized := NULL;
            END IF;
        WHEN 'meta[name="citation_pmcid"]@content' THEN
            scheme := 'pmcid';
            normalized := upper(
                official_url_trim_identifier_space(raw_value)
            );
            IF normalized !~ '^PMC[0-9]+$' THEN
                normalized := NULL;
            END IF;
        WHEN 'meta[name="citation_arxiv_id"]@content' THEN
            scheme := 'arxiv';
            normalized := official_url_normalize_arxiv(raw_value);
        WHEN 'meta[name="dc.identifier"]@content' THEN
            trimmed := official_url_trim_identifier_space(raw_value);
            lower_trimmed := lower(trimmed);
            IF left(lower_trimmed, 4) = 'doi:'
                OR left(lower_trimmed, 16) =
                    'https://doi.org/'
                OR left(lower_trimmed, 15) =
                    'http://doi.org/'
            THEN
                scheme := 'doi';
                normalized := official_url_normalize_doi(trimmed);
            ELSIF left(lower_trimmed, 6) = 'arxiv:' THEN
                scheme := 'arxiv';
                normalized := official_url_normalize_arxiv(trimmed);
            ELSE
                RETURN NULL;
            END IF;
        ELSE
            RETURN NULL;
    END CASE;

    IF normalized IS NULL THEN
        RETURN NULL;
    END IF;
    RETURN jsonb_build_object(
        'scheme',
        scheme,
        'value',
        normalized
    );
END;
$$;

CREATE FUNCTION official_url_identifier_evidence_is_valid(
    evidence jsonb,
    observed jsonb
)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    identifier jsonb;
    scheme text;
    source_path text;
    raw_value text;
    normalized_identifier jsonb;
BEGIN
    IF jsonb_typeof(evidence) <> 'object'
        OR jsonb_typeof(observed) <> 'object'
        OR ARRAY(
            SELECT key_name
            FROM jsonb_object_keys(evidence) AS keys(key_name)
            ORDER BY key_name
        ) <> ARRAY[
            'identifier',
            'raw_value',
            'source_kind',
            'source_path'
        ]::text[]
        OR ARRAY(
            SELECT key_name
            FROM jsonb_object_keys(observed) AS keys(key_name)
            ORDER BY key_name
        ) <> ARRAY['scheme', 'value']::text[]
        OR jsonb_typeof(evidence->'identifier') <> 'object'
        OR evidence->'identifier' IS DISTINCT FROM observed
    THEN
        RETURN false;
    END IF;

    identifier := evidence->'identifier';
    IF ARRAY(
        SELECT key_name
        FROM jsonb_object_keys(identifier) AS keys(key_name)
        ORDER BY key_name
    ) <> ARRAY['scheme', 'value']::text[]
        OR jsonb_typeof(identifier->'scheme') <> 'string'
        OR jsonb_typeof(identifier->'value') <> 'string'
        OR jsonb_typeof(evidence->'source_kind') <> 'string'
        OR jsonb_typeof(evidence->'source_path') <> 'string'
        OR jsonb_typeof(evidence->'raw_value') <> 'string'
        OR evidence->>'source_kind' <> 'html_meta'
    THEN
        RETURN false;
    END IF;

    scheme := identifier->>'scheme';
    source_path := evidence->>'source_path';
    raw_value := evidence->>'raw_value';
    IF raw_value !~ '[^[:space:]]' THEN
        RETURN false;
    END IF;
    normalized_identifier :=
        official_url_identifier_from_evidence_raw(
            source_path,
            raw_value
        );
    RETURN normalized_identifier IS NOT NULL
        AND normalized_identifier = identifier
        AND normalized_identifier->>'scheme' = scheme;
END;
$$;

CREATE FUNCTION official_url_hostname(value text)
RETURNS text
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    remainder text;
    authority text;
BEGIN
    IF NOT official_url_is_accepted(value, false) THEN
        RETURN NULL;
    END IF;
    IF left(value, 8) = 'https://' THEN
        remainder := substr(value, 9);
    ELSE
        remainder := substr(value, 8);
    END IF;
    authority := split_part(
        split_part(remainder, '/', 1),
        '?',
        1
    );
    IF left(authority, 1) = '[' THEN
        RETURN lower(
            split_part(substr(authority, 2), ']', 1)
        );
    END IF;
    RETURN lower(split_part(authority, ':', 1));
END;
$$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_roles WHERE rolname = 'paper_hub_api'
    ) THEN
        CREATE ROLE paper_hub_api NOLOGIN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_roles WHERE rolname = 'paper_hub_worker'
    ) THEN
        CREATE ROLE paper_hub_worker NOLOGIN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_roles
        WHERE rolname = 'paper_hub_url_verifier'
    ) THEN
        CREATE ROLE paper_hub_url_verifier NOLOGIN;
    END IF;
END;
$$;

CREATE TABLE official_url_registry_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    registry_name text NOT NULL,
    policy_version text NOT NULL,
    file_sha256 text NOT NULL,
    host_count integer NOT NULL,
    imported_at timestamptz NOT NULL,
    sealed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT official_url_registry_versions_name_check
        CHECK (registry_name = 'official-url-hosts'),
    CONSTRAINT official_url_registry_versions_policy_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT official_url_registry_versions_file_sha256_check
        CHECK (file_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT official_url_registry_versions_host_count_check
        CHECK (host_count > 0),
    CONSTRAINT official_url_registry_versions_sealed_at_check
        CHECK (
            sealed_at IS NULL
            OR sealed_at >= imported_at
        ),
    CONSTRAINT official_url_registry_versions_policy_key
        UNIQUE (policy_version),
    CONSTRAINT official_url_registry_versions_file_sha256_key
        UNIQUE (file_sha256),
    CONSTRAINT official_url_registry_versions_id_policy_key
        UNIQUE (id, policy_version)
);

CREATE FUNCTION seal_official_url_registry_receipt()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    actual_host_count integer;
BEGIN
    IF OLD.sealed_at IS NOT NULL
       OR NEW.sealed_at IS NULL
       OR (to_jsonb(NEW) - 'sealed_at') <>
          (to_jsonb(OLD) - 'sealed_at') THEN
        RAISE EXCEPTION
            'official URL Registry receipt % only permits one NULL-to-non-NULL seal',
            OLD.id
            USING
                ERRCODE = '55000',
                CONSTRAINT =
                    'official_url_registry_versions_seal';
    END IF;

    SELECT count(*)
    INTO actual_host_count
    FROM official_url_host_registry
    WHERE registry_version_id = OLD.id;

    IF actual_host_count <> NEW.host_count THEN
        RAISE EXCEPTION
            'official URL Registry receipt % content mismatch: hosts %/%',
            NEW.id,
            actual_host_count,
            NEW.host_count
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'official_url_registry_versions_seal';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER official_url_registry_versions_insert_unsealed
BEFORE INSERT ON official_url_registry_versions
FOR EACH ROW
EXECUTE FUNCTION require_registry_receipt_insert_unsealed(
    'official_url_registry_versions_insert_unsealed'
);

CREATE TRIGGER official_url_registry_versions_seal
BEFORE UPDATE ON official_url_registry_versions
FOR EACH ROW
EXECUTE FUNCTION seal_official_url_registry_receipt();

CREATE TRIGGER official_url_registry_versions_immutable
BEFORE DELETE ON official_url_registry_versions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE CONSTRAINT TRIGGER official_url_registry_versions_require_seal
AFTER INSERT ON official_url_registry_versions
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION require_registry_receipt_sealed(
    'official_url_registry_versions_must_be_sealed'
);

CREATE TABLE official_url_host_registry (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    registry_version_id uuid NOT NULL,
    policy_version text NOT NULL,
    content_channel text NOT NULL
        REFERENCES content_channels(channel_key) ON DELETE RESTRICT,
    link_role text NOT NULL,
    hostname text NOT NULL,
    registry_source text NOT NULL,
    source_reference text NOT NULL,
    registered_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT official_url_host_registry_version_fkey
        FOREIGN KEY (registry_version_id, policy_version)
        REFERENCES official_url_registry_versions(
            id,
            policy_version
        )
        ON DELETE RESTRICT,
    CONSTRAINT official_url_host_registry_policy_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT official_url_host_registry_channel_role_check
        CHECK (
            (
                content_channel IN ('journal_published', 'accepted_early')
                AND link_role IN ('official_article', 'doi_url')
            )
            OR
            (
                content_channel = 'preprint'
                AND link_role IN ('official_preprint', 'doi_url')
            )
            OR
            (
                content_channel = 'conference_proceeding'
                AND link_role IN ('official_proceeding', 'doi_url')
            )
        ),
    CONSTRAINT official_url_host_registry_hostname_check
        CHECK (
            hostname = lower(hostname)
            AND hostname = btrim(hostname)
            AND length(hostname) <= 253
            AND hostname ~
                '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'
        ),
    CONSTRAINT official_url_host_registry_source_check
        CHECK (
            btrim(registry_source) <> ''
            AND registry_source = btrim(registry_source)
        ),
    CONSTRAINT official_url_host_registry_reference_check
        CHECK (
            btrim(source_reference) <> ''
            AND source_reference = btrim(source_reference)
        ),
    CONSTRAINT official_url_host_registry_exact_key
        UNIQUE (
            policy_version,
            content_channel,
            link_role,
            hostname
        )
);

CREATE INDEX idx_official_url_host_registry_lookup
    ON official_url_host_registry(
        policy_version,
        content_channel,
        link_role,
        hostname
    );

CREATE TRIGGER official_url_host_registry_parent_unsealed
BEFORE INSERT ON official_url_host_registry
FOR EACH ROW
EXECUTE FUNCTION require_unsealed_registry_parent(
    'official_url_registry_versions',
    'registry_version_id',
    'official_url_host_registry_parent_unsealed'
);

CREATE TRIGGER official_url_host_registry_immutable
BEFORE UPDATE OR DELETE ON official_url_host_registry
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_url_candidates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL
        REFERENCES ingestion_normalized_records(id) ON DELETE RESTRICT,
    source_record_id uuid NOT NULL
        REFERENCES source_records(id) ON DELETE RESTRICT,
    source_path text NOT NULL,
    content_channel text NOT NULL
        REFERENCES content_channels(channel_key) ON DELETE RESTRICT,
    link_role text NOT NULL,
    candidate_url text NOT NULL,
    parser_version text NOT NULL,
    identifier_scheme text NOT NULL,
    identifier_value text NOT NULL,
    asserted_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_url_candidates_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        ON DELETE RESTRICT,
    CONSTRAINT work_url_candidates_projection_source_work_fkey
        FOREIGN KEY (
            projection_assertion_id,
            source_record_id,
            work_id
        )
        REFERENCES ingestion_projection_assertions(
            id,
            source_record_uuid,
            work_id
        )
        ON DELETE RESTRICT,
    CONSTRAINT work_url_candidates_source_path_check
        CHECK (
            btrim(source_path) <> ''
            AND source_path = btrim(source_path)
        ),
    CONSTRAINT work_url_candidates_role_check
        CHECK (
            link_role IN (
                'official_article',
                'doi_url',
                'official_preprint',
                'official_proceeding',
                'auxiliary'
            )
        ),
    CONSTRAINT work_url_candidates_channel_role_check
        CHECK (
            (
                content_channel IN ('journal_published', 'accepted_early')
                AND link_role IN (
                    'official_article',
                    'doi_url',
                    'auxiliary'
                )
            )
            OR
            (
                content_channel = 'preprint'
                AND link_role IN (
                    'official_preprint',
                    'doi_url',
                    'auxiliary'
                )
            )
            OR
            (
                content_channel = 'conference_proceeding'
                AND link_role IN (
                    'official_proceeding',
                    'doi_url',
                    'auxiliary'
                )
            )
        ),
    CONSTRAINT work_url_candidates_url_check
        CHECK (
            official_url_is_accepted(candidate_url, true)
        ),
    CONSTRAINT work_url_candidates_parser_version_check
        CHECK (
            btrim(parser_version) <> ''
            AND parser_version = btrim(parser_version)
        ),
    CONSTRAINT work_url_candidates_identifier_scheme_check
        CHECK (
            identifier_scheme ~
                '^[a-z0-9]+(?:[._-][a-z0-9]+)*$'
            AND identifier_scheme = btrim(identifier_scheme)
        ),
    CONSTRAINT work_url_candidates_identifier_value_check
        CHECK (
            btrim(identifier_value) <> ''
            AND identifier_value = btrim(identifier_value)
            AND identifier_value !~ '[[:space:][:cntrl:]]'
        ),
    CONSTRAINT work_url_candidates_exact_revision_path_url_key
        UNIQUE (
            normalized_assertion_id,
            source_path,
            candidate_url
        )
);

CREATE INDEX idx_work_url_candidates_work
    ON work_url_candidates(
        work_id,
        asserted_at DESC,
        id DESC
    );

CREATE INDEX idx_work_url_candidates_projection
    ON work_url_candidates(
        projection_assertion_id,
        normalized_assertion_id,
        id
    );

CREATE FUNCTION enforce_work_url_candidate_exact_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    assertion_normalized_id uuid;
    assertion_source_record_id uuid;
    assertion_work_id uuid;
    normalized_schema text;
    normalized_payload jsonb;
    source_time timestamptz;
BEGIN
    SELECT
        assertion.normalized_assertion_id,
        assertion.source_record_uuid,
        assertion.work_id,
        normalized.payload_schema_version,
        normalized.normalized_payload,
        source_record.source_time
    INTO
        assertion_normalized_id,
        assertion_source_record_id,
        assertion_work_id,
        normalized_schema,
        normalized_payload,
        source_time
    FROM ingestion_projection_assertions AS assertion
    JOIN ingestion_normalized_records AS normalized
      ON normalized.id = assertion.normalized_assertion_id
     AND normalized.raw_event_id = assertion.raw_event_id
     AND normalized.source_record_uuid = assertion.source_record_uuid
    JOIN source_records AS source_record
      ON source_record.id = assertion.source_record_uuid
    WHERE assertion.id = NEW.projection_assertion_id;

    IF NOT FOUND
        OR assertion_normalized_id <> NEW.normalized_assertion_id
        OR assertion_source_record_id <> NEW.source_record_id
        OR assertion_work_id <> NEW.work_id
    THEN
        RAISE EXCEPTION
            'official URL candidate does not bind the exact projection revision'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_url_candidates_exact_projection_revision';
    END IF;

    IF normalized_schema <> 'normalized-record/v4'
        OR jsonb_typeof(normalized_payload->'url_candidates') <> 'array'
    THEN
        RAISE EXCEPTION
            'official URL candidates require normalized-record/v4 url_candidates'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_url_candidates_normalized_v4';
    END IF;

    IF NEW.asserted_at <> source_time THEN
        RAISE EXCEPTION
            'official URL candidate asserted_at must equal source_time'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_url_candidates_source_time';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM jsonb_array_elements(
            normalized_payload->'url_candidates'
        ) AS candidate(value)
        WHERE candidate.value->>'url' = NEW.candidate_url
          AND candidate.value->>'source_path' = NEW.source_path
          AND candidate.value->>'content_channel' =
              NEW.content_channel
          AND candidate.value->>'link_role' = NEW.link_role
          AND candidate.value->>'parser_version' =
              NEW.parser_version
          AND candidate.value->>'identifier_scheme' =
              NEW.identifier_scheme
          AND candidate.value->>'identifier_value' =
              NEW.identifier_value
    ) THEN
        RAISE EXCEPTION
            'official URL candidate is absent from the exact normalized revision'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_url_candidates_exact_payload_entry';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER work_url_candidates_exact_source
BEFORE INSERT ON work_url_candidates
FOR EACH ROW
EXECUTE FUNCTION enforce_work_url_candidate_exact_source();

CREATE TRIGGER work_url_candidates_immutable
BEFORE UPDATE OR DELETE ON work_url_candidates
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_url_candidate_import_rejections (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    projection_assertion_id uuid NOT NULL,
    normalized_assertion_id uuid NOT NULL
        REFERENCES ingestion_normalized_records(id) ON DELETE RESTRICT,
    source_record_id uuid NOT NULL
        REFERENCES source_records(id) ON DELETE RESTRICT,
    candidate_ordinal integer NOT NULL,
    candidate_payload_hash text NOT NULL,
    error_code text NOT NULL,
    error_detail text NOT NULL,
    rejected_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_url_candidate_rejections_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        ON DELETE RESTRICT,
    CONSTRAINT work_url_candidate_rejections_projection_source_work_fkey
        FOREIGN KEY (
            projection_assertion_id,
            source_record_id,
            work_id
        )
        REFERENCES ingestion_projection_assertions(
            id,
            source_record_uuid,
            work_id
        )
        ON DELETE RESTRICT,
    CONSTRAINT work_url_candidate_rejections_ordinal_check
        CHECK (candidate_ordinal >= 0),
    CONSTRAINT work_url_candidate_rejections_payload_hash_check
        CHECK (
            candidate_payload_hash ~ '^[0-9a-f]{64}$'
        ),
    CONSTRAINT work_url_candidate_rejections_error_code_check
        CHECK (
            error_code IN (
                'invalid_json_shape',
                'parser_version_mismatch',
                'invalid_content_channel',
                'invalid_link_role',
                'invalid_identifier',
                'invalid_candidate',
                'conflicting_duplicate'
            )
        ),
    CONSTRAINT work_url_candidate_rejections_error_detail_check
        CHECK (
            btrim(error_detail) <> ''
            AND error_detail = btrim(error_detail)
            AND length(error_detail) <= 2000
        ),
    CONSTRAINT work_url_candidate_rejections_exact_item_key
        UNIQUE (
            normalized_assertion_id,
            candidate_ordinal,
            candidate_payload_hash
        )
);

CREATE INDEX idx_work_url_candidate_rejections_projection
    ON work_url_candidate_import_rejections(
        projection_assertion_id,
        normalized_assertion_id,
        candidate_ordinal
    );

CREATE FUNCTION enforce_work_url_candidate_rejection_exact_source()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    assertion_normalized_id uuid;
    assertion_source_record_id uuid;
    assertion_work_id uuid;
    normalized_schema text;
    normalized_payload jsonb;
    candidate_payload jsonb;
    expected_payload_hash text;
BEGIN
    SELECT
        assertion.normalized_assertion_id,
        assertion.source_record_uuid,
        assertion.work_id,
        normalized.payload_schema_version,
        normalized.normalized_payload
    INTO
        assertion_normalized_id,
        assertion_source_record_id,
        assertion_work_id,
        normalized_schema,
        normalized_payload
    FROM ingestion_projection_assertions AS assertion
    JOIN ingestion_normalized_records AS normalized
      ON normalized.id = assertion.normalized_assertion_id
     AND normalized.raw_event_id = assertion.raw_event_id
     AND normalized.source_record_uuid = assertion.source_record_uuid
    WHERE assertion.id = NEW.projection_assertion_id;

    IF NOT FOUND
        OR assertion_normalized_id <> NEW.normalized_assertion_id
        OR assertion_source_record_id <> NEW.source_record_id
        OR assertion_work_id <> NEW.work_id
        OR normalized_schema <> 'normalized-record/v4'
        OR jsonb_typeof(normalized_payload->'url_candidates') <> 'array'
        OR NEW.candidate_ordinal >= jsonb_array_length(
            normalized_payload->'url_candidates'
        )
    THEN
        RAISE EXCEPTION
            'official URL candidate rejection does not bind an exact normalized v4 item'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_url_candidate_rejections_exact_source';
    END IF;

    candidate_payload := jsonb_array_element(
        normalized_payload->'url_candidates',
        NEW.candidate_ordinal
    );
    expected_payload_hash := encode(
        sha256(convert_to(candidate_payload::text, 'UTF8')),
        'hex'
    );
    IF NEW.candidate_payload_hash <> expected_payload_hash THEN
        RAISE EXCEPTION
            'official URL candidate rejection payload hash is not exact'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_url_candidate_rejections_payload_binding';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER work_url_candidate_rejections_exact_source
BEFORE INSERT ON work_url_candidate_import_rejections
FOR EACH ROW
EXECUTE FUNCTION enforce_work_url_candidate_rejection_exact_source();

CREATE TRIGGER work_url_candidate_rejections_immutable
BEFORE UPDATE OR DELETE ON work_url_candidate_import_rejections
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE work_url_verifications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    candidate_id uuid NOT NULL
        REFERENCES work_url_candidates(id) ON DELETE RESTRICT,
    source_url text NOT NULL,
    final_url text NOT NULL,
    redirect_chain jsonb NOT NULL,
    http_status integer NOT NULL,
    expected_identifier_scheme text NOT NULL,
    expected_identifier_value text NOT NULL,
    observed_identifiers jsonb NOT NULL,
    identifier_evidence jsonb NOT NULL,
    response_metadata jsonb NOT NULL,
    matched_identifier_scheme text,
    matched_identifier_value text,
    identifier_match boolean NOT NULL,
    verification_state text NOT NULL,
    checked_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    verifier_version text NOT NULL,
    policy_version text NOT NULL,
    failure_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_url_verifications_source_url_check
        CHECK (
            official_url_is_accepted(source_url, true)
        ),
    CONSTRAINT work_url_verifications_final_url_check
        CHECK (
            official_url_is_accepted(final_url, false)
        ),
    CONSTRAINT work_url_verifications_redirect_chain_check
        CHECK (
            (
                verification_state = 'failed'
                AND failure_code IN (
                    'host_not_allowed',
                    'unsafe_resolved_address',
                    'dns_resolution_failed',
                    'network_error',
                    'response_body_limit_exceeded'
                )
                AND redirect_chain = '[]'::jsonb
                AND source_url = final_url
                AND http_status = 0
            )
            OR
            (
                official_url_redirect_chain_has_basic_shape(
                    redirect_chain,
                    source_url,
                    final_url,
                    http_status
                )
                AND (
                    verification_state = 'failed'
                    OR official_url_redirect_chain_is_valid(
                        redirect_chain,
                        source_url,
                        final_url,
                        http_status
                    )
                )
            )
        ),
    CONSTRAINT work_url_verifications_http_status_check
        CHECK (
            http_status BETWEEN 100 AND 599
            OR (
                verification_state = 'failed'
                AND failure_code IN (
                    'host_not_allowed',
                    'unsafe_resolved_address',
                    'dns_resolution_failed',
                    'network_error',
                    'response_body_limit_exceeded'
                )
                AND http_status = 0
            )
        ),
    CONSTRAINT work_url_verifications_expected_scheme_check
        CHECK (
            expected_identifier_scheme ~
                '^[a-z0-9]+(?:[._-][a-z0-9]+)*$'
            AND expected_identifier_scheme =
                btrim(expected_identifier_scheme)
        ),
    CONSTRAINT work_url_verifications_expected_value_check
        CHECK (
            btrim(expected_identifier_value) <> ''
            AND expected_identifier_value =
                btrim(expected_identifier_value)
            AND expected_identifier_value !~
                '[[:space:][:cntrl:]]'
        ),
    CONSTRAINT work_url_verifications_observed_identifiers_check
        CHECK (jsonb_typeof(observed_identifiers) = 'array'),
    CONSTRAINT work_url_verifications_identifier_evidence_check
        CHECK (jsonb_typeof(identifier_evidence) = 'array'),
    CONSTRAINT work_url_verifications_response_metadata_check
        CHECK (jsonb_typeof(response_metadata) = 'object'),
    CONSTRAINT work_url_verifications_matched_identifier_shape_check
        CHECK (
            (
                identifier_match
                AND matched_identifier_scheme =
                    expected_identifier_scheme
                AND matched_identifier_value =
                    expected_identifier_value
            )
            OR
            (
                NOT identifier_match
                AND matched_identifier_scheme IS NULL
                AND matched_identifier_value IS NULL
            )
        ),
    CONSTRAINT work_url_verifications_state_check
        CHECK (verification_state IN ('verified', 'failed')),
    CONSTRAINT work_url_verifications_interval_check
        CHECK (expires_at > checked_at),
    CONSTRAINT work_url_verifications_verifier_version_check
        CHECK (
            btrim(verifier_version) <> ''
            AND verifier_version = btrim(verifier_version)
        ),
    CONSTRAINT work_url_verifications_policy_version_check
        CHECK (
            btrim(policy_version) <> ''
            AND policy_version = btrim(policy_version)
        ),
    CONSTRAINT work_url_verifications_failure_code_check
        CHECK (
            failure_code IS NULL
            OR failure_code IN (
                'invalid_redirect_chain',
                'redirect_limit_exceeded',
                'redirect_loop',
                'non_https_final_url',
                'http_status_not_successful',
                'ineligible_link_role',
                'missing_stable_identifier',
                'identifier_mismatch',
                'expired',
                'host_not_allowed',
                'unsafe_resolved_address',
                'dns_resolution_failed',
                'network_error',
                'response_body_limit_exceeded'
            )
        ),
    CONSTRAINT work_url_verifications_state_shape_check
        CHECK (
            (
                verification_state = 'verified'
                AND failure_code IS NULL
                AND identifier_match
                AND http_status BETWEEN 200 AND 299
                AND official_url_is_accepted(final_url, true)
            )
            OR
            (
                verification_state = 'failed'
                AND failure_code IS NOT NULL
            )
        )
);

CREATE INDEX idx_work_url_verifications_candidate_history
    ON work_url_verifications(
        candidate_id,
        checked_at DESC,
        id DESC
    );

CREATE FUNCTION enforce_work_url_verification_candidate()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    candidate_url text;
    candidate_channel text;
    candidate_role text;
    candidate_identifier_scheme text;
    candidate_identifier_value text;
    evidence_count integer;
BEGIN
    SELECT
        candidate.candidate_url,
        candidate.content_channel,
        candidate.link_role,
        candidate.identifier_scheme,
        candidate.identifier_value
    INTO
        candidate_url,
        candidate_channel,
        candidate_role,
        candidate_identifier_scheme,
        candidate_identifier_value
    FROM work_url_candidates AS candidate
    WHERE candidate.id = NEW.candidate_id;

    IF NOT FOUND
        OR NEW.source_url <> candidate_url
        OR NEW.expected_identifier_scheme <>
            candidate_identifier_scheme
        OR NEW.expected_identifier_value <>
            candidate_identifier_value
    THEN
        RAISE EXCEPTION
            'official URL verification conflicts with its source candidate'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_url_verifications_candidate_consistency';
    END IF;

    IF jsonb_typeof(NEW.identifier_evidence) <> 'array'
        OR jsonb_typeof(NEW.observed_identifiers) <> 'array'
        OR jsonb_array_length(NEW.identifier_evidence) <>
            jsonb_array_length(NEW.observed_identifiers)
        OR EXISTS (
            SELECT 1
            FROM jsonb_array_elements(
                NEW.identifier_evidence
            ) WITH ORDINALITY AS evidence(value, ordinal)
            FULL JOIN jsonb_array_elements(
                NEW.observed_identifiers
            ) WITH ORDINALITY AS observed(value, ordinal)
              USING (ordinal)
            WHERE evidence.value IS NULL
               OR observed.value IS NULL
               OR NOT official_url_identifier_evidence_is_valid(
                    evidence.value,
                    observed.value
               )
        )
    THEN
        RAISE EXCEPTION
            'official URL identifier evidence conflicts with observed identifiers'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_url_verifications_identifier_evidence_consistency';
    END IF;

    IF NEW.verification_state = 'verified'
        AND candidate_role NOT IN (
            'official_article',
            'doi_url',
            'official_preprint',
            'official_proceeding'
        )
    THEN
        RAISE EXCEPTION
            'verified official URL requires a projectable source candidate'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'work_url_verifications_projectable_role';
    END IF;

    IF NEW.verification_state = 'verified' THEN
        IF EXISTS (
            SELECT 1
            FROM jsonb_array_elements(NEW.redirect_chain)
                AS hop(value)
            WHERE NOT EXISTS (
                SELECT 1
                FROM official_url_host_registry AS registry
                JOIN official_url_registry_versions AS version
                  ON version.id = registry.registry_version_id
                 AND version.policy_version =
                        registry.policy_version
                WHERE version.policy_version =
                        NEW.policy_version
                  AND version.sealed_at IS NOT NULL
                  AND registry.content_channel =
                        candidate_channel
                  AND registry.link_role =
                        candidate_role
                  AND registry.hostname =
                        official_url_hostname(
                            hop.value->>'url'
                        )
            )
        ) THEN
            RAISE EXCEPTION
                'verified official URL redirect hosts require explicit registry rows'
                USING
                    ERRCODE = '23514',
                    CONSTRAINT =
                        'work_url_verifications_registered_hosts';
        END IF;

        SELECT count(*)
        INTO evidence_count
        FROM jsonb_array_elements(NEW.identifier_evidence)
            AS evidence(value)
        WHERE evidence.value->'identifier'->>'scheme' =
                NEW.expected_identifier_scheme
          AND evidence.value->'identifier'->>'value' =
                NEW.expected_identifier_value;

        IF evidence_count = 0 THEN
            RAISE EXCEPTION
                'verified official URL requires matching structured identifier evidence'
                USING
                    ERRCODE = '23514',
                    CONSTRAINT =
                        'work_url_verifications_matching_identifier_evidence';
        END IF;
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER work_url_verifications_candidate_consistency
BEFORE INSERT ON work_url_verifications
FOR EACH ROW
EXECUTE FUNCTION enforce_work_url_verification_candidate();

CREATE TRIGGER work_url_verifications_immutable
BEFORE UPDATE OR DELETE ON work_url_verifications
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE current_work_official_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE RESTRICT,
    content_channel text NOT NULL
        REFERENCES content_channels(channel_key) ON DELETE RESTRICT,
    link_role text NOT NULL,
    verification_id uuid NOT NULL UNIQUE
        REFERENCES work_url_verifications(id) ON DELETE RESTRICT,
    official_url text NOT NULL,
    projection_version bigint NOT NULL,
    projected_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT current_work_official_links_role_check
        CHECK (
            link_role IN (
                'official_article',
                'doi_url',
                'official_preprint',
                'official_proceeding'
            )
        ),
    CONSTRAINT current_work_official_links_channel_role_check
        CHECK (
            (
                content_channel IN ('journal_published', 'accepted_early')
                AND link_role IN ('official_article', 'doi_url')
            )
            OR
            (
                content_channel = 'preprint'
                AND link_role IN ('official_preprint', 'doi_url')
            )
            OR
            (
                content_channel = 'conference_proceeding'
                AND link_role IN ('official_proceeding', 'doi_url')
            )
        ),
    CONSTRAINT current_work_official_links_url_check
        CHECK (
            official_url_is_accepted(official_url, true)
        ),
    CONSTRAINT current_work_official_links_version_check
        CHECK (projection_version > 0),
    CONSTRAINT current_work_official_links_interval_check
        CHECK (expires_at > projected_at),
    CONSTRAINT current_work_official_links_work_role_version_key
        UNIQUE (work_id, link_role, projection_version)
);

CREATE INDEX idx_current_work_official_links_lookup
    ON current_work_official_links(
        work_id,
        link_role,
        projection_version DESC,
        id DESC
    );

CREATE FUNCTION enforce_current_work_official_link_projection()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    candidate_work_id uuid;
    candidate_channel text;
    candidate_role text;
    verification_state text;
    verification_final_url text;
    verification_checked_at timestamptz;
    verification_expires_at timestamptz;
    latest_checked_at timestamptz;
    expected_version bigint;
BEGIN
    SELECT
        candidate.work_id,
        candidate.content_channel,
        candidate.link_role,
        verification.verification_state,
        verification.final_url,
        verification.checked_at,
        verification.expires_at
    INTO
        candidate_work_id,
        candidate_channel,
        candidate_role,
        verification_state,
        verification_final_url,
        verification_checked_at,
        verification_expires_at
    FROM work_url_verifications AS verification
    JOIN work_url_candidates AS candidate
      ON candidate.id = verification.candidate_id
    WHERE verification.id = NEW.verification_id;

    IF NOT FOUND
        OR verification_state <> 'verified'
        OR NEW.work_id <> candidate_work_id
        OR NEW.content_channel <> candidate_channel
        OR NEW.link_role <> candidate_role
        OR NEW.official_url <> verification_final_url
        OR NEW.expires_at <> verification_expires_at
        OR NEW.projected_at < verification_checked_at
        OR NEW.projected_at >= verification_expires_at
    THEN
        RAISE EXCEPTION
            'current official link conflicts with its verification'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'current_work_official_links_verification_consistency';
    END IF;

    SELECT max(verification.checked_at)
    INTO latest_checked_at
    FROM current_work_official_links AS link
    JOIN work_url_verifications AS verification
      ON verification.id = link.verification_id
    WHERE link.work_id = NEW.work_id
      AND link.link_role = NEW.link_role;

    IF latest_checked_at IS NOT NULL
        AND verification_checked_at < latest_checked_at
    THEN
        RAISE EXCEPTION
            'current official link cannot regress verification checked_at'
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'current_work_official_links_checked_at_monotonic';
    END IF;

    SELECT COALESCE(max(projection_version), 0) + 1
    INTO expected_version
    FROM current_work_official_links
    WHERE work_id = NEW.work_id
      AND link_role = NEW.link_role;

    IF NEW.projection_version <> expected_version THEN
        RAISE EXCEPTION
            'current official link projection version must be %, got %',
            expected_version,
            NEW.projection_version
            USING
                ERRCODE = '23514',
                CONSTRAINT =
                    'current_work_official_links_next_version';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER current_work_official_links_projection
BEFORE INSERT ON current_work_official_links
FOR EACH ROW
EXECUTE FUNCTION enforce_current_work_official_link_projection();

CREATE TRIGGER current_work_official_links_immutable
BEFORE UPDATE OR DELETE ON current_work_official_links
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE FUNCTION persist_official_url_verification_receipt(
    p_candidate_id uuid,
    p_source_url text,
    p_final_url text,
    p_redirect_chain jsonb,
    p_http_status integer,
    p_expected_identifier_scheme text,
    p_expected_identifier_value text,
    p_observed_identifiers jsonb,
    p_identifier_evidence jsonb,
    p_response_metadata jsonb,
    p_matched_identifier_scheme text,
    p_matched_identifier_value text,
    p_identifier_match boolean,
    p_verification_state text,
    p_checked_at timestamptz,
    p_expires_at timestamptz,
    p_verifier_version text,
    p_policy_version text,
    p_failure_code text
)
RETURNS uuid
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    INSERT INTO public.work_url_verifications (
        candidate_id,
        source_url,
        final_url,
        redirect_chain,
        http_status,
        expected_identifier_scheme,
        expected_identifier_value,
        observed_identifiers,
        identifier_evidence,
        response_metadata,
        matched_identifier_scheme,
        matched_identifier_value,
        identifier_match,
        verification_state,
        checked_at,
        expires_at,
        verifier_version,
        policy_version,
        failure_code
    ) VALUES (
        p_candidate_id,
        p_source_url,
        p_final_url,
        p_redirect_chain,
        p_http_status,
        p_expected_identifier_scheme,
        p_expected_identifier_value,
        p_observed_identifiers,
        p_identifier_evidence,
        p_response_metadata,
        p_matched_identifier_scheme,
        p_matched_identifier_value,
        p_identifier_match,
        p_verification_state,
        p_checked_at,
        p_expires_at,
        p_verifier_version,
        p_policy_version,
        p_failure_code
    )
    RETURNING id
$$;

REVOKE ALL
ON FUNCTION persist_official_url_verification_receipt(
    uuid,
    text,
    text,
    jsonb,
    integer,
    text,
    text,
    jsonb,
    jsonb,
    jsonb,
    text,
    text,
    boolean,
    text,
    timestamptz,
    timestamptz,
    text,
    text,
    text
)
FROM PUBLIC;

GRANT USAGE ON SCHEMA public
TO paper_hub_api, paper_hub_worker, paper_hub_url_verifier;

GRANT SELECT ON ALL TABLES IN SCHEMA public
TO paper_hub_api, paper_hub_worker, paper_hub_url_verifier;

GRANT INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public
TO paper_hub_worker;

GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public
TO paper_hub_worker, paper_hub_url_verifier;

GRANT INSERT ON
    work_url_candidates,
    work_url_candidate_import_rejections,
    current_work_official_links
TO paper_hub_url_verifier;

REVOKE INSERT, UPDATE, DELETE ON
    official_url_registry_versions,
    official_url_host_registry,
    work_url_verifications
FROM paper_hub_worker;

GRANT EXECUTE
ON FUNCTION persist_official_url_verification_receipt(
    uuid,
    text,
    text,
    jsonb,
    integer,
    text,
    text,
    jsonb,
    jsonb,
    jsonb,
    text,
    text,
    boolean,
    text,
    timestamptz,
    timestamptz,
    text,
    text,
    text
)
TO paper_hub_url_verifier;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
GRANT SELECT ON TABLES
TO paper_hub_api, paper_hub_worker, paper_hub_url_verifier;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
GRANT INSERT, UPDATE, DELETE ON TABLES
TO paper_hub_worker;

ALTER DEFAULT PRIVILEGES IN SCHEMA public
GRANT USAGE, SELECT ON SEQUENCES
TO paper_hub_worker, paper_hub_url_verifier;
