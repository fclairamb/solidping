package jobdef

// JobType represents the type of a job.
type JobType string

// Supported job types.
const (
	// JobTypeSleep is a simple sleep job for testing.
	JobTypeSleep JobType = "sleep"
	// JobTypeEmail sends an email notification.
	JobTypeEmail JobType = "email"
	// JobTypeWebhook sends a webhook notification.
	JobTypeWebhook JobType = "webhook"
	// JobTypeStartup executes a startup check.
	JobTypeStartup JobType = "startup"
	// JobTypeAggregation aggregates results into hourly, daily, and monthly summaries.
	JobTypeAggregation JobType = "aggregation"
	// JobTypeStateCleanup removes expired state entries.
	JobTypeStateCleanup JobType = "state_cleanup"
	// JobTypeNotification sends incident notifications via connections.
	JobTypeNotification JobType = "notification"
	// JobTypeSnoozeSweep auto-unsnoozes incidents whose snooze window has passed.
	JobTypeSnoozeSweep JobType = "snooze_sweep"
)
