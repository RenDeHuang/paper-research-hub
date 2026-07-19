package venueenrich

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var testSourceCSVHeader = []string{
	"domain",
	"source_order",
	"journal_name",
	"impact_factor",
	"jcr_value",
	"cass_value",
	"source_url",
	"verification_status",
}

func TestLoadSourceFilesAcceptsExactHeaderAndPreservesSourceFields(t *testing.T) {
	t.Parallel()

	path := filepath.Join("testdata", "source.csv")
	rows, err := LoadSourceFiles([]string{path})
	if err != nil {
		t.Fatalf("LoadSourceFiles(%q) error = %v", path, err)
	}
	if len(rows) != 2 {
		t.Fatalf("LoadSourceFiles(%q) row count = %d, want 2", path, len(rows))
	}

	first := rows[0]
	if first.Domain != "medicine" {
		t.Fatalf("first Domain = %q, want medicine", first.Domain)
	}
	if first.SourceOrder != 1 {
		t.Fatalf("first SourceOrder = %d, want 1", first.SourceOrder)
	}
	if first.SourceJournalName != "MiXeD: Journal—A  /  B\tC" {
		t.Fatalf(
			"first SourceJournalName = %q, want original case, punctuation, and internal whitespace",
			first.SourceJournalName,
		)
	}
	if first.ImpactFactor != "101.8" {
		t.Fatalf("first ImpactFactor = %q, want 101.8", first.ImpactFactor)
	}
	if first.JCRValue != "生物工程与应用微生物-1区\n药学-1区" {
		t.Fatalf("first JCRValue = %q, want preserved multiline value", first.JCRValue)
	}
	if first.CASSValue != "1区" {
		t.Fatalf("first CASSValue = %q, want 1区", first.CASSValue)
	}
	if first.SourceURL != "https://example.test/medicine/1" {
		t.Fatalf(
			"first SourceURL = %q, want https://example.test/medicine/1",
			first.SourceURL,
		)
	}

	second := rows[1]
	if second.Domain != "biology" || second.SourceOrder != 7 {
		t.Fatalf("second source key = (%q, %d), want (biology, 7)", second.Domain, second.SourceOrder)
	}
	if second.SourceJournalName != "NATURE Reviews: Cell.Biology" {
		t.Fatalf(
			"second SourceJournalName = %q, want exact source title",
			second.SourceJournalName,
		)
	}
}

func TestLoadSourceFilesRequiresExactHeader(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header []string
	}{
		{
			name:   "missing field",
			header: testSourceCSVHeader[:len(testSourceCSVHeader)-1],
		},
		{
			name: "extra field",
			header: append(
				append([]string(nil), testSourceCSVHeader...),
				"unexpected",
			),
		},
		{
			name: "reordered field",
			header: []string{
				"domain",
				"journal_name",
				"source_order",
				"impact_factor",
				"jcr_value",
				"cass_value",
				"source_url",
				"verification_status",
			},
		},
		{
			name: "trimmed lookalike",
			header: []string{
				" domain ",
				"source_order",
				"journal_name",
				"impact_factor",
				"jcr_value",
				"cass_value",
				"source_url",
				"verification_status",
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := writeSourceCSV(t, test.header, nil)
			err := loadSourceFilesError(t, path)
			assertSourceLoadError(t, err, path, 1)
			if !strings.Contains(err.Error(), "exact header") {
				t.Fatalf("LoadSourceFiles() error = %q, want exact header failure", err)
			}
		})
	}
}

func TestLoadSourceFilesRequiresNumericPositiveSourceOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "missing", value: "", want: "source_order"},
		{name: "not numeric", value: "first", want: "source_order"},
		{name: "decimal", value: "1.5", want: "source_order"},
		{name: "zero", value: "0", want: "source_order must be positive"},
		{name: "negative", value: "-1", want: "source_order must be positive"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			record := validSourceCSVRecord("medicine", test.value, "Journal One")
			path := writeSourceCSV(t, testSourceCSVHeader, [][]string{record})
			err := loadSourceFilesError(t, path)
			assertSourceLoadError(t, err, path, 2)
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadSourceFiles() error = %q, want %q", err, test.want)
			}
		})
	}
}

func TestLoadSourceFilesRejectsInvalidMissingAndExtraDataFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		record []string
		want   string
	}{
		{
			name:   "missing column",
			record: validSourceCSVRecord("medicine", "1", "Journal One")[:7],
			want:   "wrong number of fields",
		},
		{
			name: "extra column",
			record: append(
				validSourceCSVRecord("medicine", "1", "Journal One"),
				"unexpected",
			),
			want: "wrong number of fields",
		},
		{
			name:   "missing domain",
			record: validSourceCSVRecord("", "1", "Journal One"),
			want:   "domain",
		},
		{
			name:   "missing journal name",
			record: validSourceCSVRecord("medicine", "1", ""),
			want:   "source_journal_name",
		},
		{
			name: "missing source URL",
			record: func() []string {
				record := validSourceCSVRecord("medicine", "1", "Journal One")
				record[6] = ""
				return record
			}(),
			want: "source_url",
		},
		{
			name: "missing verification status",
			record: func() []string {
				record := validSourceCSVRecord("medicine", "1", "Journal One")
				record[7] = ""
				return record
			}(),
			want: "verification_status",
		},
		{
			name: "unsupported verification status",
			record: func() []string {
				record := validSourceCSVRecord("medicine", "1", "Journal One")
				record[7] = "verified"
				return record
			}(),
			want: "verification_status",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := writeSourceCSV(t, testSourceCSVHeader, [][]string{test.record})
			err := loadSourceFilesError(t, path)
			assertSourceLoadError(t, err, path, 2)
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadSourceFiles() error = %q, want %q", err, test.want)
			}
		})
	}
}

func TestLoadSourceFilesRejectsDuplicateDomainAndSourceOrder(t *testing.T) {
	t.Parallel()

	firstPath := writeSourceCSV(
		t,
		testSourceCSVHeader,
		[][]string{validSourceCSVRecord("medicine", "9", "Journal One")},
	)
	secondPath := writeSourceCSV(
		t,
		testSourceCSVHeader,
		[][]string{validSourceCSVRecord("medicine", "9", "Journal Two")},
	)

	_, err := LoadSourceFiles([]string{firstPath, secondPath})
	if err == nil {
		t.Fatal("LoadSourceFiles() error = nil, want duplicate source key failure")
	}
	assertSourceLoadError(t, err, secondPath, 2)
	if !strings.Contains(err.Error(), `duplicate source key ("medicine", 9)`) {
		t.Fatalf("LoadSourceFiles() error = %q, want duplicate source key", err)
	}
	if !strings.Contains(err.Error(), firstPath) {
		t.Fatalf("LoadSourceFiles() error = %q, want first occurrence path %q", err, firstPath)
	}
}

func TestLoadSourceFilesUsesLogicalDataRowAfterMultilineField(t *testing.T) {
	t.Parallel()

	first := validSourceCSVRecord("medicine", "1", "Journal One")
	first[4] = "Category A-1区\nCategory B-1区"
	second := validSourceCSVRecord("medicine", "not-a-number", "Journal Two")
	path := writeSourceCSV(t, testSourceCSVHeader, [][]string{first, second})

	err := loadSourceFilesError(t, path)
	assertSourceLoadError(t, err, path, 3)
}

func TestLoadSourceFilesConcatenatesAll2032RowsInInputFileOrder(t *testing.T) {
	t.Parallel()

	type source struct {
		domain string
		count  int
	}
	sources := []source{
		{domain: "medicine", count: 1546},
		{domain: "biology", count: 302},
		{domain: "computer_science", count: 184},
	}

	paths := make([]string, 0, len(sources))
	for _, source := range sources {
		records := make([][]string, 0, source.count)
		for sourceOrder := 1; sourceOrder <= source.count; sourceOrder++ {
			records = append(
				records,
				validSourceCSVRecord(
					source.domain,
					strconv.Itoa(sourceOrder),
					fmt.Sprintf("%s Journal %04d", source.domain, sourceOrder),
				),
			)
		}
		paths = append(paths, writeSourceCSV(t, testSourceCSVHeader, records))
	}

	rows, err := LoadSourceFiles(paths)
	if err != nil {
		t.Fatalf("LoadSourceFiles() error = %v", err)
	}
	if len(rows) != 2032 {
		t.Fatalf("LoadSourceFiles() row count = %d, want 2032", len(rows))
	}

	assertSourceKey := func(index int, domain string, sourceOrder int) {
		t.Helper()

		row := rows[index]
		if row.Domain != domain || row.SourceOrder != sourceOrder {
			t.Fatalf(
				"rows[%d] source key = (%q, %d), want (%q, %d)",
				index,
				row.Domain,
				row.SourceOrder,
				domain,
				sourceOrder,
			)
		}
	}
	assertSourceKey(0, "medicine", 1)
	assertSourceKey(1545, "medicine", 1546)
	assertSourceKey(1546, "biology", 1)
	assertSourceKey(1847, "biology", 302)
	assertSourceKey(1848, "computer_science", 1)
	assertSourceKey(2031, "computer_science", 184)
}

func writeSourceCSV(t *testing.T, header []string, records [][]string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "source.csv")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("os.Create(%q) error = %v", path, err)
	}

	writer := csv.NewWriter(file)
	if err := writer.Write(header); err != nil {
		_ = file.Close()
		t.Fatalf("csv.Writer.Write(header) error = %v", err)
	}
	if err := writer.WriteAll(records); err != nil {
		_ = file.Close()
		t.Fatalf("csv.Writer.WriteAll(records) error = %v", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		_ = file.Close()
		t.Fatalf("csv.Writer error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("file.Close() error = %v", err)
	}
	return path
}

func validSourceCSVRecord(domain, sourceOrder, journalName string) []string {
	return []string{
		domain,
		sourceOrder,
		journalName,
		"12.3",
		"Category-1区",
		"1区",
		"https://example.test/" + domain + "/" + sourceOrder,
		"pending_clarivate_verification",
	}
}

func loadSourceFilesError(t *testing.T, path string) error {
	t.Helper()

	_, err := LoadSourceFiles([]string{path})
	if err == nil {
		t.Fatal("LoadSourceFiles() error = nil, want failure")
	}
	return err
}

func assertSourceLoadError(t *testing.T, err error, path string, rowNumber int) {
	t.Helper()

	if !strings.Contains(err.Error(), path) {
		t.Fatalf("LoadSourceFiles() error = %q, want file path %q", err, path)
	}
	wantRow := fmt.Sprintf("row %d", rowNumber)
	if !strings.Contains(err.Error(), wantRow) {
		t.Fatalf("LoadSourceFiles() error = %q, want %q", err, wantRow)
	}
}
