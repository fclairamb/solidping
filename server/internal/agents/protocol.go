package agents

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// ProtocolVersion is announced in the `hello` frame so agents can detect
// incompatible server upgrades and force a clean reconnect.
const ProtocolVersion = 1

// Client -> server frame types.
const (
	MsgTypeEnroll = "enroll"
	MsgTypeClaim  = "claim"
	MsgTypeResult = "result"
)

// Server -> client frame types.
const (
	MsgTypeHello         = "hello"
	MsgTypeEnrolled      = "enrolled"
	MsgTypeJobs          = "jobs"
	MsgTypeAck           = "ack"
	MsgTypeError         = "error"
	MsgTypeJobsAvailable = "jobs-available"
)

// ClientFrame is the envelope every agent->server frame decodes into. Fields
// irrelevant to the frame's type are left zero.
type ClientFrame struct {
	Type string `json:"type"`
	// ID is the correlation id echoed on the response frame.
	ID string `json:"id,omitempty"`

	// enroll
	Name             string `json:"name,omitempty"`
	Ed25519PublicKey string `json:"ed25519PublicKey,omitempty"`
	X25519PublicKey  string `json:"x25519PublicKey,omitempty"`

	// claim
	MaxJobs  int    `json:"maxJobs,omitempty"`
	CheckUID string `json:"checkUid,omitempty"`
	// EgressIPv4 / EgressIPv6 are the agent's self-probed egress families
	// (spec 2026-08-15-11), reported on the claim frame because that is the
	// agent's real heartbeat — WSBackend.Heartbeat is a no-op and the server
	// refreshes liveness from frames. Pointers, and OPTIONAL ON BOTH SIDES: an
	// older agent omits them and the server leaves the columns null, which
	// means "unknown" and must never be rendered as "no IPv6". That is what
	// makes the agent/server rollout order irrelevant.
	//
	// This is advertised capability — a hint for the region picker. It never
	// gates execution; the per-run egress pre-flight remains the authority.
	EgressIPv4 *bool `json:"egressIpv4,omitempty"`
	EgressIPv6 *bool `json:"egressIpv6,omitempty"`

	// result
	JobUID   string         `json:"jobUid,omitempty"`
	Status   int            `json:"status,omitempty"`
	Duration float32        `json:"duration,omitempty"`
	Metrics  map[string]any `json:"metrics,omitempty"`
	Output   map[string]any `json:"output,omitempty"`
	// ExecStart is the agent's wall-clock instant the outbound probe began. It
	// is the ONE scheduling input the server cannot derive from the result, and
	// it feeds the server-side delay EWMA (spec 2026-07-27-01 Decisions).
	// Optional on both sides: an older agent omits it and simply contributes no
	// delay sample. Agent clock skew is harmless here — the delay EWMA is
	// telemetry and never steers claim order or lanes.
	ExecStart *time.Time `json:"execStart,omitempty"`
}

// AgentJob is the wire shape of one dispatched job: the public config plus the
// region-sealed credential blob shipped VERBATIM (the server never decrypts on
// this path), and just enough scheduling metadata for the agent's worker loop
// (phase-locked rescheduling, cost-aware ordering).
type AgentJob struct {
	UID          string         `json:"uid"`
	CheckUID     string         `json:"checkUid"`
	Type         string         `json:"type"`
	Config       map[string]any `json:"config,omitempty"`
	ConfigSealed *string        `json:"configSealed,omitempty"`
	Region       *string        `json:"region,omitempty"`
	Period       string         `json:"period"`
	ScheduledAt  *time.Time     `json:"scheduledAt,omitempty"`
	CostEWMAMs   float64        `json:"costEwmaMs,omitempty"`
	Lane         uint8          `json:"lane,omitempty"`
	// CheckPeriod / CheckRegions carry the attached check's schedule identity
	// so the agent's phase-locked rescheduling matches the server's.
	CheckPeriod  string   `json:"checkPeriod,omitempty"`
	CheckRegions []string `json:"checkRegions,omitempty"`
	// Tunnel carries the sealed SSH-tunnel endpoint for a job whose check dials
	// its probe through an SSH check (`tunnelCheckUid`). Assembled per claim from
	// the live SSH check row (spec 2026-07-18-07) and nil for the common
	// untunneled job. Like the job's own config, the SSH check's credentials
	// cross ONLY as its region-sealed envelope, never as config_private.
	Tunnel *AgentJobTunnel `json:"tunnel,omitempty"`
}

// AgentJobTunnel is the SSH-tunnel endpoint attached to a dispatched job. It
// carries the referenced SSH check's PUBLIC config plus its region-sealed
// credential envelope shipped VERBATIM — the same zero-new-exposure posture as
// AgentJob.ConfigSealed. config_private is DELIBERATELY absent from this shape:
// the server never decrypts on the agent path, and the symmetric envelope must
// never leave the server. The agent unseals ConfigSealed with its own identity
// (it is a recipient by construction — the SSH check is allocated to the
// agent's region) and merges the secrets over Config to build the dialer.
type AgentJobTunnel struct {
	// CheckUID is the SSH check's uid, the resolver key the agent's worker loop
	// looks the unsealed snapshot up by (`tunnelCheckUid` in the job's config).
	CheckUID string `json:"checkUid"`
	// Config is the SSH check's PUBLIC config only (host, port, timeout,
	// expected_fingerprint, username) — never a secret value.
	Config map[string]any `json:"config,omitempty"`
	// ConfigSealed is the SSH check's region-sealed (age X25519 v2) envelope,
	// shipped verbatim. nil when the SSH check has no sealed secrets (e.g.
	// plaintext self-hosted fallback), in which case Config alone must carry the
	// credentials.
	ConfigSealed *string `json:"configSealed,omitempty"`
}

// ServerFrame is the envelope for every server->agent frame.
type ServerFrame struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`

	// hello / enrolled
	Protocol    int    `json:"protocol,omitempty"`
	ServerTime  string `json:"serverTime,omitempty"`
	AgentUID    string `json:"agentUid,omitempty"`
	WorkerUID   string `json:"workerUid,omitempty"`
	Region      string `json:"region,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`

	// jobs
	Jobs []AgentJob `json:"jobs,omitempty"`
	// RetryInMs, on a jobs frame, tells the agent how long until the next job
	// in its scope becomes claimable (0/absent = none known within the
	// server's hint horizon). The agent's fetcher sleeps on it instead of its
	// flat fallback poll, so sub-minute check periods hold on an idle agent.
	// Optional on both sides: old agents ignore it, old servers omit it.
	RetryInMs int64 `json:"retryInMs,omitempty"`

	// ack
	NextScheduledAt *time.Time `json:"nextScheduledAt,omitempty"`

	// error
	Code  string `json:"code,omitempty"`
	Title string `json:"title,omitempty"`
}

// ToAgentJob converts a claimed CheckJob row into its wire shape. ConfigPrivate
// (the v1 symmetric envelope) is deliberately NOT mapped: the server-side
// decrypt-and-merge that the cloud dispatch path performs must never happen on
// the agent path, and the envelope itself must never leave the server.
func ToAgentJob(job *models.CheckJob) AgentJob {
	out := AgentJob{
		UID:          job.UID,
		CheckUID:     job.CheckUID,
		Type:         job.Type,
		Config:       job.Config,
		ConfigSealed: job.ConfigSealed,
		Region:       job.Region,
		Period:       time.Duration(job.Period).String(),
		ScheduledAt:  job.ScheduledAt,
		CostEWMAMs:   job.CostEWMAMs,
		Lane:         job.Lane,
	}

	if job.Check != nil {
		out.CheckPeriod = time.Duration(job.Check.Period).String()
		out.CheckRegions = job.Check.Regions
	}

	return out
}

// ToCheckJob reconstructs the worker-loop model from the wire shape
// (agent side). The attached Check carries only what phase-locked
// rescheduling needs.
func (j *AgentJob) ToCheckJob() *models.CheckJob {
	period, _ := time.ParseDuration(j.Period)

	job := &models.CheckJob{
		UID:          j.UID,
		CheckUID:     j.CheckUID,
		Type:         j.Type,
		Config:       models.JSONMap(j.Config),
		ConfigSealed: j.ConfigSealed,
		Region:       j.Region,
		Period:       timeutils.Duration(period),
		ScheduledAt:  j.ScheduledAt,
		CostEWMAMs:   j.CostEWMAMs,
		Lane:         j.Lane,
	}

	if j.CheckPeriod != "" {
		checkPeriod, err := time.ParseDuration(j.CheckPeriod)
		if err == nil {
			job.Check = &models.Check{
				UID:     j.CheckUID,
				Type:    j.Type,
				Period:  timeutils.Duration(checkPeriod),
				Regions: j.CheckRegions,
			}
		}
	}

	return job
}
