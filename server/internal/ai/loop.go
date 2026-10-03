package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// Purpose says why the LLM is called, for logs and usage accounting.
type Purpose string

// Call purposes.
const (
	PurposeContract Purpose = "contract"
	PurposeGenerate Purpose = "generate"
	PurposeRepair   Purpose = "repair"
)

// CallMeta names who a call is for. Logged with every call.
type CallMeta struct {
	OrgUID   string
	CheckUID string
	Purpose  Purpose
}

type callMetaKey struct{}

// WithCallMeta puts the call metadata on the context.
func WithCallMeta(ctx context.Context, meta CallMeta) context.Context {
	return context.WithValue(ctx, callMetaKey{}, meta)
}

// CallMetaFromContext returns the call metadata on the context.
func CallMetaFromContext(ctx context.Context) CallMeta {
	meta, _ := ctx.Value(callMetaKey{}).(CallMeta)

	return meta
}

// ToolFunc runs one tool call and returns the text handed back to the model.
// An error is reported to the model as the tool's result, not to the caller.
type ToolFunc func(ctx context.Context, args json.RawMessage) (string, error)

// Tool is a tool definition and its implementation.
type Tool struct {
	Def ToolDef
	Run ToolFunc
}

// LoopResult is what an agent loop produced.
type LoopResult struct {
	// Text is the model's last text.
	Text string
	// Turns is the number of provider calls made.
	Turns int
	// Usage sums every call.
	Usage Usage
	// Messages is the full history, initial messages included.
	Messages []Message
}

// Loop is SolidPing's own agent loop: call the model, run the tools it asks
// for, append their results, repeat. It stops when the model answers without
// a tool call, when Done reports true after a tool round, or with ErrMaxTurns
// at the cap.
type Loop struct {
	Client *Client
	System string
	Tools  []Tool
	// Done is checked after every tool round. Nil means "only stop when the
	// model does".
	Done func() bool
	// OnUsage is called after every call with its token usage.
	OnUsage func(ctx context.Context, usage Usage)
	// Logger receives one line per call. Nil uses slog.Default().
	Logger *slog.Logger
}

const defaultLoopMaxTurns = 12

// Run runs the loop from the given messages.
func (l *Loop) Run(ctx context.Context, messages []Message) (*LoopResult, error) {
	maxTurns := l.Client.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultLoopMaxTurns
	}

	tools := make(map[string]ToolFunc, len(l.Tools))
	defs := make([]ToolDef, 0, len(l.Tools))

	for i := range l.Tools {
		tools[l.Tools[i].Def.Name] = l.Tools[i].Run
		defs = append(defs, l.Tools[i].Def)
	}

	result := &LoopResult{Messages: append([]Message(nil), messages...)}

	for result.Turns < maxTurns {
		resp, err := l.call(ctx, Request{System: l.System, Messages: result.Messages, Tools: defs})
		result.Turns++

		if err != nil {
			return result, err
		}

		result.Usage = result.Usage.Add(resp.Usage)
		result.Text = resp.Text
		result.Messages = append(result.Messages, Message{
			Role: RoleAssistant, Content: resp.Text, ToolCalls: resp.ToolCalls,
		})

		if len(resp.ToolCalls) == 0 {
			return result, nil
		}

		for i := range resp.ToolCalls {
			call := &resp.ToolCalls[i]
			result.Messages = append(result.Messages, Message{
				Role: RoleTool, ToolCallID: call.ID, ToolName: call.Name,
				Content: runTool(ctx, tools, call),
			})
		}

		if l.Done != nil && l.Done() {
			return result, nil
		}
	}

	return result, fmt.Errorf("%w (%d turns)", ErrMaxTurns, maxTurns)
}

func runTool(ctx context.Context, tools map[string]ToolFunc, call *ToolCall) string {
	run, ok := tools[call.Name]
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", call.Name)
	}

	out, err := run(ctx, call.Arguments)
	if err != nil {
		return "error: " + err.Error()
	}

	return out
}

// Complete makes one logged, metered call outside a loop.
func (l *Loop) Complete(ctx context.Context, req Request) (*Response, error) {
	return l.call(ctx, req)
}

func (l *Loop) call(ctx context.Context, req Request) (*Response, error) {
	callCtx := ctx

	if l.Client.Timeout > 0 {
		var cancel context.CancelFunc

		callCtx, cancel = context.WithTimeout(ctx, l.Client.Timeout)
		defer cancel()
	}

	start := time.Now()
	resp, err := l.Client.Provider.Complete(callCtx, req)

	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}

	meta := CallMetaFromContext(ctx)
	attrs := []any{
		"org_uid", meta.OrgUID, "check_uid", meta.CheckUID, "purpose", string(meta.Purpose),
		"model", l.Client.Model, "duration_ms", time.Since(start).Milliseconds(),
	}

	if err != nil {
		logger.WarnContext(ctx, "AI call failed", append(attrs, "error", err.Error())...)

		return nil, err
	}

	logger.InfoContext(ctx, "AI call",
		append(attrs, "input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens,
			"stop_reason", resp.StopReason, "tool_calls", len(resp.ToolCalls))...)

	if l.OnUsage != nil {
		l.OnUsage(ctx, resp.Usage)
	}

	return resp, nil
}
