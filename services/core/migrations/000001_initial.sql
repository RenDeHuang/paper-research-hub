CREATE FUNCTION reject_immutable_row()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION '% rows are immutable', TG_TABLE_NAME
        USING ERRCODE = '55000';
END;
$$;

CREATE TABLE venues (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    venue_type text NOT NULL,
    display_title text NOT NULL,
    issn_l text,
    issn text,
    eissn text,
    source_scheme text,
    source_identifier text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT venues_type_check
        CHECK (venue_type IN ('journal', 'conference', 'preprint', 'repository')),
    CONSTRAINT venues_display_title_check
        CHECK (btrim(display_title) <> ''),
    CONSTRAINT venues_issn_l_format_check
        CHECK (issn_l IS NULL OR issn_l ~ '^[0-9]{4}-[0-9]{3}[0-9X]$'),
    CONSTRAINT venues_issn_format_check
        CHECK (issn IS NULL OR issn ~ '^[0-9]{4}-[0-9]{3}[0-9X]$'),
    CONSTRAINT venues_eissn_format_check
        CHECK (eissn IS NULL OR eissn ~ '^[0-9]{4}-[0-9]{3}[0-9X]$'),
    CONSTRAINT venues_controlled_source_check
        CHECK (
            (source_scheme IS NULL AND source_identifier IS NULL)
            OR (
                source_scheme IS NOT NULL
                AND source_identifier IS NOT NULL
                AND
                source_scheme IN ('openalex', 'crossref', 'doaj', 'issn_portal', 'nlm')
                AND btrim(source_identifier) <> ''
            )
        ),
    UNIQUE (issn_l),
    UNIQUE (issn),
    UNIQUE (eissn),
    UNIQUE (source_scheme, source_identifier)
);

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

CREATE TABLE venue_aliases (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    venue_id uuid NOT NULL REFERENCES venues(id) ON DELETE CASCADE,
    alias text NOT NULL,
    source text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT venue_aliases_alias_check CHECK (btrim(alias) <> ''),
    CONSTRAINT venue_aliases_source_check CHECK (btrim(source) <> ''),
    UNIQUE (venue_id, alias, source)
);

CREATE TABLE works (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    canonical_key text NOT NULL UNIQUE,
    status text NOT NULL,
    title text NOT NULL,
    abstract text,
    published_at timestamptz,
    venue_id uuid REFERENCES venues(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT works_canonical_key_check
        CHECK (canonical_key ~ '^(doi|arxiv|openreview|s2|openalex):[^[:space:]]+$'),
    CONSTRAINT works_status_check
        CHECK (status IN ('active', 'withdrawn', 'retracted', 'rejected', 'superseded')),
    CONSTRAINT works_title_check CHECK (btrim(title) <> '')
);

CREATE INDEX idx_works_venue_id ON works(venue_id);
CREATE INDEX idx_works_published_at ON works(published_at DESC);

CREATE TABLE source_records (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source text NOT NULL,
    source_record_id text NOT NULL,
    source_identity jsonb NOT NULL,
    source_time timestamptz NOT NULL,
    content_hash text NOT NULL,
    raw_payload jsonb NOT NULL,
    retrieved_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT source_records_source_check CHECK (btrim(source) <> ''),
    CONSTRAINT source_records_source_record_id_check CHECK (btrim(source_record_id) <> ''),
    CONSTRAINT source_records_content_hash_check CHECK (btrim(content_hash) <> ''),
    CONSTRAINT source_records_source_identity_json_check
        CHECK (jsonb_typeof(source_identity) = 'object'),
    CONSTRAINT source_records_raw_payload_json_check
        CHECK (jsonb_typeof(raw_payload) = 'object'),
    UNIQUE (source, source_record_id, content_hash)
);

CREATE INDEX idx_source_records_source_identity ON source_records USING gin(source_identity);

CREATE TRIGGER source_records_immutable
BEFORE UPDATE OR DELETE ON source_records
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

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

CREATE TABLE paper_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    source_record_id uuid NOT NULL,
    version_label text NOT NULL,
    status text NOT NULL,
    released_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT paper_versions_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT paper_versions_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT paper_versions_version_label_check CHECK (btrim(version_label) <> ''),
    CONSTRAINT paper_versions_status_check
        CHECK (status IN ('active', 'withdrawn', 'retracted', 'rejected', 'superseded')),
    UNIQUE (work_id, version_label)
);

CREATE INDEX idx_paper_versions_source_record_id ON paper_versions(source_record_id);

CREATE TABLE external_identifiers (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    source_record_id uuid,
    scheme text NOT NULL,
    normalized_value text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT external_identifiers_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT external_identifiers_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT external_identifiers_scheme_check
        CHECK (scheme IN ('doi', 'arxiv', 'openreview', 's2', 'openalex', 'pmid', 'pmcid')),
    CONSTRAINT external_identifiers_normalized_value_check
        CHECK (btrim(normalized_value) <> ''),
    UNIQUE (scheme, normalized_value)
);

CREATE INDEX idx_external_identifiers_work_id ON external_identifiers(work_id);
CREATE INDEX idx_external_identifiers_source_record_id ON external_identifiers(source_record_id);

CREATE TABLE field_assertions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid REFERENCES works(id) ON DELETE SET NULL,
    source_record_id uuid NOT NULL,
    field_name text NOT NULL,
    asserted_value jsonb NOT NULL,
    asserted_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT field_assertions_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT field_assertions_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT field_assertions_field_name_check CHECK (btrim(field_name) <> ''),
    CONSTRAINT field_assertions_value_json_check
        CHECK (jsonb_typeof(asserted_value) = 'object')
);

CREATE INDEX idx_field_assertions_work_id ON field_assertions(work_id);
CREATE INDEX idx_field_assertions_source_record_id ON field_assertions(source_record_id);
CREATE INDEX idx_field_assertions_field_name ON field_assertions(field_name);

CREATE TABLE authors (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name text NOT NULL,
    orcid text UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT authors_display_name_check CHECK (btrim(display_name) <> ''),
    CONSTRAINT authors_orcid_check
        CHECK (orcid IS NULL OR orcid ~ '^[0-9]{4}-[0-9]{4}-[0-9]{4}-[0-9]{3}[0-9X]$')
);

CREATE TABLE institutions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name text NOT NULL,
    ror_id text UNIQUE,
    country_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT institutions_display_name_check CHECK (btrim(display_name) <> ''),
    CONSTRAINT institutions_country_code_check
        CHECK (country_code IS NULL OR country_code ~ '^[A-Z]{2}$')
);

CREATE TABLE work_authors (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    author_id uuid NOT NULL REFERENCES authors(id) ON DELETE RESTRICT,
    institution_id uuid REFERENCES institutions(id) ON DELETE SET NULL,
    author_position integer NOT NULL,
    is_corresponding boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_authors_position_check CHECK (author_position > 0),
    UNIQUE (work_id, author_id),
    UNIQUE (work_id, author_position)
);

CREATE INDEX idx_work_authors_author_id ON work_authors(author_id);
CREATE INDEX idx_work_authors_institution_id ON work_authors(institution_id);

CREATE TABLE topics (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    description text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT topics_name_check CHECK (btrim(name) <> '')
);

CREATE TABLE work_topics (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    topic_id uuid NOT NULL REFERENCES topics(id) ON DELETE RESTRICT,
    confidence numeric NOT NULL DEFAULT 1,
    source_record_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_topics_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_topics_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_topics_confidence_check CHECK (confidence >= 0 AND confidence <= 1),
    UNIQUE (work_id, topic_id)
);

CREATE INDEX idx_work_topics_topic_id ON work_topics(topic_id);
CREATE INDEX idx_work_topics_source_record_id ON work_topics(source_record_id);

CREATE TABLE methods (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    description text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT methods_name_check CHECK (btrim(name) <> '')
);

CREATE TABLE work_methods (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    method_id uuid NOT NULL REFERENCES methods(id) ON DELETE RESTRICT,
    confidence numeric NOT NULL DEFAULT 1,
    source_record_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_methods_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT work_methods_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT work_methods_confidence_check CHECK (confidence >= 0 AND confidence <= 1),
    UNIQUE (work_id, method_id)
);

CREATE INDEX idx_work_methods_method_id ON work_methods(method_id);
CREATE INDEX idx_work_methods_source_record_id ON work_methods(source_record_id);

CREATE TABLE datasets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    canonical_url text,
    description text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT datasets_name_check CHECK (btrim(name) <> '')
);

CREATE TABLE work_datasets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    dataset_id uuid NOT NULL REFERENCES datasets(id) ON DELETE RESTRICT,
    relation_type text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_datasets_relation_type_check
        CHECK (relation_type IN ('used', 'introduced', 'evaluated')),
    UNIQUE (work_id, dataset_id, relation_type)
);

CREATE INDEX idx_work_datasets_dataset_id ON work_datasets(dataset_id);

CREATE TABLE benchmarks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    canonical_url text,
    description text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT benchmarks_name_check CHECK (btrim(name) <> '')
);

CREATE TABLE work_benchmarks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    benchmark_id uuid NOT NULL REFERENCES benchmarks(id) ON DELETE RESTRICT,
    relation_type text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_benchmarks_relation_type_check
        CHECK (relation_type IN ('used', 'introduced', 'evaluated')),
    UNIQUE (work_id, benchmark_id, relation_type)
);

CREATE INDEX idx_work_benchmarks_benchmark_id ON work_benchmarks(benchmark_id);

CREATE TABLE models (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE,
    canonical_url text,
    description text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT models_name_check CHECK (btrim(name) <> '')
);

CREATE TABLE work_models (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    model_id uuid NOT NULL REFERENCES models(id) ON DELETE RESTRICT,
    relation_type text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_models_relation_type_check
        CHECK (relation_type IN ('used', 'introduced', 'evaluated')),
    UNIQUE (work_id, model_id, relation_type)
);

CREATE INDEX idx_work_models_model_id ON work_models(model_id);

CREATE TABLE code_repositories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    canonical_url text NOT NULL UNIQUE,
    host text NOT NULL,
    owner_name text NOT NULL,
    repository_name text NOT NULL,
    archived_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT code_repositories_url_check CHECK (canonical_url ~ '^https?://'),
    CONSTRAINT code_repositories_host_check CHECK (btrim(host) <> ''),
    CONSTRAINT code_repositories_owner_check CHECK (btrim(owner_name) <> ''),
    CONSTRAINT code_repositories_name_check CHECK (btrim(repository_name) <> '')
);

CREATE TABLE work_code_repositories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    repository_id uuid NOT NULL REFERENCES code_repositories(id) ON DELETE RESTRICT,
    relation_type text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT work_code_repositories_relation_type_check
        CHECK (relation_type IN ('official', 'evaluation', 'community')),
    UNIQUE (work_id, repository_id, relation_type)
);

CREATE INDEX idx_work_code_repositories_repository_id
    ON work_code_repositories(repository_id);

CREATE TABLE metric_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid REFERENCES works(id) ON DELETE CASCADE,
    code_repository_id uuid REFERENCES code_repositories(id) ON DELETE CASCADE,
    metric_name text NOT NULL,
    metric_value numeric NOT NULL,
    observed_at timestamptz NOT NULL,
    source text,
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT metric_snapshots_exactly_one_target_check
        CHECK (num_nonnulls(work_id, code_repository_id) = 1),
    CONSTRAINT metric_snapshots_metric_name_check CHECK (btrim(metric_name) <> ''),
    CONSTRAINT metric_snapshots_metric_value_check CHECK (metric_value >= 0),
    CONSTRAINT metric_snapshots_metadata_json_check CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE NULLS NOT DISTINCT (work_id, code_repository_id, metric_name, observed_at)
);

CREATE INDEX idx_metric_snapshots_work_observed_at
    ON metric_snapshots(work_id, observed_at DESC)
    WHERE work_id IS NOT NULL;
CREATE INDEX idx_metric_snapshots_repository_observed_at
    ON metric_snapshots(code_repository_id, observed_at DESC)
    WHERE code_repository_id IS NOT NULL;

CREATE TABLE ranking_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_work_id uuid REFERENCES works(id) ON DELETE CASCADE,
    subject_topic_id uuid REFERENCES topics(id) ON DELETE CASCADE,
    subject_method_id uuid REFERENCES methods(id) ON DELETE CASCADE,
    rank integer NOT NULL,
    score numeric NOT NULL,
    coverage numeric NOT NULL,
    formula_version text NOT NULL,
    window_start timestamptz NOT NULL,
    window_end timestamptz NOT NULL,
    generated_at timestamptz NOT NULL,
    evidence jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ranking_snapshots_exactly_one_subject_check
        CHECK (num_nonnulls(subject_work_id, subject_topic_id, subject_method_id) = 1),
    CONSTRAINT ranking_snapshots_rank_check CHECK (rank > 0),
    CONSTRAINT ranking_snapshots_coverage_check CHECK (coverage >= 0 AND coverage <= 1),
    CONSTRAINT ranking_snapshots_formula_version_check CHECK (btrim(formula_version) <> ''),
    CONSTRAINT ranking_snapshots_window_check CHECK (window_start < window_end),
    CONSTRAINT ranking_snapshots_evidence_json_check CHECK (jsonb_typeof(evidence) = 'object')
);

CREATE INDEX idx_ranking_snapshots_generated_at
    ON ranking_snapshots(generated_at DESC, rank);
CREATE INDEX idx_ranking_snapshots_work
    ON ranking_snapshots(subject_work_id, generated_at DESC)
    WHERE subject_work_id IS NOT NULL;
CREATE INDEX idx_ranking_snapshots_topic
    ON ranking_snapshots(subject_topic_id, generated_at DESC)
    WHERE subject_topic_id IS NOT NULL;
CREATE INDEX idx_ranking_snapshots_method
    ON ranking_snapshots(subject_method_id, generated_at DESC)
    WHERE subject_method_id IS NOT NULL;

CREATE TABLE ingestion_cursors (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source text NOT NULL,
    cursor_name text NOT NULL,
    cursor_value jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ingestion_cursors_source_check CHECK (btrim(source) <> ''),
    CONSTRAINT ingestion_cursors_name_check CHECK (btrim(cursor_name) <> ''),
    CONSTRAINT ingestion_cursors_value_json_check CHECK (jsonb_typeof(cursor_value) = 'object'),
    UNIQUE (source, cursor_name)
);

CREATE TABLE ingestion_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source text NOT NULL,
    job_type text NOT NULL,
    idempotency_key text NOT NULL UNIQUE,
    status text NOT NULL,
    payload jsonb NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    max_attempts integer NOT NULL,
    available_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    last_error jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ingestion_jobs_source_check CHECK (btrim(source) <> ''),
    CONSTRAINT ingestion_jobs_type_check CHECK (btrim(job_type) <> ''),
    CONSTRAINT ingestion_jobs_idempotency_key_check CHECK (btrim(idempotency_key) <> ''),
    CONSTRAINT ingestion_jobs_status_check
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT ingestion_jobs_payload_json_check CHECK (jsonb_typeof(payload) = 'object'),
    CONSTRAINT ingestion_jobs_last_error_json_check
        CHECK (last_error IS NULL OR jsonb_typeof(last_error) = 'object'),
    CONSTRAINT ingestion_jobs_attempts_check
        CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts)
);

CREATE INDEX idx_ingestion_jobs_dequeue
    ON ingestion_jobs(status, available_at, created_at)
    WHERE status = 'pending';

CREATE TABLE analysis_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    analysis_type text NOT NULL,
    model_provider text NOT NULL,
    model_name text NOT NULL,
    prompt_version text NOT NULL,
    status text NOT NULL,
    input_payload jsonb NOT NULL,
    output_payload jsonb,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT analysis_runs_type_check CHECK (btrim(analysis_type) <> ''),
    CONSTRAINT analysis_runs_provider_check CHECK (btrim(model_provider) <> ''),
    CONSTRAINT analysis_runs_model_check CHECK (btrim(model_name) <> ''),
    CONSTRAINT analysis_runs_prompt_version_check CHECK (btrim(prompt_version) <> ''),
    CONSTRAINT analysis_runs_status_check
        CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    CONSTRAINT analysis_runs_input_json_check CHECK (jsonb_typeof(input_payload) = 'object'),
    CONSTRAINT analysis_runs_output_json_check
        CHECK (output_payload IS NULL OR jsonb_typeof(output_payload) = 'object')
);

CREATE TABLE venue_metric_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    venue_id uuid NOT NULL,
    metric_year integer NOT NULL,
    category text NOT NULL,
    jif numeric,
    quartile text,
    metric_status text NOT NULL,
    source_name text NOT NULL,
    source_license text NOT NULL,
    captured_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT venue_metric_snapshots_venue_id_fkey
        FOREIGN KEY (venue_id) REFERENCES venues(id) ON DELETE RESTRICT,
    CONSTRAINT venue_metric_snapshots_year_check CHECK (metric_year BETWEEN 1900 AND 3000),
    CONSTRAINT venue_metric_snapshots_category_check CHECK (btrim(category) <> ''),
    CONSTRAINT venue_metric_snapshots_jif_check CHECK (jif IS NULL OR jif >= 0),
    CONSTRAINT venue_metric_snapshots_quartile_check
        CHECK (quartile IS NULL OR quartile IN ('Q1', 'Q2', 'Q3', 'Q4')),
    CONSTRAINT venue_metric_snapshots_status_check
        CHECK (metric_status IN ('known', 'unknown')),
    CONSTRAINT venue_metric_snapshots_status_consistency_check
        CHECK (
            (metric_status = 'unknown' AND jif IS NULL AND quartile IS NULL)
            OR
            (metric_status = 'known' AND num_nonnulls(jif, quartile) >= 1)
        ),
    CONSTRAINT venue_metric_snapshots_source_name_check CHECK (btrim(source_name) <> ''),
    CONSTRAINT venue_metric_snapshots_source_license_check CHECK (btrim(source_license) <> ''),
    UNIQUE (venue_id, metric_year, category)
);

CREATE INDEX idx_venue_metric_snapshots_lookup
    ON venue_metric_snapshots(venue_id, metric_year DESC, category);
CREATE INDEX idx_venue_metric_snapshots_quartile
    ON venue_metric_snapshots(metric_year DESC, quartile)
    WHERE quartile IS NOT NULL;
CREATE INDEX idx_venue_metric_snapshots_jif
    ON venue_metric_snapshots(metric_year DESC, jif DESC)
    WHERE jif IS NOT NULL;

CREATE TRIGGER venue_metric_snapshots_immutable
BEFORE UPDATE OR DELETE ON venue_metric_snapshots
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE venue_policy_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    policy_name text NOT NULL,
    version_number integer NOT NULL,
    definition jsonb NOT NULL,
    effective_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT venue_policy_versions_name_check CHECK (btrim(policy_name) <> ''),
    CONSTRAINT venue_policy_versions_version_check CHECK (version_number > 0),
    CONSTRAINT venue_policy_versions_definition_json_check
        CHECK (jsonb_typeof(definition) = 'object' AND definition <> '{}'::jsonb),
    UNIQUE (policy_name, version_number)
);

CREATE TRIGGER venue_policy_versions_immutable
BEFORE UPDATE OR DELETE ON venue_policy_versions
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE venue_policy_assessments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    venue_id uuid NOT NULL REFERENCES venues(id) ON DELETE RESTRICT,
    policy_version_id uuid NOT NULL,
    metric_year integer NOT NULL,
    decision text NOT NULL,
    matched_rules jsonb NOT NULL,
    evidence jsonb NOT NULL,
    assessed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT venue_policy_assessments_policy_version_fkey
        FOREIGN KEY (policy_version_id)
        REFERENCES venue_policy_versions(id)
        ON DELETE RESTRICT,
    CONSTRAINT venue_policy_assessments_year_check CHECK (metric_year BETWEEN 1900 AND 3000),
    CONSTRAINT venue_policy_assessments_decision_check
        CHECK (decision IN ('accepted', 'rejected', 'unknown')),
    CONSTRAINT venue_policy_assessments_matched_rules_json_check
        CHECK (jsonb_typeof(matched_rules) = 'array'),
    CONSTRAINT venue_policy_assessments_evidence_json_check
        CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb),
    CONSTRAINT venue_policy_assessments_decision_semantics_check
        CHECK (
            (decision = 'unknown' AND jsonb_array_length(matched_rules) = 0)
            OR
            (decision = 'accepted' AND jsonb_array_length(matched_rules) > 0)
            OR
            decision = 'rejected'
        ),
    UNIQUE (venue_id, policy_version_id, metric_year)
);

CREATE INDEX idx_venue_policy_assessments_decision
    ON venue_policy_assessments(decision, metric_year DESC);

CREATE TRIGGER venue_policy_assessments_immutable
BEFORE UPDATE OR DELETE ON venue_policy_assessments
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE fulltext_assets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL REFERENCES works(id) ON DELETE CASCADE,
    source_record_id uuid NOT NULL,
    source_url text NOT NULL,
    license text NOT NULL,
    content_hash text NOT NULL,
    retrieved_at timestamptz NOT NULL,
    reusable_status text NOT NULL,
    publication_status text NOT NULL,
    storage_key text,
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT fulltext_assets_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT fulltext_assets_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT fulltext_assets_source_url_check
        CHECK (source_url ~ '^https?://[^[:space:]]+$'),
    CONSTRAINT fulltext_assets_license_check CHECK (btrim(license) <> ''),
    CONSTRAINT fulltext_assets_content_hash_check CHECK (btrim(content_hash) <> ''),
    CONSTRAINT fulltext_assets_reusable_status_check
        CHECK (reusable_status IN ('unknown', 'not_reusable', 'reusable')),
    CONSTRAINT fulltext_assets_publication_status_check
        CHECK (publication_status IN ('private', 'public')),
    CONSTRAINT fulltext_assets_public_reusable_check
        CHECK (publication_status <> 'public' OR reusable_status = 'reusable'),
    CONSTRAINT fulltext_assets_metadata_json_check CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (work_id, content_hash)
);

CREATE INDEX idx_fulltext_assets_source_record_id ON fulltext_assets(source_record_id);
CREATE INDEX idx_fulltext_assets_public
    ON fulltext_assets(work_id, retrieved_at DESC)
    WHERE publication_status = 'public' AND reusable_status = 'reusable';
