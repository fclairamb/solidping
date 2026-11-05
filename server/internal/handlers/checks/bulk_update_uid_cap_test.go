package checks_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// distinctUIDs returns n distinct UIDs that name no check.
func distinctUIDs(n int) []string {
	uids := make([]string, n)
	for idx := range uids {
		uids[idx] = "missing-" + strconv.Itoa(idx)
	}

	return uids
}

// TestBulkUpdateCapsTheRawUIDList: the 1000-UID cap applies to the list as
// sent, duplicates included, before any dedup (security scan 2026-11-05, AE1).
func TestBulkUpdateCapsTheRawUIDList(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	check := w.create(t, checks.CreateCheckRequest{})

	repeated := make([]string, checks.MaxBulkUpdateChecks+1)
	for idx := range repeated {
		repeated[idx] = check.UID
	}

	cases := map[string][]string{
		"distinct unknown uids": distinctUIDs(checks.MaxBulkUpdateChecks + 1),
		"one uid repeated":      repeated,
	}

	for name, uids := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			_, err := w.svc.BulkUpdateChecks(ctx, w.org.Slug, bulkRequest(uids, `{"enabled": false}`, false))
			r.Error(err)
			r.True(checks.IsBulkUpdateRequestError(err), "a 400, got %v", err)
			r.Contains(err.Error(), "at most")
		})
	}
}

// TestBulkUpdateAcceptsExactlyTheCap: MaxBulkUpdateChecks UIDs, some real, are
// accepted; a repeated UID below the cap is still deduplicated (the order
// is covered by TestUniqueCheckUIDsKeepsOrder).
func TestBulkUpdateAcceptsExactlyTheCap(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	first := w.create(t, checks.CreateCheckRequest{})
	second := w.create(t, checks.CreateCheckRequest{})

	uids := append([]string{first.UID, second.UID}, distinctUIDs(checks.MaxBulkUpdateChecks-2)...)
	resp, err := w.svc.BulkUpdateChecks(ctx, w.org.Slug, bulkRequest(uids, `{"enabled": false}`, false))
	r.NoError(err)
	r.Len(resp.Data, 2)
	r.Len(resp.Skipped, checks.MaxBulkUpdateChecks-2)

	for _, skip := range resp.Skipped {
		r.Equal(checks.BulkUpdateSkipNotFound, skip.Reason)
	}

	for _, uid := range []string{first.UID, second.UID} {
		got, getErr := w.svc.GetCheck(ctx, w.org.Slug, uid, checks.GetCheckOptions{})
		r.NoError(getErr)
		r.False(*got.Enabled)
	}

	resp, err = w.svc.BulkUpdateChecks(ctx, w.org.Slug, bulkRequest(
		[]string{second.UID, "ghost", first.UID, second.UID, "ghost"}, `{"enabled": true}`, true))
	r.NoError(err)
	r.Len(resp.Data, 2, "a repeated UID is updated once")
	r.ElementsMatch([]string{first.UID, second.UID}, []string{resp.Data[0].UID, resp.Data[1].UID})
	r.Len(resp.Skipped, 1, "a repeated UID is reported once")
	r.Equal("ghost", resp.Skipped[0].UID)
}

// TestSwitchToAutoPlacementCapsTheUIDList: the deprecated route takes at most
// MaxBulkUpdateChecks UIDs, and a short list still works.
func TestSwitchToAutoPlacementCapsTheUIDList(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	w := newPlacementWorld(t, 0, "gravelines", "paris")
	pinned := w.create(t, checks.CreateCheckRequest{Regions: []string{"gravelines", "paris"}})

	_, err := w.svc.SwitchToAutoPlacement(ctx, w.org.Slug, &checks.AutoPlacementRequest{
		CheckUIDs: distinctUIDs(checks.MaxBulkUpdateChecks + 1),
	})
	r.Error(err)
	r.True(checks.IsBulkUpdateRequestError(err), "a 400, got %v", err)

	resp, err := w.svc.SwitchToAutoPlacement(ctx, w.org.Slug, &checks.AutoPlacementRequest{
		CheckUIDs: []string{pinned.UID}, DryRun: true,
	})
	r.NoError(err)
	r.Len(resp.Data, 1)
}

// TestBulkRoutesRefuseOversizedRequests: over HTTP, too many UIDs is a 400 on
// both bulk routes, and a body over MaxBulkRequestBytes is refused unread.
func TestBulkRoutesRefuseOversizedRequests(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	router := newFullChecksRouter(t)
	base := "/orgs/acme/checks"
	tooMany := `["` + strings.Join(distinctUIDs(checks.MaxBulkUpdateChecks+1), `","`) + `"]`

	rec := checksCall(t, router, http.MethodPost, base+"/auto-placement", `{"checkUids":`+tooMany+`}`)
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = checksCall(t, router, http.MethodPatch, base, `{"checkUids":`+tooMany+`,"changes":{"enabled":false}}`)
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	huge := `{"checkUids":["` + strings.Repeat("a", checks.MaxBulkRequestBytes) + `"],"changes":{"enabled":false}}`

	rec = checksCall(t, router, http.MethodPatch, base, huge)
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())

	rec = checksCall(t, router, http.MethodPost, base+"/auto-placement", huge)
	r.Equal(http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}
