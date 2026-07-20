package pubmedsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
)

const MaxDailyISSNTerms = 2048

type DailyJournalBatch struct {
	key      string
	journals []Journal
	issns    []string
}

func (batch DailyJournalBatch) Key() string {
	return batch.key
}

func (batch DailyJournalBatch) Journals() []Journal {
	return cloneDailyJournals(batch.journals)
}

func (batch DailyJournalBatch) ISSNs() []string {
	return slices.Clone(batch.issns)
}

func BuildDailyJournalBatches(journals []Journal) ([]DailyJournalBatch, error) {
	if len(journals) == 0 {
		return nil, errors.New("daily journal batching requires at least one journal")
	}

	batches := make([]DailyJournalBatch, 0)
	currentJournals := make([]Journal, 0)
	currentISSNs := make([]string, 0, MaxDailyISSNTerms)
	issnOwners := make(map[string]string)

	flush := func() error {
		if len(currentJournals) == 0 {
			return nil
		}
		batch, err := newDailyJournalBatch(currentJournals, currentISSNs)
		if err != nil {
			return err
		}
		batches = append(batches, batch)
		currentJournals = nil
		currentISSNs = make([]string, 0, MaxDailyISSNTerms)
		return nil
	}

	for index := range journals {
		journal := journals[index].clone()
		if journal.key == "" {
			return nil, fmt.Errorf("daily journal at index %d has an empty key", index)
		}
		if len(journal.issns) == 0 {
			return nil, fmt.Errorf(
				"daily journal %q has no ISSNs",
				journal.key,
			)
		}

		journalISSNs := make([]string, 0, len(journal.issns))
		seenInJournal := make(map[string]struct{}, len(journal.issns))
		for _, issn := range journal.issns {
			if _, duplicate := seenInJournal[issn]; duplicate {
				continue
			}
			seenInJournal[issn] = struct{}{}

			if owner, exists := issnOwners[issn]; exists {
				return nil, fmt.Errorf(
					"daily journals %q and %q intersect at ISSN %q",
					owner,
					journal.key,
					issn,
				)
			}
			issnOwners[issn] = journal.key
			journalISSNs = append(journalISSNs, issn)
		}

		if len(journalISSNs) > MaxDailyISSNTerms {
			return nil, fmt.Errorf(
				"daily journal %q has %d unique ISSNs, exceeding limit %d",
				journal.key,
				len(journalISSNs),
				MaxDailyISSNTerms,
			)
		}
		if len(currentJournals) > 0 &&
			len(currentISSNs)+len(journalISSNs) > MaxDailyISSNTerms {
			if err := flush(); err != nil {
				return nil, err
			}
		}

		currentJournals = append(currentJournals, journal)
		currentISSNs = append(currentISSNs, journalISSNs...)
	}

	if err := flush(); err != nil {
		return nil, err
	}
	return batches, nil
}

func newDailyJournalBatch(
	journals []Journal,
	issns []string,
) (DailyJournalBatch, error) {
	clonedJournals := cloneDailyJournals(journals)
	sortedISSNs := slices.Clone(issns)
	sort.Strings(sortedISSNs)

	journalKeys := make([]string, len(clonedJournals))
	for index := range clonedJournals {
		journalKeys[index] = clonedJournals[index].Key()
	}
	key, err := dailyJournalBatchKey(journalKeys, sortedISSNs)
	if err != nil {
		return DailyJournalBatch{}, fmt.Errorf("derive daily journal batch key: %w", err)
	}
	return DailyJournalBatch{
		key:      key,
		journals: clonedJournals,
		issns:    sortedISSNs,
	}, nil
}

func cloneDailyJournals(journals []Journal) []Journal {
	result := make([]Journal, len(journals))
	for index := range journals {
		result[index] = journals[index].clone()
	}
	return result
}

type dailyJournalBatchKeyPayload struct {
	Version     int      `json:"version"`
	JournalKeys []string `json:"journal_keys"`
	ISSNs       []string `json:"issns"`
}

func dailyJournalBatchKey(journalKeys, issns []string) (string, error) {
	payload, err := json.Marshal(dailyJournalBatchKeyPayload{
		Version:     1,
		JournalKeys: slices.Clone(journalKeys),
		ISSNs:       slices.Clone(issns),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "daily-journal-batch-sha256:" + hex.EncodeToString(sum[:]), nil
}
