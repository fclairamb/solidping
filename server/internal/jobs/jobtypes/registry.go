package jobtypes

import (
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// GetJobDefinition retrieves a job definition by type.
func GetJobDefinition(jobType jobdef.JobType) (jobdef.JobDefinition, bool) {
	switch jobType {
	case jobdef.JobTypeSleep:
		return &SleepJobDefinition{}, true
	case jobdef.JobTypeEmail:
		return &EmailJobDefinition{}, true
	case jobdef.JobTypeWebhook:
		return &WebhookJobDefinition{}, true
	case jobdef.JobTypeStartup:
		return &StartupJobDefinition{}, true
	case jobdef.JobTypeAggregation:
		return &AggregationJobDefinition{}, true
	case jobdef.JobTypeStateCleanup:
		return &StateCleanupJobDefinition{}, true
	case jobdef.JobTypeNotification:
		return &NotificationJobDefinition{}, true
	}

	return nil, false
}
