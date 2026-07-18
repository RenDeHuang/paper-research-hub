package openairesponses_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/RenDeHuang/paper-research-hub/services/core/internal/abstractanalysis"
	"github.com/RenDeHuang/paper-research-hub/services/core/internal/openairesponses"
)

func TestClientAcceptsTheFrozenAbstractRouteSchemaAndEvidenceResult(t *testing.T) {
	t.Parallel()

	const abstract = "Patients with advanced cancer were evaluated in an external validation cohort."
	output := abstractRouteOutput(t)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		var body struct {
			Model string `json:"model"`
			Store *bool  `json:"store"`
			Input any    `json:"input"`
			Text  struct {
				Format struct {
					Type   string          `json:"type"`
					Name   string          `json:"name"`
					Schema json.RawMessage `json:"schema"`
					Strict bool            `json:"strict"`
				} `json:"format"`
			} `json:"text"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Text.Format.Name != abstractanalysis.SchemaName ||
			!body.Text.Format.Strict ||
			string(body.Text.Format.Schema) !=
				string(abstractanalysis.SchemaJSON()) {
			t.Errorf("strict abstract-route format = %#v", body.Text.Format)
		}
		if body.Store == nil || *body.Store {
			t.Errorf("store = %#v, want explicit false", body.Store)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"resp_abstract_route",
			"status":"completed",
			"incomplete_details":null,
			"model":"gpt-5.4-mini-2026-06-01",
			"output":[{
				"type":"message",
				"status":"completed",
				"role":"assistant",
				"content":[{
					"type":"output_text",
					"text":` + mustJSONQuote(t, output) + `
				}]
			}],
			"usage":{
				"input_tokens":100,
				"output_tokens":200,
				"total_tokens":300
			}
		}`))
	}))
	defer server.Close()

	client, err := openairesponses.New(
		server.Client(),
		openairesponses.Config{
			BaseURL: server.URL + "/v1",
			Mode:    openairesponses.ModeResponses,
			APIKey:  "test-openai-key",
			Model:   "gpt-5.4-mini",
		},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response, err := client.Create(
		context.Background(),
		openairesponses.Request{
			Messages: []openairesponses.Message{
				{
					Role:    openairesponses.RoleUser,
					Content: abstract,
				},
			},
			OutputSchema: openairesponses.JSONSchema{
				Name:   abstractanalysis.SchemaName,
				Schema: abstractanalysis.SchemaJSON(),
			},
		},
	)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	result, err := abstractanalysis.DecodeResult(response.JSON)
	if err != nil {
		t.Fatalf("DecodeResult() error = %v", err)
	}
	if err := abstractanalysis.ValidateEvidence(abstract, result); err != nil {
		t.Fatalf("ValidateEvidence() error = %v", err)
	}
	if response.Model != "gpt-5.4-mini-2026-06-01" ||
		response.ResponseID != "resp_abstract_route" {
		t.Fatalf("auditable response identity = %#v", response)
	}
	assertRequestedModel(t, response, "gpt-5.4-mini")
}

func abstractRouteOutput(t *testing.T) string {
	t.Helper()

	fields := make([]string, 0, len(abstractanalysis.FieldNames()))
	for _, name := range abstractanalysis.FieldNames() {
		field := map[string]any{
			"state":    "not_reported",
			"value":    "",
			"evidence": []string{},
		}
		if name == "domain" {
			field = map[string]any{
				"state":    "supported",
				"value":    "medicine",
				"evidence": []string{"Patients with advanced cancer"},
			}
		}
		encoded, err := json.Marshal(field)
		if err != nil {
			t.Fatalf("json.Marshal(%s) error = %v", name, err)
		}
		fields = append(fields, mustJSONQuote(t, name)+":"+string(encoded))
	}
	return "{" + strings.Join(fields, ",") + "}"
}

func mustJSONQuote(t *testing.T, value string) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return string(encoded)
}
