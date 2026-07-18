package ingestion

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

func TestNewEnvelopeCreatesValidatedUpsertEvent(t *testing.T) {
	t.Parallel()

	raw := mustRawRecord(t, `{"id":"W1","title":"Durable ingestion"}`)
	record := source.Record{
		Source:         source.OpenAlex,
		SourceRecordID: "W1",
		Raw:            raw,
		Title:          "Durable ingestion",
	}
	sourceTime := time.Date(2026, time.July, 16, 9, 30, 0, 0, time.FixedZone("CST", 8*60*60))

	envelope, err := NewEnvelope(
		source.OpenAlex,
		"openalex:W1",
		sourceTime,
		"page-0001/item-0001",
		1,
		record,
		raw,
	)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}

	if envelope.Kind() != EventKindUpsert {
		t.Fatalf("Kind() = %q, want %q", envelope.Kind(), EventKindUpsert)
	}
	if envelope.LogicalSource != source.OpenAlex ||
		envelope.EventKey != "openalex:W1" ||
		envelope.TieBreakKey != "page-0001/item-0001" ||
		envelope.Position != 1 {
		t.Fatalf("Envelope metadata = %#v", envelope)
	}
	if !envelope.SourceTime.Equal(sourceTime) || envelope.SourceTime.Location() != time.UTC {
		t.Fatalf("SourceTime = %v, want UTC instant %v", envelope.SourceTime, sourceTime.UTC())
	}
	if envelope.Record.Source != source.OpenAlex ||
		envelope.Record.SourceRecordID != "W1" ||
		envelope.Record.Title != "Durable ingestion" {
		t.Fatalf("Record = %#v", envelope.Record)
	}
	if !bytes.Equal(envelope.Raw.Payload, raw.Payload) || envelope.Raw.SHA256 != raw.SHA256 {
		t.Fatalf("Raw = %#v, want %#v", envelope.Raw, raw)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestNewDeletionEnvelopeCreatesValidatedDeleteEvent(t *testing.T) {
	t.Parallel()

	raw := mustRawRecord(t, `{"delete":"12345678"}`)
	sourceTime := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)

	envelope, err := NewDeletionEnvelope(
		source.PubMed,
		"pubmed:12345678",
		sourceTime,
		"pubmed26n0002.xml.gz/00000002",
		2,
		raw,
	)
	if err != nil {
		t.Fatalf("NewDeletionEnvelope() error = %v", err)
	}

	if envelope.Kind() != EventKindDelete {
		t.Fatalf("Kind() = %q, want %q", envelope.Kind(), EventKindDelete)
	}
	if envelope.LogicalSource != source.PubMed ||
		envelope.EventKey != "pubmed:12345678" ||
		envelope.TieBreakKey != "pubmed26n0002.xml.gz/00000002" ||
		envelope.Position != 2 ||
		!envelope.SourceTime.Equal(sourceTime) {
		t.Fatalf("DeletionEnvelope = %#v", envelope)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestEnvelopeConstructorsRejectInvalidMetadataAndSourceMismatch(t *testing.T) {
	t.Parallel()

	raw := mustRawRecord(t, `{"id":"W1"}`)
	validRecord := source.Record{
		Source:         source.OpenAlex,
		SourceRecordID: "W1",
		Raw:            raw,
	}
	now := time.Date(2026, time.July, 16, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name          string
		logicalSource string
		eventKey      string
		sourceTime    time.Time
		tieBreakKey   string
		position      int64
		record        source.Record
		raw           source.RawRecord
	}{
		{
			name:        "missing logical source",
			eventKey:    "openalex:W1",
			sourceTime:  now,
			tieBreakKey: "1",
			position:    1,
			record:      validRecord,
			raw:         raw,
		},
		{
			name:          "missing event key",
			logicalSource: source.OpenAlex,
			sourceTime:    now,
			tieBreakKey:   "1",
			position:      1,
			record:        validRecord,
			raw:           raw,
		},
		{
			name:          "zero source time",
			logicalSource: source.OpenAlex,
			eventKey:      "openalex:W1",
			tieBreakKey:   "1",
			position:      1,
			record:        validRecord,
			raw:           raw,
		},
		{
			name:          "missing tie break key",
			logicalSource: source.OpenAlex,
			eventKey:      "openalex:W1",
			sourceTime:    now,
			position:      1,
			record:        validRecord,
			raw:           raw,
		},
		{
			name:          "nonpositive position",
			logicalSource: source.OpenAlex,
			eventKey:      "openalex:W1",
			sourceTime:    now,
			tieBreakKey:   "1",
			record:        validRecord,
			raw:           raw,
		},
		{
			name:          "record source mismatch",
			logicalSource: source.OpenAlex,
			eventKey:      "openalex:W1",
			sourceTime:    now,
			tieBreakKey:   "1",
			position:      1,
			record: source.Record{
				Source:         source.Crossref,
				SourceRecordID: "W1",
				Raw:            raw,
			},
			raw: raw,
		},
		{
			name:          "missing record source id",
			logicalSource: source.OpenAlex,
			eventKey:      "openalex:W1",
			sourceTime:    now,
			tieBreakKey:   "1",
			position:      1,
			record: source.Record{
				Source: source.OpenAlex,
				Raw:    raw,
			},
			raw: raw,
		},
		{
			name:          "missing raw payload",
			logicalSource: source.OpenAlex,
			eventKey:      "openalex:W1",
			sourceTime:    now,
			tieBreakKey:   "1",
			position:      1,
			record:        validRecord,
		},
		{
			name:          "raw differs from record raw",
			logicalSource: source.OpenAlex,
			eventKey:      "openalex:W1",
			sourceTime:    now,
			tieBreakKey:   "1",
			position:      1,
			record:        validRecord,
			raw:           mustRawRecord(t, `{"id":"W1","revision":2}`),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := NewEnvelope(
				tt.logicalSource,
				tt.eventKey,
				tt.sourceTime,
				tt.tieBreakKey,
				tt.position,
				tt.record,
				tt.raw,
			); err == nil {
				t.Fatalf("NewEnvelope() = %#v, want error", got)
			}
		})
	}

	if got, err := NewDeletionEnvelope(
		source.PubMed,
		"pubmed:123",
		time.Time{},
		"1",
		1,
		raw,
	); err == nil {
		t.Fatalf("NewDeletionEnvelope() = %#v, want zero-time error", got)
	}
}

func TestEnvelopeRejectsNonCanonicalSource(t *testing.T) {
	t.Parallel()

	raw := mustRawRecord(t, `{"id":"W1"}`)
	now := time.Date(2026, time.July, 18, 9, 30, 0, 0, time.UTC)
	testCases := []struct {
		name          string
		logicalSource string
		recordSource  string
	}{
		{
			name:          "record source leading whitespace",
			logicalSource: source.OpenAlex,
			recordSource:  " " + source.OpenAlex,
		},
		{
			name:          "record source trailing whitespace",
			logicalSource: source.OpenAlex,
			recordSource:  source.OpenAlex + " ",
		},
		{
			name:          "logical source whitespace",
			logicalSource: " " + source.OpenAlex + " ",
			recordSource:  source.OpenAlex,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewEnvelope(
				testCase.logicalSource,
				"openalex:W1",
				now,
				"1",
				1,
				source.Record{
					Source:         testCase.recordSource,
					SourceRecordID: "W1",
					Raw:            raw,
				},
				raw,
			)
			if err == nil ||
				!strings.Contains(err.Error(), "source") {
				t.Fatalf(
					"NewEnvelope(non-canonical source) error = %v, want source validation error",
					err,
				)
			}
		})
	}
}

func TestNewEnvelopeDeepCopiesNestedRecordAndRawPayload(t *testing.T) {
	t.Parallel()

	raw := mustRawRecord(t, `{"id":"W1","nested":{"value":1}}`)
	publishedAt := time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC)
	publishedDate := source.SourceDate{Year: 2026, Month: time.July, Day: 15}
	score := 0.75
	authorsTruncated := true
	citedByCount := 10
	retracted := false
	isOA := true
	hasFulltext := true
	record := source.Record{
		Source:              source.OpenAlex,
		SourceRecordID:      "W1",
		Raw:                 raw,
		Identifiers:         []source.Identifier{{Scheme: source.IdentifierDOI, Value: "10.1000/test"}},
		RejectedIdentifiers: []source.RejectedIdentifierAssertion{{Scheme: source.IdentifierPMID, Value: "bad"}},
		Evidence:            []source.FieldEvidence{{Field: "title", SourcePath: "title"}},
		AbstractSections:    []source.AbstractSection{{Label: "Background", Text: "Text"}},
		PublishedAt:         &publishedAt,
		PublishedDate:       &publishedDate,
		Authors: []source.Author{{
			DisplayName:  "Ada",
			Affiliations: []string{"Institute A"},
			Institutions: []source.Institution{{DisplayName: "Institute A"}},
		}},
		AuthorsTruncated: &authorsTruncated,
		MeSHHeadings: []source.MeSHHeading{{
			Descriptor: source.MeSHTerm{UI: "D1", Name: "AI"},
			Qualifiers: []source.MeSHTerm{{UI: "Q1", Name: "methods"}},
		}},
		PublicationTypes: []source.PublicationType{{UI: "PT1", Name: "Journal Article"}},
		Relations:        []source.Relation{{Type: "updates", TargetID: "W0"}},
		Topics:           []source.Topic{{OpenAlexID: "T1", DisplayName: "AI", Score: &score}},
		Keywords:         []source.Keyword{{OpenAlexID: "K1", DisplayName: "agents", Score: &score}},
		CitedByCount:     &citedByCount,
		Venue: &source.Venue{
			OpenAlexID:  "S1",
			DisplayName: "Journal",
			ISSN:        []string{"1234-5679"},
			ISSNDetails: []source.VenueISSN{{Value: "1234-5679", Type: "print"}},
		},
		OpenAccess: source.OpenAccess{
			IsOA:                     &isOA,
			AnyRepositoryHasFulltext: &hasFulltext,
		},
		Licenses:  []source.License{{Name: "CC BY", URL: "https://license.invalid"}},
		Retracted: &retracted,
		CodeURLs:  []string{"https://code.invalid/repository"},
		Scope: source.ScopeDecision{
			Status:   source.ScopeIncluded,
			Reason:   "fixture",
			Evidence: []source.FieldEvidence{{Field: "title", SourcePath: "title"}},
		},
	}

	envelope, err := NewEnvelope(
		source.OpenAlex,
		"openalex:W1",
		time.Date(2026, time.July, 16, 9, 30, 0, 0, time.UTC),
		"1",
		1,
		record,
		raw,
	)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}

	raw.Payload[0] = '['
	record.Raw.Payload[1] = '!'
	record.Identifiers[0].Value = "changed"
	record.RejectedIdentifiers[0].Value = "changed"
	record.Evidence[0].Field = "changed"
	record.AbstractSections[0].Text = "changed"
	*record.PublishedAt = record.PublishedAt.Add(24 * time.Hour)
	record.PublishedDate.Year = 1999
	record.Authors[0].Affiliations[0] = "changed"
	record.Authors[0].Institutions[0].DisplayName = "changed"
	*record.AuthorsTruncated = false
	record.MeSHHeadings[0].Qualifiers[0].Name = "changed"
	record.PublicationTypes[0].Name = "changed"
	record.Relations[0].TargetID = "changed"
	*record.Topics[0].Score = 0
	*record.Keywords[0].Score = 0
	*record.CitedByCount = 0
	record.Venue.ISSN[0] = "changed"
	record.Venue.ISSNDetails[0].Value = "changed"
	*record.OpenAccess.IsOA = false
	*record.OpenAccess.AnyRepositoryHasFulltext = false
	record.Licenses[0].Name = "changed"
	*record.Retracted = true
	record.CodeURLs[0] = "changed"
	record.Scope.Evidence[0].Field = "changed"

	if envelope.Raw.Payload[0] != '{' || envelope.Record.Raw.Payload[1] != '"' {
		t.Fatal("Envelope retained a mutable alias to raw payload")
	}
	if envelope.Record.Identifiers[0].Value != "10.1000/test" ||
		envelope.Record.RejectedIdentifiers[0].Value != "bad" ||
		envelope.Record.Evidence[0].Field != "title" ||
		envelope.Record.AbstractSections[0].Text != "Text" {
		t.Fatal("Envelope retained a mutable alias to top-level record slices")
	}
	if !envelope.Record.PublishedAt.Equal(time.Date(2026, time.July, 15, 8, 0, 0, 0, time.UTC)) ||
		envelope.Record.PublishedDate.Year != 2026 ||
		envelope.Record.Authors[0].Affiliations[0] != "Institute A" ||
		envelope.Record.Authors[0].Institutions[0].DisplayName != "Institute A" ||
		!*envelope.Record.AuthorsTruncated {
		t.Fatal("Envelope retained a mutable alias to nested author or date state")
	}
	if envelope.Record.MeSHHeadings[0].Qualifiers[0].Name != "methods" ||
		envelope.Record.PublicationTypes[0].Name != "Journal Article" ||
		envelope.Record.Relations[0].TargetID != "W0" ||
		*envelope.Record.Topics[0].Score != 0.75 ||
		*envelope.Record.Keywords[0].Score != 0.75 ||
		*envelope.Record.CitedByCount != 10 {
		t.Fatal("Envelope retained a mutable alias to nested scientific metadata")
	}
	if envelope.Record.Venue.ISSN[0] != "1234-5679" ||
		envelope.Record.Venue.ISSNDetails[0].Value != "1234-5679" ||
		!*envelope.Record.OpenAccess.IsOA ||
		!*envelope.Record.OpenAccess.AnyRepositoryHasFulltext ||
		envelope.Record.Licenses[0].Name != "CC BY" ||
		*envelope.Record.Retracted ||
		envelope.Record.CodeURLs[0] != "https://code.invalid/repository" ||
		envelope.Record.Scope.Evidence[0].Field != "title" {
		t.Fatal("Envelope retained a mutable alias to nested projection metadata")
	}
}

func TestEnvelopeAndCloneDeepCopyPublicationHistory(t *testing.T) {
	t.Parallel()

	raw := mustRawRecord(t, `{"pmid":"123"}`)
	record := source.Record{
		Source:         source.PubMed,
		SourceRecordID: "123",
		Raw:            raw,
		PublicationHistory: []source.PublicationHistoryEntry{{
			Status: "accepted",
			Date: source.SourceDate{
				Year:      2026,
				Month:     time.July,
				Day:       1,
				Precision: source.DatePrecisionDay,
			},
			SourcePath: "/PubmedArticle/PubmedData/History/PubMedPubDate[1]",
			Ordinal:    1,
		}},
	}
	envelope, err := NewEnvelope(
		source.PubMed,
		"pubmed:123",
		time.Date(2026, time.July, 16, 9, 30, 0, 0, time.UTC),
		"1",
		1,
		record,
		raw,
	)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	cloned := envelope.Clone()

	record.PublicationHistory[0].Status = "changed-original"
	record.PublicationHistory[0].Date.Year = 1999
	if got := envelope.Record.PublicationHistory[0]; got.Status != "accepted" || got.Date.Year != 2026 {
		t.Fatalf("Envelope PublicationHistory = %#v, retained original Record alias", got)
	}

	envelope.Record.PublicationHistory[0].Status = "changed-envelope"
	envelope.Record.PublicationHistory[0].Date.Month = time.December
	if got := cloned.Record.PublicationHistory[0]; got.Status != "accepted" || got.Date.Month != time.July {
		t.Fatalf("Clone PublicationHistory = %#v, retained Envelope alias", got)
	}
}

func TestNewDeletionEnvelopeDeepCopiesRawPayload(t *testing.T) {
	t.Parallel()

	raw := mustRawRecord(t, `{"delete":"123"}`)
	envelope, err := NewDeletionEnvelope(
		source.PubMed,
		"pubmed:123",
		time.Date(2026, time.July, 16, 9, 30, 0, 0, time.UTC),
		"1",
		1,
		raw,
	)
	if err != nil {
		t.Fatalf("NewDeletionEnvelope() error = %v", err)
	}

	raw.Payload[0] = '['
	if envelope.Raw.Payload[0] != '{' {
		t.Fatal("DeletionEnvelope retained a mutable alias to raw payload")
	}
}

func TestEventKindValidationIsClosed(t *testing.T) {
	t.Parallel()

	if !EventKindUpsert.Valid() || !EventKindDelete.Valid() {
		t.Fatal("defined event kinds are invalid")
	}
	if EventKind("").Valid() || EventKind("replace").Valid() {
		t.Fatal("undefined event kind was accepted")
	}
	if !strings.Contains(EventKindDelete.String(), "delete") {
		t.Fatalf("EventKindDelete.String() = %q", EventKindDelete.String())
	}
}

func mustRawRecord(t *testing.T, payload string) source.RawRecord {
	t.Helper()

	raw, err := source.NewRawRecord([]byte(payload))
	if err != nil {
		t.Fatalf("source.NewRawRecord() error = %v", err)
	}
	return raw
}
