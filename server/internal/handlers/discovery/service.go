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

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// staleScanThreshold is how old a child network_discovery job's updated_at may be
// before the "already running" guard stops counting it as active. This is the
// pragmatic stand-in for a real lease/reaper: a single child stuck in `running`
// (e.g. a crashed worker) stops blocking new scans after this window.
const staleScanThreshold = 30 * time.Minute

var (
	// ErrHostNotFound is returned when a discovered host is not found.
	ErrHostNotFound = errors.New("discovered host not found")
	// ErrScanNotFound is returned when a discovery scan (plan job) is not found.
	ErrScanNotFound = errors.New("discovery scan not found")
	// ErrAlreadyRunning is returned when a discovery job is already running for the org.
	ErrAlreadyRunning = errors.New("a discovery scan is already running for this organization")
	// ErrAlreadyPromoted is returned when a host has already been promoted.
	ErrAlreadyPromoted = errors.New("host already promoted")
	// ErrFreeboxNotGranted is returned when a Freebox discovery targets a
	// channel that has not completed the LCD-pairing flow.
	ErrFreeboxNotGranted = errors.New("freebox channel is not paired yet")
	// ErrFreeboxChannelNotFound is returned when the targeted Freebox channel
	// does not exist, belongs to another org, or is not a Freebox channel.
	ErrFreeboxChannelNotFound = errors.New("freebox channel not found")
)

// ListHostsOptions configures which hosts to return.
type ListHostsOptions struct {
	JobUID   string
	Promoted *bool
	Sources  []models.DiscoverySource
	Limit    int
	Offset   int
}

// PromoteCheckSpec describes a single check to create when promoting a host.
type PromoteCheckSpec struct {
	CheckType   string         `json:"checkType"`
	Name        string         `json:"name,omitempty"`
	Slug        string         `json:"slug,omitempty"`
	Period      *string        `json:"period,omitempty"`
	ExtraConfig map[string]any `json:"extraConfig,omitempty"`
}

// PromoteRequest is the request body for promoting a discovered host into one
// or more checks in a single call.
type PromoteRequest struct {
	Checks []PromoteCheckSpec `json:"checks"`
}

// Service provides business logic for network discovery operations.
type Service struct {
	db         *bun.DB
	dbSvc      db.Service
	checksSvc  *checks.Service
	jobSvc     jobsvc.Service
	creds      credentials.Service
	isPostgres bool
}

// NewService creates a new discovery service.
func NewService(
	bunDB *bun.DB,
	dbSvc db.Service,
	checksSvc *checks.Service,
	jobSvc jobsvc.Service,
	creds credentials.Service,
) *Service {
	_, isPostgres := bunDB.Dialect().(*pgdialect.Dialect)

	return &Service{
		db:         bunDB,
		dbSvc:      dbSvc,
		checksSvc:  checksSvc,
		jobSvc:     jobSvc,
		creds:      creds,
		isPostgres: isPostgres,
	}
}

// StartScan creates a fan-out discovery scan. It validates the range against the
// overall ceiling (ValidatePlanCIDRs), guards against an already-running scan for
// the org, then creates a single network_discovery_plan job. That plan job's UID
// is the scan UID; when it runs it splits the range into bounded child jobs.
// Returns ErrRangeTooLarge if the range exceeds MaxScanChunks, ErrAlreadyRunning
// if another scan is in progress.
func (s *Service) StartScan(ctx context.Context, orgUID string, cfg *disc.Config) (*models.Job, error) {
	if err := disc.ValidatePlanCIDRs(cfg.CIDRs); err != nil {
		return nil, err
	}

	// Check for already-running scan (plan or any non-stale child).
	if err := s.checkScanAlreadyRunning(ctx, orgUID); err != nil {
		return nil, err
	}

	// The plan job carries no parentJobUid — children inherit it from the plan UID.
	cfg.ParentJobUID = ""

	configBytes, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal discovery config: %w", err)
	}

	job, err := s.jobSvc.CreateJob(
		ctx,
		orgUID,
		string(jobdef.JobTypeNetworkDiscoveryPlan),
		configBytes,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery plan job: %w", err)
	}

	return job, nil
}

// StartFreeboxScan validates that channelUID is a paired Freebox channel,
// guards against a Freebox discovery already running for the org, then creates
// a freebox_lan_discovery job. Returns ErrFreeboxNotGranted /
// ErrFreeboxChannelNotFound when validation fails, ErrAlreadyRunning when a
// run is already in flight.
func (s *Service) StartFreeboxScan(ctx context.Context, orgUID, channelUID string) (*models.Job, error) {
	// Fail fast: probe the channel via the shared resolver so we surface a
	// 409/404 before queueing a job that would just fail on first run.
	if _, err := freebox.ListLanHostsForChannel(ctx, s.dbSvc, s.creds, orgUID, channelUID); err != nil {
		switch {
		case errors.Is(err, freebox.ErrFreeboxNotGranted):
			return nil, ErrFreeboxNotGranted
		case errors.Is(err, freebox.ErrFreeboxChannelNotFound):
			return nil, ErrFreeboxChannelNotFound
		default:
			return nil, fmt.Errorf("validate freebox channel: %w", err)
		}
	}

	if err := s.checkAlreadyRunning(ctx, orgUID, jobdef.JobTypeFreeboxLanDiscovery); err != nil {
		return nil, err
	}

	configBytes, err := json.Marshal(jobFreeboxConfig{ChannelUID: channelUID})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal freebox discovery config: %w", err)
	}

	job, err := s.jobSvc.CreateJob(
		ctx,
		orgUID,
		string(jobdef.JobTypeFreeboxLanDiscovery),
		configBytes,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create freebox discovery job: %w", err)
	}

	return job, nil
}

// jobFreeboxConfig is the config payload for a freebox_lan_discovery job. Kept
// local to avoid importing the jobtypes package (which would create a cycle).
type jobFreeboxConfig struct {
	ChannelUID string `json:"channelUid"`
}

// checkAlreadyRunning returns ErrAlreadyRunning if a discovery job of the given
// type is already running for the org. Per-org and per-type so LAN and Freebox
// runs guard independently.
func (s *Service) checkAlreadyRunning(ctx context.Context, orgUID string, jobType jobdef.JobType) error {
	count, err := s.db.NewSelect().
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
func (s *Service) checkScanAlreadyRunning(ctx context.Context, orgUID string) error {
	// Active plan jobs.
	planCount, err := s.db.NewSelect().
		TableExpr("jobs").
		Where("type = ?", string(jobdef.JobTypeNetworkDiscoveryPlan)).
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

	childCount, err := s.db.NewSelect().
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

	return nil
}

// ScanProgress is the derived progress view of a fan-out scan, aggregated from
// the plan job plus its child network_discovery jobs.
type ScanProgress struct {
	TotalChunks     int    `json:"totalChunks"`
	CompletedChunks int    `json:"completedChunks"`
	FailedChunks    int    `json:"failedChunks"`
	RunningChunks   int    `json:"runningChunks"`
	PendingChunks   int    `json:"pendingChunks"`
	DerivedStatus   string `json:"derivedStatus"`
	HostCount       int    `json:"hostCount"`
}

// GetScanProgress aggregates a fan-out scan's progress from the plan job and its
// children. derivedStatus is "running" while the plan is pending/running or any
// child is pending/running; "success" once every child is terminal; "failed" only
// if the plan job itself failed.
func (s *Service) GetScanProgress(ctx context.Context, orgUID, planUID string) (*ScanProgress, error) {
	plan, err := s.jobSvc.GetJob(ctx, planUID)
	if err != nil {
		return nil, ErrScanNotFound
	}

	progress := &ScanProgress{}

	// Denominator: prefer the plan's recorded totalChunks, fall back to counting children.
	if plan.Output != nil {
		if v, ok := plan.Output["totalChunks"]; ok {
			progress.TotalChunks = toInt(v)
		}
	}

	// Count children by status.
	parentExpr := s.parentJobUIDExpr()

	var children []*models.Job

	err = s.db.NewSelect().
		Model(&children).
		Where("type = ?", string(jobdef.JobTypeNetworkDiscovery)).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Where(parentExpr+" = ?", planUID).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to count child jobs: %w", err)
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

	// Host count rolled up under the plan UID.
	hostCount, err := s.db.NewSelect().
		TableExpr("discovered_hosts").
		Where("organization_uid = ?", orgUID).
		Where("job_uid = ?", planUID).
		Where("deleted_at IS NULL").
		Count(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to count hosts: %w", err)
	}

	progress.HostCount = hostCount

	progress.DerivedStatus = deriveScanStatus(plan, progress)

	return progress, nil
}

// deriveScanStatus computes the overall scan status from the plan job and the
// aggregated child progress.
func deriveScanStatus(plan *models.Job, progress *ScanProgress) string {
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

// CancelScan stops a fan-out scan: it cancels the plan job if still pending, then
// cancels every pending child network_discovery job (soft delete). Running
// children are bounded (~minutes) and finish naturally; the net effect is that no
// new chunks start. Returns ErrScanNotFound if no such plan exists for the org.
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

	// Soft-delete every pending child in one statement (CancelJob is per-UID and
	// pending-only; a bulk update matches its semantics for the child set).
	now := time.Now()
	parentExpr := s.parentJobUIDExpr()

	_, err = s.db.NewUpdate().
		Model((*models.Job)(nil)).
		Set("deleted_at = ?", now).
		Set("updated_at = ?", now).
		Where("type = ?", string(jobdef.JobTypeNetworkDiscovery)).
		Where("organization_uid = ?", orgUID).
		Where("status = ?", models.JobStatusPending).
		Where("deleted_at IS NULL").
		Where(parentExpr+" = ?", planUID).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("cancel pending child jobs: %w", err)
	}

	return nil
}

// UpsertDiscoveredHost inserts or updates a discovered host record.
// On conflict (same org+ip, not deleted, not promoted) it updates the scan data.
func (s *Service) UpsertDiscoveredHost(ctx context.Context, host *models.DiscoveredHost) error {
	openPortsJSON, err := json.Marshal(host.OpenPorts)
	if err != nil {
		return fmt.Errorf("marshal open_ports: %w", err)
	}

	suggestedJSON, err := json.Marshal(host.SuggestedChecks)
	if err != nil {
		return fmt.Errorf("marshal suggested_checks: %w", err)
	}

	host.OpenPorts = openPortsJSON
	host.SuggestedChecks = suggestedJSON

	var insertQuery *bun.InsertQuery

	if s.isPostgres {
		insertQuery = s.db.NewInsert().
			Model(host).
			On("CONFLICT (organization_uid, ip, source) WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL DO UPDATE").
			Set("job_uid = EXCLUDED.job_uid").
			Set("hostname = EXCLUDED.hostname").
			Set("open_ports = EXCLUDED.open_ports").
			Set("icmp_reachable = EXCLUDED.icmp_reachable").
			Set("suggested_checks = EXCLUDED.suggested_checks").
			Set("discovered_at = EXCLUDED.discovered_at")
	} else {
		// SQLite: INSERT OR REPLACE semantics via conflict handling.
		insertQuery = s.db.NewInsert().
			Model(host).
			On("CONFLICT (organization_uid, ip, source) WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL DO UPDATE").
			Set("job_uid = excluded.job_uid").
			Set("hostname = excluded.hostname").
			Set("open_ports = excluded.open_ports").
			Set("icmp_reachable = excluded.icmp_reachable").
			Set("suggested_checks = excluded.suggested_checks").
			Set("discovered_at = excluded.discovered_at")
	}

	_, err = insertQuery.Exec(ctx)
	if err != nil {
		return fmt.Errorf("upsert discovered host: %w", err)
	}

	return nil
}

// ListByJob returns all active discovered hosts for a specific job.
func (s *Service) ListByJob(ctx context.Context, orgUID, jobUID string) ([]*models.DiscoveredHost, error) {
	var hosts []*models.DiscoveredHost

	err := s.db.NewSelect().
		Model(&hosts).
		Where("organization_uid = ?", orgUID).
		Where("job_uid = ?", jobUID).
		Where("deleted_at IS NULL").
		Order("discovered_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list by job: %w", err)
	}

	return hosts, nil
}

// ListHosts returns discovered hosts for an org with optional filters.
func (s *Service) ListHosts(
	ctx context.Context, orgUID string, opts ListHostsOptions,
) ([]*models.DiscoveredHost, error) {
	var hosts []*models.DiscoveredHost

	query := s.db.NewSelect().
		Model(&hosts).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("discovered_at DESC")

	if opts.JobUID != "" {
		query = query.Where("job_uid = ?", opts.JobUID)
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

	err := query.Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}

	return hosts, nil
}

// GetHost returns a single discovered host by UID (scoped to org).
func (s *Service) GetHost(ctx context.Context, orgUID, hostUID string) (*models.DiscoveredHost, error) {
	var host models.DiscoveredHost

	err := s.db.NewSelect().
		Model(&host).
		Where("uid = ?", hostUID).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrHostNotFound
	} else if err != nil {
		return nil, fmt.Errorf("get host: %w", err)
	}

	return &host, nil
}

// PromoteHost creates one or more Checks from the discovered host and marks the
// host as promoted (pointing promoted_to_check_uid at the first created check).
// Check creation is fail-fast: if a later CreateCheck errors, earlier checks
// persist and the error is returned (full transactional rollback is a follow-up).
func (s *Service) PromoteHost(
	ctx context.Context, orgUID, orgSlug, hostUID string, req PromoteRequest,
) ([]checks.CheckResponse, error) {
	host, err := s.GetHost(ctx, orgUID, hostUID)
	if err != nil {
		return nil, err
	}

	if host.PromotedToCheckUID != nil {
		return nil, ErrAlreadyPromoted
	}

	// Add discovery labels — shared across every created check.
	labels := map[string]string{
		"auto-discovery": "true",
		"discovery-job":  host.JobUID,
	}

	created := make([]checks.CheckResponse, 0, len(req.Checks))

	for i := range req.Checks {
		spec := req.Checks[i]
		// Build the check config from the host's matching suggested check, then merge ExtraConfig.
		checkConfig, cfgErr := buildCheckConfig(host, spec)
		if cfgErr != nil {
			return nil, cfgErr
		}

		createReq := checks.CreateCheckRequest{
			Type:   normalizeCheckType(spec.CheckType),
			Name:   spec.Name,
			Slug:   spec.Slug,
			Period: spec.Period,
			Config: checkConfig,
			Labels: labels,
		}

		if createReq.Name == "" {
			createReq.Name = host.IP
		}

		checkResp, createErr := s.checksSvc.CreateCheck(ctx, orgSlug, createReq)
		if createErr != nil {
			return nil, fmt.Errorf("create check: %w", createErr)
		}

		created = append(created, checkResp)
	}

	// Mark host as promoted, pointing at the first created check.
	firstUID := created[0].UID
	_, dbErr := s.db.NewUpdate().
		TableExpr("discovered_hosts").
		Set("promoted_to_check_uid = ?", firstUID).
		Where("uid = ?", hostUID).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Exec(ctx)

	if dbErr != nil {
		slog.ErrorContext(ctx, "failed to mark host as promoted (checks were created)",
			"host_uid", hostUID, "check_uid", firstUID, "error", dbErr)
		// Not fatal — the checks were created. Just log.
	}

	return created, nil
}

// SoftDeleteHost soft-deletes a discovered host.
func (s *Service) SoftDeleteHost(ctx context.Context, orgUID, hostUID string) error {
	now := time.Now()

	result, err := s.db.NewUpdate().
		TableExpr("discovered_hosts").
		Set("deleted_at = ?", now).
		Where("uid = ?", hostUID).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("soft delete host: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return ErrHostNotFound
	}

	return nil
}

// normalizeCheckType maps the discovery scan engine's check-type aliases to the
// registered check types accepted by the checks service. The suggest engine emits
// "ping" (matching the urlparse alias), which the registry knows only as "icmp".
func normalizeCheckType(checkType string) string {
	if checkType == "ping" {
		return "icmp"
	}

	return checkType
}

// buildCheckConfig constructs the check config merging suggested config with override.
func buildCheckConfig(host *models.DiscoveredHost, spec PromoteCheckSpec) (map[string]any, error) {
	config := make(map[string]any)

	// Try to find a matching suggested check for the requested type.
	var suggestions []disc.SuggestedCheck
	if len(host.SuggestedChecks) > 0 {
		if err := json.Unmarshal(host.SuggestedChecks, &suggestions); err != nil {
			return nil, fmt.Errorf("parse suggested checks: %w", err)
		}
	}

	for _, s := range suggestions {
		if s.Type == spec.CheckType {
			for k, v := range s.Config {
				config[k] = v
			}

			break
		}
	}

	// If no suggestion matched, use the host IP as the base.
	if len(config) == 0 {
		config["host"] = host.IP
	}

	// Merge ExtraConfig overrides.
	for k, v := range spec.ExtraConfig {
		config[k] = v
	}

	return config, nil
}
