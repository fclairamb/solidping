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
	// JobTypeEscalationStep fires one step of an escalation policy for an incident.
	// Canceled via the same incidentUid match the notification job uses, so
	// ack/snooze/resolve drops the rest of the policy without extra wiring.
	JobTypeEscalationStep JobType = "escalation_step"
	// JobTypeNetworkDiscovery scans a set of CIDR ranges for responsive hosts and
	// records them in the discovered_hosts table for operator review and promotion.
	JobTypeNetworkDiscovery JobType = "network_discovery"
	// JobTypeFreeboxLanDiscovery queries a paired Freebox channel's LAN browser
	// and records the hosts it knows about in the discovered_hosts table
	// (source='freebox') so they share the LAN-scan promote/dismiss UX.
	JobTypeFreeboxLanDiscovery JobType = "freebox_lan_discovery"
)
