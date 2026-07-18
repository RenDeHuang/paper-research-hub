package openalex_test

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
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source/openalex"
)

func TestOpenAlexParserVersionIsExplicitAndAttachedToRecords(t *testing.T) {
	t.Parallel()

	if openalex.ParserVersion != "openalex/works-v1" {
		t.Fatalf("ParserVersion = %q", openalex.ParserVersion)
	}
	record, err := openalex.Parse(fixtureWork(t))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if record.ParserVersion != openalex.ParserVersion {
		t.Fatalf(
			"record.ParserVersion = %q, want %q",
			record.ParserVersion,
			openalex.ParserVersion,
		)
	}
}

func TestParseMapsOpenAlexRecordWithoutInventingValues(t *testing.T) {
	t.Parallel()

	raw := fixtureWork(t)
	record, err := openalex.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if record.Source != source.OpenAlex {
		t.Fatalf("Source = %q, want %q", record.Source, source.OpenAlex)
	}
	if record.SourceRecordID != "W1234567890" {
		t.Fatalf("SourceRecordID = %q, want normalized OpenAlex ID", record.SourceRecordID)
	}
	if record.Identity.CanonicalKey() != "doi:10.1234/agent.1" {
		t.Fatalf("Identity = %q, want DOI precedence", record.Identity.CanonicalKey())
	}
	if !bytes.Equal(record.Raw.Payload, raw) {
		t.Fatal("Raw.Payload did not preserve the original single-record JSON")
	}
	if record.Raw.SHA256 != "8689ada40136504d7bb2cd4026643737b092dc9cfa56ce4239c8dada95b236d5" {
		t.Fatalf("Raw.SHA256 = %q, want fixture canonical hash", record.Raw.SHA256)
	}

	wantIdentifiers := []source.Identifier{
		{Scheme: source.IdentifierOpenAlex, Value: "W1234567890"},
		{Scheme: source.IdentifierDOI, Value: "10.1234/agent.1"},
		{Scheme: source.IdentifierArXiv, Value: "2401.01234"},
		{Scheme: source.IdentifierPMID, Value: "12345678"},
		{Scheme: source.IdentifierPMCID, Value: "PMC7654321"},
	}
	if !slices.Equal(record.Identifiers, wantIdentifiers) {
		t.Fatalf("Identifiers = %#v, want %#v", record.Identifiers, wantIdentifiers)
	}
	if record.Title != "Deterministic Agent Systems" {
		t.Fatalf("Title = %q", record.Title)
	}
	if record.Abstract != "Agents are strictly reproducible" {
		t.Fatalf("Abstract = %q, want reconstructed abstract", record.Abstract)
	}

	assertTime(t, record.PublishedAt, time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC))
	assertTime(t, record.CreatedAt, time.Date(2026, time.July, 10, 0, 0, 0, 0, time.UTC))
	assertTime(t, record.UpdatedAt, time.Date(2026, time.July, 16, 12, 34, 56, 0, time.UTC))

	if len(record.Authors) != 2 {
		t.Fatalf("Authors = %#v, want 2", record.Authors)
	}
	if got := record.Authors[0]; got.DisplayName != "Ada Agent" ||
		got.OpenAlexID != "A111" ||
		got.ORCID != "0000-0001-2345-6789" ||
		got.Position != 1 ||
		!got.IsCorresponding ||
		len(got.Institutions) != 1 {
		t.Fatalf("first author = %#v, want mapped authorship", got)
	}
	if got := record.Authors[0].Institutions[0]; got.DisplayName != "Deterministic Systems Lab" ||
		got.OpenAlexID != "I111" ||
		got.ROR != "https://ror.org/012345678" ||
		got.CountryCode != "CN" {
		t.Fatalf("institution = %#v, want mapped institution", got)
	}

	if len(record.Topics) != 1 ||
		record.Topics[0].DisplayName != "Autonomous Agents" ||
		record.Topics[0].Score == nil ||
		*record.Topics[0].Score != 0.97 ||
		record.Topics[0].Field.DisplayName != "Computer Science" {
		t.Fatalf("Topics = %#v, want mapped topic hierarchy", record.Topics)
	}
	if len(record.Keywords) != 1 ||
		record.Keywords[0].DisplayName != "LLM agent" ||
		record.Keywords[0].Score == nil ||
		*record.Keywords[0].Score != 0.88 {
		t.Fatalf("Keywords = %#v, want mapped keyword", record.Keywords)
	}
	if record.CitedByCount == nil || *record.CitedByCount != 42 {
		t.Fatalf("CitedByCount = %v, want 42", record.CitedByCount)
	}

	if record.Venue == nil ||
		record.Venue.OpenAlexID != "S987654321" ||
		record.Venue.DisplayName != "Journal of Deterministic Systems" ||
		record.Venue.ISSNL != "1234-567X" ||
		!slices.Equal(record.Venue.ISSN, []string{"1234-567X", "2049-3630"}) {
		t.Fatalf("Venue = %#v, want source identifiers and ISSNs", record.Venue)
	}
	if record.OpenAccess.IsOA == nil || !*record.OpenAccess.IsOA ||
		record.OpenAccess.Status != "gold" ||
		record.OpenAccess.URL != "https://example.test/paper.pdf" ||
		record.OpenAccess.AnyRepositoryHasFulltext == nil ||
		!*record.OpenAccess.AnyRepositoryHasFulltext {
		t.Fatalf("OpenAccess = %#v, want mapped OA state", record.OpenAccess)
	}
	if len(record.Licenses) != 2 {
		t.Fatalf("Licenses = %#v, want distinct explicit licenses", record.Licenses)
	}
	if record.Retracted == nil || *record.Retracted {
		t.Fatalf("Retracted = %v, want explicit false", record.Retracted)
	}
	if record.AuthorsTruncated == nil || !*record.AuthorsTruncated {
		t.Fatalf("AuthorsTruncated = %v, want explicit true", record.AuthorsTruncated)
	}
	if !slices.Equal(record.CodeURLs, []string{"https://github.com/example-org/agent-system"}) {
		t.Fatalf("CodeURLs = %#v, want only verified GitHub repository URL", record.CodeURLs)
	}

	if record.Scope.Status != source.ScopePending ||
		record.Scope.Reason != source.ScopeReasonAwaitingDeterministicEvaluation ||
		len(record.Scope.Evidence) != 0 {
		t.Fatalf("Scope = %#v, want pending without title guessing", record.Scope)
	}
	for _, field := range []string{
		"title",
		"identifiers",
		"authors",
		"authors_truncated",
		"institutions",
		"published_at",
		"updated_at",
		"abstract",
		"topics",
		"keywords",
		"cited_by_count",
		"venue",
		"open_access",
		"licenses",
		"retracted",
		"code_urls",
	} {
		if !hasEvidence(record.Evidence, field) {
			t.Errorf("Evidence missing field %q: %#v", field, record.Evidence)
		}
	}
}

func TestParseLeavesMissingOptionalFieldsAbsent(t *testing.T) {
	t.Parallel()

	record, err := openalex.Parse(json.RawMessage(`{"id":"https://openalex.org/W1"}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if record.Identity.CanonicalKey() != "openalex:W1" {
		t.Fatalf("Identity = %q, want required OpenAlex identity", record.Identity.CanonicalKey())
	}
	if record.Title != "" ||
		record.Abstract != "" ||
		record.PublishedAt != nil ||
		record.CreatedAt != nil ||
		record.UpdatedAt != nil ||
		record.CitedByCount != nil ||
		record.Retracted != nil ||
		record.AuthorsTruncated != nil ||
		record.Venue != nil ||
		record.OpenAccess.IsOA != nil ||
		len(record.Authors) != 0 ||
		len(record.Topics) != 0 ||
		len(record.Keywords) != 0 ||
		len(record.Licenses) != 0 ||
		len(record.CodeURLs) != 0 {
		t.Fatalf("Parse() invented optional values: %#v", record)
	}
	if record.Scope.Status != source.ScopePending {
		t.Fatalf("Scope = %#v, want pending", record.Scope)
	}
}

func TestParsePreservesExplicitFalseAuthorsTruncated(t *testing.T) {
	t.Parallel()

	record, err := openalex.Parse(json.RawMessage(`{
		"id":"https://openalex.org/W1",
		"is_authors_truncated":false
	}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if record.AuthorsTruncated == nil || *record.AuthorsTruncated {
		t.Fatalf("AuthorsTruncated = %v, want explicit false", record.AuthorsTruncated)
	}
	if !hasEvidence(record.Evidence, "authors_truncated") {
		t.Fatalf("Evidence missing authors_truncated: %#v", record.Evidence)
	}
}

func TestParseRejectsMalformedJSONAndRequiredOpenAlexID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "malformed JSON", raw: `{"id":`, want: "JSON"},
		{name: "missing ID", raw: `{"title":"No identity"}`, want: "OpenAlex ID"},
		{name: "malformed ID", raw: `{"id":"https://openalex.org/A123"}`, want: "OpenAlex ID"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got, err := openalex.Parse(json.RawMessage(tt.raw)); err == nil {
				t.Fatalf("Parse(%s) = %#v, want error", tt.raw, got)
			} else if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParseRejectsIdentityConflictsExplicitly(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`{
			"id":"https://openalex.org/W1",
			"ids":{"openalex":"https://openalex.org/W2"}
		}`,
		`{
			"id":"https://openalex.org/W1",
			"doi":"https://doi.org/10.1000/one",
			"ids":{"doi":"https://doi.org/10.1000/two"}
		}`,
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			got, err := openalex.Parse(json.RawMessage(raw))
			if !errors.Is(err, paper.ErrConflictingIdentifiers) {
				t.Fatalf("Parse() = %#v, %v, want ErrConflictingIdentifiers", got, err)
			}
			if got.Identity.Valid() {
				t.Fatalf("Parse() returned identity %q on conflict", got.Identity)
			}
		})
	}
}

func TestParseRejectsMalformedAbstractIndex(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		`{"id":"https://openalex.org/W1","abstract_inverted_index":{"one":[0],"two":[0]}}`,
		`{"id":"https://openalex.org/W1","abstract_inverted_index":{"one":[0],"three":[2]}}`,
		`{"id":"https://openalex.org/W1","abstract_inverted_index":{"one":[-1]}}`,
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()

			if got, err := openalex.Parse(json.RawMessage(raw)); err == nil {
				t.Fatalf("Parse() = %#v, want malformed abstract error", got)
			}
		})
	}
}

func fixtureWork(t *testing.T) json.RawMessage {
	t.Helper()

	payload, err := os.ReadFile("testdata/works.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var envelope struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("fixture json.Unmarshal() error = %v", err)
	}
	if len(envelope.Results) != 1 {
		t.Fatalf("fixture results = %d, want 1", len(envelope.Results))
	}
	return envelope.Results[0]
}

func assertTime(t *testing.T, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil || !got.Equal(want) {
		t.Fatalf("time = %v, want %v", got, want)
	}
}

func hasEvidence(evidence []source.FieldEvidence, field string) bool {
	for _, item := range evidence {
		if item.Field == field && item.SourcePath != "" {
			return true
		}
	}
	return false
}
