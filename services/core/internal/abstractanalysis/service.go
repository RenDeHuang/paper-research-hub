package abstractanalysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/openairesponses"
)

const (
	PromptVersion    = "abstract-route-prompt/v1"
	ModelProvider    = "openai-compatible"
	RunLeaseDuration = 10 * time.Minute

	FailureHTTPStatus         = "openai_http_status"
	FailureRefusal            = "openai_refusal"
	FailureIncomplete         = "openai_incomplete"
	FailureInvalidResponse    = "openai_invalid_response"
	FailureInvalidOutput      = "openai_invalid_structured_output"
	FailureSchemaMismatch     = "openai_schema_mismatch"
	FailureInvalidSchema      = "openai_invalid_schema"
	FailureRequestCancelled   = "openai_request_cancelled"
	FailureRequestFailed      = "openai_request_failed"
	FailureDecode             = "abstract_route_decode_failed"
	FailureEvidenceValidation = "abstract_route_evidence_validation_failed"
	FailureLeaseExpired       = "abstract_analysis_lease_expired"
)

const promptText = `Extract the research route using only facts explicitly stated in the supplied abstract.
For every required field, use state "supported" only when the abstract directly supports the value.
For supported fields, copy one or more exact abstract substrings into evidence.
Use state "not_reported", an empty value, and an empty evidence array when the abstract does not report the field.
Do not infer unstated mechanisms, methods, findings, limitations, novelty, or applications.`

type Candidate struct {
	WorkID                 string
	ProjectionAssertionID  string
	NormalizedAssertionID  string
	SourceRecordID         string
	Source                 string
	SourceRecordExternalID string
	ParserVersion          string
	SourceTime             time.Time
	Title                  string
	Abstract               string
}

func (candidate Candidate) Validate() error {
	for name, value := range map[string]string{
		"work ID":                 candidate.WorkID,
		"projection assertion ID": candidate.ProjectionAssertionID,
		"normalized assertion ID": candidate.NormalizedAssertionID,
		"source record ID":        candidate.SourceRecordID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("abstract analysis %s must be a UUID", name)
		}
	}
	for name, value := range map[string]string{
		"source":                    candidate.Source,
		"source record external ID": candidate.SourceRecordExternalID,
		"parser version":            candidate.ParserVersion,
		"title":                     candidate.Title,
		"abstract":                  candidate.Abstract,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("abstract analysis candidate %s is required", name)
		}
		if value != strings.TrimSpace(value) {
			return fmt.Errorf("abstract analysis candidate %s must be trimmed", name)
		}
	}
	if candidate.SourceTime.IsZero() {
		return errors.New("abstract analysis candidate source time is required")
	}
	return nil
}

type Selection struct {
	Cutoff         time.Time
	Limit          int
	PromptVersion  string
	SchemaVersion  string
	APIMode        openairesponses.Mode
	RequestedModel string
}

func (selection Selection) Validate() error {
	if selection.Cutoff.IsZero() {
		return errors.New("abstract analysis cutoff is required")
	}
	if selection.Limit < 1 || selection.Limit > 1000 {
		return errors.New("abstract analysis limit must be between 1 and 1000")
	}
	if selection.PromptVersion != PromptVersion {
		return fmt.Errorf(
			"abstract analysis prompt version must equal %s",
			PromptVersion,
		)
	}
	if selection.SchemaVersion != SchemaVersion {
		return fmt.Errorf(
			"abstract analysis schema version must equal %s",
			SchemaVersion,
		)
	}
	if !validAPIMode(selection.APIMode) {
		return errors.New(
			"abstract analysis API mode must be responses or chat_completions",
		)
	}
	if strings.TrimSpace(selection.RequestedModel) == "" ||
		selection.RequestedModel != strings.TrimSpace(selection.RequestedModel) {
		return errors.New(
			"abstract analysis requested model must be non-empty and trimmed",
		)
	}
	return nil
}

type RunDescriptor struct {
	Candidate      Candidate
	ModelProvider  string
	APIMode        openairesponses.Mode
	RequestedModel string
	PromptVersion  string
	PromptText     string
	PromptSHA256   string
	SchemaName     string
	SchemaVersion  string
	SchemaJSON     json.RawMessage
	SchemaSHA256   string
	Title          string
	TitleSHA256    string
	Abstract       string
	AbstractSHA256 string
	InputSHA256    string
	StartedAt      time.Time
	LeaseExpiresAt time.Time
}

type Run struct {
	ID         string
	Descriptor RunDescriptor
}

type Success struct {
	ResponseID  string
	ActualModel string
	Usage       openairesponses.Usage
	OutputJSON  json.RawMessage
	Result      Result
	CompletedAt time.Time
}

type Failure struct {
	Code        string
	ResponseID  string
	ActualModel string
	Usage       openairesponses.Usage
	CompletedAt time.Time
}

type AnalyzeInput struct {
	PromptVersion string
	SchemaVersion string
	Cutoff        time.Time
	Limit         int
}

type Summary struct {
	Selected  int
	Succeeded int
	Failed    int
}

type Store interface {
	Candidates(context.Context, Selection) ([]Candidate, error)
	Start(context.Context, RunDescriptor) (Run, error)
	Succeed(context.Context, Run, Success) error
	Fail(context.Context, Run, Failure) error
}

type StructuredClient interface {
	Create(
		context.Context,
		openairesponses.Request,
	) (openairesponses.Result, error)
}

type Service struct {
	store          Store
	client         StructuredClient
	apiMode        openairesponses.Mode
	requestedModel string
	now            func() time.Time
}

func NewService(
	store Store,
	client StructuredClient,
	apiMode openairesponses.Mode,
	requestedModel string,
	now func() time.Time,
) (*Service, error) {
	if store == nil {
		return nil, errors.New("abstract analysis store is required")
	}
	if client == nil {
		return nil, errors.New("abstract analysis structured client is required")
	}
	if !validAPIMode(apiMode) {
		return nil, errors.New(
			"abstract analysis API mode must be responses or chat_completions",
		)
	}
	if strings.TrimSpace(requestedModel) == "" ||
		requestedModel != strings.TrimSpace(requestedModel) {
		return nil, errors.New(
			"abstract analysis requested model must be non-empty and trimmed",
		)
	}
	if now == nil {
		return nil, errors.New("abstract analysis clock is required")
	}
	return &Service{
		store:          store,
		client:         client,
		apiMode:        apiMode,
		requestedModel: requestedModel,
		now:            now,
	}, nil
}

func (service *Service) Analyze(
	ctx context.Context,
	input AnalyzeInput,
) (Summary, error) {
	if service == nil {
		return Summary{}, errors.New("abstract analysis service is nil")
	}
	if ctx == nil {
		return Summary{}, errors.New("abstract analysis context is required")
	}
	selection := Selection{
		Cutoff:         input.Cutoff.UTC(),
		Limit:          input.Limit,
		PromptVersion:  input.PromptVersion,
		SchemaVersion:  input.SchemaVersion,
		APIMode:        service.apiMode,
		RequestedModel: service.requestedModel,
	}
	if err := selection.Validate(); err != nil {
		return Summary{}, err
	}
	candidates, err := service.store.Candidates(ctx, selection)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{Selected: len(candidates)}
	for _, candidate := range candidates {
		if err := candidate.Validate(); err != nil {
			return summary, err
		}
		descriptor, err := newRunDescriptor(
			candidate,
			service.apiMode,
			service.requestedModel,
			service.now().UTC(),
		)
		if err != nil {
			return summary, err
		}
		run, err := service.store.Start(ctx, descriptor)
		if err != nil {
			return summary, err
		}
		response, requestErr := service.client.Create(
			ctx,
			openairesponses.Request{
				Messages: []openairesponses.Message{
					{
						Role:    openairesponses.RoleDeveloper,
						Content: promptText,
					},
					{
						Role: openairesponses.RoleUser,
						Content: "Title:\n" + candidate.Title +
							"\n\nAbstract:\n" + candidate.Abstract,
					},
				},
				OutputSchema: openairesponses.JSONSchema{
					Name:   SchemaName,
					Schema: SchemaJSON(),
				},
			},
		)
		if requestErr != nil {
			failure := Failure{
				Code:        failureCode(requestErr),
				ResponseID:  response.ResponseID,
				ActualModel: response.Model,
				Usage:       response.Usage,
				CompletedAt: service.now().UTC(),
			}
			summary.Failed++
			return summary, errors.Join(
				requestErr,
				service.store.Fail(
					context.WithoutCancel(ctx),
					run,
					failure,
				),
			)
		}
		if response.APIMode != service.apiMode {
			err := errors.New(
				"OpenAI-compatible client returned a different API mode",
			)
			summary.Failed++
			return summary, errors.Join(
				err,
				service.store.Fail(
					context.WithoutCancel(ctx),
					run,
					Failure{
						Code:        FailureInvalidResponse,
						ResponseID:  response.ResponseID,
						ActualModel: response.Model,
						Usage:       response.Usage,
						CompletedAt: service.now().UTC(),
					},
				),
			)
		}
		result, err := DecodeResult(response.JSON)
		if err != nil {
			summary.Failed++
			return summary, errors.Join(
				err,
				service.store.Fail(
					context.WithoutCancel(ctx),
					run,
					Failure{
						Code:        FailureDecode,
						ResponseID:  response.ResponseID,
						ActualModel: response.Model,
						Usage:       response.Usage,
						CompletedAt: service.now().UTC(),
					},
				),
			)
		}
		if err := ValidateEvidence(candidate.Abstract, result); err != nil {
			summary.Failed++
			return summary, errors.Join(
				err,
				service.store.Fail(
					context.WithoutCancel(ctx),
					run,
					Failure{
						Code:        FailureEvidenceValidation,
						ResponseID:  response.ResponseID,
						ActualModel: response.Model,
						Usage:       response.Usage,
						CompletedAt: service.now().UTC(),
					},
				),
			)
		}
		if err := service.store.Succeed(ctx, run, Success{
			ResponseID:  response.ResponseID,
			ActualModel: response.Model,
			Usage:       response.Usage,
			OutputJSON:  append(json.RawMessage(nil), response.JSON...),
			Result:      result,
			CompletedAt: service.now().UTC(),
		}); err != nil {
			return summary, err
		}
		summary.Succeeded++
	}
	return summary, nil
}

func newRunDescriptor(
	candidate Candidate,
	apiMode openairesponses.Mode,
	requestedModel string,
	startedAt time.Time,
) (RunDescriptor, error) {
	if err := candidate.Validate(); err != nil {
		return RunDescriptor{}, err
	}
	if !validAPIMode(apiMode) {
		return RunDescriptor{}, errors.New(
			"abstract analysis API mode must be responses or chat_completions",
		)
	}
	schema := SchemaJSON()
	input, err := json.Marshal(struct {
		PromptVersion string `json:"prompt_version"`
		PromptText    string `json:"prompt_text"`
		SchemaVersion string `json:"schema_version"`
		APIMode       string `json:"api_mode"`
		Title         string `json:"title"`
		Abstract      string `json:"abstract"`
	}{
		PromptVersion: PromptVersion,
		PromptText:    promptText,
		SchemaVersion: SchemaVersion,
		APIMode:       string(apiMode),
		Title:         candidate.Title,
		Abstract:      candidate.Abstract,
	})
	if err != nil {
		return RunDescriptor{}, fmt.Errorf(
			"encode abstract analysis input descriptor: %w",
			err,
		)
	}
	return RunDescriptor{
		Candidate:      candidate,
		ModelProvider:  ModelProvider,
		APIMode:        apiMode,
		RequestedModel: requestedModel,
		PromptVersion:  PromptVersion,
		PromptText:     promptText,
		PromptSHA256:   sha256String(promptText),
		SchemaName:     SchemaName,
		SchemaVersion:  SchemaVersion,
		SchemaJSON:     schema,
		SchemaSHA256:   sha256Bytes(schema),
		Title:          candidate.Title,
		TitleSHA256:    sha256String(candidate.Title),
		Abstract:       candidate.Abstract,
		AbstractSHA256: sha256String(candidate.Abstract),
		InputSHA256:    sha256Bytes(input),
		StartedAt:      startedAt,
		LeaseExpiresAt: startedAt.Add(RunLeaseDuration),
	}, nil
}

func validAPIMode(mode openairesponses.Mode) bool {
	switch mode {
	case openairesponses.ModeResponses,
		openairesponses.ModeChatCompletions:
		return true
	default:
		return false
	}
}

func failureCode(err error) string {
	switch {
	case errors.Is(err, openairesponses.ErrHTTPStatus):
		return FailureHTTPStatus
	case errors.Is(err, openairesponses.ErrRefusal):
		return FailureRefusal
	case errors.Is(err, openairesponses.ErrIncomplete):
		return FailureIncomplete
	case errors.Is(err, openairesponses.ErrInvalidResponse):
		return FailureInvalidResponse
	case errors.Is(err, openairesponses.ErrInvalidStructuredOutput):
		return FailureInvalidOutput
	case errors.Is(err, openairesponses.ErrSchemaMismatch):
		return FailureSchemaMismatch
	case errors.Is(err, openairesponses.ErrInvalidSchema):
		return FailureInvalidSchema
	case errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return FailureRequestCancelled
	default:
		return FailureRequestFailed
	}
}

func sha256String(value string) string {
	return sha256Bytes([]byte(value))
}

func sha256Bytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
