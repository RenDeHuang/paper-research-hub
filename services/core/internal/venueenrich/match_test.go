package venueenrich

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestMatchCrossrefCatalogResolvesUniqueNormalizedTitleAndCanonicalizesISSNs(
	t *testing.T,
) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Journal of Exact Matching",
			"Deterministic Publisher",
			42,
			[]string{"2049-3630", "0028-0836", "2049-3630"},
			[]CrossrefJournalISSNType{
				{Value: "2049-3630", Type: "electronic"},
				{Value: "0028-0836", Type: "print"},
			},
		),
	)
	source := matchSourceRow(1, "ＪＯＵＲＮＡＬ　of\tExact Matching")

	rows, err := MatchCrossrefCatalog([]SourceRow{source}, catalog)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.SourceRow != source {
		t.Fatalf("SourceRow = %#v, want preserved %#v", row.SourceRow, source)
	}
	if row.MatchStatus != MatchStatusResolved ||
		row.MatchMethod != MatchMethodCrossrefExactNormalizedTitle ||
		row.CrossrefSupported != SupportStatusYes ||
		row.OpenAlexSupported != SupportStatusUnknown ||
		row.PubMedSupported != SupportStatusUnknown {
		t.Fatalf("match/support state = %#v", row)
	}
	if row.CrossrefTitle != "Journal of Exact Matching" ||
		row.CrossrefPublisher != "Deterministic Publisher" ||
		row.CrossrefTotalDOIs != 42 {
		t.Fatalf("Crossref evidence = %#v", row)
	}
	if row.ISSNL != "" ||
		row.PrintISSN != "0028-0836" ||
		row.EISSN != "2049-3630" ||
		!slices.Equal(row.AllISSNs, []string{"0028-0836", "2049-3630"}) {
		t.Fatalf("ISSN identity = %#v", row)
	}
	if err := row.Validate(); err != nil {
		t.Fatalf("resolved RegistryRow.Validate() error = %v", err)
	}
}

func TestMatchCrossrefCatalogCollapsesDuplicateRowsWithTheSameIdentity(t *testing.T) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Duplicate Identity Journal",
			"Stable Publisher",
			19,
			[]string{"2049-3630", "0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "2049-3630", Type: "electronic"},
				{Value: "0028-0836", Type: "print"},
			},
		),
		matchCatalogRecord(
			t,
			"Duplicate Identity Journal",
			"Stable Publisher",
			19,
			[]string{"0028-0836", "2049-3630", "0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
				{Value: "2049-3630", Type: "electronic"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "duplicate identity journal")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 || rows[0].MatchStatus != MatchStatusResolved {
		t.Fatalf("rows = %#v, want one resolved identity", rows)
	}
	if !slices.Equal(rows[0].AllISSNs, []string{"0028-0836", "2049-3630"}) {
		t.Fatalf("AllISSNs = %v, want canonical sorted set", rows[0].AllISSNs)
	}
}

func TestMatchCrossrefCatalogMarksDifferentISSNSetsAmbiguousWithoutEvidence(
	t *testing.T,
) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Shared Journal Title",
			"First Publisher",
			10,
			[]string{"0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
			},
		),
		matchCatalogRecord(
			t,
			"SHARED JOURNAL TITLE",
			"Second Publisher",
			20,
			[]string{"2049-3630"},
			[]CrossrefJournalISSNType{
				{Value: "2049-3630", Type: "electronic"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "Shared Journal Title")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 || rows[0].MatchStatus != MatchStatusAmbiguous {
		t.Fatalf("rows = %#v, want one ambiguous row", rows)
	}
	assertMatchHasNoCrossrefEvidence(t, rows[0])
	if err := rows[0].Validate(); err != nil {
		t.Fatalf("ambiguous RegistryRow.Validate() error = %v", err)
	}
}

func TestMatchCrossrefCatalogLeavesPunctuationDifferencesAndMissingTitlesUnresolved(
	t *testing.T,
) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Journal Medicine",
			"Publisher",
			3,
			[]string{"0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
			},
		),
	)
	sources := []SourceRow{
		matchSourceRow(1, "Journal: Medicine"),
		matchSourceRow(2, "Absent Journal"),
	}

	rows, err := MatchCrossrefCatalog(sources, catalog)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != len(sources) {
		t.Fatalf("rows = %d, want %d", len(rows), len(sources))
	}
	for index, row := range rows {
		if row.MatchStatus != MatchStatusUnresolved {
			t.Fatalf("rows[%d].MatchStatus = %q, want unresolved", index, row.MatchStatus)
		}
		assertMatchHasNoCrossrefEvidence(t, row)
		if err := row.Validate(); err != nil {
			t.Fatalf("rows[%d].Validate() error = %v", index, err)
		}
	}
}

func TestMatchCrossrefCatalogPreservesInputOrder(t *testing.T) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"First Catalog Journal",
			"Publisher A",
			1,
			[]string{"0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
			},
		),
		matchCatalogRecord(
			t,
			"Second Catalog Journal",
			"Publisher B",
			2,
			[]string{"2049-3630"},
			[]CrossrefJournalISSNType{
				{Value: "2049-3630", Type: "electronic"},
			},
		),
	)
	sources := []SourceRow{
		matchSourceRow(31, "Second Catalog Journal"),
		matchSourceRow(7, "Missing Journal"),
		matchSourceRow(19, "First Catalog Journal"),
	}

	rows, err := MatchCrossrefCatalog(sources, catalog)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != len(sources) {
		t.Fatalf("rows = %d, want %d", len(rows), len(sources))
	}
	for index := range sources {
		if rows[index].SourceRow != sources[index] {
			t.Fatalf(
				"rows[%d].SourceRow = %#v, want %#v",
				index,
				rows[index].SourceRow,
				sources[index],
			)
		}
	}
	if got := []MatchStatus{
		rows[0].MatchStatus,
		rows[1].MatchStatus,
		rows[2].MatchStatus,
	}; !slices.Equal(got, []MatchStatus{
		MatchStatusResolved,
		MatchStatusUnresolved,
		MatchStatusResolved,
	}) {
		t.Fatalf("match statuses = %v", got)
	}
}

func TestMatchCrossrefCatalogRejectsInvalidCatalogRecordsWithLocation(t *testing.T) {
	t.Parallel()

	valid := matchCatalogRecord(
		t,
		"Valid Journal",
		"Publisher",
		1,
		[]string{"0028-0836"},
		[]CrossrefJournalISSNType{
			{Value: "0028-0836", Type: "print"},
		},
	)
	tests := []struct {
		name    string
		record  []byte
		want    string
		recordN int
	}{
		{
			name:    "malformed JSON",
			record:  []byte(`{"title":`),
			want:    "JSON",
			recordN: 2,
		},
		{
			name: "invalid UTF-8",
			record: append(
				[]byte(`{"title":"`),
				0xff,
				'"', '}',
			),
			want:    "UTF-8",
			recordN: 2,
		},
		{
			name: "empty title",
			record: matchCatalogRecord(
				t,
				" \t ",
				"Publisher",
				1,
				[]string{"0028-0836"},
				[]CrossrefJournalISSNType{
					{Value: "0028-0836", Type: "print"},
				},
			),
			want:    "title",
			recordN: 2,
		},
		{
			name: "negative DOI count",
			record: matchCatalogRecord(
				t,
				"Negative DOI Journal",
				"Publisher",
				-1,
				[]string{"0028-0836"},
				[]CrossrefJournalISSNType{
					{Value: "0028-0836", Type: "print"},
				},
			),
			want:    "negative",
			recordN: 2,
		},
		{
			name: "invalid ISSN checksum",
			record: matchCatalogRecord(
				t,
				"Bad Checksum Journal",
				"Publisher",
				1,
				[]string{"1234-5678"},
				[]CrossrefJournalISSNType{
					{Value: "1234-5678", Type: "print"},
				},
			),
			want:    "checksum",
			recordN: 2,
		},
		{
			name: "non-canonical ISSN",
			record: matchCatalogRecord(
				t,
				"Noncanonical Journal",
				"Publisher",
				1,
				[]string{"3141-592x"},
				[]CrossrefJournalISSNType{
					{Value: "3141-592x", Type: "electronic"},
				},
			),
			want:    "invalid",
			recordN: 2,
		},
		{
			name: "empty ISSN set",
			record: matchCatalogRecord(
				t,
				"Empty Identity Journal",
				"Publisher",
				1,
				[]string{},
				[]CrossrefJournalISSNType{},
			),
			want:    "at least one ISSN",
			recordN: 2,
		},
		{
			name: "conflicting print roles",
			record: matchCatalogRecord(
				t,
				"Role Conflict Journal",
				"Publisher",
				1,
				[]string{"0028-0836", "2049-3630"},
				[]CrossrefJournalISSNType{
					{Value: "0028-0836", Type: "print"},
					{Value: "2049-3630", Type: "print"},
				},
			),
			want:    "print",
			recordN: 2,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			catalog := writeMatchCatalog(t, valid, test.record)
			_, err := MatchCrossrefCatalog(
				[]SourceRow{matchSourceRow(1, "Valid Journal")},
				catalog,
			)
			if err == nil {
				t.Fatal("MatchCrossrefCatalog() error = nil, want strict catalog error")
			}
			for _, want := range []string{
				filepath.Base(catalog.CatalogPath),
				"record " + strconv.Itoa(test.recordN),
				test.want,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf(
						"MatchCrossrefCatalog() error = %q, want %q",
						err,
						want,
					)
				}
			}
		})
	}
}

func TestMatchCrossrefCatalogRejectsOversizedAndUnterminatedRecords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{
			name: "oversized record",
			payload: append(
				bytes.Repeat([]byte{'x'}, (8<<20)+1),
				'\n',
			),
			want: "exceeds",
		},
		{
			name:    "missing final newline",
			payload: []byte(`{"title":"truncated"}`),
			want:    "newline",
		},
		{
			name:    "empty record",
			payload: []byte("\n"),
			want:    "record",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), CrossrefCatalogJSONLName)
			if err := os.WriteFile(path, test.payload, 0o600); err != nil {
				t.Fatalf("WriteFile(%q) error = %v", path, err)
			}
			_, err := MatchCrossrefCatalog(
				[]SourceRow{matchSourceRow(1, "Any Journal")},
				CrossrefCatalog{CatalogPath: path},
			)
			if err == nil ||
				!strings.Contains(err.Error(), filepath.Base(path)) ||
				!strings.Contains(err.Error(), "record 1") ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"MatchCrossrefCatalog() error = %v, want located %q error",
					err,
					test.want,
				)
			}
		})
	}
}

func TestMatchCrossrefCatalogRejectsConflictingDuplicateEvidence(t *testing.T) {
	t.Parallel()

	type evidence struct {
		title     string
		publisher string
		totalDOIs int64
		roles     []CrossrefJournalISSNType
	}
	base := evidence{
		title:     "Conflict Journal",
		publisher: "Stable Publisher",
		totalDOIs: 9,
		roles: []CrossrefJournalISSNType{
			{Value: "0028-0836", Type: "print"},
			{Value: "2049-3630", Type: "electronic"},
		},
	}
	tests := []struct {
		name   string
		second evidence
		want   string
	}{
		{
			name: "title",
			second: evidence{
				title:     "CONFLICT JOURNAL",
				publisher: base.publisher,
				totalDOIs: base.totalDOIs,
				roles:     base.roles,
			},
			want: "title",
		},
		{
			name: "publisher",
			second: evidence{
				title:     base.title,
				publisher: "Changed Publisher",
				totalDOIs: base.totalDOIs,
				roles:     base.roles,
			},
			want: "publisher",
		},
		{
			name: "DOI count",
			second: evidence{
				title:     base.title,
				publisher: base.publisher,
				totalDOIs: 10,
				roles:     base.roles,
			},
			want: "DOI",
		},
		{
			name: "ISSN roles",
			second: evidence{
				title:     base.title,
				publisher: base.publisher,
				totalDOIs: base.totalDOIs,
				roles: []CrossrefJournalISSNType{
					{Value: "2049-3630", Type: "print"},
					{Value: "0028-0836", Type: "electronic"},
				},
			},
			want: "role",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			catalog := writeMatchCatalog(t,
				matchCatalogRecord(
					t,
					base.title,
					base.publisher,
					base.totalDOIs,
					[]string{"0028-0836", "2049-3630"},
					base.roles,
				),
				matchCatalogRecord(
					t,
					test.second.title,
					test.second.publisher,
					test.second.totalDOIs,
					[]string{"2049-3630", "0028-0836"},
					test.second.roles,
				),
			)
			_, err := MatchCrossrefCatalog(
				[]SourceRow{matchSourceRow(1, base.title)},
				catalog,
			)
			if err == nil ||
				!strings.Contains(err.Error(), "record 1") ||
				!strings.Contains(err.Error(), "record 2") ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"MatchCrossrefCatalog() error = %v, want located %s conflict",
					err,
					test.want,
				)
			}
		})
	}
}

func TestMatchCrossrefCatalogRejectsInvalidSourceRowsBeforeReadingCatalog(t *testing.T) {
	t.Parallel()

	source := matchSourceRow(1, "Valid Journal")
	source.SourceJournalName = string([]byte{'J', 0xff})
	_, err := MatchCrossrefCatalog(
		[]SourceRow{source},
		CrossrefCatalog{CatalogPath: filepath.Join(t.TempDir(), "missing.jsonl")},
	)
	if err == nil ||
		!strings.Contains(err.Error(), "source row 1") ||
		!strings.Contains(err.Error(), "UTF-8") ||
		strings.Contains(err.Error(), "open") {
		t.Fatalf(
			"MatchCrossrefCatalog() error = %v, want source UTF-8 error before catalog open",
			err,
		)
	}
}

func assertMatchHasNoCrossrefEvidence(t *testing.T, row RegistryRow) {
	t.Helper()

	if row.ISSNL != "" ||
		row.PrintISSN != "" ||
		row.EISSN != "" ||
		len(row.AllISSNs) != 0 ||
		row.CrossrefTitle != "" ||
		row.CrossrefPublisher != "" ||
		row.CrossrefTotalDOIs != 0 ||
		row.CrossrefSupported != SupportStatusUnknown ||
		row.OpenAlexSupported != SupportStatusUnknown ||
		row.PubMedSupported != SupportStatusUnknown {
		t.Fatalf("row contains unsupported evidence: %#v", row)
	}
}

func matchSourceRow(order int, title string) SourceRow {
	return SourceRow{
		Domain:            "test-domain",
		SourceOrder:       order,
		SourceJournalName: title,
		SourceURL:         "https://example.test/journals",
	}
}

func writeMatchCatalog(t *testing.T, records ...[]byte) CrossrefCatalog {
	t.Helper()

	path := filepath.Join(t.TempDir(), CrossrefCatalogJSONLName)
	payload := bytes.Join(records, []byte{'\n'})
	if len(records) > 0 {
		payload = append(payload, '\n')
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return CrossrefCatalog{CatalogPath: path}
}

func matchCatalogRecord(
	t *testing.T,
	title string,
	publisher string,
	totalDOIs int64,
	issns []string,
	issnTypes []CrossrefJournalISSNType,
) []byte {
	t.Helper()

	record := struct {
		Title     string                    `json:"title"`
		ISSNs     []string                  `json:"ISSN"`
		ISSNTypes []CrossrefJournalISSNType `json:"issn-type"`
		Publisher string                    `json:"publisher"`
		Counts    struct {
			TotalDOIs int64 `json:"total-dois"`
		} `json:"counts"`
	}{
		Title:     title,
		ISSNs:     issns,
		ISSNTypes: issnTypes,
		Publisher: publisher,
	}
	record.Counts.TotalDOIs = totalDOIs
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal(Crossref record) error = %v", err)
	}
	return payload
}
