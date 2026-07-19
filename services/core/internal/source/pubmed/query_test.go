package pubmed_test

import (
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
)

func TestSearchQueryBuildsExplicitEntrezDateAndValidatedISSNFilter(t *testing.T) {
	t.Parallel()

	query := pubmed.SearchQuery{
		Term:         "agent systems",
		JournalISSNs: []string{"0028-0836", "0140-6736"},
		DateType:     pubmed.DateTypeEntrez,
		DateWindow: pubmed.DateWindow{
			From: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC),
		},
		MaxResults: 250,
	}

	values, err := query.Values()
	if err != nil {
		t.Fatalf("Values() error = %v", err)
	}
	if got := values.Get("term"); got != "(agent systems) AND (0028-0836[issn] OR 0140-6736[issn])" {
		t.Fatalf("term = %q, want validated ISSN journal filter", got)
	}
	if got := values.Get("datetype"); got != "edat" {
		t.Fatalf("datetype = %q, want edat", got)
	}
	if got := values.Get("mindate"); got != "2026/07/01" {
		t.Fatalf("mindate = %q", got)
	}
	if got := values.Get("maxdate"); got != "2026/07/16" {
		t.Fatalf("maxdate = %q", got)
	}
}

func TestSearchQueryParenthesizesBaseQueryBeforeISSNFilter(t *testing.T) {
	t.Parallel()

	values, err := (pubmed.SearchQuery{
		Term:         "cancer OR diabetes",
		JournalISSNs: []string{"0028-0836"},
		DateType:     pubmed.DateTypeEntrez,
		DateWindow: pubmed.DateWindow{
			From: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			To:   time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC),
		},
		MaxResults: 10,
	}).Values()
	if err != nil {
		t.Fatalf("Values() error = %v", err)
	}
	if got := values.Get("term"); got != "(cancer OR diabetes) AND (0028-0836[issn])" {
		t.Fatalf("term = %q, want explicit OR precedence", got)
	}
}

func TestSearchQueryMapsExplicitDateType(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		dateType pubmed.DateType
		want     string
	}{
		{name: "publication", dateType: pubmed.DateTypePublication, want: "pdat"},
		{name: "entrez", dateType: pubmed.DateTypeEntrez, want: "edat"},
		{name: "modification", dateType: pubmed.DateTypeModification, want: "mdat"},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			values, err := (pubmed.SearchQuery{
				DateType: tt.dateType,
				DateWindow: pubmed.DateWindow{
					From: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
					To:   time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC),
				},
				MaxResults: 1,
			}).Values()
			if err != nil {
				t.Fatalf("Values() error = %v", err)
			}
			if got := values.Get("datetype"); got != tt.want {
				t.Fatalf("datetype = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSearchQueryRejectsEmptyUnknownAndAliasDateTypes(t *testing.T) {
	t.Parallel()

	for _, value := range []pubmed.DateType{
		"",
		"unknown",
		"pdat",
		"edat",
		"mdat",
	} {
		value := value
		t.Run(string(value), func(t *testing.T) {
			t.Parallel()

			query := pubmed.SearchQuery{
				DateType: value,
				DateWindow: pubmed.DateWindow{
					From: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
					To:   time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC),
				},
				MaxResults: 1,
			}
			if got, err := query.Values(); err == nil {
				t.Fatalf("Values() = %v, want explicit DateType error", got)
			} else if !strings.Contains(strings.ToLower(err.Error()), "date type") {
				t.Fatalf("Values() error = %v, want DateType error", err)
			}
		})
	}
}

func TestSearchQueryRejectsJournalTitlesInvalidISSNsAndMissingWindow(t *testing.T) {
	t.Parallel()

	validWindow := pubmed.DateWindow{
		From: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name  string
		query pubmed.SearchQuery
		want  string
	}{
		{
			name: "journal title",
			query: pubmed.SearchQuery{
				JournalISSNs: []string{"Nature"},
				DateType:     pubmed.DateTypeEntrez,
				DateWindow:   validWindow,
				MaxResults:   1,
			},
			want: "ISSN",
		},
		{
			name: "invalid check digit",
			query: pubmed.SearchQuery{
				JournalISSNs: []string{"0028-0837"},
				DateType:     pubmed.DateTypeEntrez,
				DateWindow:   validWindow,
				MaxResults:   1,
			},
			want: "ISSN",
		},
		{
			name: "missing from",
			query: pubmed.SearchQuery{
				DateWindow: pubmed.DateWindow{To: validWindow.To},
				DateType:   pubmed.DateTypeEntrez,
				MaxResults: 1,
			},
			want: "date window",
		},
		{
			name: "reversed window",
			query: pubmed.SearchQuery{
				DateWindow: pubmed.DateWindow{From: validWindow.To, To: validWindow.From},
				DateType:   pubmed.DateTypeEntrez,
				MaxResults: 1,
			},
			want: "date window",
		},
		{
			name: "unbounded results",
			query: pubmed.SearchQuery{
				DateWindow: validWindow,
				DateType:   pubmed.DateTypeEntrez,
			},
			want: "max results",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := tt.query.Values(); err == nil {
				t.Fatalf("Values() = %v, want error", got)
			} else if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				t.Fatalf("Values() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestSearchResultBuildsStableBoundedBatches(t *testing.T) {
	t.Parallel()

	batches, err := pubmed.BuildBatches(5, 4, 2)
	if err != nil {
		t.Fatalf("BuildBatches() error = %v", err)
	}
	want := []pubmed.Batch{
		{RetStart: 0, RetMax: 2},
		{RetStart: 2, RetMax: 2},
	}
	if len(batches) != len(want) {
		t.Fatalf("batches = %#v, want %#v", batches, want)
	}
	for index := range want {
		if batches[index] != want[index] {
			t.Fatalf("batch %d = %#v, want %#v", index, batches[index], want[index])
		}
	}
}

func TestBuildBatchesHandlesMaxIntWithoutOverflow(t *testing.T) {
	t.Parallel()

	maxInt := int(^uint(0) >> 1)
	batchSize := maxInt/2 + 1
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("BuildBatches() panicked near MaxInt: %v", recovered)
		}
	}()

	batches, err := pubmed.BuildBatches(maxInt, maxInt, batchSize)
	if err != nil {
		t.Fatalf("BuildBatches() error = %v", err)
	}
	want := []pubmed.Batch{
		{RetStart: 0, RetMax: batchSize},
		{RetStart: batchSize, RetMax: maxInt - batchSize},
	}
	if len(batches) != len(want) {
		t.Fatalf("batches = %#v, want %#v", batches, want)
	}
	for index := range want {
		if batches[index] != want[index] {
			t.Fatalf("batch %d = %#v, want %#v", index, batches[index], want[index])
		}
	}
}
