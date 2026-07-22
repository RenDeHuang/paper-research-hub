package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ingestion"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/crossref"
)

type crossrefObservationTracker struct {
	mu             sync.Mutex
	connectorRunID string
	page           crossref.PageReceipt
	nextRecord     int
	hasPage        bool
}

func newCrossrefObservationTracker(
	connectorRunID string,
) (*crossrefObservationTracker, error) {
	if _, err := uuid.Parse(connectorRunID); err != nil {
		return nil, errors.New(
			"Crossref observation tracker connector run ID must be a UUID",
		)
	}
	return &crossrefObservationTracker{
		connectorRunID: connectorRunID,
	}, nil
}

func (tracker *crossrefObservationTracker) RecordPage(
	page crossref.PageReceipt,
) error {
	if tracker == nil {
		return errors.New("Crossref observation tracker is nil")
	}
	if page.Ordinal < 1 {
		return errors.New("Crossref observed page ordinal must be positive")
	}
	if page.RecordCount < 0 {
		return errors.New("Crossref observed page record count must not be negative")
	}
	if page.ObservedAt.IsZero() {
		return errors.New("Crossref observed page requires observed_at")
	}
	if _, err := ingestion.NewRawObservationBoundary(
		tracker.connectorRunID,
		page.ObservedAt,
		page.Ordinal,
		1,
	); err != nil {
		return err
	}

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.hasPage && tracker.nextRecord <= tracker.page.RecordCount {
		return fmt.Errorf(
			"Crossref page %d has %d unconsumed records",
			tracker.page.Ordinal,
			tracker.page.RecordCount-tracker.nextRecord+1,
		)
	}
	tracker.page = page
	tracker.nextRecord = 1
	tracker.hasPage = true
	return nil
}

func (tracker *crossrefObservationTracker) Next() (
	ingestion.RawObservationBoundary,
	error,
) {
	if tracker == nil {
		return ingestion.RawObservationBoundary{}, errors.New(
			"Crossref observation tracker is nil",
		)
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if !tracker.hasPage {
		return ingestion.RawObservationBoundary{}, errors.New(
			"Crossref record arrived before its page receipt",
		)
	}
	if tracker.nextRecord > tracker.page.RecordCount {
		return ingestion.RawObservationBoundary{}, fmt.Errorf(
			"Crossref page %d has no unconsumed record",
			tracker.page.Ordinal,
		)
	}
	boundary, err := ingestion.NewRawObservationBoundary(
		tracker.connectorRunID,
		tracker.page.ObservedAt,
		tracker.page.Ordinal,
		tracker.nextRecord,
	)
	if err != nil {
		return ingestion.RawObservationBoundary{}, err
	}
	tracker.nextRecord++
	return boundary, nil
}

func observedCrossrefEventsForCommand(
	command workerCommand,
	records source.ClientSequence,
	tracker *crossrefObservationTracker,
) ingestion.EventSequence {
	return func(yield func(ingestion.Event, error) bool) {
		if records == nil {
			yield(nil, errors.New("Crossref source record sequence is required"))
			return
		}
		if tracker == nil {
			yield(nil, errors.New("Crossref observation tracker is required"))
			return
		}
		var revisionTime recordRevisionTimeFunc
		switch command.Kind {
		case commandSyncCrossrefCreated:
			revisionTime = func(record source.Record) (time.Time, bool) {
				return exactRecordTime(record.CreatedAt)
			}
		case commandSyncCrossrefUpdated:
			revisionTime = func(record source.Record) (time.Time, bool) {
				return exactRecordTime(record.UpdatedAt)
			}
		default:
			yield(nil, fmt.Errorf(
				"unsupported observed Crossref command kind %q",
				command.Kind,
			))
			return
		}

		position := int64(0)
		for record, recordErr := range records {
			if recordErr != nil {
				yield(nil, recordErr)
				return
			}
			position++
			if strings.TrimSpace(record.Source) != source.Crossref {
				yield(nil, fmt.Errorf(
					"record source %q conflicts with observed Crossref source",
					record.Source,
				))
				return
			}
			sourceTime, ok := revisionTime(record)
			if !ok {
				yield(nil, fmt.Errorf(
					"record %s:%s has no deterministic source revision time",
					source.Crossref,
					record.SourceRecordID,
				))
				return
			}
			boundary, err := tracker.Next()
			if err != nil {
				yield(nil, err)
				return
			}
			envelope, err := ingestion.NewEnvelope(
				source.Crossref,
				source.Crossref+":"+record.SourceRecordID,
				sourceTime,
				record.Raw.SHA256,
				position,
				record,
				record.Raw,
			)
			if err != nil {
				yield(nil, err)
				return
			}
			observed, err := envelope.WithRawObservation(boundary)
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(observed, nil) {
				return
			}
		}
	}
}
