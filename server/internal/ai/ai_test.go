package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/ai"
	"github.com/fclairamb/solidping/server/internal/config"
)

const testKey = "sk-secret-key-123456"

// fakeServer answers every request with the given status and body and
// records the last request body and headers.
type fakeServer struct {
	status  int
	body    string
	lastReq map[string]any
	lastHdr http.Header
}

func (f *fakeServer) start(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.lastReq = map[string]any{}
		_ = json.Unmarshal(raw, &f.lastReq)
		f.lastHdr = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func newClient(t *testing.T, provider, baseURL string) *ai.Client {
	t.Helper()

	client, err := ai.New(&config.AIConfig{
		Provider: provider, BaseURL: baseURL, APIKey: testKey, Model: "test-model", MaxTurns: 4,
	}, nil)
	require.NoError(t, err)

	return client
}

// loggedLoop returns a loop whose logs land in buf.
func loggedLoop(client *ai.Client, buf *bytes.Buffer) *ai.Loop {
	handler := slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})

	return &ai.Loop{Client: client, Logger: slog.New(handler)}
}

func TestNewDisabled(t *testing.T) {
	t.Parallel()

	_, err := ai.New(&config.AIConfig{}, nil)
	require.ErrorIs(t, err, ai.ErrDisabled)

	_, err = ai.New(&config.AIConfig{Provider: "gemini", Model: "m"}, nil)
	require.ErrorIs(t, err, ai.ErrUnknownProvider)
}

func TestOpenAITextReply(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := &fakeServer{status: 200, body: `{"choices":[{"message":{"role":"assistant","content":"hello",
		"reasoning_content":"thinking..."},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3}}`}
	srv := fake.start(t)

	var logs bytes.Buffer

	resp, err := loggedLoop(newClient(t, config.AIProviderOpenAI, srv.URL), &logs).Complete(
		ai.WithCallMeta(context.Background(), ai.CallMeta{OrgUID: "org-1", CheckUID: "chk-1", Purpose: ai.PurposeGenerate}),
		ai.Request{System: "sys", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}}},
	)
	r.NoError(err)
	r.Equal("hello", resp.Text)
	r.Equal("stop", resp.StopReason)
	r.Equal(ai.Usage{InputTokens: 11, OutputTokens: 3}, resp.Usage)
	r.Equal("Bearer "+testKey, fake.lastHdr.Get("Authorization"))
	r.Equal("test-model", fake.lastReq["model"])

	msgs, _ := fake.lastReq["messages"].([]any)
	r.Len(msgs, 2)
	first, ok := msgs[0].(map[string]any)
	r.True(ok)
	r.Equal("system", first["role"])

	r.Contains(logs.String(), "org_uid=org-1")
	r.Contains(logs.String(), "check_uid=chk-1")
	r.Contains(logs.String(), "purpose=generate")
	r.Contains(logs.String(), "input_tokens=11")
	r.NotContains(logs.String(), testKey)
}

func TestOpenAIToolCallsReply(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := &fakeServer{status: 200, body: `{"choices":[{"message":{"role":"assistant","content":null,
		"tool_calls":[{"id":"call_1","type":"function","function":{"name":"run_script",
		"arguments":"{\"script\":\"return {status:'up'}\"}"}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":5,"completion_tokens":7}}`}
	srv := fake.start(t)

	client := newClient(t, config.AIProviderOpenAI, srv.URL)
	resp, err := client.Provider.Complete(context.Background(), ai.Request{
		Messages: []ai.Message{
			{Role: ai.RoleUser, Content: "go"},
			{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{{ID: "c0", Name: "fetch_page", Arguments: json.RawMessage(`{}`)}}},
			{Role: ai.RoleTool, ToolCallID: "c0", Content: "status 200"},
		},
		Tools: []ai.ToolDef{{Name: "run_script", Description: "run", Parameters: map[string]any{"type": "object"}}},
	})
	r.NoError(err)
	r.Empty(resp.Text)
	r.Len(resp.ToolCalls, 1)
	r.Equal("call_1", resp.ToolCalls[0].ID)
	r.Equal("run_script", resp.ToolCalls[0].Name)
	r.JSONEq(`{"script":"return {status:'up'}"}`, string(resp.ToolCalls[0].Arguments))

	tools, _ := fake.lastReq["tools"].([]any)
	r.Len(tools, 1)

	msgs, _ := fake.lastReq["messages"].([]any)
	r.Len(msgs, 3)
	toolMsg, ok := msgs[2].(map[string]any)
	r.True(ok)
	r.Equal("c0", toolMsg["tool_call_id"])
}

func TestOpenAIErrorMappedAndKeyRedacted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := &fakeServer{status: 401, body: `{"error":{"message":"Incorrect API key provided: ` + testKey + `"}}`}
	srv := fake.start(t)

	var logs bytes.Buffer

	_, err := loggedLoop(newClient(t, config.AIProviderOpenAI, srv.URL), &logs).Complete(
		context.Background(), ai.Request{Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}}})
	r.Error(err)

	var apiErr *ai.APIError
	r.ErrorAs(err, &apiErr)
	r.Equal(401, apiErr.StatusCode)
	r.NotContains(err.Error(), testKey)
	r.Contains(err.Error(), "[redacted]")
	r.NotContains(logs.String(), testKey)
}

func TestAnthropicTextReply(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := &fakeServer{status: 200, body: `{"id":"msg_1","type":"message","role":"assistant","model":"test-model",
		"content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn",
		"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":5}}`}
	srv := fake.start(t)

	var logs bytes.Buffer

	resp, err := loggedLoop(newClient(t, config.AIProviderAnthropic, srv.URL), &logs).Complete(
		context.Background(), ai.Request{System: "the js api", Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}}})
	r.NoError(err)
	r.Equal("hello", resp.Text)
	r.Equal("end_turn", resp.StopReason)
	r.Equal(ai.Usage{InputTokens: 15, OutputTokens: 2}, resp.Usage)
	r.Equal(testKey, fake.lastHdr.Get("X-Api-Key"))

	// The system prompt carries a cache breakpoint.
	system, _ := fake.lastReq["system"].([]any)
	r.Len(system, 1)
	block, ok := system[0].(map[string]any)
	r.True(ok)
	r.Contains(block, "cache_control")
	r.NotContains(logs.String(), testKey)
}

func TestAnthropicToolUseReply(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := &fakeServer{status: 200, body: `{"id":"msg_1","type":"message","role":"assistant","model":"test-model",
		"content":[{"type":"text","text":"let me try"},{"type":"tool_use","id":"tu_1","name":"run_script",
		"input":{"script":"x"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`}
	srv := fake.start(t)

	client := newClient(t, config.AIProviderAnthropic, srv.URL)
	resp, err := client.Provider.Complete(context.Background(), ai.Request{
		Messages: []ai.Message{
			{Role: ai.RoleUser, Content: "go"},
			{Role: ai.RoleAssistant, ToolCalls: []ai.ToolCall{
				{ID: "a", Name: "fetch_page", Arguments: json.RawMessage(`{"url":"https://acme.com"}`)},
				{ID: "b", Name: "fetch_page", Arguments: json.RawMessage(`{"url":"https://acme.com/x"}`)},
			}},
			{Role: ai.RoleTool, ToolCallID: "a", Content: "ok"},
			{Role: ai.RoleTool, ToolCallID: "b", Content: "ok"},
		},
		Tools: []ai.ToolDef{{Name: "run_script", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"script": map[string]any{"type": "string"}},
			"required": []string{"script"},
		}}},
	})
	r.NoError(err)
	r.Equal("let me try", resp.Text)
	r.Len(resp.ToolCalls, 1)
	r.Equal("tu_1", resp.ToolCalls[0].ID)
	r.JSONEq(`{"script":"x"}`, string(resp.ToolCalls[0].Arguments))

	// Both tool results fold into one user message.
	msgs, _ := fake.lastReq["messages"].([]any)
	r.Len(msgs, 3)
	last, ok := msgs[2].(map[string]any)
	r.True(ok)
	r.Equal("user", last["role"])
	r.Len(last["content"], 2)
}

func TestAnthropicErrorMappedAndKeyRedacted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := &fakeServer{status: 400, body: `{"type":"error","error":{"type":"invalid_request_error",
		"message":"bad key ` + testKey + `"}}`}
	srv := fake.start(t)

	var logs bytes.Buffer

	_, err := loggedLoop(newClient(t, config.AIProviderAnthropic, srv.URL), &logs).Complete(
		context.Background(), ai.Request{Messages: []ai.Message{{Role: ai.RoleUser, Content: "hi"}}})
	r.Error(err)

	var apiErr *ai.APIError
	r.ErrorAs(err, &apiErr)
	r.Equal(400, apiErr.StatusCode)
	r.NotContains(err.Error(), testKey)
	r.NotContains(logs.String(), testKey)
}

// deepSeekServer mimics DeepSeek's thinking mode with tools: every reply
// carries reasoning_content when thinking is true, and a request whose
// assistant turn lacks it is rejected with HTTP 400, as the real API does.
type deepSeekServer struct {
	thinking bool
	requests []map[string]any
}

func (d *deepSeekServer) start(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := map[string]any{}
		_ = json.Unmarshal(raw, &req)
		d.requests = append(d.requests, req)

		w.Header().Set("Content-Type", "application/json")

		msgs, _ := req["messages"].([]any)
		for i, m := range msgs {
			msg, _ := m.(map[string]any)
			if msg["role"] == "assistant" && d.thinking && msg["reasoning_content"] == nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"error":{"message":"missing reasoning_content at messages[%d]"}}`, i)

				return
			}
		}

		reasoning := ""
		if d.thinking {
			reasoning = fmt.Sprintf(`"reasoning_content":"thought %d",`, len(d.requests))
		}

		if len(d.requests) == 1 {
			_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":null,%s
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"run_script","arguments":"{}"}}]},
				"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, reasoning)

			return
		}

		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant",%s"content":"done"},
			"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`, reasoning)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestOpenAIReasoningContentRoundTrip(t *testing.T) {
	t.Parallel()

	for _, thinking := range []bool{true, false} {
		t.Run(fmt.Sprintf("thinking=%v", thinking), func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			fake := &deepSeekServer{thinking: thinking}
			srv := fake.start(t)

			loop := &ai.Loop{
				Client: newClient(t, config.AIProviderOpenAI, srv.URL),
				Tools: []ai.Tool{{Def: ai.ToolDef{Name: "run_script"}, Run: func(context.Context, json.RawMessage) (string, error) {
					return "ok", nil
				}}},
			}

			res, err := loop.Run(context.Background(), []ai.Message{{Role: ai.RoleUser, Content: "write it"}})
			r.NoError(err)
			r.Equal("done", res.Text)
			r.Len(fake.requests, 2)

			msgs, _ := fake.requests[1]["messages"].([]any)
			r.Len(msgs, 3)
			assistant, ok := msgs[1].(map[string]any)
			r.True(ok)
			r.Equal("assistant", assistant["role"])

			if thinking {
				r.Equal("thought 1", assistant["reasoning_content"])
			} else {
				// OpenAI rejects the field: it is never sent unless the model produced it.
				r.NotContains(assistant, "reasoning_content")
			}

			userMsg, ok := msgs[0].(map[string]any)
			r.True(ok)
			r.NotContains(userMsg, "reasoning_content")
		})
	}
}
