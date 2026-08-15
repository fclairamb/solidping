package jobdef

// JobType represents the type of a job.
type JobType string

// Supported job types.
const (
	// JobTypeSleep is a simple sleep job for testing.
	JobTypeSleep JobType = "sleep"
	// JobTypeEmail sends an email notification.
	JobTypeEmail JobType = "email"
	// JobTypeWebhook sends a webhook notification.
	JobTypeWebhook JobType = "webhook"
	// JobTypeStartup executes a startup check.
	JobTypeStartup JobType = "startup"
	// JobTypeAggregation aggregates results into hourly, daily, and monthly summaries.
	JobTypeAggregation JobType = "aggregation"
	// JobTypeStateCleanup removes expired state entries.
	JobTypeStateCleanup JobType = "state_cleanup"
	// JobTypeNotification sends incident notifications via connections.
	JobTypeNotification JobType = "notification"
	// JobTypeSnoozeSweep auto-unsnoozes incidents whose snooze window has passed.
	JobTypeSnoozeSweep JobType = "snooze_sweep"
	// JobTypeEscalationStep fires one step of an escalation policy for an incident.
	// Canceled via the same incidentUid match the notification job uses, so
	// ack/snooze/resolve drops the rest of the policy without extra wiring.
	JobTypeEscalationStep JobType = "escalation_step"
	// JobTypeIncidentResolutionNotice tells the PEOPLE who were paged for an
	// incident that it is over. Channel connections hear the whole lifecycle
	// through JobTypeNotification, but person contacts are only ever reached by
	// the escalation step, whose contract is "fire while the incident is
	// unhandled" — so without this job the on-call engineer is paged and then
	// never told the incident ended. V1 covers Telegram (spec 2026-08-14-01).
	JobTypeIncidentResolutionNotice JobType = "incident_resolution_notice"
	// JobTypeNetworkDiscovery scans a set of CIDR ranges for responsive hosts and
	// records suggested checks in the discovered_checks table (grouped by IP) for
	// operator review and promotion.
	JobTypeNetworkDiscovery JobType = "network_discovery"
	// JobTypeNetworkDiscoveryPlan is the lightweight coordinator job for a large
	// fan-out scan. It splits the requested CIDRs into ≤MaxAddresses chunks and
	// schedules one JobTypeNetworkDiscovery child per chunk, each carrying the plan
	// UID as parentJobUid. Its own UID is the scan UID shown in the UI.
	JobTypeNetworkDiscoveryPlan JobType = "network_discovery_plan"
	// JobTypeFreeboxLanDiscovery queries a paired Freebox channel's LAN browser
	// and records suggested checks in the discovered_checks table
	// (source='freebox') so they share the LAN-scan promote/dismiss UX.
	JobTypeFreeboxLanDiscovery JobType = "freebox_lan_discovery"
	// JobTypeContainerDiscovery connects to one or more Docker-compatible API
	// endpoints, lists running containers, and records them in discovered_checks
	// (source='container', one group per container) for operator review and promotion.
	JobTypeContainerDiscovery JobType = "container_discovery"
	// JobTypeKubernetesDiscovery connects to a configured Kubernetes cluster, lists
	// Deployments and bare ReplicaSets, and records them in discovered_checks
	// (source='kubernetes', one group per workload) for operator review and promotion.
	JobTypeKubernetesDiscovery JobType = "kubernetes_discovery"
	// JobTypeStuckJobReaper periodically recovers jobs left in 'running' by a
	// dead/redeployed worker: it rides the existing retry chain (retried +
	// backoff clone) until the retry cap, then 'failed' with reason
	// "stuck_timeout". Self-reschedules like snooze_sweep.
	JobTypeStuckJobReaper JobType = "stuck_job_reaper"
	// JobTypeJobsCleanup soft-deletes finished jobs older than the configured
	// window (stage 1) and hard-deletes rows soft-deleted past the grace window
	// (stage 2), so the jobs table stops growing unbounded. Global, self-
	// rescheduling daily; its own terminal rows self-clean (spec 2026-07-11-17).
	JobTypeJobsCleanup JobType = "jobs_cleanup"
	// JobTypeCustomDomainVerify re-runs the ownership (TXT) check for every
	// status page with a custom domain, clearing verification after repeated
	// failures (domain release/takeover protection). Global, self-rescheduling
	// every 6h (spec 2026-07-22-01).
	JobTypeCustomDomainVerify JobType = "custom_domain_verify"
	// JobTypeAgentGC retires platform-operated ("system") agents that stopped
	// reporting: a fly.io fleet enrolls on boot with a per-machine keypair, so
	// every machine replacement leaves a dead agent row (and its workers row)
	// behind. It also prunes consumed reconnect nonces. Org agents are
	// user-managed and never touched. Global, self-rescheduling every 6h
	// (spec 2026-07-27-01).
	JobTypeAgentGC JobType = "agent_gc"
)

// publiclyCreatableJobTypes is the allowlist of job types that may be enqueued
// through the public API (POST /api/v1/orgs/:org/jobs). It is an ALLOWLIST, not
// a blocklist: anything absent is refused, so a job type added later stays
// closed until someone deliberately opts it in here.
//
// Only `sleep` is on it. Every other type is either directly abusable from
// outside — `email` sends attacker-authored content through the org's own SMTP
// sender, `webhook` issues arbitrary server-side HTTP requests (an SSRF
// primitive) — or is internal plumbing that schedulers enqueue themselves.
// `sleep` is harmless and doubles as the endpoint's own smoke test.
//
// This governs the HTTP surface only. Internal callers (auth's transactional
// mail, testapi, every scheduler) go through jobsvc.CreateJob directly and are
// unaffected — the gate lives in jobs.Handler.CreateJob, never in the service.
//
//nolint:gochecknoglobals // static lookup table, treated as a constant.
var publiclyCreatableJobTypes = map[JobType]struct{}{
	JobTypeSleep: {},
}

// IsPubliclyCreatable reports whether a job type may be created through the
// public job-creation endpoint. Deny by default.
func IsPubliclyCreatable(jobType JobType) bool {
	_, ok := publiclyCreatableJobTypes[jobType]

	return ok
}
