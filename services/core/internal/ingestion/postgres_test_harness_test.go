package ingestion

import (
	"context"
	"testing"
)

func TestOpenIngestionTestPoolUsesProductionPublicSchema(t *testing.T) {
	pool := openIngestionTestPool(t)

	var currentSchema string
	var officialURLTableExists bool
	if err := pool.QueryRow(context.Background(), `
		SELECT
			current_schema(),
			to_regclass('public.work_url_verifications') IS NOT NULL
	`).Scan(&currentSchema, &officialURLTableExists); err != nil {
		t.Fatalf("inspect ingestion test database schema: %v", err)
	}
	if currentSchema != "public" {
		t.Fatalf("current schema = %q, want production schema public", currentSchema)
	}
	if !officialURLTableExists {
		t.Fatal("public.work_url_verifications does not exist after migrations")
	}
}
