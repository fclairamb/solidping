package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const providerAnthropic = "anthropic"

// Anthropic is the native Messages API driver, on the official Go SDK. The
// system prompt (the js API reference, a large stable prefix) carries a cache
// breakpoint so repeated turns of the same loop read it from the prompt
// cache.
type Anthropic struct {
	client anthropic.Client
	apiKey string
	model  string
}

// NewAnthropic builds the driver. An empty baseURL uses Anthropic's.
func NewAnthropic(baseURL, apiKey, model string, httpClient *http.Client) *Anthropic {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(httpClient),
		// The agent loop owns retries (a failed call fails the attempt).
		option.WithMaxRetries(0),
	}

	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(strings.TrimRight(baseURL, "/")+"/"))
	}

	return &Anthropic{client: anthropic.NewClient(opts...), apiKey: apiKey, model: model}
}

func (a *Anthropic) buildParams(req Request) anthropic.MessageNewParams {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}

	params := anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: int64(maxTokens),
		Messages:  anthropicMessages(req.Messages),
	}

	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{
			Text:         req.System,
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}}
	}

	for i := range req.Tools {
		tool := &req.Tools[i]
		schema := anthropic.ToolInputSchemaParam{}
		if props, ok := tool.Parameters["properties"]; ok {
			schema.Properties = props
		}

		if required, ok := tool.Parameters["required"].([]string); ok {
			schema.Required = required
		}

		param := anthropic.ToolParam{Name: tool.Name, InputSchema: schema}
		if tool.Description != "" {
			param.Description = anthropic.String(tool.Description)
		}

		params.Tools = append(params.Tools, anthropic.ToolUnionParam{OfTool: &param})
	}

	return params
}

// anthropicMessages maps the chat history. Consecutive tool results fold into
// one user message, which is what the Messages API expects after an assistant
// turn with several tool_use blocks.
func anthropicMessages(messages []Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(messages))

	var pendingResults []anthropic.ContentBlockParamUnion

	flush := func() {
		if len(pendingResults) > 0 {
			out = append(out, anthropic.NewUserMessage(pendingResults...))
			pendingResults = nil
		}
	}

	for i := range messages {
		msg := &messages[i]

		switch msg.Role {
		case RoleTool:
			pendingResults = append(pendingResults, anthropic.NewToolResultBlock(msg.ToolCallID, msg.Content, false))
		case RoleAssistant:
			flush()

			blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.ToolCalls)+1)
			if msg.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
			}

			for j := range msg.ToolCalls {
				call := &msg.ToolCalls[j]

				var input any = map[string]any{}
				if len(call.Arguments) > 0 {
					input = call.Arguments
				}

				blocks = append(blocks, anthropic.NewToolUseBlock(call.ID, input, call.Name))
			}

			out = append(out, anthropic.NewAssistantMessage(blocks...))
		case RoleUser:
			flush()

			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)))
		}
	}

	flush()

	return out
}

// Complete implements Provider.
func (a *Anthropic) Complete(ctx context.Context, req Request) (*Response, error) {
	msg, err := a.client.Messages.New(ctx, a.buildParams(req))
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return nil, &APIError{
				Provider:   providerAnthropic,
				StatusCode: apiErr.StatusCode,
				Message:    truncate(redact(strings.TrimSpace(apiErr.RawJSON()), a.apiKey), maxErrorBody),
			}
		}

		return nil, fmt.Errorf("%w: %s: %s", ErrCallFailed, providerAnthropic, redact(err.Error(), a.apiKey))
	}

	out := &Response{
		StopReason: string(msg.StopReason),
		Usage: Usage{
			InputTokens: int(msg.Usage.InputTokens + msg.Usage.CacheReadInputTokens +
				msg.Usage.CacheCreationInputTokens),
			OutputTokens: int(msg.Usage.OutputTokens),
		},
	}

	var text strings.Builder

	for i := range msg.Content {
		block := &msg.Content[i]

		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			args := block.Input
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}

			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: block.ID, Name: block.Name, Arguments: args})
		}
	}

	out.Text = text.String()

	return out, nil
}
