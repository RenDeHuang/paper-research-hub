package venue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDecimalComparisonIsExactAtJIFThreshold(t *testing.T) {
	t.Parallel()

	zero := mustParseDecimal(t, "0.000")
	below := mustParseDecimal(t, "9.999999999999999999")
	threshold := mustParseDecimal(t, "10")
	equal := mustParseDecimal(t, "10.000000000000000000")
	above := mustParseDecimal(t, "10.000000000000000001")

	if !zero.Valid() {
		t.Fatal("Decimal zero is not valid")
	}
	if zero.String() != "0" {
		t.Fatalf("zero.String() = %q, want %q", zero.String(), "0")
	}
	if zero.Cmp(below) >= 0 {
		t.Fatalf("%s compares at or above %s", zero, below)
	}
	if below.Cmp(threshold) >= 0 {
		t.Fatalf("%s compares at or above %s", below, threshold)
	}
	if equal.Cmp(threshold) != 0 {
		t.Fatalf("%s does not compare equal to %s", equal, threshold)
	}
	if above.Cmp(threshold) <= 0 {
		t.Fatalf("%s compares at or below %s", above, threshold)
	}
	if equal.String() != "10" {
		t.Fatalf("Decimal.String() = %q, want canonical %q", equal.String(), "10")
	}
}

func TestParseDecimalRejectsNonDecimalOrNegativeValues(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"-0.1",
		"+10",
		"1e1",
		"NaN",
		"Inf",
		".5",
		"10.",
		"1 0",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := ParseDecimal(raw); err == nil {
				t.Fatalf("ParseDecimal(%q) = %s, want error", raw, got)
			}
		})
	}
}

func TestJCRImporterPreservesCategoriesUnknownRowsAndImportMetadata(t *testing.T) {
	t.Parallel()

	alpha := venueForTest(
		t,
		"venue-alpha",
		"Synthetic Journal of Exact Systems",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	unknown := venueForTest(
		t,
		"venue-unknown",
		"Synthetic Journal of Unknown Metrics",
		"9876-5434",
		"9876-5434",
		"1357-2466",
	)
	repository := &fakeJCRRepository{venues: []Venue{alpha, unknown}}
	sink := &fakeJCRSink{}
	importedAt := time.Date(2026, time.July, 16, 12, 30, 0, 0, time.UTC)
	importer, err := NewJCRImporter(repository, sink, func() time.Time { return importedAt })
	if err != nil {
		t.Fatalf("NewJCRImporter() error = %v", err)
	}
	input := validJCRHeader +
		"Synthetic Journal of Exact Systems,1234-5679,1234-5679,2049-3630,2025,Artificial Intelligence,12.500,Q1,known,synthetic-jcr\n" +
		"Synthetic Journal of Exact Systems,1234-5679,1234-5679,2049-3630,2025,Robotics,12.5,Q2,known,synthetic-jcr\n" +
		"Synthetic Journal of Unknown Metrics,9876-5434,9876-5434,1357-2466,2025,Interdisciplinary Applications,,,unknown,synthetic-jcr\n"

	result, err := importer.Import(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	digest := sha256.Sum256([]byte(input))
	wantSHA := hex.EncodeToString(digest[:])
	if result.FileSHA256() != wantSHA {
		t.Fatalf("ImportResult.FileSHA256() = %q, want %q", result.FileSHA256(), wantSHA)
	}
	if result.Source() != "synthetic-jcr" {
		t.Fatalf("ImportResult.Source() = %q, want %q", result.Source(), "synthetic-jcr")
	}
	if !result.ImportedAt().Equal(importedAt) {
		t.Fatalf("ImportResult.ImportedAt() = %v, want %v", result.ImportedAt(), importedAt)
	}
	if result.InputRows() != 3 || result.InsertedRows() != 3 || result.UnchangedRows() != 0 {
		t.Fatalf(
			"ImportResult counts = input %d inserted %d unchanged %d, want 3/3/0",
			result.InputRows(),
			result.InsertedRows(),
			result.UnchangedRows(),
		)
	}
	if sink.calls != 1 {
		t.Fatalf("JCRSink calls = %d, want 1", sink.calls)
	}

	rows := sink.batch.Rows()
	if len(rows) != 3 {
		t.Fatalf("persisted metric rows = %d, want 3", len(rows))
	}
	if rows[0].VenueID() != alpha.ID() ||
		rows[0].Category() != "Artificial Intelligence" ||
		rows[0].JIF().String() != "12.5" ||
		rows[0].Quartile() != QuartileQ1 ||
		rows[0].Status() != MetricStatusKnown ||
		rows[0].Source() != "synthetic-jcr" {
		t.Fatalf("first metric row = %#v, want normalized known Q1 evidence", rows[0])
	}
	if rows[1].Category() != "Robotics" {
		t.Fatalf("second category = %q, want preserved multi-category row", rows[1].Category())
	}
	if rows[2].Status() != MetricStatusUnknown ||
		rows[2].HasJIF() ||
		rows[2].Quartile() != "" {
		t.Fatalf("unknown metric row = %#v, want no invented JIF or quartile", rows[2])
	}

	aliases := sink.batch.Aliases()
	if len(aliases) != 2 {
		t.Fatalf("persisted aliases = %d, want one deduplicated title alias per venue", len(aliases))
	}
	for _, evidence := range aliases {
		if evidence.Alias().Source() != "synthetic-jcr" {
			t.Fatalf("alias source = %q, want CSV source", evidence.Alias().Source())
		}
	}
}

func TestJCRImporterMatchesOnlyExactISSNs(t *testing.T) {
	t.Parallel()

	existing := venueForTest(
		t,
		"venue-existing",
		"Same Synthetic Title",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	repository := &fakeJCRRepository{venues: []Venue{existing}}
	sink := &fakeJCRSink{}
	importer := mustNewJCRImporter(t, repository, sink)
	input := validJCRHeader +
		"Same Synthetic Title,9876-5434,9876-5434,1357-2466,2025,Artificial Intelligence,12.5,Q1,known,synthetic-jcr\n"

	_, err := importer.Import(context.Background(), strings.NewReader(input))
	if !errors.Is(err, ErrNoVenueMatch) {
		t.Fatalf("Import() error = %v, want ErrNoVenueMatch", err)
	}
	if sink.calls != 0 {
		t.Fatalf("JCRSink calls = %d after title-only candidate, want 0", sink.calls)
	}
}

func TestJCRImporterRejectsAmbiguousExactISSNs(t *testing.T) {
	t.Parallel()

	first := venueForTest(
		t,
		"venue-first",
		"First Synthetic Venue",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	second := venueForTest(
		t,
		"venue-second",
		"Second Synthetic Venue",
		"9876-5434",
		"9876-5434",
		"1357-2466",
	)
	repository := &fakeJCRRepository{venues: []Venue{first, second}}
	sink := &fakeJCRSink{}
	importer := mustNewJCRImporter(t, repository, sink)
	input := validJCRHeader +
		"Untrusted CSV Title,1234-5679,9876-5434,1357-2466,2025,Artificial Intelligence,12.5,Q1,known,synthetic-jcr\n"

	_, err := importer.Import(context.Background(), strings.NewReader(input))
	if !errors.Is(err, ErrMultipleVenueMatches) {
		t.Fatalf("Import() error = %v, want ErrMultipleVenueMatches", err)
	}
	if sink.calls != 0 {
		t.Fatalf("JCRSink calls = %d after ambiguous match, want 0", sink.calls)
	}
}

func TestJCRImporterRequiresStrictSchemaAndConsistentRows(t *testing.T) {
	t.Parallel()

	validRow := "Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,Artificial Intelligence,12.5,Q1,known,synthetic-jcr\n"
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "missing source header",
			input: strings.Replace(validJCRHeader, ",source\n", "\n", 1) + strings.TrimSuffix(validRow, ",synthetic-jcr\n") + "\n",
			want:  "required CSV column",
		},
		{
			name: "citedness cannot replace jif",
			input: "title,issn_l,issn,eissn,metric_year,category,openalex_2yr_mean_citedness,quartile,status,source\n" +
				validRow,
			want: "required CSV column \"jif\"",
		},
		{
			name:  "inconsistent row width",
			input: validJCRHeader + strings.TrimSuffix(validRow, ",synthetic-jcr\n") + "\n",
			want:  "CSV row",
		},
		{
			name: "duplicate header",
			input: "title,issn_l,issn,eissn,metric_year,category,jif,jif,quartile,status,source\n" +
				"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.5,12.5,Q1,known,synthetic-jcr\n",
			want: "duplicate CSV column",
		},
		{
			name: "known row missing jif",
			input: validJCRHeader +
				"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,,Q1,known,synthetic-jcr\n",
			want: "known metric requires JIF and quartile",
		},
		{
			name: "known row missing quartile",
			input: validJCRHeader +
				"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.5,,known,synthetic-jcr\n",
			want: "known metric requires JIF and quartile",
		},
		{
			name: "unknown row has jif",
			input: validJCRHeader +
				"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.5,,unknown,synthetic-jcr\n",
			want: "unknown metric must not contain JIF or quartile",
		},
		{
			name: "unknown row has quartile",
			input: validJCRHeader +
				"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,,Q1,unknown,synthetic-jcr\n",
			want: "unknown metric must not contain JIF or quartile",
		},
		{
			name: "invalid status",
			input: validJCRHeader +
				"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.5,Q1,estimated,synthetic-jcr\n",
			want: "invalid metric status",
		},
		{
			name: "invalid quartile",
			input: validJCRHeader +
				"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.5,Q5,known,synthetic-jcr\n",
			want: "invalid JCR quartile",
		},
		{
			name: "invalid issn checksum",
			input: validJCRHeader +
				"Synthetic Journal,1234-567X,1234-5679,2049-3630,2025,AI,12.5,Q1,known,synthetic-jcr\n",
			want: "checksum",
		},
		{
			name: "blank issn role",
			input: validJCRHeader +
				"Synthetic Journal,1234-5679,,2049-3630,2025,AI,12.5,Q1,known,synthetic-jcr\n",
			want: "issn is required",
		},
		{
			name: "blank category",
			input: validJCRHeader +
				"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,,12.5,Q1,known,synthetic-jcr\n",
			want: "category is required",
		},
		{
			name: "blank source",
			input: validJCRHeader +
				"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.5,Q1,known,\n",
			want: "source is required",
		},
		{
			name: "invalid metric year",
			input: validJCRHeader +
				"Synthetic Journal,1234-5679,1234-5679,2049-3630,0,AI,12.5,Q1,known,synthetic-jcr\n",
			want: "metric_year",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repository := &fakeJCRRepository{}
			sink := &fakeJCRSink{}
			importer := mustNewJCRImporter(t, repository, sink)
			_, err := importer.Import(context.Background(), strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Import() error = %v, want containing %q", err, tt.want)
			}
			if sink.calls != 0 {
				t.Fatalf("JCRSink calls = %d for invalid CSV, want 0", sink.calls)
			}
		})
	}
}

func TestJCRImporterRejectsMixedFileSources(t *testing.T) {
	t.Parallel()

	item := venueForTest(
		t,
		"venue-alpha",
		"Synthetic Journal",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	repository := &fakeJCRRepository{venues: []Venue{item}}
	sink := &fakeJCRSink{}
	importer := mustNewJCRImporter(t, repository, sink)
	input := validJCRHeader +
		"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.5,Q1,known,synthetic-jcr\n" +
		"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,Robotics,12.5,Q2,known,other-source\n"

	_, err := importer.Import(context.Background(), strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "single source") {
		t.Fatalf("Import() error = %v, want single-source error", err)
	}
	if sink.calls != 0 {
		t.Fatalf("JCRSink calls = %d for mixed sources, want 0", sink.calls)
	}
}

func TestJCRImporterTreatsIdenticalDuplicatesAsIdempotent(t *testing.T) {
	t.Parallel()

	item := venueForTest(
		t,
		"venue-alpha",
		"Synthetic Journal",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	repository := &fakeJCRRepository{venues: []Venue{item}}
	sink := &fakeJCRSink{}
	importer := mustNewJCRImporter(t, repository, sink)
	row := "Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.500,Q1,known,synthetic-jcr\n"

	result, err := importer.Import(
		context.Background(),
		strings.NewReader(validJCRHeader+row+strings.Replace(row, "12.500", "12.5", 1)),
	)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if len(sink.batch.Rows()) != 1 {
		t.Fatalf("persisted rows = %d, want one normalized identical row", len(sink.batch.Rows()))
	}
	if result.InputRows() != 2 || result.InsertedRows() != 1 || result.UnchangedRows() != 1 {
		t.Fatalf(
			"ImportResult counts = %d/%d/%d, want input/inserted/unchanged 2/1/1",
			result.InputRows(),
			result.InsertedRows(),
			result.UnchangedRows(),
		)
	}
}

func TestJCRImporterRejectsConflictingDuplicateRows(t *testing.T) {
	t.Parallel()

	item := venueForTest(
		t,
		"venue-alpha",
		"Synthetic Journal",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	repository := &fakeJCRRepository{venues: []Venue{item}}
	sink := &fakeJCRSink{}
	importer := mustNewJCRImporter(t, repository, sink)
	input := validJCRHeader +
		"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.5,Q1,known,synthetic-jcr\n" +
		"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,9.9,Q2,known,synthetic-jcr\n"

	_, err := importer.Import(context.Background(), strings.NewReader(input))
	if !errors.Is(err, ErrConflictingMetric) {
		t.Fatalf("Import() error = %v, want ErrConflictingMetric", err)
	}
	if sink.calls != 0 {
		t.Fatalf("JCRSink calls = %d after conflicting duplicate, want 0", sink.calls)
	}
}

func TestJCRImporterKeepsHistoricalYearsAndRejectsPersistedConflicts(t *testing.T) {
	t.Parallel()

	item := venueForTest(
		t,
		"venue-alpha",
		"Synthetic Journal",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	oldMetric := mustMetricSnapshot(
		t,
		item.ID(),
		2024,
		"AI",
		"9.8",
		QuartileQ2,
		MetricStatusKnown,
		"synthetic-jcr",
	)
	repository := &fakeJCRRepository{
		venues: []Venue{item},
		metrics: map[string]MetricSnapshot{
			oldMetric.Key().String(): oldMetric,
		},
	}
	sink := &fakeJCRSink{}
	importer := mustNewJCRImporter(t, repository, sink)
	input := validJCRHeader +
		"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,10.1,Q1,known,synthetic-jcr\n"

	result, err := importer.Import(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("Import(new year) error = %v", err)
	}
	if result.InsertedRows() != 1 || len(sink.batch.Rows()) != 1 {
		t.Fatalf("new metric year was not appended: result=%#v rows=%d", result, len(sink.batch.Rows()))
	}
	if sink.batch.Rows()[0].MetricYear() != 2025 {
		t.Fatalf("persisted metric year = %d, want 2025", sink.batch.Rows()[0].MetricYear())
	}
	if got := repository.metrics[oldMetric.Key().String()]; !got.Equal(oldMetric) {
		t.Fatal("historical metric was mutated")
	}

	conflicting := mustMetricSnapshot(
		t,
		item.ID(),
		2025,
		"AI",
		"9.9",
		QuartileQ2,
		MetricStatusKnown,
		"synthetic-jcr",
	)
	repository.metrics[conflicting.Key().String()] = conflicting
	sink.calls = 0
	_, err = importer.Import(context.Background(), strings.NewReader(input))
	if !errors.Is(err, ErrConflictingMetric) {
		t.Fatalf("Import(conflict) error = %v, want ErrConflictingMetric", err)
	}
	if sink.calls != 0 {
		t.Fatalf("JCRSink calls = %d after persisted conflict, want 0", sink.calls)
	}
}

func TestJCRImporterSkipsIdenticalPersistedRowsAndRecordsNewFile(t *testing.T) {
	t.Parallel()

	item := venueForTest(
		t,
		"venue-alpha",
		"Synthetic Journal",
		"1234-5679",
		"1234-5679",
		"2049-3630",
	)
	existing := mustMetricSnapshot(
		t,
		item.ID(),
		2025,
		"AI",
		"12.5",
		QuartileQ1,
		MetricStatusKnown,
		"synthetic-jcr",
	)
	repository := &fakeJCRRepository{
		venues: []Venue{item},
		metrics: map[string]MetricSnapshot{
			existing.Key().String(): existing,
		},
	}
	sink := &fakeJCRSink{}
	importer := mustNewJCRImporter(t, repository, sink)
	input := validJCRHeader +
		"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.500,Q1,known,synthetic-jcr\n"

	result, err := importer.Import(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.InsertedRows() != 0 || result.UnchangedRows() != 1 {
		t.Fatalf(
			"ImportResult inserted/unchanged = %d/%d, want 0/1",
			result.InsertedRows(),
			result.UnchangedRows(),
		)
	}
	if sink.calls != 1 || len(sink.batch.Rows()) != 0 {
		t.Fatalf("sink calls/rows = %d/%d, want 1/0 to record new file receipt", sink.calls, len(sink.batch.Rows()))
	}
}

func TestJCRImporterReturnsOriginalReceiptForIdenticalFile(t *testing.T) {
	t.Parallel()

	input := validJCRHeader +
		"Synthetic Journal,1234-5679,1234-5679,2049-3630,2025,AI,12.5,Q1,known,synthetic-jcr\n"
	digest := sha256.Sum256([]byte(input))
	fileSHA := hex.EncodeToString(digest[:])
	originalTime := time.Date(2025, time.December, 1, 8, 0, 0, 0, time.UTC)
	receipt, err := NewImportReceipt(
		fileSHA,
		"synthetic-jcr",
		originalTime,
		1,
		1,
		0,
	)
	if err != nil {
		t.Fatalf("NewImportReceipt() error = %v", err)
	}
	repository := &fakeJCRRepository{
		imports: map[string]ImportReceipt{fileSHA: receipt},
	}
	sink := &fakeJCRSink{}
	importer := mustNewJCRImporter(t, repository, sink)

	result, err := importer.Import(context.Background(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if !result.ImportedAt().Equal(originalTime) || result.FileSHA256() != fileSHA {
		t.Fatalf("idempotent result = %#v, want original receipt", result)
	}
	if repository.venueCalls != 0 || repository.metricCalls != 0 || sink.calls != 0 {
		t.Fatalf(
			"idempotent file performed work: venue=%d metric=%d sink=%d",
			repository.venueCalls,
			repository.metricCalls,
			sink.calls,
		)
	}
}

func TestJCRImporterHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	importer := mustNewJCRImporter(t, &fakeJCRRepository{}, &fakeJCRSink{})

	_, err := importer.Import(ctx, strings.NewReader(validJCRHeader))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Import() error = %v, want context.Canceled", err)
	}
}

func TestCommittedSyntheticJCRFixtureCoversPolicyOutcomes(t *testing.T) {
	t.Parallel()

	alpha := venueForTest(
		t,
		"venue-alpha",
		"Synthetic Journal of Deterministic Agents",
		"0000-0019",
		"0000-0027",
		"0000-0035",
	)
	unknown := venueForTest(
		t,
		"venue-unknown",
		"Synthetic Journal of Unknown Evidence",
		"0000-0043",
		"0000-0051",
		"0000-006X",
	)
	rejected := venueForTest(
		t,
		"venue-rejected",
		"Synthetic Journal of Subthreshold Results",
		"0000-0078",
		"0000-0086",
		"0000-0094",
	)
	repository := &fakeJCRRepository{venues: []Venue{alpha, unknown, rejected}}
	sink := &fakeJCRSink{}
	importer := mustNewJCRImporter(t, repository, sink)

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve jcr_import_test.go path")
	}
	path := filepath.Join(
		filepath.Dir(currentFile),
		"..",
		"..",
		"..",
		"..",
		"data",
		"venues",
		"jcr-q1.example.csv",
	)
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open committed synthetic JCR fixture: %v", err)
	}
	defer file.Close()

	result, err := importer.Import(context.Background(), file)
	if err != nil {
		t.Fatalf("Import(committed fixture) error = %v", err)
	}
	if result.InputRows() != 5 || result.InsertedRows() != 5 {
		t.Fatalf(
			"fixture row counts = input %d inserted %d, want 5/5",
			result.InputRows(),
			result.InsertedRows(),
		)
	}

	rowsByVenue := make(map[string][]MetricSnapshot)
	foundZeroJIF := false
	for _, row := range sink.batch.Rows() {
		rowsByVenue[row.VenueID()] = append(rowsByVenue[row.VenueID()], row)
		if row.Source() != "synthetic-jcr-fixture" {
			t.Fatalf("fixture source = %q, want explicit synthetic source", row.Source())
		}
		if row.HasJIF() && row.JIF().String() == "0" {
			foundZeroJIF = true
		}
	}
	if !foundZeroJIF {
		t.Fatal("fixture does not contain an exact zero JIF row")
	}
	policy, err := NewJournalPolicy("journal-jif-or-q1/v1")
	if err != nil {
		t.Fatalf("NewJournalPolicy() error = %v", err)
	}
	evaluatedAt := time.Date(2026, time.July, 16, 14, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		item     Venue
		decision PolicyDecision
	}{
		{item: alpha, decision: PolicyDecisionAccepted},
		{item: unknown, decision: PolicyDecisionUnknown},
		{item: rejected, decision: PolicyDecisionRejected},
	} {
		policyResult, evaluateErr := policy.Evaluate(
			test.item,
			2025,
			rowsByVenue[test.item.ID()],
			evaluatedAt,
		)
		if evaluateErr != nil {
			t.Fatalf("Evaluate(%s) error = %v", test.item.ID(), evaluateErr)
		}
		if policyResult.Decision() != test.decision {
			t.Fatalf(
				"fixture decision for %s = %q, want %q",
				test.item.ID(),
				policyResult.Decision(),
				test.decision,
			)
		}
	}
	if len(rowsByVenue[alpha.ID()]) != 2 {
		t.Fatalf("fixture multi-category rows = %d, want 2", len(rowsByVenue[alpha.ID()]))
	}
}

const validJCRHeader = "title,issn_l,issn,eissn,metric_year,category,jif,quartile,status,source\n"

type fakeJCRRepository struct {
	venues      []Venue
	metrics     map[string]MetricSnapshot
	imports     map[string]ImportReceipt
	venueCalls  int
	metricCalls int
}

func (repository *fakeJCRRepository) FindImport(
	_ context.Context,
	fileSHA256 string,
) (ImportReceipt, bool, error) {
	receipt, found := repository.imports[fileSHA256]
	return receipt, found, nil
}

func (repository *fakeJCRRepository) FindVenuesByISSNs(
	_ context.Context,
	identifiers ISSNSet,
) ([]Venue, error) {
	repository.venueCalls++
	var matches []Venue
	for _, item := range repository.venues {
		if item.MatchesISSNs(identifiers) {
			matches = append(matches, item)
		}
	}
	return matches, nil
}

func (repository *fakeJCRRepository) FindMetric(
	_ context.Context,
	key MetricKey,
) (MetricSnapshot, bool, error) {
	repository.metricCalls++
	value, found := repository.metrics[key.String()]
	return value, found, nil
}

type fakeJCRSink struct {
	calls int
	batch JCRImport
	err   error
}

func (sink *fakeJCRSink) PersistJCRImport(
	_ context.Context,
	batch JCRImport,
) (ImportReceipt, error) {
	sink.calls++
	sink.batch = batch
	if sink.err != nil {
		return ImportReceipt{}, sink.err
	}
	return NewImportReceipt(
		batch.FileSHA256(),
		batch.Source(),
		batch.ImportedAt(),
		batch.InputRows(),
		len(batch.Rows()),
		batch.UnchangedRows(),
	)
}

func mustNewJCRImporter(
	t *testing.T,
	repository JCRRepository,
	sink JCRSink,
) *JCRImporter {
	t.Helper()

	importer, err := NewJCRImporter(
		repository,
		sink,
		func() time.Time {
			return time.Date(2026, time.July, 16, 12, 30, 0, 0, time.UTC)
		},
	)
	if err != nil {
		t.Fatalf("NewJCRImporter() error = %v", err)
	}
	return importer
}

func venueForTest(
	t *testing.T,
	id string,
	title string,
	issnL string,
	printISSN string,
	electronicISSN string,
) Venue {
	t.Helper()

	identifiers, err := NewISSNSet(
		mustParseISSN(t, ISSNRoleLinking, issnL),
		mustParseISSN(t, ISSNRolePrint, printISSN),
		mustParseISSN(t, ISSNRoleElectronic, electronicISSN),
	)
	if err != nil {
		t.Fatalf("NewISSNSet() error = %v", err)
	}
	alias, err := NewAlias(title, "fixture")
	if err != nil {
		t.Fatalf("NewAlias() error = %v", err)
	}
	item, err := NewVenue(id, VenueTypeJournal, identifiers, []Alias{alias})
	if err != nil {
		t.Fatalf("NewVenue() error = %v", err)
	}
	return item
}

func mustParseDecimal(t *testing.T, raw string) Decimal {
	t.Helper()

	value, err := ParseDecimal(raw)
	if err != nil {
		t.Fatalf("ParseDecimal(%q) error = %v", raw, err)
	}
	return value
}

func mustMetricSnapshot(
	t *testing.T,
	venueID string,
	year int,
	category string,
	jif string,
	quartile Quartile,
	status MetricStatus,
	source string,
) MetricSnapshot {
	t.Helper()

	var value *Decimal
	if jif != "" {
		parsed := mustParseDecimal(t, jif)
		value = &parsed
	}
	snapshot, err := NewMetricSnapshot(
		venueID,
		year,
		category,
		value,
		quartile,
		status,
		source,
	)
	if err != nil {
		t.Fatalf("NewMetricSnapshot() error = %v", err)
	}
	return snapshot
}
