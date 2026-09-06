package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	enthandler "github.com/fclairamb/solidping/server/internal/handlers/entitlements"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// The billing service is the one caller of an org-scoped write route that has
// no membership row at all: it proves identity by signing the request, and
// RequireOrgWrite lets it through by short-circuiting on isServiceAuthorized
// BEFORE the role gate runs.
//
// internal/servicesig and internal/handlers/entitlements already pin that
// middleware in isolation. What they cannot pin is the ORDER of the real chain
// registered in server.go: if the write floor ever moved ahead of the service
// short-circuit, or the signature middleware were dropped from the group,
// billing would start answering 401/403 in production and every isolated test
// would stay green. So this drives the production route table — real NewServer,
// real SetupRoutes — over the wire.
const (
	billingPushKeyID  = "billing-push-route-test"
	billingPushSecret = "billing-push-route-test-secret"
)

// billingPushEnv is the viewer fixture plus a configured inbound signing key.
// It reuses newViewerEnv on purpose: that is the harness that boots the REAL
// wired server, and the property under test here is the other half of the same
// gate — the floor refuses a viewer (proven there) and must NOT refuse the
// signed service (proven here).
type billingPushEnv struct {
	*viewerEnv
	key servicesig.Key
}

func newBillingPushEnv(t *testing.T) *billingPushEnv {
	t.Helper()
	r := require.New(t)

	env := newViewerEnv(t)
	key := servicesig.Key{ID: billingPushKeyID, Secret: billingPushSecret}

	raw, err := json.Marshal(servicesig.KeySet{key})
	r.NoError(err)
	r.NoError(env.server.dbService.SetSystemParameter(
		context.Background(), enthandler.ParamServiceSigningKeys, string(raw), true))

	return &billingPushEnv{viewerEnv: env, key: key}
}

func (e *billingPushEnv) path() string {
	return "/api/v1/orgs/" + e.org.Slug + "/entitlements"
}

// putEntitlements sends a PUT with no session credential whatsoever and lets
// the caller stamp (or not stamp) the signature headers.
func (e *billingPushEnv) putEntitlements(body []byte, sign func(*http.Request)) (int, string) {
	e.t.Helper()
	r := require.New(e.t)

	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodPut, e.ts.URL+e.path(), bytes.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	if sign != nil {
		sign(req)
	}

	resp, err := e.ts.Client().Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)

	rendered, _ := json.Marshal(decoded)

	return resp.StatusCode, string(rendered)
}

// signBillingPush stamps the wire contract: HMAC over
// <timestamp>.<METHOD>.<path>.<sha256 body>, against the path only.
func signBillingPush(key servicesig.Key, method, path string, body []byte) func(*http.Request) {
	return func(req *http.Request) {
		ts := time.Now().Unix()
		req.Header.Set(servicesig.HeaderTimestamp, strconv.FormatInt(ts, 10))
		req.Header.Set(servicesig.HeaderKeyID, key.ID)
		req.Header.Set(servicesig.HeaderSignature,
			servicesig.SignaturePrefix+servicesig.Sign(key, ts, method, path, body))
	}
}

// storedMaxChecks reads the row back, so the assertion is about what actually
// landed rather than about a response body.
func (e *billingPushEnv) storedMaxChecks() *int {
	e.t.Helper()

	row, err := e.server.dbService.GetOrgEntitlements(context.Background(), e.org.UID)
	require.NoError(e.t, err)

	if row == nil {
		return nil
	}

	return row.Payload.Limits.MaxChecks
}

// TestSignedBillingPushSurvivesTheWriteFloor is the production-critical case of
// spec 2026-09-06-03 §3: a correctly-signed entitlements PUT, carrying no user
// session at all, must still return 2xx through the full wired chain
// (ServiceSignature -> ServiceTokenBypass -> RequireAuth -> RequireOrgAccess ->
// RequireOrgWrite -> handler) and must actually write the row.
//
// The negative controls are not decoration. Without them the test could pass
// vacuously — an endpoint that accepted everything, or a fixture whose org slug
// never reached the guarded group, would look identical from a lone 200.
func TestSignedBillingPushSurvivesTheWriteFloor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newBillingPushEnv(t)
	body := []byte(`{"limits":{"maxChecks":4242}}`)

	// Negative control 1: the byte-identical request with NO signature and no
	// session is refused, so the route is genuinely guarded.
	status, respBody := env.putEntitlements(body, nil)
	r.Equal(http.StatusUnauthorized, status, respBody)
	r.Nil(env.storedMaxChecks(), "an unauthenticated push must not land")

	// Negative control 2: a well-formed signature from the wrong secret is
	// refused too, so it is the signature that authorizes and not the mere
	// presence of the headers.
	rogue := servicesig.Key{ID: billingPushKeyID, Secret: "not-the-configured-secret"}
	status, respBody = env.putEntitlements(body, signBillingPush(rogue, http.MethodPut, env.path(), body))
	r.Equal(http.StatusUnauthorized, status, respBody)
	r.Nil(env.storedMaxChecks(), "a badly-signed push must not land")

	// Negative control 3: a viewer session — a real, valid credential — is
	// refused by the write floor on this very route. This is what proves the
	// floor is actually in the chain the 2xx below travels through.
	req, err := http.NewRequestWithContext(
		context.Background(), http.MethodPut, env.ts.URL+env.path(), bytes.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.viewerToken)

	resp, err := env.ts.Client().Do(req)
	r.NoError(err)
	r.NoError(resp.Body.Close())
	r.Equal(http.StatusForbidden, resp.StatusCode, "the write floor must still refuse a viewer here")
	r.Nil(env.storedMaxChecks(), "a viewer push must not land")

	// The property: correctly signed, no session, 2xx, and the row is written.
	status, respBody = env.putEntitlements(body, signBillingPush(env.key, http.MethodPut, env.path(), body))
	r.GreaterOrEqual(status, http.StatusOK, respBody)
	r.Less(status, http.StatusMultipleChoices,
		"a correctly-signed billing push must not be refused by the write floor: %s", respBody)

	stored := env.storedMaxChecks()
	r.NotNil(stored, "the signed push must have written the entitlements row")
	r.Equal(4242, *stored)
}
