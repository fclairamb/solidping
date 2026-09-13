package checkjobsvc_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/paramkeys"
	"github.com/fclairamb/solidping/server/internal/secretref"
)

// fakeParamStore is the one-method slice of the DB a ${param:} lookup needs.
// It stores rows under their REAL storage key, so a test that forgot
// paramkeys.StorageKey fails here rather than passing against a fake that was
// more generous than the database.
type fakeParamStore struct {
	rows map[string]string
}

// newFakeParamStore takes keys as an operator writes them and stores them where
// the org parameters API puts them.
func newFakeParamStore(params map[string]string) fakeParamStore {
	rows := make(map[string]string, len(params))
	for key, value := range params {
		rows[paramkeys.StorageKey(key)] = value
	}

	return fakeParamStore{rows: rows}
}

func (f fakeParamStore) GetOrgParameter(
	_ context.Context, _, key string,
) (*models.Parameter, error) {
	value, ok := f.rows[key]
	if !ok {
		return nil, nil //nolint:nilnil // "not found" is how the real store answers
	}

	return &models.Parameter{Key: key, Value: models.JSONMap{models.ParameterValueKey: value}}, nil
}

// TestParamOverlayResolvesParamsAndLeavesEnvAlone pins the split that makes the
// two-stage design work: the API resolves what only it can read (the org's
// parameters) and must NOT touch ${env:}, which belongs to whichever process
// executes the check. An API that eagerly resolved `env:` would silently turn a
// deported agent's per-region credential into the API pod's own.
func TestParamOverlayResolvesParamsAndLeavesEnvAlone(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	store := newFakeParamStore(map[string]string{
		"sso-password": "hunter2",
		"shared-token": "org-token",
	})

	overlay, err := checkjobsvc.ParamOverlay(t.Context(), store, "org-1", map[string]any{
		"url":        "https://sso.acme.com/token",
		"body":       "password=${param:sso-password}",
		"header":     "Bearer ${param:shared-token}",
		"agent_only": "Bearer ${env:SP_AGENT_TOKEN}",
		"plain":      "nothing to see",
	})
	r.NoError(err)

	r.Equal("password=hunter2", overlay["body"])
	r.Equal("Bearer org-token", overlay["header"])

	_, touchedEnv := overlay["agent_only"]
	r.False(touchedEnv, "${env:} must be left for the executing process")

	_, touchedPlain := overlay["plain"]
	r.False(touchedPlain, "a value with no reference is not part of the overlay")
}

// TestParamOverlayFailsOnAMissingParameter is what turns a deleted parameter
// into a visible error result rather than a probe carrying the literal text.
func TestParamOverlayFailsOnAMissingParameter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, err := checkjobsvc.ParamOverlay(t.Context(), newFakeParamStore(nil), "org-1", map[string]any{
		"body": "password=${param:deleted-yesterday}",
	})
	r.ErrorIs(err, secretref.ErrUnresolved)
	r.Contains(err.Error(), "unresolved secret reference: param:deleted-yesterday")
}

// TestParamOverlayCannotReachAPlatformRow keeps the boundary in force on the
// EXECUTION path, not only at write time: a check row written before any of
// this existed must not be able to exfiltrate the org's wrapped DEK when it
// next runs.
//
// The row is seeded at its REAL storage key (no paramkeys.StorageKey), which is
// where the credentials package writes it — so this fails if the lookup ever
// starts reading outside the org-managed namespace again.
func TestParamOverlayCannotReachAPlatformRow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	store := fakeParamStore{rows: map[string]string{"encryption.dek": "wrapped-dek-material"}}

	_, err := checkjobsvc.ParamOverlay(t.Context(), store, "org-1", map[string]any{
		"body": "stolen=${param:encryption.dek}",
	})
	r.ErrorIs(err, secretref.ErrUnresolved)
}

// TestParamOverlayNeverMutatesTheStoredConfig is the invariant behind "the
// reference is what is stored": the map handed in belongs to a claimed job row
// and must come back untouched, or the resolved value would ride along into
// anything that later reads or logs it.
func TestParamOverlayNeverMutatesTheStoredConfig(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	store := newFakeParamStore(map[string]string{"sso-password": "hunter2"})
	config := map[string]any{"body": "password=${param:sso-password}"}

	_, err := checkjobsvc.ParamOverlay(t.Context(), store, "org-1", config)
	r.NoError(err)
	r.Equal("password=${param:sso-password}", config["body"])
}
