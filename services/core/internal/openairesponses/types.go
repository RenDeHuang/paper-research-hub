package openairesponses

import (
	"encoding/json"
	"errors"
)

var (
	ErrHTTPStatus              = errors.New("OpenAI Responses HTTP status rejected")
	ErrRefusal                 = errors.New("OpenAI Responses refusal")
	ErrIncomplete              = errors.New("OpenAI Responses output incomplete")
	ErrInvalidResponse         = errors.New("invalid OpenAI Responses response")
	ErrInvalidStructuredOutput = errors.New("invalid structured JSON output")
	ErrSchemaMismatch          = errors.New("structured output schema mismatch")
	ErrInvalidSchema           = errors.New("invalid strict output schema")
)

type Role string

type Mode string

const (
	RoleDeveloper Role = "developer"
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"

	ModeResponses       Mode = "responses"
	ModeChatCompletions Mode = "chat_completions"
)

type Config struct {
	BaseURL string
	Mode    Mode
	APIKey  string
	Model   string
}

type Message struct {
	Role    Role
	Content string
}

type JSONSchema struct {
	Name   string
	Schema json.RawMessage
}

type Request struct {
	Messages     []Message
	OutputSchema JSONSchema
}

type Usage struct {
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	Raw          json.RawMessage
}

type Result struct {
	ResponseID     string
	APIMode        Mode
	RequestedModel string
	Model          string
	Usage          Usage
	JSON           json.RawMessage
}
