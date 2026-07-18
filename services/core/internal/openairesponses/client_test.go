package openairesponses_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/openairesponses"
)

const testAPIKey = "sk-test-openai-responses-secret"

func TestClientSendsStrictResponsesRequestAndReturnsAuditableResult(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", request.Method)
		}
		if request.URL.EscapedPath() != "/compatible/v1/responses" {
			t.Errorf("path = %q, want preserved base path plus /responses", request.URL.EscapedPath())
		}
		if request.URL.RawQuery != "" {
			t.Errorf("query = %q, want empty", request.URL.RawQuery)
		}
		if request.Header.Get("Authorization") != "Bearer "+testAPIKey {
			t.Error("Authorization header does not contain the configured Bearer credential")
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", request.Header.Get("Content-Type"))
		}

		var body requestEnvelope
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "configured-model" {
			t.Errorf("model = %q, want configured-model", body.Model)
		}
		if body.Store == nil || *body.Store {
			t.Errorf("store = %#v, want explicit false", body.Store)
		}
		if len(body.Input) != 2 {
			t.Fatalf("input messages = %d, want 2", len(body.Input))
		}
		assertInputMessage(t, body.Input[0], "developer", "Return only source-supported facts.")
		assertInputMessage(t, body.Input[1], "user", "Title: Example\nAbstract: Exact evidence.")
		if body.Text.Format.Type != "json_schema" {
			t.Errorf("text.format.type = %q, want json_schema", body.Text.Format.Type)
		}
		if body.Text.Format.Name != "abstract_route_v1" {
			t.Errorf("text.format.name = %q, want abstract_route_v1", body.Text.Format.Name)
		}
		if !body.Text.Format.Strict {
			t.Error("text.format.strict = false, want true")
		}
		if got := body.Text.Format.Schema["additionalProperties"]; got != false {
			t.Errorf("schema additionalProperties = %#v, want false", got)
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(completedResponse(
			`{"summary":"Exact evidence.","evidence":["Exact evidence."]}`,
		)))
	}))
	defer server.Close()

	client := newTestClient(t, server.Client(), server.URL+"/compatible/v1/")
	result, err := client.Create(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if result.ResponseID != "resp_123" {
		t.Errorf("ResponseID = %q, want resp_123", result.ResponseID)
	}
	assertRequestedModel(t, result, "configured-model")
	if result.Model != "actual-model-2026-07-18" {
		t.Errorf("Model = %q, want response model/version", result.Model)
	}
	if result.Usage.InputTokens != 12 ||
		result.Usage.OutputTokens != 8 ||
		result.Usage.TotalTokens != 20 {
		t.Errorf("Usage = %#v, want recorded token counts", result.Usage)
	}
	if string(result.Usage.Raw) != `{"input_tokens":12,"output_tokens":8,"total_tokens":20}` {
		t.Errorf("Usage.Raw = %s, want exact provider usage JSON", result.Usage.Raw)
	}
	if string(result.JSON) != `{"summary":"Exact evidence.","evidence":["Exact evidence."]}` {
		t.Errorf("JSON = %s, want exact structured output", result.JSON)
	}
}

func TestClientNormalizesBasePathWithoutReplacingItsLastSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		basePath string
		wantPath string
	}{
		{name: "origin", basePath: "", wantPath: "/responses"},
		{name: "base path", basePath: "/proxy/openai/v1", wantPath: "/proxy/openai/v1/responses"},
		{name: "trailing slashes", basePath: "/proxy/openai/v1///", wantPath: "/proxy/openai/v1/responses"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.EscapedPath() != tt.wantPath {
					t.Errorf("path = %q, want %q", request.URL.EscapedPath(), tt.wantPath)
				}
				_, _ = writer.Write([]byte(completedResponse(
					`{"summary":"Exact evidence.","evidence":["Exact evidence."]}`,
				)))
			}))
			defer server.Close()

			client := newTestClient(t, server.Client(), server.URL+tt.basePath)
			if _, err := client.Create(context.Background(), validRequest()); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
		})
	}
}

func TestNewRejectsAmbiguousOrUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config openairesponses.Config
	}{
		{
			name: "nil HTTP client",
			config: openairesponses.Config{
				BaseURL: "https://api.example.test/v1",
				Mode:    openairesponses.ModeResponses,
				APIKey:  testAPIKey,
				Model:   "configured-model",
			},
		},
		{
			name: "remote plaintext URL",
			config: openairesponses.Config{
				BaseURL: "http://api.example.test/v1",
				Mode:    openairesponses.ModeResponses,
				APIKey:  testAPIKey,
				Model:   "configured-model",
			},
		},
		{
			name: "non HTTP URL",
			config: openairesponses.Config{
				BaseURL: "file:///tmp/openai/v1",
				Mode:    openairesponses.ModeResponses,
				APIKey:  testAPIKey,
				Model:   "configured-model",
			},
		},
		{
			name: "credentials in URL",
			config: openairesponses.Config{
				BaseURL: "https://user:password@api.example.test/v1",
				Mode:    openairesponses.ModeResponses,
				APIKey:  testAPIKey,
				Model:   "configured-model",
			},
		},
		{
			name: "query in URL",
			config: openairesponses.Config{
				BaseURL: "https://api.example.test/v1?key=value",
				Mode:    openairesponses.ModeResponses,
				APIKey:  testAPIKey,
				Model:   "configured-model",
			},
		},
		{
			name: "fragment in URL",
			config: openairesponses.Config{
				BaseURL: "https://api.example.test/v1#fragment",
				Mode:    openairesponses.ModeResponses,
				APIKey:  testAPIKey,
				Model:   "configured-model",
			},
		},
		{
			name: "endpoint instead of base path",
			config: openairesponses.Config{
				BaseURL: "https://api.example.test/v1/responses",
				Mode:    openairesponses.ModeResponses,
				APIKey:  testAPIKey,
				Model:   "configured-model",
			},
		},
		{
			name: "untrimmed API key",
			config: openairesponses.Config{
				BaseURL: "https://api.example.test/v1",
				Mode:    openairesponses.ModeResponses,
				APIKey:  " " + testAPIKey,
				Model:   "configured-model",
			},
		},
		{
			name: "empty model",
			config: openairesponses.Config{
				BaseURL: "https://api.example.test/v1",
				Mode:    openairesponses.ModeResponses,
				APIKey:  testAPIKey,
				Model:   "",
			},
		},
		{
			name: "missing API mode",
			config: openairesponses.Config{
				BaseURL: "https://api.example.test/v1",
				APIKey:  testAPIKey,
				Model:   "configured-model",
			},
		},
		{
			name: "unsupported API mode",
			config: openairesponses.Config{
				BaseURL: "https://api.example.test/v1",
				Mode:    "auto",
				APIKey:  testAPIKey,
				Model:   "configured-model",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			httpClient := http.DefaultClient
			if tt.name == "nil HTTP client" {
				httpClient = nil
			}
			client, err := openairesponses.New(httpClient, tt.config)
			if err == nil {
				t.Fatalf("New() = %#v, want configuration error", client)
			}
			if strings.Contains(err.Error(), testAPIKey) {
				t.Fatal("configuration error leaked API key")
			}
		})
	}
}

func TestClientRejectsNon2xxWithoutLeakingCredentialsOrResponseBody(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(
			writer,
			`{"error":"upstream echoed %s and %s"}`,
			testAPIKey,
			request.Header.Get("Authorization"),
		)
	}))
	defer server.Close()

	client := newTestClient(t, server.Client(), server.URL)
	_, err := client.Create(context.Background(), validRequest())
	if !errors.Is(err, openairesponses.ErrHTTPStatus) {
		t.Fatalf("Create() error = %v, want ErrHTTPStatus", err)
	}
	if strings.Contains(err.Error(), testAPIKey) || strings.Contains(err.Error(), "upstream echoed") {
		t.Fatal("non-2xx error leaked credentials or untrusted response body")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want exactly one strict request", requests.Load())
	}
}

func TestClientRedactsCredentialFromTransportErrors(t *testing.T) {
	t.Parallel()

	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("transport observed %s", request.Header.Get("Authorization"))
	})}
	client := newTestClient(t, httpClient, "https://api.example.test/v1")

	_, err := client.Create(context.Background(), validRequest())
	if err == nil {
		t.Fatal("Create() error = nil, want transport failure")
	}
	if strings.Contains(err.Error(), testAPIKey) ||
		strings.Contains(err.Error(), "Bearer") {
		t.Fatal("transport error leaked Authorization credential")
	}
}

func TestClientDoesNotFollowRedirectsWithBearerCredential(t *testing.T) {
	t.Parallel()

	var redirectedRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, _ *http.Request) {
			redirectedRequests.Add(1)
			_, _ = writer.Write([]byte(completedResponse(
				`{"summary":"Exact evidence.","evidence":["Exact evidence."]}`,
			)))
		},
	))
	defer redirectTarget.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", redirectTarget.URL+"/responses")
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	client := newTestClient(t, origin.Client(), origin.URL)
	if _, err := client.Create(context.Background(), validRequest()); !errors.Is(
		err,
		openairesponses.ErrHTTPStatus,
	) {
		t.Fatalf("Create() error = %v, want redirect rejected as ErrHTTPStatus", err)
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf(
			"redirected requests = %d, want Bearer request pinned to configured endpoint",
			redirectedRequests.Load(),
		)
	}
}

func TestClientRejectsRefusalIncompleteAndInvalidStructuredOutputWithoutRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		body           string
		wantErr        error
		wantResponseID string
		wantModel      string
		wantUsage      openairesponses.Usage
	}{
		{
			name: "refusal",
			body: `{
				"id":"resp_refusal",
				"status":"completed",
				"model":"actual-model",
				"output":[{
					"type":"message",
					"role":"assistant",
					"status":"completed",
					"content":[{"type":"refusal","refusal":"cannot comply"}]
				}],
				"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
			}`,
			wantErr:        openairesponses.ErrRefusal,
			wantResponseID: "resp_refusal",
			wantModel:      "actual-model",
			wantUsage: openairesponses.Usage{
				InputTokens:  1,
				OutputTokens: 1,
				TotalTokens:  2,
			},
		},
		{
			name: "top-level incomplete",
			body: `{
				"id":"resp_incomplete",
				"status":"incomplete",
				"incomplete_details":{"reason":"max_output_tokens"},
				"model":"actual-model",
				"output":[],
				"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
			}`,
			wantErr:        openairesponses.ErrIncomplete,
			wantResponseID: "resp_incomplete",
			wantModel:      "actual-model",
			wantUsage: openairesponses.Usage{
				InputTokens:  1,
				OutputTokens: 1,
				TotalTokens:  2,
			},
		},
		{
			name: "message incomplete",
			body: `{
				"id":"resp_incomplete_message",
				"status":"completed",
				"model":"actual-model",
				"output":[{
					"type":"message",
					"role":"assistant",
					"status":"incomplete",
					"content":[{"type":"output_text","text":"{}"}]
				}],
				"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
			}`,
			wantErr:        openairesponses.ErrIncomplete,
			wantResponseID: "resp_incomplete_message",
			wantModel:      "actual-model",
			wantUsage: openairesponses.Usage{
				InputTokens:  1,
				OutputTokens: 1,
				TotalTokens:  2,
			},
		},
		{
			name: "unsupported output content",
			body: `{
				"id":"resp_unsupported_output",
				"status":"completed",
				"model":"actual-model",
				"output":[{
					"type":"message",
					"role":"assistant",
					"status":"completed",
					"content":[{"type":"unsupported","text":"{}"}]
				}],
				"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
			}`,
			wantErr:        openairesponses.ErrInvalidResponse,
			wantResponseID: "resp_unsupported_output",
			wantModel:      "actual-model",
			wantUsage: openairesponses.Usage{
				InputTokens:  1,
				OutputTokens: 1,
				TotalTokens:  2,
			},
		},
		{
			name:           "invalid JSON",
			body:           completedResponse(`{"summary":`),
			wantErr:        openairesponses.ErrInvalidStructuredOutput,
			wantResponseID: "resp_123",
			wantModel:      "actual-model-2026-07-18",
			wantUsage: openairesponses.Usage{
				InputTokens:  12,
				OutputTokens: 8,
				TotalTokens:  20,
			},
		},
		{
			name: "extra fields",
			body: completedResponse(
				`{"summary":"Exact evidence.","evidence":["Exact evidence."],"` +
					testAPIKey + `":true}`,
			),
			wantErr:        openairesponses.ErrSchemaMismatch,
			wantResponseID: "resp_123",
			wantModel:      "actual-model-2026-07-18",
			wantUsage: openairesponses.Usage{
				InputTokens:  12,
				OutputTokens: 8,
				TotalTokens:  20,
			},
		},
		{
			name: "missing required field",
			body: completedResponse(
				`{"summary":"Exact evidence."}`,
			),
			wantErr:        openairesponses.ErrSchemaMismatch,
			wantResponseID: "resp_123",
			wantModel:      "actual-model-2026-07-18",
			wantUsage: openairesponses.Usage{
				InputTokens:  12,
				OutputTokens: 8,
				TotalTokens:  20,
			},
		},
		{
			name: "wrong field type",
			body: completedResponse(
				`{"summary":"Exact evidence.","evidence":"Exact evidence."}`,
			),
			wantErr:        openairesponses.ErrSchemaMismatch,
			wantResponseID: "resp_123",
			wantModel:      "actual-model-2026-07-18",
			wantUsage: openairesponses.Usage{
				InputTokens:  12,
				OutputTokens: 8,
				TotalTokens:  20,
			},
		},
		{
			name: "schema constraint mismatch",
			body: completedResponse(
				`{"summary":"","evidence":[]}`,
			),
			wantErr:        openairesponses.ErrSchemaMismatch,
			wantResponseID: "resp_123",
			wantModel:      "actual-model-2026-07-18",
			wantUsage: openairesponses.Usage{
				InputTokens:  12,
				OutputTokens: 8,
				TotalTokens:  20,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := newTestClient(t, server.Client(), server.URL)
			result, err := client.Create(context.Background(), validRequest())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Create() error = %v, want %v", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), testAPIKey) {
				t.Fatal("typed response error leaked API key")
			}
			assertRequestedModel(t, result, "configured-model")
			if result.ResponseID != tt.wantResponseID {
				t.Errorf("ResponseID = %q, want %q", result.ResponseID, tt.wantResponseID)
			}
			if result.Model != tt.wantModel {
				t.Errorf("Model = %q, want %q", result.Model, tt.wantModel)
			}
			if result.Usage.InputTokens != tt.wantUsage.InputTokens ||
				result.Usage.OutputTokens != tt.wantUsage.OutputTokens ||
				result.Usage.TotalTokens != tt.wantUsage.TotalTokens {
				t.Errorf("Usage = %#v, want partial usage %#v", result.Usage, tt.wantUsage)
			}
			if len(result.Usage.Raw) == 0 {
				t.Error("Usage.Raw is empty, want available provider usage JSON")
			}
			if requests.Load() != 1 {
				t.Fatalf(
					"requests = %d, want no retry or free-form repair request",
					requests.Load(),
				)
			}
		})
	}
}

func TestClientRejectsUsageWhoseTotalDoesNotEqualInputPlusOutput(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte(strings.Replace(
			completedResponse(`{"summary":"Exact evidence.","evidence":["Exact evidence."]}`),
			`"total_tokens":20`,
			`"total_tokens":21`,
			1,
		)))
	}))
	defer server.Close()

	client := newTestClient(t, server.Client(), server.URL)
	result, err := client.Create(context.Background(), validRequest())
	if !errors.Is(err, openairesponses.ErrInvalidResponse) {
		t.Fatalf("Create() error = %v, want ErrInvalidResponse", err)
	}
	assertRequestedModel(t, result, "configured-model")
	if result.ResponseID != "resp_123" ||
		result.Model != "actual-model-2026-07-18" {
		t.Fatalf("partial response identity = %#v", result)
	}
	if result.Usage.InputTokens != 0 ||
		result.Usage.OutputTokens != 0 ||
		result.Usage.TotalTokens != 0 ||
		len(result.Usage.Raw) != 0 {
		t.Fatalf("Usage = %#v, want invalid usage omitted", result.Usage)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want exactly one request", requests.Load())
	}
}

func TestClientRejectsAmbiguousOrUnauditableCompletedResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing response ID",
			body: strings.Replace(completedResponse(
				`{"summary":"Exact evidence.","evidence":["Exact evidence."]}`,
			), `"id":"resp_123",`, "", 1),
		},
		{
			name: "missing actual model",
			body: strings.Replace(completedResponse(
				`{"summary":"Exact evidence.","evidence":["Exact evidence."]}`,
			), `"model":"actual-model-2026-07-18",`, "", 1),
		},
		{
			name: "missing usage",
			body: strings.Replace(completedResponse(
				`{"summary":"Exact evidence.","evidence":["Exact evidence."]}`,
			), `"usage":`, `"omitted_usage":`, 1),
		},
		{
			name: "multiple output texts",
			body: `{
				"id":"resp_multiple",
				"status":"completed",
				"model":"actual-model",
				"output":[{
					"type":"message",
					"role":"assistant",
					"status":"completed",
					"content":[
						{"type":"output_text","text":"{\"summary\":\"one\",\"evidence\":[\"one\"]}"},
						{"type":"output_text","text":"{\"summary\":\"two\",\"evidence\":[\"two\"]}"}
					]
				}],
				"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
			}`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(tt.body))
			}))
			defer server.Close()

			client := newTestClient(t, server.Client(), server.URL)
			if _, err := client.Create(context.Background(), validRequest()); !errors.Is(
				err,
				openairesponses.ErrInvalidResponse,
			) {
				t.Fatalf("Create() error = %v, want ErrInvalidResponse", err)
			}
		})
	}
}

func TestClientRejectsNonStrictOrUnsupportedSchemaBeforeNetwork(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema string
	}{
		{
			name: "additional properties allowed",
			schema: `{
				"type":"object",
				"additionalProperties":true,
				"required":["summary"],
				"properties":{"summary":{"type":"string"}}
			}`,
		},
		{
			name: "property not required",
			schema: `{
				"type":"object",
				"additionalProperties":false,
				"required":[],
				"properties":{"summary":{"type":"string"}}
			}`,
		},
		{
			name: "unsupported keyword",
			schema: `{
				"type":"object",
				"additionalProperties":false,
				"required":["summary"],
				"properties":{"summary":{"type":"string","format":"citation"}}
			}`,
		},
		{
			name:   "malformed JSON",
			schema: `{"type":"object"`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			httpClient := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, errors.New("network must not be reached")
			})}
			client := newTestClient(t, httpClient, "https://api.example.test/v1")
			request := validRequest()
			request.OutputSchema.Schema = json.RawMessage(tt.schema)

			if _, err := client.Create(context.Background(), request); !errors.Is(
				err,
				openairesponses.ErrInvalidSchema,
			) {
				t.Fatalf("Create() error = %v, want ErrInvalidSchema", err)
			}
			if requests.Load() != 0 {
				t.Fatalf("requests = %d, want schema rejection before network", requests.Load())
			}
		})
	}
}

type requestEnvelope struct {
	Model string `json:"model"`
	Store *bool  `json:"store"`
	Input []struct {
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"input"`
	Text struct {
		Format struct {
			Type   string         `json:"type"`
			Name   string         `json:"name"`
			Schema map[string]any `json:"schema"`
			Strict bool           `json:"strict"`
		} `json:"format"`
	} `json:"text"`
}

func assertRequestedModel(
	t *testing.T,
	result openairesponses.Result,
	want string,
) {
	t.Helper()

	if result.RequestedModel != want {
		t.Errorf("RequestedModel = %q, want %q", result.RequestedModel, want)
	}
}

func assertInputMessage(t *testing.T, got struct {
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}, wantRole, wantText string) {
	t.Helper()

	if got.Role != wantRole {
		t.Errorf("role = %q, want %q", got.Role, wantRole)
	}
	if len(got.Content) != 1 {
		t.Fatalf("content parts = %d, want 1", len(got.Content))
	}
	if got.Content[0].Type != "input_text" || got.Content[0].Text != wantText {
		t.Errorf("content = %#v, want one exact input_text part", got.Content)
	}
}

func newTestClient(
	t *testing.T,
	httpClient *http.Client,
	baseURL string,
) *openairesponses.Client {
	t.Helper()

	client, err := openairesponses.New(httpClient, openairesponses.Config{
		BaseURL: baseURL,
		Mode:    openairesponses.ModeResponses,
		APIKey:  testAPIKey,
		Model:   "configured-model",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func validRequest() openairesponses.Request {
	return openairesponses.Request{
		Messages: []openairesponses.Message{
			{
				Role:    openairesponses.RoleDeveloper,
				Content: "Return only source-supported facts.",
			},
			{
				Role:    openairesponses.RoleUser,
				Content: "Title: Example\nAbstract: Exact evidence.",
			},
		},
		OutputSchema: openairesponses.JSONSchema{
			Name: "abstract_route_v1",
			Schema: json.RawMessage(`{
				"type":"object",
				"additionalProperties":false,
				"required":["summary","evidence"],
				"properties":{
					"summary":{"type":"string","minLength":1},
					"evidence":{
						"type":"array",
						"items":{"type":"string","minLength":1},
						"minItems":1
					}
				}
			}`),
		},
	}
}

func completedResponse(output string) string {
	encodedOutput, err := json.Marshal(output)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf(`{
		"id":"resp_123",
		"status":"completed",
		"model":"actual-model-2026-07-18",
		"output":[{
			"type":"message",
			"role":"assistant",
			"status":"completed",
			"content":[{"type":"output_text","text":%s}]
		}],
		"usage":{"input_tokens":12,"output_tokens":8,"total_tokens":20}
	}`, encodedOutput)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
