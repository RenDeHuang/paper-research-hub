package ingestion

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

func TestNormalizedPayloadV4PreservesCompleteSourceAssertion(t *testing.T) {
	t.Parallel()

	if normalizedPayloadSchemaVersion != "normalized-record/v4" {
		t.Fatalf(
			"normalizedPayloadSchemaVersion = %q, want normalized-record/v4",
			normalizedPayloadSchemaVersion,
		)
	}
	identity, err := paper.NewIdentifier(
		paper.SchemeDOI,
		"10.1000/normalized-v4",
	)
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	publishedAt := time.Date(2026, time.July, 1, 1, 2, 3, 0, time.UTC)
	electronicAt := publishedAt.Add(time.Hour)
	createdAt := publishedAt.Add(2 * time.Hour)
	completedAt := publishedAt.Add(3 * time.Hour)
	updatedAt := publishedAt.Add(4 * time.Hour)
	revisedAt := publishedAt.Add(5 * time.Hour)
	record := source.Record{
		Source:         source.PubMed,
		SourceRecordID: "12345678",
		ParserVersion:  "pubmed/pubmed-article-v1",
		Identity:       identity,
		Identifiers: []source.Identifier{
			{Scheme: source.IdentifierDOI, Value: "10.1000/normalized-v4"},
			{Scheme: source.IdentifierPMID, Value: "12345678"},
		},
		RejectedIdentifiers: []source.RejectedIdentifierAssertion{{
			Scheme:     source.IdentifierDOI,
			Value:      "10.1000/rejected",
			SourcePath: "/PubmedArticle/PubmedData/ArticleIdList/ArticleId[2]",
			Reason:     source.IdentifierRejectionSourceInvalid,
		}},
		Title:                "Complete normalized assertion",
		Abstract:             "Complete abstract.",
		Publisher:            "Evidence Publisher",
		CopyrightInformation: "Copyright evidence",
		PublicationModel:     "Electronic",
		PublicationStatus:    "epublish",
		PublishedAt:          &publishedAt,
		PublishedDate: &source.SourceDate{
			Year:      2026,
			Month:     time.July,
			Day:       1,
			Precision: source.DatePrecisionDay,
		},
		ElectronicPublishedAt: &electronicAt,
		ElectronicPublicationDate: &source.SourceDate{
			Year:      2026,
			Month:     time.July,
			Precision: source.DatePrecisionMonth,
		},
		CreatedAt:   &createdAt,
		CompletedAt: &completedAt,
		CompletedDate: &source.SourceDate{
			Year:      2026,
			Precision: source.DatePrecisionYear,
		},
		UpdatedAt: &updatedAt,
		RevisedAt: &revisedAt,
		RevisionDate: &source.SourceDate{
			Raw:       "Summer 2026",
			Season:    "Summer",
			Precision: source.DatePrecisionSeason,
		},
		Authors: []source.Author{{
			DisplayName:  "Ada Evidence",
			ORCID:        "0000-0001-2345-6789",
			Position:     1,
			Affiliations: []string{"Evidence Institute"},
			Institutions: []source.Institution{{
				DisplayName: "Evidence Institute",
				ROR:         "https://ror.org/000000001",
			}},
		}},
		Relations: []source.Relation{{
			Type:     "is-update-of",
			TargetID: "10.1000/prior",
			Note:     "updated article",
		}},
		Venue: &source.Venue{
			DisplayName: "Evidence Journal",
			Type:        "journal",
			ISSN:        []string{"0028-0836"},
		},
		OpenAccess: source.OpenAccess{
			IsOA:   boolPointer(true),
			Status: "gold",
			URL:    "https://publisher.example.test/article",
		},
		Licenses: []source.License{{
			Name:       "CC BY 4.0",
			URL:        "https://creativecommons.org/licenses/by/4.0/",
			SourcePath: "/PubmedArticle/PubmedData/License",
		}},
		CodeURLs: []string{"https://github.com/example/normalized-v4"},
		Evidence: []source.FieldEvidence{{
			Field:      "abstract",
			SourcePath: "/PubmedArticle/MedlineCitation/Article/Abstract",
		}},
	}

	encoded, err := json.Marshal(recordPayload(record))
	if err != nil {
		t.Fatalf("Marshal(recordPayload) error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("Unmarshal(recordPayload) error = %v", err)
	}
	required := []string{
		"source",
		"source_record_id",
		"parser_version",
		"canonical_key",
		"identifiers",
		"rejected_identifiers",
		"title",
		"abstract",
		"publisher",
		"copyright_information",
		"publication_model",
		"publication_status",
		"published_at",
		"published_date",
		"electronic_published_at",
		"electronic_publication_date",
		"created_at",
		"completed_at",
		"completed_date",
		"updated_at",
		"revised_at",
		"revision_date",
		"authors",
		"relations",
		"venue",
		"open_access",
		"licenses",
		"code_urls",
		"evidence",
	}
	for _, key := range required {
		if _, exists := payload[key]; !exists {
			t.Errorf("normalized payload missing %q: %s", key, encoded)
		}
	}
	if payload["parser_version"] != record.ParserVersion {
		t.Errorf("parser_version = %#v", payload["parser_version"])
	}
	if got := nestedString(payload, "open_access", "url"); got != record.OpenAccess.URL {
		t.Errorf("open_access.url = %q", got)
	}
	if got := firstNestedString(payload, "licenses", "source_path"); got !=
		record.Licenses[0].SourcePath {
		t.Errorf("licenses[0].source_path = %q", got)
	}
	if got := firstNestedString(payload, "evidence", "SourcePath"); got !=
		record.Evidence[0].SourcePath {
		t.Errorf("evidence[0].SourcePath = %q", got)
	}
	authors, ok := payload["authors"].([]any)
	if !ok || len(authors) != 1 {
		t.Fatalf("authors = %#v", payload["authors"])
	}
	author, ok := authors[0].(map[string]any)
	if !ok {
		t.Fatalf("authors[0] = %#v", authors[0])
	}
	affiliations, ok := author["Affiliations"].([]any)
	if !ok || !slices.Equal(affiliations, []any{"Evidence Institute"}) {
		t.Errorf("authors[0].Affiliations = %#v", author["Affiliations"])
	}
}

func nestedString(
	payload map[string]any,
	key string,
	nestedKey string,
) string {
	nested, _ := payload[key].(map[string]any)
	value, _ := nested[nestedKey].(string)
	return value
}

func firstNestedString(
	payload map[string]any,
	key string,
	nestedKey string,
) string {
	values, _ := payload[key].([]any)
	if len(values) == 0 {
		return ""
	}
	nested, _ := values[0].(map[string]any)
	value, _ := nested[nestedKey].(string)
	return value
}

func boolPointer(value bool) *bool {
	return &value
}
