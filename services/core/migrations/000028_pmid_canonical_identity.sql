ALTER TABLE works
    DROP CONSTRAINT works_canonical_key_check;

ALTER TABLE works
    ADD CONSTRAINT works_canonical_key_check
    CHECK (
        canonical_key ~ '^pmid:[1-9][0-9]*$'
        OR (
            normalize_work_canonical_key(canonical_key) IS NOT NULL
            AND canonical_key = normalize_work_canonical_key(canonical_key)
        )
    );
