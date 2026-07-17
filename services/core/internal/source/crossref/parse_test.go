package crossref_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/crossref"
)

func TestParseRequiresAndNormalizesDOIAsCrossrefIdentity(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"unknown":{"kept":true},"DOI":" HTTPS://DOI.ORG/10.1000/ABC "}`)
	record, err := crossref.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if record.Source != source.Crossref {
		t.Fatalf("Source = %q, want crossref", record.Source)
	}
	if record.SourceRecordID != "10.1000/abc" {
		t.Fatalf("SourceRecordID = %q", record.SourceRecordID)
	}
	if record.Identity.Scheme() != paper.SchemeDOI || record.Identity.Value() != "10.1000/abc" {
		t.Fatalf("Identity = %q, want normalized DOI", record.Identity)
	}
	wantIdentifiers := []source.Identifier{{
		Scheme: source.IdentifierDOI,
		Value:  "10.1000/abc",
	}}
	if len(record.Identifiers) != 1 || record.Identifiers[0] != wantIdentifiers[0] {
		t.Fatalf("Identifiers = %#v, want %#v", record.Identifiers, wantIdentifiers)
	}
	if !bytes.Equal(record.Raw.Payload, raw) {
		t.Fatalf("Raw.Payload = %q, want exact item JSON %q", record.Raw.Payload, raw)
	}
	if record.Raw.SHA256 == "" {
		t.Fatal("Raw.SHA256 is empty")
	}
	if len(record.Evidence) != 1 || record.Evidence[0] != (source.FieldEvidence{
		Field:      "identifiers",
		SourcePath: "$.DOI",
	}) {
		t.Fatalf("Evidence = %#v, want only mapped DOI evidence", record.Evidence)
	}
}

func TestParseRejectsMissingOrMalformedRequiredDOI(t *testing.T) {
	t.Parallel()

	for _, raw := range [][]byte{
		[]byte(`{"title":["missing DOI"]}`),
		[]byte(`{"DOI":"not-a-doi"}`),
	} {
		if _, err := crossref.Parse(raw); err == nil {
			t.Fatalf("Parse(%s) accepted invalid required DOI", raw)
		}
	}
}

func TestParseMapsCrossrefItemWithoutInventingValues(t *testing.T) {
	t.Parallel()

	raw := fixtureItem(t)
	record, err := crossref.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if record.Title != "A deterministic Crossref record" {
		t.Fatalf("Title = %q, want title[0]", record.Title)
	}
	if record.Publisher != "Association for Deterministic Metadata" {
		t.Fatalf("Publisher = %q", record.Publisher)
	}
	if record.Abstract != "Agents preserve evidence." {
		t.Fatalf("Abstract = %q", record.Abstract)
	}
	if len(record.Authors) != 2 {
		t.Fatalf("Authors = %#v, want two mapped authors", record.Authors)
	}
	if got := record.Authors[0]; got.DisplayName != "Ada Agent" ||
		got.ForeName != "Ada" ||
		got.LastName != "Agent" ||
		got.ORCID != "0000-0001-2345-6789" ||
		got.Position != 1 ||
		!slices.Equal(got.Affiliations, []string{
			"Deterministic Systems Lab",
			"Example University",
		}) {
		t.Fatalf("first author = %#v", got)
	}
	if got := record.Authors[1]; got.DisplayName != "Evidence Consortium" ||
		got.CollectiveName != "Evidence Consortium" ||
		got.Position != 2 {
		t.Fatalf("collective author = %#v", got)
	}

	if record.Venue == nil ||
		record.Venue.DisplayName != "Journal of Reproducible Agents" ||
		record.Venue.ISOAbbreviation != "J. Reprod. Agents" ||
		record.Venue.Type != "journal" ||
		record.Venue.ISSNL != "" ||
		!slices.Equal(record.Venue.ISSN, []string{"0028-0836", "2049-3630"}) ||
		!slices.Equal(record.Venue.ISSNDetails, []source.VenueISSN{
			{Value: "0028-0836", Type: "Print"},
			{Value: "2049-3630", Type: "Electronic"},
		}) {
		t.Fatalf("Venue = %#v", record.Venue)
	}

	if record.PublishedAt != nil {
		t.Fatalf("PublishedAt = %v, must not invent a day for month precision", record.PublishedAt)
	}
	wantPublishedDate := source.SourceDate{
		Year:      2026,
		Month:     time.July,
		Precision: source.DatePrecisionMonth,
	}
	if record.PublishedDate == nil || *record.PublishedDate != wantPublishedDate {
		t.Fatalf("PublishedDate = %#v, want %#v", record.PublishedDate, wantPublishedDate)
	}
	wantElectronicDate := source.SourceDate{
		Year:      2026,
		Month:     time.July,
		Day:       8,
		Precision: source.DatePrecisionDay,
	}
	if record.ElectronicPublicationDate == nil ||
		*record.ElectronicPublicationDate != wantElectronicDate {
		t.Fatalf(
			"ElectronicPublicationDate = %#v, want %#v",
			record.ElectronicPublicationDate,
			wantElectronicDate,
		)
	}
	assertTime(
		t,
		record.ElectronicPublishedAt,
		time.Date(2026, time.July, 8, 0, 0, 0, 0, time.UTC),
	)
	assertTime(
		t,
		record.CreatedAt,
		time.Date(2026, time.July, 1, 10, 34, 56, 0, time.UTC),
	)
	assertTime(
		t,
		record.UpdatedAt,
		time.Date(2026, time.July, 16, 3, 45, 0, 0, time.UTC),
	)

	wantLicenses := []source.License{
		{
			URL:        "https://creativecommons.org/licenses/by/4.0/",
			SourcePath: "$.license[0].URL",
		},
		{
			URL:        "http://example.test/license/metadata",
			SourcePath: "$.license[1].URL",
		},
	}
	if !slices.Equal(record.Licenses, wantLicenses) {
		t.Fatalf("Licenses = %#v, want %#v", record.Licenses, wantLicenses)
	}
	wantRelations := []source.Relation{{
		Type:     "correction",
		TargetID: "10.1000/correction",
		Note:     "Correction",
	}}
	if !slices.Equal(record.Relations, wantRelations) {
		t.Fatalf("Relations = %#v, want %#v", record.Relations, wantRelations)
	}
	if len(record.Identifiers) != 1 ||
		record.Identifiers[0] != (source.Identifier{
			Scheme: source.IdentifierDOI,
			Value:  "10.1000/article",
		}) {
		t.Fatalf("Identifiers = %#v, relation DOI must not join current identity", record.Identifiers)
	}
	if record.OpenAccess != (source.OpenAccess{}) {
		t.Fatalf("OpenAccess = %#v, Crossref license must not infer OA", record.OpenAccess)
	}

	wantEvidence := []source.FieldEvidence{
		{Field: "identifiers", SourcePath: "$.DOI"},
		{Field: "title", SourcePath: "$.title[0]"},
		{Field: "publisher", SourcePath: "$.publisher"},
		{Field: "abstract", SourcePath: "$.abstract"},
		{Field: "authors", SourcePath: "$.author"},
		{Field: "venue", SourcePath: "$.container-title[0]"},
		{Field: "venue", SourcePath: "$.short-container-title[0]"},
		{Field: "venue", SourcePath: "$.type"},
		{Field: "venue", SourcePath: "$.ISSN"},
		{Field: "venue", SourcePath: "$.issn-type"},
		{Field: "published_date", SourcePath: "$.published.date-parts"},
		{Field: "electronic_publication_date", SourcePath: "$.published-online.date-parts"},
		{Field: "electronic_published_at", SourcePath: "$.published-online.date-parts"},
		{Field: "created_at", SourcePath: "$.created.date-time"},
		{Field: "updated_at", SourcePath: "$.indexed.date-time"},
		{Field: "licenses", SourcePath: "$.license[0].URL"},
		{Field: "licenses", SourcePath: "$.license[1].URL"},
		{Field: "relations", SourcePath: "$.update-to"},
	}
	if !slices.Equal(record.Evidence, wantEvidence) {
		t.Fatalf("Evidence = %#v, want only successful mappings %#v", record.Evidence, wantEvidence)
	}
	if record.Scope.Status != source.ScopePending {
		t.Fatalf("Scope = %#v, want pending", record.Scope)
	}
}

func TestParseLeavesOptionalFieldsAbsentAndNeverFallsBackToOtherCrossrefDates(t *testing.T) {
	t.Parallel()

	record, err := crossref.Parse([]byte(`{
		"DOI":"10.1000/minimal",
		"title":["", "must not use title[1]"],
		"container-title":["", "must not use container-title[1]"],
		"short-container-title":["", "must not use short-container-title[1]"],
		"issued":{"date-parts":[[2020,1,2]]},
		"published-print":{"date-parts":[[2021,2,3]]},
		"current-time":{"date-time":"2026-07-16T00:00:00Z"}
	}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if record.Title != "" ||
		record.Publisher != "" ||
		record.Abstract != "" ||
		record.PublishedAt != nil ||
		record.PublishedDate != nil ||
		record.ElectronicPublishedAt != nil ||
		record.ElectronicPublicationDate != nil ||
		record.CreatedAt != nil ||
		record.UpdatedAt != nil ||
		record.Venue != nil ||
		len(record.Authors) != 0 ||
		len(record.Licenses) != 0 ||
		len(record.Relations) != 0 {
		t.Fatalf("Parse() invented optional values or used forbidden fallback: %#v", record)
	}
	wantEvidence := []source.FieldEvidence{{
		Field:      "identifiers",
		SourcePath: "$.DOI",
	}}
	if !slices.Equal(record.Evidence, wantEvidence) {
		t.Fatalf("Evidence = %#v, want only actual DOI mapping", record.Evidence)
	}
}

func TestParseMapsOnlySupportedCrossrefWorkTypesToVenueType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		workType         string
		wantVenueType    string
		wantTypeEvidence bool
	}{
		{
			name:             "journal article",
			workType:         `"type":"journal-article",`,
			wantVenueType:    "journal",
			wantTypeEvidence: true,
		},
		{
			name:             "proceedings article",
			workType:         `"type":"proceedings-article",`,
			wantVenueType:    "conference",
			wantTypeEvidence: true,
		},
		{
			name: "missing type",
		},
		{
			name:     "unsupported type",
			workType: `"type":"book-chapter",`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			record, err := crossref.Parse([]byte(
				`{"DOI":"10.1000/venue-type",` +
					test.workType +
					`"container-title":["Deterministic Venue"]}`,
			))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if record.Venue == nil {
				t.Fatal("Venue = nil, want container-title venue")
			}
			if record.Venue.Type != test.wantVenueType {
				t.Fatalf(
					"Venue.Type = %q, want %q",
					record.Venue.Type,
					test.wantVenueType,
				)
			}

			gotTypeEvidence := slices.Contains(
				record.Evidence,
				source.FieldEvidence{
					Field:      "venue",
					SourcePath: "$.type",
				},
			)
			if gotTypeEvidence != test.wantTypeEvidence {
				t.Fatalf(
					"Evidence contains venue $.type = %t, want %t; Evidence = %#v",
					gotTypeEvidence,
					test.wantTypeEvidence,
					record.Evidence,
				)
			}
		})
	}
}

func TestParsePreservesCrossrefDatePrecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		dateParts     string
		wantDate      source.SourceDate
		wantTimestamp *time.Time
	}{
		{
			name:      "year",
			dateParts: `[[2026]]`,
			wantDate: source.SourceDate{
				Year:      2026,
				Precision: source.DatePrecisionYear,
			},
		},
		{
			name:      "month",
			dateParts: `[[2026,7]]`,
			wantDate: source.SourceDate{
				Year:      2026,
				Month:     time.July,
				Precision: source.DatePrecisionMonth,
			},
		},
		{
			name:      "day",
			dateParts: `[[2026,7,16]]`,
			wantDate: source.SourceDate{
				Year:      2026,
				Month:     time.July,
				Day:       16,
				Precision: source.DatePrecisionDay,
			},
			wantTimestamp: timePointer(time.Date(
				2026,
				time.July,
				16,
				0,
				0,
				0,
				0,
				time.UTC,
			)),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw := []byte(`{"DOI":"10.1000/date","published":{"date-parts":` +
				test.dateParts + `}}`)
			record, err := crossref.Parse(raw)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if record.PublishedDate == nil || *record.PublishedDate != test.wantDate {
				t.Fatalf("PublishedDate = %#v, want %#v", record.PublishedDate, test.wantDate)
			}
			if test.wantTimestamp == nil {
				if record.PublishedAt != nil {
					t.Fatalf("PublishedAt = %v, must remain absent", record.PublishedAt)
				}
			} else {
				assertTime(t, record.PublishedAt, *test.wantTimestamp)
			}
		})
	}
}

func TestParseRejectsInvalidCrossrefDatesAndTimestamps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing date parts", body: `"published":{}`, want: "published"},
		{name: "null date parts", body: `"published":{"date-parts":null}`, want: "date-parts"},
		{name: "empty outer", body: `"published":{"date-parts":[]}`, want: "date-parts"},
		{name: "multiple candidates", body: `"published":{"date-parts":[[2026],[2027]]}`, want: "date-parts"},
		{name: "empty parts", body: `"published":{"date-parts":[[]]}`, want: "date-parts"},
		{name: "too precise", body: `"published":{"date-parts":[[2026,7,16,1]]}`, want: "date-parts"},
		{name: "year zero", body: `"published":{"date-parts":[[0]]}`, want: "year"},
		{name: "month invalid", body: `"published":{"date-parts":[[2026,13]]}`, want: "month"},
		{name: "day invalid", body: `"published":{"date-parts":[[2026,2,30]]}`, want: "day"},
		{name: "created missing date-time", body: `"created":{"timestamp":1700000000000}`, want: "created"},
		{name: "created malformed", body: `"created":{"date-time":"not-a-time"}`, want: "created"},
		{name: "indexed missing date-time", body: `"indexed":{"timestamp":1700000000000}`, want: "indexed"},
		{name: "indexed malformed", body: `"indexed":{"date-time":"2026-07-16"}`, want: "indexed"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := crossref.Parse([]byte(`{"DOI":"10.1000/date",` + test.body + `}`))
			if err == nil {
				t.Fatalf("Parse() accepted invalid %s", test.body)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("Parse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseAcceptsPlainTextOrStrictJATSAbstractOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		abstract string
		want     string
	}{
		{
			name:     "plain text",
			abstract: "  Agents\n preserve\t evidence.  ",
			want:     "Agents preserve evidence.",
		},
		{
			name: "strict JATS",
			abstract: `<jats:sec xmlns:jats="http://www.ncbi.nlm.nih.gov/JATS1">` +
				`<jats:title>Background</jats:title>` +
				`<jats:p>Agents <jats:bold>preserve</jats:bold> evidence.</jats:p>` +
				`</jats:sec>`,
			want: "Background Agents preserve evidence.",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(map[string]any{
				"DOI":      "10.1000/abstract",
				"abstract": test.abstract,
			})
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			record, err := crossref.Parse(raw)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if record.Abstract != test.want {
				t.Fatalf("Abstract = %q, want %q", record.Abstract, test.want)
			}
		})
	}

	for _, test := range []struct {
		name     string
		abstract string
	}{
		{name: "malformed", abstract: `<jats:p>unclosed`},
		{name: "HTML", abstract: `<p>not JATS</p>`},
		{name: "unknown JATS", abstract: `<jats:script>alert(1)</jats:script>`},
		{
			name: "inline element as fragment root",
			abstract: `<jats:title>Abstract</jats:title>` +
				`<jats:italic>not a block root</jats:italic>`,
		},
		{name: "doctype", abstract: `<!DOCTYPE foo><jats:p>text</jats:p>`},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(map[string]any{
				"DOI":      "10.1000/abstract",
				"abstract": test.abstract,
			})
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if _, err := crossref.Parse(raw); err == nil {
				t.Fatalf("Parse() accepted non-strict abstract %q", test.abstract)
			}
		})
	}
}

func TestParseAcceptsStrictCrossrefJATSAbstractFragment(t *testing.T) {
	t.Parallel()

	abstract := `<jats:title>Abstract</jats:title>` +
		`<jats:sec sec-type="background">` +
		`<jats:title>Background</jats:title>` +
		`<jats:p>Agents preserve evidence.</jats:p>` +
		`</jats:sec>` +
		`<jats:sec sec-type="objective">` +
		`<jats:title>Objective</jats:title>` +
		`<jats:p>Verify the complete ingestion path.</jats:p>` +
		`</jats:sec>`
	raw, err := json.Marshal(map[string]any{
		"DOI":      "10.1000/jats-fragment",
		"abstract": abstract,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	record, err := crossref.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	const want = "Abstract Background Agents preserve evidence. Objective Verify the complete ingestion path."
	if record.Abstract != want {
		t.Fatalf("Abstract = %q, want %q", record.Abstract, want)
	}
}

func TestParseAbstractClassifiesOnlyAnAllowedLeadingJATSRootAsXML(t *testing.T) {
	t.Parallel()

	for _, abstract := range []string{
		"p < 0.05",
		"Effect size was 2 < 3 and remained significant.",
		"& leading ampersand remains plain text",
		"Summary before <jats:p>literal text that is not a leading root</jats:p>",
	} {
		abstract := abstract
		t.Run(abstract, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(map[string]any{
				"DOI":      "10.1000/plain-less-than",
				"abstract": abstract,
			})
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			record, err := crossref.Parse(raw)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if record.Abstract != strings.Join(strings.Fields(abstract), " ") {
				t.Fatalf("Abstract = %q, want plain text preserved", record.Abstract)
			}
		})
	}
}

func TestParseJATSPreservesInlineTextPunctuationEntitiesAndBlockOrder(t *testing.T) {
	t.Parallel()

	abstract := `<jats:sec xmlns:jats="http://www.ncbi.nlm.nih.gov/JATS1">` +
		`<jats:title>Back<jats:italic>ground</jats:italic></jats:title>` +
		`<jats:p>inter<jats:bold>oper</jats:bold>ability` +
		`<jats:italic>,</jats:italic> A &amp; B.</jats:p>` +
		`<jats:p>Not frag<jats:italic>mented</jats:italic>.</jats:p>` +
		`</jats:sec>`
	raw, err := json.Marshal(map[string]any{
		"DOI":      "10.1000/jats-inline",
		"abstract": abstract,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	record, err := crossref.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	const want = "Background interoperability, A & B. Not fragmented."
	if record.Abstract != want {
		t.Fatalf("Abstract = %q, want document-order text %q", record.Abstract, want)
	}
}

func TestParseJATSHasIndependentTypedResourceLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		kind     crossref.JATSLimitKind
		abstract func() string
	}{
		{
			name: "depth",
			kind: crossref.JATSLimitDepth,
			abstract: func() string {
				return `<jats:sec xmlns:jats="http://www.ncbi.nlm.nih.gov/JATS1">` +
					strings.Repeat("<jats:sec>", crossref.MaxJATSDepth) +
					"text" +
					strings.Repeat("</jats:sec>", crossref.MaxJATSDepth) +
					`</jats:sec>`
			},
		},
		{
			name: "elements",
			kind: crossref.JATSLimitElements,
			abstract: func() string {
				return `<jats:sec xmlns:jats="http://www.ncbi.nlm.nih.gov/JATS1">` +
					strings.Repeat("<jats:p/>", crossref.MaxJATSElements) +
					`</jats:sec>`
			},
		},
		{
			name: "text bytes",
			kind: crossref.JATSLimitTextBytes,
			abstract: func() string {
				return `<jats:p xmlns:jats="http://www.ncbi.nlm.nih.gov/JATS1">` +
					strings.Repeat("x", crossref.MaxJATSTextBytes+1) +
					`</jats:p>`
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(map[string]any{
				"DOI":      "10.1000/jats-limit",
				"abstract": test.abstract(),
			})
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			_, err = crossref.Parse(raw)
			if err == nil {
				t.Fatal("Parse() accepted JATS beyond explicit resource limit")
			}
			var limitErr *crossref.JATSLimitError
			if !errors.As(err, &limitErr) {
				t.Fatalf("Parse() error = %T %v, want *JATSLimitError", err, err)
			}
			if limitErr.Kind != test.kind {
				t.Fatalf("JATSLimitError.Kind = %q, want %q", limitErr.Kind, test.kind)
			}
		})
	}
}

func TestParseStrictlyValidatesISSNAndISSNType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "bad ISSN format", body: `"ISSN":["Nature"]`, want: "ISSN"},
		{name: "bad ISSN checksum", body: `"ISSN":["0028-0837"]`, want: "ISSN"},
		{
			name: "unknown ISSN type",
			body: `"ISSN":["0028-0836"],"issn-type":[{"value":"0028-0836","type":"linking"}]`,
			want: "issn-type",
		},
		{
			name: "typed ISSN missing from ISSN",
			body: `"ISSN":["0028-0836"],"issn-type":[{"value":"2049-3630","type":"electronic"}]`,
			want: "issn-type",
		},
		{
			name: "conflicting types",
			body: `"ISSN":["0028-0836"],"issn-type":[` +
				`{"value":"0028-0836","type":"print"},` +
				`{"value":"0028-0836","type":"electronic"}]`,
			want: "conflicting",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := crossref.Parse([]byte(`{"DOI":"10.1000/issn",` + test.body + `}`))
			if err == nil {
				t.Fatalf("Parse() accepted invalid ISSN payload")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("Parse() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseRejectsNonAbsoluteHTTPLicenses(t *testing.T) {
	t.Parallel()

	for _, licenseURL := range []string{
		"",
		"/relative/license",
		"ftp://example.test/license",
		"https://user:secret@example.test/license",
		"://malformed",
	} {
		licenseURL := licenseURL
		t.Run(licenseURL, func(t *testing.T) {
			t.Parallel()

			raw, err := json.Marshal(map[string]any{
				"DOI": "10.1000/license",
				"license": []map[string]any{{
					"URL": licenseURL,
				}},
			})
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if _, err := crossref.Parse(raw); err == nil {
				t.Fatalf("Parse() accepted license URL %q", licenseURL)
			}
		})
	}
}

func TestParseValidatesUpdateRelationsWithoutChangingCurrentIdentity(t *testing.T) {
	t.Parallel()

	record, err := crossref.Parse([]byte(`{
		"DOI":"10.1000/current",
		"update-to":[{
			"DOI":"https://doi.org/10.1000/TARGET",
			"type":"retraction",
			"label":"Retraction"
		}]
	}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := []source.Relation{{
		Type:     "retraction",
		TargetID: "10.1000/target",
		Note:     "Retraction",
	}}
	if !slices.Equal(record.Relations, want) {
		t.Fatalf("Relations = %#v, want %#v", record.Relations, want)
	}
	if len(record.Identifiers) != 1 || record.Identifiers[0].Value != "10.1000/current" {
		t.Fatalf("Identifiers = %#v, relation DOI contaminated current identity", record.Identifiers)
	}

	for _, relation := range []string{
		`{"type":"correction"}`,
		`{"DOI":"not-a-doi","type":"correction"}`,
	} {
		if _, err := crossref.Parse([]byte(
			`{"DOI":"10.1000/current","update-to":[` + relation + `]}`,
		)); err == nil {
			t.Fatalf("Parse() accepted invalid update-to relation %s", relation)
		}
	}
}

func TestParseRawHashIsStableForEquivalentJSONAndChangesWithUnknownFields(t *testing.T) {
	t.Parallel()

	first := []byte(`{"DOI":"10.1000/hash","future":{"b":2,"a":1}}`)
	reordered := []byte(`{"future":{"a":1,"b":2},"DOI":"10.1000/hash"}`)
	changed := []byte(`{"DOI":"10.1000/hash","future":{"b":3,"a":1}}`)

	firstRecord, err := crossref.Parse(first)
	if err != nil {
		t.Fatalf("Parse(first) error = %v", err)
	}
	reorderedRecord, err := crossref.Parse(reordered)
	if err != nil {
		t.Fatalf("Parse(reordered) error = %v", err)
	}
	changedRecord, err := crossref.Parse(changed)
	if err != nil {
		t.Fatalf("Parse(changed) error = %v", err)
	}
	if firstRecord.Raw.SHA256 != reorderedRecord.Raw.SHA256 {
		t.Fatalf(
			"equivalent JSON hashes differ: %q != %q",
			firstRecord.Raw.SHA256,
			reorderedRecord.Raw.SHA256,
		)
	}
	if firstRecord.Raw.SHA256 == changedRecord.Raw.SHA256 {
		t.Fatalf("unknown field update did not change raw hash %q", firstRecord.Raw.SHA256)
	}
	if !bytes.Equal(firstRecord.Raw.Payload, first) ||
		!bytes.Equal(reorderedRecord.Raw.Payload, reordered) {
		t.Fatal("Raw.Payload did not preserve exact item JSON including unknown fields")
	}
}

func TestParseRejectsDuplicateJSONKeysAtEveryNestedObjectLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		key  string
	}{
		{
			name: "item DOI",
			raw:  `{"DOI":"10.1000/first","DOI":"10.1000/second"}`,
			key:  "DOI",
		},
		{
			name: "author",
			raw: `{
				"DOI":"10.1000/duplicate",
				"author":[{"given":"Ada","given":"Grace"}]
			}`,
			key: "given",
		},
		{
			name: "affiliation",
			raw: `{
				"DOI":"10.1000/duplicate",
				"author":[{"affiliation":[{"name":"First","name":"Second"}]}]
			}`,
			key: "name",
		},
		{
			name: "license",
			raw: `{
				"DOI":"10.1000/duplicate",
				"license":[{
					"URL":"https://example.test/first",
					"URL":"https://example.test/second"
				}]
			}`,
			key: "URL",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			record, err := crossref.Parse([]byte(test.raw))
			if err == nil {
				t.Fatalf("Parse() = %#v, want duplicate-key rejection", record)
			}
			if record.Raw.SHA256 != "" {
				t.Fatalf("Parse() constructed Raw hash %q before duplicate-key rejection", record.Raw.SHA256)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "duplicate") ||
				!strings.Contains(err.Error(), test.key) {
				t.Fatalf("Parse() error = %v, want duplicate key %q", err, test.key)
			}
		})
	}
}

func TestParseDuplicateKeysCannotCollapseToCanonicalSuccessfulHash(t *testing.T) {
	t.Parallel()

	canonical, err := crossref.Parse([]byte(
		`{"DOI":"10.1000/final","future":{"value":2}}`,
	))
	if err != nil {
		t.Fatalf("Parse(canonical) error = %v", err)
	}
	duplicate, err := crossref.Parse([]byte(
		`{"DOI":"10.1000/ignored","DOI":"10.1000/final","future":{"value":1,"value":2}}`,
	))
	if err == nil {
		t.Fatalf(
			"Parse(duplicate) succeeded with hash %q matching canonical %q",
			duplicate.Raw.SHA256,
			canonical.Raw.SHA256,
		)
	}
	if duplicate.Raw.SHA256 != "" {
		t.Fatalf("duplicate payload received Raw hash %q before rejection", duplicate.Raw.SHA256)
	}
}

func TestParseRejectsInvalidORCIDAndNonObjectOrMultipleJSON(t *testing.T) {
	t.Parallel()

	for _, raw := range [][]byte{
		[]byte(`{"DOI":"10.1000/orcid","author":[{"ORCID":"0000-0001-2345-6788"}]}`),
		[]byte(`[]`),
		[]byte(`{"DOI":"10.1000/one"} {"DOI":"10.1000/two"}`),
	} {
		if _, err := crossref.Parse(raw); err == nil {
			t.Fatalf("Parse(%s) accepted invalid item", raw)
		}
	}
}

func fixtureItem(t *testing.T) json.RawMessage {
	t.Helper()

	payload, err := os.ReadFile("testdata/works.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var envelope struct {
		Message struct {
			Items []json.RawMessage `json:"items"`
		} `json:"message"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("Unmarshal fixture error = %v", err)
	}
	if len(envelope.Message.Items) != 1 {
		t.Fatalf("fixture items = %d, want 1", len(envelope.Message.Items))
	}
	return envelope.Message.Items[0]
}

func assertTime(t *testing.T, got *time.Time, want time.Time) {
	t.Helper()

	if got == nil || !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("time = %v, want UTC %v", got, want)
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}
