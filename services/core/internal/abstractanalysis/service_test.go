package abstractanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/openairesponses"
)

func TestServicePersistsCompleteAuditableAbstractRouteSuccess(t *testing.T) {
	t.Parallel()

	const (
		title    = "External validation of an oncology model"
		abstract = "Patients with advanced cancer were evaluated in an external validation cohort."
	)
	result := notReportedResult()
	result.Domain = EvidenceField{
		State:    StateSupported,
		Value:    "oncology",
		Evidence: []string{"Patients with advanced cancer"},
	}
	output, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal(result) error = %v", err)
	}
	now := time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC)
	store := &fakeAnalysisStore{
		candidates: []Candidate{analysisCandidate(title, abstract)},
	}
	client := &fakeStructuredClient{
		result: openairesponses.Result{
			ResponseID: "resp_abstract_1",
			APIMode:    openairesponses.ModeChatCompletions,
			Model:      "actual-model-2026-07-18",
			Usage: openairesponses.Usage{
				InputTokens:  100,
				OutputTokens: 200,
				TotalTokens:  300,
				Raw: json.RawMessage(
					`{"input_tokens":100,"output_tokens":200,"total_tokens":300}`,
				),
			},
			JSON: output,
		},
	}
	service, err := NewService(
		store,
		client,
		openairesponses.ModeChatCompletions,
		"requested-model",
		func() time.Time {
			current := now
			now = now.Add(time.Second)
			return current
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	summary, err := service.Analyze(context.Background(), AnalyzeInput{
		PromptVersion: PromptVersion,
		SchemaVersion: SchemaVersion,
		Cutoff:        time.Date(2026, time.July, 18, 7, 0, 0, 0, time.UTC),
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	if summary.Selected != 1 || summary.Succeeded != 1 || summary.Failed != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if len(store.started) != 1 || len(store.succeeded) != 1 ||
		len(store.failed) != 0 {
		t.Fatalf(
			"store calls = started %d succeeded %d failed %d",
			len(store.started),
			len(store.succeeded),
			len(store.failed),
		)
	}
	started := store.started[0]
	if started.APIMode != openairesponses.ModeChatCompletions ||
		started.RequestedModel != "requested-model" ||
		started.PromptVersion != PromptVersion ||
		started.SchemaVersion != SchemaVersion ||
		started.SchemaName != SchemaName ||
		string(started.SchemaJSON) != string(SchemaJSON()) {
		t.Fatalf("started descriptor = %#v", started)
	}
	for name, hash := range map[string]string{
		"title":    started.TitleSHA256,
		"abstract": started.AbstractSHA256,
		"schema":   started.SchemaSHA256,
		"prompt":   started.PromptSHA256,
		"input":    started.InputSHA256,
	} {
		if len(hash) != 64 {
			t.Errorf("%s SHA-256 = %q", name, hash)
		}
	}
	if started.Title != title || started.Abstract != abstract ||
		started.Candidate.NormalizedAssertionID == "" ||
		started.Candidate.ProjectionAssertionID == "" ||
		started.Candidate.SourceRecordID == "" {
		t.Fatalf("started input/provenance = %#v", started)
	}
	if len(client.requests) != 1 ||
		len(client.requests[0].Messages) != 2 ||
		!strings.Contains(client.requests[0].Messages[1].Content, title) ||
		!strings.Contains(client.requests[0].Messages[1].Content, abstract) {
		t.Fatalf("OpenAI request = %#v", client.requests)
	}
	completed := store.succeeded[0]
	if completed.ResponseID != "resp_abstract_1" ||
		completed.ActualModel != "actual-model-2026-07-18" ||
		completed.Usage.TotalTokens != 300 ||
		completed.Result.Domain.Value != "oncology" ||
		completed.CompletedAt.Sub(started.StartedAt) != time.Second {
		t.Fatalf("completed result = %#v", completed)
	}
}

func TestServicePersistsStableFailureCodeAndAvailableResponseMetadata(
	t *testing.T,
) {
	t.Parallel()

	store := &fakeAnalysisStore{
		candidates: []Candidate{analysisCandidate(
			"Refused analysis",
			"Patients were enrolled.",
		)},
	}
	client := &fakeStructuredClient{
		result: openairesponses.Result{
			ResponseID: "resp_refusal",
			APIMode:    openairesponses.ModeChatCompletions,
			Model:      "actual-model",
			Usage: openairesponses.Usage{
				InputTokens:  10,
				OutputTokens: 2,
				TotalTokens:  12,
				Raw: json.RawMessage(
					`{"input_tokens":10,"output_tokens":2,"total_tokens":12}`,
				),
			},
		},
		err: openairesponses.ErrRefusal,
	}
	now := time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC)
	service, err := NewService(
		store,
		client,
		openairesponses.ModeChatCompletions,
		"requested-model",
		func() time.Time {
			now = now.Add(time.Second)
			return now
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	summary, err := service.Analyze(context.Background(), AnalyzeInput{
		PromptVersion: PromptVersion,
		SchemaVersion: SchemaVersion,
		Cutoff:        time.Date(2026, time.July, 18, 8, 0, 0, 0, time.UTC),
		Limit:         1,
	})
	if err == nil || !errors.Is(err, openairesponses.ErrRefusal) {
		t.Fatalf("Analyze() error = %v, want refusal", err)
	}
	if summary.Selected != 1 || summary.Succeeded != 0 || summary.Failed != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if len(store.failed) != 1 {
		t.Fatalf("failed runs = %#v", store.failed)
	}
	failure := store.failed[0]
	if failure.Code != FailureRefusal ||
		failure.ResponseID != "resp_refusal" ||
		failure.ActualModel != "actual-model" ||
		failure.Usage.TotalTokens != 12 ||
		failure.CompletedAt.IsZero() {
		t.Fatalf("failure = %#v", failure)
	}
}

func TestServiceRejectsEvidenceNotFoundInTheExactInputAbstract(t *testing.T) {
	t.Parallel()

	result := notReportedResult()
	result.Domain = EvidenceField{
		State:    StateSupported,
		Value:    "oncology",
		Evidence: []string{"invented evidence"},
	}
	output, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal(result) error = %v", err)
	}
	store := &fakeAnalysisStore{
		candidates: []Candidate{analysisCandidate(
			"Evidence validation",
			"Patients were enrolled.",
		)},
	}
	client := &fakeStructuredClient{
		result: openairesponses.Result{
			ResponseID: "resp_bad_evidence",
			APIMode:    openairesponses.ModeChatCompletions,
			Model:      "actual-model",
			Usage: openairesponses.Usage{
				InputTokens:  10,
				OutputTokens: 10,
				TotalTokens:  20,
				Raw: json.RawMessage(
					`{"input_tokens":10,"output_tokens":10,"total_tokens":20}`,
				),
			},
			JSON: output,
		},
	}
	service, err := NewService(
		store,
		client,
		openairesponses.ModeChatCompletions,
		"requested-model",
		func() time.Time {
			return time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	summary, err := service.Analyze(context.Background(), AnalyzeInput{
		PromptVersion: PromptVersion,
		SchemaVersion: SchemaVersion,
		Cutoff:        time.Date(2026, time.July, 18, 9, 0, 0, 0, time.UTC),
		Limit:         1,
	})
	if err == nil {
		t.Fatal("Analyze() error = nil, want evidence rejection")
	}
	if summary.Failed != 1 || len(store.failed) != 1 ||
		store.failed[0].Code != FailureEvidenceValidation {
		t.Fatalf("summary/failure = %#v / %#v", summary, store.failed)
	}
}

type fakeStructuredClient struct {
	result   openairesponses.Result
	err      error
	requests []openairesponses.Request
}

func (client *fakeStructuredClient) Create(
	_ context.Context,
	request openairesponses.Request,
) (openairesponses.Result, error) {
	client.requests = append(client.requests, request)
	return client.result, client.err
}

type fakeAnalysisStore struct {
	candidates []Candidate
	started    []RunDescriptor
	succeeded  []Success
	failed     []Failure
}

func (store *fakeAnalysisStore) Candidates(
	context.Context,
	Selection,
) ([]Candidate, error) {
	return append([]Candidate(nil), store.candidates...), nil
}

func (store *fakeAnalysisStore) Start(
	_ context.Context,
	descriptor RunDescriptor,
) (Run, error) {
	store.started = append(store.started, descriptor)
	return Run{
		ID:         "00000000-0000-0000-0000-000000000801",
		Descriptor: descriptor,
	}, nil
}

func (store *fakeAnalysisStore) Succeed(
	_ context.Context,
	_ Run,
	success Success,
) error {
	store.succeeded = append(store.succeeded, success)
	return nil
}

func (store *fakeAnalysisStore) Fail(
	_ context.Context,
	_ Run,
	failure Failure,
) error {
	store.failed = append(store.failed, failure)
	return nil
}

func analysisCandidate(title string, abstract string) Candidate {
	return Candidate{
		WorkID:                 "00000000-0000-0000-0000-000000000701",
		ProjectionAssertionID:  "00000000-0000-0000-0000-000000000702",
		NormalizedAssertionID:  "00000000-0000-0000-0000-000000000703",
		SourceRecordID:         "00000000-0000-0000-0000-000000000704",
		Source:                 "pubmed",
		SourceRecordExternalID: "12345678",
		ParserVersion:          "pubmed/pubmed-article-v1",
		SourceTime: time.Date(
			2026,
			time.July,
			18,
			7,
			30,
			0,
			0,
			time.UTC,
		),
		Title:    title,
		Abstract: abstract,
	}
}
