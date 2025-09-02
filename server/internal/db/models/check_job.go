package models

import (
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// CheckJob represents a scheduled job for executing a check.
type CheckJob struct {
	UID             string             `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string             `bun:"organization_uid,notnull"`
	CheckUID        string             `bun:"check_uid"`
	Region          *string            `bun:"region"`
	Type            string             `bun:"type"`
	Config          JSONMap            `bun:"config,type:jsonb,nullzero"`
	Encrypted       bool               `bun:"encrypted,notnull,default:false"`
	Period          timeutils.Duration `bun:"period,notnull"`
	ScheduledAt     *time.Time         `bun:"scheduled_at"`
	LeaseWorkerUID  *string            `bun:"lease_worker_uid"`
	LeaseExpiresAt  *time.Time         `bun:"lease_expires_at"`
	LeaseStarts     int                `bun:"lease_starts,notnull,default:0"`
	UpdatedAt       time.Time          `bun:"updated_at,notnull,default:current_timestamp"`
}

// NewCheckJob creates a new check job with generated UID.
func NewCheckJob(orgUID string, checkUID string, period timeutils.Duration) *CheckJob {
	now := time.Now()

	return &CheckJob{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		CheckUID:        checkUID,
		Period:          period,
		Config:          make(JSONMap),
		ScheduledAt:     &now,
		UpdatedAt:       now,
	}
}

// CheckJobUpdate represents fields that can be updated.
type CheckJobUpdate struct {
	Region         *string
	Config         *JSONMap
	Encrypted      *bool
	Period         *timeutils.Duration
	ScheduledAt    *time.Time
	LeaseWorkerUID *string
	LeaseExpiresAt *time.Time
	LeaseStarts    *int
}
