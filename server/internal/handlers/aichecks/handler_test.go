package aichecks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/ai"
	svc "github.com/fclairamb/solidping/server/internal/aichecks"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/aichecks"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

type textProvider struct{ text string }

func (p *textProvider) Complete(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{Text: p.text}, nil
}

type downProvider struct{}

func (downProvider) Complete(context.Context, ai.Request) (*ai.Response, error) {
	args, err := json.Marshal(map[string]string{"script": `return { status: "down" };`})
	if err != nil {
		return nil, err
	}

	return &ai.Response{ToolCalls: []ai.ToolCall{{ID: "1", Name: svc.ToolRunScript, Arguments: args}}}, nil
}

type upProvider struct{}

func (upProvider) Complete(context.Context, ai.Request) (*ai.Response, error) {
	args, err := json.Marshal(map[string]any{"script": `return { status: "up" };`, "final": true})
	if err != nil {
		return nil, err
	}

	return &ai.Response{
		Text:      "Testing the finished script.",
		ToolCalls: []ai.ToolCall{{ID: "1", Name: svc.ToolRunScript, Arguments: args}},
	}, nil
}

func newRouter(t *testing.T, client *ai.Client) (*httpx.Router, *models.Organization) {
	t.Helper()

	ctx := t.Context()
	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	service := svc.NewService(&svc.Options{Client: client, DB: dbSvc})
	handler := aichecks.NewHandler(service, dbSvc, &config.Config{})

	router := httpx.New()
	group := router.NewGroup("/api/v1/orgs/:org/checks")
	group.POST("/ai/contract", handler.Contract)
	group.POST("/ai/generate", handler.Generate)

	return router, org
}

func post(t *testing.T, router *httpx.Router, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestFeatureOffAnswers404: with SP_AI_PROVIDER empty the endpoints are 404.
func TestFeatureOffAnswers404(t *testing.T) {
	t.Parallel()

	router, org := newRouter(t, nil)
	base := "/api/v1/orgs/" + org.Slug + "/checks/ai/"

	rec := post(t, router, base+"generate", `{"prompt":"p","contract":["a"]}`)
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = post(t, router, base+"contract", `{"prompt":"p"}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestContractEndpoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router, org := newRouter(t, &ai.Client{Provider: &textProvider{text: `["home answers 200"]`}, Model: "m"})
	base := "/api/v1/orgs/" + org.Slug + "/checks/ai/"

	rec := post(t, router, base+"contract", `{"prompt":"watch https://acme.com"}`)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())

	var resp svc.ContractResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal([]string{"home answers 200"}, resp.Contract)

	rec = post(t, router, base+"contract", `{"prompt":""}`)
	r.Equal(http.StatusBadRequest, rec.Code)

	rec = post(t, router, "/api/v1/orgs/nope/checks/ai/contract", `{"prompt":"p"}`)
	r.Equal(http.StatusNotFound, rec.Code)
}

func TestGenerateEndpointTurnCapIs422(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router, org := newRouter(t, &ai.Client{Provider: downProvider{}, Model: "m", MaxTurns: 2})

	rec := post(t, router, "/api/v1/orgs/"+org.Slug+"/checks/ai/generate",
		`{"prompt":"watch acme","contract":["acme answers"]}`)
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	var resp aichecks.GenerationFailedResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("AI_GENERATION_FAILED", resp.Code)
	r.Equal("The AI used its 2 turns without a passing script. The last test run returned down.", resp.Detail)
	r.Equal(2, resp.Turns)
	r.Contains(resp.LastScript, "down")
	r.Equal("down", resp.LastRun.Status)

	rec = post(t, router, "/api/v1/orgs/"+org.Slug+"/checks/ai/generate", `{"prompt":"watch acme"}`)
	r.Equal(http.StatusBadRequest, rec.Code)
}

func postStream(t *testing.T, router *httpx.Router, path, body string) []map[string]any {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Accept", "application/x-ndjson, application/json")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "application/x-ndjson", rec.Header().Get("Content-Type"))

	raws := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	lines := make([]map[string]any, 0, len(raws))

	for _, raw := range raws {
		var line map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &line), raw)

		lines = append(lines, line)
	}

	return lines
}

func lineTypes(lines []map[string]any) []string {
	types := make([]string, 0, len(lines))
	for _, line := range lines {
		lineType, _ := line["type"].(string)
		types = append(types, lineType)
	}

	return types
}

// TestGenerateEndpointStreamsProgress: with Accept: application/x-ndjson each
// step is a line, and the last line is the response.
func TestGenerateEndpointStreamsProgress(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router, org := newRouter(t, &ai.Client{Provider: upProvider{}, Model: "m"})

	lines := postStream(t, router, "/api/v1/orgs/"+org.Slug+"/checks/ai/generate",
		`{"prompt":"watch acme","contract":["acme answers"]}`)
	r.Equal([]string{"turn", "message", "tool", "toolResult", "result"}, lineTypes(lines))
	r.Equal("Testing the finished script.", lines[1]["text"])
	r.Equal(true, lines[3]["final"])
	r.Equal("up", lines[3]["status"])

	result, ok := lines[4]["result"].(map[string]any)
	r.True(ok)
	r.Equal(`return { status: "up" };`, result["script"])
}

// TestGenerateEndpointStreamsTheFailure: an error is the last line, with the
// status and body the plain endpoint answers.
func TestGenerateEndpointStreamsTheFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	router, org := newRouter(t, &ai.Client{Provider: downProvider{}, Model: "m", MaxTurns: 2})
	path := "/api/v1/orgs/" + org.Slug + "/checks/ai/generate"

	lines := postStream(t, router, path, `{"prompt":"watch acme","contract":["acme answers"]}`)
	last := lines[len(lines)-1]
	r.Equal("error", last["type"])
	r.InDelta(http.StatusUnprocessableEntity, last["httpStatus"], 0)

	body, ok := last["response"].(map[string]any)
	r.True(ok)
	r.Equal("AI_GENERATION_FAILED", body["code"])
	r.Contains(body["lastScript"], "down")

	lines = postStream(t, router, path, `{"prompt":"watch acme"}`)
	r.Len(lines, 1)
	r.InDelta(http.StatusBadRequest, lines[0]["httpStatus"], 0)
}
