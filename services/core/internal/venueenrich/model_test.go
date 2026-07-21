package venueenrich

import (
	"reflect"
	"strings"
	"testing"
)

func TestRegistryRowSourcePreservesDomainOrderTitleAndSourceURL(t *testing.T) {
	t.Parallel()

	row := SourceRow{
		Domain:            "medicine",
		SourceOrder:       17,
		SourceJournalName: "Journal: α—β",
		ImpactFactor:      "12.34",
		JCRValue:          "Q1",
		CASSValue:         "1区",
		SourceURL:         "https://example.test/list?domain=medicine&order=17",
	}
	before := row

	if err := row.Validate(); err != nil {
		t.Fatalf("SourceRow.Validate() error = %v", err)
	}
	if !reflect.DeepEqual(row, before) {
		t.Fatalf("SourceRow.Validate() changed row:\n got: %#v\nwant: %#v", row, before)
	}
	if row.Domain != "medicine" ||
		row.SourceOrder != 17 ||
		row.SourceJournalName != "Journal: α—β" ||
		row.SourceURL != "https://example.test/list?domain=medicine&order=17" {
		t.Fatalf("SourceRow did not preserve source identity fields: %#v", row)
	}
}

func TestRegistryRowRejectsInvalidISSNsAndMatchStates(t *testing.T) {
	t.Parallel()

	valid := validRegistryRow()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid RegistryRow.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RegistryRow)
		want   string
		forbid string
	}{
		{
			name: "invalid ISSN-L checksum",
			mutate: func(row *RegistryRow) {
				row.ISSNL = "1234-5678"
			},
			want: "issn_l",
		},
		{
			name: "invalid print ISSN format",
			mutate: func(row *RegistryRow) {
				row.PrintISSN = "20493630"
			},
			want: "print_issn",
		},
		{
			name: "invalid electronic ISSN",
			mutate: func(row *RegistryRow) {
				row.EISSN = "not-an-issn"
			},
			want: "eissn",
		},
		{
			name: "invalid all ISSNs member",
			mutate: func(row *RegistryRow) {
				row.AllISSNs = []string{"1234-5679", "2049-3631"}
			},
			want:   "all_issns",
			forbid: "issn_l",
		},
		{
			name: "blank all ISSNs member",
			mutate: func(row *RegistryRow) {
				row.AllISSNs = []string{"1234-5679", ""}
			},
			want: "all_issns",
		},
		{
			name: "invalid match state",
			mutate: func(row *RegistryRow) {
				row.MatchStatus = MatchStatus("matched")
			},
			want: "match_status",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			candidate := valid
			candidate.AllISSNs = append([]string(nil), valid.AllISSNs...)
			test.mutate(&candidate)

			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"RegistryRow.Validate() error = %v, want field %q",
					err,
					test.want,
				)
			}
			if test.forbid != "" && strings.Contains(err.Error(), test.forbid) {
				t.Fatalf(
					"RegistryRow.Validate() error = %q, must not contain %q",
					err,
					test.forbid,
				)
			}
		})
	}
}

func TestRegistryRowRejectsContradictorySupportEvidenceAndCounts(t *testing.T) {
	t.Parallel()

	valid := validRegistryRow()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid RegistryRow.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RegistryRow)
		want   string
	}{
		{
			name: "invalid Crossref support status",
			mutate: func(row *RegistryRow) {
				row.CrossrefSupported = SupportStatus("supported")
			},
			want: "crossref_supported",
		},
		{
			name: "invalid OpenAlex support status",
			mutate: func(row *RegistryRow) {
				row.OpenAlexSupported = SupportStatus("supported")
			},
			want: "openalex_supported",
		},
		{
			name: "invalid PubMed support status",
			mutate: func(row *RegistryRow) {
				row.PubMedSupported = SupportStatus("supported")
			},
			want: "pubmed_supported",
		},
		{
			name: "negative Crossref DOI count",
			mutate: func(row *RegistryRow) {
				row.CrossrefTotalDOIs = -1
			},
			want: "crossref_total_dois",
		},
		{
			name: "negative PubMed record count",
			mutate: func(row *RegistryRow) {
				row.PubMedRecordCount = -1
			},
			want: "pubmed_record_count",
		},
		{
			name: "PubMed no with positive count",
			mutate: func(row *RegistryRow) {
				row.PubMedSupported = SupportStatusNo
			},
			want: "pubmed_record_count",
		},
		{
			name: "PubMed yes with zero count",
			mutate: func(row *RegistryRow) {
				row.PubMedRecordCount = 0
			},
			want: "pubmed_record_count",
		},
		{
			name: "PubMed unknown with observed count",
			mutate: func(row *RegistryRow) {
				row.PubMedSupported = SupportStatusUnknown
			},
			want: "pubmed_record_count",
		},
		{
			name: "Crossref yes without title",
			mutate: func(row *RegistryRow) {
				row.CrossrefTitle = ""
			},
			want: "crossref_title",
		},
		{
			name: "Crossref yes with untrimmed publisher",
			mutate: func(row *RegistryRow) {
				row.CrossrefPublisher = " Publisher "
			},
			want: "crossref_publisher",
		},
		{
			name: "Crossref no with catalog evidence",
			mutate: func(row *RegistryRow) {
				row.CrossrefSupported = SupportStatusNo
			},
			want: "crossref_supported",
		},
		{
			name: "Crossref unknown with catalog evidence",
			mutate: func(row *RegistryRow) {
				row.CrossrefSupported = SupportStatusUnknown
			},
			want: "crossref_supported",
		},
		{
			name: "OpenAlex yes without source ID",
			mutate: func(row *RegistryRow) {
				row.OpenAlexSourceID = ""
			},
			want: "openalex_source_id",
		},
		{
			name: "OpenAlex no with source ID",
			mutate: func(row *RegistryRow) {
				row.OpenAlexSupported = SupportStatusNo
			},
			want: "openalex_supported",
		},
		{
			name: "OpenAlex unknown with source ID",
			mutate: func(row *RegistryRow) {
				row.OpenAlexSupported = SupportStatusUnknown
			},
			want: "openalex_supported",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			candidate := valid
			candidate.AllISSNs = append([]string(nil), valid.AllISSNs...)
			test.mutate(&candidate)

			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf(
					"RegistryRow.Validate() error = %v, want field %q",
					err,
					test.want,
				)
			}
		})
	}
}

func TestRegistryRowAcceptsConsistentNoAndUnknownSupportEvidence(t *testing.T) {
	t.Parallel()

	for _, status := range []SupportStatus{SupportStatusNo, SupportStatusUnknown} {
		row := validRegistryRow()
		row.CrossrefTitle = ""
		row.CrossrefPublisher = ""
		row.CrossrefTotalDOIs = 0
		row.CrossrefSupported = status
		row.OpenAlexSourceID = ""
		row.OpenAlexSupported = status
		row.PubMedSupported = status
		row.PubMedRecordCount = 0
		row.MatchStatus = MatchStatusUnresolved

		if err := row.Validate(); err != nil {
			t.Fatalf(
				"RegistryRow.Validate() status %q error = %v",
				status,
				err,
			)
		}
	}
}

func validRegistryRow() RegistryRow {
	return RegistryRow{
		SourceRow: SourceRow{
			Domain:            "biology",
			SourceOrder:       8,
			SourceJournalName: "Synthetic Biology Journal",
			SourceURL:         "https://example.test/biology",
		},
		ISSNL:             "1234-5679",
		PrintISSN:         "2049-3630",
		EISSN:             "3141-592X",
		AllISSNs:          []string{"1234-5679", "2049-3630", "3141-592X"},
		CrossrefTitle:     "Synthetic Biology Journal",
		CrossrefPublisher: "Example Publisher",
		CrossrefTotalDOIs: 25,
		CrossrefSupported: SupportStatusYes,
		OpenAlexSourceID:  "https://openalex.org/S123",
		OpenAlexSupported: SupportStatusYes,
		PubMedSupported:   SupportStatusYes,
		PubMedRecordCount: 7,
		MatchStatus:       MatchStatusResolved,
	}
}
