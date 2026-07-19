package venueenrich

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
)

const (
	MatchMethodCrossrefExactNormalizedTitle = "crossref_exact_normalized_title"

	maxCrossrefMatchRecordBytes = 8 << 20
)

type crossrefMatchCandidate struct {
	title          string
	publisher      string
	totalDOIs      int64
	allISSNs       []string
	printISSN      string
	electronicISSN string
	recordNumber   int
}

func MatchCrossrefCatalog(
	sources []SourceRow,
	catalog CrossrefCatalog,
) ([]RegistryRow, error) {
	normalizedTitles := make([]string, len(sources))
	targetTitles := make(map[string]struct{}, len(sources))
	for index, source := range sources {
		if err := source.Validate(); err != nil {
			return nil, fmt.Errorf(
				"source row %d: invalid source row: %w",
				index+1,
				err,
			)
		}
		normalized, err := NormalizeTitle(source.SourceJournalName)
		if err != nil {
			return nil, fmt.Errorf(
				"source row %d: normalize source_journal_name: %w",
				index+1,
				err,
			)
		}
		if normalized == "" {
			return nil, fmt.Errorf(
				"source row %d: normalized source_journal_name must not be empty",
				index+1,
			)
		}
		normalizedTitles[index] = normalized
		targetTitles[normalized] = struct{}{}
	}

	candidates := make(
		map[string]map[string]crossrefMatchCandidate,
		len(targetTitles),
	)
	if err := streamCrossrefMatchCandidates(
		catalog.CatalogPath,
		targetTitles,
		candidates,
	); err != nil {
		return nil, err
	}

	rows := make([]RegistryRow, len(sources))
	for index, source := range sources {
		row := RegistryRow{
			SourceRow:         source,
			CrossrefSupported: SupportStatusUnknown,
			OpenAlexSupported: SupportStatusUnknown,
			PubMedSupported:   SupportStatusUnknown,
			MatchMethod:       MatchMethodCrossrefExactNormalizedTitle,
			MatchStatus:       MatchStatusUnresolved,
		}

		identities := candidates[normalizedTitles[index]]
		switch len(identities) {
		case 0:
		case 1:
			var candidate crossrefMatchCandidate
			for _, current := range identities {
				candidate = current
			}
			row.PrintISSN = candidate.printISSN
			row.EISSN = candidate.electronicISSN
			row.AllISSNs = slices.Clone(candidate.allISSNs)
			row.CrossrefTitle = candidate.title
			row.CrossrefPublisher = candidate.publisher
			row.CrossrefTotalDOIs = candidate.totalDOIs
			row.CrossrefSupported = SupportStatusYes
			row.MatchStatus = MatchStatusResolved
		default:
			row.MatchStatus = MatchStatusAmbiguous
		}

		if err := row.Validate(); err != nil {
			return nil, fmt.Errorf(
				"source row %d: constructed registry row is invalid: %w",
				index+1,
				err,
			)
		}
		rows[index] = row
	}
	return rows, nil
}

func streamCrossrefMatchCandidates(
	path string,
	targetTitles map[string]struct{},
	candidates map[string]map[string]crossrefMatchCandidate,
) (returnErr error) {
	if path == "" {
		return errors.New("Crossref catalog path must not be empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open Crossref catalog %q: %w", path, err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			returnErr = errors.Join(
				returnErr,
				fmt.Errorf("close Crossref catalog %q: %w", path, err),
			)
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat Crossref catalog %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf(
			"Crossref catalog %q must be a regular JSONL file",
			path,
		)
	}

	reader := bufio.NewReaderSize(file, maxCrossrefMatchRecordBytes+2)
	for recordNumber := 1; ; recordNumber++ {
		record, readErr := reader.ReadSlice('\n')
		switch {
		case readErr == nil:
			record = record[:len(record)-1]
			if len(record) > maxCrossrefMatchRecordBytes {
				return oversizedCrossrefMatchRecordError(path, recordNumber)
			}
		case errors.Is(readErr, bufio.ErrBufferFull):
			return oversizedCrossrefMatchRecordError(path, recordNumber)
		case errors.Is(readErr, io.EOF):
			if len(record) == 0 {
				return nil
			}
			if len(record) > maxCrossrefMatchRecordBytes {
				return oversizedCrossrefMatchRecordError(path, recordNumber)
			}
			return fmt.Errorf(
				"Crossref catalog %q record %d lacks a terminating newline",
				path,
				recordNumber,
			)
		default:
			return fmt.Errorf(
				"read Crossref catalog %q record %d: %w",
				path,
				recordNumber,
				readErr,
			)
		}

		if len(record) == 0 {
			return fmt.Errorf(
				"Crossref catalog %q record %d is empty",
				path,
				recordNumber,
			)
		}
		if !utf8.Valid(record) {
			return fmt.Errorf(
				"Crossref catalog %q record %d contains invalid UTF-8",
				path,
				recordNumber,
			)
		}

		journal, err := decodeCrossrefJournal(record)
		if err != nil {
			return fmt.Errorf(
				"Crossref catalog %q record %d: %w",
				path,
				recordNumber,
				err,
			)
		}
		normalizedTitle, err := NormalizeTitle(journal.Title)
		if err != nil {
			return fmt.Errorf(
				"Crossref catalog %q record %d: normalize title: %w",
				path,
				recordNumber,
				err,
			)
		}
		if normalizedTitle == "" {
			return fmt.Errorf(
				"Crossref catalog %q record %d title normalizes to empty",
				path,
				recordNumber,
			)
		}

		candidate, identityKey, err := canonicalCrossrefMatchCandidate(
			journal,
			recordNumber,
		)
		if err != nil {
			return fmt.Errorf(
				"Crossref catalog %q record %d: %w",
				path,
				recordNumber,
				err,
			)
		}
		if _, targeted := targetTitles[normalizedTitle]; !targeted {
			continue
		}

		identities := candidates[normalizedTitle]
		if identities == nil {
			identities = make(map[string]crossrefMatchCandidate)
			candidates[normalizedTitle] = identities
		}
		if existing, duplicate := identities[identityKey]; duplicate {
			if conflict := crossrefMatchEvidenceConflict(existing, candidate); conflict != "" {
				return fmt.Errorf(
					"Crossref catalog %q record %d conflicts with record %d for normalized title %q and ISSN set %v: %s",
					path,
					recordNumber,
					existing.recordNumber,
					normalizedTitle,
					candidate.allISSNs,
					conflict,
				)
			}
			continue
		}
		identities[identityKey] = candidate
	}
}

func canonicalCrossrefMatchCandidate(
	journal CrossrefJournal,
	recordNumber int,
) (crossrefMatchCandidate, string, error) {
	if len(journal.ISSNs) == 0 {
		return crossrefMatchCandidate{}, "", errors.New(
			"journal identity requires at least one ISSN",
		)
	}

	allISSNs := make([]string, 0, len(journal.ISSNs))
	seen := make(map[string]struct{}, len(journal.ISSNs))
	for index, raw := range journal.ISSNs {
		parsed, err := venue.ParseISSN(venue.ISSNRoleLinking, raw)
		if err != nil {
			return crossrefMatchCandidate{}, "", fmt.Errorf(
				"ISSN[%d] %q: %w",
				index,
				raw,
				err,
			)
		}
		canonical := parsed.String()
		if canonical != raw {
			return crossrefMatchCandidate{}, "", fmt.Errorf(
				"ISSN[%d] %q must use canonical form %q",
				index,
				raw,
				canonical,
			)
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		allISSNs = append(allISSNs, canonical)
	}
	sort.Strings(allISSNs)

	var printISSN, electronicISSN string
	for index, typed := range journal.ISSNTypes {
		if _, exists := seen[typed.Value]; !exists {
			return crossrefMatchCandidate{}, "", fmt.Errorf(
				"issn-type[%d] value %q is absent from canonical ISSN set",
				index,
				typed.Value,
			)
		}

		var (
			role    venue.ISSNRole
			current *string
		)
		switch typed.Type {
		case "print":
			role = venue.ISSNRolePrint
			current = &printISSN
		case "electronic":
			role = venue.ISSNRoleElectronic
			current = &electronicISSN
		default:
			return crossrefMatchCandidate{}, "", fmt.Errorf(
				"issn-type[%d] has unsupported role %q",
				index,
				typed.Type,
			)
		}

		parsed, err := venue.ParseISSN(role, typed.Value)
		if err != nil {
			return crossrefMatchCandidate{}, "", fmt.Errorf(
				"issn-type[%d] %s value %q: %w",
				index,
				typed.Type,
				typed.Value,
				err,
			)
		}
		canonical := parsed.String()
		if canonical != typed.Value {
			return crossrefMatchCandidate{}, "", fmt.Errorf(
				"issn-type[%d] %s value %q must use canonical form %q",
				index,
				typed.Type,
				typed.Value,
				canonical,
			)
		}
		if *current != "" && *current != canonical {
			return crossrefMatchCandidate{}, "", fmt.Errorf(
				"%s ISSN role has conflicting values %q and %q",
				typed.Type,
				*current,
				canonical,
			)
		}
		*current = canonical
	}

	candidate := crossrefMatchCandidate{
		title:          journal.Title,
		publisher:      journal.Publisher,
		totalDOIs:      journal.TotalDOIs,
		allISSNs:       allISSNs,
		printISSN:      printISSN,
		electronicISSN: electronicISSN,
		recordNumber:   recordNumber,
	}
	return candidate, strings.Join(allISSNs, "\x00"), nil
}

func crossrefMatchEvidenceConflict(
	first crossrefMatchCandidate,
	second crossrefMatchCandidate,
) string {
	switch {
	case first.title != second.title:
		return fmt.Sprintf(
			"title evidence differs: %q versus %q",
			first.title,
			second.title,
		)
	case first.publisher != second.publisher:
		return fmt.Sprintf(
			"publisher evidence differs: %q versus %q",
			first.publisher,
			second.publisher,
		)
	case first.printISSN != second.printISSN ||
		first.electronicISSN != second.electronicISSN:
		return fmt.Sprintf(
			"ISSN role evidence differs: print %q/%q, electronic %q/%q",
			first.printISSN,
			second.printISSN,
			first.electronicISSN,
			second.electronicISSN,
		)
	case first.totalDOIs != second.totalDOIs:
		return fmt.Sprintf(
			"DOI count evidence differs: %d versus %d",
			first.totalDOIs,
			second.totalDOIs,
		)
	default:
		return ""
	}
}

func oversizedCrossrefMatchRecordError(path string, recordNumber int) error {
	return fmt.Errorf(
		"Crossref catalog %q record %d exceeds the %d-byte limit",
		path,
		recordNumber,
		maxCrossrefMatchRecordBytes,
	)
}
