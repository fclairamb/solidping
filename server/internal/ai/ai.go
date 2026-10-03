// Package ai is SolidPing's LLM provider layer (spec 2026-10-03-07). It knows
// two wire protocols, OpenAI Chat Completions (which also covers every
// OpenAI-compatible endpoint) and the native Anthropic Messages API, and a
// small agent loop that calls tools until the model stops or a turn cap is
// hit. No agent framework, nothing provider-specific hardcoded.
//
// The API key only ever lives in the process that calls the provider. It is
// never logged, and every provider error is scrubbed of it before it leaves a
// driver.
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
)

// Errors returned by the provider layer.
var (
	// ErrDisabled means no provider is configured.
	ErrDisabled = errors.New("ai: no provider configured")
	// ErrUnknownProvider means the configured provider name is not supported.
	ErrUnknownProvider = errors.New("ai: unknown provider")
	// ErrMaxTurns means the agent loop hit its turn cap before the model
	// finished.
	ErrMaxTurns = errors.New("ai: turn cap reached")
	// ErrCallFailed is a provider call that never got an answer.
	ErrCallFailed = errors.New("ai: provider call failed")
	// ErrNoChoice is an answer without any completion.
	ErrNoChoice = errors.New("ai: provider returned no completion")
)

// Role is a chat message role.
type Role string

// Message roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one chat turn. An assistant message may carry tool calls; a tool
// message answers one call (ToolCallID).
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	// ToolName is the name of the tool a RoleTool message answers.
	ToolName string
}

// ToolDef describes a tool the model may call. Parameters is a JSON Schema
// object.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall is one call the model asked for. Arguments is the raw JSON object.
type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

// Usage is the token count of one call.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// Total is input plus output tokens.
func (u Usage) Total() int {
	return u.InputTokens + u.OutputTokens
}

// Add sums two usages.
func (u Usage) Add(other Usage) Usage {
	return Usage{InputTokens: u.InputTokens + other.InputTokens, OutputTokens: u.OutputTokens + other.OutputTokens}
}

// Request is one completion request.
type Request struct {
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
}

// Response is one completion.
type Response struct {
	Text       string
	ToolCalls  []ToolCall
	StopReason string
	Usage      Usage
}

// Provider is an LLM endpoint.
type Provider interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}

// APIError is a non-2xx answer from a provider. Message never contains the
// API key.
type APIError struct {
	Provider   string
	StatusCode int
	Message    string
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("ai: %s returned HTTP %d: %s", e.Provider, e.StatusCode, e.Message)
}

// Client is the provider client behind the feature: the configured driver plus
// the model and loop settings.
type Client struct {
	Provider Provider
	Model    string
	MaxTurns int
	Timeout  time.Duration
}

const defaultMaxTokens = 4096

// New builds the client for the configured provider. It returns ErrDisabled
// when no provider is set. httpClient may be nil.
func New(cfg *config.AIConfig, httpClient *http.Client) (*Client, error) {
	if cfg == nil || !cfg.Enabled() {
		return nil, ErrDisabled
	}

	if httpClient == nil {
		httpClient = &http.Client{}
	}

	var provider Provider

	switch cfg.Provider {
	case config.AIProviderOpenAI:
		provider = NewOpenAI(cfg.BaseURL, cfg.APIKey, cfg.Model, httpClient)
	case config.AIProviderAnthropic:
		provider = NewAnthropic(cfg.BaseURL, cfg.APIKey, cfg.Model, httpClient)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, cfg.Provider)
	}

	return &Client{Provider: provider, Model: cfg.Model, MaxTurns: cfg.MaxTurns, Timeout: cfg.Timeout}, nil
}

// redact removes the API key from a provider message.
func redact(message, apiKey string) string {
	if apiKey == "" {
		return message
	}

	return strings.ReplaceAll(message, apiKey, "[redacted]")
}

// truncate bounds an error body.
func truncate(message string, limit int) string {
	if len(message) <= limit {
		return message
	}

	return message[:limit] + "..."
}
