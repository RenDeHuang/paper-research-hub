package pubmedsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
)

type recordedCount struct {
	dateType pubmed.DateType
	window   pubmed.DateWindow
}

type recordingCounter struct {
	count func(pubmed.DateType, pubmed.DateWindow) (int64, error)
	calls []recordedCount
}

func (counter *recordingCounter) Count(
	_ context.Context,
	_ Journal,
	dateType pubmed.DateType,
	window pubmed.DateWindow,
) (int64, error) {
	counter.calls = append(counter.calls, recordedCount{
		dateType: dateType,
		window:   window,
	})
	return counter.count(dateType, window)
}

func TestPlanBackfillKeepsCountBelowThresholdAsOnePublicationWindow(t *testing.T) {
	t.Parallel()

	counter := &recordingCounter{
		count: func(_ pubmed.DateType, window pubmed.DateWindow) (int64, error) {
			if !sameDates(window, dateWindow("2023-07-19", "2026-07-19")) {
				t.Fatalf("initial count window = %#v, want complete fixed backfill", window)
			}
			return 9999, nil
		},
	}

	windows, err := PlanBackfill(context.Background(), counter, testJournal(t))
	if err != nil {
		t.Fatalf("PlanBackfill() error = %v", err)
	}
	assertWindows(t, windows, []expectedWindow{
		{pubmed.DateTypePublication, "2023-07-19", "2026-07-19"},
	})
	if len(counter.calls) != 1 {
		t.Fatalf("Count() calls = %d, want only complete-range count", len(counter.calls))
	}
}

func TestPlanBackfillSplitsYearsAndMonthsWithExactCivilBoundaries(t *testing.T) {
	t.Parallel()

	counter := &recordingCounter{
		count: func(dateType pubmed.DateType, window pubmed.DateWindow) (int64, error) {
			if dateType != pubmed.DateTypePublication {
				t.Fatalf("Count() date type = %q, want publication", dateType)
			}
			switch {
			case sameDates(window, dateWindow("2023-07-19", "2026-07-19")):
				return 10000, nil
			case sameDates(window, dateWindow("2023-07-19", "2023-12-31")):
				return 9999, nil
			case sameDates(window, dateWindow("2024-01-01", "2024-12-31")):
				return 10000, nil
			case sameDates(window, dateWindow("2025-01-01", "2025-12-31")):
				return 0, nil
			case sameDates(window, dateWindow("2026-01-01", "2026-07-19")):
				return 0, nil
			default:
				return 9999, nil
			}
		},
	}

	windows, err := PlanBackfill(context.Background(), counter, testJournal(t))
	if err != nil {
		t.Fatalf("PlanBackfill() error = %v", err)
	}

	if len(windows) != 15 {
		t.Fatalf("PlanBackfill() returned %d windows, want 15", len(windows))
	}
	assertContiguous(t, windows)
	if windows[0].From().Format("2006-01-02") != "2023-07-19" ||
		windows[0].To().Format("2006-01-02") != "2023-12-31" {
		t.Fatalf("first window = %s..%s, want partial 2023 year",
			windows[0].From().Format("2006-01-02"),
			windows[0].To().Format("2006-01-02"),
		)
	}
	if windows[1].From().Format("2006-01-02") != "2024-01-01" ||
		windows[1].To().Format("2006-01-02") != "2024-01-31" {
		t.Fatalf("first monthly window = %s..%s, want January 2024",
			windows[1].From().Format("2006-01-02"),
			windows[1].To().Format("2006-01-02"),
		)
	}
	for _, window := range windows {
		if window.From().Format("2006-01-02") == "2024-02-01" &&
			window.To().Format("2006-01-02") != "2024-02-29" {
			t.Fatalf("February 2024 window ends at %s, want leap day",
				window.To().Format("2006-01-02"),
			)
		}
	}
	full2025 := windows[len(windows)-2]
	if full2025.From().Format("2006-01-02") != "2025-01-01" ||
		full2025.To().Format("2006-01-02") != "2025-12-31" {
		t.Fatalf("2025 window = %s..%s, want 2025 full year",
			full2025.From().Format("2006-01-02"),
			full2025.To().Format("2006-01-02"),
		)
	}
	last := windows[len(windows)-1]
	if last.From().Format("2006-01-02") != "2026-01-01" ||
		last.To().Format("2006-01-02") != "2026-07-19" {
		t.Fatalf("last window = %s..%s, want partial 2026 year",
			last.From().Format("2006-01-02"),
			last.To().Format("2006-01-02"),
		)
	}
}

func TestPlanBackfillAcceptsYearAndMonthCountThresholdBoundaries(t *testing.T) {
	t.Parallel()

	counter := &recordingCounter{
		count: func(_ pubmed.DateType, window pubmed.DateWindow) (int64, error) {
			switch {
			case sameDates(window, dateWindow("2023-07-19", "2026-07-19")):
				return 10000, nil
			case sameDates(window, dateWindow("2023-07-19", "2023-12-31")):
				return 10000, nil
			case sameDates(window, dateWindow("2024-01-01", "2024-12-31")):
				return 9999, nil
			default:
				return 9999, nil
			}
		},
	}

	windows, err := PlanBackfill(context.Background(), counter, testJournal(t))
	if err != nil {
		t.Fatalf("PlanBackfill() error = %v", err)
	}
	if len(windows) != 9 {
		t.Fatalf("PlanBackfill() returned %d windows, want 9", len(windows))
	}
	assertContiguous(t, windows)
	if windows[0].From().Format("2006-01-02") != "2023-07-19" ||
		windows[0].To().Format("2006-01-02") != "2023-07-31" {
		t.Fatalf("first monthly window = %s..%s, want July 2023 partial month",
			windows[0].From().Format("2006-01-02"),
			windows[0].To().Format("2006-01-02"),
		)
	}
	if windows[5].From().Format("2006-01-02") != "2023-12-01" ||
		windows[5].To().Format("2006-01-02") != "2023-12-31" {
		t.Fatalf("last monthly 2023 window = %s..%s, want December 2023",
			windows[5].From().Format("2006-01-02"),
			windows[5].To().Format("2006-01-02"),
		)
	}
}

func TestPlanBackfillRejectsMonthAtThresholdWithoutDailyFallback(t *testing.T) {
	t.Parallel()

	var calls int
	counter := &recordingCounter{
		count: func(_ pubmed.DateType, window pubmed.DateWindow) (int64, error) {
			calls++
			switch calls {
			case 1:
				return 10000, nil
			case 2:
				return 10000, nil
			case 3:
				if !sameDates(window, dateWindow("2023-07-19", "2023-07-31")) {
					t.Fatalf("first monthly count window = %#v", window)
				}
				return 10000, nil
			default:
				t.Fatalf("Count() continued after month threshold failure")
				return 0, nil
			}
		},
	}

	if _, err := PlanBackfill(context.Background(), counter, testJournal(t)); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "month") {
		t.Fatalf("PlanBackfill() error = %v, want explicit monthly threshold failure", err)
	}
	if calls != 3 {
		t.Fatalf("Count() calls = %d, want full/year/first-month only", calls)
	}
}

func TestPlanBackfillPropagatesNegativeCountCounterErrorAndCancellation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func() (context.Context, *recordingCounter)
	}{
		{
			name: "negative count",
			setup: func() (context.Context, *recordingCounter) {
				return context.Background(), &recordingCounter{
					count: func(_ pubmed.DateType, _ pubmed.DateWindow) (int64, error) {
						return -1, nil
					},
				}
			},
		},
		{
			name: "counter error",
			setup: func() (context.Context, *recordingCounter) {
				sentinel := errors.New("counter failed")
				return context.Background(), &recordingCounter{
					count: func(_ pubmed.DateType, _ pubmed.DateWindow) (int64, error) {
						return 0, sentinel
					},
				}
			},
		},
		{
			name: "context canceled",
			setup: func() (context.Context, *recordingCounter) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, &recordingCounter{
					count: func(_ pubmed.DateType, _ pubmed.DateWindow) (int64, error) {
						t.Fatal("Count() called with canceled context")
						return 0, nil
					},
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, counter := tt.setup()
			if _, err := PlanBackfill(ctx, counter, testJournal(t)); err == nil {
				t.Fatal("PlanBackfill() succeeded for invalid counter state")
			}
		})
	}
}

func TestPlanDailyEmitsEDATThenMDATForTwoUTCPreviousDays(t *testing.T) {
	t.Parallel()

	runDate := time.Date(2026, 7, 20, 1, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	windows, err := PlanDaily(testJournal(t), runDate)
	if err != nil {
		t.Fatalf("PlanDaily() error = %v", err)
	}
	assertWindows(t, windows, []expectedWindow{
		{pubmed.DateTypeEntrez, "2026-07-17", "2026-07-19"},
		{pubmed.DateTypeModification, "2026-07-17", "2026-07-19"},
	})
}

func TestPlanDailyRejectsZeroRunDate(t *testing.T) {
	t.Parallel()

	if _, err := PlanDaily(testJournal(t), time.Time{}); err == nil {
		t.Fatal("PlanDaily() accepted zero runDate")
	}
}

func TestSyncWindowKeyIsStableCompleteAndJournalSliceIsImmutable(t *testing.T) {
	t.Parallel()

	journal := testJournal(t)
	from := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	first, err := NewSyncWindow(journal, pubmed.DateTypeEntrez, pubmed.DateWindow{From: from, To: to})
	if err != nil {
		t.Fatalf("NewSyncWindow() error = %v", err)
	}
	second, err := NewSyncWindow(journal, pubmed.DateTypeEntrez, pubmed.DateWindow{From: from, To: to})
	if err != nil {
		t.Fatalf("NewSyncWindow() repeat error = %v", err)
	}
	if first.Key() != second.Key() {
		t.Fatalf("equal SyncWindow inputs produced unstable keys %q and %q", first.Key(), second.Key())
	}

	mutated := first.Journal().ISSNs()
	mutated[0] = "9876-5434"
	if !reflect.DeepEqual(first.Journal().ISSNs(), journal.ISSNs()) {
		t.Fatalf("SyncWindow.Journal() exposed mutable ISSN storage")
	}

	changedDateType, err := NewSyncWindow(
		journal,
		pubmed.DateTypeModification,
		pubmed.DateWindow{From: from, To: to},
	)
	if err != nil {
		t.Fatalf("NewSyncWindow() date type error = %v", err)
	}
	changedFrom, err := NewSyncWindow(
		journal,
		pubmed.DateTypeEntrez,
		pubmed.DateWindow{From: from.AddDate(0, 0, -1), To: to},
	)
	if err != nil {
		t.Fatalf("NewSyncWindow() from date error = %v", err)
	}
	changedTo, err := NewSyncWindow(
		journal,
		pubmed.DateTypeEntrez,
		pubmed.DateWindow{From: from, To: to.AddDate(0, 0, 1)},
	)
	if err != nil {
		t.Fatalf("NewSyncWindow() to date error = %v", err)
	}
	differentJournal, err := LoadPubMedSupportedRegistry(bytes.NewReader(encodeRegistryCSV(
		testRegistryHeader,
		validRegistryRecord(
			"Different ISSN Set",
			9,
			"9876-5434",
			"",
			"",
			`["9876-5434"]`,
			"resolved",
			"yes",
			"1",
		),
	)))
	if err != nil {
		t.Fatalf("LoadPubMedSupportedRegistry() different journal error = %v", err)
	}
	changedJournal, err := NewSyncWindow(
		differentJournal[0],
		pubmed.DateTypeEntrez,
		pubmed.DateWindow{From: from, To: to},
	)
	if err != nil {
		t.Fatalf("NewSyncWindow() journal error = %v", err)
	}

	for name, window := range map[string]SyncWindow{
		"date type": changedDateType,
		"from":      changedFrom,
		"to":        changedTo,
		"journal":   changedJournal,
	} {
		if window.Key() == first.Key() {
			t.Fatalf("%s change did not change SyncWindow key %q", name, window.Key())
		}
	}
}

type expectedWindow struct {
	dateType pubmed.DateType
	from     string
	to       string
}

func assertWindows(t *testing.T, got []SyncWindow, want []expectedWindow) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("windows = %d, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].DateType() != want[index].dateType ||
			got[index].From().Format("2006-01-02") != want[index].from ||
			got[index].To().Format("2006-01-02") != want[index].to {
			t.Fatalf(
				"window %d = %q %s..%s, want %q %s..%s",
				index,
				got[index].DateType(),
				got[index].From().Format("2006-01-02"),
				got[index].To().Format("2006-01-02"),
				want[index].dateType,
				want[index].from,
				want[index].to,
			)
		}
	}
}

func assertContiguous(t *testing.T, windows []SyncWindow) {
	t.Helper()

	for index, window := range windows {
		if window.From().After(window.To()) {
			t.Fatalf("window %d is reversed: %#v", index, window)
		}
		if index == 0 {
			continue
		}
		previous := windows[index-1]
		wantFrom := previous.To().AddDate(0, 0, 1)
		if !sameDay(window.From(), wantFrom) {
			t.Fatalf(
				"window %d starts %s after previous %s, want %s",
				index,
				window.From().Format("2006-01-02"),
				previous.To().Format("2006-01-02"),
				wantFrom.Format("2006-01-02"),
			)
		}
	}
}

func testJournal(t *testing.T) Journal {
	t.Helper()

	journals, err := LoadPubMedSupportedRegistry(bytes.NewReader(encodeRegistryCSV(
		testRegistryHeader,
		validRegistryRecord(
			"Planner Journal",
			1,
			"1234-5679",
			"",
			"2049-3630",
			`["1234-5679","2049-3630"]`,
			"resolved",
			"yes",
			"1",
		),
	)))
	if err != nil {
		t.Fatalf("test journal load error = %v", err)
	}
	return journals[0]
}

func dateWindow(from, to string) pubmed.DateWindow {
	return pubmed.DateWindow{
		From: parseTestDate(from),
		To:   parseTestDate(to),
	}
}

func parseTestDate(value string) time.Time {
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		panic(fmt.Sprintf("invalid test date %q: %v", value, err))
	}
	return date
}

func sameDates(left, right pubmed.DateWindow) bool {
	return sameDay(left.From, right.From) && sameDay(left.To, right.To)
}

func sameDay(left, right time.Time) bool {
	return left.UTC().Format("2006-01-02") == right.UTC().Format("2006-01-02")
}
