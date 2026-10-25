// Package discovery provides HTTP handlers for network discovery operations.
package discovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/dblock"
	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/discovery/scantypes"
	"github.com/fclairamb/solidping/server/internal/discoverytask"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// staleScanThreshold is how old a child network_discovery job's updated_at may be
// before the "already running" guard stops counting it as active. This is the
// pragmatic stand-in for a real lease/reaper: a single child stuck in `running`
// (e.g. a crashed worker) stops blocking new scans after this window.
const staleScanThreshold = 30 * time.Minute

// lanType is the discovery type a region-bound scan must be.
const lanType = "lan"

// Audit payload keys and bounds of discovery.scan_started (spec 2026-10-22-04).
// No key may contain private, key, token or auth: redaction drops those.
const (
	payloadScanType     = "scan_type"
	payloadRegion       = "region"
	payloadCIDRCount    = "cidr_count"
	payloadCIDRs        = "cidrs"
	payloadAddressCount = "address_count"
	payloadPortCount    = "port_count"
	auditTargetScan     = "discovery_scan"
	maxAuditCIDRs       = 32
)

var (
	// ErrCheckNotFound is returned when a discovered check is not found.
	ErrCheckNotFound = errors.New("discovered check not found")
	// ErrScanNotFound is returned when a discovery scan (plan job) is not found.
	ErrScanNotFound = errors.New("discovery scan not found")
	// ErrAlreadyRunning is returned when a discovery job is already running for the org.
	ErrAlreadyRunning = errors.New("a discovery scan is already running for this organization")
	// ErrAlreadyPromoted is returned when a check has already been promoted.
	ErrAlreadyPromoted = errors.New("discovered check already promoted")
	// ErrRegionUnavailable means the private region a row was found from no longer exists.
	ErrRegionUnavailable = errors.New("discovery region no longer available")
)

// ListChecksOptions configures which discovered checks to return.
type ListChecksOptions struct {
	JobUID   string
	Group    string
	Promoted *bool
	Sources  []models.DiscoverySource
	Limit    int
	Offset   int
}

// PromoteOverrides carries optional adjustments applied to every check created
// in a promote call (the row already carries name/slug/config).
type PromoteOverrides struct {
	Name   string  `json:"name,omitempty"`
	Period *string `json:"period,omitempty"`
}

// PromoteRequest is the request body for promoting one or more discovered checks
// into real checks in a single call.
type PromoteRequest struct {
	UIDs      []string         `json:"uids"`
	Overrides PromoteOverrides `json:"overrides,omitempty"`
}

// Service provides business logic for network discovery operations.
type Service struct {
	db         *bun.DB
	dbSvc      db.Service
	checksSvc  *checks.Service
	regionsSvc *regions.Service
	jobSvc     jobsvc.Service
	creds      credentials.Service
	isPostgres bool
	// egressGuard is the process egress guard; dockerBlocked the docker-check
	// rule (SaaS and not an agent node). Together they decide which discovery
	// types this deployment refuses (spec 2026-10-16-10).
	egressGuard   *egress.Guard
	dockerBlocked bool
	// tasks holds the chunks of agent-run scans (spec 2026-10-22-04);
	// entitlements their daily address quota. Nil entitlements is unlimited.
	tasks        *workertasks.Service
	entitlements *entitlements.Service
}

// NewService creates a new discovery service.
func NewService(
	bunDB *bun.DB,
	dbSvc db.Service,
	checksSvc *checks.Service,
	jobSvc jobsvc.Service,
	creds credentials.Service,
	egressGuard *egress.Guard,
	dockerBlocked bool,
) *Service {
	_, isPostgres := bunDB.Dialect().(*pgdialect.Dialect)

	return &Service{
		db:            bunDB,
		dbSvc:         dbSvc,
		checksSvc:     checksSvc,
		regionsSvc:    regions.NewService(dbSvc),
		jobSvc:        jobSvc,
		creds:         creds,
		isPostgres:    isPostgres,
		egressGuard:   egressGuard,
		dockerBlocked: dockerBlocked,
	}
}

// SetWorkerTasks installs the worker-task service agent-run scans cancel their
// chunks through. Set once at startup.
func (s *Service) SetWorkerTasks(tasks *workertasks.Service) {
	s.tasks = tasks
}

// SetEntitlements installs the entitlements service behind the daily
// agent-discovery address quota. Set once at startup.
func (s *Service) SetEntitlements(ent *entitlements.Service) {
	s.entitlements = ent
}

// deps returns the discovery-type dependencies of this service, carrying the
// deployment's egress policy and docker gate.
func (s *Service) deps() scantypes.Deps {
	return scantypes.Deps{
		DBService:     s.dbSvc,
		Creds:         s.creds,
		EgressGuard:   s.egressGuard,
		DockerBlocked: s.dockerBlocked,
		Regions:       s.regionsSvc,
	}
}

// AvailableTypes returns the registered discovery types this deployment
// accepts, sorted by type. Refused types (lan under an enforcing egress policy,
// container where docker checks are refused) are left out.
func (s *Service) AvailableTypes() []scantypes.Definition {
	deps := s.deps()
	all := scantypes.List()
	out := make([]scantypes.Definition, 0, len(all))

	for _, def := range all {
		if scantypes.Available(def, deps) == nil {
			out = append(out, def)
		}
	}

	return out
}

// TypeAvailability is a discovery type the org may start, and whether the
// server itself may run it (otherwise only a private region's agents can).
type TypeAvailability struct {
	Definition      scantypes.Definition
	ServerAvailable bool
}

// TypesFor returns the discovery types the org may start. `lan`, refused on
// the server under an enforcing egress policy, stays listed (ServerAvailable
// false) when the org has a private region its agents could scan from.
func (s *Service) TypesFor(ctx context.Context, orgUID string) ([]TypeAvailability, error) {
	deps := s.deps()
	all := scantypes.List()
	out := make([]TypeAvailability, 0, len(all))

	for _, def := range all {
		if scantypes.Available(def, deps) == nil {
			out = append(out, TypeAvailability{Definition: def, ServerAvailable: true})

			continue
		}

		if def.Type() != lanType {
			continue
		}

		privateRegions, err := s.regionsSvc.GetOrgCustomRegions(ctx, orgUID)
		if err != nil {
			return nil, fmt.Errorf("list private regions: %w", err)
		}

		if len(privateRegions) > 0 {
			out = append(out, TypeAvailability{Definition: def})
		}
	}

	return out, nil
}

// StartScan resolves the discovery type from the registry, validates its
// parameters (which yields the job type + config to enqueue), guards against an
// already-running scan of that resolved job type, then creates the scan job. The
// returned job's UID is the scan UID. region is the private region the scan
// runs from ("" = the server; only `lan` takes one). A coded
// *scantypes.DiscoveryError is returned for an unknown type, invalid
// parameters, or a precondition failure; ErrAlreadyRunning when a scan of the
// same type is already in flight.
func (s *Service) StartScan(
	ctx context.Context, orgUID, typ, region string, parameters json.RawMessage,
) (*models.Job, error) {
	def, ok := scantypes.Get(typ)
	if !ok {
		return nil, scantypes.NewError(scantypes.CodeUnknownType, fmt.Sprintf("unknown discovery type %q", typ))
	}

	if region != "" && def.Type() != lanType {
		return nil, scantypes.NewError(string(base.ErrorCodeValidationError), "region is only supported by lan scans")
	}

	deps := s.deps()

	jobType, jobConfig, err := def.BuildJob(ctx, deps, orgUID, region, parameters)
	if err != nil {
		return nil, err
	}

	summary := summarize(jobConfig)

	job, err := s.createScanJob(ctx, orgUID, jobType, jobConfig, summary.addresses)
	if err != nil {
		return nil, err
	}

	audit.Record(ctx, s.dbSvc, orgUID, models.EventTypeDiscoveryScanStarted, scanTarget(job), models.JSONMap{
		payloadScanType:     typ,
		payloadRegion:       region,
		payloadCIDRCount:    len(summary.cidrs),
		payloadCIDRs:        summary.cidrs[:min(len(summary.cidrs), maxAuditCIDRs)],
		payloadAddressCount: summary.addresses,
		payloadPortCount:    len(summary.ports),
	})

	return job, nil
}

// scanSummary is what the audit trail and the quota read off a scan config.
type scanSummary struct {
	cidrs     []string
	ports     []int
	addresses int
}

func summarize(jobConfig json.RawMessage) scanSummary {
	var cfg struct {
		CIDRs []string `json:"cidrs"`
		Ports []int    `json:"ports"`
	}

	_ = json.Unmarshal(jobConfig, &cfg)

	addresses, _ := disc.AddressCount(cfg.CIDRs)

	if cfg.CIDRs == nil {
		cfg.CIDRs = []string{}
	}

	return scanSummary{cidrs: cfg.CIDRs, ports: cfg.Ports, addresses: addresses}
}

// createScanJob guards against a concurrent scan, then creates the job. A
// fan-out scan (both plan types) guards per org on its plan and live chunks,
// and an agent plan also on the daily address quota: guard, quota and insert
// run in one transaction under a per-org lock (Postgres: an advisory
// transaction lock; SQLite: its single connection), so two concurrent starts
// can never both pass. Other types guard per type, then dedup in CreateJob.
func (s *Service) createScanJob(
	ctx context.Context, orgUID, jobType string, jobConfig json.RawMessage, addresses int,
) (*models.Job, error) {
	if !isPlanType(jobType) {
		if guardErr := s.checkAlreadyRunning(ctx, s.db, orgUID, jobdef.JobType(jobType)); guardErr != nil {
			return nil, guardErr
		}

		job, err := s.jobSvc.CreateJob(ctx, orgUID, jobType, jobConfig, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create discovery scan job: %w", err)
		}

		return job, nil
	}

	// Resolved before the transaction: on SQLite its single connection is
	// held by the transaction, and the cap needs no lock.
	var limit *int

	if jobType == string(jobdef.JobTypeNetworkDiscoveryAgentPlan) {
		var err error
		if limit, err = s.agentQuotaLimit(ctx, orgUID); err != nil {
			return nil, err
		}
	}

	job, err := s.jobSvc.CreateJobAdmitted(ctx, orgUID, jobType, jobConfig,
		func(ctx context.Context, tx bun.Tx) error {
			if s.isPostgres {
				if _, lockErr := tx.NewRaw("SELECT pg_advisory_xact_lock(?, hashtext(?))",
					dblock.ClassDiscoveryScanStart, orgUID).Exec(ctx); lockErr != nil {
					return fmt.Errorf("lock discovery scan start: %w", lockErr)
				}
			}

			if guardErr := s.checkScanAlreadyRunning(ctx, tx, orgUID); guardErr != nil {
				return guardErr
			}

			if limit != nil {
				return s.checkAgentQuota(ctx, tx, orgUID, *limit, addresses)
			}

			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery scan job: %w", err)
	}

	return job, nil
}

func scanTarget(job *models.Job) audit.Target {
	return audit.Target{Type: auditTargetScan, UID: job.UID, Name: job.Type}
}

func isPlanType(jobType string) bool {
	return jobType == string(jobdef.JobTypeNetworkDiscoveryPlan) ||
		jobType == string(jobdef.JobTypeNetworkDiscoveryAgentPlan)
}

// agentQuotaLimit is the org's daily agent-discovery address cap
// (discovery_agent_addresses_per_day); nil (self-hosted) is unlimited.
func (s *Service) agentQuotaLimit(ctx context.Context, orgUID string) (*int, error) {
	if s.entitlements == nil {
		return nil, nil //nolint:nilnil // nil limit is unlimited
	}

	resolved, err := s.entitlements.Resolve(ctx, orgUID)
	if err != nil {
		return nil, fmt.Errorf("resolve entitlements: %w", err)
	}

	return resolved.Limits.MaxDiscoveryAgentAddressesPerDay, nil
}

// checkAgentQuota refuses a region-bound scan that would take the org's agents
// over their daily address limit. Usage is summed from the source of truth:
// the addressCount of the org's agent plans created today (UTC), canceled ones
// included (a started scan is charged), retry clones excluded (same scan).
func (s *Service) checkAgentQuota(ctx context.Context, idb bun.IDB, orgUID string, limit, addresses int) error {
	now := time.Now().UTC()
	since := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	addressExpr := "CAST(json_extract(config, '$.addressCount') AS INTEGER)"
	if s.isPostgres {
		addressExpr = "(config->>'addressCount')::bigint"
	}

	var used int

	err := idb.NewSelect().
		TableExpr("jobs").
		ColumnExpr("COALESCE(SUM("+addressExpr+"), 0)").
		Where("type = ?", string(jobdef.JobTypeNetworkDiscoveryAgentPlan)).
		Where("organization_uid = ?", orgUID).
		Where("previous_job_uid IS NULL").
		Where("created_at >= ?", since).
		Scan(ctx, &used)
	if err != nil {
		return fmt.Errorf("read discovery usage: %w", err)
	}

	if used+addresses > limit {
		return scantypes.NewError(scantypes.CodeQuotaExceeded, fmt.Sprintf(
			"agent discovery is limited to %d addresses a day: %d used today, this scan needs %d",
			limit, used, addresses))
	}

	return nil
}

// scanJobTypes returns the job types that constitute discovery scans, for the
// scan list. It is keyed off the registry being non-empty (every registered type
// maps onto one of these job types via its BuildJob; the activation test asserts
// the correspondence) plus the fan-out child type so child rows can be filtered
// out of the user-facing list.
func scanJobTypes() []string {
	return []string{
		string(jobdef.JobTypeNetworkDiscoveryPlan),
		string(jobdef.JobTypeNetworkDiscoveryAgentPlan),
		string(jobdef.JobTypeNetworkDiscovery),
		string(jobdef.JobTypeFreeboxLanDiscovery),
		string(jobdef.JobTypeContainerDiscovery),
		string(jobdef.JobTypeKubernetesDiscovery),
	}
}

// checkAlreadyRunning returns ErrAlreadyRunning if a discovery job of the given
// type is already running for the org. Per-org and per-type so LAN and Freebox
// runs guard independently.
func (s *Service) checkAlreadyRunning(
	ctx context.Context, idb bun.IDB, orgUID string, jobType jobdef.JobType,
) error {
	count, err := idb.NewSelect().
		TableExpr("jobs").
		Where("type = ?", string(jobType)).
		Where("organization_uid = ?", orgUID).
		Where("status = ?", models.JobStatusRunning).
		Where("deleted_at IS NULL").
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to check running jobs: %w", err)
	}

	if count > 0 {
		return ErrAlreadyRunning
	}

	return nil
}

// parentJobUIDExpr returns the dialect-specific SQL expression that reads the
// parentJobUid key out of a job's JSON config column.
func (s *Service) parentJobUIDExpr() string {
	if s.isPostgres {
		return "config->>'parentJobUid'"
	}

	return "json_extract(config, '$.parentJobUid')"
}

// checkScanAlreadyRunning returns ErrAlreadyRunning when a fan-out scan is active
// for the org. A scan is active if its plan job is pending/running, OR any child
// network_discovery job is pending/running. Children stuck in `running` past
// staleScanThreshold are ignored so a single crashed worker cannot block all
// future scans for the org (the pragmatic stand-in for a real reaper).
func (s *Service) checkScanAlreadyRunning(ctx context.Context, idb bun.IDB, orgUID string) error {
	// Active plan jobs.
	planCount, err := idb.NewSelect().
		TableExpr("jobs").
		Where("type IN (?)", bun.List([]string{
			string(jobdef.JobTypeNetworkDiscoveryPlan), string(jobdef.JobTypeNetworkDiscoveryAgentPlan),
		})).
		Where("organization_uid = ?", orgUID).
		Where("status IN (?)", bun.List([]models.JobStatus{models.JobStatusPending, models.JobStatusRunning})).
		Where("deleted_at IS NULL").
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to check running plan jobs: %w", err)
	}

	if planCount > 0 {
		return ErrAlreadyRunning
	}

	// Active child jobs, ignoring children whose updated_at is stale.
	staleBefore := time.Now().Add(-staleScanThreshold)

	childCount, err := idb.NewSelect().
		TableExpr("jobs").
		Where("type = ?", string(jobdef.JobTypeNetworkDiscovery)).
		Where("organization_uid = ?", orgUID).
		Where("status IN (?)", bun.List([]models.JobStatus{models.JobStatusPending, models.JobStatusRunning})).
		Where("deleted_at IS NULL").
		Where("updated_at >= ?", staleBefore).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to check running child jobs: %w", err)
	}

	if childCount > 0 {
		return ErrAlreadyRunning
	}

	// Live chunks of agent plans: a pending task not yet expired, or a claimed
	// one under a live lease.
	now := time.Now()

	taskCount, err := idb.NewSelect().
		TableExpr("worker_tasks").
		Where("kind = ?", discoverytask.Kind).
		Where("organization_uid = ?", orgUID).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				Where("(status = ? AND updated_at >= ?)", models.WorkerTaskStatusPending,
					now.Add(-models.WorkerTaskStaleAfter)).
				WhereOr("(status = ? AND lease_until >= ?)", models.WorkerTaskStatusClaimed, now)
		}).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to check live discovery tasks: %w", err)
	}

	if taskCount > 0 {
		return ErrAlreadyRunning
	}

	return nil
}

// ScanProgress is the derived progress view of a scan. For a fan-out (chunked)
// scan it aggregates the plan job plus its child network_discovery jobs; for a
// non-chunked scan it reports a single chunk and the job's own status. groupCount
// and checkCount come from the discovered_checks rolled up under the scan UID.
type ScanProgress struct {
	TotalChunks     int    `json:"totalChunks"`
	CompletedChunks int    `json:"completedChunks"`
	FailedChunks    int    `json:"failedChunks"`
	RunningChunks   int    `json:"runningChunks"`
	PendingChunks   int    `json:"pendingChunks"`
	DerivedStatus   string `json:"derivedStatus"`
	GroupCount      int    `json:"groupCount"`
	CheckCount      int    `json:"checkCount"`
	// Region is the private region an agent-run scan runs from; LastErrorCode
	// the code of its latest failed chunk (spec 2026-10-22-04).
	Region        string `json:"region,omitempty"`
	LastErrorCode string `json:"lastErrorCode,omitempty"`
}

// GetScanProgress returns a uniform progress view for any scan job. A fan-out
// plan rolls up its children; any other scan job reports totalChunks=1 and a
// chunk bucket derived from its own status.
func (s *Service) GetScanProgress(ctx context.Context, orgUID, scanUID string) (*ScanProgress, error) {
	job, err := s.jobSvc.GetJob(ctx, scanUID)
	if err != nil {
		return nil, ErrScanNotFound
	}

	progress := &ScanProgress{}

	switch job.Type {
	case string(jobdef.JobTypeNetworkDiscoveryPlan):
		s.fillPlanProgress(ctx, job, orgUID, scanUID, progress)
	case string(jobdef.JobTypeNetworkDiscoveryAgentPlan):
		if err = s.fillAgentPlanProgress(ctx, job, orgUID, progress); err != nil {
			return nil, err
		}
	default:
		fillSingleJobProgress(job, progress)
	}

	groupCount, checkCount, err := s.countDiscovered(ctx, orgUID, scanUID)
	if err != nil {
		return nil, err
	}

	progress.GroupCount = groupCount
	progress.CheckCount = checkCount

	return progress, nil
}

// fillPlanProgress aggregates a fan-out plan's children into the progress block.
func (s *Service) fillPlanProgress(
	ctx context.Context, plan *models.Job, orgUID, planUID string, progress *ScanProgress,
) {
	// Denominator: prefer the plan's recorded totalChunks, fall back to counting children.
	if plan.Output != nil {
		if v, ok := plan.Output["totalChunks"]; ok {
			progress.TotalChunks = toInt(v)
		}
	}

	parentExpr := s.parentJobUIDExpr()

	var children []*models.Job

	err := s.db.NewSelect().
		Model(&children).
		Where("type = ?", string(jobdef.JobTypeNetworkDiscovery)).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Where(parentExpr+" = ?", planUID).
		Scan(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to count child jobs for scan progress", "plan_uid", planUID, "error", err)

		return
	}

	for _, child := range children {
		switch child.Status {
		case models.JobStatusSuccess:
			progress.CompletedChunks++
		case models.JobStatusFailed:
			progress.FailedChunks++
		case models.JobStatusRunning:
			progress.RunningChunks++
		case models.JobStatusPending:
			progress.PendingChunks++
		case models.JobStatusRetried:
			// Retried children are superseded by a fresh pending child; don't
			// double-count them toward any terminal bucket.
		}
	}

	if progress.TotalChunks == 0 {
		progress.TotalChunks = len(children)
	}

	progress.DerivedStatus = derivePlanStatus(plan, progress)
}

// fillAgentPlanProgress rolls an agent plan's discovery tasks up: done is
// completed; failed, or pending past models.WorkerTaskStaleAfter, is failed;
// claimed under a live lease is running; the rest still waits.
func (s *Service) fillAgentPlanProgress(
	ctx context.Context, plan *models.Job, orgUID string, progress *ScanProgress,
) error {
	if plan.Output != nil {
		progress.TotalChunks = toInt(plan.Output["totalChunks"])
	}

	if region, ok := plan.Config["region"].(string); ok {
		progress.Region = region
	}

	var tasks []*models.WorkerTask

	err := s.db.NewSelect().
		Model(&tasks).
		Column("status", "lease_until", "error_code", "updated_at").
		Where("job_uid = ?", plan.UID).
		Where("organization_uid = ?", orgUID).
		Where("kind = ?", discoverytask.Kind).
		Scan(ctx)
	if err != nil {
		return fmt.Errorf("read discovery tasks: %w", err)
	}

	now := time.Now()

	var lastFailed time.Time

	for _, task := range tasks {
		countTask(task, now, progress)

		if task.Status == models.WorkerTaskStatusFailed && task.ErrorCode != nil && !task.UpdatedAt.Before(lastFailed) {
			lastFailed = task.UpdatedAt
			progress.LastErrorCode = *task.ErrorCode
		}
	}

	if progress.TotalChunks == 0 {
		progress.TotalChunks = len(tasks)
	}

	progress.DerivedStatus = derivePlanStatus(plan, progress)

	return nil
}

// countTask adds one discovery task to its progress bucket. A canceled task
// counts nowhere.
func countTask(task *models.WorkerTask, now time.Time, progress *ScanProgress) {
	switch {
	case task.Status == models.WorkerTaskStatusDone:
		progress.CompletedChunks++
	case task.Status == models.WorkerTaskStatusFailed, task.IsStale(now):
		progress.FailedChunks++
	case task.Status == models.WorkerTaskStatusClaimed && task.LeaseUntil != nil && !task.LeaseUntil.Before(now):
		progress.RunningChunks++
	case task.IsActive():
		progress.PendingChunks++
	}
}

// fillSingleJobProgress reports a non-chunked scan as a single chunk whose state
// mirrors the job's own status.
func fillSingleJobProgress(job *models.Job, progress *ScanProgress) {
	progress.TotalChunks = 1

	switch job.Status {
	case models.JobStatusSuccess:
		progress.CompletedChunks = 1
		progress.DerivedStatus = string(models.JobStatusSuccess)
	case models.JobStatusFailed:
		progress.FailedChunks = 1
		progress.DerivedStatus = string(models.JobStatusFailed)
	case models.JobStatusRunning:
		progress.RunningChunks = 1
		progress.DerivedStatus = string(models.JobStatusRunning)
	case models.JobStatusRetried:
		progress.PendingChunks = 1
		progress.DerivedStatus = string(models.JobStatusRunning)
	case models.JobStatusPending:
		progress.PendingChunks = 1
		progress.DerivedStatus = string(models.JobStatusRunning)
	default:
		progress.PendingChunks = 1
		progress.DerivedStatus = string(models.JobStatusPending)
	}
}

// countDiscovered returns the distinct group count and total check count rolled
// up under a scan UID.
func (s *Service) countDiscovered(ctx context.Context, orgUID, scanUID string) (int, int, error) {
	checkCount, err := s.db.NewSelect().
		TableExpr("discovered_checks").
		Where("organization_uid = ?", orgUID).
		Where("job_uid = ?", scanUID).
		Where("deleted_at IS NULL").
		Count(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to count discovered checks: %w", err)
	}

	var groupCount int

	err = s.db.NewSelect().
		ColumnExpr("COUNT(DISTINCT group_key)").
		TableExpr("discovered_checks").
		Where("organization_uid = ?", orgUID).
		Where("job_uid = ?", scanUID).
		Where("deleted_at IS NULL").
		Scan(ctx, &groupCount)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to count discovered groups: %w", err)
	}

	return groupCount, checkCount, nil
}

// derivePlanStatus computes the overall scan status from the plan job and the
// aggregated child progress.
func derivePlanStatus(plan *models.Job, progress *ScanProgress) string {
	switch {
	case plan.Status == models.JobStatusFailed:
		return string(models.JobStatusFailed)
	case plan.Status == models.JobStatusPending || plan.Status == models.JobStatusRunning:
		return string(models.JobStatusRunning)
	case progress.RunningChunks > 0 || progress.PendingChunks > 0:
		return string(models.JobStatusRunning)
	default:
		return string(models.JobStatusSuccess)
	}
}

// toInt coerces a JSON-decoded numeric value (float64, json.Number, int) to int.
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()

		return int(i)
	default:
		return 0
	}
}

// CancelScan stops a fan-out scan: it cancels the plan job if still pending,
// then soft-deletes every live (pending or running) child network_discovery
// job. Soft-deleting a running child does two things at once: the
// already-running guard stops counting it immediately (a new scan can start
// right away instead of waiting out the chunk, measured at ~1min for a /20 of
// unroutable addresses), and the job worker's cancellation watcher notices the
// deletion within its poll interval and cancels the chunk's context so the
// runner slot frees promptly. A plan still mid-chunking can enqueue a child in
// the small window between this sweep and its own watcher-driven cancellation;
// that residual chunk is bounded and finishes naturally. Returns
// ErrScanNotFound if no such plan exists for the org.
func (s *Service) CancelScan(ctx context.Context, orgUID, planUID string) error {
	plan, err := s.jobSvc.GetJob(ctx, planUID)
	if err != nil || plan.OrganizationUID == nil || *plan.OrganizationUID != orgUID {
		return ErrScanNotFound
	}

	// Cancel the plan job itself if it hasn't started scheduling yet. CancelJob
	// only affects pending jobs, so a running/finished plan is left untouched.
	if cancelErr := s.jobSvc.CancelJob(ctx, planUID); cancelErr != nil &&
		!errors.Is(cancelErr, jobsvc.ErrJobNotPending) {
		return fmt.Errorf("cancel plan job: %w", cancelErr)
	}

	// Soft-delete every live child in one statement. Pending children never
	// start; running children are aborted by the worker's cancellation
	// watcher (and stop counting against the already-running guard the moment
	// this commits).
	now := time.Now()
	parentExpr := s.parentJobUIDExpr()

	_, err = s.db.NewUpdate().
		Model((*models.Job)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("type = ?", string(jobdef.JobTypeNetworkDiscovery)).
		Where("organization_uid = ?", orgUID).
		Where("status IN (?)", bun.List([]models.JobStatus{models.JobStatusPending, models.JobStatusRunning})).
		Where("deleted_at IS NULL").
		Where(parentExpr+" = ?", planUID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("cancel live child jobs: %w", err)
	}

	// An agent plan's chunks are worker tasks: an agent running one gets 410
	// on its next heartbeat and its late result is discarded.
	if plan.Type == string(jobdef.JobTypeNetworkDiscoveryAgentPlan) && s.tasks != nil {
		if _, err = s.tasks.Cancel(ctx, workertasks.CancelFilter{JobUID: planUID}); err != nil {
			return fmt.Errorf("cancel discovery tasks: %w", err)
		}
	}

	audit.Record(ctx, s.dbSvc, orgUID, models.EventTypeDiscoveryScanCanceled, scanTarget(plan), nil)

	return nil
}

// ListDiscoveredChecks returns discovered checks for an org with optional
// filters. Rows are ordered by group then slug so the frontend can group them.
func (s *Service) ListDiscoveredChecks(
	ctx context.Context, orgUID string, opts *ListChecksOptions,
) ([]*models.DiscoveredCheck, error) {
	var rows []*models.DiscoveredCheck

	query := s.db.NewSelect().
		Model(&rows).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("group_label ASC", "slug ASC")

	if opts.JobUID != "" {
		query = query.Where("job_uid = ?", opts.JobUID)
	}

	if opts.Group != "" {
		query = query.Where("group_key = ?", opts.Group)
	}

	if opts.Promoted != nil {
		if *opts.Promoted {
			query = query.Where("promoted_to_check_uid IS NOT NULL")
		} else {
			query = query.Where("promoted_to_check_uid IS NULL")
		}
	}

	if len(opts.Sources) > 0 {
		sources := make([]string, 0, len(opts.Sources))
		for _, src := range opts.Sources {
			sources = append(sources, string(src))
		}

		query = query.Where("source IN (?)", bun.List(sources))
	}

	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}

	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("list discovered checks: %w", err)
	}

	return rows, nil
}

// getChecks loads the active, unpromoted discovered checks for the given UIDs,
// scoped to the org. It errors with ErrCheckNotFound when any UID is missing and
// ErrAlreadyPromoted when any is already promoted.
func (s *Service) getChecks(ctx context.Context, orgUID string, uids []string) ([]*models.DiscoveredCheck, error) {
	var rows []*models.DiscoveredCheck

	err := s.db.NewSelect().
		Model(&rows).
		Where("organization_uid = ?", orgUID).
		Where("uid IN (?)", bun.List(uids)).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("load discovered checks: %w", err)
	}

	if len(rows) != len(uids) {
		return nil, ErrCheckNotFound
	}

	for _, row := range rows {
		if row.PromotedToCheckUID != nil {
			return nil, ErrAlreadyPromoted
		}
	}

	return rows, nil
}

// PromoteChecks creates one real Check per referenced discovered-check row
// (config/name/slug already on the row; overrides may adjust name/period) and
// marks each row promoted. Created checks carry the auto-discovery +
// discovery-job labels. Check creation is fail-fast: if a later CreateCheck
// errors, earlier checks persist and the error is returned.
func (s *Service) PromoteChecks(
	ctx context.Context, orgUID, orgSlug string, req PromoteRequest,
) ([]checks.CheckResponse, error) {
	rows, err := s.getChecks(ctx, orgUID, req.UIDs)
	if err != nil {
		return nil, err
	}

	// Validate every region first so a refusal creates nothing.
	if err = s.checkRegionsOwned(ctx, orgUID, rows); err != nil {
		return nil, err
	}

	created := make([]checks.CheckResponse, 0, len(rows))

	for _, row := range rows {
		checkResp, createErr := s.promoteOne(ctx, orgSlug, row, req.Overrides)
		if createErr != nil {
			return nil, createErr
		}

		created = append(created, checkResp)

		s.markPromoted(ctx, orgUID, row.UID, checkResp.UID)
	}

	return created, nil
}

// checkRegionsOwned refuses rows found from a private region the org no longer has.
func (s *Service) checkRegionsOwned(ctx context.Context, orgUID string, rows []*models.DiscoveredCheck) error {
	for _, row := range rows {
		if row.Region == "" {
			continue
		}

		owned, err := s.regionsSvc.OwnsPrivateRegion(ctx, orgUID, row.Region)
		if err != nil {
			return fmt.Errorf("check discovery region: %w", err)
		}

		if !owned {
			return fmt.Errorf("%w: %s", ErrRegionUnavailable, row.Region)
		}
	}

	return nil
}

// promoteOne creates a single real check from a discovered-check row.
func (s *Service) promoteOne(
	ctx context.Context, orgSlug string, row *models.DiscoveredCheck, overrides PromoteOverrides,
) (checks.CheckResponse, error) {
	config := map[string]any{}
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &config); err != nil {
			return checks.CheckResponse{}, fmt.Errorf("parse discovered check config: %w", err)
		}
	}

	name := row.Name
	if overrides.Name != "" {
		name = overrides.Name
	}

	if name == "" {
		name = row.GroupLabel
	}

	createReq := checks.CreateCheckRequest{
		Type:   normalizeCheckType(row.Type),
		Name:   name,
		Slug:   row.Slug,
		Period: overrides.Period,
		Config: config,
		Labels: map[string]string{
			"auto-discovery": "true",
			"discovery-job":  row.JobUID,
		},
	}

	// A check found from a private region always runs from that region.
	if row.Region != "" {
		createReq.Regions = []string{row.Region}
	}

	checkResp, err := s.checksSvc.CreateCheck(ctx, orgSlug, createReq)
	if err != nil {
		return checks.CheckResponse{}, fmt.Errorf("create check: %w", err)
	}

	return checkResp, nil
}

// markPromoted points a discovered check's promoted_to_check_uid at the created
// check. A failure here is non-fatal (the check exists) — it is logged.
func (s *Service) markPromoted(ctx context.Context, orgUID, checkUID, createdUID string) {
	_, err := s.db.NewUpdate().
		TableExpr("discovered_checks").
		Set("promoted_to_check_uid = ?", createdUID).
		Set("updated_at = ?", time.Now()).
		Where("uid = ?", checkUID).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to mark discovered check as promoted (check was created)",
			"discovered_check_uid", checkUID, "check_uid", createdUID, "error", err)
	}
}

// SoftDeleteCheck soft-deletes a single discovered check (dismiss).
func (s *Service) SoftDeleteCheck(ctx context.Context, orgUID, uid string) error {
	now := time.Now()

	result, err := s.db.NewUpdate().
		TableExpr("discovered_checks").
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("soft delete discovered check: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return ErrCheckNotFound
	}

	return nil
}

// SoftDeleteGroup soft-deletes every active discovered check in a group for a
// scan (dismiss a whole group) found from region ("" = the server itself). It
// returns the number of rows dismissed.
func (s *Service) SoftDeleteGroup(ctx context.Context, orgUID, jobUID, group, region string) (int, error) {
	now := time.Now()

	query := s.db.NewUpdate().
		TableExpr("discovered_checks").
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("organization_uid = ?", orgUID).
		Where("group_key = ?", group).
		Where("region = ?", region).
		Where("deleted_at IS NULL")

	if jobUID != "" {
		query = query.Where("job_uid = ?", jobUID)
	}

	result, err := query.Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("soft delete discovered group: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return 0, ErrCheckNotFound
	}

	return int(rows), nil
}

// GetCheck returns a single discovered check by UID (scoped to org).
func (s *Service) GetCheck(ctx context.Context, orgUID, uid string) (*models.DiscoveredCheck, error) {
	var row models.DiscoveredCheck

	err := s.db.NewSelect().
		Model(&row).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCheckNotFound
	} else if err != nil {
		return nil, fmt.Errorf("get discovered check: %w", err)
	}

	return &row, nil
}

// normalizeCheckType maps the discovery scan engine's check-type aliases to the
// registered check types accepted by the checks service. The suggest engine emits
// "ping" (matching the urlparse alias), which the registry knows only as "icmp".
// docker/kubernetes pass through unchanged.
func normalizeCheckType(checkType string) string {
	if checkType == "ping" {
		return "icmp"
	}

	return checkType
}
