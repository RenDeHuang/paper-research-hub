package pubmedsync

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venue"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/venueenrich"
)

var registryHeader = []string{
	"domain",
	"source_order",
	"source_journal_name",
	"impact_factor",
	"jcr_value",
	"cass_value",
	"issn_l",
	"print_issn",
	"eissn",
	"all_issns",
	"crossref_publisher",
	"resolution_status",
	"pubmed_supported",
	"pubmed_record_count",
	"pubmed_checked_at",
	"source_url",
	"verification_status",
}

type Journal struct {
	key               string
	issns             []string
	domain            string
	sourceOrder       int
	sourceJournalName string
}

func (journal Journal) Key() string {
	return journal.key
}

func (journal Journal) ISSNs() []string {
	return slices.Clone(journal.issns)
}

func (journal Journal) Domain() string {
	return journal.domain
}

func (journal Journal) SourceOrder() int {
	return journal.sourceOrder
}

func (journal Journal) SourceJournalName() string {
	return journal.sourceJournalName
}

func (journal Journal) clone() Journal {
	journal.issns = slices.Clone(journal.issns)
	return journal
}

func LoadRegistry(source io.Reader) ([]Journal, error) {
	if source == nil {
		return nil, errors.New("registry CSV reader is required")
	}

	reader := csv.NewReader(source)
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = false

	header, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("registry CSV is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("read registry CSV header: %w", err)
	}
	if err := validateRegistryRecordUTF8(1, header); err != nil {
		return nil, err
	}
	if !slices.Equal(header, registryHeader) {
		return nil, fmt.Errorf(
			"registry CSV exact header mismatch: expected %q, actual %q",
			strings.Join(registryHeader, ","),
			strings.Join(header, ","),
		)
	}

	journals := make([]Journal, 0)
	seenKeys := make(map[string]struct{})
	for rowNumber := 2; ; rowNumber++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read registry CSV row %d: %w", rowNumber, err)
		}
		if err := validateRegistryRecordUTF8(rowNumber, record); err != nil {
			return nil, err
		}
		if len(record) != len(registryHeader) {
			return nil, fmt.Errorf(
				"registry CSV row %d has %d fields, want %d",
				rowNumber,
				len(record),
				len(registryHeader),
			)
		}

		journal, eligible, err := parseRegistryRecord(rowNumber, record)
		if err != nil {
			return nil, err
		}
		if !eligible {
			continue
		}

		if _, ok := seenKeys[journal.key]; ok {
			continue
		}
		for index := range journals {
			if issnSetsIntersect(journals[index].issns, journal.issns) {
				return nil, fmt.Errorf(
					"journal ISSN identity conflict between %q and %q",
					journals[index].key,
					journal.key,
				)
			}
		}
		seenKeys[journal.key] = struct{}{}
		journals = append(journals, journal)
	}
	return journals, nil
}

func parseRegistryRecord(
	rowNumber int,
	record []string,
) (Journal, bool, error) {
	sourceOrder, err := strconv.Atoi(record[1])
	if err != nil || sourceOrder <= 0 {
		return Journal{}, false, fmt.Errorf(
			"registry CSV row %d source_order %q must be a positive integer",
			rowNumber,
			record[1],
		)
	}

	resolutionStatus := venueenrich.MatchStatus(record[11])
	if !resolutionStatus.Valid() {
		return Journal{}, false, fmt.Errorf(
			"registry CSV row %d invalid resolution_status %q",
			rowNumber,
			record[11],
		)
	}
	pubmedSupported := venueenrich.SupportStatus(record[12])
	if !pubmedSupported.Valid() {
		return Journal{}, false, fmt.Errorf(
			"registry CSV row %d invalid pubmed_supported %q",
			rowNumber,
			record[12],
		)
	}
	pubmedRecordCount, err := strconv.ParseInt(record[13], 10, 64)
	if err != nil {
		return Journal{}, false, fmt.Errorf(
			"registry CSV row %d pubmed_record_count %q must be an integer",
			rowNumber,
			record[13],
		)
	}
	if pubmedRecordCount < 0 {
		return Journal{}, false, fmt.Errorf(
			"registry CSV row %d pubmed_record_count must not be negative",
			rowNumber,
		)
	}
	switch pubmedSupported {
	case venueenrich.SupportStatusYes:
		if pubmedRecordCount == 0 {
			return Journal{}, false, fmt.Errorf(
				"registry CSV row %d pubmed_supported yes requires positive pubmed_record_count",
				rowNumber,
			)
		}
	case venueenrich.SupportStatusNo, venueenrich.SupportStatusUnknown:
		if pubmedRecordCount != 0 {
			return Journal{}, false, fmt.Errorf(
				"registry CSV row %d pubmed_supported %s requires zero pubmed_record_count",
				rowNumber,
				pubmedSupported,
			)
		}
	}
	if resolutionStatus != venueenrich.MatchStatusResolved &&
		pubmedSupported == venueenrich.SupportStatusYes {
		return Journal{}, false, fmt.Errorf(
			"registry CSV row %d non-resolved resolution_status %q cannot have pubmed_supported yes",
			rowNumber,
			resolutionStatus,
		)
	}

	issns, err := parseStrictISSNArrayWithEmpty(
		record[9],
		resolutionStatus != venueenrich.MatchStatusResolved,
	)
	if err != nil {
		return Journal{}, false, fmt.Errorf(
			"registry CSV row %d all_issns: %w",
			rowNumber,
			err,
		)
	}
	issnSet := make(map[string]struct{}, len(issns))
	for _, issn := range issns {
		issnSet[issn] = struct{}{}
	}
	if err := validateRegistryISSNFields(rowNumber, record, issnSet); err != nil {
		return Journal{}, false, err
	}

	if resolutionStatus != venueenrich.MatchStatusResolved ||
		pubmedSupported != venueenrich.SupportStatusYes {
		return Journal{}, false, nil
	}

	return Journal{
		key:               journalIdentityKey(issns),
		issns:             slices.Clone(issns),
		domain:            record[0],
		sourceOrder:       sourceOrder,
		sourceJournalName: record[2],
	}, true, nil
}

func parseStrictISSNArray(raw string) ([]string, error) {
	return parseStrictISSNArrayWithEmpty(raw, false)
}

func parseStrictISSNArrayWithEmpty(raw string, allowEmpty bool) ([]string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	var values []string
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("must be a JSON string array: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("must not contain trailing JSON")
		}
		return nil, fmt.Errorf("must not contain trailing JSON: %w", err)
	}
	if values == nil {
		return nil, errors.New("must be a JSON string array")
	}
	if len(values) == 0 && !allowEmpty {
		return nil, errors.New("must contain at least one ISSN")
	}

	for index, rawISSN := range values {
		if rawISSN == "" || rawISSN != strings.TrimSpace(rawISSN) {
			return nil, fmt.Errorf("member %d must be trimmed", index)
		}
		if rawISSN != strings.ToUpper(rawISSN) {
			return nil, fmt.Errorf("member %d must be uppercase", index)
		}
		parsed, err := venue.ParseISSN(venue.ISSNRoleLinking, rawISSN)
		if err != nil {
			return nil, fmt.Errorf("member %d: %w", index, err)
		}
		if parsed.String() != rawISSN {
			return nil, fmt.Errorf(
				"member %d must use canonical ISSN %q",
				index,
				parsed.String(),
			)
		}
		if index > 0 && values[index-1] >= rawISSN {
			return nil, errors.New("members must be strictly ascending without duplicates")
		}
	}
	return values, nil
}

func validateRegistryISSNFields(
	rowNumber int,
	record []string,
	issnSet map[string]struct{},
) error {
	for _, field := range []struct {
		name string
		role venue.ISSNRole
		raw  string
	}{
		{name: "issn_l", role: venue.ISSNRoleLinking, raw: record[6]},
		{name: "print_issn", role: venue.ISSNRolePrint, raw: record[7]},
		{name: "eissn", role: venue.ISSNRoleElectronic, raw: record[8]},
	} {
		if field.raw == "" {
			continue
		}
		parsed, err := venue.ParseISSN(field.role, field.raw)
		if err != nil {
			return fmt.Errorf(
				"registry CSV row %d %s: %w",
				rowNumber,
				field.name,
				err,
			)
		}
		if parsed.String() != field.raw {
			return fmt.Errorf(
				"registry CSV row %d %s must use canonical ISSN %q",
				rowNumber,
				field.name,
				parsed.String(),
			)
		}
		if _, ok := issnSet[field.raw]; !ok {
			return fmt.Errorf(
				"registry CSV row %d %s %q is absent from all_issns",
				rowNumber,
				field.name,
				field.raw,
			)
		}
	}
	return nil
}

func validateRegistryRecordUTF8(rowNumber int, record []string) error {
	for index, field := range record {
		if utf8.ValidString(field) {
			continue
		}
		return fmt.Errorf(
			"registry CSV row %d field %d contains invalid UTF-8",
			rowNumber,
			index,
		)
	}
	return nil
}

func journalIdentityKey(issns []string) string {
	return "issn:" + strings.Join(issns, ",")
}

func issnSetsIntersect(left, right []string) bool {
	seen := make(map[string]struct{}, len(left))
	for _, issn := range left {
		seen[issn] = struct{}{}
	}
	for _, issn := range right {
		if _, ok := seen[issn]; ok {
			return true
		}
	}
	return false
}
