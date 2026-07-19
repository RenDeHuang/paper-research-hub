package pubmedsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
)

type Counter interface {
	Count(context.Context, Journal, pubmed.DateType, pubmed.DateWindow) (int64, error)
}

type SyncWindow struct {
	journal    Journal
	dateType   pubmed.DateType
	dateWindow pubmed.DateWindow
	key        string
}

func NewSyncWindow(
	journal Journal,
	dateType pubmed.DateType,
	dateWindow pubmed.DateWindow,
) (SyncWindow, error) {
	if journal.key == "" || len(journal.issns) == 0 {
		return SyncWindow{}, errors.New("sync window requires a resolved journal")
	}
	if !validDateType(dateType) {
		return SyncWindow{}, fmt.Errorf("invalid PubMed date type %q", dateType)
	}
	if dateWindow.From.IsZero() || dateWindow.To.IsZero() {
		return SyncWindow{}, errors.New("sync window requires both from and to dates")
	}

	from := utcCivilDate(dateWindow.From)
	to := utcCivilDate(dateWindow.To)
	if from.After(to) {
		return SyncWindow{}, errors.New("sync window from date must not follow to date")
	}

	journal = journal.clone()
	key, err := syncWindowKey(journal, dateType, from, to)
	if err != nil {
		return SyncWindow{}, fmt.Errorf("derive sync window key: %w", err)
	}
	return SyncWindow{
		journal:  journal,
		dateType: dateType,
		dateWindow: pubmed.DateWindow{
			From: from,
			To:   to,
		},
		key: key,
	}, nil
}

func (window SyncWindow) Journal() Journal {
	return window.journal.clone()
}

func (window SyncWindow) DateType() pubmed.DateType {
	return window.dateType
}

func (window SyncWindow) DateWindow() pubmed.DateWindow {
	return window.dateWindow
}

func (window SyncWindow) From() time.Time {
	return window.dateWindow.From
}

func (window SyncWindow) To() time.Time {
	return window.dateWindow.To
}

func (window SyncWindow) Key() string {
	return window.key
}

func PlanBackfill(
	ctx context.Context,
	counter Counter,
	journal Journal,
) ([]SyncWindow, error) {
	if ctx == nil {
		return nil, errors.New("backfill context is required")
	}
	if counter == nil {
		return nil, errors.New("backfill counter is required")
	}
	if journal.key == "" || len(journal.issns) == 0 {
		return nil, errors.New("backfill requires a resolved journal")
	}

	backfillFrom := dateAtUTC(2023, time.July, 19)
	backfillTo := dateAtUTC(2026, time.July, 19)
	fullWindow := pubmed.DateWindow{From: backfillFrom, To: backfillTo}
	count := func(window pubmed.DateWindow) (int64, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		value, err := counter.Count(ctx, journal, pubmed.DateTypePublication, window)
		if err != nil {
			return 0, fmt.Errorf("count %s..%s: %w",
				formatDate(window.From), formatDate(window.To), err)
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if value < 0 {
			return 0, fmt.Errorf(
				"count %s..%s must not be negative",
				formatDate(window.From),
				formatDate(window.To),
			)
		}
		return value, nil
	}

	fullCount, err := count(fullWindow)
	if err != nil {
		return nil, err
	}
	if fullCount < pubmed.MaxSearchResults {
		return makeSyncWindows(journal, pubmed.DateTypePublication, []pubmed.DateWindow{fullWindow})
	}

	windows := make([]pubmed.DateWindow, 0)
	for year := 2023; year <= 2026; year++ {
		yearWindow := boundedYearWindow(year, backfillFrom, backfillTo)
		yearCount, err := count(yearWindow)
		if err != nil {
			return nil, fmt.Errorf("count year %d: %w", year, err)
		}
		if yearCount < pubmed.MaxSearchResults {
			windows = append(windows, yearWindow)
			continue
		}

		monthWindows := boundedMonthWindows(year, backfillFrom, backfillTo)
		for _, monthWindow := range monthWindows {
			monthCount, err := count(monthWindow)
			if err != nil {
				return nil, fmt.Errorf(
					"count month %s..%s: %w",
					formatDate(monthWindow.From),
					formatDate(monthWindow.To),
					err,
				)
			}
			if monthCount >= pubmed.MaxSearchResults {
				return nil, fmt.Errorf(
					"month %s..%s count %d reaches PubMed limit %d; daily split is not supported",
					formatDate(monthWindow.From),
					formatDate(monthWindow.To),
					monthCount,
					pubmed.MaxSearchResults,
				)
			}
			windows = append(windows, monthWindow)
		}
	}

	if err := validateBackfillCoverage(windows, backfillFrom, backfillTo); err != nil {
		return nil, err
	}
	return makeSyncWindows(journal, pubmed.DateTypePublication, windows)
}

func PlanDaily(journal Journal, runDate time.Time) ([]SyncWindow, error) {
	if runDate.IsZero() {
		return nil, errors.New("daily plan runDate is required")
	}
	runTo := utcCivilDate(runDate)
	runFrom := runTo.AddDate(0, 0, -2)
	dateWindow := pubmed.DateWindow{From: runFrom, To: runTo}
	result := make([]SyncWindow, 0, 2)
	for _, dateType := range []pubmed.DateType{
		pubmed.DateTypeEntrez,
		pubmed.DateTypeModification,
	} {
		window, err := NewSyncWindow(journal, dateType, dateWindow)
		if err != nil {
			return nil, err
		}
		result = append(result, window)
	}
	return result, nil
}

func makeSyncWindows(
	journal Journal,
	dateType pubmed.DateType,
	dateWindows []pubmed.DateWindow,
) ([]SyncWindow, error) {
	result := make([]SyncWindow, 0, len(dateWindows))
	for _, dateWindow := range dateWindows {
		window, err := NewSyncWindow(journal, dateType, dateWindow)
		if err != nil {
			return nil, err
		}
		result = append(result, window)
	}
	return result, nil
}

func boundedYearWindow(year int, from, to time.Time) pubmed.DateWindow {
	yearFrom := dateAtUTC(year, time.January, 1)
	yearTo := dateAtUTC(year, time.December, 31)
	return pubmed.DateWindow{
		From: maxDate(yearFrom, from),
		To:   minDate(yearTo, to),
	}
}

func boundedMonthWindows(year int, from, to time.Time) []pubmed.DateWindow {
	firstMonth := time.January
	lastMonth := time.December
	if year == from.Year() {
		firstMonth = from.Month()
	}
	if year == to.Year() {
		lastMonth = to.Month()
	}

	result := make([]pubmed.DateWindow, 0, int(lastMonth-firstMonth)+1)
	for month := firstMonth; month <= lastMonth; month++ {
		monthFrom := dateAtUTC(year, month, 1)
		monthTo := dateAtUTC(year, month+1, 1).AddDate(0, 0, -1)
		result = append(result, pubmed.DateWindow{
			From: maxDate(monthFrom, from),
			To:   minDate(monthTo, to),
		})
	}
	return result
}

func validateBackfillCoverage(
	windows []pubmed.DateWindow,
	from time.Time,
	to time.Time,
) error {
	if len(windows) == 0 {
		return errors.New("backfill produced no windows")
	}
	for index, window := range windows {
		if window.From.After(window.To) {
			return fmt.Errorf("backfill window %d is reversed", index)
		}
		if index == 0 {
			if !sameCivilDate(window.From, from) {
				return errors.New("backfill windows do not start at fixed from date")
			}
			continue
		}
		wantFrom := windows[index-1].To.AddDate(0, 0, 1)
		if !sameCivilDate(window.From, wantFrom) {
			return fmt.Errorf("backfill windows have a gap or overlap before window %d", index)
		}
	}
	if !sameCivilDate(windows[len(windows)-1].To, to) {
		return errors.New("backfill windows do not end at fixed to date")
	}
	return nil
}

type syncWindowKeyPayload struct {
	Version  int             `json:"version"`
	ISSNs    []string        `json:"issns"`
	DateType pubmed.DateType `json:"date_type"`
	From     string          `json:"from"`
	To       string          `json:"to"`
}

func syncWindowKey(
	journal Journal,
	dateType pubmed.DateType,
	from time.Time,
	to time.Time,
) (string, error) {
	payload, err := json.Marshal(syncWindowKeyPayload{
		Version:  1,
		ISSNs:    journal.ISSNs(),
		DateType: dateType,
		From:     formatDate(from),
		To:       formatDate(to),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sync-window-sha256:" + hex.EncodeToString(sum[:]), nil
}

func validDateType(dateType pubmed.DateType) bool {
	switch dateType {
	case pubmed.DateTypePublication, pubmed.DateTypeEntrez, pubmed.DateTypeModification:
		return true
	default:
		return false
	}
}

func dateAtUTC(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func utcCivilDate(value time.Time) time.Time {
	year, month, day := value.UTC().Date()
	return dateAtUTC(year, month, day)
}

func maxDate(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}

func minDate(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func sameCivilDate(left, right time.Time) bool {
	return utcCivilDate(left).Equal(utcCivilDate(right))
}

func formatDate(value time.Time) string {
	return utcCivilDate(value).Format("2006-01-02")
}
