package openairesponses_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/openairesponses"
)

func TestClientSendsStrictChatCompletionsRequest(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.EscapedPath() != "/v1/chat/completions" {
			t.Errorf(
				"path = %q, want /v1/chat/completions",
				request.URL.EscapedPath(),
			)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string          `json:"name"`
					Strict bool            `json:"strict"`
					Schema json.RawMessage `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != "configured-model" ||
			len(body.Messages) != 2 ||
			body.ResponseFormat.Type != "json_schema" ||
			body.ResponseFormat.JSONSchema.Name != "abstract_route_v1" ||
			!body.ResponseFormat.JSONSchema.Strict {
			t.Fatalf("strict chat request = %#v", body)
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"chatcmpl_123",
			"model":"actual-chat-model-2026-07-18",
			"choices":[{
				"index":0,
				"finish_reason":"stop",
				"message":{
					"role":"assistant",
					"content":"{\"summary\":\"Exact evidence.\",\"evidence\":[\"Exact evidence.\"]}"
				}
			}],
			"usage":{
				"prompt_tokens":12,
				"completion_tokens":8,
				"total_tokens":20
			}
		}`))
	}))
	defer server.Close()

	client, err := openairesponses.New(
		server.Client(),
		openairesponses.Config{
			BaseURL: server.URL + "/v1",
			Mode:    openairesponses.ModeChatCompletions,
			APIKey:  testAPIKey,
			Model:   "configured-model",
		},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := client.Create(context.Background(), validRequest())
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.APIMode != openairesponses.ModeChatCompletions ||
		result.ResponseID != "chatcmpl_123" ||
		result.Model != "actual-chat-model-2026-07-18" ||
		result.Usage.InputTokens != 12 ||
		result.Usage.OutputTokens != 8 ||
		result.Usage.TotalTokens != 20 ||
		string(result.JSON) !=
			`{"summary":"Exact evidence.","evidence":["Exact evidence."]}` {
		t.Fatalf("result = %#v", result)
	}
}
