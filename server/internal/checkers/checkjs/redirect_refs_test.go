package checkjs

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// refValue stands for the value a `${param:api-key}` reference in env resolved to.
const redirectRefValue = "resolved-param-value"

// refScript has no `secrets`: its key comes from env, a resolved reference.
const refScript = `
var resp = http.get(env.URL, {headers: {
  "X-Api-Key": env.KEY,
  "User-Agent": "acme-probe/1.0"
}});
if (resp.error) { return {status: "error", output: {error: resp.error}}; }
return {status: resp.statusCode === 200 ? "up" : "down",
  output: {dropped: resp.redirectDroppedHeaders || []}};
`

// runRefRedirect runs refScript through a cross-host redirect, with the env
// value registered as a resolved reference value when registered is set.
func runRefRedirect(t *testing.T, registered bool) (*checkerdef.Result, *headerRecorder) {
	t.Helper()

	rec, final := redirectFixture(t)
	start := redirectingServer(t, otherHostURL(final.URL)+"/landing")

	ctx := context.Background()
	if registered {
		ctx = checkerdef.WithResolvedReferenceValues(ctx, []string{redirectRefValue})
	}

	result, err := (&JSChecker{}).Execute(ctx, &JSConfig{
		Script:  refScript,
		Timeout: 10 * time.Second,
		Env:     map[string]string{"URL": start.URL + "/go", "KEY": redirectRefValue},
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	return result, rec
}

func assertRefHeaderDropped(t *testing.T, result *checkerdef.Result, rec *headerRecorder) {
	t.Helper()

	require.Equal(t, "up", result.Status.String(), "output: %#v", result.Output)

	got := rec.last()
	require.NotNil(t, got)
	require.Empty(t, got.Get("X-Api-Key"), "a referenced value must not cross origins")
	require.Equal(t, "acme-probe/1.0", got.Get("User-Agent"))
	require.Equal(t, []string{"X-Api-Key"}, result.Output[OutputKeyRedirectDroppedSecretHeaders], "%#v", result.Output)
	require.NotContains(t, fmt.Sprint(result.Output), redirectRefValue)
}

// Spec 2026-11-15-01, isolated child (the default): a check with no `secrets`
// whose header holds a resolved reference drops it on a cross-origin hop.
func TestRedirectStripsReferenceHeaderCrossHostIsolated(t *testing.T) {
	t.Parallel()

	result, rec := runRefRedirect(t, true)
	assertRefHeaderDropped(t, result, rec)
}

// Positive control: without a registered value the header follows, as before.
func TestRedirectKeepsHeaderWithoutReferenceValue(t *testing.T) {
	t.Parallel()

	result, rec := runRefRedirect(t, false)
	require.Equal(t, "up", result.Status.String(), "output: %#v", result.Output)
	require.Equal(t, redirectRefValue, rec.last().Get("X-Api-Key"))
	require.NotContains(t, result.Output, OutputKeyRedirectDroppedSecretHeaders)
}

// The same, in process.
//
//nolint:paralleltest // Swaps the package-level isolation settings.
func TestRedirectStripsReferenceHeaderCrossHostInProcess(t *testing.T) {
	previous := SetIsolation(IsolationSettings{Enabled: false})
	t.Cleanup(func() { SetIsolation(previous) })

	result, rec := runRefRedirect(t, true)
	assertRefHeaderDropped(t, result, rec)
}
