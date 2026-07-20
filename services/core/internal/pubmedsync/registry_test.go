package pubmedsync

import (
	"bytes"
	"encoding/csv"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

var testRegistryHeader = []string{
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

func TestLoadRegistryRequiresExactHeaderAndRecordWidth(t *testing.T) {
	t.Parallel()

	valid := validRegistryRecord(
		"Alpha Journal",
		1,
		"1234-5679",
		"",
		"2049-3630",
		`["1234-5679","2049-3630"]`,
		"resolved",
		"yes",
		"7",
	)
	tests := []struct {
		name   string
		header []string
		record []string
	}{
		{
			name:   "missing column",
			header: testRegistryHeader[:len(testRegistryHeader)-1],
			record: valid[:len(valid)-1],
		},
		{
			name:   "added column",
			header: append(append([]string(nil), testRegistryHeader...), "extra"),
			record: append(append([]string(nil), valid...), "value"),
		},
		{
			name: "reordered columns",
			header: func() []string {
				header := append([]string(nil), testRegistryHeader...)
				header[0], header[1] = header[1], header[0]
				return header
			}(),
			record: func() []string {
				record := append([]string(nil), valid...)
				record[0], record[1] = record[1], record[0]
				return record
			}(),
		},
		{
			name: "UTF-8 BOM",
			header: func() []string {
				header := append([]string(nil), testRegistryHeader...)
				header[0] = "\ufeff" + header[0]
				return header
			}(),
			record: valid,
		},
		{
			name:   "short record",
			header: testRegistryHeader,
			record: valid[:len(valid)-1],
		},
		{
			name:   "wide record",
			header: testRegistryHeader,
			record: append(append([]string(nil), valid...), "extra"),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(tt.header, tt.record))); err == nil {
				t.Fatal("LoadRegistry() succeeded for malformed header/record width")
			}
		})
	}
}

func TestLoadRegistryReturnsOnlyResolvedPubMedSupportedRows(t *testing.T) {
	t.Parallel()

	records := [][]string{
		validRegistryRecord(
			"resolved yes",
			1,
			"1234-5679",
			"",
			"",
			`["1234-5679"]`,
			"resolved",
			"yes",
			"7",
		),
		validRegistryRecord(
			"resolved no",
			2,
			"2049-3630",
			"",
			"",
			`["2049-3630"]`,
			"resolved",
			"no",
			"0",
		),
		validRegistryRecord(
			"resolved unknown",
			3,
			"9876-5434",
			"",
			"",
			`["9876-5434"]`,
			"resolved",
			"unknown",
			"0",
		),
		validRegistryRecord(
			"ambiguous",
			4,
			"",
			"",
			"",
			"[]",
			"ambiguous",
			"unknown",
			"0",
		),
		validRegistryRecord(
			"unresolved",
			5,
			"",
			"",
			"",
			"[]",
			"unresolved",
			"unknown",
			"0",
		),
	}

	journals, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(testRegistryHeader, records...)))
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	if len(journals) != 1 {
		t.Fatalf("LoadRegistry() returned %d journals, want 1", len(journals))
	}
	if got := journals[0].SourceJournalName(); got != "resolved yes" {
		t.Fatalf("SourceJournalName() = %q, want %q", got, "resolved yes")
	}
}

func TestLoadResolvedRegistryReturnsEveryResolvedRowRegardlessOfPubMedSupport(t *testing.T) {
	t.Parallel()

	records := [][]string{
		validRegistryRecord(
			"resolved yes",
			1,
			"1234-5679",
			"",
			"",
			`["1234-5679"]`,
			"resolved",
			"yes",
			"7",
		),
		validRegistryRecord(
			"resolved no",
			2,
			"2049-3630",
			"",
			"",
			`["2049-3630"]`,
			"resolved",
			"no",
			"0",
		),
		validRegistryRecord(
			"resolved unknown",
			3,
			"9876-5434",
			"",
			"",
			`["9876-5434"]`,
			"resolved",
			"unknown",
			"0",
		),
		validRegistryRecord(
			"ambiguous",
			4,
			"",
			"",
			"",
			"[]",
			"ambiguous",
			"unknown",
			"0",
		),
		validRegistryRecord(
			"unresolved",
			5,
			"",
			"",
			"",
			"[]",
			"unresolved",
			"unknown",
			"0",
		),
	}

	journals, err := LoadResolvedRegistry(
		bytes.NewReader(encodeRegistryCSV(testRegistryHeader, records...)),
	)
	if err != nil {
		t.Fatalf("LoadResolvedRegistry() error = %v", err)
	}
	if len(journals) != 3 {
		t.Fatalf("LoadResolvedRegistry() returned %d journals, want 3", len(journals))
	}
	gotNames := []string{
		journals[0].SourceJournalName(),
		journals[1].SourceJournalName(),
		journals[2].SourceJournalName(),
	}
	wantNames := []string{"resolved yes", "resolved no", "resolved unknown"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("SourceJournalName() values = %q, want %q", gotNames, wantNames)
	}
}

func TestLoadResolvedRegistryPreservesStrictValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		records [][]string
	}{
		{
			name: "status and count contradiction",
			records: [][]string{
				validRegistryRecord(
					"contradictory",
					1,
					"1234-5679",
					"",
					"",
					`["1234-5679"]`,
					"resolved",
					"no",
					"1",
				),
			},
		},
		{
			name: "invalid ISSN",
			records: [][]string{
				validRegistryRecord(
					"invalid ISSN",
					1,
					"1234-5678",
					"",
					"",
					`["1234-5678"]`,
					"resolved",
					"unknown",
					"0",
				),
			},
		},
		{
			name: "ISSN identity conflict",
			records: [][]string{
				validRegistryRecord(
					"first",
					1,
					"1234-5679",
					"",
					"2049-3630",
					`["1234-5679","2049-3630"]`,
					"resolved",
					"no",
					"0",
				),
				validRegistryRecord(
					"second",
					2,
					"1234-5679",
					"",
					"9876-5434",
					`["1234-5679","9876-5434"]`,
					"resolved",
					"unknown",
					"0",
				),
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := LoadResolvedRegistry(
				bytes.NewReader(encodeRegistryCSV(testRegistryHeader, tt.records...)),
			); err == nil {
				t.Fatal("LoadResolvedRegistry() accepted invalid registry evidence")
			}
		})
	}
}

func TestLoadRegistryValidatesAllISSNsBeforeFilteringNonEligibleRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		resolution string
		supported  string
		count      string
		allISSNs   string
		wantError  bool
	}{
		{
			name:       "unresolved empty array is allowed",
			resolution: "unresolved",
			supported:  "unknown",
			count:      "0",
			allISSNs:   "[]",
		},
		{
			name:       "ambiguous empty array is allowed",
			resolution: "ambiguous",
			supported:  "unknown",
			count:      "0",
			allISSNs:   "[]",
		},
		{
			name:       "unresolved non-json",
			resolution: "unresolved",
			supported:  "unknown",
			count:      "0",
			allISSNs:   "not-json",
			wantError:  true,
		},
		{
			name:       "unresolved null",
			resolution: "unresolved",
			supported:  "unknown",
			count:      "0",
			allISSNs:   "null",
			wantError:  true,
		},
		{
			name:       "unresolved non-array",
			resolution: "unresolved",
			supported:  "unknown",
			count:      "0",
			allISSNs:   `{}`,
			wantError:  true,
		},
		{
			name:       "unresolved trailing json",
			resolution: "unresolved",
			supported:  "unknown",
			count:      "0",
			allISSNs:   `["1234-5679"] {}`,
			wantError:  true,
		},
		{
			name:       "unresolved invalid checksum",
			resolution: "unresolved",
			supported:  "unknown",
			count:      "0",
			allISSNs:   `["1234-5678"]`,
			wantError:  true,
		},
		{
			name:       "unresolved noncanonical case",
			resolution: "unresolved",
			supported:  "unknown",
			count:      "0",
			allISSNs:   `["3141-592x"]`,
			wantError:  true,
		},
		{
			name:       "resolved no requires non-empty array",
			resolution: "resolved",
			supported:  "no",
			count:      "0",
			allISSNs:   "[]",
			wantError:  true,
		},
		{
			name:       "resolved unknown requires non-empty array",
			resolution: "resolved",
			supported:  "unknown",
			count:      "0",
			allISSNs:   "[]",
			wantError:  true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			record := validRegistryRecord(
				tt.name,
				1,
				"",
				"",
				"",
				tt.allISSNs,
				tt.resolution,
				tt.supported,
				tt.count,
			)
			_, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(testRegistryHeader, record)))
			if tt.wantError && err == nil {
				t.Fatal("LoadRegistry() returned nil error for invalid all_issns")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("LoadRegistry() error = %v, want valid non-eligible row", err)
			}
		})
	}
}

func TestLoadRegistryRejectsPubMedNoForNonResolvedRows(t *testing.T) {
	t.Parallel()

	for _, resolution := range []string{"ambiguous", "unresolved"} {
		resolution := resolution
		t.Run(resolution, func(t *testing.T) {
			t.Parallel()

			record := validRegistryRecord(
				resolution,
				1,
				"",
				"",
				"",
				"[]",
				resolution,
				"no",
				"0",
			)
			if _, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(testRegistryHeader, record))); err == nil {
				t.Fatalf(
					"LoadRegistry() accepted %s + pubmed_supported=no + count=0",
					resolution,
				)
			}
		})
	}
}

func TestLoadRegistryValidatesResolvedISSNFieldsWhenPubMedIsNotYes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		issnL  string
		print  string
		eissn  string
		wantOK bool
	}{
		{
			name:   "canonical linking ISSN in all_issns",
			issnL:  "1234-5679",
			wantOK: true,
		},
		{
			name:  "linking ISSN absent from all_issns",
			issnL: "9876-5434",
		},
		{
			name:  "noncanonical electronic ISSN",
			eissn: "3141-592x",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			record := validRegistryRecord(
				tt.name,
				1,
				tt.issnL,
				tt.print,
				tt.eissn,
				`["1234-5679"]`,
				"resolved",
				"no",
				"0",
			)
			_, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(testRegistryHeader, record)))
			if tt.wantOK && err != nil {
				t.Fatalf("LoadRegistry() error = %v, want resolved non-eligible row", err)
			}
			if !tt.wantOK && err == nil {
				t.Fatal("LoadRegistry() accepted invalid resolved ISSN evidence")
			}
		})
	}
}

func TestLoadRegistryValidatesStrictISSNArrayAndNamedISSNs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		issnL string
		print string
		eissn string
		all   string
	}{
		{
			name:  "null array",
			issnL: "1234-5679",
			eissn: "2049-3630",
			all:   "null",
		},
		{
			name:  "empty array",
			issnL: "1234-5679",
			eissn: "2049-3630",
			all:   "[]",
		},
		{
			name:  "trailing JSON",
			issnL: "1234-5679",
			eissn: "2049-3630",
			all:   `["1234-5679","2049-3630"] {}`,
		},
		{
			name:  "non-string member",
			issnL: "1234-5679",
			eissn: "2049-3630",
			all:   `[1234]`,
		},
		{
			name:  "untrimmed member",
			issnL: "1234-5679",
			eissn: "2049-3630",
			all:   `["1234-5679"," 2049-3630"]`,
		},
		{
			name:  "not uppercase",
			issnL: "3141-592X",
			eissn: "",
			all:   `["3141-592x"]`,
		},
		{
			name:  "not sorted",
			issnL: "1234-5679",
			eissn: "2049-3630",
			all:   `["2049-3630","1234-5679"]`,
		},
		{
			name:  "duplicate",
			issnL: "1234-5679",
			eissn: "2049-3630",
			all:   `["1234-5679","1234-5679","2049-3630"]`,
		},
		{
			name:  "invalid checksum",
			issnL: "1234-5679",
			eissn: "",
			all:   `["1234-5678"]`,
		},
		{
			name:  "named ISSN absent from array",
			issnL: "9876-5434",
			eissn: "",
			all:   `["1234-5679"]`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			record := validRegistryRecord(
				tt.name,
				1,
				tt.issnL,
				tt.print,
				tt.eissn,
				tt.all,
				"resolved",
				"yes",
				"1",
			)
			if _, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(testRegistryHeader, record))); err == nil {
				t.Fatal("LoadRegistry() accepted invalid eligible ISSN evidence")
			}
		})
	}
}

func TestLoadRegistryRejectsStatusAndCountContradictions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		resolution string
		supported  string
		count      string
	}{
		{name: "PubMed yes with zero", resolution: "resolved", supported: "yes", count: "0"},
		{name: "PubMed no with positive", resolution: "resolved", supported: "no", count: "1"},
		{name: "PubMed unknown with positive", resolution: "resolved", supported: "unknown", count: "1"},
		{name: "non-resolved PubMed yes", resolution: "ambiguous", supported: "yes", count: "1"},
		{name: "negative count", resolution: "resolved", supported: "no", count: "-1"},
		{name: "invalid resolution status", resolution: "matched", supported: "unknown", count: "0"},
		{name: "invalid support status", resolution: "resolved", supported: "maybe", count: "0"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			record := validRegistryRecord(
				tt.name,
				1,
				"",
				"",
				"",
				"[]",
				tt.resolution,
				tt.supported,
				tt.count,
			)
			if _, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(testRegistryHeader, record))); err == nil {
				t.Fatal("LoadRegistry() accepted contradictory status/count evidence")
			}
		})
	}
}

func TestLoadRegistryFoldsExactISSNSetsWithoutTitleMatching(t *testing.T) {
	t.Parallel()

	records := [][]string{
		validRegistryRecord(
			"Same Title",
			1,
			"1234-5679",
			"",
			"2049-3630",
			`["1234-5679","2049-3630"]`,
			"resolved",
			"yes",
			"1",
		),
		validRegistryRecord(
			"Different Title",
			2,
			"1234-5679",
			"",
			"2049-3630",
			`["1234-5679","2049-3630"]`,
			"resolved",
			"yes",
			"2",
		),
		validRegistryRecord(
			"Same Title",
			3,
			"9876-5434",
			"",
			"",
			`["9876-5434"]`,
			"resolved",
			"yes",
			"3",
		),
	}

	journals, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(testRegistryHeader, records...)))
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}
	if len(journals) != 2 {
		t.Fatalf("LoadRegistry() returned %d journals, want exact-set fold to 2", len(journals))
	}
	if journals[0].SourceJournalName() != "Same Title" || journals[0].SourceOrder() != 1 {
		t.Fatalf("first folded journal = %#v, want first source row", journals[0])
	}
	if journals[1].SourceOrder() != 3 {
		t.Fatalf("second journal source order = %d, want 3", journals[1].SourceOrder())
	}
}

func TestLoadRegistryRejectsDistinctISSNSetsThatShareAnyISSN(t *testing.T) {
	t.Parallel()

	records := [][]string{
		validRegistryRecord(
			"First",
			1,
			"1234-5679",
			"",
			"2049-3630",
			`["1234-5679","2049-3630"]`,
			"resolved",
			"yes",
			"1",
		),
		validRegistryRecord(
			"Second",
			2,
			"1234-5679",
			"",
			"9876-5434",
			`["1234-5679","9876-5434"]`,
			"resolved",
			"yes",
			"1",
		),
	}

	if _, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(testRegistryHeader, records...))); err == nil ||
		!strings.Contains(strings.ToLower(err.Error()), "conflict") {
		t.Fatalf("LoadRegistry() error = %v, want explicit ISSN conflict", err)
	}
}

func TestJournalAccessorsDoNotExposeInternalISSNSlice(t *testing.T) {
	t.Parallel()

	record := validRegistryRecord(
		"Immutable Journal",
		7,
		"1234-5679",
		"",
		"2049-3630",
		`["1234-5679","2049-3630"]`,
		"resolved",
		"yes",
		"1",
	)
	journals, err := LoadRegistry(bytes.NewReader(encodeRegistryCSV(testRegistryHeader, record)))
	if err != nil {
		t.Fatalf("LoadRegistry() error = %v", err)
	}

	got := journals[0].ISSNs()
	got[0] = "9876-5434"
	if want := []string{"1234-5679", "2049-3630"}; !reflect.DeepEqual(journals[0].ISSNs(), want) {
		t.Fatalf("ISSNs() exposed mutable storage: got %v, want %v", journals[0].ISSNs(), want)
	}
}

func validRegistryRecord(
	title string,
	sourceOrder int,
	issnL string,
	printISSN string,
	eissn string,
	allISSNs string,
	resolutionStatus string,
	pubmedSupported string,
	pubmedRecordCount string,
) []string {
	return []string{
		"medicine",
		strconv.Itoa(sourceOrder),
		title,
		"10.0",
		"Q1",
		"1区",
		issnL,
		printISSN,
		eissn,
		allISSNs,
		"Publisher",
		resolutionStatus,
		pubmedSupported,
		pubmedRecordCount,
		"2026-07-19T00:00:00Z",
		"https://example.test/" + strings.ReplaceAll(title, " ", "-"),
		"pending_clarivate_verification",
	}
}

func encodeRegistryCSV(header []string, records ...[]string) []byte {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	_ = writer.Write(header)
	for _, record := range records {
		_ = writer.Write(record)
	}
	writer.Flush()
	return buffer.Bytes()
}
