package checkkubernetes

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// reasonProgressDeadlineExceeded is the Deployment Progressing-condition reason
// the controller sets when a rollout exceeds its progress deadline (a stuck
// rollout). It is a stable Kubernetes API string; we match on it without
// importing the kubernetes core package (not vendored).
const reasonProgressDeadlineExceeded = "ProgressDeadlineExceeded"

// ClientsetResolver resolves a stored cluster connection UID to an
// authenticated clientset. Implementations own the DB lookup and the
// credentials decryption; the checker stays free of those concerns so this
// package is importable from unit tests without a database. The resolver is
// wired by the API server at startup (see internal/app/server.go).
type ClientsetResolver func(ctx context.Context, clusterUID string) (kubernetes.Interface, error)

// ClientsetResolverFunc is the active resolver. Production wires it once at
// startup; tests should prefer WithResolver(ctx, …) so parallel tests don't
// race over a single global. It is a package-level variable (not a checker
// field) so the registry-built checker (a zero-valued struct) still has access
// without plumbing services through the checkerdef.Checker interface — the same
// indirection checkfreeboxline uses.
//
//nolint:gochecknoglobals // Mirrors the checkfreeboxline.ConnectionResolverFunc pattern.
var ClientsetResolverFunc ClientsetResolver

// resolverCtxKeyType makes the context-key unique within the binary.
type resolverCtxKeyType struct{}

//nolint:gochecknoglobals // sentinel value for context key
var resolverCtxKey = resolverCtxKeyType{}

// WithResolver returns a context carrying the given resolver, taking precedence
// over the package-level ClientsetResolverFunc. Intended for tests.
func WithResolver(ctx context.Context, resolver ClientsetResolver) context.Context {
	return context.WithValue(ctx, resolverCtxKey, resolver)
}

// resolverFromContext returns the per-context resolver if set, otherwise the
// package-level ClientsetResolverFunc.
func resolverFromContext(ctx context.Context) ClientsetResolver {
	if v, ok := ctx.Value(resolverCtxKey).(ClientsetResolver); ok && v != nil {
		return v
	}

	return ClientsetResolverFunc
}

// ErrResolverNotConfigured is returned when Execute is called without a
// ClientsetResolver wired up — typically a unit-test misconfiguration.
var ErrResolverNotConfigured = errors.New("kubernetes: ClientsetResolver is not set")

// KubernetesChecker implements checkerdef.Checker for the `kubernetes` type.
type KubernetesChecker struct{}

// Type returns the check type identifier.
func (c *KubernetesChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeKubernetes
}

// Validate runs config-only validation. Networking is reserved for Execute.
func (c *KubernetesChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &KubernetesConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = cfg.Kind + " " + cfg.Namespace + "/" + cfg.Name
	}

	if spec.Slug == "" {
		spec.Slug = "k8s-" + cfg.Namespace + "-" + cfg.Name
	}

	return nil
}

// workloadStatus is the normalized replica view extracted from a Deployment or
// ReplicaSet, so the up/warning/down decision is kind-agnostic.
type workloadStatus struct {
	desired     int32
	ready       int32
	available   int32
	updated     int32
	unavailable int32
	images      []string
	conditions  []conditionView
	// stuckRollout is true when a Deployment's Progressing condition reports
	// ProgressDeadlineExceeded (a stuck rollout). Always false for ReplicaSets.
	stuckRollout bool
}

// conditionView is a compact, JSON-friendly view of a workload condition.
type conditionView struct {
	Type   string `json:"type"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Execute resolves the clientset, fetches the workload, and reports replica
// health per the spec's Q1 rule.
func (c *KubernetesChecker) Execute(
	ctx context.Context, config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*KubernetesConfig](config)
	if err != nil {
		return nil, err
	}

	resolver := resolverFromContext(ctx)
	if resolver == nil {
		return nil, ErrResolverNotConfigured
	}

	ctx, cancel := context.WithTimeout(ctx, cfg.resolveTimeout())
	defer cancel()

	start := time.Now()

	clientset, err := resolver(ctx, cfg.ClusterUID)
	if err != nil {
		// Connection/auth/credential failure → error (a misconfiguration, not a
		// "the workload is down" signal). Mirrors checkdocker's createClient
		// branch.
		return errorResult(start, fmt.Errorf("resolve cluster connection: %w", err)), nil
	}

	status, err := fetchWorkloadStatus(ctx, clientset, cfg)
	if err != nil {
		return classifyFetchError(ctx, err, start), nil
	}

	return buildResult(cfg, status, start), nil
}

// fetchWorkloadStatus loads the workload and normalizes its replica view.
func fetchWorkloadStatus(
	ctx context.Context, clientset kubernetes.Interface, cfg *KubernetesConfig,
) (*workloadStatus, error) {
	switch cfg.Kind {
	case KindDeployment:
		dep, err := clientset.AppsV1().Deployments(cfg.Namespace).Get(ctx, cfg.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		return deploymentStatus(dep), nil
	case KindReplicaSet:
		rs, err := clientset.AppsV1().ReplicaSets(cfg.Namespace).Get(ctx, cfg.Name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}

		return replicaSetStatus(rs), nil
	default:
		// Unreachable: Validate rejects other kinds before Execute.
		return nil, fmt.Errorf("unsupported workload kind %q", cfg.Kind)
	}
}

// deploymentStatus normalizes a Deployment's spec/status into a workloadStatus.
func deploymentStatus(dep *appsv1.Deployment) *workloadStatus {
	ws := &workloadStatus{
		desired:     derefReplicas(dep.Spec.Replicas),
		ready:       dep.Status.ReadyReplicas,
		available:   dep.Status.AvailableReplicas,
		updated:     dep.Status.UpdatedReplicas,
		unavailable: dep.Status.UnavailableReplicas,
		images:      containerImages(dep.Spec.Template.Spec.Containers),
	}

	for _, cond := range dep.Status.Conditions {
		ws.conditions = append(ws.conditions, conditionView{
			Type:   string(cond.Type),
			Status: string(cond.Status),
			Reason: cond.Reason,
		})

		if cond.Type == appsv1.DeploymentProgressing &&
			cond.Status == corev1.ConditionFalse &&
			cond.Reason == reasonProgressDeadlineExceeded {
			ws.stuckRollout = true
		}
	}

	return ws
}

// replicaSetStatus normalizes a ReplicaSet's spec/status. ReplicaSets have no
// Progressing condition, so stuckRollout is never set.
func replicaSetStatus(rs *appsv1.ReplicaSet) *workloadStatus {
	return &workloadStatus{
		desired:   derefReplicas(rs.Spec.Replicas),
		ready:     rs.Status.ReadyReplicas,
		available: rs.Status.AvailableReplicas,
		updated:   rs.Status.FullyLabeledReplicas,
		images:    containerImages(rs.Spec.Template.Spec.Containers),
	}
}

// buildResult applies the spec's Q1 up/warning/down rule and assembles the
// Result with outputs + metrics.
func buildResult(
	cfg *KubernetesConfig, ws *workloadStatus, start time.Time,
) *checkerdef.Result {
	output := map[string]any{
		"namespace":  cfg.Namespace,
		"kind":       cfg.Kind,
		"name":       cfg.Name,
		"images":     ws.images,
		"conditions": ws.conditions,
	}

	metrics := map[string]any{
		"desiredReplicas":     ws.desired,
		"readyReplicas":       ws.ready,
		"availableReplicas":   ws.available,
		"updatedReplicas":     ws.updated,
		"unavailableReplicas": ws.unavailable,
		"query_time_ms":       durationMs(time.Since(start)),
	}

	status, note := classifyReplicaHealth(ws)
	if note != "" {
		output[checkerdef.OutputKeyError] = note
	}

	return &checkerdef.Result{
		Status:   status,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

// classifyReplicaHealth implements the Q1 decision table:
//
//   - down: stuck rollout (ProgressDeadlineExceeded), or readyReplicas == 0
//     with desiredReplicas > 0;
//   - warning: 0 < ready < desired (mid-rollout / partially degraded), or
//     desired == 0 (intentionally scaled to zero — surfaced, not paged);
//   - up: ready == desired and desired > 0.
//
// The returned note is a human-readable reason for non-up statuses.
func classifyReplicaHealth(ws *workloadStatus) (checkerdef.Status, string) {
	if ws.stuckRollout {
		return checkerdef.StatusDown, "rollout stuck: " + reasonProgressDeadlineExceeded
	}

	if ws.desired == 0 {
		return checkerdef.StatusWarning, "workload is scaled to zero (0 desired replicas)"
	}

	if ws.ready == 0 {
		return checkerdef.StatusDown, fmt.Sprintf("no ready replicas (0/%d ready)", ws.desired)
	}

	if ws.ready < ws.desired {
		return checkerdef.StatusWarning, fmt.Sprintf("partially ready (%d/%d ready)", ws.ready, ws.desired)
	}

	return checkerdef.StatusUp, ""
}

// classifyFetchError maps a workload Get error to a Result. NotFound → down
// (the workload no longer exists, per Q1); deadline → timeout; everything else
// → down. Unlike checkdocker's handleInspectError (which folds everything to
// down without a NotFound branch), we add the explicit IsNotFound check.
func classifyFetchError(ctx context.Context, err error, start time.Time) *checkerdef.Result {
	if apierrors.IsNotFound(err) {
		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  map[string]any{"query_time_ms": durationMs(time.Since(start))},
			Output:   map[string]any{checkerdef.OutputKeyError: "workload not found"},
		}
	}

	if ctx.Err() != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Metrics:  map[string]any{"query_time_ms": durationMs(time.Since(start))},
			Output:   map[string]any{checkerdef.OutputKeyError: "connection timeout"},
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Metrics:  map[string]any{"query_time_ms": durationMs(time.Since(start))},
		Output:   map[string]any{checkerdef.OutputKeyError: "workload fetch failed: " + err.Error()},
	}
}

// errorResult builds a StatusError result (used for client/auth/resolver
// failures that are misconfigurations rather than workload-down signals).
func errorResult(start time.Time, err error) *checkerdef.Result {
	return &checkerdef.Result{
		Status:   checkerdef.StatusError,
		Duration: time.Since(start),
		Output:   map[string]any{checkerdef.OutputKeyError: err.Error()},
	}
}

// derefReplicas returns the desired replica count, treating a nil pointer as 0
// (the API server defaults spec.replicas to 1, but a nil guard is cheap).
func derefReplicas(r *int32) int32 {
	if r == nil {
		return 0
	}

	return *r
}

// containerImages collects the container images from a pod template, for the
// output. Preallocated since the count is known.
func containerImages(containers []corev1.Container) []string {
	images := make([]string, 0, len(containers))
	for i := range containers {
		images = append(images, containers[i].Image)
	}

	return images
}

func durationMs(duration time.Duration) float64 {
	return float64(duration.Microseconds()) / microsecondsPerMilli
}
