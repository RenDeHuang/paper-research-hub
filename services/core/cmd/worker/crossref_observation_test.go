package main

import (
	"iter"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ingestion"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/crossref"
)

func TestCrossrefObservationTrackerMapsReceiptToExactRecordCoordinates(
	t *testing.T,
) {
	t.Parallel()

	runID := uuid.NewString()
	tracker, err := newCrossrefObservationTracker(runID)
	if err != nil {
		t.Fatalf("newCrossrefObservationTracker() error = %v", err)
	}
	observedAt := time.Date(
		2026,
		time.July,
		19,
		8,
		30,
		0,
		123456000,
		time.UTC,
	)
	if err := tracker.RecordPage(crossref.PageReceipt{
		Ordinal:       3,
		CursorIn:      "page-3",
		CursorOut:     "page-4",
		ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RecordCount:   2,
		ObservedAt:    observedAt,
	}); err != nil {
		t.Fatalf("RecordPage() error = %v", err)
	}

	first, err := tracker.Next()
	if err != nil {
		t.Fatalf("Next(first) error = %v", err)
	}
	second, err := tracker.Next()
	if err != nil {
		t.Fatalf("Next(second) error = %v", err)
	}
	if first.ConnectorRunID != runID ||
		first.PageOrdinal != 3 ||
		first.RecordOrdinal != 1 ||
		!first.ObservedAt.Equal(observedAt) {
		t.Fatalf("first boundary = %#v", first)
	}
	if second.ConnectorRunID != runID ||
		second.PageOrdinal != 3 ||
		second.RecordOrdinal != 2 ||
		!second.ObservedAt.Equal(observedAt) {
		t.Fatalf("second boundary = %#v", second)
	}
	if _, err := tracker.Next(); err == nil {
		t.Fatal("Next() beyond page record count error = nil")
	}
}

func TestCrossrefObservationTrackerRejectsMissingOrOverlappingPageState(
	t *testing.T,
) {
	t.Parallel()

	tracker, err := newCrossrefObservationTracker(uuid.NewString())
	if err != nil {
		t.Fatalf("newCrossrefObservationTracker() error = %v", err)
	}
	if _, err := tracker.Next(); err == nil {
		t.Fatal("Next() before page receipt error = nil")
	}
	if err := tracker.RecordPage(crossref.PageReceipt{
		Ordinal:       1,
		CursorIn:      "*",
		CursorOut:     "page-2",
		ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RecordCount:   2,
		ObservedAt: time.Date(
			2026,
			time.July,
			19,
			8,
			30,
			0,
			0,
			time.UTC,
		),
	}); err != nil {
		t.Fatalf("RecordPage(first) error = %v", err)
	}
	if _, err := tracker.Next(); err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if err := tracker.RecordPage(crossref.PageReceipt{
		Ordinal:       2,
		CursorIn:      "page-2",
		CursorOut:     "",
		ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		RecordCount:   0,
		ObservedAt: time.Date(
			2026,
			time.July,
			19,
			8,
			31,
			0,
			0,
			time.UTC,
		),
	}); err == nil {
		t.Fatal("RecordPage() before prior records consumed error = nil")
	}
}

func TestObservedCrossrefEventsAttachReceiptBoundaryToEveryEnvelope(t *testing.T) {
	t.Parallel()

	runID := uuid.NewString()
	tracker, err := newCrossrefObservationTracker(runID)
	if err != nil {
		t.Fatalf("newCrossrefObservationTracker() error = %v", err)
	}
	observedAt := time.Date(
		2026,
		time.July,
		19,
		8,
		30,
		0,
		123456000,
		time.UTC,
	)
	if err := tracker.RecordPage(crossref.PageReceipt{
		Ordinal:       1,
		CursorIn:      "*",
		CursorOut:     "",
		ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		RecordCount:   1,
		ObservedAt:    observedAt,
	}); err != nil {
		t.Fatalf("RecordPage() error = %v", err)
	}
	createdAt := time.Date(2026, time.July, 19, 8, 29, 0, 0, time.UTC)
	raw, err := source.NewRawRecord([]byte(`{"DOI":"10.1000/observed-event"}`))
	if err != nil {
		t.Fatalf("source.NewRawRecord() error = %v", err)
	}
	identity, err := paper.NewIdentifier(
		paper.SchemeDOI,
		"10.1000/observed-event",
	)
	if err != nil {
		t.Fatalf("paper.NewIdentifier() error = %v", err)
	}
	records := source.ClientSequence(iter.Seq2[source.Record, error](
		func(yield func(source.Record, error) bool) {
			yield(source.Record{
				Source:         source.Crossref,
				SourceRecordID: "10.1000/observed-event",
				Identity:       identity,
				Raw:            raw,
				Title:          "Observed event",
				CreatedAt:      &createdAt,
			}, nil)
		},
	))

	events := observedCrossrefEventsForCommand(
		workerCommand{Kind: commandSyncCrossrefCreated},
		records,
		tracker,
	)
	var got []ingestion.Event
	for event, eventErr := range events {
		if eventErr != nil {
			t.Fatalf("observedCrossrefEventsForCommand() error = %v", eventErr)
		}
		got = append(got, event)
	}
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
	envelope, ok := got[0].(ingestion.Envelope)
	if !ok {
		t.Fatalf("event type = %T, want ingestion.Envelope", got[0])
	}
	if envelope.RawObservation == nil ||
		envelope.RawObservation.ConnectorRunID != runID ||
		envelope.RawObservation.PageOrdinal != 1 ||
		envelope.RawObservation.RecordOrdinal != 1 ||
		!envelope.RawObservation.ObservedAt.Equal(observedAt) {
		t.Fatalf("RawObservation = %#v", envelope.RawObservation)
	}
}
