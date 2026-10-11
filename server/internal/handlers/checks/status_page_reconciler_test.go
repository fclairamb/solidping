package checks_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// recordingReconciler is a checks.StatusPageReconciler that only remembers
// which org it was asked to reconcile.
type recordingReconciler struct {
	mu   sync.Mutex
	orgs []string
}

func (r *recordingReconciler) ReconcileOrgSelectors(_ context.Context, orgUID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.orgs = append(r.orgs, orgUID)
}

func (r *recordingReconciler) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.orgs...)
}

// TestCheckWritesTriggerTheStatusPageReconcile pins the wiring that makes a
// selector-driven status page section (spec 2026-08-29-11) follow check writes
// with nobody touching the page: creating a labeled check, replacing its
// labels and deleting it each ask the status page layer to reconcile the org.
// What the reconcile then does with the labels is pinned by
// statuspages.TestSelector_LabelRoundTrip; the public-view backstop is
// throttled, so without these calls a page read right after the write is stale.
func TestCheckWritesTriggerTheStatusPageReconcile(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, _, org := setupQuotaService(t, 100)
	recorder := &recordingReconciler{}
	svc.SetStatusPageReconciler(recorder)

	req := httpCheckReq()
	req.Labels = map[string]string{"public": "true"}

	created, err := svc.CreateCheck(ctx, org.Slug, req)
	r.NoError(err)
	r.Equal([]string{org.UID}, recorder.calls(), "creating a labeled check must reconcile its org")

	// `labels: {}` is the replace-all update the dashboard sends to drop them.
	noLabels := map[string]string{}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Labels: &noLabels})
	r.NoError(err)
	r.Equal([]string{org.UID, org.UID}, recorder.calls(), "replacing a check's labels must reconcile its org")

	r.NoError(svc.DeleteCheck(ctx, org.Slug, created.UID))
	r.Equal([]string{org.UID, org.UID, org.UID}, recorder.calls(), "deleting a check must reconcile its org")
}
