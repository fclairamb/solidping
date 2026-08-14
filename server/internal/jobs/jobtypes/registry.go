package jobtypes

import (
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// jobDefinitionFactories maps each job type to a factory producing a fresh
// definition. A map keeps GetJobDefinition flat as the type set grows (a long
// switch trips the cyclomatic-complexity linter).
//
//nolint:gochecknoglobals // static lookup table, treated as a constant.
var jobDefinitionFactories = map[jobdef.JobType]func() jobdef.JobDefinition{
	jobdef.JobTypeSleep:            func() jobdef.JobDefinition { return &SleepJobDefinition{} },
	jobdef.JobTypeEmail:            func() jobdef.JobDefinition { return &EmailJobDefinition{} },
	jobdef.JobTypeWebhook:          func() jobdef.JobDefinition { return &WebhookJobDefinition{} },
	jobdef.JobTypeStartup:          func() jobdef.JobDefinition { return &StartupJobDefinition{} },
	jobdef.JobTypeAggregation:      func() jobdef.JobDefinition { return &AggregationJobDefinition{} },
	jobdef.JobTypeStateCleanup:     func() jobdef.JobDefinition { return &StateCleanupJobDefinition{} },
	jobdef.JobTypeNotification:     func() jobdef.JobDefinition { return &NotificationJobDefinition{} },
	jobdef.JobTypeSnoozeSweep:      func() jobdef.JobDefinition { return &SnoozeSweepJobDefinition{} },
	jobdef.JobTypeEscalationStep:   func() jobdef.JobDefinition { return &EscalationStepJobDefinition{} },
	jobdef.JobTypeNetworkDiscovery: func() jobdef.JobDefinition { return &NetworkDiscoveryJobDefinition{} },
	jobdef.JobTypeIncidentResolutionNotice: func() jobdef.JobDefinition {
		return &IncidentResolutionNoticeJobDefinition{}
	},
	jobdef.JobTypeNetworkDiscoveryPlan: func() jobdef.JobDefinition {
		return &NetworkDiscoveryPlanJobDefinition{}
	},
	jobdef.JobTypeFreeboxLanDiscovery: func() jobdef.JobDefinition { return &FreeboxLanDiscoveryJobDefinition{} },
	jobdef.JobTypeContainerDiscovery:  func() jobdef.JobDefinition { return &ContainerDiscoveryJobDefinition{} },
	jobdef.JobTypeKubernetesDiscovery: func() jobdef.JobDefinition { return &KubernetesDiscoveryJobDefinition{} },
	jobdef.JobTypeStuckJobReaper:      func() jobdef.JobDefinition { return &StuckJobReaperJobDefinition{} },
	jobdef.JobTypeJobsCleanup:         func() jobdef.JobDefinition { return &JobsCleanupJobDefinition{} },
	jobdef.JobTypeCustomDomainVerify:  func() jobdef.JobDefinition { return &CustomDomainVerifyJobDefinition{} },
	jobdef.JobTypeAgentGC:             func() jobdef.JobDefinition { return &AgentGCJobDefinition{} },
}

// GetJobDefinition retrieves a job definition by type.
//
//nolint:ireturn // registry pattern returns the JobDefinition interface
func GetJobDefinition(jobType jobdef.JobType) (jobdef.JobDefinition, bool) {
	factory, ok := jobDefinitionFactories[jobType]
	if !ok {
		return nil, false
	}

	return factory(), true
}
