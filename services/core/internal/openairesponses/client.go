package openairesponses

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"strings"
	"unicode/utf8"
)

const (
	maxResponseBytes = 8 << 20
	maxJSONDepth     = 128
)

type Client struct {
	httpClient *http.Client
	endpoint   string
	mode       Mode
	apiKey     string
	model      string
}

type classifiedError struct {
	kind   error
	detail string
}

func (err classifiedError) Error() string {
	if err.detail == "" {
		return err.kind.Error()
	}
	return err.kind.Error() + ": " + err.detail
}

func (err classifiedError) Unwrap() error {
	return err.kind
}

func New(httpClient *http.Client, config Config) (*Client, error) {
	if httpClient == nil {
		return nil, errors.New("OpenAI Responses HTTP client is required")
	}
	if config.BaseURL == "" || config.BaseURL != strings.TrimSpace(config.BaseURL) {
		return nil, errors.New("OpenAI Responses base URL must be non-empty and trimmed")
	}
	if config.APIKey == "" || config.APIKey != strings.TrimSpace(config.APIKey) {
		return nil, errors.New("OpenAI Responses API key must be non-empty and trimmed")
	}
	if !validHeaderValue(config.APIKey) {
		return nil, errors.New("OpenAI Responses API key is not a valid HTTP credential")
	}
	if config.Model == "" || config.Model != strings.TrimSpace(config.Model) {
		return nil, errors.New("OpenAI Responses model must be non-empty and trimmed")
	}
	switch config.Mode {
	case ModeResponses, ModeChatCompletions:
	default:
		return nil, errors.New(
			"OpenAI API mode must be responses or chat_completions",
		)
	}

	baseURL, err := url.Parse(config.BaseURL)
	if err != nil {
		return nil, errors.New("OpenAI Responses base URL is invalid")
	}
	if (baseURL.Scheme != "http" && baseURL.Scheme != "https") ||
		baseURL.Host == "" ||
		baseURL.Opaque != "" {
		return nil, errors.New("OpenAI Responses base URL must be an explicit HTTP(S) URL")
	}
	if baseURL.User != nil {
		return nil, errors.New("OpenAI Responses base URL must not contain credentials")
	}
	if baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, errors.New("OpenAI Responses base URL must not contain query or fragment")
	}
	if baseURL.RawPath != "" {
		return nil, errors.New("OpenAI Responses base URL must not contain an escaped base path")
	}
	if baseURL.Scheme == "http" && !isLoopbackHost(baseURL.Hostname()) {
		return nil, errors.New(
			"OpenAI Responses base URL must use HTTPS unless it targets a loopback host",
		)
	}

	normalizedPath := strings.TrimRight(baseURL.Path, "/")
	switch config.Mode {
	case ModeResponses:
		if strings.EqualFold(path.Base(normalizedPath), "responses") {
			return nil, errors.New(
				"OpenAI Responses base URL must be a base path, not the /responses endpoint",
			)
		}
		baseURL.Path = normalizedPath + "/responses"
	case ModeChatCompletions:
		if strings.HasSuffix(
			strings.ToLower(normalizedPath),
			"/chat/completions",
		) {
			return nil, errors.New(
				"OpenAI chat completions base URL must be a base path, not the /chat/completions endpoint",
			)
		}
		baseURL.Path = normalizedPath + "/chat/completions"
	}

	pinnedHTTPClient := *httpClient
	pinnedHTTPClient.CheckRedirect = func(
		_ *http.Request,
		_ []*http.Request,
	) error {
		return http.ErrUseLastResponse
	}

	return &Client{
		httpClient: &pinnedHTTPClient,
		endpoint:   baseURL.String(),
		mode:       config.Mode,
		apiKey:     config.APIKey,
		model:      config.Model,
	}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (client *Client) Create(ctx context.Context, input Request) (Result, error) {
	if client == nil {
		return Result{}, errors.New("OpenAI Responses client is nil")
	}
	if ctx == nil {
		return Result{}, errors.New("OpenAI Responses context is required")
	}
	if err := validateInput(input); err != nil {
		return Result{}, err
	}

	schema, err := compileSchema(input.OutputSchema.Schema)
	if err != nil {
		return Result{}, client.classified(ErrInvalidSchema, err.Error())
	}
	if client.mode == ModeChatCompletions {
		return client.createChatCompletions(ctx, input, schema)
	}

	requestBody := responseRequest{
		Model: client.model,
		Store: false,
		Input: make([]inputMessage, 0, len(input.Messages)),
		Text: responseText{
			Format: responseFormat{
				Type:   "json_schema",
				Name:   input.OutputSchema.Name,
				Schema: cloneRawMessage(input.OutputSchema.Schema),
				Strict: true,
			},
		},
	}
	for _, message := range input.Messages {
		requestBody.Input = append(requestBody.Input, inputMessage{
			Role: message.Role,
			Content: []inputContent{{
				Type: "input_text",
				Text: message.Content,
			}},
		})
	}

	encodedRequest, err := json.Marshal(requestBody)
	if err != nil {
		return Result{}, client.classified(
			ErrInvalidSchema,
			"strict request could not be encoded",
		)
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		client.endpoint,
		bytes.NewReader(encodedRequest),
	)
	if err != nil {
		return Result{}, errors.New("OpenAI Responses HTTP request could not be created")
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return Result{}, contextErr
		}
		return Result{}, client.plain(
			"OpenAI Responses transport failed: " + err.Error(),
		)
	}
	if response == nil {
		return Result{}, client.classified(ErrInvalidResponse, "HTTP response is nil")
	}
	if response.Body == nil {
		return Result{}, client.classified(ErrInvalidResponse, "HTTP response body is nil")
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		return Result{}, client.classified(
			ErrHTTPStatus,
			fmt.Sprintf("status %d", response.StatusCode),
		)
	}

	responseBody, err := readBounded(response.Body, maxResponseBytes)
	if err != nil {
		return Result{}, client.classified(
			ErrInvalidResponse,
			"response body could not be read within the fixed limit",
		)
	}

	var envelope responseEnvelope
	if _, err := decodeJSON(responseBody); err != nil {
		return Result{}, client.classified(
			ErrInvalidResponse,
			"response envelope is not one strict JSON value",
		)
	}
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return Result{}, client.classified(
			ErrInvalidResponse,
			"response envelope does not match the Responses API shape",
		)
	}

	return client.validateResponse(envelope, schema)
}

func (client *Client) validateResponse(
	envelope responseEnvelope,
	schema *compiledSchema,
) (Result, error) {
	result := Result{
		APIMode:        client.mode,
		RequestedModel: client.model,
	}
	if envelope.ID != "" && envelope.ID == strings.TrimSpace(envelope.ID) {
		result.ResponseID = envelope.ID
	}
	if envelope.Model != "" &&
		envelope.Model == strings.TrimSpace(envelope.Model) {
		result.Model = envelope.Model
	}
	usage, usageErr := parseUsage(envelope.Usage)
	if usageErr == nil {
		result.Usage = usage
	}

	if envelope.Status == "incomplete" || nonNullJSON(envelope.IncompleteDetails) {
		return result, client.classified(ErrIncomplete, "response did not complete")
	}
	if envelope.Status != "completed" {
		return result, client.classified(
			ErrInvalidResponse,
			"response status is not completed",
		)
	}
	if result.ResponseID == "" {
		return result, client.classified(
			ErrInvalidResponse,
			"completed response has no exact response ID",
		)
	}
	if result.Model == "" {
		return result, client.classified(
			ErrInvalidResponse,
			"completed response has no exact actual model",
		)
	}

	if usageErr != nil {
		return result, client.classified(ErrInvalidResponse, usageErr.Error())
	}

	var outputText string
	outputTextCount := 0
	for _, item := range envelope.Output {
		if item.Type != "message" {
			continue
		}
		if item.Status == "incomplete" {
			return result, client.classified(
				ErrIncomplete,
				"assistant message did not complete",
			)
		}
		if item.Status != "completed" || item.Role != "assistant" {
			return result, client.classified(
				ErrInvalidResponse,
				"assistant message is not an exact completed output",
			)
		}
		for _, content := range item.Content {
			switch content.Type {
			case "refusal":
				return result, client.classified(
					ErrRefusal,
					"provider returned a refusal instead of structured output",
				)
			case "output_text":
				outputTextCount++
				outputText = content.Text
			default:
				return result, client.classified(
					ErrInvalidResponse,
					"assistant message contains an unsupported content item",
				)
			}
		}
	}
	if outputTextCount != 1 || outputText == "" {
		return result, client.classified(
			ErrInvalidResponse,
			"completed response must contain exactly one structured output text",
		)
	}

	structuredJSON := json.RawMessage(outputText)
	document, err := decodeJSON(structuredJSON)
	if err != nil {
		return result, client.classified(
			ErrInvalidStructuredOutput,
			"output text is not one strict JSON value",
		)
	}
	if err := schema.validate(document); err != nil {
		return result, client.classified(ErrSchemaMismatch, err.Error())
	}

	result.JSON = cloneRawMessage(structuredJSON)
	return result, nil
}

func (client *Client) createChatCompletions(
	ctx context.Context,
	input Request,
	schema *compiledSchema,
) (Result, error) {
	requestBody := chatCompletionRequest{
		Model:    client.model,
		Messages: make([]chatMessage, 0, len(input.Messages)),
		ResponseFormat: chatResponseFormat{
			Type: "json_schema",
			JSONSchema: chatJSONSchema{
				Name:   input.OutputSchema.Name,
				Strict: true,
				Schema: cloneRawMessage(input.OutputSchema.Schema),
			},
		},
	}
	for _, message := range input.Messages {
		requestBody.Messages = append(requestBody.Messages, chatMessage{
			Role:    message.Role,
			Content: message.Content,
		})
	}

	encodedRequest, err := json.Marshal(requestBody)
	if err != nil {
		return Result{}, client.classified(
			ErrInvalidSchema,
			"strict chat completions request could not be encoded",
		)
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		client.endpoint,
		bytes.NewReader(encodedRequest),
	)
	if err != nil {
		return Result{}, errors.New(
			"OpenAI chat completions HTTP request could not be created",
		)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return Result{}, contextErr
		}
		return Result{}, client.plain(
			"OpenAI chat completions transport failed: " + err.Error(),
		)
	}
	if response == nil {
		return Result{}, client.classified(
			ErrInvalidResponse,
			"HTTP response is nil",
		)
	}
	if response.Body == nil {
		return Result{}, client.classified(
			ErrInvalidResponse,
			"HTTP response body is nil",
		)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		return Result{}, client.classified(
			ErrHTTPStatus,
			fmt.Sprintf("status %d", response.StatusCode),
		)
	}

	responseBody, err := readBounded(response.Body, maxResponseBytes)
	if err != nil {
		return Result{}, client.classified(
			ErrInvalidResponse,
			"response body could not be read within the fixed limit",
		)
	}
	if _, err := decodeJSON(responseBody); err != nil {
		return Result{}, client.classified(
			ErrInvalidResponse,
			"response envelope is not one strict JSON value",
		)
	}
	var envelope chatCompletionEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		return Result{}, client.classified(
			ErrInvalidResponse,
			"response envelope does not match the chat completions API shape",
		)
	}
	return client.validateChatCompletion(envelope, schema)
}

func (client *Client) validateChatCompletion(
	envelope chatCompletionEnvelope,
	schema *compiledSchema,
) (Result, error) {
	result := Result{
		APIMode:        client.mode,
		RequestedModel: client.model,
	}
	if envelope.ID != "" && envelope.ID == strings.TrimSpace(envelope.ID) {
		result.ResponseID = envelope.ID
	}
	if envelope.Model != "" &&
		envelope.Model == strings.TrimSpace(envelope.Model) {
		result.Model = envelope.Model
	}
	usage, usageErr := parseChatUsage(envelope.Usage)
	if usageErr == nil {
		result.Usage = usage
	}

	if result.ResponseID == "" {
		return result, client.classified(
			ErrInvalidResponse,
			"completed chat response has no exact response ID",
		)
	}
	if result.Model == "" {
		return result, client.classified(
			ErrInvalidResponse,
			"completed chat response has no exact actual model",
		)
	}
	if usageErr != nil {
		return result, client.classified(ErrInvalidResponse, usageErr.Error())
	}
	if len(envelope.Choices) != 1 {
		return result, client.classified(
			ErrInvalidResponse,
			"chat response must contain exactly one choice",
		)
	}
	choice := envelope.Choices[0]
	if choice.Index != 0 {
		return result, client.classified(
			ErrInvalidResponse,
			"chat response choice index must be zero",
		)
	}
	switch choice.FinishReason {
	case "stop":
	case "length":
		return result, client.classified(
			ErrIncomplete,
			"chat response reached its output limit",
		)
	case "content_filter":
		return result, client.classified(
			ErrRefusal,
			"provider content filter rejected the structured output",
		)
	default:
		return result, client.classified(
			ErrInvalidResponse,
			"chat response has an unsupported finish reason",
		)
	}
	if choice.Message.Role != string(RoleAssistant) {
		return result, client.classified(
			ErrInvalidResponse,
			"chat response message is not an assistant output",
		)
	}
	if nonNullJSON(choice.Message.Refusal) {
		return result, client.classified(
			ErrRefusal,
			"provider returned a refusal instead of structured output",
		)
	}
	var outputText string
	if err := json.Unmarshal(choice.Message.Content, &outputText); err != nil ||
		outputText == "" {
		return result, client.classified(
			ErrInvalidResponse,
			"chat response content must be one non-empty string",
		)
	}

	structuredJSON := json.RawMessage(outputText)
	document, err := decodeJSON(structuredJSON)
	if err != nil {
		return result, client.classified(
			ErrInvalidStructuredOutput,
			"chat response content is not one strict JSON value",
		)
	}
	if err := schema.validate(document); err != nil {
		return result, client.classified(ErrSchemaMismatch, err.Error())
	}
	result.JSON = cloneRawMessage(structuredJSON)
	return result, nil
}

func validateInput(input Request) error {
	if len(input.Messages) == 0 {
		return errors.New("OpenAI Responses input messages are required")
	}
	for index, message := range input.Messages {
		switch message.Role {
		case RoleDeveloper, RoleSystem, RoleUser, RoleAssistant:
		default:
			return fmt.Errorf(
				"OpenAI Responses input message %d has an unsupported role",
				index,
			)
		}
		if strings.TrimSpace(message.Content) == "" {
			return fmt.Errorf(
				"OpenAI Responses input message %d has empty content",
				index,
			)
		}
	}
	if !validSchemaName(input.OutputSchema.Name) {
		return classifiedError{
			kind:   ErrInvalidSchema,
			detail: "schema name must use 1-64 letters, digits, underscores, or hyphens",
		}
	}
	if len(input.OutputSchema.Schema) == 0 {
		return classifiedError{
			kind:   ErrInvalidSchema,
			detail: "schema JSON is required",
		}
	}
	return nil
}

func validHeaderValue(value string) bool {
	for _, character := range []byte(value) {
		if character == '\r' ||
			character == '\n' ||
			character == 0x7f ||
			character < 0x20 {
			return false
		}
	}
	return true
}

func validSchemaName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name != strings.TrimSpace(name) {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '_' ||
			character == '-' {
			continue
		}
		return false
	}
	return true
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("response body exceeds limit")
	}
	return body, nil
}

func parseUsage(raw json.RawMessage) (Usage, error) {
	if !nonNullJSON(raw) {
		return Usage{}, errors.New("completed response has no usage metadata")
	}
	decoded, err := decodeJSON(raw)
	if err != nil {
		return Usage{}, errors.New("usage metadata is not strict JSON")
	}
	if _, ok := decoded.(map[string]any); !ok {
		return Usage{}, errors.New("usage metadata is not an object")
	}

	var wire struct {
		InputTokens  *int64 `json:"input_tokens"`
		OutputTokens *int64 `json:"output_tokens"`
		TotalTokens  *int64 `json:"total_tokens"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil ||
		wire.InputTokens == nil ||
		wire.OutputTokens == nil ||
		wire.TotalTokens == nil ||
		*wire.InputTokens < 0 ||
		*wire.OutputTokens < 0 ||
		*wire.TotalTokens < 0 {
		return Usage{}, errors.New("usage metadata has invalid token counts")
	}
	if *wire.TotalTokens < *wire.InputTokens ||
		*wire.TotalTokens-*wire.InputTokens != *wire.OutputTokens {
		return Usage{}, errors.New(
			"usage total_tokens does not equal input_tokens plus output_tokens",
		)
	}
	return Usage{
		InputTokens:  *wire.InputTokens,
		OutputTokens: *wire.OutputTokens,
		TotalTokens:  *wire.TotalTokens,
		Raw:          cloneRawMessage(raw),
	}, nil
}

func parseChatUsage(raw json.RawMessage) (Usage, error) {
	if !nonNullJSON(raw) {
		return Usage{}, errors.New(
			"completed chat response has no usage metadata",
		)
	}
	decoded, err := decodeJSON(raw)
	if err != nil {
		return Usage{}, errors.New("chat usage metadata is not strict JSON")
	}
	if _, ok := decoded.(map[string]any); !ok {
		return Usage{}, errors.New("chat usage metadata is not an object")
	}

	var wire struct {
		PromptTokens     *int64 `json:"prompt_tokens"`
		CompletionTokens *int64 `json:"completion_tokens"`
		TotalTokens      *int64 `json:"total_tokens"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil ||
		wire.PromptTokens == nil ||
		wire.CompletionTokens == nil ||
		wire.TotalTokens == nil ||
		*wire.PromptTokens < 0 ||
		*wire.CompletionTokens < 0 ||
		*wire.TotalTokens < 0 {
		return Usage{}, errors.New(
			"chat usage metadata has invalid token counts",
		)
	}
	if *wire.TotalTokens < *wire.PromptTokens ||
		*wire.TotalTokens-*wire.PromptTokens != *wire.CompletionTokens {
		return Usage{}, errors.New(
			"chat usage total_tokens does not equal prompt_tokens plus completion_tokens",
		)
	}
	return Usage{
		InputTokens:  *wire.PromptTokens,
		OutputTokens: *wire.CompletionTokens,
		TotalTokens:  *wire.TotalTokens,
		Raw:          cloneRawMessage(raw),
	}, nil
}

func nonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func (client *Client) classified(kind error, detail string) error {
	return classifiedError{
		kind:   kind,
		detail: client.redact(detail),
	}
}

func (client *Client) plain(message string) error {
	return errors.New(client.redact(message))
}

func (client *Client) redact(message string) string {
	redacted := strings.ReplaceAll(message, "Bearer "+client.apiKey, "[REDACTED]")
	return strings.ReplaceAll(redacted, client.apiKey, "[REDACTED]")
}

type responseRequest struct {
	Model string         `json:"model"`
	Store bool           `json:"store"`
	Input []inputMessage `json:"input"`
	Text  responseText   `json:"text"`
}

type inputMessage struct {
	Role    Role           `json:"role"`
	Content []inputContent `json:"content"`
}

type inputContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responseText struct {
	Format responseFormat `json:"format"`
}

type responseFormat struct {
	Type   string          `json:"type"`
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
}

type responseEnvelope struct {
	ID                string          `json:"id"`
	Status            string          `json:"status"`
	IncompleteDetails json.RawMessage `json:"incomplete_details"`
	Model             string          `json:"model"`
	Output            []outputItem    `json:"output"`
	Usage             json.RawMessage `json:"usage"`
}

type outputItem struct {
	Type    string        `json:"type"`
	Status  string        `json:"status"`
	Role    string        `json:"role"`
	Content []contentItem `json:"content"`
}

type contentItem struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

type chatCompletionRequest struct {
	Model          string             `json:"model"`
	Messages       []chatMessage      `json:"messages"`
	ResponseFormat chatResponseFormat `json:"response_format"`
}

type chatMessage struct {
	Role    Role   `json:"role"`
	Content string `json:"content"`
}

type chatResponseFormat struct {
	Type       string         `json:"type"`
	JSONSchema chatJSONSchema `json:"json_schema"`
}

type chatJSONSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type chatCompletionEnvelope struct {
	ID      string                 `json:"id"`
	Model   string                 `json:"model"`
	Choices []chatCompletionChoice `json:"choices"`
	Usage   json.RawMessage        `json:"usage"`
}

type chatCompletionChoice struct {
	Index        int                   `json:"index"`
	FinishReason string                `json:"finish_reason"`
	Message      chatCompletionMessage `json:"message"`
}

type chatCompletionMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Refusal json.RawMessage `json:"refusal"`
}

type compiledSchema struct {
	root map[string]any
}

func compileSchema(raw json.RawMessage) (*compiledSchema, error) {
	decoded, err := decodeJSON(raw)
	if err != nil {
		return nil, errors.New("schema must be one strict JSON value")
	}
	root, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("schema root must be an object")
	}
	if !schemaHasExactType(root, "object") {
		return nil, errors.New("strict schema root type must be object")
	}
	if err := validateSchemaDefinition(root, root, "#", 0); err != nil {
		return nil, err
	}
	return &compiledSchema{root: root}, nil
}

func (schema *compiledSchema) validate(document any) error {
	if schema == nil {
		return errors.New("compiled schema is nil")
	}
	return validateJSONValue(document, schema.root, schema.root, "$", 0)
}

var supportedSchemaKeywords = map[string]struct{}{
	"$schema":              {},
	"$id":                  {},
	"$defs":                {},
	"definitions":          {},
	"$ref":                 {},
	"title":                {},
	"description":          {},
	"type":                 {},
	"properties":           {},
	"required":             {},
	"additionalProperties": {},
	"items":                {},
	"enum":                 {},
	"const":                {},
	"minLength":            {},
	"maxLength":            {},
	"minItems":             {},
	"maxItems":             {},
	"minProperties":        {},
	"maxProperties":        {},
	"minimum":              {},
	"maximum":              {},
	"oneOf":                {},
	"anyOf":                {},
	"allOf":                {},
	"not":                  {},
	"if":                   {},
	"then":                 {},
	"else":                 {},
}

func validateSchemaDefinition(
	schema map[string]any,
	root map[string]any,
	location string,
	depth int,
) error {
	if depth > maxJSONDepth {
		return fmt.Errorf("%s exceeds maximum schema depth", location)
	}
	for keyword := range schema {
		if _, supported := supportedSchemaKeywords[keyword]; !supported {
			return fmt.Errorf("%s contains unsupported keyword %q", location, keyword)
		}
	}

	if rawType, exists := schema["type"]; exists {
		if err := validateSchemaType(rawType, location); err != nil {
			return err
		}
	}
	if reference, exists := schema["$ref"]; exists {
		referenceString, ok := reference.(string)
		if !ok {
			return fmt.Errorf("%s $ref must be a string", location)
		}
		if _, err := resolveLocalReference(root, referenceString); err != nil {
			return fmt.Errorf("%s has invalid $ref", location)
		}
	}

	for _, definitionsKeyword := range []string{"$defs", "definitions"} {
		rawDefinitions, exists := schema[definitionsKeyword]
		if !exists {
			continue
		}
		definitions, ok := rawDefinitions.(map[string]any)
		if !ok {
			return fmt.Errorf("%s %s must be an object", location, definitionsKeyword)
		}
		for name, rawDefinition := range definitions {
			definition, ok := rawDefinition.(map[string]any)
			if !ok {
				return fmt.Errorf(
					"%s/%s/%s must be a schema object",
					location,
					definitionsKeyword,
					name,
				)
			}
			if err := validateSchemaDefinition(
				definition,
				root,
				location+"/"+definitionsKeyword+"/"+name,
				depth+1,
			); err != nil {
				return err
			}
		}
	}

	properties, err := schemaProperties(schema, location)
	if err != nil {
		return err
	}
	required, err := schemaRequired(schema, location)
	if err != nil {
		return err
	}
	for name, property := range properties {
		if err := validateSchemaDefinition(
			property,
			root,
			location+"/properties/"+name,
			depth+1,
		); err != nil {
			return err
		}
	}

	if schemaDeclaresType(schema, "object") {
		additionalProperties, exists := schema["additionalProperties"]
		allowAdditional, ok := additionalProperties.(bool)
		if !exists || !ok || allowAdditional {
			return fmt.Errorf(
				"%s strict object must set additionalProperties=false",
				location,
			)
		}
		requiredSet := make(map[string]struct{}, len(required))
		for _, name := range required {
			requiredSet[name] = struct{}{}
			if _, exists := properties[name]; !exists {
				return fmt.Errorf(
					"%s requires undefined property %q",
					location,
					name,
				)
			}
		}
		for name := range properties {
			if _, exists := requiredSet[name]; !exists {
				return fmt.Errorf(
					"%s strict property %q must be required",
					location,
					name,
				)
			}
		}
	}

	if schemaDeclaresType(schema, "array") {
		rawItems, exists := schema["items"]
		items, ok := rawItems.(map[string]any)
		if !exists || !ok {
			return fmt.Errorf("%s strict array must define one items schema", location)
		}
		if err := validateSchemaDefinition(
			items,
			root,
			location+"/items",
			depth+1,
		); err != nil {
			return err
		}
	} else if rawItems, exists := schema["items"]; exists {
		items, ok := rawItems.(map[string]any)
		if !ok {
			return fmt.Errorf("%s items must be a schema object", location)
		}
		if err := validateSchemaDefinition(
			items,
			root,
			location+"/items",
			depth+1,
		); err != nil {
			return err
		}
	}

	for _, integerKeyword := range []string{
		"minLength",
		"maxLength",
		"minItems",
		"maxItems",
		"minProperties",
		"maxProperties",
	} {
		if rawValue, exists := schema[integerKeyword]; exists {
			if _, err := nonNegativeSchemaInteger(rawValue); err != nil {
				return fmt.Errorf(
					"%s %s must be a non-negative integer",
					location,
					integerKeyword,
				)
			}
		}
	}
	for _, numberKeyword := range []string{"minimum", "maximum"} {
		if rawValue, exists := schema[numberKeyword]; exists {
			if _, ok := rawValue.(json.Number); !ok {
				return fmt.Errorf("%s %s must be a number", location, numberKeyword)
			}
		}
	}
	if rawEnum, exists := schema["enum"]; exists {
		enum, ok := rawEnum.([]any)
		if !ok || len(enum) == 0 {
			return fmt.Errorf("%s enum must be a non-empty array", location)
		}
	}

	for _, keyword := range []string{"oneOf", "anyOf", "allOf"} {
		rawBranches, exists := schema[keyword]
		if !exists {
			continue
		}
		branches, ok := rawBranches.([]any)
		if !ok || len(branches) == 0 {
			return fmt.Errorf("%s %s must be a non-empty array", location, keyword)
		}
		for index, rawBranch := range branches {
			branch, ok := rawBranch.(map[string]any)
			if !ok {
				return fmt.Errorf(
					"%s/%s/%d must be a schema object",
					location,
					keyword,
					index,
				)
			}
			if err := validateSchemaDefinition(
				branch,
				root,
				fmt.Sprintf("%s/%s/%d", location, keyword, index),
				depth+1,
			); err != nil {
				return err
			}
		}
	}
	for _, keyword := range []string{"not", "if", "then", "else"} {
		rawBranch, exists := schema[keyword]
		if !exists {
			continue
		}
		branch, ok := rawBranch.(map[string]any)
		if !ok {
			return fmt.Errorf("%s/%s must be a schema object", location, keyword)
		}
		if err := validateSchemaDefinition(
			branch,
			root,
			location+"/"+keyword,
			depth+1,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateSchemaType(raw any, location string) error {
	switch value := raw.(type) {
	case string:
		if !validJSONType(value) {
			return fmt.Errorf("%s contains unsupported type %q", location, value)
		}
	case []any:
		if len(value) == 0 {
			return fmt.Errorf("%s type array must not be empty", location)
		}
		seen := make(map[string]struct{}, len(value))
		for _, rawType := range value {
			typeName, ok := rawType.(string)
			if !ok || !validJSONType(typeName) {
				return fmt.Errorf("%s contains an invalid union type", location)
			}
			if _, duplicate := seen[typeName]; duplicate {
				return fmt.Errorf("%s contains duplicate union type %q", location, typeName)
			}
			seen[typeName] = struct{}{}
		}
	default:
		return fmt.Errorf("%s type must be a string or string array", location)
	}
	return nil
}

func validJSONType(value string) bool {
	switch value {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	default:
		return false
	}
}

func schemaHasExactType(schema map[string]any, wanted string) bool {
	value, ok := schema["type"].(string)
	return ok && value == wanted
}

func schemaDeclaresType(schema map[string]any, wanted string) bool {
	switch value := schema["type"].(type) {
	case string:
		return value == wanted
	case []any:
		for _, member := range value {
			if member == wanted {
				return true
			}
		}
	}
	return false
}

func schemaProperties(
	schema map[string]any,
	location string,
) (map[string]map[string]any, error) {
	rawProperties, exists := schema["properties"]
	if !exists {
		return map[string]map[string]any{}, nil
	}
	propertyValues, ok := rawProperties.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s properties must be an object", location)
	}
	properties := make(map[string]map[string]any, len(propertyValues))
	for name, rawProperty := range propertyValues {
		property, ok := rawProperty.(map[string]any)
		if !ok {
			return nil, fmt.Errorf(
				"%s property %q must be a schema object",
				location,
				name,
			)
		}
		properties[name] = property
	}
	return properties, nil
}

func schemaRequired(schema map[string]any, location string) ([]string, error) {
	rawRequired, exists := schema["required"]
	if !exists {
		return nil, nil
	}
	requiredValues, ok := rawRequired.([]any)
	if !ok {
		return nil, fmt.Errorf("%s required must be an array", location)
	}
	required := make([]string, 0, len(requiredValues))
	seen := make(map[string]struct{}, len(requiredValues))
	for _, rawName := range requiredValues {
		name, ok := rawName.(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("%s required contains an invalid property name", location)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("%s required contains duplicate property %q", location, name)
		}
		seen[name] = struct{}{}
		required = append(required, name)
	}
	return required, nil
}

func validateJSONValue(
	value any,
	schema map[string]any,
	root map[string]any,
	location string,
	depth int,
) error {
	if depth > maxJSONDepth {
		return fmt.Errorf("%s exceeds maximum validation depth", location)
	}
	if rawReference, exists := schema["$ref"]; exists {
		reference := rawReference.(string)
		resolved, err := resolveLocalReference(root, reference)
		if err != nil {
			return fmt.Errorf("%s has an invalid schema reference", location)
		}
		if err := validateJSONValue(value, resolved, root, location, depth+1); err != nil {
			return err
		}
	}
	if rawType, exists := schema["type"]; exists && !matchesSchemaType(value, rawType) {
		return fmt.Errorf("%s has the wrong JSON type", location)
	}
	if expected, exists := schema["const"]; exists && !equalJSON(value, expected) {
		return fmt.Errorf("%s does not match const", location)
	}
	if rawEnum, exists := schema["enum"]; exists {
		matched := false
		for _, option := range rawEnum.([]any) {
			if equalJSON(value, option) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed enum value", location)
		}
	}

	if branches, exists := schema["allOf"]; exists {
		for _, rawBranch := range branches.([]any) {
			if err := validateJSONValue(
				value,
				rawBranch.(map[string]any),
				root,
				location,
				depth+1,
			); err != nil {
				return err
			}
		}
	}
	if branches, exists := schema["anyOf"]; exists {
		matches := 0
		for _, rawBranch := range branches.([]any) {
			if validateJSONValue(
				value,
				rawBranch.(map[string]any),
				root,
				location,
				depth+1,
			) == nil {
				matches++
			}
		}
		if matches == 0 {
			return fmt.Errorf("%s does not match anyOf", location)
		}
	}
	if branches, exists := schema["oneOf"]; exists {
		matches := 0
		for _, rawBranch := range branches.([]any) {
			if validateJSONValue(
				value,
				rawBranch.(map[string]any),
				root,
				location,
				depth+1,
			) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s does not match exactly one oneOf branch", location)
		}
	}
	if rawNot, exists := schema["not"]; exists {
		if validateJSONValue(
			value,
			rawNot.(map[string]any),
			root,
			location,
			depth+1,
		) == nil {
			return fmt.Errorf("%s matches forbidden schema", location)
		}
	}
	if rawIf, exists := schema["if"]; exists {
		conditionMatches := validateJSONValue(
			value,
			rawIf.(map[string]any),
			root,
			location,
			depth+1,
		) == nil
		branchKeyword := "else"
		if conditionMatches {
			branchKeyword = "then"
		}
		if rawBranch, branchExists := schema[branchKeyword]; branchExists {
			if err := validateJSONValue(
				value,
				rawBranch.(map[string]any),
				root,
				location,
				depth+1,
			); err != nil {
				return err
			}
		}
	}

	switch typedValue := value.(type) {
	case map[string]any:
		if err := validateObjectValue(typedValue, schema, root, location, depth); err != nil {
			return err
		}
	case []any:
		if err := validateArrayValue(typedValue, schema, root, location, depth); err != nil {
			return err
		}
	case string:
		if err := validateStringValue(typedValue, schema, location); err != nil {
			return err
		}
	case json.Number:
		if err := validateNumberValue(typedValue, schema, location); err != nil {
			return err
		}
	}
	return nil
}

func validateObjectValue(
	value map[string]any,
	schema map[string]any,
	root map[string]any,
	location string,
	depth int,
) error {
	properties, err := schemaProperties(schema, location)
	if err != nil {
		return err
	}
	required, err := schemaRequired(schema, location)
	if err != nil {
		return err
	}
	for _, name := range required {
		if _, exists := value[name]; !exists {
			return fmt.Errorf("%s is missing required property %q", location, name)
		}
	}
	for name, propertyValue := range value {
		propertySchema, exists := properties[name]
		if !exists {
			if allowAdditional, explicitlySet := schema["additionalProperties"].(bool); explicitlySet &&
				!allowAdditional {
				return fmt.Errorf("%s contains additional property %q", location, name)
			}
			continue
		}
		if err := validateJSONValue(
			propertyValue,
			propertySchema,
			root,
			location+"."+name,
			depth+1,
		); err != nil {
			return err
		}
	}
	if err := validateCollectionBounds(
		len(value),
		schema,
		"minProperties",
		"maxProperties",
		location,
	); err != nil {
		return err
	}
	return nil
}

func validateArrayValue(
	value []any,
	schema map[string]any,
	root map[string]any,
	location string,
	depth int,
) error {
	if rawItems, exists := schema["items"]; exists {
		items := rawItems.(map[string]any)
		for index, item := range value {
			if err := validateJSONValue(
				item,
				items,
				root,
				fmt.Sprintf("%s[%d]", location, index),
				depth+1,
			); err != nil {
				return err
			}
		}
	}
	return validateCollectionBounds(
		len(value),
		schema,
		"minItems",
		"maxItems",
		location,
	)
}

func validateStringValue(
	value string,
	schema map[string]any,
	location string,
) error {
	length := utf8.RuneCountInString(value)
	return validateCollectionBounds(
		length,
		schema,
		"minLength",
		"maxLength",
		location,
	)
}

func validateCollectionBounds(
	length int,
	schema map[string]any,
	minimumKeyword string,
	maximumKeyword string,
	location string,
) error {
	if rawMinimum, exists := schema[minimumKeyword]; exists {
		minimum, _ := nonNegativeSchemaInteger(rawMinimum)
		if length < minimum {
			return fmt.Errorf("%s violates %s", location, minimumKeyword)
		}
	}
	if rawMaximum, exists := schema[maximumKeyword]; exists {
		maximum, _ := nonNegativeSchemaInteger(rawMaximum)
		if length > maximum {
			return fmt.Errorf("%s violates %s", location, maximumKeyword)
		}
	}
	return nil
}

func validateNumberValue(
	value json.Number,
	schema map[string]any,
	location string,
) error {
	number, ok := new(big.Rat).SetString(value.String())
	if !ok {
		return fmt.Errorf("%s is not an exact JSON number", location)
	}
	if rawMinimum, exists := schema["minimum"]; exists {
		minimum, _ := new(big.Rat).SetString(rawMinimum.(json.Number).String())
		if number.Cmp(minimum) < 0 {
			return fmt.Errorf("%s is less than minimum", location)
		}
	}
	if rawMaximum, exists := schema["maximum"]; exists {
		maximum, _ := new(big.Rat).SetString(rawMaximum.(json.Number).String())
		if number.Cmp(maximum) > 0 {
			return fmt.Errorf("%s is greater than maximum", location)
		}
	}
	return nil
}

func matchesSchemaType(value any, rawType any) bool {
	switch typedType := rawType.(type) {
	case string:
		return matchesJSONType(value, typedType)
	case []any:
		for _, member := range typedType {
			if matchesJSONType(value, member.(string)) {
				return true
			}
		}
	}
	return false
}

func matchesJSONType(value any, typeName string) bool {
	switch typeName {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		rational, valid := new(big.Rat).SetString(number.String())
		return valid && rational.IsInt()
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func nonNegativeSchemaInteger(value any) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("not a number")
	}
	integer, err := number.Int64()
	if err != nil || integer < 0 || int64(int(integer)) != integer {
		return 0, errors.New("not a non-negative integer")
	}
	return int(integer), nil
}

func equalJSON(left any, right any) bool {
	leftNumber, leftIsNumber := left.(json.Number)
	rightNumber, rightIsNumber := right.(json.Number)
	if leftIsNumber || rightIsNumber {
		if !leftIsNumber || !rightIsNumber {
			return false
		}
		leftRational, leftOK := new(big.Rat).SetString(leftNumber.String())
		rightRational, rightOK := new(big.Rat).SetString(rightNumber.String())
		return leftOK && rightOK && leftRational.Cmp(rightRational) == 0
	}
	return reflect.DeepEqual(left, right)
}

func resolveLocalReference(
	root map[string]any,
	reference string,
) (map[string]any, error) {
	if reference == "#" {
		return root, nil
	}
	if !strings.HasPrefix(reference, "#/") {
		return nil, errors.New("only local JSON pointers are supported")
	}
	var current any = root
	for _, encodedToken := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		token := strings.ReplaceAll(
			strings.ReplaceAll(encodedToken, "~1", "/"),
			"~0",
			"~",
		)
		object, ok := current.(map[string]any)
		if !ok {
			return nil, errors.New("reference does not resolve to an object")
		}
		current, ok = object[token]
		if !ok {
			return nil, errors.New("reference target is missing")
		}
	}
	resolved, ok := current.(map[string]any)
	if !ok {
		return nil, errors.New("reference target is not a schema object")
	}
	return resolved, nil
}

func decodeJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func decodeJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, errors.New("JSON exceeds maximum depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}

	switch delimiter {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			rawKey, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := rawKey.(string)
			if !ok {
				return nil, errors.New("JSON object key is not a string")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, errors.New("JSON object contains duplicate key")
			}
			value, err := decodeJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New("JSON object is not closed")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New("JSON array is not closed")
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}
