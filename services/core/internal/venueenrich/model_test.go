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

	valid := RegistryRow{
		SourceRow: SourceRow{
			Domain:            "biology",
			SourceOrder:       8,
			SourceJournalName: "Synthetic Biology Journal",
			SourceURL:         "https://example.test/biology",
		},
		ISSNL:       "1234-5679",
		PrintISSN:   "2049-3630",
		EISSN:       "3141-592X",
		AllISSNs:    []string{"1234-5679", "2049-3630", "3141-592X"},
		MatchStatus: MatchStatusResolved,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid RegistryRow.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RegistryRow)
		want   string
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
			want: "all_issns",
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
		})
	}
}
