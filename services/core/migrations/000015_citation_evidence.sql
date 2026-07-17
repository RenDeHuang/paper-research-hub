CREATE TABLE citation_snapshots (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    work_id uuid NOT NULL,
    source text NOT NULL,
    observed_at timestamptz NOT NULL,
    count bigint NOT NULL,
    source_record_id uuid NOT NULL,
    ingestion_job_id uuid NOT NULL,
    retrieved_at timestamptz NOT NULL,
    coverage numeric NOT NULL,
    definition_version text NOT NULL,
    dataset_version text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT citation_snapshots_work_id_fkey
        FOREIGN KEY (work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_snapshots_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_snapshots_ingestion_job_id_fkey
        FOREIGN KEY (ingestion_job_id)
        REFERENCES ingestion_jobs(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_snapshots_source_record_work_fkey
        FOREIGN KEY (source_record_id, work_id)
        REFERENCES source_record_works(source_record_id, work_id)
        DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT citation_snapshots_source_check
        CHECK (btrim(source) <> '' AND source = btrim(source)),
    CONSTRAINT citation_snapshots_count_check
        CHECK (count >= 0),
    CONSTRAINT citation_snapshots_time_check
        CHECK (retrieved_at >= observed_at),
    CONSTRAINT citation_snapshots_coverage_check
        CHECK (coverage >= 0 AND coverage <= 1),
    CONSTRAINT citation_snapshots_definition_check
        CHECK (
            btrim(definition_version) <> ''
            AND definition_version = btrim(definition_version)
        ),
    CONSTRAINT citation_snapshots_dataset_check
        CHECK (
            btrim(dataset_version) <> ''
            AND dataset_version = btrim(dataset_version)
        ),
    CONSTRAINT citation_snapshots_work_source_observed_key
        UNIQUE (work_id, source, observed_at)
);

CREATE INDEX idx_citation_snapshots_work_source_observed
    ON citation_snapshots(work_id, source, observed_at DESC, id DESC);

CREATE INDEX idx_citation_snapshots_source_dataset
    ON citation_snapshots(source, dataset_version, observed_at DESC, id DESC);

CREATE FUNCTION enforce_citation_snapshot_provenance()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM source_records AS source_record
        JOIN ingestion_projection_assertions AS projection
          ON projection.source_record_uuid = source_record.id
         AND projection.work_id = NEW.work_id
         AND projection.job_id = NEW.ingestion_job_id
        JOIN ingestion_raw_events AS raw_event
          ON raw_event.id = projection.raw_event_id
        JOIN ingestion_jobs AS ingestion_job
          ON ingestion_job.id = projection.job_id
        WHERE source_record.id = NEW.source_record_id
          AND source_record.source = NEW.source
          AND raw_event.logical_source = NEW.source
          AND ingestion_job.source = NEW.source
          AND NEW.observed_at = source_record.source_time
          AND NEW.retrieved_at = source_record.retrieved_at
    ) THEN
        RAISE EXCEPTION
            'citation snapshot provenance does not match an exact projected source record and ingestion job'
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'citation_evidence_provenance';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER citation_snapshots_provenance
BEFORE INSERT ON citation_snapshots
FOR EACH ROW
EXECUTE FUNCTION enforce_citation_snapshot_provenance();

CREATE TRIGGER citation_snapshots_immutable
BEFORE UPDATE OR DELETE ON citation_snapshots
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TABLE citation_edges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    citing_work_id uuid,
    cited_work_id uuid,
    citing_identifier text NOT NULL,
    cited_identifier text NOT NULL,
    source text NOT NULL,
    source_record_id uuid NOT NULL,
    ingestion_job_id uuid NOT NULL,
    retrieved_at timestamptz NOT NULL,
    definition_version text NOT NULL,
    dataset_version text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT citation_edges_citing_work_id_fkey
        FOREIGN KEY (citing_work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_edges_cited_work_id_fkey
        FOREIGN KEY (cited_work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_edges_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_edges_ingestion_job_id_fkey
        FOREIGN KEY (ingestion_job_id)
        REFERENCES ingestion_jobs(id)
        ON DELETE RESTRICT,
    CONSTRAINT citation_edges_citing_identifier_check
        CHECK (
            btrim(citing_identifier) <> ''
            AND citing_identifier = btrim(citing_identifier)
        ),
    CONSTRAINT citation_edges_cited_identifier_check
        CHECK (
            btrim(cited_identifier) <> ''
            AND cited_identifier = btrim(cited_identifier)
        ),
    CONSTRAINT citation_edges_source_check
        CHECK (btrim(source) <> '' AND source = btrim(source)),
    CONSTRAINT citation_edges_definition_check
        CHECK (
            btrim(definition_version) <> ''
            AND definition_version = btrim(definition_version)
        ),
    CONSTRAINT citation_edges_dataset_check
        CHECK (
            btrim(dataset_version) <> ''
            AND dataset_version = btrim(dataset_version)
        ),
    CONSTRAINT citation_edges_work_shape_check
        CHECK (num_nonnulls(citing_work_id, cited_work_id) >= 1),
    CONSTRAINT citation_edges_not_self_check
        CHECK (
            citing_identifier <> cited_identifier
            AND (
                citing_work_id IS NULL
                OR cited_work_id IS NULL
                OR citing_work_id <> cited_work_id
            )
        ),
    CONSTRAINT citation_edges_identity_key
        UNIQUE (
            source,
            citing_identifier,
            cited_identifier,
            definition_version,
            dataset_version
        )
);

CREATE INDEX idx_citation_edges_citing_work
    ON citation_edges(citing_work_id, source, retrieved_at DESC, id DESC)
    WHERE citing_work_id IS NOT NULL;

CREATE INDEX idx_citation_edges_cited_work
    ON citation_edges(cited_work_id, source, retrieved_at DESC, id DESC)
    WHERE cited_work_id IS NOT NULL;

CREATE TABLE reference_edges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    citing_work_id uuid,
    cited_work_id uuid,
    citing_identifier text NOT NULL,
    cited_identifier text NOT NULL,
    source text NOT NULL,
    source_record_id uuid NOT NULL,
    ingestion_job_id uuid NOT NULL,
    retrieved_at timestamptz NOT NULL,
    definition_version text NOT NULL,
    dataset_version text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT reference_edges_citing_work_id_fkey
        FOREIGN KEY (citing_work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT reference_edges_cited_work_id_fkey
        FOREIGN KEY (cited_work_id)
        REFERENCES works(id)
        ON DELETE RESTRICT,
    CONSTRAINT reference_edges_source_record_id_fkey
        FOREIGN KEY (source_record_id)
        REFERENCES source_records(id)
        ON DELETE RESTRICT,
    CONSTRAINT reference_edges_ingestion_job_id_fkey
        FOREIGN KEY (ingestion_job_id)
        REFERENCES ingestion_jobs(id)
        ON DELETE RESTRICT,
    CONSTRAINT reference_edges_citing_identifier_check
        CHECK (
            btrim(citing_identifier) <> ''
            AND citing_identifier = btrim(citing_identifier)
        ),
    CONSTRAINT reference_edges_cited_identifier_check
        CHECK (
            btrim(cited_identifier) <> ''
            AND cited_identifier = btrim(cited_identifier)
        ),
    CONSTRAINT reference_edges_source_check
        CHECK (btrim(source) <> '' AND source = btrim(source)),
    CONSTRAINT reference_edges_definition_check
        CHECK (
            btrim(definition_version) <> ''
            AND definition_version = btrim(definition_version)
        ),
    CONSTRAINT reference_edges_dataset_check
        CHECK (
            btrim(dataset_version) <> ''
            AND dataset_version = btrim(dataset_version)
        ),
    CONSTRAINT reference_edges_work_shape_check
        CHECK (num_nonnulls(citing_work_id, cited_work_id) >= 1),
    CONSTRAINT reference_edges_not_self_check
        CHECK (
            citing_identifier <> cited_identifier
            AND (
                citing_work_id IS NULL
                OR cited_work_id IS NULL
                OR citing_work_id <> cited_work_id
            )
        ),
    CONSTRAINT reference_edges_identity_key
        UNIQUE (
            source,
            citing_identifier,
            cited_identifier,
            definition_version,
            dataset_version
        )
);

CREATE INDEX idx_reference_edges_citing_work
    ON reference_edges(citing_work_id, source, retrieved_at DESC, id DESC)
    WHERE citing_work_id IS NOT NULL;

CREATE INDEX idx_reference_edges_cited_work
    ON reference_edges(cited_work_id, source, retrieved_at DESC, id DESC)
    WHERE cited_work_id IS NOT NULL;

CREATE FUNCTION enforce_citation_edge_provenance()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM source_records AS source_record
        JOIN ingestion_projection_assertions AS projection
          ON projection.source_record_uuid = source_record.id
         AND projection.job_id = NEW.ingestion_job_id
        JOIN ingestion_raw_events AS raw_event
          ON raw_event.id = projection.raw_event_id
        JOIN ingestion_jobs AS ingestion_job
          ON ingestion_job.id = projection.job_id
        WHERE source_record.id = NEW.source_record_id
          AND source_record.source = NEW.source
          AND raw_event.logical_source = NEW.source
          AND ingestion_job.source = NEW.source
          AND NEW.retrieved_at = source_record.retrieved_at
          AND projection.work_id IN (
              NEW.citing_work_id,
              NEW.cited_work_id
          )
    ) THEN
        RAISE EXCEPTION
            'citation edge provenance does not match either resolved Work and its exact projected source record and ingestion job'
            USING
                ERRCODE = '23514',
                CONSTRAINT = 'citation_evidence_provenance';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER citation_edges_provenance
BEFORE INSERT ON citation_edges
FOR EACH ROW
EXECUTE FUNCTION enforce_citation_edge_provenance();

CREATE TRIGGER reference_edges_provenance
BEFORE INSERT ON reference_edges
FOR EACH ROW
EXECUTE FUNCTION enforce_citation_edge_provenance();

CREATE TRIGGER citation_edges_immutable
BEFORE UPDATE OR DELETE ON citation_edges
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();

CREATE TRIGGER reference_edges_immutable
BEFORE UPDATE OR DELETE ON reference_edges
FOR EACH ROW
EXECUTE FUNCTION reject_immutable_row();
