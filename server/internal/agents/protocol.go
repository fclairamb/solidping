package agents

import (
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// ProtocolVersion is announced in the `hello` frame so agents can detect
// incompatible server upgrades and force a clean reconnect.
const ProtocolVersion = 1

// MinSupportedAgentVersion is the oldest agent release the server still
// serves in full. Raised only when a compatibility shim for older agents is
// removed, and never past a release less than 30 days old: see
// wiki/conventions/agent-compatibility.md. An agent below it is still
// accepted (the server only refuses a connection on a ProtocolVersion break);
// it is flagged "Unsupported" in the dashboard and logged once per connection.
const MinSupportedAgentVersion = "0.41.0"

// HeaderBootID carries the agent's boot ID on a signed reconnect, which has
// no frame of its own (the enroll frame carries it as BootID). See
// ClientFrame.BootID. Not covered by the reconnect signature: the worst a
// forged value does is free the agent's own leases early.
const HeaderBootID = "X-Sp-Agent-Boot-Id"

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
	// MsgTypeUploadRequest asks the agent to upload a binary capture it is
	// holding (spec 2026-08-21-05). It is the server's half of the split the
	// JSON control channel forces: the result frame carries only a marker
	// naming the agent's own cache slot, and THIS frame — emitted only when
	// that result opened or reopened an incident — names the topic to POST the
	// bytes to.
	//
	// UNSOLICITED AND UNCORRELATED: it carries no ID, expects no response on
	// the socket, and an agent that does not understand it ignores it. That is
	// what makes it safe to add to a protocol whose agents upgrade on their own
	// schedule.
	MsgTypeUploadRequest = "upload-request"
	// MsgTypeTraceRequest asks the agent to RUN an MTR-style path trace and
	// upload the resulting capture (spec 2026-08-21-10).
	//
	// It differs from MsgTypeUploadRequest in one important way: there is no
	// capture waiting in the agent's cache. The trace has not happened yet, and
	// it has to happen ON THE AGENT because the agent's path to the target is
	// the one that failed — a trace run on the master would describe a route
	// the probe never took.
	//
	// UNSOLICITED AND UNCORRELATED, exactly like the upload request: no id, no
	// response on the socket, and an agent that predates it ignores it.
	MsgTypeTraceRequest = "trace-request"
	// MsgTypeUpgrade asks the agent to restart on the server's version (spec
	// 2026-11-22-04): it stops claiming, finishes its in-flight jobs, closes
	// the socket and exits 0, so its supervisor (kubelet with
	// imagePullPolicy: Always, a compose updater) starts it again on the
	// image the server's registry now resolves `current` to.
	//
	// UNSOLICITED AND UNCORRELATED, like the two frames above: an agent that
	// predates it ignores it, which is what keeps the rollout order free.
	MsgTypeUpgrade = "upgrade"
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
	// BootID is a random ID the agent generates once per process (spec
	// 2026-11-14-03), sent here on enroll and in HeaderBootID on reconnect.
	// The server frees the agent's check-job leases when it changes: a new
	// process cannot finish the runs the old one leased. OPTIONAL: an older
	// agent omits it and its leases wait for expiry, as before.
	BootID string `json:"bootId,omitempty"`

	// claim
	MaxJobs  int    `json:"maxJobs,omitempty"`
	CheckUID string `json:"checkUid,omitempty"`
	// Capabilities is the agent's self-reported capability set — the names of
	// what it CAN do, today the egress families it self-probed (specs
	// 2026-08-15-11, 2026-08-16-02). Reported on the claim frame because that
	// is the agent's real heartbeat — WSBackend.Heartbeat is a no-op and the
	// server refreshes liveness from frames.
	//
	// OPTIONAL ON BOTH SIDES, AND THREE-STATE. `null`/absent means "not
	// reported": an older agent omits the field and the server leaves the
	// column NULL, which means "unknown" and must never be rendered as "no
	// IPv6" — that is what makes the agent/server rollout order irrelevant.
	// `[]` is a DIFFERENT answer: "I reported, and I have none of them".
	//
	// DELIBERATELY NOT `omitempty`: omitempty erases an empty non-nil slice,
	// which would make "I have none" indistinguishable from "I did not report"
	// and silently collapse the three states back to two on the wire.
	//
	// This is advertised capability — a hint for the region picker. It never
	// gates execution; the per-run egress pre-flight remains the authority.
	Capabilities []string `json:"capabilities"`
	// Version is the agent's self-reported build version (spec
	// 2026-08-19-07), populated from version.Get().Version and sent on both
	// the enroll frame and every claim. OPTIONAL, AND `omitempty` IS CORRECT
	// HERE — unlike Capabilities, a real version is never the empty string,
	// so there is no "I have none" answer that omitempty could collapse into
	// "not reported". An older agent simply omits the field; an older server
	// ignores it. Detection only: this never gates execution, throttles, or
	// disconnects an agent on a stale version.
	Version string `json:"version,omitempty"`
	// AutoUpgrade is the agent's answer to the rolling upgrade (spec
	// 2026-11-22-04), sent on the enroll frame and every claim. `false` means
	// the agent is pinned (SP_AGENT_IGNORE_VERSION_DRIFT=true): it ignores any
	// `upgrade` frame, so the server never sends one and skips it in the
	// rollout. OPTIONAL AND THREE-STATE ON THE WIRE: absent means the agent
	// predates the rolling upgrade and does not know the `upgrade` frame, so
	// the server never sends it one either (it must be upgraded by hand). Use
	// VersionPinned and PredatesAutoUpgrade rather than reading the pointer.
	AutoUpgrade *bool `json:"autoUpgrade,omitempty"`
	// Plugins are the plugin check types the agent loaded, with their schema
	// (spec 2026-10-17-05 §18). Sent on the first claim of each connection
	// only; the server stores them per org so the form and the validation of
	// those types work while the agent is offline. Optional on both sides: an
	// older agent omits it, an older server ignores it.
	Plugins []protocol.TypeManifest `json:"plugins,omitempty"`

	// result
	JobUID   string         `json:"jobUid,omitempty"`
	Status   int            `json:"status,omitempty"`
	Duration float32        `json:"duration,omitempty"`
	Metrics  map[string]any `json:"metrics,omitempty"`
	Output   map[string]any `json:"output,omitempty"`
	// Diagnostics carries the opt-in failure capture (spec 2026-08-20-01) —
	// what the probe actually received from a failing target. It rides NEXT TO
	// Output rather than inside it precisely because Output is persisted on
	// every raw result row and this payload is kilobytes; the server persists
	// it only when the result opens or reopens an incident.
	//
	// OPTIONAL AND `omitempty` ON BOTH SIDES. An older agent omits the field
	// entirely and the server simply stores no capture; a newer agent talking
	// to an older server loses nothing but the capture. That is what makes the
	// rollout order irrelevant.
	Diagnostics *checkerdef.Diagnostics `json:"diagnostics,omitempty"`
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
	// CaptureRequestedAt is set when an operator asked for an on-demand
	// screenshot of this check ("Capture now", spec 2026-09-25-34): the agent
	// forces the capture and stamps its marker OnDemand. An agent predating the
	// field ignores it and runs the job normally — no capture unless it fails.
	CaptureRequestedAt *time.Time `json:"captureRequestedAt,omitempty"`
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

// VersionPinned reports whether the agent announced it ignores upgrade frames
// (AutoUpgrade explicitly false). An absent field is not a pin.
func (f *ClientFrame) VersionPinned() bool {
	return f.AutoUpgrade != nil && !*f.AutoUpgrade
}

// PredatesAutoUpgrade reports whether the agent is older than the rolling
// upgrade protocol (no AutoUpgrade field): it ignores `upgrade` frames, so it
// is never asked and must be upgraded by hand.
func (f *ClientFrame) PredatesAutoUpgrade() bool {
	return f.AutoUpgrade == nil
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
	// NoWorkerTasks, on the identity hello, tells the agent the server refuses
	// it worker tasks (a system agent: cloud workers already run the shared
	// regions' tasks), so it never starts its task loop. Absent from old
	// servers, which keeps the loop on.
	NoWorkerTasks bool `json:"noWorkerTasks,omitempty"`
	// ReportsOnce, on the identity hello, tells the agent the server keeps its
	// capability set and version for the whole connection: the agent sends
	// them on its first claim, then the set again only when it changes.
	// Absent from old servers, so the agent keeps sending both on every claim.
	ReportsOnce bool `json:"reportsOnce,omitempty"`
	// ServerVersion is the server's build version (version.Get().Version),
	// carried on every hello (spec 2026-11-22-04). Informational: older
	// agents ignore it, older servers omit it.
	ServerVersion string `json:"serverVersion,omitempty"`

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

	// upload-request
	//
	// Topic is the attachment topic the agent must POST to. It is ALWAYS
	// server-generated from the entity the server just wrote (e.g.
	// `incidents/<uid>/screenshot`) and is NEVER echoed from anything the agent
	// sent: the topic is a storage key and an authorization subject, so letting
	// an agent name it would hand it the choice of what it is writing over.
	Topic string `json:"topic,omitempty"`
	// CaptureID names the capture in the AGENT'S OWN cache. It is echoed back
	// verbatim from the marker the agent sent, and that is fine precisely
	// because it never leaves the agent's process boundary in any meaningful
	// sense — it indexes a map in the agent's RAM, never a path, a query, or a
	// storage key.
	CaptureID string `json:"captureId,omitempty"`

	// trace-request
	//
	// Trace carries what the agent needs to run one path capture. It travels as
	// its own object rather than as more flat fields so a frame that is not a
	// trace request costs nothing on the wire.
	Trace *TraceRequestFrame `json:"trace,omitempty"`

	// upgrade
	//
	// TargetVersion is the version the server expects the agent to come back
	// on (its own). Informational: the agent does not pick an image, its
	// supervisor does.
	TargetVersion string `json:"targetVersion,omitempty"`
}

// TraceRequestFrame is the payload of MsgTypeTraceRequest.
//
// EVERY FIELD IS SERVER-CHOSEN, and Address is the load-bearing one: it is the
// IP the FAILING PROBE actually dialed, carried forward from the checker's own
// network-failure marker. Letting the agent re-resolve the hostname would let a
// round-robin record point the trace at a machine that never failed, and would
// quietly discard the check's IP-family pinning.
type TraceRequestFrame struct {
	// Host is the configured hostname, for display in the capture.
	Host string `json:"host,omitempty"`
	// Address is the IP to trace to.
	Address string `json:"address"`
	// Port is the target port. Zero for an ICMP check, which is what makes the
	// unprivileged TCP fallback unavailable there.
	Port int `json:"port,omitempty"`
	// Rounds / MaxHops / BudgetMs are the server's settings, so an operator
	// tunes one place rather than every agent.
	Rounds   int   `json:"rounds,omitempty"`
	MaxHops  int   `json:"maxHops,omitempty"`
	BudgetMs int64 `json:"budgetMs,omitempty"`
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
		// A pending "Capture now" request travels with the job so a deported
		// agent forces the capture exactly like the in-process worker.
		CaptureRequestedAt: job.CaptureRequestedAt,
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

		CaptureRequestedAt: j.CaptureRequestedAt,
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

// VersionSupported reports whether an agent's self-reported build version is at
// or above MinSupportedAgentVersion. An empty or unparsable version (never
// reported, dev build) is never "unsupported": we do not know.
func VersionSupported(agentVersion string) bool {
	return versionAtLeast(agentVersion, MinSupportedAgentVersion)
}

func versionAtLeast(agentVersion, minimum string) bool {
	canonical := "v" + strings.TrimPrefix(agentVersion, "v")
	if !semver.IsValid(canonical) {
		return true
	}

	return semver.Compare(canonical, "v"+strings.TrimPrefix(minimum, "v")) >= 0
}
