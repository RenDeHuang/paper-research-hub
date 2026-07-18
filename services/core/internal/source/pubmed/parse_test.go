package pubmed_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/pubmed"
)

func TestParseMapsPubMedArticleAndPreservesExactSingleElementBytes(t *testing.T) {
	t.Parallel()

	payload, err := os.ReadFile("testdata/efetch.xml")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	records, err := pubmed.Parse(payload)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2", len(records))
	}

	record := records[0]
	if record.Source != source.PubMed || record.SourceRecordID != "12345678" {
		t.Fatalf("source identity = %q/%q", record.Source, record.SourceRecordID)
	}
	if record.Identity.CanonicalKey() != "doi:10.1000/pubmed.1" {
		t.Fatalf("Identity = %q, want DOI canonical identity", record.Identity.CanonicalKey())
	}
	wantIdentifiers := []source.Identifier{
		{Scheme: source.IdentifierPMID, Value: "12345678"},
		{Scheme: source.IdentifierDOI, Value: "10.1000/pubmed.1"},
		{Scheme: source.IdentifierPMCID, Value: "PMC7654321"},
	}
	if !slices.Equal(record.Identifiers, wantIdentifiers) {
		t.Fatalf("Identifiers = %#v, want %#v", record.Identifiers, wantIdentifiers)
	}

	start := bytes.Index(payload, []byte(`<PubmedArticle Status="MEDLINE">`))
	endMarker := []byte(`</PubmedArticle>`)
	end := start + bytes.Index(payload[start:], endMarker) + len(endMarker)
	if start < 0 || end < start {
		t.Fatal("fixture does not contain first PubmedArticle")
	}
	wantRaw := payload[start:end]
	if !bytes.Equal(record.Raw.Payload, wantRaw) {
		t.Fatalf("Raw.Payload did not preserve exact single-element bytes:\n got %q\nwant %q", record.Raw.Payload, wantRaw)
	}
	digest := sha256.Sum256(wantRaw)
	if record.Raw.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("Raw.SHA256 = %q, want exact original element hash", record.Raw.SHA256)
	}

	if record.Title != "A deterministic agent study with unknown markup" {
		t.Fatalf("Title = %q", record.Title)
	}
	if record.Abstract != "Agents are reproducible.\n\nThey preserve evidence." {
		t.Fatalf("Abstract = %q", record.Abstract)
	}
	wantSections := []source.AbstractSection{
		{Label: "BACKGROUND", NLMCategory: "BACKGROUND", Text: "Agents are reproducible."},
		{Label: "METHODS", NLMCategory: "METHODS", Text: "They preserve evidence."},
	}
	if !slices.Equal(record.AbstractSections, wantSections) {
		t.Fatalf("AbstractSections = %#v, want %#v", record.AbstractSections, wantSections)
	}
	if record.CopyrightInformation != "© 2026 Authors. CC BY 4.0." {
		t.Fatalf("CopyrightInformation = %q", record.CopyrightInformation)
	}

	if len(record.Authors) != 2 {
		t.Fatalf("Authors = %#v, want two", record.Authors)
	}
	if got := record.Authors[0]; got.DisplayName != "Ada Agent" ||
		got.LastName != "Agent" ||
		got.ForeName != "Ada" ||
		got.Initials != "AA" ||
		got.ORCID != "0000-0001-2345-6789" ||
		!slices.Equal(got.Affiliations, []string{"Deterministic Systems Lab, Example University"}) {
		t.Fatalf("first author = %#v", got)
	}
	if got := record.Authors[1]; got.DisplayName != "Evidence Consortium" ||
		got.CollectiveName != "Evidence Consortium" {
		t.Fatalf("collective author = %#v", got)
	}
	if record.AuthorsTruncated == nil || !*record.AuthorsTruncated {
		t.Fatalf("AuthorsTruncated = %v, want true from CompleteYN=N", record.AuthorsTruncated)
	}

	if record.Venue == nil ||
		record.Venue.DisplayName != "Journal of Reproducible Agents" ||
		record.Venue.ISOAbbreviation != "J Reprod Agents" ||
		record.Venue.ISSNL != "0028-0836" ||
		!slices.Equal(record.Venue.ISSN, []string{"0028-0836", "2049-3630"}) ||
		!slices.Equal(record.Venue.ISSNDetails, []source.VenueISSN{
			{Value: "0028-0836", Type: "Print"},
			{Value: "2049-3630", Type: "Electronic"},
		}) {
		t.Fatalf("Venue = %#v", record.Venue)
	}

	if len(record.MeSHHeadings) != 1 {
		t.Fatalf("MeSHHeadings = %#v", record.MeSHHeadings)
	}
	if got := record.MeSHHeadings[0]; got.Descriptor.Name != "Artificial Intelligence" ||
		got.Descriptor.UI != "D001185" ||
		!got.Descriptor.MajorTopic ||
		len(got.Qualifiers) != 1 ||
		got.Qualifiers[0].Name != "methods" ||
		got.Qualifiers[0].UI != "Q000379" {
		t.Fatalf("MeSH heading = %#v", got)
	}
	wantPublicationTypes := []source.PublicationType{
		{UI: "D016428", Name: "Journal Article"},
		{UI: "D000077182", Name: "Retracted Publication"},
	}
	if !slices.Equal(record.PublicationTypes, wantPublicationTypes) {
		t.Fatalf("PublicationTypes = %#v, want %#v", record.PublicationTypes, wantPublicationTypes)
	}

	assertTime(t, record.PublishedAt, time.Date(2026, time.July, 10, 0, 0, 0, 0, time.UTC))
	assertTime(t, record.ElectronicPublishedAt, time.Date(2026, time.July, 8, 0, 0, 0, 0, time.UTC))
	assertTime(t, record.CompletedAt, time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC))
	assertTime(t, record.RevisedAt, time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC))
	assertTime(t, record.UpdatedAt, time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC))

	wantRelations := []source.Relation{
		{Type: "RetractionIn", TargetID: "22345678", Note: "Retracted by"},
		{Type: "ErratumIn", TargetID: "32345678", Note: "Corrected by"},
		{Type: "UpdateIn", TargetID: "42345678", Note: "Updated by"},
		{Type: "CommentIn", TargetID: "52345678", Note: "Commented on by"},
	}
	if !slices.Equal(record.Relations, wantRelations) {
		t.Fatalf("Relations = %#v, want %#v", record.Relations, wantRelations)
	}
	if record.Retracted == nil || !*record.Retracted {
		t.Fatalf("Retracted = %v, want explicit true", record.Retracted)
	}
	if len(record.Licenses) != 1 ||
		record.Licenses[0].Name != "Creative Commons Attribution 4.0 International" ||
		record.Licenses[0].ID != "cc-by-4.0" ||
		record.Licenses[0].URL != "https://creativecommons.org/licenses/by/4.0/" {
		t.Fatalf("Licenses = %#v", record.Licenses)
	}
	if record.Scope.Status != source.ScopePending {
		t.Fatalf("Scope = %#v, want pending", record.Scope)
	}
}

func TestParseUnknownElementsDoNotCorruptKnownFields(t *testing.T) {
	t.Parallel()

	records, err := pubmed.Parse([]byte(`<?xml version="1.0"?>
<PubmedArticleSet>
  <FutureEnvelope><PMID>999</PMID></FutureEnvelope>
  <PubmedArticle>
    <MedlineCitation>
      <PMID>123</PMID>
      <FutureCitation><PMID>wrong</PMID></FutureCitation>
      <Article>
        <ArticleTitle>Known <FutureInline>title</FutureInline></ArticleTitle>
        <FutureArticleField>ignored</FutureArticleField>
      </Article>
    </MedlineCitation>
    <PubmedData><FutureData/></PubmedData>
  </PubmedArticle>
</PubmedArticleSet>`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(records) != 1 || records[0].SourceRecordID != "123" || records[0].Title != "Known title" {
		t.Fatalf("records = %#v, want known fields unaffected", records)
	}
}

func TestParseReportsMalformedRequiredPMIDWithRecordPosition(t *testing.T) {
	t.Parallel()

	_, err := pubmed.Parse([]byte(`<PubmedArticleSet>
<PubmedArticle><MedlineCitation><PMID>123</PMID><Article/></MedlineCitation></PubmedArticle>
<PubmedArticle><MedlineCitation><PMID>not-a-pmid</PMID><Article/></MedlineCitation></PubmedArticle>
</PubmedArticleSet>`))
	if err == nil {
		t.Fatal("Parse() accepted malformed required PMID")
	}
	if !strings.Contains(err.Error(), "record 2") || !strings.Contains(err.Error(), "required PMID") {
		t.Fatalf("Parse() error = %v, want required PMID and record position", err)
	}
}

func TestParseRelationAllowsAbsentPMIDButRejectsMalformedPMIDWithRecordPosition(t *testing.T) {
	t.Parallel()

	record, err := pubmed.ParseRecord([]byte(`<PubmedArticle>
  <MedlineCitation>
    <PMID>123</PMID>
    <Article/>
    <CommentsCorrectionsList>
      <CommentsCorrections RefType="CommentIn"><RefSource>Unindexed editorial comment</RefSource></CommentsCorrections>
    </CommentsCorrectionsList>
  </MedlineCitation>
</PubmedArticle>`))
	if err != nil {
		t.Fatalf("ParseRecord() error = %v", err)
	}
	want := []source.Relation{{
		Type: "CommentIn",
		Note: "Unindexed editorial comment",
	}}
	if !slices.Equal(record.Relations, want) {
		t.Fatalf("Relations = %#v, want %#v", record.Relations, want)
	}

	_, err = pubmed.Parse([]byte(`<PubmedArticleSet>
  <PubmedArticle><MedlineCitation><PMID>123</PMID><Article/></MedlineCitation></PubmedArticle>
  <PubmedArticle>
    <MedlineCitation>
      <PMID>456</PMID><Article/>
      <CommentsCorrectionsList>
        <CommentsCorrections RefType="UpdateIn"><RefSource>Malformed target</RefSource><PMID>bad-id</PMID></CommentsCorrections>
      </CommentsCorrectionsList>
    </MedlineCitation>
  </PubmedArticle>
</PubmedArticleSet>`))
	if err == nil {
		t.Fatal("Parse() accepted malformed optional relation PMID")
	}
	if !strings.Contains(err.Error(), "record 2") ||
		!strings.Contains(err.Error(), "relation") ||
		!strings.Contains(err.Error(), "bad-id") {
		t.Fatalf("Parse() error = %v, want record position and malformed relation PMID", err)
	}
}

func TestParseRejectsSourceInvalidELocationDOIFromIdentityWithProvenance(t *testing.T) {
	t.Parallel()

	record, err := pubmed.ParseRecord([]byte(`<PubmedArticle>
  <MedlineCitation>
    <PMID>123</PMID>
    <Article>
      <ArticleTitle>Invalidated DOI assertion</ArticleTitle>
      <ELocationID EIdType="doi" ValidYN="N">10.1000/must-not-merge</ELocationID>
    </Article>
  </MedlineCitation>
</PubmedArticle>`))
	if err != nil {
		t.Fatalf("ParseRecord() error = %v", err)
	}
	if record.Identity.Valid() {
		t.Fatalf("Identity = %q, source-invalid DOI must not become canonical", record.Identity)
	}
	if !slices.Equal(record.Identifiers, []source.Identifier{{
		Scheme: source.IdentifierPMID,
		Value:  "123",
	}}) {
		t.Fatalf("Identifiers = %#v, source-invalid DOI must not be asserted", record.Identifiers)
	}
	wantRejected := []source.RejectedIdentifierAssertion{{
		Scheme:     source.IdentifierDOI,
		Value:      "10.1000/must-not-merge",
		SourcePath: "/PubmedArticle/MedlineCitation/Article/ELocationID",
		Reason:     source.IdentifierRejectionSourceInvalid,
	}}
	if !slices.Equal(record.RejectedIdentifiers, wantRejected) {
		t.Fatalf("RejectedIdentifiers = %#v, want %#v", record.RejectedIdentifiers, wantRejected)
	}
}

func TestParseLeavesAbsentOptionalPubMedFieldsUnknown(t *testing.T) {
	t.Parallel()

	record, err := pubmed.ParseRecord([]byte(
		`<PubmedArticle><MedlineCitation><PMID>123</PMID><Article/></MedlineCitation></PubmedArticle>`,
	))
	if err != nil {
		t.Fatalf("ParseRecord() error = %v", err)
	}
	if record.Identity.Valid() ||
		record.PublishedAt != nil ||
		record.PublishedDate != nil ||
		record.ElectronicPublishedAt != nil ||
		record.ElectronicPublicationDate != nil ||
		record.CompletedAt != nil ||
		record.CompletedDate != nil ||
		record.RevisedAt != nil ||
		record.RevisionDate != nil ||
		record.UpdatedAt != nil ||
		record.AuthorsTruncated != nil ||
		record.Retracted != nil ||
		record.Venue != nil ||
		len(record.AbstractSections) != 0 ||
		len(record.Authors) != 0 ||
		len(record.MeSHHeadings) != 0 ||
		len(record.PublicationTypes) != 0 ||
		len(record.Relations) != 0 ||
		len(record.Licenses) != 0 {
		t.Fatalf("ParseRecord() invented optional values: %#v", record)
	}
}

func TestParsePreservesPartialAndMedlinePublicationDatesWithoutInventingDay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pubDate   string
		want      source.SourceDate
		precision source.DatePrecision
	}{
		{
			name:      "month precision",
			pubDate:   `<Year>2026</Year><Month>Jul</Month>`,
			want:      source.SourceDate{Year: 2026, Month: time.July, Precision: source.DatePrecisionMonth},
			precision: source.DatePrecisionMonth,
		},
		{
			name:      "year precision",
			pubDate:   `<Year>2026</Year>`,
			want:      source.SourceDate{Year: 2026, Precision: source.DatePrecisionYear},
			precision: source.DatePrecisionYear,
		},
		{
			name:      "MedlineDate text",
			pubDate:   `<MedlineDate>2025 Dec-2026 Jan</MedlineDate>`,
			want:      source.SourceDate{Raw: "2025 Dec-2026 Jan", Precision: source.DatePrecisionText},
			precision: source.DatePrecisionText,
		},
		{
			name:      "season precision",
			pubDate:   `<Year>2025</Year><Season>Winter</Season>`,
			want:      source.SourceDate{Year: 2025, Season: "Winter", Precision: source.DatePrecisionSeason},
			precision: source.DatePrecisionSeason,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			record, err := pubmed.ParseRecord([]byte(`<PubmedArticle>
  <MedlineCitation>
    <PMID>123</PMID>
    <Article>
      <Journal><JournalIssue><PubDate>` + tt.pubDate + `</PubDate></JournalIssue></Journal>
      <ArticleTitle>Partial date</ArticleTitle>
    </Article>
  </MedlineCitation>
</PubmedArticle>`))
			if err != nil {
				t.Fatalf("ParseRecord() error = %v", err)
			}
			if record.PublishedAt != nil {
				t.Fatalf("PublishedAt = %v, must not invent a complete date", record.PublishedAt)
			}
			if record.PublishedDate == nil || *record.PublishedDate != tt.want {
				t.Fatalf("PublishedDate = %#v, want %#v", record.PublishedDate, tt.want)
			}
			if record.PublishedDate.Precision != tt.precision {
				t.Fatalf("precision = %q, want %q", record.PublishedDate.Precision, tt.precision)
			}
		})
	}
}

func TestParsePublicationHistoryPreservesExplicitOrderedEvidence(t *testing.T) {
	t.Parallel()

	record, err := pubmed.ParseRecord([]byte(`<PubmedArticle>
  <MedlineCitation>
    <PMID>123</PMID>
    <Article PubModel=" Print-Electronic ">
      <ArticleTitle>Explicit publication history</ArticleTitle>
    </Article>
  </MedlineCitation>
  <PubmedData>
    <History>
      <PubMedPubDate PubStatus=" accepted ">
        <Year>2026</Year><Month>7</Month><Day>1</Day>
      </PubMedPubDate>
      <PubMedPubDate PubStatus=" aheadofprint ">
        <Year>2026</Year><Month>Jul</Month>
      </PubMedPubDate>
      <PubMedPubDate PubStatus=" epublish ">
        <Year>2026</Year>
      </PubMedPubDate>
      <PubMedPubDate PubStatus=" ppublish ">
        <Year>2026</Year><Month>July</Month><Day>15</Day>
      </PubMedPubDate>
      <PubMedPubDate PubStatus=" FutureStatus ">
        <Year>2027</Year><Month>2</Month><Day>3</Day>
      </PubMedPubDate>
    </History>
    <PublicationStatus> ppublish </PublicationStatus>
  </PubmedData>
</PubmedArticle>`))
	if err != nil {
		t.Fatalf("ParseRecord() error = %v", err)
	}

	if record.PublicationModel != "Print-Electronic" {
		t.Fatalf("PublicationModel = %q, want trimmed raw value", record.PublicationModel)
	}
	if record.PublicationStatus != "ppublish" {
		t.Fatalf("PublicationStatus = %q, want trimmed raw value", record.PublicationStatus)
	}
	wantHistory := []source.PublicationHistoryEntry{
		{
			Status: "accepted",
			Date: source.SourceDate{
				Year:      2026,
				Month:     time.July,
				Day:       1,
				Precision: source.DatePrecisionDay,
			},
			SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
			Ordinal:    1,
		},
		{
			Status: "aheadofprint",
			Date: source.SourceDate{
				Year:      2026,
				Month:     time.July,
				Precision: source.DatePrecisionMonth,
			},
			SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[2]",
			Ordinal:    2,
		},
		{
			Status: "epublish",
			Date: source.SourceDate{
				Year:      2026,
				Precision: source.DatePrecisionYear,
			},
			SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[3]",
			Ordinal:    3,
		},
		{
			Status: "ppublish",
			Date: source.SourceDate{
				Year:      2026,
				Month:     time.July,
				Day:       15,
				Precision: source.DatePrecisionDay,
			},
			SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[4]",
			Ordinal:    4,
		},
		{
			Status: "FutureStatus",
			Date: source.SourceDate{
				Year:      2027,
				Month:     time.February,
				Day:       3,
				Precision: source.DatePrecisionDay,
			},
			SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[5]",
			Ordinal:    5,
		},
	}
	if !slices.Equal(record.PublicationHistory, wantHistory) {
		t.Fatalf("PublicationHistory = %#v, want %#v", record.PublicationHistory, wantHistory)
	}

	wantEvidence := []source.FieldEvidence{
		{
			Field:      "publication_model",
			SourcePath: "/PubmedArticle/MedlineCitation/Article/@PubModel",
		},
		{
			Field:      "publication_status",
			SourcePath: "/PubmedArticle/PubmedData/PublicationStatus",
		},
		{
			Field:      "publication_history",
			SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate",
		},
	}
	for _, evidence := range wantEvidence {
		if !slices.Contains(record.Evidence, evidence) {
			t.Errorf("Evidence = %#v, missing %#v", record.Evidence, evidence)
		}
	}
}

func TestParsePublicationHistoryRejectsEntryWithoutDate(t *testing.T) {
	t.Parallel()

	_, err := pubmed.ParseRecord([]byte(`<PubmedArticle>
  <MedlineCitation><PMID>123</PMID><Article/></MedlineCitation>
  <PubmedData>
    <History>
      <PubMedPubDate PubStatus="accepted"/>
    </History>
  </PubmedData>
</PubmedArticle>`))
	if err == nil {
		t.Fatal("ParseRecord() accepted publication history evidence without a date")
	}
	if !strings.Contains(err.Error(), "publication history") ||
		!strings.Contains(err.Error(), "PubMedPubDate[1]") ||
		!strings.Contains(err.Error(), "requires a date") {
		t.Fatalf("ParseRecord() error = %v, want explicit missing history date error", err)
	}
}

func TestParsePublicationHistoryRejectsInvalidShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		statusAttribute string
		date            string
		wantReason      string
	}{
		{
			name:       "missing PubStatus",
			date:       `<Year>2026</Year>`,
			wantReason: "requires a non-empty PubStatus",
		},
		{
			name:            "blank PubStatus",
			statusAttribute: ` PubStatus="   "`,
			date:            `<Year>2026</Year>`,
			wantReason:      "requires a non-empty PubStatus",
		},
		{
			name:            "season precision",
			statusAttribute: ` PubStatus="accepted"`,
			date:            `<Year>2026</Year><Season>Summer</Season>`,
			wantReason:      `date precision "season" is not supported`,
		},
		{
			name:            "MedlineDate text precision",
			statusAttribute: ` PubStatus="accepted"`,
			date:            `<MedlineDate>2026 Jul-Aug</MedlineDate>`,
			wantReason:      `date precision "text" is not supported`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pubmed.ParseRecord([]byte(`<PubmedArticle>
  <MedlineCitation><PMID>123</PMID><Article/></MedlineCitation>
  <PubmedData>
    <History>
      <PubMedPubDate` + tt.statusAttribute + `>` + tt.date + `</PubMedPubDate>
    </History>
  </PubmedData>
</PubmedArticle>`))
			if err == nil {
				t.Fatal("ParseRecord() accepted invalid publication history shape")
			}
			if !strings.Contains(err.Error(), "publication history") ||
				!strings.Contains(err.Error(), "PubMedPubDate[1]") ||
				!strings.Contains(err.Error(), tt.wantReason) {
				t.Fatalf(
					"ParseRecord() error = %v, want ordinal and reason %q",
					err,
					tt.wantReason,
				)
			}
		})
	}
}

func TestParsePublicationHistoryDoesNotInferAheadOfPrintFromElectronicArticleDate(t *testing.T) {
	t.Parallel()

	record, err := pubmed.ParseRecord([]byte(`<PubmedArticle>
  <MedlineCitation>
    <PMID>123</PMID>
    <Article PubModel="Electronic">
      <ArticleTitle>Ahead of print wording is not evidence</ArticleTitle>
      <ArticleDate DateType="Electronic">
        <Year>2026</Year><Month>7</Month><Day>8</Day>
      </ArticleDate>
    </Article>
  </MedlineCitation>
  <PubmedData/>
</PubmedArticle>`))
	if err != nil {
		t.Fatalf("ParseRecord() error = %v", err)
	}
	if record.ElectronicPublicationDate == nil {
		t.Fatal("ElectronicPublicationDate = nil, want explicit ArticleDate evidence retained")
	}
	if len(record.PublicationHistory) != 0 {
		t.Fatalf("PublicationHistory = %#v, must not synthesize aheadofprint", record.PublicationHistory)
	}
}

func assertTime(t *testing.T, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil || !got.Equal(want) {
		t.Fatalf("time = %v, want %v", got, want)
	}
}
