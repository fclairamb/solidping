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
	// JobTypeIncidentAckNotice tells the PEOPLE who were paged for an incident
	// that a teammate has taken it. Exact counterpart of
	// JobTypeIncidentResolutionNotice, and it exists for the same reason: the
	// escalation step is the only thing that ever reaches a person contact, and
	// it stops firing the moment the incident is acknowledged — so without this
	// job everyone who was woken up is left assuming the page is still
	// unclaimed. V1 covers Telegram (spec 2026-08-24-01).
	JobTypeIncidentAckNotice JobType = "incident_ack_notice"
	// JobTypeIncidentUnackNotice RETRACTS that with the same people. An
	// acknowledgment stops the escalation that was waking them, so the ack
	// notice is what tells them to stand down — and without this job nothing
	// ever tells them to stand back up. Withdrawing the acknowledgment leaves
	// four people believing an incident is owned when it is not, which is the
	// exact wrong belief acknowledgments exist to prevent
	// (spec 2026-08-28-07, superseding 2026-08-24-01's "unack is silent").
	JobTypeIncidentUnackNotice JobType = "incident_unack_notice"
	// JobTypeIncidentCommentNotice forwards ONE incident comment to the people
	// the escalation policy paged. Channel connections have received comments
	// since the feature shipped; person contacts never did, so someone woken by
	// a Telegram page who is on none of the check's channels got the page, the
	// ack notice and the resolution notice — and never a word of the discussion
	// in between. Per comment, immediately, no batching (spec 2026-08-28-07).
	JobTypeIncidentCommentNotice JobType = "incident_comment_notice"
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

	// JobTypeEventsCleanup enforces audit retention on the events table
	// (spec 2026-08-21-09): a daily, self-rescheduling, batched delete of rows
	// older than audit.retention_days (default 365). Before this the table had
	// no retention at all, which stopped being survivable once every login and
	// config change started landing in it.
	JobTypeEventsCleanup JobType = "events_cleanup"
	// JobTypeDemoCleanup sweeps the public live demo (spec 2026-09-06-02):
	// expired visitor-created checks are deleted through the checks service,
	// and the demo identity (password, flags, memberships, entitlements,
	// session cap) is reconciled back to what the startup job provisioned.
	// Global, self-rescheduling every 30 minutes, a no-op when the demo is off.
	JobTypeDemoCleanup JobType = "demo_cleanup"
	// JobTypeSupportCleanup purges closed support threads past the retention
	// window (spec 2026-08-22-02). Message bodies are personal data.
	JobTypeSupportCleanup JobType = "support_cleanup"
	// JobTypeCustomDomainVerify re-runs the ownership (TXT) check for every
	// status page with a custom domain, clearing verification after repeated
	// failures (domain release/takeover protection). Global, self-rescheduling
	// every 6h (spec 2026-07-22-01).
	JobTypeCustomDomainVerify JobType = "custom_domain_verify"
	// JobTypeAbandonedResultReaper periodically finalizes raw results left in
	// ResultStatusCreated (most visibly CreateCheck's one-time "Check created"
	// marker) once they are well past any plausible execution window for their
	// check: each is flipped to a terminal error carrying Abandoned=true,
	// which keeps it out of availability math while still recording that an
	// attempt happened. Deliberately does NOT touch ResultStatusRunning, which
	// heartbeat checks use as a legitimate long-lived status. Global,
	// self-rescheduling (spec 2026-08-18-03).
	JobTypeAbandonedResultReaper JobType = "abandoned_result_reaper"
	// JobTypeIncidentPublish is the debounce timer of the incident
	// auto-publication pipeline (spec 2026-08-19-08): scheduled at
	// now + status_pages.auto_publish_delay_seconds when an incident opens, it
	// re-evaluates eligibility at FIRE time and only then makes the incident
	// visible on the status page. That re-check is the whole point — an
	// incident that resolved inside the delay never reaches the public page.
	JobTypeIncidentPublish JobType = "incident_publish"
	// JobTypeAgentGC retires platform-operated ("system") agents that stopped
	// reporting: a fly.io fleet enrolls on boot with a per-machine keypair, so
	// every machine replacement leaves a dead agent row (and its workers row)
	// behind. It also prunes consumed reconnect nonces. Org agents are
	// user-managed and never touched. Global, self-rescheduling every 6h
	// (spec 2026-07-27-01).
	JobTypeAgentGC JobType = "agent_gc"
	// JobTypeUptimeReport emits the scheduled uptime-report digests
	// (spec 2026-08-20-01). Global and self-rescheduling hourly; each run
	// looks for schedules whose weekly/monthly period has just closed in
	// their own timezone. Multi-replica safety comes from the conditional
	// last_period_start claim in MarkReportScheduleRun, not from a leader.
	//
	// Deliberately absent from publiclyCreatableJobTypes: it sends mail to
	// arbitrary stored addresses, so a public creation endpoint for it would
	// be a spam primitive with extra steps.
	JobTypeUptimeReport JobType = "uptime_report"
	// JobTypeSLOBurnEval evaluates every enabled SLO burn-rate alert policy
	// (spec 2026-08-21-08). Global and self-rescheduling every minute: a burn
	// alert whose whole point is "you are spending the month in an afternoon"
	// is worthless at hourly resolution.
	//
	// Deliberately absent from publiclyCreatableJobTypes — it opens incidents
	// and therefore pages people.
	JobTypeSLOBurnEval JobType = "slo_burn_eval"
	// JobTypePlatformWatchdog is the hourly internal watchdog (spec
	// 2026-08-24-10): it evaluates the platform's own vitals — dark regions
	// with assigned work, a collapse in fleet execution rate, active incidents
	// frozen because their check stopped running — and reports the state
	// TRANSITIONS to a configured operator list through those operators' own
	// notification routes.
	//
	// It exists because every other alerting path in solidping is check-level,
	// which means the worst failure mode of a monitoring product (going blind)
	// is exactly the one that produces zero signal. Self-rescheduling like the
	// snooze sweep; never publicly creatable — it reports on the instance, not
	// on an org.
	JobTypePlatformWatchdog JobType = "platform_watchdog"
	// JobTypeOperatorNotice delivers ONE instance-level operator notice — a
	// captured support message, a new user registration — to the super admins
	// subscribed to that event in the `operator_notifications` system
	// parameter, through each of their own notification routes.
	//
	// It is a job rather than an inline send because both raisers are on a
	// latency-critical path: a support webhook answering a provider under a
	// deadline, and a signup completing. Neither may fail or slow down because
	// Telegram is down. Never publicly creatable — its payload is free text
	// delivered to super admins, which is exactly what must not be reachable
	// from POST /orgs/:org/jobs.
	JobTypeOperatorNotice JobType = "operator_notice"
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
