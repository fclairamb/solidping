package models

import (
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// CheckStatus represents the health status of a check.
type CheckStatus int

const (
	// CheckStatusUnknown indicates the check status is not yet known.
	CheckStatusUnknown CheckStatus = 0
	// CheckStatusUp indicates the check is healthy.
	CheckStatusUp CheckStatus = 1
	// CheckStatusDown indicates the check is failing.
	CheckStatusDown CheckStatus = 2
	// CheckStatusDegraded indicates the check is experiencing issues (reserved for future use).
	CheckStatusDegraded CheckStatus = 3
)

// Check represents a monitoring configuration.
type Check struct {
	UID             string             `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string             `bun:"organization_uid,notnull"`
	CheckGroupUID   *string            `bun:"check_group_uid"`
	Name            *string            `bun:"name"`
	Slug            *string            `bun:"slug"`
	Description     *string            `bun:"description"`
	Type            string             `bun:"type,notnull"`
	Config          JSONMap            `bun:"config,type:jsonb,nullzero"`
	Regions         []string           `bun:"regions,type:text[],array"`
	Enabled         bool               `bun:"enabled,notnull"`
	Internal        bool               `bun:"internal,notnull,default:false"`
	Period          timeutils.Duration `bun:"period,notnull"`

	// Incident tracking thresholds
	IncidentThreshold   int `bun:"incident_threshold,notnull,default:1"`
	EscalationThreshold int `bun:"escalation_threshold,notnull,default:3"`
	RecoveryThreshold   int `bun:"recovery_threshold,notnull,default:1"`

	// Adaptive resolution settings (nil = use defaults)
	ReopenCooldownMultiplier *int `bun:"reopen_cooldown_multiplier"`
	MaxAdaptiveIncrease      *int `bun:"max_adaptive_increase"`

	// Status tracking
	Status          CheckStatus `bun:"status,notnull,default:0"`
	StatusStreak    int         `bun:"status_streak,notnull,default:0"`
	StatusChangedAt *time.Time  `bun:"status_changed_at"`

	CreatedAt time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt *time.Time `bun:"deleted_at"`
}

// NewCheck creates a new check with generated UID.
func NewCheck(orgUID, slug, checkType string) *Check {
	now := time.Now()

	var slugPtr *string
	if slug != "" {
		slugPtr = &slug
	}

	return &Check{
		UID:                 uuid.New().String(),
		OrganizationUID:     orgUID,
		Slug:                slugPtr,
		Type:                checkType,
		Config:              make(JSONMap),
		Enabled:             true,
		Period:              timeutils.Duration(time.Minute), // default to 1 minute
		IncidentThreshold:   3,
		EscalationThreshold: 10,
		RecoveryThreshold:   3,
		Status:              CheckStatusUnknown,
		StatusStreak:        0,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
}

// CheckUpdate represents fields that can be updated.
type CheckUpdate struct {
	CheckGroupUID *string
	Name          *string
	Slug          *string
	Description   *string
	Type          *string
	Config        *JSONMap
	Regions       *[]string
	Enabled       *bool
	Internal      *bool
	Period        *timeutils.Duration

	// Incident tracking thresholds
	IncidentThreshold   *int
	EscalationThreshold *int
	RecoveryThreshold   *int

	// Adaptive resolution settings
	ReopenCooldownMultiplier *int
	MaxAdaptiveIncrease      *int

	// Status tracking (internal use)
	Status          *CheckStatus
	StatusStreak    *int
	StatusChangedAt *time.Time
}

// Label represents a key-value pair for categorizing checks.
type Label struct {
	UID             string     `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string     `bun:"organization_uid,notnull"`
	Key             string     `bun:"key,notnull"`
	Value           string     `bun:"value,notnull"`
	CreatedAt       time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	DeletedAt       *time.Time `bun:"deleted_at"`
}

// NewLabel creates a new label with generated UID.
func NewLabel(orgUID, key, value string) *Label {
	now := time.Now()

	return &Label{
		UID:             uuid.New().String(),
		OrganizationUID: orgUID,
		Key:             key,
		Value:           value,
		CreatedAt:       now,
	}
}

// CheckLabel represents the many-to-many relationship between checks and labels.
type CheckLabel struct {
	UID       string    `bun:"uid,pk,type:varchar(36)"`
	CheckUID  string    `bun:"check_uid,notnull"`
	LabelUID  string    `bun:"label_uid,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull,default:current_timestamp"`
}

// NewCheckLabel creates a new check-label relationship with generated UID.
func NewCheckLabel(checkUID, labelUID string) *CheckLabel {
	return &CheckLabel{
		UID:       uuid.New().String(),
		CheckUID:  checkUID,
		LabelUID:  labelUID,
		CreatedAt: time.Now(),
	}
}

// ListChecksFilter provides filtering options for listing checks.
type ListChecksFilter struct {
	Labels          map[string]string // key:value pairs for AND filtering
	CheckGroupUID   *string           // filter by check group UID; "none" = ungrouped checks only
	Query           string            // search term for name/slug (case-insensitive substring)
	Internal        *string           // "true", "false", or "all" — filter by internal status
	Limit           int               // max results to return (0 = no limit)
	CursorCreatedAt *time.Time        // cursor: created_at of last item from previous page
	CursorUID       *string           // cursor: uid of last item from previous page
}
