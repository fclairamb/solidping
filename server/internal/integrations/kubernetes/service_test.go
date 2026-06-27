package kubernetes_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	integrationk8s "github.com/fclairamb/solidping/server/internal/integrations/kubernetes"
)

// contextCancelled returns an already-cancelled context plus its cancel func.
func contextCancelled() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return ctx, cancel
}

func TestValidateConnectionSuccess(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cs := fake.NewSimpleClientset()
	// Pin a known server version so the success note is deterministic.
	disc, ok := cs.Discovery().(*fakediscovery.FakeDiscovery)
	r.True(ok)
	disc.FakedServerVersion = &version.Info{GitVersion: "v1.31.4"}

	res, err := integrationk8s.ValidateConnection(t.Context(), cs)
	r.NoError(err)
	r.Equal("v1.31.4", res.GitVersion)
}

func TestValidateConnectionAuthFailure(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cs := fake.NewSimpleClientset()
	// Inject an unauthorized error on the version probe.
	cs.PrependReactor("get", "version", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewUnauthorized("bad token")
	})

	_, err := integrationk8s.ValidateConnection(t.Context(), cs)
	r.Error(err)
	r.Contains(err.Error(), "validate connection")
}

func TestValidateConnectionNilClientset(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := integrationk8s.ValidateConnection(t.Context(), nil)
	r.ErrorIs(err, integrationk8s.ErrMissingCredentials)
}

func TestValidateConnectionContextCancelled(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ctx, cancel := contextCancelled()
	defer cancel()

	cs := fake.NewSimpleClientset()
	_, err := integrationk8s.ValidateConnection(ctx, cs)
	r.Error(err)
	r.True(errors.Is(err, context.Canceled))
}
