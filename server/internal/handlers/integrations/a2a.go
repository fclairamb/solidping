package integrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// A2ACardRequest asks the server to resolve an AI agent's Agent Card before
// the integration is saved (spec 2026-10-18-02, the dashboard's Test button).
type A2ACardRequest struct {
	// Settings are the form's current values. An empty secret means "keep
	// the stored one" when IntegrationUID is set, as on PATCH.
	Settings map[string]any `json:"settings"`
	// IntegrationUID names a saved A2A integration whose stored secrets fill
	// in the ones the form does not echo back. Optional.
	IntegrationUID string `json:"integrationUid,omitempty"`
}

// A2ACardResult is the outcome of a card resolution. Always HTTP 200: the
// caller reads Success, like IntegrationTestResult.
type A2ACardResult struct {
	Success bool                        `json:"success"`
	Agent   *notifications.A2AAgentInfo `json:"agent,omitempty"`
	Error   string                      `json:"error,omitempty"`
}

// ResolveA2ACard resolves the Agent Card for possibly unsaved settings.
func (s *Service) ResolveA2ACard(ctx context.Context, orgSlug string, req *A2ACardRequest) (*A2ACardResult, error) {
	settings := map[string]any{}

	if req.IntegrationUID != "" {
		conn, err := s.loadChannel(ctx, orgSlug, req.IntegrationUID)
		if err != nil {
			return nil, err
		}

		if conn.Type != models.ConnectionTypeA2A {
			return nil, ErrInvalidConnectionType
		}

		stored, err := s.loadDecryptedSettings(ctx, conn)
		if err != nil {
			return nil, err
		}

		settings = stored
	} else if _, err := s.db.GetOrganizationBySlug(ctx, orgSlug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	for key, value := range req.Settings {
		if text, ok := value.(string); ok && text == "" {
			if _, kept := settings[key]; kept {
				continue
			}
		}

		settings[key] = value
	}

	agent, err := notifications.ResolveA2AAgent(ctx, s.egressGuard(), models.JSONMap(settings))
	if err != nil {
		return &A2ACardResult{Error: err.Error()}, nil //nolint:nilerr // the failure is the result
	}

	return &A2ACardResult{Success: true, Agent: agent}, nil
}

// testA2AConnection is TestIntegration for a saved A2A integration.
func (s *Service) testA2AConnection(ctx context.Context, settings models.JSONMap) *IntegrationTestResult {
	start := time.Now()
	agent, err := notifications.ResolveA2AAgent(ctx, s.egressGuard(), settings)
	result := &IntegrationTestResult{DurationMs: time.Since(start).Milliseconds()}

	if err != nil {
		result.Error = err.Error()

		if setting, ok := notifications.MissingSetting(err); ok {
			result.Code = TestResultCodeMisconfigured
			result.MissingSetting = setting
		}

		return result
	}

	result.Success = true
	result.StatusCode = http.StatusOK
	result.Detail = fmt.Sprintf("%s, A2A %s, %d skill(s)", agent.Name, agent.ProtocolVersion, len(agent.Skills))

	return result
}

// ResolveA2ACard handles POST /orgs/:org/integrations/a2a/resolve-card.
func (h *Handler) ResolveA2ACard(writer http.ResponseWriter, req *http.Request) error {
	var body A2ACardRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: invalidJSONField, Message: invalidJSONMessage},
		})
	}

	result, err := h.svc.ResolveA2ACard(req.Context(), httpx.Param(req, "org"), &body)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, result)
}
