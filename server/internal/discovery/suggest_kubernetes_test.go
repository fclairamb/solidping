package discovery

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// k8sCheckConfig is the promoted-kubernetes-check config shape, for assertions.
type k8sCheckConfig struct {
	ClusterUID string `json:"clusterUid"`
	Namespace  string `json:"namespace"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

func TestSuggestKubernetesChecksAlwaysEmitsReplicaHealthCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rows := SuggestKubernetesChecks(&KubernetesSuggestInput{
		UID:             "wl-uid",
		ClusterUID:      "cluster-1",
		Kind:            "Deployment",
		Namespace:       "prod",
		Name:            "api-server",
		Images:          []string{"nginx:1.27"},
		DesiredReplicas: 3,
		ReadyReplicas:   2,
		// No endpoints (ClusterIP-only) → only the kubernetes check.
	})

	r.Len(rows, 1, "a ClusterIP-only workload yields only the replica-health check")

	row := rows[0]
	r.Equal(checkTypeKubernetes, row.Type)
	r.Equal("wl-uid", row.GroupKey)
	r.Equal("prod/api-server", row.GroupLabel)

	var cfg k8sCheckConfig
	r.NoError(json.Unmarshal(row.Config, &cfg))
	r.Equal("cluster-1", cfg.ClusterUID)
	r.Equal("prod", cfg.Namespace)
	r.Equal("Deployment", cfg.Kind)
	r.Equal("api-server", cfg.Name)

	// Metadata carries the group-display hints.
	r.Contains(string(row.Metadata), "nginx:1.27")
	r.Contains(string(row.Metadata), "desiredReplicas")
	r.Contains(string(row.Metadata), "readyReplicas")
}

func TestSuggestKubernetesChecksAddsEndpointRows(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rows := SuggestKubernetesChecks(&KubernetesSuggestInput{
		UID:        "wl-uid",
		ClusterUID: "cluster-1",
		Kind:       "Deployment",
		Namespace:  "prod",
		Name:       "web",
		Endpoints: []KubernetesEndpoint{
			{Address: "203.0.113.10", Port: 443, Scheme: "https", Source: "LoadBalancer"},
			{Address: "web.example.com", Port: 80, Scheme: "http", Source: "Ingress"},
			{Address: "10.0.0.5", Port: 5432, Scheme: "tcp", Source: "NodePort"},
		},
	})

	r.Len(rows, 4, "the kubernetes check plus one row per endpoint")

	types := map[string]int{}
	slugs := map[string]struct{}{}
	allSameGroup := true

	for i := range rows {
		types[rows[i].Type]++
		if rows[i].GroupKey != "wl-uid" || rows[i].GroupLabel != "prod/web" {
			allSameGroup = false
		}

		_, dup := slugs[rows[i].Slug]
		r.False(dup, "slug %q must be unique within the group", rows[i].Slug)
		slugs[rows[i].Slug] = struct{}{}
	}

	r.True(allSameGroup, "every row shares group_key + group_label")
	r.Equal(1, types[checkTypeKubernetes])
	r.Equal(2, types[checkTypeHTTP], "https + http endpoints both promote to http checks")
	r.Equal(1, types[checkTypeTCP], "the 5432 endpoint is a tcp check")
}

func TestSuggestKubernetesChecksSkipsAddresslessEndpoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rows := SuggestKubernetesChecks(&KubernetesSuggestInput{
		UID:        "wl-uid",
		ClusterUID: "cluster-1",
		Kind:       "Deployment",
		Namespace:  "prod",
		Name:       "api",
		Endpoints: []KubernetesEndpoint{
			// A NodePort whose node IP is unknown out-of-cluster.
			{Address: "", Port: 31000, Scheme: "http", Source: "NodePort"},
		},
	})

	r.Len(rows, 1, "an address-less endpoint yields no http/tcp suggestion")
	r.Equal(checkTypeKubernetes, rows[0].Type)
}

func TestSuggestKubernetesChecksSlugStartsWithLetter(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rows := SuggestKubernetesChecks(&KubernetesSuggestInput{
		UID:        "wl-uid",
		ClusterUID: "cluster-1",
		Kind:       "Deployment",
		Namespace:  "prod",
		Name:       "api",
	})

	r.NotEmpty(rows)
	// The checks service requires slugs to start with a letter.
	first := rows[0].Slug[0]
	r.True(first >= 'a' && first <= 'z', "slug %q must start with a letter", rows[0].Slug)
}
