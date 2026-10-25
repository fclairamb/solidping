package checktypes

import (
	"context"
	"errors"
	"net/http"
	"sort"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/schemas"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/plugins"
)

// OrgPluginTypes reads the plugin types an org's agents reported (spec
// 2026-10-17-05 §18). Every call names the org.
type OrgPluginTypes interface {
	List(ctx context.Context, orgUID string) ([]*plugins.RemoteType, error)
	Lookup(ctx context.Context, orgUID, checkType string) (*plugins.RemoteType, bool, error)
}

// SetOrgPluginTypes wires the store of agent-reported plugin types. Without
// it the org endpoints serve the server's own types only.
func (h *Handler) SetOrgPluginTypes(store OrgPluginTypes) { h.orgTypes = store }

// withOrgPluginTypes appends the org's agent-reported types the server does
// not already list. They are enabled: they run on the org's own agents, where
// even an unsafe (exec, nagios) type is allowed.
func withOrgPluginTypes(response ListCheckTypesResponse, remotes []*plugins.RemoteType) ListCheckTypesResponse {
	listed := make(map[string]bool, len(response.Data))
	for i := range response.Data {
		listed[response.Data[i].Type] = true
	}

	for _, remote := range remotes {
		meta := remote.Meta()
		if listed[string(meta.Type)] {
			continue
		}

		secrets := append([]string{}, remote.SecretFields()...)
		sort.Strings(secrets)

		response.Data = append(response.Data, CheckTypeResponse{
			Type:                 string(meta.Type),
			Description:          meta.Description,
			Labels:               meta.Labels,
			Enabled:              true,
			MinPeriodSeconds:     durationToSeconds(meta.MinPeriod),
			MaxPeriodSeconds:     durationToSeconds(meta.MaxPeriod),
			DefaultPeriodSeconds: durationToSeconds(meta.DefaultPeriod),
			ExcludedFromTally:    true,
			SecretFields:         secrets,
			Plugin:               meta.Plugin,
			AgentReported:        true,
		})
	}

	return response
}

// orgUIDOf returns the org the middleware resolved for this request.
func orgUIDOf(req *http.Request) string {
	if org, ok := middleware.GetOrganizationFromContext(req.Context()); ok && org != nil {
		return org.UID
	}

	return ""
}

// GetOrgConfigSchema handles GET /api/v1/orgs/:org/check-types/:type/schema:
// the server's schema of the type, or the one the org's agents reported for
// it. Another org's agent type is a 404 here, like an unknown type.
func (h *Handler) GetOrgConfigSchema(writer http.ResponseWriter, req *http.Request) error {
	checkType := httpx.Param(req, "type")

	raw, err := schemas.Get(checkType)
	if errors.Is(err, schemas.ErrNotFound) && h.orgTypes != nil && checkerdef.CheckType(checkType).IsPlugin() {
		remote, ok, lookupErr := h.orgTypes.Lookup(req.Context(), orgUIDOf(req), checkType)
		if lookupErr != nil {
			return h.WriteInternalError(writer, req, lookupErr)
		}

		if ok {
			raw, err = remote.Schema(), nil
		}
	}

	if err != nil {
		if errors.Is(err, schemas.ErrNotFound) {
			return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound,
				"No config schema for check type "+checkType)
		}

		return h.WriteInternalError(writer, req, err)
	}

	writer.Header().Set("Content-Type", schemas.MediaType)
	writer.Header().Set("Cache-Control", "private, no-cache")
	writer.WriteHeader(http.StatusOK)

	if _, err := writer.Write(raw); err != nil {
		return err //nolint:wrapcheck // a write failure on a flushed response has no useful wrapping
	}

	return nil
}
