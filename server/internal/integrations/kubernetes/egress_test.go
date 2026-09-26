package kubernetes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// A `kubernetes` check under an enforcing egress policy cannot reach an API
// server on a non-public address — through a REAL client-go clientset — and
// the environment proxy is off, so HTTPS_PROXY cannot carry the request past
// the guard. The permissive run against the same server is the control.
func TestEgressGuardAppliesToTheClientset(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"major":"1","minor":"31","gitVersion":"v1.31.0"}`))
	}))
	t.Cleanup(apiServer.Close)

	version := func(restCfg *rest.Config) error {
		clientset, err := kubernetes.NewForConfig(restCfg)
		r.NoError(err)

		_, err = clientset.Discovery().ServerVersion()

		return err
	}

	denyCtx := egress.WithGuard(t.Context(), egress.New(false))
	denied := &rest.Config{Host: apiServer.URL}
	applyEgressGuard(denyCtx, denied)

	r.NotNil(denied.Dial)
	r.NotNil(denied.Proxy)

	proxyURL, err := denied.Proxy(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://10.0.0.1/", nil))
	r.NoError(err)
	r.Nil(proxyURL, "no proxy: the guard must see the real destination")

	r.ErrorIs(version(denied), egress.ErrDenied)

	allowCtx := egress.WithGuard(t.Context(), egress.New(true))
	allowed := &rest.Config{Host: apiServer.URL}
	applyEgressGuard(allowCtx, allowed)
	r.Nil(allowed.Dial, "a permissive guard leaves client-go's defaults alone")
	r.Nil(allowed.Proxy)
	r.NoError(version(allowed))

	untouched := &rest.Config{Host: apiServer.URL}
	applyEgressGuard(t.Context(), untouched)
	r.Nil(untouched.Dial)
}
