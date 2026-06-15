// Package checkjobs provides read-only HTTP access to the check_jobs schedule
// table and aggregate stats over both the background jobs queue and the check
// schedule. It is admin-only and never exposes encrypted secret values.
package checkjobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrCheckJobNotFound is returned when a check job cannot be found.
var ErrCheckJobNotFound = errors.New("check job not found")

// crashLoopThreshold is the lease_starts value above which a check job is
// considered crash-looping (mirrors the worker claim logic in the spec).
const crashLoopThreshold = 9

// failedWindow is the look-back window used for the "failed (24h)" stat.
const failedWindow = 24 * time.Hour

// DerivedState describes the lease-derived health of a check job.
type DerivedState string

const (
	// StateIdle means the row has no active lease.
	StateIdle DerivedState = "idle"
	// StateInFlight means the row is leased and the lease has not expired.
	StateInFlight DerivedState = "inFlight"
	// StateStalled means the row is leased but the lease has expired.
	StateStalled DerivedState = "stalled"
	// StateCrashLooping means the row has been claimed too many times.
	StateCrashLooping DerivedState = "crashLooping"
)

// Service provides read-only check-job queries.
type Service interface {
	// ListCheckJobs lists check jobs. When orgUID is empty, all orgs are
	// returned (system / super-admin mode).
	ListCheckJobs(ctx context.Context, orgUID string, opts ListOptions) ([]*CheckJobView, error)
	// GetCheckJob returns a single check job. When orgUID is non-empty, the
	// row must belong to that org or ErrCheckJobNotFound is returned.
	GetCheckJob(ctx context.Context, orgUID, uid string) (*CheckJobView, error)
	// Stats computes aggregate counts for the overview strip. When orgUID is
	// empty, counts span all orgs.
	Stats(ctx context.Context, orgUID string) (*Stats, error)
}

// ListOptions holds pagination options for listing check jobs.
type ListOptions struct {
	Limit  int
	Offset int
}

// CheckJobView is the redacted, API-facing representation of a check_jobs row.
// It deliberately omits config_private / config_private_keys values and only
// exposes the *names* of encrypted keys.
type CheckJobView struct {
	UID             string         `json:"uid"`
	OrganizationUID string         `json:"organizationUid"`
	CheckUID        string         `json:"checkUid"`
	CheckName       *string        `json:"checkName"`
	Region          *string        `json:"region"`
	Type            string         `json:"type"`
	Config          models.JSONMap `json:"config"`
	EncryptedKeys   []string       `json:"encryptedKeys"`
	Encrypted       bool           `json:"encrypted"`
	PeriodSeconds   int64          `json:"periodSeconds"`
	ScheduledAt     *time.Time     `json:"scheduledAt"`
	LeaseWorkerUID  *string        `json:"leaseWorkerUid"`
	LeaseExpiresAt  *time.Time     `json:"leaseExpiresAt"`
	LeaseStarts     int            `json:"leaseStarts"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	State           DerivedState   `json:"state"`
}

// Stats holds aggregate counts over background jobs and the check schedule.
type Stats struct {
	Jobs   JobStats      `json:"jobs"`
	Checks CheckJobStats `json:"checks"`
}

// JobStats holds background-jobs queue counts.
type JobStats struct {
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Failed24h int `json:"failed24h"`
}

// CheckJobStats holds check-schedule counts.
type CheckJobStats struct {
	Total        int `json:"total"`
	DueNow       int `json:"dueNow"`
	InFlight     int `json:"inFlight"`
	Stalled      int `json:"stalled"`
	CrashLooping int `json:"crashLooping"`
}

type serviceImpl struct {
	db *bun.DB
}

// NewService creates a new check-jobs read service.
func NewService(db *bun.DB) Service {
	return &serviceImpl{db: db}
}

// computeState derives the lease-based state of a check job.
func computeState(cj *models.CheckJob, now time.Time) DerivedState {
	if cj.LeaseStarts > crashLoopThreshold {
		return StateCrashLooping
	}

	if cj.LeaseExpiresAt == nil {
		return StateIdle
	}

	if cj.LeaseExpiresAt.After(now) {
		return StateInFlight
	}

	return StateStalled
}

// parseEncryptedKeys parses the config_private_keys JSON array into a slice of
// key names. Values are never returned — only the names.
func parseEncryptedKeys(raw *string) []string {
	if raw == nil || *raw == "" {
		return []string{}
	}

	var keys []string
	if err := json.Unmarshal([]byte(*raw), &keys); err != nil {
		// Malformed: report nothing rather than leaking the raw envelope.
		return []string{}
	}

	if keys == nil {
		return []string{}
	}

	return keys
}

// toView converts a check_jobs row into the redacted API view. checkName, when
// non-nil, is the resolved name of the associated check.
func toView(cj *models.CheckJob, checkName *string, now time.Time) *CheckJobView {
	return &CheckJobView{
		UID:             cj.UID,
		OrganizationUID: cj.OrganizationUID,
		CheckUID:        cj.CheckUID,
		CheckName:       checkName,
		Region:          cj.Region,
		Type:            cj.Type,
		Config:          cj.Config,
		EncryptedKeys:   parseEncryptedKeys(cj.ConfigPrivateKeys),
		Encrypted:       cj.Encrypted,
		PeriodSeconds:   int64(time.Duration(cj.Period).Seconds()),
		ScheduledAt:     cj.ScheduledAt,
		LeaseWorkerUID:  cj.LeaseWorkerUID,
		LeaseExpiresAt:  cj.LeaseExpiresAt,
		LeaseStarts:     cj.LeaseStarts,
		UpdatedAt:       cj.UpdatedAt,
		State:           computeState(cj, now),
	}
}

// checkNames resolves a set of check UIDs to their names in one query. The
// Check struct on CheckJob is transient (bun:"-"), so it is not auto-loaded.
func (s *serviceImpl) checkNames(ctx context.Context, checkUIDs []string) (map[string]*string, error) {
	result := make(map[string]*string, len(checkUIDs))
	if len(checkUIDs) == 0 {
		return result, nil
	}

	var checks []struct {
		UID  string  `bun:"uid"`
		Name *string `bun:"name"`
	}

	err := s.db.NewSelect().
		Model((*models.Check)(nil)).
		Column("uid", "name").
		Where("uid IN (?)", bun.In(checkUIDs)).
		Scan(ctx, &checks)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve check names: %w", err)
	}

	for i := range checks {
		result[checks[i].UID] = checks[i].Name
	}

	return result, nil
}

func (s *serviceImpl) ListCheckJobs(
	ctx context.Context, orgUID string, opts ListOptions,
) ([]*CheckJobView, error) {
	var rows []*models.CheckJob

	query := s.db.NewSelect().
		Model(&rows).
		Order("scheduled_at ASC")

	if orgUID != "" {
		query = query.Where("organization_uid = ?", orgUID)
	}

	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}

	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, fmt.Errorf("failed to list check jobs: %w", err)
	}

	checkUIDs := make([]string, 0, len(rows))
	for _, cj := range rows {
		if cj.CheckUID != "" {
			checkUIDs = append(checkUIDs, cj.CheckUID)
		}
	}

	names, err := s.checkNames(ctx, checkUIDs)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	views := make([]*CheckJobView, 0, len(rows))

	for _, cj := range rows {
		views = append(views, toView(cj, names[cj.CheckUID], now))
	}

	return views, nil
}

func (s *serviceImpl) GetCheckJob(ctx context.Context, orgUID, uid string) (*CheckJobView, error) {
	var cj models.CheckJob

	query := s.db.NewSelect().
		Model(&cj).
		Where("uid = ?", uid)

	if orgUID != "" {
		query = query.Where("organization_uid = ?", orgUID)
	}

	err := query.Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrCheckJobNotFound, uid)
	} else if err != nil {
		return nil, fmt.Errorf("failed to get check job: %w", err)
	}

	names, err := s.checkNames(ctx, []string{cj.CheckUID})
	if err != nil {
		return nil, err
	}

	return toView(&cj, names[cj.CheckUID], time.Now()), nil
}

// jobStatusCount is a scan target for the GROUP BY status query.
type jobStatusCount struct {
	Status string `bun:"status"`
	Count  int    `bun:"count"`
}

func (s *serviceImpl) Stats(ctx context.Context, orgUID string) (*Stats, error) {
	stats := &Stats{}

	if err := s.jobStats(ctx, orgUID, &stats.Jobs); err != nil {
		return nil, err
	}

	if err := s.checkJobStats(ctx, orgUID, &stats.Checks); err != nil {
		return nil, err
	}

	return stats, nil
}

func (s *serviceImpl) jobStats(ctx context.Context, orgUID string, out *JobStats) error {
	var counts []jobStatusCount

	query := s.db.NewSelect().
		Model((*models.Job)(nil)).
		Column("status").
		ColumnExpr("COUNT(*) AS count").
		Where("deleted_at IS NULL").
		Group("status")

	if orgUID != "" {
		query = query.Where("organization_uid = ?", orgUID)
	}

	if err := query.Scan(ctx, &counts); err != nil {
		return fmt.Errorf("failed to count jobs by status: %w", err)
	}

	for _, c := range counts {
		switch models.JobStatus(c.Status) {
		case models.JobStatusPending:
			out.Pending = c.Count
		case models.JobStatusRunning:
			out.Running = c.Count
		case models.JobStatusSuccess, models.JobStatusRetried, models.JobStatusFailed:
			// success/retried not surfaced here; failed handled below with a 24h window.
		}
	}

	since := time.Now().Add(-failedWindow)

	failedQuery := s.db.NewSelect().
		Model((*models.Job)(nil)).
		Where("deleted_at IS NULL").
		Where("status = ?", models.JobStatusFailed).
		Where("updated_at >= ?", since)

	if orgUID != "" {
		failedQuery = failedQuery.Where("organization_uid = ?", orgUID)
	}

	failed, err := failedQuery.Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to count failed jobs: %w", err)
	}

	out.Failed24h = failed

	return nil
}

func (s *serviceImpl) checkJobStats(ctx context.Context, orgUID string, out *CheckJobStats) error {
	now := time.Now()

	newBase := func() *bun.SelectQuery {
		q := s.db.NewSelect().Model((*models.CheckJob)(nil))
		if orgUID != "" {
			q = q.Where("organization_uid = ?", orgUID)
		}

		return q
	}

	total, err := newBase().Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to count check jobs: %w", err)
	}

	out.Total = total

	// Due now: scheduled in the past and not currently leased.
	dueNow, err := newBase().
		Where("scheduled_at <= ?", now).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.
				WhereOr("lease_expires_at IS NULL").
				WhereOr("lease_expires_at < ?", now)
		}).
		Where("lease_starts <= ?", crashLoopThreshold).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to count due check jobs: %w", err)
	}

	out.DueNow = dueNow

	inFlight, err := newBase().
		Where("lease_expires_at IS NOT NULL").
		Where("lease_expires_at > ?", now).
		Where("lease_starts <= ?", crashLoopThreshold).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to count in-flight check jobs: %w", err)
	}

	out.InFlight = inFlight

	stalled, err := newBase().
		Where("lease_expires_at IS NOT NULL").
		Where("lease_expires_at < ?", now).
		Where("lease_starts <= ?", crashLoopThreshold).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to count stalled check jobs: %w", err)
	}

	out.Stalled = stalled

	crashLooping, err := newBase().
		Where("lease_starts > ?", crashLoopThreshold).
		Count(ctx)
	if err != nil {
		return fmt.Errorf("failed to count crash-looping check jobs: %w", err)
	}

	out.CrashLooping = crashLooping

	return nil
}
