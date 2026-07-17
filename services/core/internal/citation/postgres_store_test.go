package citation

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestPostgresStoreAppendsSnapshotAndAcceptsOnlyIdenticalReplay(
	t *testing.T,
) {
	fixture := openCitationPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	snapshot := Snapshot{
		WorkID:            fixture.WorkID,
		Source:            "openalex",
		ObservedAt:        fixture.ObservedAt,
		Count:             42,
		SourceRecordID:    fixture.SourceRecordID,
		IngestionJobID:    fixture.IngestionJobID,
		RetrievedAt:       fixture.RetrievedAt,
		Coverage:          1,
		DefinitionVersion: "openalex-cited-by-count/v1",
		DatasetVersion:    "openalex-api/2026-07-17",
	}

	firstID, err := store.AppendSnapshot(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("AppendSnapshot(first) error = %v", err)
	}
	secondID, err := store.AppendSnapshot(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("AppendSnapshot(replay) error = %v", err)
	}
	if firstID == uuid.Nil || secondID != firstID {
		t.Fatalf("snapshot IDs = first %s replay %s", firstID, secondID)
	}

	conflicting := snapshot
	conflicting.Count = 43
	_, err = store.AppendSnapshot(context.Background(), conflicting)
	if !errors.Is(err, ErrEvidenceConflict) {
		t.Fatalf(
			"AppendSnapshot(conflict) error = %v, want ErrEvidenceConflict",
			err,
		)
	}

	var count int64
	if err := fixture.Pool.QueryRow(context.Background(), `
		SELECT count
		FROM citation_snapshots
		WHERE id = $1
	`, firstID).Scan(&count); err != nil {
		t.Fatalf("query retained citation snapshot: %v", err)
	}
	if count != 42 {
		t.Fatalf("retained citation count = %d, want 42", count)
	}
}

func TestPostgresStoreAppendsCitationAndReferenceEdgesIndependently(
	t *testing.T,
) {
	fixture := openCitationPostgresFixture(t)
	store, err := NewPostgresStore(fixture.Pool)
	if err != nil {
		t.Fatalf("NewPostgresStore() error = %v", err)
	}
	edge := CitationEdge{
		CitingWorkID:      fixture.WorkID,
		CitedWorkID:       fixture.CitedWorkID,
		CitingIdentifier:  "openalex:W2001",
		CitedIdentifier:   "doi:10.1000/cited-work",
		Source:            "openalex",
		SourceRecordID:    fixture.SourceRecordID,
		IngestionJobID:    fixture.IngestionJobID,
		RetrievedAt:       fixture.RetrievedAt,
		DatasetVersion:    "openalex-api/2026-07-17",
		DefinitionVersion: "citation-edge/v1",
	}

	citationID, err := store.AppendCitationEdge(context.Background(), edge)
	if err != nil {
		t.Fatalf("AppendCitationEdge() error = %v", err)
	}
	referenceID, err := store.AppendReferenceEdge(context.Background(), edge)
	if err != nil {
		t.Fatalf("AppendReferenceEdge() error = %v", err)
	}
	if citationID == uuid.Nil || referenceID == uuid.Nil {
		t.Fatalf(
			"edge IDs = citation %s reference %s",
			citationID,
			referenceID,
		)
	}

	replayedCitationID, err := store.AppendCitationEdge(
		context.Background(),
		edge,
	)
	if err != nil || replayedCitationID != citationID {
		t.Fatalf(
			"AppendCitationEdge(replay) = (%s, %v), want (%s, nil)",
			replayedCitationID,
			err,
			citationID,
		)
	}

	conflicting := edge
	conflicting.CitedWorkID = uuid.Nil
	_, err = store.AppendCitationEdge(context.Background(), conflicting)
	if !errors.Is(err, ErrEvidenceConflict) {
		t.Fatalf(
			"AppendCitationEdge(conflict) error = %v, want ErrEvidenceConflict",
			err,
		)
	}
}

func TestNewPostgresStoreRejectsNilPool(t *testing.T) {
	t.Parallel()

	if _, err := NewPostgresStore(nil); err == nil {
		t.Fatal("NewPostgresStore(nil) error = nil")
	}
}
