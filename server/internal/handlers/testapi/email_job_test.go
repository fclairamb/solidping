package testapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// capturingJobs records the configs CreateJob is given.
type capturingJobs struct {
	jobsvc.Service
	configs []json.RawMessage
}

func (c *capturingJobs) CreateJob(
	_ context.Context, _, _ string, config json.RawMessage, _ *jobsvc.JobOptions,
) (*models.Job, error) {
	c.configs = append(c.configs, config)

	return &models.Job{UID: "job-1"}, nil
}

func postEmailJob(t *testing.T, handler *Handler, body map[string]any) jobtypes.EmailJobConfig {
	t.Helper()

	raw, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/test/jobs", bytes.NewReader(raw))
	w := httptest.NewRecorder()

	router := httpx.New()
	router.POST("/api/v1/test/jobs", handler.CreateEmailJob)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	jobs, _ := handler.jobSvc.(*capturingJobs)

	var cfg jobtypes.EmailJobConfig
	require.NoError(t, json.Unmarshal(jobs.configs[len(jobs.configs)-1], &cfg))

	return cfg
}

// TestCreateEmailJob_ResolvesTheRecipientsLocale: a test send follows the
// same chain as a real one: the account behind the address, then the
// request's locale and timezone as the hint, then English/UTC.
func TestCreateEmailJob_ResolvesTheRecipientsLocale(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	handler := setupBulkTestHandler(t)
	handler.jobSvc = &capturingJobs{}

	lea := models.NewUser("lea@acme.com")
	lea.Preferences = &models.UserPreferences{Locale: "fr", Timezone: "Europe/Paris"}
	r.NoError(handler.dbService.CreateUser(t.Context(), lea))

	cfg := postEmailJob(t, handler, map[string]any{
		"to": "lea@acme.com", "type": "incident", "locale": "de",
		"params": map[string]any{"CheckName": "API"},
	})
	r.Equal("fr", cfg.Locale, "the account wins over the request")
	r.Equal("Europe/Paris", cfg.Timezone)
	r.Equal("[SolidPing] Alerte : API", cfg.Subject)
	r.Equal("incident-created.html", cfg.Template)

	cfg = postEmailJob(t, handler, map[string]any{
		"to": "new@acme.com", "type": "welcome", "locale": "es-ES", "timezone": "Europe/Madrid",
	})
	r.Equal("es", cfg.Locale)
	r.Equal("Europe/Madrid", cfg.Timezone)
	r.Equal("[SolidPing] ¡Bienvenido!", cfg.Subject)

	cfg = postEmailJob(t, handler, map[string]any{"to": "new@acme.com", "type": "incident"})
	r.Equal("en", cfg.Locale)
	r.Equal("UTC", cfg.Timezone)
	r.Equal("[SolidPing] Alert", cfg.Subject, "English keeps its old subjects")
}

// TestSubjectForType_EnglishIsUnchanged pins the English subjects of the test
// sends to what they were before localization.
func TestSubjectForType_EnglishIsUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal("[SolidPing] Welcome!", subjectForType(i18n.English, "welcome", nil))
	r.Equal("[SolidPing] Alert: API", subjectForType(i18n.English, "incident", map[string]any{"CheckName": "API"}))
	r.Equal("[SolidPing] Alert", subjectForType(i18n.English, "incident", nil))
}
