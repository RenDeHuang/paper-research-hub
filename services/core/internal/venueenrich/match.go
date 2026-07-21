package venueenrich

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
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
	title                    string
	titleConflicted          bool
	publisher                string
	publisherConflicted      bool
	totalDOIs                int64
	totalDOIsConflicted      bool
	allISSNs                 []string
	printISSN                string
	printISSNConflicted      bool
	electronicISSN           string
	electronicISSNConflicted bool
	recordNumber             int
}

type crossrefTitleMatch struct {
	status      MatchStatus
	identityKey string
	candidate   crossrefMatchCandidate
	components  []crossrefIdentityComponent
}

type crossrefIdentityComponent struct {
	identityKey string
	candidate   crossrefMatchCandidate
}

func MatchCrossrefCatalog(
	sources []SourceRow,
	catalog CrossrefCatalog,
) ([]RegistryRow, error) {
	if len(sources) == 0 {
		return []RegistryRow{}, nil
	}

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

	matches := make(
		map[string]*crossrefTitleMatch,
		len(targetTitles),
	)
	if err := streamCrossrefMatchCandidates(
		catalog,
		targetTitles,
		matches,
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

		match := matches[normalizedTitles[index]]
		switch {
		case match == nil:
		case match.status == MatchStatusResolved:
			candidate := match.candidate
			row.PrintISSN = candidate.printISSN
			row.EISSN = candidate.electronicISSN
			row.AllISSNs = slices.Clone(candidate.allISSNs)
			row.CrossrefTitle = candidate.title
			row.CrossrefPublisher = candidate.publisher
			row.CrossrefTotalDOIs = candidate.totalDOIs
			row.CrossrefSupported = SupportStatusYes
			row.MatchStatus = MatchStatusResolved
		case match.status == MatchStatusAmbiguous:
			row.MatchStatus = MatchStatusAmbiguous
		default:
			return nil, fmt.Errorf(
				"source row %d: internal Crossref match state %q is invalid",
				index+1,
				match.status,
			)
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
	catalog CrossrefCatalog,
	targetTitles map[string]struct{},
	matches map[string]*crossrefTitleMatch,
) (returnErr error) {
	if err := validateCrossrefMatchCatalog(catalog); err != nil {
		return err
	}
	path := catalog.CatalogPath
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
	hasher := sha256.New()
	var recordCount, catalogBytes int64
	for {
		line, readErr := reader.ReadSlice('\n')
		recordNumber := int(recordCount) + 1
		switch {
		case readErr == nil:
			written, hashErr := hasher.Write(line)
			if hashErr != nil {
				return fmt.Errorf(
					"hash Crossref catalog %q record %d: %w",
					path,
					recordNumber,
					hashErr,
				)
			}
			if written != len(line) {
				return fmt.Errorf(
					"hash Crossref catalog %q record %d: wrote %d of %d bytes",
					path,
					recordNumber,
					written,
					len(line),
				)
			}
			catalogBytes += int64(len(line))
			recordCount++
			record := line[:len(line)-1]
			if len(record) > maxCrossrefMatchRecordBytes {
				return oversizedCrossrefMatchRecordError(path, recordNumber)
			}
			if err := processCrossrefMatchRecord(
				path,
				recordNumber,
				record,
				targetTitles,
				matches,
			); err != nil {
				return err
			}
		case errors.Is(readErr, bufio.ErrBufferFull):
			return oversizedCrossrefMatchRecordError(path, recordNumber)
		case errors.Is(readErr, io.EOF):
			if len(line) == 0 {
				return verifyCrossrefMatchCatalogStream(
					path,
					catalog.Manifest,
					recordCount,
					catalogBytes,
					hex.EncodeToString(hasher.Sum(nil)),
				)
			}
			if len(line) > maxCrossrefMatchRecordBytes {
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
	}
}

func validateCrossrefMatchCatalog(catalog CrossrefCatalog) error {
	if catalog.CatalogPath == "" {
		return errors.New("Crossref catalog path must not be empty")
	}
	if !catalog.Manifest.Complete {
		return errors.New(
			"Crossref catalog manifest complete must be true",
		)
	}
	sourceURL := catalog.Manifest.SourceURL
	if sourceURL == "" || sourceURL != strings.TrimSpace(sourceURL) {
		return errors.New(
			"Crossref catalog manifest source_url must be non-empty and trimmed",
		)
	}
	parsedSourceURL, err := url.Parse(sourceURL)
	if err != nil ||
		(parsedSourceURL.Scheme != "http" &&
			parsedSourceURL.Scheme != "https") ||
		parsedSourceURL.Host == "" ||
		parsedSourceURL.User != nil ||
		parsedSourceURL.Fragment != "" {
		return fmt.Errorf(
			"Crossref catalog manifest source_url %q must be an absolute HTTP(S) URL without credentials or fragment",
			sourceURL,
		)
	}
	if err := validateCrossrefCatalogManifest(
		catalog.Manifest,
		sourceURL,
	); err != nil {
		return err
	}
	return nil
}

func verifyCrossrefMatchCatalogStream(
	path string,
	manifest CrossrefCatalogManifest,
	recordCount int64,
	catalogBytes int64,
	catalogSHA256 string,
) error {
	if recordCount != manifest.RecordCount {
		return fmt.Errorf(
			"Crossref catalog %q record count = %d, manifest = %d",
			path,
			recordCount,
			manifest.RecordCount,
		)
	}
	if catalogBytes != manifest.CatalogBytes {
		return fmt.Errorf(
			"Crossref catalog %q byte count = %d, manifest = %d",
			path,
			catalogBytes,
			manifest.CatalogBytes,
		)
	}
	if catalogSHA256 != manifest.CatalogSHA256 {
		return fmt.Errorf(
			"Crossref catalog %q SHA-256 = %s, manifest = %s",
			path,
			catalogSHA256,
			manifest.CatalogSHA256,
		)
	}
	return nil
}

func processCrossrefMatchRecord(
	path string,
	recordNumber int,
	record []byte,
	targetTitles map[string]struct{},
	matches map[string]*crossrefTitleMatch,
) error {
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
	if len(journal.ISSNs) == 0 {
		return nil
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
	if _, targeted := targetTitles[normalizedTitle]; !targeted {
		return nil
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
	if len(candidate.allISSNs) == 0 {
		return nil
	}

	match := matches[normalizedTitle]
	if match == nil {
		match = &crossrefTitleMatch{}
		matches[normalizedTitle] = match
	}
	existingRecord, conflict := match.observe(identityKey, candidate)
	if conflict != "" {
		return fmt.Errorf(
			"Crossref catalog %q record %d conflicts with record %d for normalized title %q and ISSN set %v: %s",
			path,
			recordNumber,
			existingRecord,
			normalizedTitle,
			candidate.allISSNs,
			conflict,
		)
	}
	return nil
}

func (match *crossrefTitleMatch) observe(
	identityKey string,
	candidate crossrefMatchCandidate,
) (existingRecord int, conflict string) {
	for index := range match.components {
		component := &match.components[index]
		if component.identityKey == identityKey {
			component.candidate = mergeCrossrefMatchCandidateEvidence(
				component.candidate,
				candidate,
			)
			match.refreshResolution()
			return 0, ""
		}
	}

	match.components = append(match.components, crossrefIdentityComponent{
		identityKey: identityKey,
		candidate:   candidate,
	})
	match.refreshResolution()
	return 0, ""
}

func (match *crossrefTitleMatch) refreshResolution() {
	switch len(match.components) {
	case 0:
		match.status = ""
		match.identityKey = ""
		match.candidate = crossrefMatchCandidate{}
	case 1:
		match.status = MatchStatusResolved
		match.identityKey = match.components[0].identityKey
		match.candidate = match.components[0].candidate
	default:
		candidate, compatible := mergeCompatibleCrossrefIdentityComponents(
			match.components,
		)
		if compatible {
			match.status = MatchStatusResolved
			match.identityKey = strings.Join(candidate.allISSNs, "\x00")
			match.candidate = candidate
			return
		}
		match.status = MatchStatusAmbiguous
		match.identityKey = ""
		match.candidate = crossrefMatchCandidate{}
	}
}

func mergeCompatibleCrossrefIdentityComponents(
	components []crossrefIdentityComponent,
) (crossrefMatchCandidate, bool) {
	if len(components) < 2 {
		return crossrefMatchCandidate{}, false
	}
	baseline := components[0].candidate
	if baseline.titleConflicted ||
		baseline.publisherConflicted ||
		baseline.publisher == "" ||
		baseline.totalDOIsConflicted ||
		baseline.totalDOIs <= 0 {
		return crossrefMatchCandidate{}, false
	}
	for _, component := range components[1:] {
		candidate := component.candidate
		if candidate.titleConflicted ||
			candidate.publisherConflicted ||
			candidate.totalDOIsConflicted ||
			candidate.title != baseline.title ||
			candidate.publisher != baseline.publisher ||
			candidate.totalDOIs != baseline.totalDOIs {
			return crossrefMatchCandidate{}, false
		}
	}
	for left := 0; left < len(components); left++ {
		for right := left + 1; right < len(components); right++ {
			leftISSNs := components[left].candidate.allISSNs
			rightISSNs := components[right].candidate.allISSNs
			if !crossrefISSNSetSubset(leftISSNs, rightISSNs) &&
				!crossrefISSNSetSubset(rightISSNs, leftISSNs) {
				return crossrefMatchCandidate{}, false
			}
		}
	}

	merged := baseline
	for _, component := range components[1:] {
		merged = mergeCrossrefMatchCandidateEvidence(
			merged,
			component.candidate,
		)
	}
	return merged, true
}

func crossrefISSNSetSubset(subset, superset []string) bool {
	left, right := 0, 0
	for left < len(subset) && right < len(superset) {
		switch {
		case subset[left] == superset[right]:
			left++
			right++
		case subset[left] > superset[right]:
			right++
		default:
			return false
		}
	}
	return left == len(subset)
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
			continue
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
	if len(allISSNs) == 0 {
		return crossrefMatchCandidate{}, "", nil
	}

	var printISSN, electronicISSN string
	for index, typed := range journal.ISSNTypes {
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
			continue
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
		if _, exists := seen[canonical]; !exists {
			return crossrefMatchCandidate{}, "", fmt.Errorf(
				"issn-type[%d] value %q is absent from canonical ISSN set",
				index,
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

func mergeCrossrefMatchCandidateEvidence(
	first crossrefMatchCandidate,
	second crossrefMatchCandidate,
) crossrefMatchCandidate {
	merged := first
	merged.allISSNs = append(
		slices.Clone(first.allISSNs),
		second.allISSNs...,
	)
	sort.Strings(merged.allISSNs)
	merged.allISSNs = slices.Compact(merged.allISSNs)
	if first.titleConflicted ||
		second.titleConflicted ||
		first.title != second.title {
		merged.titleConflicted = true
	}
	merged.publisher, merged.publisherConflicted = mergeOptionalCrossrefEvidence(
		first.publisher,
		first.publisherConflicted,
		second.publisher,
		second.publisherConflicted,
	)
	merged.printISSN, merged.printISSNConflicted = mergeOptionalCrossrefEvidence(
		first.printISSN,
		first.printISSNConflicted,
		second.printISSN,
		second.printISSNConflicted,
	)
	merged.electronicISSN, merged.electronicISSNConflicted = mergeOptionalCrossrefEvidence(
		first.electronicISSN,
		first.electronicISSNConflicted,
		second.electronicISSN,
		second.electronicISSNConflicted,
	)
	if first.totalDOIsConflicted ||
		second.totalDOIsConflicted ||
		first.totalDOIs != second.totalDOIs {
		merged.totalDOIs = 0
		merged.totalDOIsConflicted = true
	}
	return merged
}

func mergeOptionalCrossrefEvidence(
	first string,
	firstConflicted bool,
	second string,
	secondConflicted bool,
) (string, bool) {
	switch {
	case firstConflicted || secondConflicted:
		return "", true
	case first == "":
		return second, false
	case second == "":
		return first, false
	case first == second:
		return first, false
	default:
		return "", true
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
