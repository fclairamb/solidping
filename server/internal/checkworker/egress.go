package checkworker

import (
	"context"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/egress"
)

// newEgressGuard builds the egress guard of the check workers in this process
// (spec 2026-09-25-19). It is built from THIS process's config because this is
// the process that dials: a deported agent (private location) allows private
// targets by default, a SaaS shared worker refuses them, a self-hosted worker
// allows them — see config.Config.EgressAllowsPrivateTargets.
//
// Resolution goes through checkerdef.LookupIPAddr so a guarded check keeps the
// short-attempt DNS retry every other checker path has.
func newEgressGuard(cfg *config.Config, logger *slog.Logger) *egress.Guard {
	allow := cfg.EgressAllowsPrivateTargets()

	logger.Info("Egress policy for check targets",
		"allow_private_targets", allow,
		"source", cfg.EgressPolicySource(),
		"env", egress.EnvAllowPrivate,
		"parameter", egress.ParamAllowPrivate)

	return egress.New(allow, egress.WithLookup(checkerdef.LookupIPAddr))
}

// EgressGuard returns the worker's egress policy, for the process's other
// outbound probes (the agent's path traces).
func (r *CheckWorker) EgressGuard() *egress.Guard { return r.egressGuard }

// withEgress puts the worker's guard and a fresh denial recorder on an
// execution context. It must run before the SSH tunnel is set up: the
// connection to the bastion is itself an outbound connection to a
// user-chosen host.
func (r *CheckWorker) withEgress(ctx context.Context) (context.Context, *egress.Recorder) {
	ctx = egress.WithGuard(ctx, r.egressGuard)

	return egress.WithRecorder(ctx)
}

// applyEgressDenial logs and applies the refusal an execution recorded, if
// any. The resolved address is logged here, server-side only: the user-facing
// message deliberately never carries it (see egress.DeniedError.Error).
func (r *CheckWorker) applyEgressDenial(
	ctx context.Context, checkJob *models.CheckJob, result *checkerdef.Result, denials *egress.Recorder,
) {
	denied := denials.Denied()
	if denied == nil {
		return
	}

	r.logger.InfoContext(ctx, "Check target refused by the egress policy",
		"check_uid", checkJob.CheckUID,
		"organization_uid", checkJob.OrganizationUID,
		"host", denied.Host,
		"resolved_ip", denied.IP.String())

	applyEgressDenial(result, denied)
}

// applyEgressDenial rewrites the result of an execution the egress policy
// refused somewhere, whatever the checker made of the dial error: a wrapped
// "request failed: …", a DOWN, a script that caught the exception.
//
// The outcome is uniform on every check type: StatusError (a configuration
// this worker will never run, not an outage of the target), the policy's own
// message naming the operator switch, an `egress_denied` marker, and no
// network-failure endpoint, so no path trace is ever sent towards the refused
// address. A captured failure response cannot exist (nothing was connected),
// but it is dropped too so the rule does not depend on that.
func applyEgressDenial(result *checkerdef.Result, denied *egress.DeniedError) {
	if result == nil || denied == nil {
		return
	}

	result.Status = checkerdef.StatusError

	if result.Output == nil {
		result.Output = map[string]any{}
	}

	result.Output[checkerdef.OutputKeyError] = denied.Error()
	result.Output[egress.OutputKeyDenied] = true

	if result.Diagnostics != nil {
		result.Diagnostics.NetworkFailure = nil
		result.Diagnostics.FailureResponse = nil
	}
}
