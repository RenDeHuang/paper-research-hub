package pubmedsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
)

func TestBuildDailyJournalBatchesRejectsEmptyInput(t *testing.T) {
	t.Parallel()

	if _, err := BuildDailyJournalBatches(nil); err == nil {
		t.Fatal("BuildDailyJournalBatches() accepted empty input")
	}
}

func TestBuildDailyJournalBatchesAcceptsExactlyMaximumISSNTerms(t *testing.T) {
	t.Parallel()

	journal := dailyTestJournal(
		"journal:maximum",
		dailyTestISSNs(t, 1_000_000, MaxDailyISSNTerms),
	)

	batches, err := BuildDailyJournalBatches([]Journal{journal})
	if err != nil {
		t.Fatalf("BuildDailyJournalBatches() error = %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	if got := len(batches[0].ISSNs()); got != MaxDailyISSNTerms {
		t.Fatalf("batch ISSN terms = %d, want %d", got, MaxDailyISSNTerms)
	}
}

func TestBuildDailyJournalBatchesKeepsWholeJournalAfter2047Terms(t *testing.T) {
	t.Parallel()

	first := dailyTestJournal(
		"journal:first",
		dailyTestISSNs(t, 1_100_000, MaxDailyISSNTerms-1),
	)
	second := dailyTestJournal(
		"journal:second",
		dailyTestISSNs(t, 2_100_000, 2),
	)

	batches, err := BuildDailyJournalBatches([]Journal{first, second})
	if err != nil {
		t.Fatalf("BuildDailyJournalBatches() error = %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(batches))
	}
	if got := len(batches[0].ISSNs()); got != MaxDailyISSNTerms-1 {
		t.Fatalf("first batch ISSN terms = %d, want %d", got, MaxDailyISSNTerms-1)
	}
	if got := len(batches[1].ISSNs()); got != 2 {
		t.Fatalf("second batch ISSN terms = %d, want 2", got)
	}
	if got := dailyJournalKeys(batches[1].Journals()); !slices.Equal(got, []string{second.Key()}) {
		t.Fatalf("second batch journals = %v, want whole second journal", got)
	}
}

func TestBuildDailyJournalBatchesRejectsJournalAboveMaximumISSNTerms(t *testing.T) {
	t.Parallel()

	journal := dailyTestJournal(
		"journal:too-large",
		dailyTestISSNs(t, 1_200_000, MaxDailyISSNTerms+1),
	)

	if _, err := BuildDailyJournalBatches([]Journal{journal}); err == nil ||
		!strings.Contains(err.Error(), "2049") ||
		!strings.Contains(err.Error(), "2048") {
		t.Fatalf(
			"BuildDailyJournalBatches() error = %v, want explicit 2049-over-2048 failure",
			err,
		)
	}
}

func TestBuildDailyJournalBatchesRejectsCrossJournalISSNIntersection(t *testing.T) {
	t.Parallel()

	issns := dailyTestISSNs(t, 1_300_000, 3)
	first := dailyTestJournal("journal:first", []string{issns[0], issns[1]})
	second := dailyTestJournal("journal:second", []string{issns[1], issns[2]})

	if _, err := BuildDailyJournalBatches([]Journal{first, second}); err == nil ||
		!strings.Contains(err.Error(), issns[1]) ||
		!strings.Contains(err.Error(), first.Key()) ||
		!strings.Contains(err.Error(), second.Key()) {
		t.Fatalf(
			"BuildDailyJournalBatches() error = %v, want explicit intersecting ISSN failure",
			err,
		)
	}
}

func TestBuildDailyJournalBatchesClonesInputAndAccessors(t *testing.T) {
	t.Parallel()

	issns := dailyTestISSNs(t, 1_400_000, 3)
	journals := []Journal{
		dailyTestJournal("journal:first", []string{issns[2], issns[0], issns[2]}),
		dailyTestJournal("journal:second", []string{issns[1]}),
	}
	wantJournalKeys := []string{journals[0].Key(), journals[1].Key()}
	wantISSNs := slices.Clone(issns)
	sort.Strings(wantISSNs)

	batches, err := BuildDailyJournalBatches(journals)
	if err != nil {
		t.Fatalf("BuildDailyJournalBatches() error = %v", err)
	}
	if len(batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(batches))
	}
	if got := dailyJournalKeys(batches[0].Journals()); !slices.Equal(got, wantJournalKeys) {
		t.Fatalf("batch journal order = %v, want registry order %v", got, wantJournalKeys)
	}
	if got := batches[0].ISSNs(); !slices.Equal(got, wantISSNs) {
		t.Fatalf("batch ISSNs = %v, want sorted unique %v", got, wantISSNs)
	}

	journals[0].key = "mutated-input"
	journals[0].issns[0] = dailyTestISSNs(t, 1_500_000, 1)[0]
	journals = append(
		journals,
		dailyTestJournal("journal:third", dailyTestISSNs(t, 1_500_001, 1)),
	)
	if got := dailyJournalKeys(batches[0].Journals()); !slices.Equal(got, wantJournalKeys) {
		t.Fatalf("input mutation changed batch journals to %v", got)
	}
	if got := batches[0].ISSNs(); !slices.Equal(got, wantISSNs) {
		t.Fatalf("input mutation changed batch ISSNs to %v", got)
	}

	returnedJournals := batches[0].Journals()
	returnedJournals[0].key = "mutated-accessor"
	returnedJournals[0].issns[0] = dailyTestISSNs(t, 1_600_000, 1)[0]
	returnedJournals = append(returnedJournals, Journal{})
	returnedISSNs := batches[0].ISSNs()
	returnedISSNs[0] = dailyTestISSNs(t, 1_600_001, 1)[0]

	if got := dailyJournalKeys(batches[0].Journals()); !slices.Equal(got, wantJournalKeys) {
		t.Fatalf("Journals() exposed mutable storage: got %v", got)
	}
	if got := batches[0].ISSNs(); !slices.Equal(got, wantISSNs) {
		t.Fatalf("ISSNs() exposed mutable storage: got %v", got)
	}
}

func TestBuildDailyJournalBatchesProducesStableContentKey(t *testing.T) {
	t.Parallel()

	prefix := dailyTestJournal(
		"journal:prefix",
		dailyTestISSNs(t, 1_700_000, MaxDailyISSNTerms-1),
	)
	targetISSNs := dailyTestISSNs(t, 2_700_000, 2)
	target := dailyTestJournal("journal:target", []string{targetISSNs[1], targetISSNs[0]})

	firstPlan, err := BuildDailyJournalBatches([]Journal{prefix, target})
	if err != nil {
		t.Fatalf("BuildDailyJournalBatches() first error = %v", err)
	}
	repeatedPlan, err := BuildDailyJournalBatches([]Journal{prefix, target})
	if err != nil {
		t.Fatalf("BuildDailyJournalBatches() repeated error = %v", err)
	}
	targetOnlyPlan, err := BuildDailyJournalBatches([]Journal{target})
	if err != nil {
		t.Fatalf("BuildDailyJournalBatches() target-only error = %v", err)
	}

	if firstPlan[1].Key() != repeatedPlan[1].Key() {
		t.Fatalf(
			"repeated planning produced keys %q and %q",
			firstPlan[1].Key(),
			repeatedPlan[1].Key(),
		)
	}
	if firstPlan[1].Key() != targetOnlyPlan[0].Key() {
		t.Fatalf(
			"same content at different batch indexes produced keys %q and %q",
			firstPlan[1].Key(),
			targetOnlyPlan[0].Key(),
		)
	}

	sortedTargetISSNs := slices.Clone(targetISSNs)
	sort.Strings(sortedTargetISSNs)
	wantKey := dailyExpectedBatchKey(t, []string{target.Key()}, sortedTargetISSNs)
	if firstPlan[1].Key() != wantKey {
		t.Fatalf("batch key = %q, want %q", firstPlan[1].Key(), wantKey)
	}
}

func dailyTestJournal(key string, issns []string) Journal {
	return Journal{
		key:               key,
		issns:             slices.Clone(issns),
		domain:            "test",
		sourceOrder:       1,
		sourceJournalName: key,
	}
}

func dailyTestISSNs(t *testing.T, start, count int) []string {
	t.Helper()

	if start < 0 || count < 0 || start+count > 10_000_000 {
		t.Fatalf("invalid test ISSN range start=%d count=%d", start, count)
	}
	result := make([]string, 0, count)
	for value := start; value < start+count; value++ {
		body := fmt.Sprintf("%07d", value)
		sum := 0
		for index := 0; index < 7; index++ {
			sum += int(body[index]-'0') * (8 - index)
		}
		checksum := (11 - sum%11) % 11
		checkDigit := fmt.Sprintf("%d", checksum)
		if checksum == 10 {
			checkDigit = "X"
		}
		issn := body[:4] + "-" + body[4:] + checkDigit
		parsed, err := venue.ParseISSN(venue.ISSNRoleLinking, issn)
		if err != nil || parsed.String() != issn {
			t.Fatalf("generated invalid ISSN %q: parsed=%q error=%v", issn, parsed.String(), err)
		}
		result = append(result, issn)
	}
	return result
}

func dailyJournalKeys(journals []Journal) []string {
	result := make([]string, len(journals))
	for index := range journals {
		result[index] = journals[index].Key()
	}
	return result
}

func dailyExpectedBatchKey(t *testing.T, journalKeys, issns []string) string {
	t.Helper()

	payload, err := json.Marshal(struct {
		Version     int      `json:"version"`
		JournalKeys []string `json:"journal_keys"`
		ISSNs       []string `json:"issns"`
	}{
		Version:     1,
		JournalKeys: journalKeys,
		ISSNs:       issns,
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	sum := sha256.Sum256(payload)
	return "daily-journal-batch-sha256:" + hex.EncodeToString(sum[:])
}
