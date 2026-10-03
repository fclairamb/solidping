package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	providerOpenAI       = "openai"
	defaultOpenAIBaseURL = "https://api.openai.com/v1"
	maxErrorBody         = 512
	maxResponseBody      = 8 << 20
)

// OpenAI is the Chat Completions driver (POST {base_url}/chat/completions).
// It covers OpenAI and every compatible endpoint: BytePlus ModelArk, Mistral,
// Groq, DeepSeek, Gemini's OpenAI endpoint, OpenRouter, LiteLLM, Ollama, vLLM.
// Unknown response fields (reasoning_content...) are ignored.
type OpenAI struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewOpenAI builds the driver. An empty baseURL uses OpenAI's.
func NewOpenAI(baseURL, apiKey, model string, httpClient *http.Client) *OpenAI {
	if baseURL == "" {
		baseURL = defaultOpenAIBaseURL
	}

	return &OpenAI{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, model: model, http: httpClient}
}

// The wire structs below follow OpenAI's snake_case JSON.
type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

//nolint:tagliatelle // OpenAI wire format
type openAIMessage struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

//nolint:tagliatelle // OpenAI wire format
type openAIRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	Tools     []openAITool    `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

//nolint:tagliatelle // OpenAI wire format
type openAIResponse struct {
	Choices []struct {
		Message      openAIMessage `json:"message"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func strPtr(s string) *string { return &s }

func (o *OpenAI) buildRequest(req Request) openAIRequest {
	body := openAIRequest{Model: o.model, MaxTokens: req.MaxTokens}
	if body.MaxTokens == 0 {
		body.MaxTokens = defaultMaxTokens
	}

	if req.System != "" {
		body.Messages = append(body.Messages, openAIMessage{Role: "system", Content: strPtr(req.System)})
	}

	for i := range req.Messages {
		msg := &req.Messages[i]
		wire := openAIMessage{Role: string(msg.Role), ToolCallID: msg.ToolCallID}
		if msg.Content != "" || msg.Role != RoleAssistant {
			wire.Content = strPtr(msg.Content)
		}

		for j := range msg.ToolCalls {
			call := &msg.ToolCalls[j]
			args := string(call.Arguments)
			if args == "" {
				args = "{}"
			}

			wire.ToolCalls = append(wire.ToolCalls, openAIToolCall{
				ID: call.ID, Type: "function",
				Function: openAIToolCallFunction{Name: call.Name, Arguments: args},
			})
		}

		body.Messages = append(body.Messages, wire)
	}

	for i := range req.Tools {
		tool := req.Tools[i]
		body.Tools = append(body.Tools, openAITool{
			Type:     "function",
			Function: openAIFunction(tool),
		})
	}

	return body
}

// Complete implements Provider.
func (o *OpenAI) Complete(ctx context.Context, req Request) (*Response, error) {
	payload, err := json.Marshal(o.buildRequest(req))
	if err != nil {
		return nil, fmt.Errorf("ai: encoding the request: %w", err)
	}

	endpoint := o.baseURL + "/chat/completions"

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("ai: building the request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	if o.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}

	resp, err := o.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %s", ErrCallFailed, providerOpenAI, redact(err.Error(), o.apiKey))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("ai: reading the response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{
			Provider:   providerOpenAI,
			StatusCode: resp.StatusCode,
			Message:    truncate(redact(strings.TrimSpace(string(raw)), o.apiKey), maxErrorBody),
		}
	}

	var decoded openAIResponse
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("ai: decoding the response: %w", err)
	}

	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoChoice, providerOpenAI)
	}

	choice := decoded.Choices[0]
	out := &Response{
		StopReason: choice.FinishReason,
		Usage:      Usage{InputTokens: decoded.Usage.PromptTokens, OutputTokens: decoded.Usage.CompletionTokens},
	}

	if choice.Message.Content != nil {
		out.Text = *choice.Message.Content
	}

	for i := range choice.Message.ToolCalls {
		call := &choice.Message.ToolCalls[i]
		args := json.RawMessage(call.Function.Arguments)
		if !json.Valid(args) {
			args = json.RawMessage("{}")
		}

		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: args})
	}

	return out, nil
}
