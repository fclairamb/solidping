package checkworker

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

const microsPerMilli = 1000.0

// activeTunnel is the per-execution state of a tunneled check: the dialer the
// checker probes through, the SSH client to tear down afterwards, and how long
// establishing it took.
//
// A nil *activeTunnel means "this check is not tunneled" and every method below
// is a no-op on it, so executeJob's untunneled path stays free of branches.
type activeTunnel struct {
	checkUID string
	dialer   *sshtunnel.Dialer
	closer   io.Closer
	setup    time.Duration
}

// setupTunnel establishes the tunnel for a job that references one, or returns
// (nil, nil) for the overwhelmingly common untunneled job.
//
// Passive check types never reach here — executeJob dispatches them earlier —
// which is correct: they make no outbound request, so there is nothing to
// tunnel.
func (r *CheckWorker) setupTunnel(ctx context.Context, checkJob *models.CheckJob) (*activeTunnel, error) {
	tunnelCheckUID, ok := checkerdef.TunnelCheckUIDFrom(checkJob.Config)
	if !ok {
		return nil, nil //nolint:nilnil // (nil, nil) is "not tunneled", the documented common case
	}

	start := time.Now()
	dialer, closer, err := sshtunnel.Resolve(ctx, checkJob.OrganizationUID, tunnelCheckUID)
	elapsed := time.Since(start)

	// The failed tunnel is still reported with its setup cost — how long a
	// broken bastion took to fail is exactly what an operator wants to see.
	tunnel := &activeTunnel{checkUID: tunnelCheckUID, setup: elapsed}

	if err != nil {
		return tunnel, err
	}

	tunnel.dialer = dialer
	tunnel.closer = closer

	return tunnel, nil
}

// close tears the SSH client down. v1 dials per execution by design; pooling a
// health-checked client per (tunnel, worker) is a follow-up.
func (t *activeTunnel) close() {
	if t == nil || t.closer == nil {
		return
	}

	_ = t.closer.Close()
}

// annotate records tunnel_setup_ms on a successful execution's metrics and
// re-labels a result whose real cause was the bastion refusing to forward
// (as opposed to the target refusing the connection, which the checker already
// reports correctly and which must stay a plain target failure).
func (t *activeTunnel) annotate(result *checkerdef.Result) {
	if t == nil || result == nil {
		return
	}

	if result.Metrics == nil {
		result.Metrics = map[string]any{}
	}

	result.Metrics[checkerdef.MetricKeyTunnelSetupMs] = float64(t.setup.Microseconds()) / microsPerMilli

	if t.dialer == nil {
		return
	}

	if forwardErr := t.dialer.TunnelFailure(); forwardErr != nil && result.Status != checkerdef.StatusUp {
		result.Status = checkerdef.StatusError
		result.Output = tunnelFailureOutput(t.checkUID, forwardErr, result.Output)
	}
}

// saveTunnelFailureResult writes the distinct result for a check whose tunnel
// could not be established. It is deliberately different from a target failure:
// the output carries a `tunnel_failed` marker plus the referenced SSH check, so
// an operator (and, later, dependency-aware alert suppression) can tell "the
// bastion is broken" from "the service behind it is down".
func (r *CheckWorker) saveTunnelFailureResult(
	ctx context.Context, checkJob *models.CheckJob, tunnelErr error, tunnel *activeTunnel,
) error {
	saveCtx := ctx //nolint:contextcheck // Conditional context assignment mirrors saveErrorResult
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		saveCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
	}

	metrics := map[string]any{}
	tunnelCheckUID := ""

	if tunnel != nil {
		metrics[checkerdef.MetricKeyTunnelSetupMs] = float64(tunnel.setup.Microseconds()) / microsPerMilli
		tunnelCheckUID = tunnel.checkUID
	}

	result := checkerdef.Result{
		Status:  checkerdef.StatusError,
		Metrics: metrics,
		Output:  tunnelFailureOutput(tunnelCheckUID, tunnelErr, nil),
	}

	region := ""
	if checkJob.Region != nil {
		region = *checkJob.Region
	}

	prommetrics.RecordExecution(checkJob.Type, result.Status.String(), region, checkJob.OrganizationUID, 0)

	return r.backend.SubmitResult(
		saveCtx, checkJob, r.getWorker().UID, r.buildSubmitRequest(checkJob, result, time.Now()),
	)
}

// tunnelFailureOutput builds the result output for a tunnel failure, preserving
// whatever diagnostic keys the checker had already produced.
func tunnelFailureOutput(tunnelCheckUID string, err error, base map[string]any) map[string]any {
	output := make(map[string]any, len(base)+3)
	for key, value := range base {
		output[key] = value
	}

	output[checkerdef.OutputKeyError] = fmt.Sprintf("tunnel failed: %v", err)
	output[checkerdef.OutputKeyTunnelFailed] = true

	if tunnelCheckUID != "" {
		output[checkerdef.TunnelCheckUIDConfigKey] = tunnelCheckUID
	}

	return output
}
