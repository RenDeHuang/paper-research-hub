package venueenrich

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
)

const (
	pubMedTestHashOne = "1111111111111111111111111111111111111111111111111111111111111111"
	pubMedTestHashTwo = "2222222222222222222222222222222222222222222222222222222222222222"
)

func TestPubMedProbeCoverageMapsYesNoErrorAndPreservesUnprobedRowsAndOrder(
	t *testing.T,
) {
	t.Parallel()

	rows := []RegistryRow{
		pubMedProbeResolvedRow(
			1,
			[]string{"2049-3630", "0028-0836", "2049-3630"},
		),
		pubMedProbeUnresolvedRow(2, MatchStatusAmbiguous),
		pubMedProbeResolvedRow(3, []string{"3141-592X"}),
		pubMedProbeUnresolvedRow(4, MatchStatusUnresolved),
		pubMedProbeResolvedRow(5, []string{"1234-5679"}),
	}
	counter := &scriptedPubMedCoverageCounter{
		steps: []pubMedCoverageStep{
			{
				result: pubmed.CoverageResult{
					Count:          17,
					ResponseSHA256: pubMedTestHashOne,
				},
			},
			{
				result: pubmed.CoverageResult{
					Count:          0,
					ResponseSHA256: pubMedTestHashTwo,
				},
			},
			{err: errors.New("bounded upstream failure")},
		},
	}
	checkedAt := time.Date(
		2026,
		time.July,
		19,
		12,
		34,
		56,
		987654321,
		time.FixedZone("test", 8*60*60),
	)

	gotRows, receipts, err := ProbePubMedCoverage(
		context.Background(),
		rows,
		counter,
		func() time.Time { return checkedAt },
	)
	if err != nil {
		t.Fatalf("ProbePubMedCoverage() error = %v", err)
	}
	if len(gotRows) != len(rows) || len(receipts) != len(rows) {
		t.Fatalf(
			"ProbePubMedCoverage() lengths = rows %d, receipts %d, want %d",
			len(gotRows),
			len(receipts),
			len(rows),
		)
	}
	if len(counter.calls) != 3 {
		t.Fatalf("CountCoverage() calls = %d, want only three resolved rows", len(counter.calls))
	}
	wantQueries := [][]string{
		{"0028-0836", "2049-3630"},
		{"3141-592X"},
		{"1234-5679"},
	}
	for index, want := range wantQueries {
		if !slices.Equal(counter.calls[index].JournalISSNs, want) {
			t.Fatalf(
				"call %d JournalISSNs = %v, want one canonical exact OR input %v",
				index+1,
				counter.calls[index].JournalISSNs,
				want,
			)
		}
	}

	wantStatuses := []SupportStatus{
		SupportStatusYes,
		SupportStatusUnknown,
		SupportStatusNo,
		SupportStatusUnknown,
		SupportStatusUnknown,
	}
	wantCounts := []int64{17, 0, 0, 0, 0}
	for index := range gotRows {
		if gotRows[index].SourceOrder != rows[index].SourceOrder {
			t.Fatalf(
				"row %d SourceOrder = %d, want unchanged order %d",
				index,
				gotRows[index].SourceOrder,
				rows[index].SourceOrder,
			)
		}
		if gotRows[index].PubMedSupported != wantStatuses[index] ||
			gotRows[index].PubMedRecordCount != wantCounts[index] {
			t.Fatalf(
				"row %d PubMed evidence = (%q, %d), want (%q, %d)",
				index,
				gotRows[index].PubMedSupported,
				gotRows[index].PubMedRecordCount,
				wantStatuses[index],
				wantCounts[index],
			)
		}
		if err := gotRows[index].Validate(); err != nil {
			t.Fatalf("row %d Validate() error = %v", index, err)
		}
	}
	if !slices.Equal(
		gotRows[0].AllISSNs,
		[]string{"0028-0836", "2049-3630"},
	) {
		t.Fatalf("first AllISSNs = %v, want stable unique order", gotRows[0].AllISSNs)
	}

	wantCheckedAt := checkedAt.UTC().Format(time.RFC3339Nano)
	for index, receipt := range receipts {
		if receipt.Domain != rows[index].Domain ||
			receipt.SourceOrder != rows[index].SourceOrder ||
			receipt.Status != wantStatuses[index] ||
			receipt.RecordCount != wantCounts[index] {
			t.Fatalf("receipt %d identity/evidence = %#v", index, receipt)
		}
		wantAttempted := index == 0 || index == 2 || index == 4
		if receipt.Attempted != wantAttempted {
			t.Fatalf(
				"receipt %d Attempted = %t, want %t",
				index,
				receipt.Attempted,
				wantAttempted,
			)
		}
		if wantAttempted && receipt.CheckedAt != wantCheckedAt {
			t.Fatalf(
				"receipt %d CheckedAt = %q, want %q",
				index,
				receipt.CheckedAt,
				wantCheckedAt,
			)
		}
		if !wantAttempted && receipt.CheckedAt != "" {
			t.Fatalf("unattempted receipt %d CheckedAt = %q, want empty", index, receipt.CheckedAt)
		}
	}
	if receipts[0].ResponseSHA256 != pubMedTestHashOne ||
		receipts[2].ResponseSHA256 != pubMedTestHashTwo {
		t.Fatalf("successful response hashes = %#v", receipts)
	}
	if receipts[4].Error != "bounded upstream failure" ||
		receipts[4].ResponseSHA256 != "" {
		t.Fatalf("failed receipt = %#v, want explicit error without response hash", receipts[4])
	}
}

func TestPubMedProbeCoverageTreatsProtocolFailureAsUnknownAndContinues(t *testing.T) {
	t.Parallel()

	rows := []RegistryRow{
		pubMedProbeResolvedRow(1, []string{"0028-0836"}),
		pubMedProbeResolvedRow(2, []string{"2049-3630"}),
	}
	counter := &scriptedPubMedCoverageCounter{
		steps: []pubMedCoverageStep{
			{
				result: pubmed.CoverageResult{
					Count:          1,
					ResponseSHA256: "not-a-sha256",
				},
			},
			{
				result: pubmed.CoverageResult{
					Count:          2,
					ResponseSHA256: pubMedTestHashTwo,
				},
			},
		},
	}

	gotRows, receipts, err := ProbePubMedCoverage(
		context.Background(),
		rows,
		counter,
		time.Now,
	)
	if err != nil {
		t.Fatalf("ProbePubMedCoverage() error = %v", err)
	}
	if len(counter.calls) != 2 {
		t.Fatalf("CountCoverage() calls = %d, want continue after protocol failure", len(counter.calls))
	}
	if gotRows[0].PubMedSupported != SupportStatusUnknown ||
		gotRows[0].PubMedRecordCount != 0 ||
		receipts[0].Status != SupportStatusUnknown ||
		!strings.Contains(receipts[0].Error, "response_sha256") {
		t.Fatalf("protocol-failed first row/receipt = %#v / %#v", gotRows[0], receipts[0])
	}
	if gotRows[1].PubMedSupported != SupportStatusYes ||
		gotRows[1].PubMedRecordCount != 2 {
		t.Fatalf("second row = %#v, want successful continuation", gotRows[1])
	}
}

func TestPubMedProbeCoverageRejectsResolvedRowWithoutISSNBeforeAnyRequest(
	t *testing.T,
) {
	t.Parallel()

	rows := []RegistryRow{
		pubMedProbeResolvedRow(1, []string{"0028-0836"}),
		pubMedProbeResolvedRow(2, nil),
	}
	counter := &scriptedPubMedCoverageCounter{}

	_, _, err := ProbePubMedCoverage(
		context.Background(),
		rows,
		counter,
		time.Now,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "row 2") ||
		!strings.Contains(err.Error(), "resolved") ||
		!strings.Contains(err.Error(), "ISSN") {
		t.Fatalf("ProbePubMedCoverage() error = %v, want located missing ISSN error", err)
	}
	if len(counter.calls) != 0 {
		t.Fatalf("CountCoverage() calls = %d, want deterministic preflight failure", len(counter.calls))
	}
}

func TestPubMedProbeCoverageStopsImmediatelyWhenParentContextIsCanceled(
	t *testing.T,
) {
	t.Parallel()

	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		counter := &scriptedPubMedCoverageCounter{}

		_, _, err := ProbePubMedCoverage(
			ctx,
			[]RegistryRow{pubMedProbeResolvedRow(1, []string{"0028-0836"})},
			counter,
			time.Now,
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ProbePubMedCoverage() error = %v, want context.Canceled", err)
		}
		if len(counter.calls) != 0 {
			t.Fatalf("CountCoverage() calls = %d, want zero", len(counter.calls))
		}
	})

	t.Run("canceled by first request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		counter := &scriptedPubMedCoverageCounter{
			steps: []pubMedCoverageStep{
				{
					beforeReturn: cancel,
					err:          errors.New("request observed cancellation"),
				},
				{
					result: pubmed.CoverageResult{
						Count:          1,
						ResponseSHA256: pubMedTestHashOne,
					},
				},
			},
		}

		_, _, err := ProbePubMedCoverage(
			ctx,
			[]RegistryRow{
				pubMedProbeResolvedRow(1, []string{"0028-0836"}),
				pubMedProbeResolvedRow(2, []string{"2049-3630"}),
			},
			counter,
			time.Now,
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ProbePubMedCoverage() error = %v, want context.Canceled", err)
		}
		if len(counter.calls) != 1 {
			t.Fatalf("CountCoverage() calls = %d, want immediate stop after first", len(counter.calls))
		}
	})
}

type pubMedCoverageStep struct {
	result       pubmed.CoverageResult
	err          error
	beforeReturn func()
}

type scriptedPubMedCoverageCounter struct {
	calls []pubmed.CoverageQuery
	steps []pubMedCoverageStep
}

func (counter *scriptedPubMedCoverageCounter) CountCoverage(
	_ context.Context,
	query pubmed.CoverageQuery,
) (pubmed.CoverageResult, error) {
	cloned := pubmed.CoverageQuery{
		JournalISSNs: slices.Clone(query.JournalISSNs),
	}
	counter.calls = append(counter.calls, cloned)
	index := len(counter.calls) - 1
	if index >= len(counter.steps) {
		return pubmed.CoverageResult{}, errors.New("unexpected coverage call")
	}
	step := counter.steps[index]
	if step.beforeReturn != nil {
		step.beforeReturn()
	}
	return step.result, step.err
}

func pubMedProbeResolvedRow(order int, issns []string) RegistryRow {
	row := RegistryRow{
		SourceRow: SourceRow{
			Domain:            "medicine",
			SourceOrder:       order,
			SourceJournalName: "Resolved Journal",
			SourceURL:         "https://example.test/medicine",
		},
		AllISSNs:          slices.Clone(issns),
		CrossrefTitle:     "Resolved Journal",
		CrossrefPublisher: "Publisher",
		CrossrefTotalDOIs: 1,
		CrossrefSupported: SupportStatusYes,
		OpenAlexSupported: SupportStatusUnknown,
		PubMedSupported:   SupportStatusUnknown,
		MatchStatus:       MatchStatusResolved,
	}
	if len(issns) > 0 {
		row.PrintISSN = issns[0]
	}
	return row
}

func pubMedProbeUnresolvedRow(order int, status MatchStatus) RegistryRow {
	return RegistryRow{
		SourceRow: SourceRow{
			Domain:            "biology",
			SourceOrder:       order,
			SourceJournalName: "Unprobed Journal",
			SourceURL:         "https://example.test/biology",
		},
		CrossrefSupported: SupportStatusUnknown,
		OpenAlexSupported: SupportStatusUnknown,
		PubMedSupported:   SupportStatusUnknown,
		MatchStatus:       status,
	}
}
