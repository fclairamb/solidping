package jobtypes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	integrationk8s "github.com/fclairamb/solidping/server/internal/integrations/kubernetes"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// errKubernetesNoCluster is returned when a kubernetes_discovery job is created
// without a clusterUid in its config.
var errKubernetesNoCluster = errors.New("kubernetes discovery job requires a clusterUid")

// k8sClientsetResolver resolves a cluster connection UID to an authenticated
// clientset. It mirrors the checkkubernetes.ClientsetResolver seam: production
// wires the real DB+credentials resolver; tests swap in one returning a fake
// clientset so the job's Run path is exercised without a live cluster.
type k8sClientsetResolver func(
	ctx context.Context, dbSvc db.Service, creds credentials.Service, clusterUID string,
) (kubernetes.Interface, error)

// resolveK8sClientset is the active resolver, defaulting to the integrations
// package's ResolveClientsetByUID. Overridable in tests via
// SetKubernetesClientsetResolver.
//
//nolint:gochecknoglobals // package-level test seam, mirrors the checker/integration indirection.
var resolveK8sClientset k8sClientsetResolver = integrationk8s.ResolveClientsetByUID

// SetKubernetesClientsetResolver overrides the clientset resolver and returns a
// restore function. Intended for tests:
//
//	defer jobtypes.SetKubernetesClientsetResolver(fake)()
func SetKubernetesClientsetResolver(f k8sClientsetResolver) func() {
	prev := resolveK8sClientset
	resolveK8sClientset = f

	return func() { resolveK8sClientset = prev }
}

// KubernetesDiscoveryConfig is the job configuration for a Kubernetes discovery
// run. ClusterUID references a stored kubernetes cluster connection (the
// 2026-06-21-02 connection); Namespaces scopes the scan (empty = all visible);
// Timeout caps the overall enumeration (default 30s).
type KubernetesDiscoveryConfig struct {
	ClusterUID string   `json:"clusterUid"`
	Namespaces []string `json:"namespaces,omitempty"`
	Timeout    string   `json:"timeout,omitempty"`
}

// KubernetesDiscoveryJobDefinition is the factory for Kubernetes discovery jobs.
type KubernetesDiscoveryJobDefinition struct{}

// Type returns the job type for Kubernetes discovery jobs.
func (d *KubernetesDiscoveryJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeKubernetesDiscovery
}

// CreateJobRun creates a new Kubernetes discovery run from the given config.
func (d *KubernetesDiscoveryJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg KubernetesDiscoveryConfig
	if err := json.Unmarshal(config, &cfg); err != nil {
		return nil, fmt.Errorf("invalid kubernetes discovery config: %w", err)
	}

	if cfg.ClusterUID == "" {
		return nil, errKubernetesNoCluster
	}

	return &KubernetesDiscoveryJobRun{config: cfg}, nil
}

// KubernetesDiscoveryJobRun is an executable Kubernetes discovery instance.
type KubernetesDiscoveryJobRun struct {
	config KubernetesDiscoveryConfig
}

// Run resolves the configured cluster connection to a clientset, lists its
// Deployments and bare ReplicaSets (with any externally-reachable endpoints),
// turns every workload into a group of suggested-check rows (a kubernetes
// replica-health check plus one http/tcp check per endpoint), and persists them
// via the shared UpsertDiscoveredChecks (source='kubernetes'). Per-row build
// errors are logged and skipped — the run never aborts on a single bad row,
// mirroring the log-and-continue resilience contract of the other discovery
// jobs.
func (r *KubernetesDiscoveryJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.OrganizationUID == nil {
		return errNoOrganizationUID
	}

	orgUID := *jctx.OrganizationUID
	jobUID := jctx.Job.UID

	log.InfoContext(ctx, "Starting Kubernetes discovery",
		"cluster_uid", r.config.ClusterUID,
		"namespace_count", len(r.config.Namespaces),
		"org_uid", orgUID,
		"job_uid", jobUID,
	)

	creds := credsFrom(jctx)

	client, err := resolveK8sClientset(ctx, jctx.DBService, creds, r.config.ClusterUID)
	if err != nil {
		return fmt.Errorf("resolve kubernetes cluster connection: %w", err)
	}

	workloads, err := disc.ListWorkloads(ctx, client, r.config.Namespaces, r.resolveTimeout(), log)
	if err != nil {
		return fmt.Errorf("list kubernetes workloads: %w", err)
	}

	rows := make([]disc.SuggestedCheck, 0, len(workloads))
	for i := range workloads {
		rows = append(rows, buildKubernetesRows(r.config.ClusterUID, &workloads[i])...)
	}

	log.InfoContext(ctx, "Kubernetes discovery complete",
		"workload_count", len(workloads),
		"check_count", len(rows),
		"org_uid", orgUID,
		"job_uid", jobUID,
	)

	return disc.UpsertDiscoveredChecks(ctx, jctx.DB, orgUID, jobUID, models.DiscoverySourceKubernetes, rows, log)
}

// resolveTimeout parses the configured overall timeout, falling back to 0 (which
// the engine treats as its own default) when unset or unparseable.
func (r *KubernetesDiscoveryJobRun) resolveTimeout() time.Duration {
	if r.config.Timeout == "" {
		return 0
	}

	d, err := time.ParseDuration(r.config.Timeout)
	if err != nil {
		return 0
	}

	return d
}

// credsFrom returns the credentials service from the job context, or nil when
// services are not wired (the resolver tolerates a nil creds in the
// plaintext-fallback path).
func credsFrom(jctx *jobdef.JobContext) credentials.Service {
	if jctx.Services == nil {
		return nil
	}

	return jctx.Services.Credentials
}

// buildKubernetesRows turns one discovered workload into its group of
// suggested-check rows, carrying the cluster UID the promoted checks reference.
func buildKubernetesRows(clusterUID string, workload *disc.DiscoveredWorkload) []disc.SuggestedCheck {
	endpoints := make([]disc.KubernetesEndpoint, 0, len(workload.Endpoints))
	for i := range workload.Endpoints {
		ep := &workload.Endpoints[i]
		endpoints = append(endpoints, disc.KubernetesEndpoint{
			Address: ep.Address,
			Port:    ep.Port,
			Scheme:  ep.Scheme,
			Source:  ep.Source,
		})
	}

	return disc.SuggestKubernetesChecks(&disc.KubernetesSuggestInput{
		UID:               workload.UID,
		ClusterUID:        clusterUID,
		Kind:              workload.Kind,
		Namespace:         workload.Namespace,
		Name:              workload.Name,
		Images:            workload.Images,
		DesiredReplicas:   workload.DesiredReplicas,
		ReadyReplicas:     workload.ReadyReplicas,
		AvailableReplicas: workload.AvailableReplicas,
		Endpoints:         endpoints,
	})
}
