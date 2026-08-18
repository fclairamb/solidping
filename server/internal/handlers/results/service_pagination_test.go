package results

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// TestListResults_PaginationHasNoTotal pins spec 2026-08-18-04: the results
// endpoint's pagination object never carried a real total (the DB layer
// hardcoded it to 0), so it is dropped from the response entirely rather than
// shipped as a documented lie. This must hold both at the Go struct level
// (PaginationResponse has no Total field — enforced at compile time by every
// other file in this package) and at the wire level: the marshaled JSON must
// not contain a "total" key under "pagination", while "cursor" and "size"
// must still be present.
func TestListResults_PaginationHasNoTotal(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("pagination-total-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "pagination-total-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// Seed enough raw rows that a hypothetical total would clearly differ
	// from 0 and from len(data), so a resurrected `total: 0` would not be
	// masked by coincidence.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 5 {
		res := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0.1)
		res.PeriodStart = base.Add(time.Duration(i) * time.Minute)
		r.NoError(dbSvc.CreateResult(ctx, res))
	}

	svc := NewService(dbSvc)

	resp, err := svc.ListResults(ctx, org.Slug, &ListResultsOptions{Size: 100})
	r.NoError(err)
	r.NotEmpty(resp.Data)

	body, err := json.Marshal(resp)
	r.NoError(err)

	var decoded map[string]any
	r.NoError(json.Unmarshal(body, &decoded))

	pagination, ok := decoded["pagination"].(map[string]any)
	r.True(ok, "pagination must be a JSON object")

	_, hasTotal := pagination["total"]
	r.False(hasTotal, "results pagination JSON must not contain a total key, got: %v", pagination)

	_, hasSize := pagination["size"]
	r.True(hasSize, "results pagination JSON must keep size")
}
