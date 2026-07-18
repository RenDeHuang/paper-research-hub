\set ON_ERROR_STOP on

DO $$
DECLARE
    current_generation uuid;
BEGIN
    SELECT generation_id
    INTO current_generation
    FROM public_catalog_current
    WHERE singleton = true;

    IF current_generation IS NULL THEN
        RAISE EXCEPTION 'release verification failed: no current Catalog generation';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM public_catalog_publications
        WHERE generation_id = current_generation
    ) THEN
        RAISE EXCEPTION
            'release verification failed: current Catalog generation % is not published',
            current_generation;
    END IF;
END;
$$;

SELECT
    current_catalog.generation_id,
    publication.published_at,
    count(paper.paper_id) AS catalog_paper_count
FROM public_catalog_current AS current_catalog
JOIN public_catalog_publications AS publication
    ON publication.generation_id = current_catalog.generation_id
LEFT JOIN public_catalog_papers AS paper
    ON paper.generation_id = current_catalog.generation_id
WHERE current_catalog.singleton = true
GROUP BY current_catalog.generation_id, publication.published_at;
