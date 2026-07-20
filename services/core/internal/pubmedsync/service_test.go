package pubmedsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ingestion"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
)

type serviceSearchCall struct {
	query pubmed.SearchQuery
}

type serviceFakeSearcher struct {
	calls  []serviceSearchCall
	counts []int
}

func (searcher *serviceFakeSearcher) Search(
	_ context.Context,
	query pubmed.SearchQuery,
) (pubmed.SearchResult, error) {
	searcher.calls = append(searcher.calls, serviceSearchCall{query: query})
	count := 1
	if len(searcher.counts) > 0 {
		count = searcher.counts[0]
		searcher.counts = searcher.counts[1:]
	}
	result := pubmed.SearchResult{
		Count:    count,
		WebEnv:   "service-test-web-env",
		QueryKey: "1",
	}
	if count > 0 {
		result.Batches = []pubmed.Batch{{RetStart: 0, RetMax: count}}
	}
	return result, nil
}

type serviceConfiguredSearcher struct {
	calls  []serviceSearchCall
	result pubmed.SearchResult
	err    error
}

func (searcher *serviceConfiguredSearcher) Search(
	_ context.Context,
	query pubmed.SearchQuery,
) (pubmed.SearchResult, error) {
	searcher.calls = append(searcher.calls, serviceSearchCall{query: query})
	return searcher.result, searcher.err
}

type serviceFakeFetcher struct {
	calls []pubmed.SearchResult
}

func (fetcher *serviceFakeFetcher) Fetch(
	_ context.Context,
	result pubmed.SearchResult,
) source.ClientSequence {
	fetcher.calls = append(fetcher.calls, result)
	return func(yield func(source.Record, error) bool) {}
}

type serviceRunCall struct {
	job ingestion.Job
}

type serviceFakeIngestion struct {
	calls       []serviceRunCall
	failByKey   map[string]error
	summarySeed int
}

func (runner *serviceFakeIngestion) Run(
	_ context.Context,
	job ingestion.Job,
	events ingestion.EventSequence,
) (ingestion.JobSummary, error) {
	runner.calls = append(runner.calls, serviceRunCall{job: job})
	if err := consumeServiceEvents(events); err != nil {
		return ingestion.JobSummary{}, err
	}
	if err := runner.failByKey[job.IdempotencyKey]; err != nil {
		return ingestion.JobSummary{}, err
	}
	runner.summarySeed++
	return ingestion.NewJobSummary(
		"job-"+job.IdempotencyKey,
		ingestion.JobStatusSucceeded,
		ingestion.JobSummaryCounts{
			RawInserted: 1,
			Projected:   1,
		},
	)
}

type serviceSummaryErrorIngestion struct {
	calls        []serviceRunCall
	summaryByKey map[string]ingestion.JobSummary
	errorByKey   map[string]error
}

func (runner *serviceSummaryErrorIngestion) Run(
	_ context.Context,
	job ingestion.Job,
	events ingestion.EventSequence,
) (ingestion.JobSummary, error) {
	runner.calls = append(runner.calls, serviceRunCall{job: job})
	if err := consumeServiceEvents(events); err != nil {
		return ingestion.JobSummary{}, err
	}
	summary := runner.summaryByKey[job.IdempotencyKey]
	if summary.JobID == "" {
		var err error
		summary, err = ingestion.NewJobSummary(
			"job-"+job.IdempotencyKey,
			ingestion.JobStatusSucceeded,
			ingestion.JobSummaryCounts{
				RawInserted: 1,
				Projected:   1,
			},
		)
		if err != nil {
			return ingestion.JobSummary{}, err
		}
	}
	return summary, runner.errorByKey[job.IdempotencyKey]
}

func consumeServiceEvents(events ingestion.EventSequence) error {
	for _, eventErr := range events {
		if eventErr != nil {
			return eventErr
		}
	}
	return nil
}

func TestServiceBackfillUsesFixedWindowCompleteISSNSetAndStableJobKey(t *testing.T) {
	t.Parallel()

	searcher := &serviceFakeSearcher{counts: []int{1, 1, 1}}
	fetcher := &serviceFakeFetcher{}
	runner := &serviceFakeIngestion{failByKey: map[string]error{}}
	var eventCalls int
	service, err := NewService(
		searcher,
		fetcher,
		runner.Run,
		func(source.ClientSequence) ingestion.EventSequence {
			eventCalls++
			return func(yield func(ingestion.Event, error) bool) {}
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	registry := encodeRegistryCSV(
		testRegistryHeader,
		validRegistryRecord(
			"Backfill Journal",
			1,
			"1234-5679",
			"",
			"2049-3630",
			`["1234-5679","2049-3630"]`,
			"resolved",
			"yes",
			"1",
		),
		validRegistryRecord(
			"Backfill No Coverage",
			2,
			"9876-5434",
			"",
			"",
			`["9876-5434"]`,
			"resolved",
			"no",
			"0",
		),
		validRegistryRecord(
			"Backfill Unknown Coverage",
			3,
			"1476-4687",
			"",
			"",
			`["1476-4687"]`,
			"resolved",
			"unknown",
			"0",
		),
	)
	newRequest := func() RunRequest {
		return RunRequest{
			Mode:         ModeBackfill,
			LookbackDays: 3,
			Registry:     bytes.NewReader(registry),
		}
	}

	first, err := service.Run(context.Background(), newRequest())
	if err != nil {
		t.Fatalf("first Run() error = %v", err)
	}
	second, err := service.Run(context.Background(), newRequest())
	if err != nil {
		t.Fatalf("second Run() error = %v", err)
	}

	if first.WindowsPlanned != 1 || first.WindowsSucceeded != 1 ||
		first.WindowsFailed != 0 {
		t.Fatalf("first report = %#v", first)
	}
	if second.WindowsPlanned != 1 || second.WindowsSucceeded != 1 {
		t.Fatalf("second report = %#v", second)
	}
	if first.RegistryJournals != 3 || first.EligibleJournals != 1 ||
		first.JournalsPlanned != 1 || first.NoCoverage != 1 ||
		first.BatchesPlanned != 0 {
		t.Fatalf("backfill registry report = %#v", first)
	}
	if len(searcher.calls) != 4 {
		t.Fatalf("Search() calls = %d, want planner plus ingestion for each run", len(searcher.calls))
	}
	for _, call := range searcher.calls {
		if call.query.DateType != pubmed.DateTypePublication {
			t.Fatalf("Search() date type = %q, want publication", call.query.DateType)
		}
		if !slices.Equal(call.query.JournalISSNs, []string{"1234-5679", "2049-3630"}) {
			t.Fatalf("Search() ISSNs = %v, want complete registry ISSN set", call.query.JournalISSNs)
		}
		if call.query.DateWindow.From.Format(time.DateOnly) != "2023-07-19" ||
			call.query.DateWindow.To.Format(time.DateOnly) != "2026-07-19" {
			t.Fatalf(
				"Search() window = %s..%s, want fixed three-year window",
				call.query.DateWindow.From.Format(time.DateOnly),
				call.query.DateWindow.To.Format(time.DateOnly),
			)
		}
		if call.query.MaxResults != pubmed.MaxSearchResults {
			t.Fatalf("Search() MaxResults = %d, want %d", call.query.MaxResults, pubmed.MaxSearchResults)
		}
	}
	if len(fetcher.calls) != 2 || len(runner.calls) != 2 || eventCalls != 2 {
		t.Fatalf(
			"Fetch/ingestion/events = %d/%d/%d, want 2/2/2",
			len(fetcher.calls),
			len(runner.calls),
			eventCalls,
		)
	}
	if runner.calls[0].job.IdempotencyKey != runner.calls[1].job.IdempotencyKey {
		t.Fatalf("repeated execution changed idempotency key: %#v", runner.calls)
	}
	payload := runner.calls[0].job.Payload
	for key, want := range map[string]string{
		"mode":        "backfill",
		"date_type":   "publication",
		"from_date":   "2023-07-19",
		"to_date":     "2026-07-19",
		"journal_key": "issn:1234-5679,2049-3630",
	} {
		if got := payload[key]; got != want {
			t.Fatalf("payload[%q] = %#v, want %#v", key, got, want)
		}
	}
	if payload["window_key"] != runner.calls[0].job.IdempotencyKey {
		t.Fatalf("payload window_key = %#v, want idempotency key %q", payload["window_key"], runner.calls[0].job.IdempotencyKey)
	}
	if !slices.Equal(
		payload["journal_issns"].([]string),
		[]string{"1234-5679", "2049-3630"},
	) {
		t.Fatalf("payload journal_issns = %#v, want complete ISSN set", payload["journal_issns"])
	}
	if !strings.Contains(runner.calls[0].job.BatchKey, runner.calls[0].job.IdempotencyKey) {
		t.Fatalf("batch key %q does not include window key %q", runner.calls[0].job.BatchKey, runner.calls[0].job.IdempotencyKey)
	}
}

func TestServiceDailyRunsTwoBatchesInWindowOrderWithBatchReportsAndPayloads(t *testing.T) {
	t.Parallel()

	searcher := &serviceFakeSearcher{}
	fetcher := &serviceFakeFetcher{}
	runner := &serviceFakeIngestion{failByKey: map[string]error{}}
	var eventCalls int
	service, err := NewService(
		searcher,
		fetcher,
		runner.Run,
		func(source.ClientSequence) ingestion.EventSequence {
			eventCalls++
			return func(yield func(ingestion.Event, error) bool) {}
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	firstISSNs := dailyTestISSNs(t, 5_000_000, MaxDailyISSNTerms-1)
	secondISSNs := dailyTestISSNs(t, 6_000_000, 2)
	registry := encodeRegistryCSV(
		testRegistryHeader,
		serviceRegistryRecordWithISSNs(
			"First Daily Journal",
			1,
			firstISSNs,
			"yes",
			"1",
		),
		serviceRegistryRecordWithISSNs(
			"Second Daily Journal",
			2,
			secondISSNs,
			"unknown",
			"0",
		),
	)

	report, err := service.Run(context.Background(), RunRequest{
		Mode:         ModeDaily,
		RunDate:      time.Date(2026, 7, 19, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)),
		LookbackDays: 3,
		Registry:     bytes.NewReader(registry),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.WindowsPlanned != 4 || report.WindowsAttempted != 4 ||
		report.WindowsSucceeded != 4 || report.WindowsFailed != 0 ||
		report.BatchesPlanned != 2 {
		t.Fatalf("report = %#v", report)
	}
	if report.RegistryJournals != 2 || report.EligibleJournals != 2 ||
		report.JournalsPlanned != 2 || report.JournalsFailed != 0 {
		t.Fatalf("journal report totals = %#v", report)
	}
	if got := report.Result()["batches_planned"]; got != 2 {
		t.Fatalf("Result()[batches_planned] = %#v, want 2", got)
	}
	if len(searcher.calls) != 4 || len(fetcher.calls) != 4 ||
		len(runner.calls) != 4 || eventCalls != 4 {
		t.Fatalf(
			"Search/Fetch/ingestion/events = %d/%d/%d/%d, want 4/4/4/4",
			len(searcher.calls),
			len(fetcher.calls),
			len(runner.calls),
			eventCalls,
		)
	}
	wantTypes := []pubmed.DateType{
		pubmed.DateTypeEntrez,
		pubmed.DateTypeModification,
		pubmed.DateTypeEntrez,
		pubmed.DateTypeModification,
	}
	wantISSNs := [][]string{firstISSNs, firstISSNs, secondISSNs, secondISSNs}
	for index, call := range searcher.calls {
		if call.query.DateType != wantTypes[index] {
			t.Errorf("Search() call %d date type = %q, want %q", index, call.query.DateType, wantTypes[index])
		}
		if !slices.Equal(call.query.JournalISSNs, wantISSNs[index]) {
			t.Errorf(
				"Search() call %d ISSNs count/content = %d/%v, want %d/%v",
				index,
				len(call.query.JournalISSNs),
				call.query.JournalISSNs,
				len(wantISSNs[index]),
				wantISSNs[index],
			)
		}
		if call.query.DateWindow.From.Format(time.DateOnly) != "2026-07-17" ||
			call.query.DateWindow.To.Format(time.DateOnly) != "2026-07-19" {
			t.Errorf("Search() call %d window = %s..%s", index, call.query.DateWindow.From.Format(time.DateOnly), call.query.DateWindow.To.Format(time.DateOnly))
		}
		if call.query.MaxResults != pubmed.MaxSearchResults {
			t.Errorf("Search() call %d MaxResults = %d, want %d", index, call.query.MaxResults, pubmed.MaxSearchResults)
		}
	}
	if len(report.Journals) != 2 {
		t.Fatalf("journal reports = %d, want 2", len(report.Journals))
	}
	for _, journalReport := range report.Journals {
		if journalReport.Status != "succeeded" ||
			journalReport.WindowsPlanned != 2 ||
			journalReport.WindowsSucceeded != 2 ||
			journalReport.WindowsFailed != 0 {
			t.Errorf("journal report = %#v, want two successful windows", journalReport)
		}
	}
	for index, windowReport := range report.Windows {
		if windowReport.JournalKey != "" || windowReport.JournalName != "" {
			t.Errorf("daily window %d has forged singular journal identity: %#v", index, windowReport)
		}
		if windowReport.JournalCount != 1 ||
			len(windowReport.JournalKeys) != 1 ||
			windowReport.BatchKey == "" {
			t.Errorf("daily window %d batch evidence = %#v", index, windowReport)
		}
		if !slices.Equal(windowReport.ISSNs, wantISSNs[index]) {
			t.Errorf("daily window %d report ISSNs = %v, want %v", index, windowReport.ISSNs, wantISSNs[index])
		}
	}
	for index, call := range runner.calls {
		windowReport := report.Windows[index]
		payload := call.job.Payload
		if call.job.IdempotencyKey != windowReport.WindowKey {
			t.Errorf("job %d idempotency key = %q, want %q", index, call.job.IdempotencyKey, windowReport.WindowKey)
		}
		for key, want := range map[string]any{
			"mode":          "daily",
			"batch_key":     windowReport.BatchKey,
			"journal_count": 1,
			"term_count":    len(wantISSNs[index]),
			"date_type":     string(wantTypes[index]),
			"from_date":     "2026-07-17",
			"to_date":       "2026-07-19",
			"run_date":      "2026-07-19",
			"window_key":    windowReport.WindowKey,
		} {
			if got := payload[key]; got != want {
				t.Errorf("job %d payload[%q] = %#v, want %#v", index, key, got, want)
			}
		}
		if _, exists := payload["journal_key"]; exists {
			t.Errorf("job %d payload contains forbidden journal_key", index)
		}
		if _, exists := payload["journal_name"]; exists {
			t.Errorf("job %d payload contains forbidden journal_name", index)
		}
		if !slices.Equal(payload["journal_keys"].([]string), windowReport.JournalKeys) ||
			!slices.Equal(payload["journal_names"].([]string), []string{report.Journals[index/2].JournalName}) ||
			!slices.Equal(payload["issns"].([]string), wantISSNs[index]) {
			t.Errorf("job %d payload batch slices = %#v", index, payload)
		}
	}
}

func TestServiceContinuesAfterDailyBatchFailuresWithoutDoubleCountingJournals(t *testing.T) {
	t.Parallel()

	searcher := &serviceFakeSearcher{}
	fetcher := &serviceFakeFetcher{}
	sentinel := errors.New("daily batch failed")
	runner := &serviceFakeIngestion{failByKey: map[string]error{}}
	service, err := NewService(
		searcher,
		fetcher,
		runner.Run,
		func(records source.ClientSequence) ingestion.EventSequence {
			return recordEventsForServiceTest(records)
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	firstISSNs := dailyTestISSNs(t, 6_100_000, MaxDailyISSNTerms-1)
	secondISSNs := dailyTestISSNs(t, 7_100_000, 2)
	registry := encodeRegistryCSV(
		testRegistryHeader,
		serviceRegistryRecordWithISSNs("Failed Batch Journal", 1, firstISSNs, "yes", "1"),
		serviceRegistryRecordWithISSNs("Later Batch Journal", 2, secondISSNs, "yes", "1"),
	)
	journals, err := LoadResolvedRegistry(bytes.NewReader(registry))
	if err != nil {
		t.Fatalf("LoadResolvedRegistry() error = %v", err)
	}
	windows, err := PlanDailyBatches(
		journals,
		time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("PlanDailyBatches() error = %v", err)
	}
	runner.failByKey[windows[0].Key()] = sentinel
	runner.failByKey[windows[1].Key()] = sentinel

	report, runErr := service.Run(context.Background(), RunRequest{
		Mode:         ModeDaily,
		RunDate:      time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
		LookbackDays: 3,
		Registry:     bytes.NewReader(registry),
	})
	if runErr == nil || !errors.Is(runErr, sentinel) {
		t.Fatalf("Run() error = %v, want batch failure", runErr)
	}
	if len(searcher.calls) != 4 || len(runner.calls) != 4 {
		t.Fatalf("Search/ingestion calls = %d/%d, want 4/4", len(searcher.calls), len(runner.calls))
	}
	if report.WindowsFailed != 2 || report.WindowsSucceeded != 2 ||
		report.JournalsFailed != 1 {
		t.Fatalf("failure totals = %#v", report)
	}
	if got := report.Journals[0]; got.Status != "failed" ||
		got.WindowsFailed != 2 || got.WindowsSucceeded != 0 {
		t.Fatalf("failed journal report = %#v", got)
	}
	if got := report.Journals[1]; got.Status != "succeeded" ||
		got.WindowsFailed != 0 || got.WindowsSucceeded != 2 {
		t.Fatalf("later journal report = %#v", got)
	}
	if len(report.Failures) != 2 {
		t.Fatalf("failures = %d, want one per failed batch window", len(report.Failures))
	}
	for index, failure := range report.Failures {
		if failure.JournalKey != "" || failure.JournalName != "" ||
			failure.BatchKey != windows[index].BatchKey() ||
			!slices.Equal(failure.JournalKeys, []string{journals[0].Key()}) ||
			!slices.Equal(failure.ISSNs, firstISSNs) {
			t.Errorf("failure %d batch evidence = %#v", index, failure)
		}
	}
	report.Windows[0].JournalKeys[0] = "mutated-window-report"
	report.Windows[0].ISSNs[0] = secondISSNs[0]
	if report.Failures[0].JournalKeys[0] != journals[0].Key() ||
		report.Failures[0].ISSNs[0] != firstISSNs[0] {
		t.Fatal("window and failure reports share mutable batch slices")
	}
	payload := runner.calls[0].job.Payload
	if payload["journal_keys"].([]string)[0] != journals[0].Key() ||
		payload["issns"].([]string)[0] != firstISSNs[0] {
		t.Fatal("window report mutation changed ingestion payload slices")
	}
}

func TestServiceRejectsDailyBatchSearchCountAtPubMedLimitBeforeFetch(t *testing.T) {
	t.Parallel()

	searcher := &serviceConfiguredSearcher{
		result: pubmed.SearchResult{
			Count:    pubmed.MaxSearchResults,
			WebEnv:   "service-test-web-env",
			QueryKey: "1",
		},
	}
	fetcher := &serviceFakeFetcher{}
	runner := &serviceFakeIngestion{failByKey: map[string]error{}}
	service, err := NewService(
		searcher,
		fetcher,
		runner.Run,
		func(records source.ClientSequence) ingestion.EventSequence {
			return recordEventsForServiceTest(records)
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	report, runErr := service.Run(context.Background(), RunRequest{
		Mode:         ModeDaily,
		RunDate:      time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
		LookbackDays: 3,
		Registry: bytes.NewReader(encodeRegistryCSV(
			testRegistryHeader,
			validRegistryRecord(
				"Count Limit Journal",
				1,
				"1234-5679",
				"",
				"",
				`["1234-5679"]`,
				"resolved",
				"yes",
				"1",
			),
		)),
	})
	if runErr == nil ||
		!strings.Contains(strings.ToLower(runErr.Error()), "limit") {
		t.Fatalf("Run() error = %v, want explicit PubMed limit error", runErr)
	}
	if len(searcher.calls) != 2 {
		t.Fatalf("Search() calls = %d, want one per daily window", len(searcher.calls))
	}
	if len(fetcher.calls) != 0 {
		t.Fatalf("Fetch() calls = %d, want zero at PubMed limit", len(fetcher.calls))
	}
	if len(runner.calls) != 2 {
		t.Fatalf("runIngestion calls = %d, want failed jobs to be persisted", len(runner.calls))
	}
	if report.WindowsFailed != 2 || report.WindowsSucceeded != 0 {
		t.Fatalf(
			"report window status = %d/%d, want 2 failed and 0 succeeded",
			report.WindowsFailed,
			report.WindowsSucceeded,
		)
	}
	if report.BatchesPlanned != 1 || report.JournalsFailed != 1 ||
		len(report.Failures) != 2 ||
		report.Failures[0].BatchKey == "" ||
		!slices.Equal(report.Failures[0].ISSNs, []string{"1234-5679"}) {
		t.Fatalf("batch limit report = %#v", report)
	}
}

func recordEventsForServiceTest(records source.ClientSequence) ingestion.EventSequence {
	return func(yield func(ingestion.Event, error) bool) {
		for record, err := range records {
			if !yield(nil, err) {
				return
			}
			if err != nil {
				return
			}
			_ = record
		}
	}
}

func TestServiceMergesPersistedSummaryWhenIngestionReturnsSummaryAndError(t *testing.T) {
	t.Parallel()

	searcher := &serviceFakeSearcher{}
	fetcher := &serviceFakeFetcher{}
	sentinel := errors.New("persisted ingestion failure")
	firstWindows, err := PlanDailyBatches(
		[]Journal{mustServiceJournal(t, "1234-5679")},
		time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("PlanDailyBatches() error = %v", err)
	}
	failedSummary, err := ingestion.NewJobSummary(
		"persisted-failed-job",
		ingestion.JobStatusSucceeded,
		ingestion.JobSummaryCounts{
			RawInserted: 2,
			RawReused:   3,
			Projected:   4,
			Excluded:    5,
			Deleted:     6,
			Unchanged:   7,
			Failed:      8,
		},
	)
	if err != nil {
		t.Fatalf("NewJobSummary() error = %v", err)
	}
	runner := &serviceSummaryErrorIngestion{
		summaryByKey: map[string]ingestion.JobSummary{
			firstWindows[0].Key(): failedSummary,
		},
		errorByKey: map[string]error{
			firstWindows[0].Key(): sentinel,
		},
	}
	service, err := NewService(
		searcher,
		fetcher,
		runner.Run,
		func(source.ClientSequence) ingestion.EventSequence {
			return func(yield func(ingestion.Event, error) bool) {}
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	report, err := service.Run(context.Background(), RunRequest{
		Mode:         ModeDaily,
		RunDate:      time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
		LookbackDays: 3,
		Registry: bytes.NewReader(encodeRegistryCSV(
			testRegistryHeader,
			validRegistryRecord(
				"Summary Error Journal",
				1,
				"1234-5679",
				"",
				"",
				`["1234-5679"]`,
				"resolved",
				"yes",
				"1",
			),
		)),
	})
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("Run() error = %v, want persisted ingestion failure", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("ingestion calls = %d, want later batch window to continue", len(runner.calls))
	}
	if report.WindowsFailed != 1 || report.WindowsSucceeded != 1 {
		t.Fatalf(
			"window status counts = %d/%d, want 1 failed and 1 succeeded",
			report.WindowsFailed,
			report.WindowsSucceeded,
		)
	}
	if report.RawInserted != 3 ||
		report.RawReused != 3 ||
		report.Projected != 5 ||
		report.Excluded != 5 ||
		report.Deleted != 6 ||
		report.Unchanged != 7 ||
		report.IngestionFailed != 8 {
		t.Fatalf("aggregated report = %#v", report)
	}
	var failedWindow *WindowReport
	for index := range report.Windows {
		if report.Windows[index].WindowKey == firstWindows[0].Key() {
			failedWindow = &report.Windows[index]
			break
		}
	}
	if failedWindow == nil || failedWindow.Status != "failed" ||
		failedWindow.RawInserted != failedSummary.RawInserted ||
		failedWindow.RawReused != failedSummary.RawReused ||
		failedWindow.Projected != failedSummary.Projected ||
		failedWindow.Excluded != failedSummary.Excluded ||
		failedWindow.Deleted != failedSummary.Deleted ||
		failedWindow.Unchanged != failedSummary.Unchanged ||
		failedWindow.Failed != failedSummary.Failed {
		t.Fatalf("failed window report = %#v, want persisted summary plus failure status", failedWindow)
	}
}

func TestServiceDailyIncludesResolvedNoAndUnknownCoverage(t *testing.T) {
	t.Parallel()

	searcher := &serviceFakeSearcher{}
	fetcher := &serviceFakeFetcher{}
	runner := &serviceFakeIngestion{failByKey: map[string]error{}}
	service, err := NewService(
		searcher,
		fetcher,
		runner.Run,
		func(source.ClientSequence) ingestion.EventSequence {
			return func(yield func(ingestion.Event, error) bool) {}
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	report, err := service.Run(context.Background(), RunRequest{
		Mode:         ModeDaily,
		RunDate:      time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
		LookbackDays: 3,
		Registry: bytes.NewReader(encodeRegistryCSV(
			testRegistryHeader,
			validRegistryRecord(
				"Covered",
				1,
				"1234-5679",
				"",
				"",
				`["1234-5679"]`,
				"resolved",
				"yes",
				"1",
			),
			validRegistryRecord(
				"No Coverage",
				2,
				"9876-5434",
				"",
				"",
				`["9876-5434"]`,
				"resolved",
				"no",
				"0",
			),
			validRegistryRecord(
				"Unknown Coverage",
				3,
				"1476-4687",
				"",
				"",
				`["1476-4687"]`,
				"resolved",
				"unknown",
				"0",
			),
		)),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.NoCoverage != 1 || report.EligibleJournals != 3 ||
		report.JournalsPlanned != 3 || report.BatchesPlanned != 1 ||
		report.WindowsPlanned != 2 {
		t.Fatalf("report coverage = %#v", report)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("ingestion calls = %d, want two batched daily windows", len(runner.calls))
	}
	if len(searcher.calls) != 2 {
		t.Fatalf("Search() calls = %d, want two batched daily windows", len(searcher.calls))
	}
	wantISSNs := []string{"1234-5679", "1476-4687", "9876-5434"}
	slices.Sort(wantISSNs)
	for index, call := range searcher.calls {
		if !slices.Equal(call.query.JournalISSNs, wantISSNs) {
			t.Errorf("Search() call %d ISSNs = %v, want all resolved %v", index, call.query.JournalISSNs, wantISSNs)
		}
	}
	for _, journalReport := range report.Journals {
		if journalReport.Status != "succeeded" ||
			journalReport.WindowsPlanned != 2 ||
			journalReport.WindowsSucceeded != 2 {
			t.Errorf("daily journal report = %#v", journalReport)
		}
	}
}

func TestServiceDailyBatchWindowJobUsesCompleteIdentity(t *testing.T) {
	t.Parallel()

	journals := []Journal{
		dailyTestJournal("journal:first", []string{"1234-5679"}),
		dailyTestJournal("journal:second", []string{"9876-5434"}),
	}
	windows, err := PlanDailyBatches(journals, parseTestDate("2026-07-19"))
	if err != nil {
		t.Fatalf("PlanDailyBatches() error = %v", err)
	}
	job, err := newDailyWindowJob(
		RunRequest{Mode: ModeDaily, RunDate: parseTestDate("2026-07-19")},
		windows[0],
	)
	if err != nil {
		t.Fatalf("newDailyWindowJob() error = %v", err)
	}
	if job.IdempotencyKey != windows[0].Key() {
		t.Fatalf("job idempotency key = %q, want %q", job.IdempotencyKey, windows[0].Key())
	}
	if got := job.Payload["batch_key"]; got != windows[0].BatchKey() {
		t.Fatalf("payload batch_key = %#v, want %q", got, windows[0].BatchKey())
	}
	if got := job.Payload["journal_count"]; got != 2 {
		t.Fatalf("payload journal_count = %#v, want 2", got)
	}
	if got := job.Payload["term_count"]; got != 2 {
		t.Fatalf("payload term_count = %#v, want 2", got)
	}
	for _, forbidden := range []string{"journal_key", "journal_name", "journal_identity", "journal_issns"} {
		if _, exists := job.Payload[forbidden]; exists {
			t.Errorf("daily payload contains forbidden singular key %q", forbidden)
		}
	}
}

func TestServiceDailyStopsImmediatelyOnContextFailure(t *testing.T) {
	t.Parallel()

	searcher := &serviceFakeSearcher{}
	fetcher := &serviceFakeFetcher{}
	var ingestionCalls int
	service, err := NewService(
		searcher,
		fetcher,
		func(
			_ context.Context,
			_ ingestion.Job,
			events ingestion.EventSequence,
		) (ingestion.JobSummary, error) {
			ingestionCalls++
			if err := consumeServiceEvents(events); err != nil {
				return ingestion.JobSummary{}, err
			}
			return ingestion.JobSummary{}, context.Canceled
		},
		func(source.ClientSequence) ingestion.EventSequence {
			return func(yield func(ingestion.Event, error) bool) {}
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	report, runErr := service.Run(context.Background(), RunRequest{
		Mode:         ModeDaily,
		RunDate:      parseTestDate("2026-07-19"),
		LookbackDays: 3,
		Registry: bytes.NewReader(encodeRegistryCSV(
			testRegistryHeader,
			validRegistryRecord(
				"Context Failure Journal",
				1,
				"1234-5679",
				"",
				"",
				`["1234-5679"]`,
				"resolved",
				"yes",
				"1",
			),
		)),
	})
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", runErr)
	}
	if ingestionCalls != 1 || len(searcher.calls) != 1 ||
		report.WindowsAttempted != 1 || report.WindowsFailed != 1 {
		t.Fatalf(
			"context stop calls/report = ingestion:%d search:%d report:%#v",
			ingestionCalls,
			len(searcher.calls),
			report,
		)
	}
}

func mustServiceJournal(t *testing.T, issn string) Journal {
	t.Helper()
	journals, err := LoadPubMedSupportedRegistry(bytes.NewReader(encodeRegistryCSV(
		testRegistryHeader,
		validRegistryRecord(
			"Service Journal",
			1,
			issn,
			"",
			"",
			`["`+issn+`"]`,
			"resolved",
			"yes",
			"1",
		),
	)))
	if err != nil {
		t.Fatalf("LoadPubMedSupportedRegistry() error = %v", err)
	}
	return journals[0]
}

func serviceRegistryRecordWithISSNs(
	title string,
	sourceOrder int,
	issns []string,
	pubmedSupported string,
	pubmedRecordCount string,
) []string {
	encodedISSNs, err := json.Marshal(issns)
	if err != nil {
		panic(err)
	}
	return validRegistryRecord(
		title,
		sourceOrder,
		issns[0],
		"",
		"",
		string(encodedISSNs),
		"resolved",
		pubmedSupported,
		pubmedRecordCount,
	)
}

var _ iter.Seq2[ingestion.Event, error] = func(yield func(ingestion.Event, error) bool) {}
