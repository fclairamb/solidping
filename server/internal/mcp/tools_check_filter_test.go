package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkfilter"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
)

// TestListResultsToolCapsCheckFilter: list_results refuses more than the
// checkUid cap with an error result, and keeps filtering a short list
// (security scan 2026-11-07, AG1).
func TestListResultsToolCapsCheckFilter(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	checks := make([]*models.Check, 0, 3)
	for _, slug := range []string{"api-prod", "db-prod", "cdn-prod"} {
		check := models.NewCheck(org.UID, slug, "http")
		r.NoError(dbSvc.CreateCheck(ctx, check))
		r.NoError(dbSvc.CreateResult(ctx, &models.Result{
			UID:             uuid.Must(uuid.NewV7()).String(),
			OrganizationUID: org.UID,
			CheckUID:        check.UID,
			PeriodType:      "raw",
			PeriodStart:     time.Now().Add(-time.Minute),
		}))
		checks = append(checks, check)
	}

	handler := &Handler{dbService: dbSvc, resultsSvc: results.NewService(dbSvc, nil)}

	tooMany := strings.TrimSuffix(strings.Repeat("api-prod,", checkfilter.MaxIdentifiers+1), ",")
	result := handler.toolListResults(ctx, org.Slug, map[string]any{propCheckUID: tooMany})
	r.True(result.IsError)
	r.Contains(result.Content[0].Text, "checkUid")

	result = handler.toolListResults(ctx, org.Slug, map[string]any{
		propCheckUID:   checks[0].UID + "," + *checks[1].Slug,
		propPeriodType: "raw",
	})
	r.False(result.IsError, "%+v", result.Content)

	payload, ok := result.StructuredContent.(ListResultsResponseWithFilter)
	r.True(ok, "%T", result.StructuredContent)

	got := map[string]struct{}{}
	for _, row := range payload.Data {
		got[row.CheckUID] = struct{}{}
	}
	r.Equal(map[string]struct{}{checks[0].UID: {}, checks[1].UID: {}}, got)
}

// TestListIncidentsToolCapsCheckFilter: list_incidents refuses more than the
// checkUid cap with an error result.
func TestListIncidentsToolCapsCheckFilter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler, org := newIncidentToolsEnv(t)

	tooMany := strings.Repeat(",", checkfilter.MaxIdentifiers)
	result := handler.toolListIncidents(t.Context(), org.Slug, map[string]any{propCheckUID: tooMany})
	r.True(result.IsError)
	r.Contains(result.Content[0].Text, "checkUid")
}
