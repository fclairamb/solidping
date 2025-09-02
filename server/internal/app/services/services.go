// Package services provides centralized service registry for dependency injection.
package services

import (
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// Registry holds all application services for dependency injection.
type Registry struct {
	Jobs           jobsvc.Service
	CheckJobs      checkjobsvc.Service
	EventNotifier  notifier.EventNotifier
	EmailSender    email.Sender
	EmailFormatter email.Formatter
}

// NewRegistry creates a new services registry.
func NewRegistry() *Registry {
	return &Registry{}
}
