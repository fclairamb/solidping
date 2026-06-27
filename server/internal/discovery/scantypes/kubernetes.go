package scantypes

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/fclairamb/solidping/server/internal/db/models"
	integrationk8s "github.com/fclairamb/solidping/server/internal/integrations/kubernetes"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// kubernetesParams is the parameter schema for a Kubernetes discovery scan. It
// is also the job config payload (the kubernetes_discovery job reads the same
// {clusterUid, namespaces, timeout} shape), so it is reused on both sides.
type kubernetesParams struct {
	ClusterUID string   `json:"clusterUid"`
	Namespaces []string `json:"namespaces,omitempty"`
	Timeout    string   `json:"timeout,omitempty"`
}

// KubernetesDefinition is the Kubernetes workload discovery type. It validates
// that the targeted cluster connection exists, belongs to the org, and is a
// kubernetes-type connection (probing it) before enqueuing the job.
type KubernetesDefinition struct{}

// Type returns the user-facing discovery type id.
func (d *KubernetesDefinition) Type() string { return "kubernetes" }

// Source returns the row source written by Kubernetes scans.
func (d *KubernetesDefinition) Source() models.DiscoverySource {
	return models.DiscoverySourceKubernetes
}

// BuildJob validates {clusterUid, namespaces?, timeout?}, probes the cluster
// connection (so an unreachable/unknown cluster surfaces before a job is queued
// that would just fail on first run), and returns the kubernetes_discovery job
// to enqueue. An unknown/wrong-org/wrong-type clusterUid returns the coded
// KUBERNETES_CLUSTER_NOT_FOUND (parallel to Freebox's FREEBOX_NOT_GRANTED).
func (d *KubernetesDefinition) BuildJob(
	ctx context.Context, deps Deps, _ string, parameters json.RawMessage,
) (string, json.RawMessage, error) {
	var params kubernetesParams

	if len(parameters) > 0 {
		if err := json.Unmarshal(parameters, &params); err != nil {
			return "", nil, NewError(CodeInvalidParameters, "invalid kubernetes scan parameters: "+err.Error())
		}
	}

	if params.ClusterUID == "" {
		return "", nil, NewError(CodeInvalidParameters, "clusterUid is required")
	}

	if deps.DBService == nil {
		return "", nil, NewError(CodeInvalidParameters, "kubernetes scan requires database access")
	}

	if err := probeCluster(ctx, deps, params.ClusterUID); err != nil {
		return "", nil, err
	}

	cfgBytes, err := json.Marshal(params)
	if err != nil {
		return "", nil, NewError(CodeInvalidParameters, "failed to encode kubernetes scan config: "+err.Error())
	}

	return string(jobdef.JobTypeKubernetesDiscovery), cfgBytes, nil
}

// probeCluster fails fast on a clusterUid that does not resolve to a usable
// kubernetes connection. ResolveClientsetByUID scopes by the connection's own
// org + type (a check/scan can only reference a connection in its own org), so a
// wrong-org or wrong-type UID resolves to ErrClusterNotFound — mapped to the
// coded KUBERNETES_CLUSTER_NOT_FOUND. ValidateConnection then performs a
// read-only /version probe so a bad credential surfaces here, not on first run.
func probeCluster(ctx context.Context, deps Deps, clusterUID string) error {
	client, err := integrationk8s.ResolveClientsetByUID(ctx, deps.DBService, deps.Creds, clusterUID)
	if err != nil {
		if errors.Is(err, integrationk8s.ErrClusterNotFound) {
			return NewError(CodeKubernetesClusterNotFound, "kubernetes cluster connection not found")
		}

		return NewError(CodeInvalidParameters, "failed to resolve kubernetes cluster connection: "+err.Error())
	}

	if _, err := integrationk8s.ValidateConnection(ctx, client); err != nil {
		return NewError(CodeInvalidParameters, "kubernetes cluster is not reachable: "+err.Error())
	}

	return nil
}
