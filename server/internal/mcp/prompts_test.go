package mcp

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListPrompts_ReturnsThreePromptsWithExpectedShape(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	prompts := listPrompts()
	r.Len(prompts, 3)

	names := make([]string, len(prompts))
	for i, p := range prompts {
		names[i] = p.Name
	}
	r.Contains(names, promptTriageIncident)
	r.Contains(names, promptSummarizeOrgHealth)
	r.Contains(names, promptDraftStatusUpdate)

	for _, p := range prompts {
		t.Run(p.Name, func(t *testing.T) {
			t.Parallel()
			r.NotEmpty(p.Description)
		})
	}
}

func TestPromptArgRequiredFlags(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, p := range listPrompts() {
		switch p.Name {
		case promptTriageIncident:
			r.Len(p.Arguments, 1)
			r.True(p.Arguments[0].Required)
			r.Equal(promptArgIncidentUID, p.Arguments[0].Name)
		case promptSummarizeOrgHealth:
			r.Empty(p.Arguments)
		case promptDraftStatusUpdate:
			r.Len(p.Arguments, 2)
			var sawUID, sawTone bool
			for _, a := range p.Arguments {
				switch a.Name {
				case promptArgIncidentUID:
					sawUID = true
					r.True(a.Required)
				case promptArgTone:
					sawTone = true
					r.False(a.Required)
				}
			}
			r.True(sawUID)
			r.True(sawTone)
		}
	}
}

func TestRenderPrompt_TriageIncident_Happy(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	desc, body, err := renderPrompt(promptTriageIncident, map[string]string{
		promptArgIncidentUID: "abc-123",
	})
	r.NoError(err)
	r.Equal("Triage incident abc-123", desc)
	r.Contains(body, "abc-123")
	r.Contains(body, "Steps:")
	r.Contains(body, "diagnose_check")
}

func TestRenderPrompt_TriageIncident_MissingArg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, _, err := renderPrompt(promptTriageIncident, map[string]string{})
	r.ErrorIs(err, errPromptArgMissing)
}

func TestRenderPrompt_UnknownName(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, _, err := renderPrompt("nonexistent_prompt", nil)
	r.ErrorIs(err, errUnknownPrompt)
}

func TestRenderPrompt_DraftStatusUpdate_DefaultsTone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, body, err := renderPrompt(promptDraftStatusUpdate, map[string]string{
		promptArgIncidentUID: "abc",
	})
	r.NoError(err)
	r.Contains(body, "Tone: non-technical")
	r.Contains(body, "Avoid jargon")
}

func TestRenderPrompt_DraftStatusUpdate_TechnicalTone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, body, err := renderPrompt(promptDraftStatusUpdate, map[string]string{
		promptArgIncidentUID: "abc",
		promptArgTone:        toneTechnical,
	})
	r.NoError(err)
	r.Contains(body, "Tone: technical")
	r.Contains(body, "Technical detail is welcome")
}

func TestRenderPrompt_SummarizeOrgHealth_NoArgsNeeded(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	desc, body, err := renderPrompt(promptSummarizeOrgHealth, nil)
	r.NoError(err)
	r.Equal("Organization health summary", desc)
	r.Contains(body, "list_incidents")
	r.Contains(body, "list_checks")
}

// --- Handler-level tests ---

func TestDispatch_PromptsList(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","id":1,"method":"prompts/list"}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))
	r.Equal(http.StatusOK, rec.Code)

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)

	result, ok := resp.Result.(map[string]any)
	r.True(ok)
	prompts, ok := result["prompts"].([]any)
	r.True(ok)
	r.Len(prompts, 3)
}

func TestDispatch_PromptsGet_Triage(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	params := PromptGetParams{
		Name:      promptTriageIncident,
		Arguments: map[string]string{promptArgIncidentUID: "xyz"},
	}
	paramsJSON, err := json.Marshal(params)
	r.NoError(err)

	rpcReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "prompts/get",
		"params":  json.RawMessage(paramsJSON),
	}
	bodyBytes, err := json.Marshal(rpcReq)
	r.NoError(err)

	rec, req := makeRequest(t, http.MethodPost, string(bodyBytes), defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)
	result, ok := resp.Result.(map[string]any)
	r.True(ok)
	messages, ok := result["messages"].([]any)
	r.True(ok)
	r.Len(messages, 1)
	first, ok := messages[0].(map[string]any)
	r.True(ok)
	r.Equal("user", first["role"])
	content, ok := first["content"].(map[string]any)
	r.True(ok)
	text, ok := content["text"].(string)
	r.True(ok)
	r.Contains(text, "xyz")
}

func TestDispatch_PromptsGet_MissingArg(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","id":3,"method":"prompts/get","params":{"name":"triage_incident","arguments":{}}}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.NotNil(resp.Error)
	r.Equal(CodeInvalidParams, resp.Error.Code)
}

func TestDispatch_PromptsGet_UnknownName(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	body := `{"jsonrpc":"2.0","id":4,"method":"prompts/get","params":{"name":"nope"}}`
	rec, req := makeRequest(t, http.MethodPost, body, defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.NotNil(resp.Error)
	r.Equal(CodeMethodNotFound, resp.Error.Code)
	r.Contains(resp.Error.Message, "nope")
}

func TestDispatch_Initialize_AdvertisesPrompts(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := newTestHandler()
	params := InitializeParams{
		ProtocolVersion: "2025-03-26",
		ClientInfo:      ClientInfo{Name: "test", Version: "1"},
	}
	paramsJSON, err := json.Marshal(params)
	r.NoError(err)
	rpcReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  json.RawMessage(paramsJSON),
	}
	bodyBytes, err := json.Marshal(rpcReq)
	r.NoError(err)

	rec, req := makeRequest(t, http.MethodPost, string(bodyBytes), defaultClaims())
	r.NoError(handler.Handle(rec, req))

	resp := decodeResponse(t, rec)
	r.Nil(resp.Error)
	result, ok := resp.Result.(map[string]any)
	r.True(ok)
	caps, ok := result["capabilities"].(map[string]any)
	r.True(ok)
	r.Contains(caps, "prompts")
	r.Contains(caps, "tools")
	r.Contains(caps, "resources")
}
