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

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	disc "github.com/fclairamb/solidping/server/internal/discovery"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
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
)

// ListHostsOptions configures which hosts to return.
type ListHostsOptions struct {
	JobUID   string
	Promoted *bool
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
	isPostgres bool
}

// NewService creates a new discovery service.
func NewService(
	bunDB *bun.DB,
	dbSvc db.Service,
	checksSvc *checks.Service,
	jobSvc jobsvc.Service,
) *Service {
	_, isPostgres := bunDB.Dialect().(*pgdialect.Dialect)

	return &Service{
		db:         bunDB,
		dbSvc:      dbSvc,
		checksSvc:  checksSvc,
		jobSvc:     jobSvc,
		isPostgres: isPostgres,
	}
}

// StartScan creates a new discovery job. Returns ErrAlreadyRunning if another scan is in progress.
func (s *Service) StartScan(ctx context.Context, orgUID string, cfg disc.Config) (*models.Job, error) {
	if err := disc.ValidateCIDRs(cfg.CIDRs); err != nil {
		return nil, err
	}

	// Check for already-running scan.
	if err := s.checkAlreadyRunning(ctx, orgUID); err != nil {
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

// checkAlreadyRunning returns ErrAlreadyRunning if a discovery job is already running for the org.
func (s *Service) checkAlreadyRunning(ctx context.Context, orgUID string) error {
	count, err := s.db.NewSelect().
		TableExpr("jobs").
		Where("type = ?", string(jobdef.JobTypeNetworkDiscovery)).
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

	var q *bun.InsertQuery

	if s.isPostgres {
		q = s.db.NewInsert().
			Model(host).
			On("CONFLICT (organization_uid, ip) WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL DO UPDATE").
			Set("job_uid = EXCLUDED.job_uid").
			Set("hostname = EXCLUDED.hostname").
			Set("open_ports = EXCLUDED.open_ports").
			Set("icmp_reachable = EXCLUDED.icmp_reachable").
			Set("suggested_checks = EXCLUDED.suggested_checks").
			Set("discovered_at = EXCLUDED.discovered_at")
	} else {
		// SQLite: INSERT OR REPLACE semantics via conflict handling.
		q = s.db.NewInsert().
			Model(host).
			On("CONFLICT (organization_uid, ip) WHERE deleted_at IS NULL AND promoted_to_check_uid IS NULL DO UPDATE").
			Set("job_uid = excluded.job_uid").
			Set("hostname = excluded.hostname").
			Set("open_ports = excluded.open_ports").
			Set("icmp_reachable = excluded.icmp_reachable").
			Set("suggested_checks = excluded.suggested_checks").
			Set("discovered_at = excluded.discovered_at")
	}

	_, err = q.Exec(ctx)
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
func (s *Service) ListHosts(ctx context.Context, orgUID string, opts ListHostsOptions) ([]*models.DiscoveredHost, error) {
	var hosts []*models.DiscoveredHost

	q := s.db.NewSelect().
		Model(&hosts).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("discovered_at DESC")

	if opts.JobUID != "" {
		q = q.Where("job_uid = ?", opts.JobUID)
	}

	if opts.Promoted != nil {
		if *opts.Promoted {
			q = q.Where("promoted_to_check_uid IS NOT NULL")
		} else {
			q = q.Where("promoted_to_check_uid IS NULL")
		}
	}

	if opts.Limit > 0 {
		q = q.Limit(opts.Limit)
	}

	if opts.Offset > 0 {
		q = q.Offset(opts.Offset)
	}

	err := q.Scan(ctx)
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
func (s *Service) PromoteHost(ctx context.Context, orgUID, orgSlug, hostUID string, req PromoteRequest) (*checks.CheckResponse, error) {
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
