package kubernetes_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/fclairamb/solidping/server/internal/db/models"
	integrationk8s "github.com/fclairamb/solidping/server/internal/integrations/kubernetes"
)

// kubeconfigWithUser renders a one-cluster kubeconfig whose single user entry
// is the given (already indented) YAML body.
func kubeconfigWithUser(clusterExtra, userBody string) string {
	return `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:1
` + clusterExtra + `contexts:
- name: ctx
  context:
    cluster: c
    user: u
current-context: ctx
users:
- name: u
  user:
` + userBody
}

// execKubeconfig returns a kubeconfig whose exec plugin would `touch marker`.
func execKubeconfig(marker string) string {
	return kubeconfigWithUser("", `    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: touch
      args: ["`+marker+`"]
      interactiveMode: Never
`)
}

func TestValidateKubeconfigSafe(t *testing.T) {
	t.Parallel()

	authProvider := `    auth-provider:
      name: oidc
      config:
        idp-issuer-url: https://idp.acme.com
`

	// A second, non-current user still counts: switching the current context
	// later must not activate a dormant exec plugin.
	dormantExec := strings.Replace(kubeconfigWithUser("", "    token: tok\n"),
		"users:\n", "users:\n- name: dormant\n  user:\n    exec:\n      apiVersion: client.authentication.k8s.io/v1\n"+
			"      command: id\n", 1)

	tests := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{name: "token", raw: kubeconfigWithUser("", "    token: tok\n")},
		{
			name: "embedded client cert",
			raw: kubeconfigWithUser("    certificate-authority-data: Zm9v\n",
				"    client-certificate-data: Zm9v\n    client-key-data: YmFy\n"),
		},
		{name: "exec", raw: execKubeconfig("/tmp/never"), wantErr: integrationk8s.ErrKubeconfigExec},
		{name: "dormant exec", raw: dormantExec, wantErr: integrationk8s.ErrKubeconfigExec},
		{name: "auth-provider", raw: kubeconfigWithUser("", authProvider), wantErr: integrationk8s.ErrKubeconfigAuthProvider},
		{
			name:    "tokenFile",
			raw:     kubeconfigWithUser("", "    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token\n"),
			wantErr: integrationk8s.ErrKubeconfigFileReference,
		},
		{
			name:    "client-key file",
			raw:     kubeconfigWithUser("", "    client-certificate: /etc/c.pem\n    client-key: /etc/k.pem\n"),
			wantErr: integrationk8s.ErrKubeconfigFileReference,
		},
		{
			name:    "certificate-authority file",
			raw:     kubeconfigWithUser("    certificate-authority: /etc/ca.pem\n", "    token: tok\n"),
			wantErr: integrationk8s.ErrKubeconfigFileReference,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			err := integrationk8s.ValidateKubeconfigSafe(tc.raw)

			if tc.wantErr == nil {
				r.NoError(err)

				return
			}

			r.ErrorIs(err, tc.wantErr)
			r.ErrorIs(err, integrationk8s.ErrUnsafeKubeconfig)
		})
	}
}

// TestBuildRestConfigRejectsUnsafeKubeconfig is the use-time regression: an
// exec or auth-provider kubeconfig yields a coded error, never a *rest.Config.
func TestBuildRestConfigRejectsUnsafeKubeconfig(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"exec":          execKubeconfig(filepath.Join(t.TempDir(), "pwned")),
		"auth-provider": kubeconfigWithUser("", "    auth-provider:\n      name: gcp\n"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			cfg, err := integrationk8s.BuildRestConfig(
				&models.KubernetesSettings{},
				&models.KubernetesPrivateSettings{Kubeconfig: raw},
			)
			r.ErrorIs(err, integrationk8s.ErrUnsafeKubeconfig)
			r.Nil(cfg)
		})
	}
}

// TestExecKubeconfigFixtureFires is the positive control for the "never
// execs" assertions: built straight through client-go (bypassing the guard),
// the same exec kubeconfig does run its command on the first API call. If this
// ever stops creating the marker, the absence assertions elsewhere prove
// nothing.
func TestExecKubeconfigFixtureFires(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	marker := filepath.Join(t.TempDir(), "pwned")

	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(execKubeconfig(marker)))
	r.NoError(err)
	r.NotNil(cfg.ExecProvider)

	cs, err := kubernetes.NewForConfig(cfg)
	r.NoError(err)

	_, _ = cs.Discovery().ServerVersion()

	_, statErr := os.Stat(marker)
	r.NoError(statErr, "the exec plugin should have run without the guard")
}

// TestBuildRestConfigInClusterDisabled: with the opt-in off, in-cluster mode
// is refused and the in-cluster loader is never called.
//
//nolint:paralleltest // swaps package-level seams; must run serially.
func TestBuildRestConfigInClusterDisabled(t *testing.T) {
	r := require.New(t)

	t.Cleanup(integrationk8s.SetAllowInCluster(false))

	called := false
	t.Cleanup(integrationk8s.SetInClusterConfig(func() (*rest.Config, error) {
		called = true

		return &rest.Config{Host: "https://kubernetes.default.svc"}, nil
	}))

	cfg, err := integrationk8s.BuildRestConfig(&models.KubernetesSettings{InCluster: true}, nil)
	r.ErrorIs(err, integrationk8s.ErrInClusterDisabled)
	r.Nil(cfg)
	r.False(called, "the in-cluster loader must not run when the opt-in is off")
}

// TestBuildRestConfigInClusterAllowed is the positive control: with the
// opt-in on, the (stubbed) in-cluster loader is used.
//
//nolint:paralleltest // swaps package-level seams; must run serially.
func TestBuildRestConfigInClusterAllowed(t *testing.T) {
	r := require.New(t)

	t.Cleanup(integrationk8s.SetAllowInCluster(true))
	t.Cleanup(integrationk8s.SetInClusterConfig(func() (*rest.Config, error) {
		return &rest.Config{Host: "https://kubernetes.default.svc"}, nil
	}))

	cfg, err := integrationk8s.BuildRestConfig(&models.KubernetesSettings{InCluster: true}, nil)
	r.NoError(err)
	r.Equal("https://kubernetes.default.svc", cfg.Host)
	r.True(integrationk8s.InClusterAllowed())
}

// TestResolveClientsetByUIDRejectsExecKubeconfig pins that the worker /
// discovery entry point surfaces the use-time rejection for a stored row
// (e.g. one written before the save-time check existed) and never execs.
//
//nolint:paralleltest // uses the real clientset factory alongside serial seam tests.
func TestResolveClientsetByUIDRejectsExecKubeconfig(t *testing.T) {
	r := require.New(t)
	f := newFixture(t)
	marker := filepath.Join(t.TempDir(), "pwned")

	conn := models.NewIntegration(f.org.UID, models.ConnectionTypeKubernetes, "Cluster")
	conn.Settings = models.JSONMap{"kubeconfig": execKubeconfig(marker)}
	r.NoError(f.dbSvc.CreateChannel(t.Context(), conn))

	cs, err := integrationk8s.ResolveClientsetByUID(t.Context(), f.dbSvc, f.creds, conn.UID)
	r.ErrorIs(err, integrationk8s.ErrKubeconfigExec)
	r.Nil(cs)

	_, statErr := os.Stat(marker)
	r.ErrorIs(statErr, os.ErrNotExist)
}
