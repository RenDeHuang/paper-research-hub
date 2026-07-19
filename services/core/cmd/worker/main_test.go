package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/catalog"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/config"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/ingestion"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/paper"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/source"
)

func TestLegacyPubMedSyncUsesExplicitEntrezDateType(t *testing.T) {
	t.Parallel()

	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured = request.URL.Query().Get("datetype")
		_, _ = writer.Write([]byte(
			`{"esearchresult":{"count":"0","querykey":"1","webenv":"empty-history"}}`,
		))
	}))
	defer server.Close()

	_, sourceName, err := fetchRecords(
		context.Background(),
		config.Config{
			PubMed: config.PubMedConfig{
				Request: config.RequestConfig{
					BaseURL:    server.URL,
					Timeout:    time.Second,
					MaxRetries: 0,
					MaxWait:    time.Second,
					BatchSize:  100,
				},
				Tool:  "paper-hub-test",
				Email: "research@example.test",
			},
		},
		workerCommand{
			Kind:       commandSyncPubMed,
			Query:      "agents",
			FromDate:   time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
			ToDate:     time.Date(2026, time.July, 2, 0, 0, 0, 0, time.UTC),
			MaxResults: 1,
		},
	)
	if err != nil {
		t.Fatalf("fetchRecords() error = %v", err)
	}
	if sourceName != source.PubMed {
		t.Fatalf("source = %q, want %q", sourceName, source.PubMed)
	}
	if captured != "edat" {
		t.Fatalf("datetype = %q, want legacy sync to remain Entrez date mode", captured)
	}
}

func TestSyncIdentityPolicyVersionsReflectPMIDSemantics(t *testing.T) {
	t.Parallel()

	if syncControlledIdentityScopePolicyVersion !=
		"scope/controlled-canonical-identity/v2" {
		t.Fatalf(
			"sync controlled identity scope policy version = %q",
			syncControlledIdentityScopePolicyVersion,
		)
	}
	if syncProjectionPolicyVersion != "projection/latest-source-revision/v2" {
		t.Fatalf(
			"sync projection policy version = %q",
			syncProjectionPolicyVersion,
		)
	}
}

func TestRealMainParsesBoundedOpenAlexSyncBeforeExecution(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var received workerCommand
	code := realMain(
		context.Background(),
		[]string{
			"sync",
			"openalex",
			"--query",
			"LLM agent",
			"--filter",
			"from_publication_date:2026-07-01",
			"--max-results",
			"25",
		},
		&stdout,
		&stderr,
		func(key string) (string, bool) {
			values := map[string]string{
				"DATABASE_URL":           "postgres://paper:secret@localhost/papers",
				"OPENALEX_API_KEY":       "openalex-secret",
				"OPENALEX_CONTACT_EMAIL": "researcher@example.test",
			}
			value, ok := values[key]
			return value, ok
		},
		func(_ context.Context, cfg config.Config, command workerCommand) (map[string]any, error) {
			received = command
			if cfg.OpenAlex.APIKey != "openalex-secret" {
				t.Fatal("runner did not receive strict OpenAlex configuration")
			}
			return map[string]any{
				"job_id":       "job-1",
				"status":       ingestion.JobStatusSucceeded,
				"raw_inserted": int64(25),
				"projected":    int64(25),
			}, nil
		},
	)

	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.Kind != commandSyncOpenAlex ||
		received.Query != "LLM agent" ||
		received.Filter != "from_publication_date:2026-07-01" ||
		received.MaxResults != 25 {
		t.Fatalf("received command = %#v", received)
	}
	if !strings.Contains(stdout.String(), `"raw_inserted":25`) ||
		!strings.Contains(stdout.String(), `"projected":25`) {
		t.Fatalf("stdout = %q, want exact job summary JSON", stdout.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "openalex-secret") {
		t.Fatal("worker output leaked OpenAlex API key")
	}
}

func TestRealMainParsesCatalogPublishAndOutputsGenerationJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	generatedAt := time.Date(2026, time.July, 16, 8, 9, 10, 123456789, time.UTC)
	publishedAt := generatedAt.Add(2 * time.Second)
	generation := catalog.Generation{
		ID:             uuid.MustParse("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		SourceRevision: "source-revision-sha256",
		FormulaVersion: "public-catalog/v1",
		GeneratedAt:    generatedAt,
		PublishedAt:    publishedAt,
	}
	var received workerCommand

	code := realMain(
		context.Background(),
		[]string{
			"publish",
			"catalog",
			"--formula-version",
			"public-catalog/v1",
			"--generated-at",
			"2026-07-16T08:09:10.123456789Z",
			"--jcr-metric-year",
			"2025",
			"--venue-policy-name",
			"journal-all-q1",
			"--venue-policy-version",
			"2",
			"--eligibility-policy-version",
			"biomedical-public-eligibility/v1",
			"--subject-version",
			"biomedical-jcr-subjects/v1",
			"--jcr-import-receipt",
			"00000000-0000-0000-0000-000000000501",
			"--citation-source",
			"openalex",
			"--citation-analysis-run-id",
			"00000000-0000-0000-0000-000000000701",
			"--trend-analysis-run-id",
			"00000000-0000-0000-0000-000000000702",
			"--journal-analysis-run-id",
			"00000000-0000-0000-0000-000000000703",
			"--opportunity-analysis-run-id",
			"00000000-0000-0000-0000-000000000704",
		},
		&stdout,
		&stderr,
		func(key string) (string, bool) {
			if key == "DATABASE_URL" {
				return "postgres://paper:secret@localhost/papers", true
			}
			return "", false
		},
		func(_ context.Context, cfg config.Config, command workerCommand) (map[string]any, error) {
			received = command
			if cfg.Catalog.CursorSecret != "" {
				t.Fatal("catalog publish role unexpectedly required a cursor secret")
			}
			return catalogGenerationResult(generation), nil
		},
	)

	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.Kind != commandPublishCatalog {
		t.Fatalf("received command kind = %q, want %q", received.Kind, commandPublishCatalog)
	}
	if received.FormulaVersion != generation.FormulaVersion {
		t.Fatalf("FormulaVersion = %q, want %q", received.FormulaVersion, generation.FormulaVersion)
	}
	if !received.GeneratedAt.Equal(generatedAt) {
		t.Fatalf("GeneratedAt = %s, want %s", received.GeneratedAt, generatedAt)
	}
	if received.MetricYear != 2025 ||
		received.VenuePolicyName != "journal-all-q1" ||
		received.VenuePolicyVersion != 2 ||
		received.EligibilityPolicyVersion !=
			"biomedical-public-eligibility/v1" ||
		received.SubjectVersion != "biomedical-jcr-subjects/v1" ||
		received.JCRReceipt != "00000000-0000-0000-0000-000000000501" ||
		received.CitationSource != "openalex" ||
		received.CitationAnalysisRunID !=
			"00000000-0000-0000-0000-000000000701" ||
		received.TrendAnalysisRunID !=
			"00000000-0000-0000-0000-000000000702" ||
		received.JournalAnalysisRunID !=
			"00000000-0000-0000-0000-000000000703" ||
		received.OpportunityAnalysisRunID !=
			"00000000-0000-0000-0000-000000000704" {
		t.Fatalf("Catalog curation inputs = %#v", received)
	}

	var output struct {
		ID             string `json:"id"`
		SourceRevision string `json:"source_revision"`
		FormulaVersion string `json:"formula_version"`
		GeneratedAt    string `json:"generated_at"`
		PublishedAt    string `json:"published_at"`
	}
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		t.Fatalf("decode generation JSON: %v; stdout=%s", err, stdout.String())
	}
	if output.ID != generation.ID.String() ||
		output.SourceRevision != generation.SourceRevision ||
		output.FormulaVersion != generation.FormulaVersion ||
		output.GeneratedAt != generatedAt.Format(time.RFC3339Nano) ||
		output.PublishedAt != publishedAt.Format(time.RFC3339Nano) {
		t.Fatalf("generation output = %#v, want %#v", output, generation)
	}
}

func TestRealMainRejectsInvalidOrUnboundedCommandsBeforeMutation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing command",
			args: nil,
			want: "usage",
		},
		{
			name: "openalex missing query and filter",
			args: []string{"sync", "openalex", "--max-results", "10"},
			want: "query or filter",
		},
		{
			name: "over maximum result limit",
			args: []string{"sync", "openalex", "--query", "agent", "--max-results", "1001"},
			want: "between 1 and 1000",
		},
		{
			name: "pubmed missing complete date window",
			args: []string{
				"sync", "pubmed",
				"--query", "agent",
				"--from-date", "2026-07-01",
				"--max-results", "10",
			},
			want: "both from-date and to-date",
		},
		{
			name: "crossref unbounded",
			args: []string{
				"sync",
				"crossref-created",
				"--max-results",
				"10",
			},
			want: "both from-date and to-date",
		},
		{
			name: "catalog publish missing formula version",
			args: []string{
				"publish", "catalog",
				"--generated-at", "2026-07-16T08:09:10.123456789Z",
			},
			want: "formula-version",
		},
		{
			name: "catalog publish formula version is not trimmed",
			args: []string{
				"publish", "catalog",
				"--formula-version", " public-catalog/v1",
				"--generated-at", "2026-07-16T08:09:10.123456789Z",
			},
			want: "trimmed",
		},
		{
			name: "catalog publish missing generated at",
			args: []string{
				"publish", "catalog",
				"--formula-version", "public-catalog/v1",
			},
			want: "generated-at",
		},
		{
			name: "catalog publish generated at is not RFC3339Nano",
			args: []string{
				"publish", "catalog",
				"--formula-version", "public-catalog/v1",
				"--generated-at", "2026-07-16 08:09:10",
			},
			want: "RFC3339Nano",
		},
		{
			name: "subject import missing file",
			args: []string{"import", "subjects"},
			want: "explicit --file",
		},
		{
			name: "subject import file is not CSV",
			args: []string{"import", "subjects", "--file", "/imports/subjects.json"},
			want: "explicit .csv",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			lookedUp := false
			called := false
			code := realMain(
				context.Background(),
				test.args,
				&stdout,
				&stderr,
				func(string) (string, bool) {
					lookedUp = true
					return "", false
				},
				func(context.Context, config.Config, workerCommand) (map[string]any, error) {
					called = true
					return nil, nil
				},
			)
			if code == 0 {
				t.Fatalf("realMain() code = 0, want failure")
			}
			if called || lookedUp {
				t.Fatal("invalid worker command reached configuration or mutation")
			}
			if !strings.Contains(strings.ToLower(stderr.String()), strings.ToLower(test.want)) {
				t.Fatalf("stderr = %q, want containing %q", stderr.String(), test.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestRealMainParsesJCRImportWithExplicitLicensedFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var received workerCommand
	code := realMain(
		context.Background(),
		[]string{
			"import",
			"jcr",
			"--file",
			"/authorized/jcr-2025.csv",
		},
		&stdout,
		&stderr,
		func(key string) (string, bool) {
			values := map[string]string{
				"DATABASE_URL":       "postgres://paper:secret@localhost/papers",
				"JCR_IMPORT_PATH":    "/authorized/jcr-2025.csv",
				"JCR_SOURCE_LICENSE": "institutional-jcr-license",
			}
			value, ok := values[key]
			return value, ok
		},
		func(_ context.Context, cfg config.Config, command workerCommand) (map[string]any, error) {
			received = command
			if cfg.Venues.JCRSourceLicense != "institutional-jcr-license" {
				t.Fatal("runner did not receive explicit JCR source license")
			}
			return map[string]any{
				"file_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"input_rows":  10,
			}, nil
		},
	)
	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.Kind != commandImportJCR ||
		received.File != "/authorized/jcr-2025.csv" {
		t.Fatalf("received JCR command = %#v", received)
	}
	if !strings.Contains(stdout.String(), `"input_rows":10`) {
		t.Fatalf("stdout = %q, want JCR receipt", stdout.String())
	}
}

func TestRealMainParsesSubjectRegistryImportWithExplicitVersionedFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var received workerCommand
	code := realMain(
		context.Background(),
		[]string{
			"import",
			"subjects",
			"--file",
			"/imports/biomedical-jcr-subjects.v1.csv",
		},
		&stdout,
		&stderr,
		func(key string) (string, bool) {
			if key == "DATABASE_URL" {
				return "postgres://paper:secret@localhost/papers", true
			}
			return "", false
		},
		func(_ context.Context, cfg config.Config, command workerCommand) (map[string]any, error) {
			received = command
			if cfg.Venues.JCRImportPath != "" || cfg.Venues.JCRSourceLicense != "" {
				t.Fatal("Subject import unexpectedly required JCR import configuration")
			}
			return map[string]any{
				"file_sha256":      strings.Repeat("a", 64),
				"source":           "medpaperhub-reviewed-jcr-category-allowlist",
				"registry_version": "biomedical-jcr-subjects/v1",
				"subject_count":    8,
				"rule_count":       8,
			}, nil
		},
	)
	if code != 0 {
		t.Fatalf("realMain() code = %d, stderr = %s", code, stderr.String())
	}
	if received.Kind != commandImportSubjects ||
		received.File != "/imports/biomedical-jcr-subjects.v1.csv" {
		t.Fatalf("received Subject command = %#v", received)
	}
	if !strings.Contains(stdout.String(), `"registry_version":"biomedical-jcr-subjects/v1"`) ||
		!strings.Contains(stdout.String(), `"subject_count":8`) ||
		!strings.Contains(stdout.String(), `"rule_count":8`) {
		t.Fatalf("stdout = %q, want Subject receipt", stdout.String())
	}
}

func TestRecordEventsPreservesCommittedPrefixAndUsesSourceRevisionTime(t *testing.T) {
	first := workerRecord(t, "W1", "10.1000/worker-one")
	second := workerRecord(t, "W2", "10.1000/worker-two")
	revisedAt := time.Date(2026, time.July, 16, 6, 0, 0, 0, time.UTC)
	first.UpdatedAt = &revisedAt
	second.UpdatedAt = nil
	second.PublishedAt = nil
	sequenceError := errors.New("page 2 malformed")

	records := func(yield func(source.Record, error) bool) {
		if !yield(first, nil) {
			return
		}
		if !yield(second, nil) {
			return
		}
		yield(source.Record{}, sequenceError)
	}
	events := recordEvents(source.OpenAlex, iter.Seq2[source.Record, error](records))

	var collected []ingestion.Event
	var collectedErr error
	for event, err := range events {
		if err != nil {
			collectedErr = err
			break
		}
		collected = append(collected, event)
	}
	if len(collected) != 1 {
		t.Fatalf("events before invalid source timestamp = %d, want 1", len(collected))
	}
	envelope, ok := collected[0].(ingestion.Envelope)
	if !ok || !envelope.SourceTime.Equal(revisedAt) ||
		envelope.EventKey != "openalex:W1" ||
		envelope.Position != 1 {
		t.Fatalf("first envelope = %#v", collected[0])
	}
	if collectedErr == nil ||
		!strings.Contains(collectedErr.Error(), "source revision time") ||
		errors.Is(collectedErr, sequenceError) {
		t.Fatalf("recordEvents error = %v, want missing source revision time before later page error", collectedErr)
	}
}

func workerRecord(t *testing.T, openAlexID string, doi string) source.Record {
	t.Helper()
	raw, err := source.NewRawRecord([]byte(`{"id":"` + openAlexID + `"}`))
	if err != nil {
		t.Fatalf("NewRawRecord() error = %v", err)
	}
	identity, err := paper.NewIdentifier(paper.SchemeDOI, doi)
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	publishedAt := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	return source.Record{
		Source:         source.OpenAlex,
		SourceRecordID: openAlexID,
		Identity:       identity,
		Identifiers: []source.Identifier{
			{Scheme: source.IdentifierDOI, Value: doi},
			{Scheme: source.IdentifierOpenAlex, Value: openAlexID},
		},
		Raw:         raw,
		Title:       "Worker record",
		PublishedAt: &publishedAt,
		Scope: source.ScopeDecision{
			Status: source.ScopePending,
			Reason: source.ScopeReasonAwaitingDeterministicEvaluation,
		},
	}
}
