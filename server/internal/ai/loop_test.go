package ai_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/ai"
)

// scriptedProvider replays a fixed list of responses.
type scriptedProvider struct {
	mu        sync.Mutex
	responses []*ai.Response
	calls     int
}

func (p *scriptedProvider) Complete(_ context.Context, _ ai.Request) (*ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	resp := p.responses[p.calls%len(p.responses)]
	p.calls++

	return resp, nil
}

func runScriptCall(id string) *ai.Response {
	return &ai.Response{
		ToolCalls: []ai.ToolCall{{ID: id, Name: "run_script", Arguments: json.RawMessage(`{"script":"x"}`)}},
		Usage:     ai.Usage{InputTokens: 10, OutputTokens: 5},
	}
}

func TestLoopRunsToolsThenStops(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	provider := &scriptedProvider{responses: []*ai.Response{
		runScriptCall("1"), runScriptCall("2"), {Text: "done", Usage: ai.Usage{InputTokens: 1, OutputTokens: 1}},
	}}

	runs := 0
	usageCalls := 0
	loop := &ai.Loop{
		Client: &ai.Client{Provider: provider, MaxTurns: 12},
		Tools: []ai.Tool{{Def: ai.ToolDef{Name: "run_script"}, Run: func(context.Context, json.RawMessage) (string, error) {
			runs++

			return `{"status":"down"}`, nil
		}}},
		OnUsage: func(context.Context, ai.Usage) { usageCalls++ },
	}

	res, err := loop.Run(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "write it"}})
	r.NoError(err)
	r.Equal(2, runs)
	r.Equal(3, res.Turns)
	r.Equal(3, usageCalls)
	r.Equal("done", res.Text)
	r.Equal(ai.Usage{InputTokens: 21, OutputTokens: 11}, res.Usage)
	// user, (assistant, tool) x2, assistant
	r.Len(res.Messages, 6)
}

func TestLoopStopsWhenDone(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	provider := &scriptedProvider{responses: []*ai.Response{runScriptCall("1"), runScriptCall("2")}}

	runs := 0
	loop := &ai.Loop{
		Client: &ai.Client{Provider: provider, MaxTurns: 12},
		Tools: []ai.Tool{{Def: ai.ToolDef{Name: "run_script"}, Run: func(context.Context, json.RawMessage) (string, error) {
			runs++

			return "up", nil
		}}},
		Done: func() bool { return runs >= 2 },
	}

	res, err := loop.Run(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "write it"}})
	r.NoError(err)
	r.Equal(2, res.Turns)
}

func TestLoopTurnCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	provider := &scriptedProvider{responses: []*ai.Response{runScriptCall("1")}}
	loop := &ai.Loop{
		Client: &ai.Client{Provider: provider, MaxTurns: 3},
		Tools: []ai.Tool{{Def: ai.ToolDef{Name: "run_script"}, Run: func(context.Context, json.RawMessage) (string, error) {
			return "down", nil
		}}},
	}

	res, err := loop.Run(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "write it"}})
	r.ErrorIs(err, ai.ErrMaxTurns)
	r.Equal(3, res.Turns)
	r.Equal(3, provider.calls)
}

func TestLoopUnknownToolReportedToModel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	provider := &scriptedProvider{responses: []*ai.Response{
		{ToolCalls: []ai.ToolCall{{ID: "1", Name: "rm_rf"}}}, {Text: "ok"},
	}}
	loop := &ai.Loop{Client: &ai.Client{Provider: provider, MaxTurns: 5}}

	res, err := loop.Run(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "x"}})
	r.NoError(err)
	r.Contains(res.Messages[2].Content, "unknown tool")
}

func TestLoopNudgeSendsTheModelBackToWork(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	provider := &scriptedProvider{responses: []*ai.Response{
		{Text: "I am done."}, runScriptCall("1"), {Text: "Really done."},
	}}

	nudges := 0
	turns := []int{}
	loop := &ai.Loop{
		Client: &ai.Client{Provider: provider, MaxTurns: 12},
		Tools: []ai.Tool{{Def: ai.ToolDef{Name: "run_script"}, Run: func(context.Context, json.RawMessage) (string, error) {
			return `{"status":"up"}`, nil
		}}},
		Nudge: func(_ context.Context, text string) string {
			nudges++
			if text == "I am done." {
				return "Test it first."
			}

			return ""
		},
		OnTurn: func(_ context.Context, turn, maxTurns int) {
			r.Equal(12, maxTurns)

			turns = append(turns, turn)
		},
	}

	res, err := loop.Run(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "write it"}})
	r.NoError(err)
	r.Equal(3, res.Turns)
	r.Equal(2, nudges)
	r.Equal([]int{1, 2, 3}, turns)
	r.Equal("Really done.", res.Text)
	r.Equal(ai.RoleUser, res.Messages[2].Role)
	r.Equal("Test it first.", res.Messages[2].Content)
}
