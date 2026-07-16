ALTER TABLE ingestion_jobs
    DROP CONSTRAINT ingestion_jobs_idempotency_key_key;

CREATE UNIQUE INDEX ingestion_jobs_active_idempotency_key
    ON ingestion_jobs(idempotency_key)
    WHERE status IN ('pending', 'running');
