package venueenrich

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestMatchCrossrefCatalogAllowsBlankPublisherEvidence(t *testing.T) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Journal Without Publisher",
			"",
			7,
			[]string{"0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "Journal Without Publisher")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 ||
		rows[0].MatchStatus != MatchStatusResolved ||
		rows[0].CrossrefPublisher != "" {
		t.Fatalf("rows = %#v, want resolved row with blank publisher", rows)
	}
	if err := rows[0].Validate(); err != nil {
		t.Fatalf("resolved RegistryRow.Validate() error = %v", err)
	}
}

func TestMatchCrossrefCatalogIgnoresRecordsWithoutISSNs(t *testing.T) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Deleted",
			"",
			0,
			[]string{},
			[]CrossrefJournalISSNType{},
		),
		matchCatalogRecord(
			t,
			"Journal With Identity",
			"Publisher",
			11,
			[]string{"0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{
			matchSourceRow(1, "Deleted"),
			matchSourceRow(2, "Journal With Identity"),
		},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 2 ||
		rows[0].MatchStatus != MatchStatusUnresolved ||
		rows[1].MatchStatus != MatchStatusResolved {
		t.Fatalf("rows = %#v, want unresolved no-ISSN row and resolved identity", rows)
	}
}

func TestMatchCrossrefCatalogIgnoresNonTargetInvalidISSNChecksum(t *testing.T) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Fortschritte der Physik",
			"Wiley",
			4586,
			[]string{"0015-8208", "1521-3978", "1521-3979"},
			[]CrossrefJournalISSNType{
				{Value: "0015-8208", Type: "print"},
				{Value: "1521-3978", Type: "electronic"},
			},
		),
		matchCatalogRecord(
			t,
			"Journal With Identity",
			"Publisher",
			11,
			[]string{"0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "Journal With Identity")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 || rows[0].MatchStatus != MatchStatusResolved {
		t.Fatalf("rows = %#v, want resolved target", rows)
	}
}

func TestMatchCrossrefCatalogFiltersInvalidChecksumWhenValidIdentityRemains(
	t *testing.T,
) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"European Journal of Epidemiology",
			"Springer-Verlag",
			5041,
			[]string{"0393-2990", "1573-7284", "0392-2990"},
			[]CrossrefJournalISSNType{
				{Value: "0393-2990", Type: "print"},
				{Value: "1573-7284", Type: "electronic"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "European Journal of Epidemiology")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 ||
		rows[0].MatchStatus != MatchStatusResolved ||
		!slices.Equal(rows[0].AllISSNs, []string{"0393-2990", "1573-7284"}) ||
		rows[0].PrintISSN != "0393-2990" ||
		rows[0].EISSN != "1573-7284" {
		t.Fatalf("rows = %#v, want only checksum-valid ISSN identity", rows)
	}
}

func TestMatchCrossrefCatalogLeavesAllInvalidChecksumUnresolved(t *testing.T) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Journal Without Valid Checksum",
			"Publisher",
			1,
			[]string{"1234-5678"},
			[]CrossrefJournalISSNType{
				{Value: "1234-5678", Type: "print"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "Journal Without Valid Checksum")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 || rows[0].MatchStatus != MatchStatusUnresolved {
		t.Fatalf("rows = %#v, want unresolved row", rows)
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

func TestMatchCrossrefCatalogMergesOverlappingISSNSetsIntoOneIdentity(
	t *testing.T,
) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Overlapping Identity Journal",
			"Stable Publisher",
			19,
			[]string{"0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
			},
		),
		matchCatalogRecord(
			t,
			"Overlapping Identity Journal",
			"Stable Publisher",
			19,
			[]string{"0028-0836", "2049-3630"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
				{Value: "2049-3630", Type: "electronic"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "Overlapping Identity Journal")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 || rows[0].MatchStatus != MatchStatusResolved {
		t.Fatalf("rows = %#v, want one resolved overlapping identity", rows)
	}
	if !slices.Equal(rows[0].AllISSNs, []string{"0028-0836", "2049-3630"}) {
		t.Fatalf("AllISSNs = %v, want union of overlapping identity", rows[0].AllISSNs)
	}
	if rows[0].PrintISSN != "0028-0836" ||
		rows[0].EISSN != "2049-3630" {
		t.Fatalf(
			"ISSN roles = print %q electronic %q, want missing evidence not to erase known roles",
			rows[0].PrintISSN,
			rows[0].EISSN,
		)
	}
}

func TestMatchCrossrefCatalogRejectsBridgeBetweenDisjointISSNSets(
	t *testing.T,
) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Transitive Identity Journal",
			"Stable Publisher",
			19,
			[]string{"0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
			},
		),
		matchCatalogRecord(
			t,
			"Transitive Identity Journal",
			"Stable Publisher",
			19,
			[]string{"3141-592X"},
			[]CrossrefJournalISSNType{
				{Value: "3141-592X", Type: "electronic"},
			},
		),
		matchCatalogRecord(
			t,
			"Transitive Identity Journal",
			"Stable Publisher",
			19,
			[]string{"0028-0836", "3141-592X"},
			[]CrossrefJournalISSNType{},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "Transitive Identity Journal")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 || rows[0].MatchStatus != MatchStatusAmbiguous {
		t.Fatalf("rows = %#v, want bridge across disjoint identities to remain ambiguous", rows)
	}
	assertMatchHasNoCrossrefEvidence(t, rows[0])
}

func TestMatchCrossrefCatalogRejectsNestedISSNSetsWithoutMatchingEvidence(
	t *testing.T,
) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Polluted Superset Journal",
			"First Publisher",
			19,
			[]string{"0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
			},
		),
		matchCatalogRecord(
			t,
			"Polluted Superset Journal",
			"Second Publisher",
			19,
			[]string{"0028-0836", "2049-3630"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
				{Value: "2049-3630", Type: "electronic"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "Polluted Superset Journal")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 || rows[0].MatchStatus != MatchStatusAmbiguous {
		t.Fatalf("rows = %#v, want unmatched publisher evidence to remain ambiguous", rows)
	}
	assertMatchHasNoCrossrefEvidence(t, rows[0])
}

func TestMatchCrossrefCatalogCollapsesEquivalentNormalizedDuplicateTitles(
	t *testing.T,
) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Journal of Exact Matching",
			"Stable Publisher",
			19,
			[]string{"0028-0836", "2049-3630"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
				{Value: "2049-3630", Type: "electronic"},
			},
		),
		matchCatalogRecord(
			t,
			"ＪＯＵＲＮＡＬ　OF   EXACT MATCHING",
			"Stable Publisher",
			19,
			[]string{"2049-3630", "0028-0836"},
			[]CrossrefJournalISSNType{
				{Value: "2049-3630", Type: "electronic"},
				{Value: "0028-0836", Type: "print"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "journal of exact matching")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 || rows[0].MatchStatus != MatchStatusResolved {
		t.Fatalf("rows = %#v, want one resolved identity", rows)
	}
	if rows[0].CrossrefTitle != "Journal of Exact Matching" {
		t.Fatalf(
			"CrossrefTitle = %q, want first verified equivalent title",
			rows[0].CrossrefTitle,
		)
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

func TestMatchCrossrefTitleStateDropsCandidateAfterAmbiguity(t *testing.T) {
	t.Parallel()

	var state crossrefTitleMatch
	first := crossrefMatchCandidate{
		title:        "Shared Journal",
		publisher:    "Publisher",
		totalDOIs:    1,
		allISSNs:     []string{"0028-0836"},
		printISSN:    "0028-0836",
		recordNumber: 1,
	}
	second := crossrefMatchCandidate{
		title:          "Shared Journal",
		publisher:      "Publisher",
		totalDOIs:      2,
		allISSNs:       []string{"2049-3630"},
		electronicISSN: "2049-3630",
		recordNumber:   2,
	}
	third := crossrefMatchCandidate{
		title:        "Shared Journal",
		publisher:    "Publisher",
		totalDOIs:    3,
		allISSNs:     []string{"3141-592X"},
		printISSN:    "3141-592X",
		recordNumber: 3,
	}

	if _, conflict := state.observe("0028-0836", first); conflict != "" {
		t.Fatalf("first observe conflict = %q", conflict)
	}
	if _, conflict := state.observe("2049-3630", second); conflict != "" {
		t.Fatalf("second observe conflict = %q", conflict)
	}
	if _, conflict := state.observe("3141-592X", third); conflict != "" {
		t.Fatalf("third observe conflict = %q", conflict)
	}
	if state.status != MatchStatusAmbiguous {
		t.Fatalf("state.status = %q, want ambiguous", state.status)
	}
	if state.identityKey != "" ||
		state.candidate.title != "" ||
		state.candidate.publisher != "" ||
		state.candidate.totalDOIs != 0 ||
		len(state.candidate.allISSNs) != 0 ||
		state.candidate.printISSN != "" ||
		state.candidate.electronicISSN != "" ||
		state.candidate.recordNumber != 0 {
		t.Fatalf("ambiguous state retained candidate detail: %#v", state)
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

func TestMatchCrossrefCatalogRejectsNewlineBoundaryTruncationAgainstManifest(
	t *testing.T,
) {
	t.Parallel()

	first := matchCatalogRecord(
		t,
		"First Complete Journal",
		"Publisher",
		1,
		[]string{"0028-0836"},
		[]CrossrefJournalISSNType{
			{Value: "0028-0836", Type: "print"},
		},
	)
	second := matchCatalogRecord(
		t,
		"Truncated Away Journal",
		"Publisher",
		2,
		[]string{"2049-3630"},
		[]CrossrefJournalISSNType{
			{Value: "2049-3630", Type: "electronic"},
		},
	)
	catalog := writeMatchCatalog(t, first, second)
	if err := os.Truncate(catalog.CatalogPath, int64(len(first)+1)); err != nil {
		t.Fatalf("Truncate(%q) error = %v", catalog.CatalogPath, err)
	}

	_, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "Truncated Away Journal")},
		catalog,
	)
	if err == nil ||
		!strings.Contains(err.Error(), filepath.Base(catalog.CatalogPath)) ||
		!strings.Contains(strings.ToLower(err.Error()), "record count") {
		t.Fatalf(
			"MatchCrossrefCatalog() error = %v, want located manifest record count mismatch",
			err,
		)
	}
}

func TestMatchCrossrefCatalogRejectsStreamIntegrityMismatch(t *testing.T) {
	t.Parallel()

	record := matchCatalogRecord(
		t,
		"Integrity Journal",
		"Publisher",
		1,
		[]string{"0028-0836"},
		[]CrossrefJournalISSNType{
			{Value: "0028-0836", Type: "print"},
		},
	)
	tests := []struct {
		name   string
		mutate func(*CrossrefCatalogManifest)
		want   string
	}{
		{
			name: "record count",
			mutate: func(manifest *CrossrefCatalogManifest) {
				manifest.RecordCount++
				manifest.Pages[0].RecordCount++
				total := *manifest.TotalResults + 1
				manifest.TotalResults = &total
			},
			want: "record count",
		},
		{
			name: "catalog bytes",
			mutate: func(manifest *CrossrefCatalogManifest) {
				manifest.CatalogBytes++
			},
			want: "byte count",
		},
		{
			name: "catalog SHA-256",
			mutate: func(manifest *CrossrefCatalogManifest) {
				manifest.CatalogSHA256 = strings.Repeat("0", sha256.Size*2)
			},
			want: "SHA-256",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			catalog := writeMatchCatalog(t, record)
			test.mutate(&catalog.Manifest)
			_, err := MatchCrossrefCatalog(
				[]SourceRow{matchSourceRow(1, "Integrity Journal")},
				catalog,
			)
			if err == nil ||
				!strings.Contains(
					strings.ToLower(err.Error()),
					strings.ToLower(test.want),
				) {
				t.Fatalf(
					"MatchCrossrefCatalog() error = %v, want %q mismatch",
					err,
					test.want,
				)
			}
		})
	}
}

func TestMatchCrossrefCatalogRejectsInvalidManifestRequiredFields(t *testing.T) {
	t.Parallel()

	record := matchCatalogRecord(
		t,
		"Manifest Journal",
		"Publisher",
		1,
		[]string{"0028-0836"},
		[]CrossrefJournalISSNType{
			{Value: "0028-0836", Type: "print"},
		},
	)
	tests := []struct {
		name   string
		mutate func(*CrossrefCatalogManifest)
		want   string
	}{
		{
			name: "incomplete",
			mutate: func(manifest *CrossrefCatalogManifest) {
				manifest.Complete = false
			},
			want: "complete",
		},
		{
			name: "schema version",
			mutate: func(manifest *CrossrefCatalogManifest) {
				manifest.SchemaVersion = ""
			},
			want: "schema_version",
		},
		{
			name: "source URL",
			mutate: func(manifest *CrossrefCatalogManifest) {
				manifest.SourceURL = ""
			},
			want: "source_url",
		},
		{
			name: "fetched at",
			mutate: func(manifest *CrossrefCatalogManifest) {
				manifest.FetchedAt = time.Time{}
			},
			want: "fetched_at",
		},
		{
			name: "rows",
			mutate: func(manifest *CrossrefCatalogManifest) {
				manifest.Rows = 0
			},
			want: "rows",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			catalog := writeMatchCatalog(t, record)
			test.mutate(&catalog.Manifest)
			_, err := MatchCrossrefCatalog(
				[]SourceRow{matchSourceRow(1, "Manifest Journal")},
				catalog,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"MatchCrossrefCatalog() error = %v, want manifest field %q",
					err,
					test.want,
				)
			}
		})
	}
}

func TestMatchCrossrefCatalogReturnsImmediatelyForEmptySources(t *testing.T) {
	t.Parallel()

	rows, err := MatchCrossrefCatalog(nil, CrossrefCatalog{})
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog(nil) error = %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("MatchCrossrefCatalog(nil) rows = %#v, want empty", rows)
	}
}

func TestMatchCrossrefCatalogRejectsInvalidCatalogPaths(t *testing.T) {
	t.Parallel()

	validManifest := validMatchCatalogManifest(nil, 0)
	tests := []struct {
		name string
		path func(*testing.T) string
		want string
	}{
		{
			name: "empty",
			path: func(*testing.T) string {
				return ""
			},
			want: "path",
		},
		{
			name: "open failure",
			path: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing.jsonl")
			},
			want: "open",
		},
		{
			name: "non regular",
			path: func(t *testing.T) string {
				return t.TempDir()
			},
			want: "regular",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := MatchCrossrefCatalog(
				[]SourceRow{matchSourceRow(1, "Any Journal")},
				CrossrefCatalog{
					Manifest:    validManifest,
					CatalogPath: test.path(t),
				},
			)
			if err == nil ||
				!strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf(
					"MatchCrossrefCatalog() error = %v, want %q path error",
					err,
					test.want,
				)
			}
		})
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
		name        string
		record      []byte
		sourceTitle string
		want        string
		recordN     int
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
			sourceTitle: "Role Conflict Journal",
			want:        "print",
			recordN:     2,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			catalog := writeMatchCatalog(t, valid, test.record)
			sourceTitle := test.sourceTitle
			if sourceTitle == "" {
				sourceTitle = "Valid Journal"
			}
			_, err := MatchCrossrefCatalog(
				[]SourceRow{matchSourceRow(1, sourceTitle)},
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
			catalog := CrossrefCatalog{
				Manifest:    validMatchCatalogManifest(test.payload, 1),
				CatalogPath: path,
			}
			_, err := MatchCrossrefCatalog(
				[]SourceRow{matchSourceRow(1, "Any Journal")},
				catalog,
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

func TestMatchCrossrefCatalogKeepsOnlyConsistentDuplicateEvidence(t *testing.T) {
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
		name           string
		second         evidence
		wantPublisher  string
		wantTotalDOIs  int64
		wantPrintISSN  string
		wantElectronic string
	}{
		{
			name: "publisher",
			second: evidence{
				title:     base.title,
				publisher: "Changed Publisher",
				totalDOIs: base.totalDOIs,
				roles:     base.roles,
			},
			wantTotalDOIs:  base.totalDOIs,
			wantPrintISSN:  "0028-0836",
			wantElectronic: "2049-3630",
		},
		{
			name: "DOI count",
			second: evidence{
				title:     base.title,
				publisher: base.publisher,
				totalDOIs: 10,
				roles:     base.roles,
			},
			wantPublisher:  base.publisher,
			wantPrintISSN:  "0028-0836",
			wantElectronic: "2049-3630",
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
			wantPublisher: base.publisher,
			wantTotalDOIs: base.totalDOIs,
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
			rows, err := MatchCrossrefCatalog(
				[]SourceRow{matchSourceRow(1, base.title)},
				catalog,
			)
			if err != nil {
				t.Fatalf("MatchCrossrefCatalog() error = %v", err)
			}
			if len(rows) != 1 ||
				rows[0].MatchStatus != MatchStatusResolved ||
				rows[0].CrossrefPublisher != test.wantPublisher ||
				rows[0].CrossrefTotalDOIs != test.wantTotalDOIs ||
				rows[0].PrintISSN != test.wantPrintISSN ||
				rows[0].EISSN != test.wantElectronic ||
				!slices.Equal(
					rows[0].AllISSNs,
					[]string{"0028-0836", "2049-3630"},
				) {
				t.Fatalf("rows = %#v, want conservative merged evidence", rows)
			}
		})
	}
}

func TestMatchCrossrefCatalogDoesNotRestoreConflictingDuplicateEvidence(
	t *testing.T,
) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Sticky Conflict Journal",
			"Publisher A",
			9,
			[]string{"0028-0836", "2049-3630"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
				{Value: "2049-3630", Type: "electronic"},
			},
		),
		matchCatalogRecord(
			t,
			"Sticky Conflict Journal",
			"Publisher B",
			10,
			[]string{"0028-0836", "2049-3630"},
			[]CrossrefJournalISSNType{
				{Value: "2049-3630", Type: "print"},
				{Value: "0028-0836", Type: "electronic"},
			},
		),
		matchCatalogRecord(
			t,
			"Sticky Conflict Journal",
			"Publisher A",
			9,
			[]string{"0028-0836", "2049-3630"},
			[]CrossrefJournalISSNType{
				{Value: "0028-0836", Type: "print"},
				{Value: "2049-3630", Type: "electronic"},
			},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "Sticky Conflict Journal")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 ||
		rows[0].MatchStatus != MatchStatusResolved ||
		rows[0].CrossrefPublisher != "" ||
		rows[0].CrossrefTotalDOIs != 0 ||
		rows[0].PrintISSN != "" ||
		rows[0].EISSN != "" {
		t.Fatalf("rows = %#v, want conflicting evidence to remain cleared", rows)
	}
}

func TestMatchCrossrefCatalogExactTitleConflictBlocksISSNSetExpansion(
	t *testing.T,
) {
	t.Parallel()

	catalog := writeMatchCatalog(t,
		matchCatalogRecord(
			t,
			"Title Conflict Journal",
			"Stable Publisher",
			19,
			[]string{"0028-0836", "2049-3630"},
			[]CrossrefJournalISSNType{},
		),
		matchCatalogRecord(
			t,
			"TITLE CONFLICT JOURNAL",
			"Stable Publisher",
			19,
			[]string{"0028-0836", "2049-3630"},
			[]CrossrefJournalISSNType{},
		),
		matchCatalogRecord(
			t,
			"Title Conflict Journal",
			"Stable Publisher",
			19,
			[]string{"0028-0836"},
			[]CrossrefJournalISSNType{},
		),
	)

	rows, err := MatchCrossrefCatalog(
		[]SourceRow{matchSourceRow(1, "Title Conflict Journal")},
		catalog,
	)
	if err != nil {
		t.Fatalf("MatchCrossrefCatalog() error = %v", err)
	}
	if len(rows) != 1 || rows[0].MatchStatus != MatchStatusAmbiguous {
		t.Fatalf("rows = %#v, want exact title conflict to block set expansion", rows)
	}
	assertMatchHasNoCrossrefEvidence(t, rows[0])
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
	return CrossrefCatalog{
		Manifest:    validMatchCatalogManifest(payload, int64(len(records))),
		CatalogPath: path,
	}
}

func validMatchCatalogManifest(
	payload []byte,
	recordCount int64,
) CrossrefCatalogManifest {
	catalogHash := sha256.Sum256(payload)
	pageHash := sha256.Sum256(payload)
	totalResults := recordCount
	fetchedAt := time.Date(2026, 7, 19, 8, 9, 10, 0, time.UTC)
	pageBytes := int64(len(payload))
	if pageBytes == 0 {
		pageBytes = 1
	}
	return CrossrefCatalogManifest{
		SchemaVersion: CrossrefCatalogSchemaVersion,
		SourceURL:     "https://api.crossref.org/journals",
		FetchedAt:     fetchedAt,
		CheckpointAt:  fetchedAt,
		Rows:          CrossrefCatalogRows,
		Pages: []CrossrefCatalogPageReceipt{
			{
				Ordinal:     1,
				CursorIn:    "*",
				CursorOut:   "",
				RecordCount: int(recordCount),
				PageFile: filepath.Join(
					crossrefCatalogPagesDirectoryName,
					"page-000001.json",
				),
				PageBytes:  pageBytes,
				PageSHA256: hex.EncodeToString(pageHash[:]),
			},
		},
		CatalogSHA256: hex.EncodeToString(catalogHash[:]),
		CatalogBytes:  int64(len(payload)),
		RecordCount:   recordCount,
		TotalResults:  &totalResults,
		Complete:      true,
	}
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
