package pubmedsync

import (
	"bytes"
	"context"
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
	_ ingestion.EventSequence,
) (ingestion.JobSummary, error) {
	runner.calls = append(runner.calls, serviceRunCall{job: job})
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

func TestServiceDailyUsesEDATThenMDATAndContinuesAfterWindowFailure(t *testing.T) {
	t.Parallel()

	searcher := &serviceFakeSearcher{}
	fetcher := &serviceFakeFetcher{}
	sentinel := errors.New("window failed")
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
			"First Daily Journal",
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
			"Second Daily Journal",
			2,
			"9876-5434",
			"",
			"",
			`["9876-5434"]`,
			"resolved",
			"yes",
			"1",
		),
	)
	plannedFirst, err := PlanDaily(
		mustServiceJournal(t, "1234-5679"),
		time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("PlanDaily() error = %v", err)
	}
	runner.failByKey[plannedFirst[0].Key()] = sentinel

	report, err := service.Run(context.Background(), RunRequest{
		Mode:         ModeDaily,
		RunDate:      time.Date(2026, 7, 19, 12, 0, 0, 0, time.FixedZone("UTC+8", 8*60*60)),
		LookbackDays: 3,
		Registry:     bytes.NewReader(registry),
	})
	if err == nil || !errors.Is(err, sentinel) {
		t.Fatalf("Run() error = %v, want window failure", err)
	}
	if report.WindowsPlanned != 4 || report.WindowsAttempted != 4 ||
		report.WindowsSucceeded != 3 || report.WindowsFailed != 1 {
		t.Fatalf("report = %#v", report)
	}
	if report.NoCoverage != 0 {
		t.Fatalf("NoCoverage = %d, want 0", report.NoCoverage)
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
	for index, call := range searcher.calls {
		if call.query.DateType != wantTypes[index] {
			t.Errorf("Search() call %d date type = %q, want %q", index, call.query.DateType, wantTypes[index])
		}
		if call.query.DateWindow.From.Format(time.DateOnly) != "2026-07-17" ||
			call.query.DateWindow.To.Format(time.DateOnly) != "2026-07-19" {
			t.Errorf("Search() call %d window = %s..%s", index, call.query.DateWindow.From.Format(time.DateOnly), call.query.DateWindow.To.Format(time.DateOnly))
		}
	}
	if got := runner.calls[0].job.Payload["journal_name"]; got != "First Daily Journal" {
		t.Fatalf("first journal = %#v, want registry order", got)
	}
	if got := runner.calls[2].job.Payload["journal_name"]; got != "Second Daily Journal" {
		t.Fatalf("second journal = %#v, want registry order", got)
	}
}

func TestServiceReportsResolvedNoCoverageWithoutProcessingIt(t *testing.T) {
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
		)),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.NoCoverage != 1 || report.EligibleJournals != 1 {
		t.Fatalf("report coverage = %#v", report)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("ingestion calls = %d, want only covered journal windows", len(runner.calls))
	}
}

func TestServiceWindowJobKeyUsesCompleteTask4WindowIdentity(t *testing.T) {
	t.Parallel()

	firstJournal := mustServiceJournal(t, "1234-5679")
	secondJournal := mustServiceJournal(t, "9876-5434")
	first, err := NewSyncWindow(
		firstJournal,
		pubmed.DateTypeEntrez,
		dateWindow("2026-07-17", "2026-07-19"),
	)
	if err != nil {
		t.Fatalf("NewSyncWindow(first) error = %v", err)
	}
	same, err := NewSyncWindow(
		firstJournal,
		pubmed.DateTypeEntrez,
		dateWindow("2026-07-17", "2026-07-19"),
	)
	if err != nil {
		t.Fatalf("NewSyncWindow(same) error = %v", err)
	}
	differentISSNs, err := NewSyncWindow(
		secondJournal,
		pubmed.DateTypeEntrez,
		dateWindow("2026-07-17", "2026-07-19"),
	)
	if err != nil {
		t.Fatalf("NewSyncWindow(differentISSNs) error = %v", err)
	}
	differentDateType, err := NewSyncWindow(
		firstJournal,
		pubmed.DateTypeModification,
		dateWindow("2026-07-17", "2026-07-19"),
	)
	if err != nil {
		t.Fatalf("NewSyncWindow(differentDateType) error = %v", err)
	}
	differentDate, err := NewSyncWindow(
		firstJournal,
		pubmed.DateTypeEntrez,
		dateWindow("2026-07-16", "2026-07-19"),
	)
	if err != nil {
		t.Fatalf("NewSyncWindow(differentDate) error = %v", err)
	}

	firstJob, err := newWindowJob(
		RunRequest{Mode: ModeDaily, RunDate: parseTestDate("2026-07-19")},
		first,
	)
	if err != nil {
		t.Fatalf("newWindowJob(first) error = %v", err)
	}
	sameJob, err := newWindowJob(
		RunRequest{Mode: ModeDaily, RunDate: parseTestDate("2026-07-19")},
		same,
	)
	if err != nil {
		t.Fatalf("newWindowJob(same) error = %v", err)
	}
	if firstJob.IdempotencyKey != first.Key() ||
		sameJob.IdempotencyKey != same.Key() ||
		firstJob.IdempotencyKey != sameJob.IdempotencyKey {
		t.Fatalf(
			"stable job keys = %q/%q, want equal Task4 key",
			firstJob.IdempotencyKey,
			sameJob.IdempotencyKey,
		)
	}
	for name, window := range map[string]SyncWindow{
		"different ISSN set":  differentISSNs,
		"different date type": differentDateType,
		"different date":      differentDate,
	} {
		job, err := newWindowJob(
			RunRequest{Mode: ModeDaily, RunDate: parseTestDate("2026-07-19")},
			window,
		)
		if err != nil {
			t.Fatalf("newWindowJob(%s) error = %v", name, err)
		}
		if job.IdempotencyKey == firstJob.IdempotencyKey {
			t.Fatalf("%s did not change idempotency key %q", name, job.IdempotencyKey)
		}
	}
}

func mustServiceJournal(t *testing.T, issn string) Journal {
	t.Helper()
	journals, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(
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
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	return journals[0]
}

var _ iter.Seq2[ingestion.Event, error] = func(yield func(ingestion.Event, error) bool) {}
