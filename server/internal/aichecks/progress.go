package aichecks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fclairamb/solidping/server/internal/ai"
)

// Progress event types, streamed to the dashboard while a script is written.
const (
	// ProgressTurn is a call to the model starting.
	ProgressTurn = "turn"
	// ProgressMessage is text the model wrote.
	ProgressMessage = "message"
	// ProgressTool is a tool call starting.
	ProgressTool = "tool"
	// ProgressToolResult is a tool call's outcome.
	ProgressToolResult = "toolResult"
)

const (
	progressTextLimit   = 600
	progressDetailLimit = 300
)

// Progress is one step of a generation. Nothing in it is secret: run results
// are scrubbed before they get here.
type Progress struct {
	Type       string `json:"type"`
	Turn       int    `json:"turn,omitempty"`
	MaxTurns   int    `json:"maxTurns,omitempty"`
	Text       string `json:"text,omitempty"`
	Tool       string `json:"tool,omitempty"`
	URL        string `json:"url,omitempty"`
	Final      bool   `json:"final,omitempty"`
	Status     string `json:"status,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"durationMs,omitempty"`
}

// runSummary is one line on a run: the failure fields a script reports, or
// a bounded dump of its output.
func runSummary(res *RunResult) string {
	if res == nil {
		return ""
	}

	parts := make([]string, 0, 4)

	for _, key := range []string{"step", "failure", outputKeyError, "message", "statusCode", "title"} {
		if value, ok := res.Output[key]; ok && value != nil {
			parts = append(parts, fmt.Sprintf("%s: %v", key, value))
		}
	}

	if len(parts) > 0 {
		return clip(strings.Join(parts, ", "), progressDetailLimit)
	}

	if res.Up() || len(res.Output) == 0 {
		return ""
	}

	raw, err := json.Marshal(res.Output)
	if err != nil {
		return ""
	}

	return clip(string(raw), progressDetailLimit)
}

// clip bounds a string to limit bytes, on a rune boundary.
func clip(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}

	return text[:cut] + "…"
}

// observeLoop sends a stopped model back to work through rec, and reports
// every model turn and message.
func observeLoop(loop *ai.Loop, rec *runRecorder, progress func(Progress)) {
	loop.Nudge = rec.nudge
	loop.OnTurn = func(_ context.Context, turn, maxTurns int) {
		progress(Progress{Type: ProgressTurn, Turn: turn, MaxTurns: maxTurns})
	}
	loop.OnResponse = func(_ context.Context, resp *ai.Response) {
		if text := withoutFences(resp.Text); text != "" {
			progress(Progress{Type: ProgressMessage, Text: clip(text, progressTextLimit)})
		}
	}
}

// observeTool reports a fetch_page or browser_snapshot call and its outcome.
func observeTool(tool ai.Tool, emit func(Progress)) ai.Tool {
	run := tool.Run
	name := tool.Def.Name
	tool.Run = func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args urlArgs
		_ = json.Unmarshal(raw, &args)

		emit(Progress{Type: ProgressTool, Tool: name, URL: args.URL})

		out, err := run(ctx, raw)
		if err != nil {
			emit(Progress{Type: ProgressToolResult, Tool: name, URL: args.URL, Error: err.Error()})

			return out, err
		}

		var res RunResult
		_ = json.Unmarshal([]byte(out), &res)
		emit(Progress{
			Type: ProgressToolResult, Tool: name, URL: args.URL,
			Status: res.Status, Detail: runSummary(&res), DurationMs: res.DurationMs,
		})

		return out, nil
	}

	return tool
}
