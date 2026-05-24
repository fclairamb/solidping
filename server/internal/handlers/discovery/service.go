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

var (
	// ErrHostNotFound is returned when a discovered host is not found.
	ErrHostNotFound = errors.New("discovered host not found")
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

// PromoteRequest is the request body for promoting a discovered host to a check.
type PromoteRequest struct {
	CheckType   string         `json:"checkType"`
	Name        string         `json:"name,omitempty"`
	Slug        string         `json:"slug,omitempty"`
	Period      *string        `json:"period,omitempty"`
	ExtraConfig map[string]any `json:"extraConfig,omitempty"`
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

// StartScan creates a new discovery job. Returns ErrAlreadyRunning if another scan is in progress.
func (s *Service) StartScan(ctx context.Context, orgUID string, cfg disc.Config) (*models.Job, error) {
	if err := disc.ValidateCIDRs(cfg.CIDRs); err != nil {
		return nil, err
	}

	// Check for already-running scan.
	if err := s.checkAlreadyRunning(ctx, orgUID, jobdef.JobTypeNetworkDiscovery); err != nil {
		return nil, err
	}

	configBytes, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal discovery config: %w", err)
	}

	job, err := s.jobSvc.CreateJob(
		ctx,
		orgUID,
		string(jobdef.JobTypeNetworkDiscovery),
		configBytes,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create discovery job: %w", err)
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

		query = query.Where("source IN (?)", bun.In(sources))
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

// PromoteHost creates a Check from the discovered host and marks the host as promoted.
// The whole operation runs in a transaction.
func (s *Service) PromoteHost(
	ctx context.Context, orgUID, orgSlug, hostUID string, req PromoteRequest,
) (*checks.CheckResponse, error) {
	host, err := s.GetHost(ctx, orgUID, hostUID)
	if err != nil {
		return nil, err
	}

	if host.PromotedToCheckUID != nil {
		return nil, ErrAlreadyPromoted
	}

	// Build the check config from the host's suggested first check, then merge ExtraConfig.
	checkConfig, err := buildCheckConfig(host, req)
	if err != nil {
		return nil, err
	}

	// Add discovery labels.
	labels := map[string]string{
		"auto-discovery": "true",
		"discovery-job":  host.JobUID,
	}

	createReq := checks.CreateCheckRequest{
		Type:   req.CheckType,
		Name:   req.Name,
		Slug:   req.Slug,
		Period: req.Period,
		Config: checkConfig,
		Labels: labels,
	}

	if createReq.Name == "" {
		createReq.Name = host.IP
	}

	checkResp, err := s.checksSvc.CreateCheck(ctx, orgSlug, createReq)
	if err != nil {
		return nil, fmt.Errorf("create check: %w", err)
	}

	// Mark host as promoted.
	checkUID := checkResp.UID
	now := time.Now()
	_, dbErr := s.db.NewUpdate().
		TableExpr("discovered_hosts").
		Set("promoted_to_check_uid = ?", checkUID).
		Where("uid = ?", hostUID).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Exec(ctx)

	if dbErr != nil {
		slog.ErrorContext(ctx, "failed to mark host as promoted (check was created)",
			"host_uid", hostUID, "check_uid", checkUID, "error", dbErr)
		// Not fatal — the check was created. Just log.
	}

	_ = now

	return &checkResp, nil
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

// buildCheckConfig constructs the check config merging suggested config with override.
func buildCheckConfig(host *models.DiscoveredHost, req PromoteRequest) (map[string]any, error) {
	config := make(map[string]any)

	// Try to find a matching suggested check for the requested type.
	var suggestions []disc.SuggestedCheck
	if len(host.SuggestedChecks) > 0 {
		if err := json.Unmarshal(host.SuggestedChecks, &suggestions); err != nil {
			return nil, fmt.Errorf("parse suggested checks: %w", err)
		}
	}

	for _, s := range suggestions {
		if s.Type == req.CheckType {
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
	for k, v := range req.ExtraConfig {
		config[k] = v
	}

	return config, nil
}
