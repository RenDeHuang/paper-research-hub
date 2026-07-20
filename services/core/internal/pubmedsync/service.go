package pubmedsync

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ingestion"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venueenrich"
)

type Mode string

const (
	ModeBackfill Mode = "backfill"
	ModeDaily    Mode = "daily"
)

type Searcher interface {
	Search(context.Context, pubmed.SearchQuery) (pubmed.SearchResult, error)
}

type Fetcher interface {
	Fetch(context.Context, pubmed.SearchResult) source.ClientSequence
}

type RunIngestionFunc func(
	context.Context,
	ingestion.Job,
	ingestion.EventSequence,
) (ingestion.JobSummary, error)

type RecordEventsFunc func(source.ClientSequence) ingestion.EventSequence

type RunRequest struct {
	Mode         Mode
	RunDate      time.Time
	LookbackDays int
	RegistryPath string
	Registry     io.Reader
}

type Service struct {
	searcher     Searcher
	fetcher      Fetcher
	runIngestion RunIngestionFunc
	recordEvents RecordEventsFunc
}

func NewService(
	searcher Searcher,
	fetcher Fetcher,
	runIngestion RunIngestionFunc,
	recordEvents RecordEventsFunc,
) (*Service, error) {
	if searcher == nil {
		return nil, errors.New("PubMed journal sync searcher is required")
	}
	if fetcher == nil {
		return nil, errors.New("PubMed journal sync fetcher is required")
	}
	if runIngestion == nil {
		return nil, errors.New("PubMed journal sync ingestion runner is required")
	}
	if recordEvents == nil {
		return nil, errors.New("PubMed journal sync event recorder is required")
	}
	return &Service{
		searcher:     searcher,
		fetcher:      fetcher,
		runIngestion: runIngestion,
		recordEvents: recordEvents,
	}, nil
}

type Report struct {
	Mode             Mode            `json:"mode"`
	RegistryPath     string          `json:"registry,omitempty"`
	RunDate          string          `json:"run_date,omitempty"`
	LookbackDays     int             `json:"lookback_days"`
	RegistryJournals int             `json:"registry_journals"`
	EligibleJournals int             `json:"eligible_journals"`
	NoCoverage       int             `json:"no_coverage"`
	JournalsPlanned  int             `json:"journals_planned"`
	JournalsFailed   int             `json:"journals_failed"`
	WindowsPlanned   int             `json:"windows_planned"`
	WindowsAttempted int             `json:"windows_attempted"`
	WindowsSucceeded int             `json:"windows_succeeded"`
	WindowsFailed    int             `json:"windows_failed"`
	RawInserted      int64           `json:"raw_inserted"`
	RawReused        int64           `json:"raw_reused"`
	Projected        int64           `json:"projected"`
	Excluded         int64           `json:"excluded"`
	Deleted          int64           `json:"deleted"`
	Unchanged        int64           `json:"unchanged"`
	IngestionFailed  int64           `json:"failed"`
	Journals         []JournalReport `json:"journals"`
	Windows          []WindowReport  `json:"windows"`
	Failures         []FailureReport `json:"failures"`
}

type JournalReport struct {
	JournalKey       string   `json:"journal_key"`
	JournalName      string   `json:"journal_name"`
	ISSNs            []string `json:"issns"`
	Status           string   `json:"status"`
	WindowsPlanned   int      `json:"windows_planned"`
	WindowsSucceeded int      `json:"windows_succeeded"`
	WindowsFailed    int      `json:"windows_failed"`
	Error            string   `json:"error,omitempty"`
}

type WindowReport struct {
	JournalKey  string          `json:"journal_key"`
	JournalName string          `json:"journal_name"`
	ISSNs       []string        `json:"issns"`
	Mode        Mode            `json:"mode"`
	DateType    pubmed.DateType `json:"date_type"`
	FromDate    string          `json:"from_date"`
	ToDate      string          `json:"to_date"`
	WindowKey   string          `json:"window_key"`
	Status      string          `json:"status"`
	Stage       string          `json:"stage,omitempty"`
	Error       string          `json:"error,omitempty"`
	RawInserted int64           `json:"raw_inserted"`
	RawReused   int64           `json:"raw_reused"`
	Projected   int64           `json:"projected"`
	Excluded    int64           `json:"excluded"`
	Deleted     int64           `json:"deleted"`
	Unchanged   int64           `json:"unchanged"`
	Failed      int64           `json:"failed"`
}

type FailureReport struct {
	JournalKey  string `json:"journal_key"`
	JournalName string `json:"journal_name"`
	WindowKey   string `json:"window_key,omitempty"`
	Stage       string `json:"stage"`
	Error       string `json:"error"`
}

func (report Report) Result() map[string]any {
	return map[string]any{
		"mode":              report.Mode,
		"registry":          report.RegistryPath,
		"run_date":          report.RunDate,
		"lookback_days":     report.LookbackDays,
		"registry_journals": report.RegistryJournals,
		"eligible_journals": report.EligibleJournals,
		"no_coverage":       report.NoCoverage,
		"journals_planned":  report.JournalsPlanned,
		"journals_failed":   report.JournalsFailed,
		"windows_planned":   report.WindowsPlanned,
		"windows_attempted": report.WindowsAttempted,
		"windows_succeeded": report.WindowsSucceeded,
		"windows_failed":    report.WindowsFailed,
		"raw_inserted":      report.RawInserted,
		"raw_reused":        report.RawReused,
		"projected":         report.Projected,
		"excluded":          report.Excluded,
		"deleted":           report.Deleted,
		"unchanged":         report.Unchanged,
		"failed":            report.IngestionFailed,
		"inserted":          report.RawInserted,
		"updated":           report.Projected,
		"journals":          report.Journals,
		"windows":           report.Windows,
		"failures":          report.Failures,
	}
}

func (service *Service) Run(
	ctx context.Context,
	request RunRequest,
) (Report, error) {
	report := Report{
		Mode:         request.Mode,
		RegistryPath: strings.TrimSpace(request.RegistryPath),
		LookbackDays: request.LookbackDays,
	}
	if request.Mode == ModeDaily && !request.RunDate.IsZero() {
		report.RunDate = request.RunDate.UTC().Format(time.DateOnly)
	}
	if err := validateRunRequest(ctx, request); err != nil {
		return report, err
	}

	registryBytes, err := io.ReadAll(request.Registry)
	if err != nil {
		return report, fmt.Errorf("read PubMed journal registry: %w", err)
	}
	journals, err := LoadPubMedSupportedRegistry(bytes.NewReader(registryBytes))
	if err != nil {
		return report, err
	}
	registryRows, noCoverage, err := countRegistryStats(bytes.NewReader(registryBytes))
	if err != nil {
		return report, err
	}
	report.RegistryJournals = registryRows
	report.EligibleJournals = len(journals)
	report.NoCoverage = noCoverage

	var runErr error
	for _, journal := range journals {
		if err := ctx.Err(); err != nil {
			return report, errors.Join(runErr, err)
		}

		journalReport := JournalReport{
			JournalKey:  journal.Key(),
			JournalName: journal.SourceJournalName(),
			ISSNs:       journal.ISSNs(),
			Status:      "planned",
		}
		report.Journals = append(report.Journals, journalReport)
		journalIndex := len(report.Journals) - 1
		report.JournalsPlanned++

		windows, planErr := service.plan(ctx, request, journal)
		if planErr != nil {
			report.Journals[journalIndex].Status = "failed"
			report.Journals[journalIndex].Error = planErr.Error()
			report.JournalsFailed++
			failure := FailureReport{
				JournalKey:  journal.Key(),
				JournalName: journal.SourceJournalName(),
				Stage:       "plan",
				Error:       planErr.Error(),
			}
			report.Failures = append(report.Failures, failure)
			if isContextError(planErr) {
				return report, errors.Join(runErr, planErr)
			}
			runErr = errors.Join(
				runErr,
				fmt.Errorf("plan journal %q: %w", journal.SourceJournalName(), planErr),
			)
			continue
		}

		report.WindowsPlanned += len(windows)
		report.Journals[journalIndex].WindowsPlanned = len(windows)
		for _, window := range windows {
			if err := ctx.Err(); err != nil {
				return report, errors.Join(runErr, err)
			}

			windowReport := WindowReport{
				JournalKey:  journal.Key(),
				JournalName: journal.SourceJournalName(),
				ISSNs:       journal.ISSNs(),
				Mode:        request.Mode,
				DateType:    window.DateType(),
				FromDate:    window.From().Format(time.DateOnly),
				ToDate:      window.To().Format(time.DateOnly),
				WindowKey:   window.Key(),
				Status:      "planned",
			}
			report.Windows = append(report.Windows, windowReport)
			windowIndex := len(report.Windows) - 1
			report.WindowsAttempted++

			summary, runWindowErr := service.runWindow(
				ctx,
				request,
				window,
			)
			if summaryErr := summary.Validate(); summaryErr == nil {
				mergeSummary(&report, &report.Windows[windowIndex], summary)
			} else if runWindowErr == nil {
				runWindowErr = fmt.Errorf(
					"validate PubMed ingestion summary: %w",
					summaryErr,
				)
			}
			if runWindowErr != nil {
				report.Windows[windowIndex].Status = "failed"
				report.Windows[windowIndex].Stage = "search_or_ingestion"
				report.Windows[windowIndex].Error = runWindowErr.Error()
				report.WindowsFailed++
				report.Journals[journalIndex].WindowsFailed++
				report.Journals[journalIndex].Status = "failed"
				report.Failures = append(report.Failures, FailureReport{
					JournalKey:  journal.Key(),
					JournalName: journal.SourceJournalName(),
					WindowKey:   window.Key(),
					Stage:       "search_or_ingestion",
					Error:       runWindowErr.Error(),
				})
				if isContextError(runWindowErr) {
					return report, errors.Join(runErr, runWindowErr)
				}
				runErr = errors.Join(
					runErr,
					fmt.Errorf(
						"run journal %q window %q: %w",
						journal.SourceJournalName(),
						window.Key(),
						runWindowErr,
					),
				)
				continue
			}

			report.Windows[windowIndex].Status = "succeeded"
			report.WindowsSucceeded++
			report.Journals[journalIndex].WindowsSucceeded++
		}

		if report.Journals[journalIndex].Status != "failed" {
			report.Journals[journalIndex].Status = "succeeded"
		} else {
			report.JournalsFailed++
		}
	}
	return report, runErr
}

func validateRunRequest(ctx context.Context, request RunRequest) error {
	if ctx == nil {
		return errors.New("PubMed journal sync context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.Registry == nil {
		return errors.New("PubMed journal sync registry reader is required")
	}
	if request.LookbackDays != 3 {
		return errors.New("PubMed journal sync lookback-days must be exactly 3")
	}
	switch request.Mode {
	case ModeBackfill:
		if !request.RunDate.IsZero() {
			return errors.New("PubMed journal backfill does not accept run-date")
		}
	case ModeDaily:
		if request.RunDate.IsZero() {
			return errors.New("PubMed journal daily sync requires run-date")
		}
	default:
		return fmt.Errorf(
			"invalid PubMed journal sync mode %q; expected backfill or daily",
			request.Mode,
		)
	}
	return nil
}

func (service *Service) plan(
	ctx context.Context,
	request RunRequest,
	journal Journal,
) ([]SyncWindow, error) {
	switch request.Mode {
	case ModeBackfill:
		return PlanBackfill(ctx, searchCounter{searcher: service.searcher}, journal)
	case ModeDaily:
		return PlanDaily(journal, request.RunDate)
	default:
		return nil, fmt.Errorf("unsupported PubMed journal sync mode %q", request.Mode)
	}
}

func (service *Service) runWindow(
	ctx context.Context,
	request RunRequest,
	window SyncWindow,
) (ingestion.JobSummary, error) {
	job, err := newWindowJob(request, window)
	if err != nil {
		return ingestion.JobSummary{}, err
	}

	events := func(yield func(ingestion.Event, error) bool) {
		history, err := service.searcher.Search(ctx, pubmed.SearchQuery{
			JournalISSNs: window.Journal().ISSNs(),
			DateType:     window.DateType(),
			DateWindow:   window.DateWindow(),
			MaxResults:   pubmed.MaxSearchResults,
		})
		if err != nil {
			yield(nil, fmt.Errorf("search PubMed window: %w", err))
			return
		}
		if history.Count >= pubmed.MaxSearchResults {
			yield(
				nil,
				fmt.Errorf(
					"PubMed search count %d reaches fetch limit %d for window %q; refusing truncated fetch",
					history.Count,
					pubmed.MaxSearchResults,
					window.Key(),
				),
			)
			return
		}

		records := service.fetcher.Fetch(ctx, history)
		for event, err := range service.recordEvents(records) {
			if !yield(event, err) {
				return
			}
		}
	}
	summary, err := service.runIngestion(
		ctx,
		job,
		events,
	)
	if err != nil {
		return summary, fmt.Errorf("ingest PubMed window: %w", err)
	}
	return summary, nil
}

func mergeSummary(
	report *Report,
	window *WindowReport,
	summary ingestion.JobSummary,
) {
	window.RawInserted = summary.RawInserted
	window.RawReused = summary.RawReused
	window.Projected = summary.Projected
	window.Excluded = summary.Excluded
	window.Deleted = summary.Deleted
	window.Unchanged = summary.Unchanged
	window.Failed = summary.Failed
	report.RawInserted += summary.RawInserted
	report.RawReused += summary.RawReused
	report.Projected += summary.Projected
	report.Excluded += summary.Excluded
	report.Deleted += summary.Deleted
	report.Unchanged += summary.Unchanged
	report.IngestionFailed += summary.Failed
}

func newWindowJob(request RunRequest, window SyncWindow) (ingestion.Job, error) {
	journal := window.Journal()
	issns := journal.ISSNs()
	payload := map[string]any{
		"mode":             string(request.Mode),
		"journal_key":      journal.Key(),
		"journal_identity": journal.Key(),
		"journal_name":     journal.SourceJournalName(),
		"issns":            issns,
		"journal_issns":    issns,
		"date_type":        string(window.DateType()),
		"from_date":        window.From().Format(time.DateOnly),
		"to_date":          window.To().Format(time.DateOnly),
		"window_key":       window.Key(),
	}
	if request.Mode == ModeDaily {
		payload["run_date"] = request.RunDate.UTC().Format(time.DateOnly)
	}
	return ingestion.NewJob(
		"sync/pubmed-journals/"+window.Key(),
		source.PubMed,
		window.Key(),
		payload,
	)
}

type searchCounter struct {
	searcher Searcher
}

func (counter searchCounter) Count(
	ctx context.Context,
	journal Journal,
	dateType pubmed.DateType,
	dateWindow pubmed.DateWindow,
) (int64, error) {
	result, err := counter.searcher.Search(ctx, pubmed.SearchQuery{
		JournalISSNs: journal.ISSNs(),
		DateType:     dateType,
		DateWindow:   dateWindow,
		MaxResults:   pubmed.MaxSearchResults,
	})
	if err != nil {
		return 0, err
	}
	return int64(result.Count), nil
}

func countRegistryStats(reader io.Reader) (int, int, error) {
	csvReader := csv.NewReader(reader)
	csvReader.FieldsPerRecord = -1
	header, err := csvReader.Read()
	if err != nil {
		return 0, 0, fmt.Errorf(
			"read registry CSV header for coverage report: %w",
			err,
		)
	}
	if len(header) != len(registryHeader) {
		return 0, 0, errors.New(
			"registry CSV coverage report header width mismatch",
		)
	}

	rows := 0
	noCoverage := 0
	for rowNumber := 2; ; rowNumber++ {
		record, err := csvReader.Read()
		if errors.Is(err, io.EOF) {
			return rows, noCoverage, nil
		}
		if err != nil {
			return 0, 0, fmt.Errorf(
				"read registry CSV row %d for coverage report: %w",
				rowNumber,
				err,
			)
		}
		if len(record) != len(registryHeader) {
			return 0, 0, fmt.Errorf(
				"registry CSV row %d coverage report width mismatch",
				rowNumber,
			)
		}
		rows++
		if record[11] == string(venueenrich.MatchStatusResolved) &&
			record[12] == string(venueenrich.SupportStatusNo) {
			noCoverage++
		}
	}
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}
