package main

import (
	"context"
	"errors"
	"iter"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ingestion"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/crossref"
)

func TestCrossrefCreatedAndUpdatedCommandsAreExplicitTypedStreams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		target   string
		wantKind commandKind
	}{
		{
			name:     "created",
			target:   "crossref-created",
			wantKind: commandSyncCrossrefCreated,
		},
		{
			name:     "updated",
			target:   "crossref-updated",
			wantKind: commandSyncCrossrefUpdated,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			command, role, err := parseWorkerCommand([]string{
				"sync",
				test.target,
				"--from-date",
				"2026-07-17",
				"--to-date",
				"2026-07-18",
				"--issn",
				"0028-0836",
				"--max-results",
				"250",
			})
			if err != nil {
				t.Fatalf("parseWorkerCommand() error = %v", err)
			}
			if role != config.RoleCrossrefSync {
				t.Fatalf("role = %q, want %q", role, config.RoleCrossrefSync)
			}
			if command.Kind != test.wantKind {
				t.Fatalf("Kind = %q, want %q", command.Kind, test.wantKind)
			}
			if !command.FromDate.Equal(time.Date(
				2026,
				time.July,
				17,
				0,
				0,
				0,
				0,
				time.UTC,
			)) || !command.ToDate.Equal(time.Date(
				2026,
				time.July,
				18,
				0,
				0,
				0,
				0,
				time.UTC,
			)) {
				t.Fatalf(
					"date window = %s..%s, want exact UTC day bounds",
					command.FromDate,
					command.ToDate,
				)
			}
			if len(command.ISSNs) != 1 || command.ISSNs[0] != "0028-0836" {
				t.Fatalf("ISSNs = %#v, want exact normalized ISSN", command.ISSNs)
			}
			if command.MaxResults != 250 {
				t.Fatalf("MaxResults = %d, want 250", command.MaxResults)
			}
		})
	}
}

func TestCrossrefEventRevisionTimeFollowsTheClaimedStream(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(
		2026,
		time.July,
		17,
		1,
		2,
		3,
		0,
		time.UTC,
	)
	updatedAt := createdAt.Add(12 * time.Hour)
	identity, err := paper.NewIdentifier(
		paper.SchemeDOI,
		"10.1000/stream-time",
	)
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	raw, err := source.NewRawRecord([]byte(
		`{"DOI":"10.1000/stream-time"}`,
	))
	if err != nil {
		t.Fatalf("NewRawRecord() error = %v", err)
	}
	record := source.Record{
		Source:         source.Crossref,
		SourceRecordID: identity.Value(),
		Identity:       identity,
		Raw:            raw,
		CreatedAt:      &createdAt,
		UpdatedAt:      &updatedAt,
	}

	for _, test := range []struct {
		kind commandKind
		want time.Time
	}{
		{kind: commandSyncCrossrefCreated, want: createdAt},
		{kind: commandSyncCrossrefUpdated, want: updatedAt},
	} {
		test := test
		t.Run(string(test.kind), func(t *testing.T) {
			t.Parallel()

			events := recordEventsForCommand(
				workerCommand{Kind: test.kind},
				source.Crossref,
				iter.Seq2[source.Record, error](func(
					yield func(source.Record, error) bool,
				) {
					yield(record, nil)
				}),
			)
			var got ingestion.Envelope
			var gotErr error
			for event, eventErr := range events {
				gotErr = eventErr
				if eventErr == nil {
					got = event.(ingestion.Envelope)
				}
			}
			if gotErr != nil {
				t.Fatalf("recordEventsForCommand() error = %v", gotErr)
			}
			if !got.SourceTime.Equal(test.want) {
				t.Fatalf(
					"SourceTime = %s, want stream time %s",
					got.SourceTime,
					test.want,
				)
			}
		})
	}
}

func TestCrossrefCommandsBuildTheMatchingSourceStreamQuery(t *testing.T) {
	t.Parallel()

	from := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.July, 18, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		kind       commandKind
		wantStream crossref.Stream
	}{
		{
			kind:       commandSyncCrossrefCreated,
			wantStream: crossref.StreamCreated,
		},
		{
			kind:       commandSyncCrossrefUpdated,
			wantStream: crossref.StreamUpdated,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(string(test.kind), func(t *testing.T) {
			t.Parallel()

			query, err := crossrefQuery(workerCommand{
				Kind:       test.kind,
				FromDate:   from,
				ToDate:     to,
				ISSNs:      []string{"0028-0836"},
				MaxResults: 25,
			})
			if err != nil {
				t.Fatalf("crossrefQuery() error = %v", err)
			}
			if query.Stream != test.wantStream {
				t.Fatalf(
					"Stream = %q, want %q",
					query.Stream,
					test.wantStream,
				)
			}
			if !query.DateWindow.From.Equal(from) ||
				!query.DateWindow.To.Equal(to) {
				t.Fatalf("DateWindow = %#v", query.DateWindow)
			}
			if len(query.ISSNs) != 1 ||
				query.ISSNs[0] != "0028-0836" ||
				query.MaxResults != 25 {
				t.Fatalf("query = %#v", query)
			}
		})
	}
}

func TestGenericCrossrefSyncIsRejectedBecauseIndexedIsNotADiscoveryStream(t *testing.T) {
	t.Parallel()

	_, _, err := parseWorkerCommand([]string{
		"sync",
		"crossref",
		"--from-date",
		"2026-07-17",
		"--to-date",
		"2026-07-18",
		"--max-results",
		"10",
	})
	if err == nil {
		t.Fatal("parseWorkerCommand() accepted ambiguous generic Crossref sync")
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "crossref-created") ||
		!strings.Contains(message, "crossref-updated") {
		t.Fatalf(
			"error = %v, want explicit created and updated command names",
			err,
		)
	}
}

func TestCrossrefStreamsRequireACompleteClaimedInterval(t *testing.T) {
	t.Parallel()

	for _, target := range []string{"crossref-created", "crossref-updated"} {
		target := target
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			_, _, err := parseWorkerCommand([]string{
				"sync",
				target,
				"--issn",
				"0028-0836",
				"--max-results",
				"10",
			})
			if err == nil {
				t.Fatal("parseWorkerCommand() accepted a stream without claimed interval")
			}
			if !strings.Contains(
				strings.ToLower(err.Error()),
				"both from-date and to-date",
			) {
				t.Fatalf("error = %v, want complete claimed interval", err)
			}
		})
	}
}

func TestExecuteCrossrefConnectorRunPersistsPagesAndAdvancesExclusiveWatermarkLast(
	t *testing.T,
) {
	t.Parallel()

	from := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.July, 18, 0, 0, 0, 0, time.UTC)
	command := workerCommand{
		Kind:       commandSyncCrossrefCreated,
		FromDate:   from,
		ToDate:     to,
		ISSNs:      []string{"0028-0836"},
		MaxResults: 10,
	}
	job, err := ingestion.NewJob(
		"sync/crossref/test",
		source.Crossref,
		"connector-idempotency",
		map[string]any{"kind": command.Kind},
	)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	store := &fakeConnectorRunStore{}
	record := crossrefWorkerRecord(t)
	observedAt := time.Date(
		2026,
		time.July,
		19,
		8,
		0,
		0,
		0,
		time.UTC,
	)
	fetch := func(
		ctx context.Context,
		query crossref.Query,
		recordPage crossref.PageReceiptRecorder,
	) source.ClientSequence {
		if query.Stream != crossref.StreamCreated {
			t.Fatalf("query stream = %q", query.Stream)
		}
		return func(yield func(source.Record, error) bool) {
			store.calls = append(store.calls, "fetch")
			if err := recordPage(ctx, crossref.PageReceipt{
				Ordinal:       1,
				CursorIn:      "*",
				CursorOut:     "",
				ContentSHA256: strings.Repeat("a", 64),
				RecordCount:   1,
				ObservedAt:    observedAt,
			}); err != nil {
				yield(source.Record{}, err)
				return
			}
			yield(record, nil)
		}
	}
	runIngestion := func(
		_ context.Context,
		pending ingestion.Job,
		events ingestion.EventSequence,
	) (ingestion.JobSummary, error) {
		store.calls = append(store.calls, "ingest")
		if pending.IdempotencyKey != job.IdempotencyKey {
			t.Fatalf("pending job = %#v", pending)
		}
		count := 0
		for event, eventErr := range events {
			if eventErr != nil {
				return ingestion.JobSummary{}, eventErr
			}
			envelope, ok := event.(ingestion.Envelope)
			if !ok {
				t.Fatalf("event type = %T, want ingestion.Envelope", event)
			}
			if envelope.RawObservation == nil ||
				envelope.RawObservation.ConnectorRunID != store.run.ID ||
				envelope.RawObservation.PageOrdinal != 1 ||
				envelope.RawObservation.RecordOrdinal != 1 ||
				!envelope.RawObservation.ObservedAt.Equal(observedAt) {
				t.Fatalf(
					"event RawObservation = %#v",
					envelope.RawObservation,
				)
			}
			count++
		}
		if count != 1 {
			t.Fatalf("events = %d, want 1", count)
		}
		store.calls = append(store.calls, "ingested")
		return ingestion.JobSummary{
			JobID:       "job-1",
			Status:      ingestion.JobStatusSucceeded,
			RawInserted: 1,
			Projected:   1,
		}, nil
	}

	summary, err := executeCrossrefConnectorRun(
		context.Background(),
		command,
		job,
		store,
		fetch,
		runIngestion,
	)
	if err != nil {
		t.Fatalf("executeCrossrefConnectorRun() error = %v", err)
	}
	if summary.JobID != "job-1" || summary.RawInserted != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if !store.claim.From.Equal(from) ||
		!store.claim.Until.Equal(to.Add(24*time.Hour)) ||
		store.claim.Stream != "created" ||
		store.claim.IdempotencyKey != job.IdempotencyKey {
		t.Fatalf("claim = %#v", store.claim)
	}
	if len(store.pages) != 1 ||
		store.pages[0].RunID != store.run.ID ||
		store.pages[0].PageOrdinal != 1 {
		t.Fatalf("pages = %#v", store.pages)
	}
	if !slices.Equal(store.calls, []string{
		"start",
		"ingest",
		"fetch",
		"page",
		"ingested",
		"succeed",
	}) {
		t.Fatalf("calls = %v", store.calls)
	}
}

func TestExecuteCrossrefConnectorRunFailsWithoutAdvancingWatermark(t *testing.T) {
	t.Parallel()

	command := workerCommand{
		Kind:       commandSyncCrossrefUpdated,
		FromDate:   time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC),
		ToDate:     time.Date(2026, time.July, 18, 0, 0, 0, 0, time.UTC),
		MaxResults: 10,
	}
	job, err := ingestion.NewJob(
		"sync/crossref/test",
		source.Crossref,
		"connector-failure",
		map[string]any{"kind": command.Kind},
	)
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}

	tests := []struct {
		name      string
		fetch     crossrefFetchFunc
		run       ingestionRunFunc
		storeErr  error
		wantStage string
		wantCode  string
	}{
		{
			name: "fetch",
			fetch: func(
				context.Context,
				crossref.Query,
				crossref.PageReceiptRecorder,
			) source.ClientSequence {
				return func(yield func(source.Record, error) bool) {
					yield(source.Record{}, errors.New("malformed Crossref page"))
				}
			},
			run:       consumeWorkerEvents,
			wantStage: "fetch",
			wantCode:  "crossref_fetch_failed",
		},
		{
			name: "ingestion",
			fetch: func(
				context.Context,
				crossref.Query,
				crossref.PageReceiptRecorder,
			) source.ClientSequence {
				return func(func(source.Record, error) bool) {}
			},
			run: func(
				context.Context,
				ingestion.Job,
				ingestion.EventSequence,
			) (ingestion.JobSummary, error) {
				return ingestion.JobSummary{}, errors.New("projection failed")
			},
			wantStage: "ingestion",
			wantCode:  "ingestion_failed",
		},
		{
			name: "watermark CAS",
			fetch: func(
				context.Context,
				crossref.Query,
				crossref.PageReceiptRecorder,
			) source.ClientSequence {
				return func(func(source.Record, error) bool) {}
			},
			run:       successfulWorkerIngestion,
			storeErr:  ingestion.ErrWatermarkConflict,
			wantStage: "watermark",
			wantCode:  "watermark_advance_failed",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := &fakeConnectorRunStore{succeedErr: test.storeErr}
			_, runErr := executeCrossrefConnectorRun(
				context.Background(),
				command,
				job,
				store,
				test.fetch,
				test.run,
			)
			if runErr == nil {
				t.Fatal("executeCrossrefConnectorRun() error = nil")
			}
			if len(store.failed) != 1 ||
				store.failed[0].FailureStage != test.wantStage ||
				store.failed[0].FailureCode != test.wantCode {
				t.Fatalf("failed runs = %#v", store.failed)
			}
			if test.storeErr == nil && slices.Contains(store.calls, "succeed") {
				t.Fatalf("calls = %v, failure unexpectedly advanced watermark", store.calls)
			}
		})
	}
}

type fakeConnectorRunStore struct {
	claim      ingestion.ConnectorClaim
	run        ingestion.ConnectorRun
	pages      []ingestion.ConnectorPageReceipt
	failed     []ingestion.ConnectorRun
	calls      []string
	succeedErr error
}

func (store *fakeConnectorRunStore) Start(
	_ context.Context,
	claim ingestion.ConnectorClaim,
) (ingestion.ConnectorRun, error) {
	store.calls = append(store.calls, "start")
	store.claim = claim
	run, err := ingestion.RestoreConnectorRun(
		"00000000-0000-0000-0000-000000000901",
		claim,
		0,
		ingestion.ConnectorRunRunning,
	)
	if err != nil {
		return ingestion.ConnectorRun{}, err
	}
	store.run = run
	return run, nil
}

func (store *fakeConnectorRunStore) RecordPage(
	_ context.Context,
	_ ingestion.ConnectorRun,
	receipt ingestion.ConnectorPageReceipt,
) error {
	store.calls = append(store.calls, "page")
	store.pages = append(store.pages, receipt)
	return nil
}

func (store *fakeConnectorRunStore) Succeed(
	_ context.Context,
	_ ingestion.ConnectorRun,
) error {
	store.calls = append(store.calls, "succeed")
	return store.succeedErr
}

func (store *fakeConnectorRunStore) Fail(
	_ context.Context,
	run ingestion.ConnectorRun,
) error {
	store.calls = append(store.calls, "fail")
	store.failed = append(store.failed, run)
	return nil
}

func consumeWorkerEvents(
	_ context.Context,
	_ ingestion.Job,
	events ingestion.EventSequence,
) (ingestion.JobSummary, error) {
	for _, err := range events {
		if err != nil {
			return ingestion.JobSummary{}, err
		}
	}
	return successfulWorkerSummary(), nil
}

func successfulWorkerIngestion(
	context.Context,
	ingestion.Job,
	ingestion.EventSequence,
) (ingestion.JobSummary, error) {
	return successfulWorkerSummary(), nil
}

func successfulWorkerSummary() ingestion.JobSummary {
	return ingestion.JobSummary{
		JobID:  "job-1",
		Status: ingestion.JobStatusSucceeded,
	}
}

func crossrefWorkerRecord(t *testing.T) source.Record {
	t.Helper()

	identity, err := paper.NewIdentifier(
		paper.SchemeDOI,
		"10.1000/connector-worker",
	)
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	raw, err := source.NewRawRecord([]byte(
		`{"DOI":"10.1000/connector-worker"}`,
	))
	if err != nil {
		t.Fatalf("NewRawRecord() error = %v", err)
	}
	updatedAt := time.Date(
		2026,
		time.July,
		17,
		12,
		0,
		0,
		0,
		time.UTC,
	)
	return source.Record{
		Source:         source.Crossref,
		SourceRecordID: identity.Value(),
		Identity:       identity,
		Raw:            raw,
		CreatedAt:      &updatedAt,
		UpdatedAt:      &updatedAt,
	}
}
