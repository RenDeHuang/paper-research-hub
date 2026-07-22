package ingestion

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

func TestObservedEnvelopeCarriesExplicitConnectorBoundary(t *testing.T) {
	t.Parallel()

	raw := mustRawRecord(t, `{"DOI":"10.1000/observed-envelope"}`)
	record := source.Record{
		Source:         source.Crossref,
		SourceRecordID: "10.1000/observed-envelope",
		Raw:            raw,
		Title:          "Observed envelope",
	}
	envelope, err := NewEnvelope(
		source.Crossref,
		"crossref:10.1000/observed-envelope",
		time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC),
		raw.SHA256,
		1,
		record,
		raw,
	)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	boundary, err := NewRawObservationBoundary(
		uuid.NewString(),
		time.Date(2026, time.July, 19, 8, 0, 1, 0, time.UTC),
		2,
		3,
	)
	if err != nil {
		t.Fatalf("NewRawObservationBoundary() error = %v", err)
	}
	observed, err := envelope.WithRawObservation(boundary)
	if err != nil {
		t.Fatalf("WithRawObservation() error = %v", err)
	}
	if observed.RawObservation == nil ||
		*observed.RawObservation != boundary {
		t.Fatalf("RawObservation = %#v, want %#v", observed.RawObservation, boundary)
	}
	cloned := observed.Clone()
	cloned.RawObservation.PageOrdinal = 99
	if observed.RawObservation.PageOrdinal != 2 {
		t.Fatal("Envelope.Clone() shares raw observation state")
	}
}

func TestRawObservationBoundaryRejectsImplicitCoordinates(t *testing.T) {
	t.Parallel()

	valid := RawObservationBoundary{
		ConnectorRunID: uuid.NewString(),
		ObservedAt:     time.Date(2026, time.July, 19, 8, 0, 0, 0, time.UTC),
		PageOrdinal:    1,
		RecordOrdinal:  1,
	}
	tests := map[string]RawObservationBoundary{
		"missing connector run": func() RawObservationBoundary {
			value := valid
			value.ConnectorRunID = ""
			return value
		}(),
		"malformed connector run": func() RawObservationBoundary {
			value := valid
			value.ConnectorRunID = "not-a-uuid"
			return value
		}(),
		"zero observed at": func() RawObservationBoundary {
			value := valid
			value.ObservedAt = time.Time{}
			return value
		}(),
		"zero page": func() RawObservationBoundary {
			value := valid
			value.PageOrdinal = 0
			return value
		}(),
		"zero record": func() RawObservationBoundary {
			value := valid
			value.RecordOrdinal = 0
			return value
		}(),
	}
	for name, boundary := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := boundary.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestPostgresRepositoryPersistsRawObservationInRawTransaction(t *testing.T) {
	pool := openIngestionTestPool(t)
	repository := mustPostgresRepository(t, pool)
	ctx := context.Background()
	job := startSourceRepositoryJob(t, repository, source.Crossref, "raw-observation")
	runStore, err := NewPostgresConnectorRunStore(pool)
	if err != nil {
		t.Fatalf("NewPostgresConnectorRunStore() error = %v", err)
	}
	now := time.Now().UTC()
	claim, err := NewConnectorClaim(
		source.Crossref,
		"created",
		WatermarkTimestamp,
		now.Add(-time.Hour),
		now.Add(time.Hour),
		"raw-observation-"+uuid.NewString(),
	)
	if err != nil {
		t.Fatalf("NewConnectorClaim() error = %v", err)
	}
	run, err := runStore.Start(ctx, claim)
	if err != nil {
		t.Fatalf("connector run Start() error = %v", err)
	}
	receipt, err := NewConnectorPageReceipt(
		run.ID,
		1,
		"*",
		"",
		strings.Repeat("a", 64),
		1,
	)
	if err != nil {
		t.Fatalf("NewConnectorPageReceipt() error = %v", err)
	}
	if err := runStore.RecordPage(ctx, run, receipt); err != nil {
		t.Fatalf("RecordPage() error = %v", err)
	}

	envelope := publicationRepositoryEnvelope(
		t,
		source.Crossref,
		"10.1000/raw-observation",
		"10.1000/raw-observation",
		"crossref:10.1000/raw-observation",
		now,
		"raw-observation",
		1,
		"journal",
		"published",
		nil,
	)
	boundary, err := NewRawObservationBoundary(
		run.ID,
		time.Now().UTC(),
		1,
		1,
	)
	if err != nil {
		t.Fatalf("NewRawObservationBoundary() error = %v", err)
	}
	envelope, err = envelope.WithRawObservation(boundary)
	if err != nil {
		t.Fatalf("WithRawObservation() error = %v", err)
	}

	first, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw(first) error = %v", err)
	}
	second, err := repository.PersistRaw(ctx, job.ID, envelope)
	if err != nil {
		t.Fatalf("PersistRaw(second) error = %v", err)
	}
	if first.ID != second.ID ||
		second.Disposition != RawDispositionReused {
		t.Fatalf(
			"replay persistence = first %#v second %#v, want same raw event and reused disposition",
			first,
			second,
		)
	}

	var count int
	var rawEventID, observedJobID, connectorRunID string
	var pageOrdinal, recordOrdinal int
	var observedAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) OVER (),
			raw_event_id::text,
			job_id::text,
			connector_run_id::text,
			page_ordinal,
			record_ordinal,
			observed_at
		FROM ingestion_raw_observations
		WHERE connector_run_id = $1
	`, run.ID).Scan(
		&count,
		&rawEventID,
		&observedJobID,
		&connectorRunID,
		&pageOrdinal,
		&recordOrdinal,
		&observedAt,
	); err != nil {
		t.Fatalf("query raw observation: %v", err)
	}
	if count != 1 ||
		rawEventID != first.ID ||
		observedJobID != job.ID ||
		connectorRunID != run.ID ||
		pageOrdinal != 1 ||
		recordOrdinal != 1 ||
		!observedAt.Equal(boundary.ObservedAt) {
		t.Fatalf(
			"persisted observation = count %d raw %q job %q run %q page %d record %d at %s",
			count,
			rawEventID,
			observedJobID,
			connectorRunID,
			pageOrdinal,
			recordOrdinal,
			observedAt,
		)
	}
}
