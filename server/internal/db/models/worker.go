package models

import (
	"time"

	"github.com/google/uuid"
)

// Worker represents a distributed worker that executes checks.
type Worker struct {
	UID          string     `bun:"uid,pk,type:varchar(36)"`
	Slug         string     `bun:"slug,notnull"`
	Name         string     `bun:"name,notnull"`
	Region       *string    `bun:"region"`
	Token        *string    `bun:"token"`
	LastActiveAt *time.Time `bun:"last_active_at"`
	CreatedAt    time.Time  `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt    time.Time  `bun:"updated_at,notnull,default:current_timestamp"`
	DeletedAt    *time.Time `bun:"deleted_at"`
}

// NewWorker creates a new worker with generated UID.
func NewWorker(slug, name string) *Worker {
	now := time.Now()

	return &Worker{
		UID:       uuid.New().String(),
		Slug:      slug,
		Name:      name,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// WorkerUpdate represents fields that can be updated.
type WorkerUpdate struct {
	Slug         *string
	Name         *string
	Region       *string
	LastActiveAt *time.Time
}
