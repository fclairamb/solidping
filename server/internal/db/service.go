// Package db provides the database abstraction layer for solidping.
package db

import (
	"context"
	"io"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Service defines the common interface for database operations.
// Both PostgreSQL and SQLite implementations must satisfy this interface.
//
//nolint:interfacebloat // This interface defines the complete database API
type Service interface {
	// Initialize sets up the database schema (runs migrations)
	Initialize(ctx context.Context) error

	// DB returns the underlying bun.DB instance for direct queries
	DB() *bun.DB

	// Organization operations
	CreateOrganization(ctx context.Context, org *models.Organization) error
	GetOrganization(ctx context.Context, uid string) (*models.Organization, error)
	GetOrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error)
	ListOrganizations(ctx context.Context) ([]*models.Organization, error)
	UpdateOrganization(ctx context.Context, uid string, update models.OrganizationUpdate) error
	DeleteOrganization(ctx context.Context, uid string) error

	// OrganizationProvider operations - single source of truth for org↔provider mapping
	CreateOrganizationProvider(ctx context.Context, provider *models.OrganizationProvider) error
	GetOrganizationProvider(ctx context.Context, uid string) (*models.OrganizationProvider, error)
	GetOrganizationProviderByProviderID(
		ctx context.Context, providerType models.ProviderType, providerID string,
	) (*models.OrganizationProvider, error)
	ListOrganizationProviders(ctx context.Context, orgUID string) ([]*models.OrganizationProvider, error)
	UpdateOrganizationProvider(ctx context.Context, uid string, update models.OrganizationProviderUpdate) error
	DeleteOrganizationProvider(ctx context.Context, uid string) error

	// User operations
	CreateUser(ctx context.Context, user *models.User) error
	GetUser(ctx context.Context, uid string) (*models.User, error)
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)
	ListUsers(ctx context.Context) ([]*models.User, error)
	UpdateUser(ctx context.Context, uid string, update *models.UserUpdate) error
	DeleteUser(ctx context.Context, uid string) error

	// UserProvider operations
	CreateUserProvider(ctx context.Context, provider *models.UserProvider) error
	GetUserProvider(ctx context.Context, uid string) (*models.UserProvider, error)
	GetUserProviderByProviderID(
		ctx context.Context, providerType models.ProviderType, providerID string,
	) (*models.UserProvider, error)
	ListUserProvidersByUser(ctx context.Context, userUID string) ([]*models.UserProvider, error)
	DeleteUserProvider(ctx context.Context, uid string) error

	// OrganizationMember operations
	CreateOrganizationMember(ctx context.Context, member *models.OrganizationMember) error
	GetOrganizationMember(ctx context.Context, uid string) (*models.OrganizationMember, error)
	GetMemberByUserAndOrg(ctx context.Context, userUID, orgUID string) (*models.OrganizationMember, error)
	ListMembersByOrg(ctx context.Context, orgUID string) ([]*models.OrganizationMember, error)
	ListMembersByUser(ctx context.Context, userUID string) ([]*models.OrganizationMember, error)
	UpdateOrganizationMember(ctx context.Context, uid string, update models.OrganizationMemberUpdate) error
	DeleteOrganizationMember(ctx context.Context, uid string) error
	CountAdminsByOrg(ctx context.Context, orgUID string) (int, error)

	// UserToken operations
	CreateUserToken(ctx context.Context, token *models.UserToken) error
	GetUserToken(ctx context.Context, uid string) (*models.UserToken, error)
	GetUserTokenByToken(ctx context.Context, token string) (*models.UserToken, error)
	ListUserTokens(ctx context.Context, userUID string) ([]*models.UserToken, error)
	ListUserTokensByType(ctx context.Context, userUID string, tokenType models.TokenType) ([]*models.UserToken, error)
	UpdateUserToken(ctx context.Context, uid string, update models.UserTokenUpdate) error
	DeleteUserToken(ctx context.Context, uid string) error

	// Worker operations
	CreateWorker(ctx context.Context, worker *models.Worker) error
	GetWorker(ctx context.Context, uid string) (*models.Worker, error)
	GetWorkerBySlug(ctx context.Context, slug string) (*models.Worker, error)
	ListWorkers(ctx context.Context) ([]*models.Worker, error)
	UpdateWorker(ctx context.Context, uid string, update models.WorkerUpdate) error
	DeleteWorker(ctx context.Context, uid string) error
	// RegisterOrUpdateWorker finds a worker by slug, creates it if not found, or updates it if exists.
	// Returns the registered/updated worker.
	RegisterOrUpdateWorker(ctx context.Context, worker *models.Worker) (*models.Worker, error)
	// UpdateWorkerHeartbeat updates the worker's last_active_at and updated_at timestamps.
	UpdateWorkerHeartbeat(ctx context.Context, workerUID string) error

	// Check operations
	CreateCheck(ctx context.Context, check *models.Check) error
	GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error)
	GetCheckByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.Check, error)
	// GetCheckByEmailToken finds an email-type check by its config.token across all
	// organizations. The token alone is unique because it's 24 random bytes.
	GetCheckByEmailToken(ctx context.Context, token string) (*models.Check, error)
	ListChecks(ctx context.Context, orgUID string, filter *models.ListChecksFilter) ([]*models.Check, int64, error)
	UpdateCheck(ctx context.Context, uid string, update *models.CheckUpdate) error
	DeleteCheck(ctx context.Context, uid string) error

	// CheckJob operations
	ListCheckJobsByCheckUID(ctx context.Context, checkUID string) ([]*models.CheckJob, error)
	DeleteCheckJob(ctx context.Context, uid string) error
	CreateCheckJob(ctx context.Context, job *models.CheckJob) error

	// Label operations
	GetOrCreateLabel(ctx context.Context, orgUID, key, value string) (*models.Label, error)
	SetCheckLabels(ctx context.Context, checkUID string, labelUIDs []string) error
	GetLabelsForCheck(ctx context.Context, checkUID string) ([]*models.Label, error)
	GetLabelsForChecks(ctx context.Context, checkUIDs []string) (map[string][]*models.Label, error)
	ListDistinctLabelKeys(
		ctx context.Context, orgUID, query string, limit int,
	) ([]models.LabelSuggestion, error)
	ListDistinctLabelValues(
		ctx context.Context, orgUID, key, query string, limit int,
	) ([]models.LabelSuggestion, error)

	// Result operations
	CreateResult(ctx context.Context, result *models.Result) error
	GetResult(ctx context.Context, uid string) (*models.Result, error)
	ListResults(ctx context.Context, filter *models.ListResultsFilter) (*models.ListResultsResponse, error)
	GetLastResultForChecks(ctx context.Context, checkUIDs []string) (map[string]*models.Result, error)
	GetLastStatusChangeForChecks(ctx context.Context, checkUIDs []string) (map[string]*models.LastStatusChange, error)
	DeleteResults(ctx context.Context, orgUID string, resultUIDs []string) (int64, error)
	// SaveResultWithStatusTracking atomically clears old last_for_status for the check+status
	// combination and inserts a new result with last_for_status = true.
	SaveResultWithStatusTracking(ctx context.Context, result *models.Result) error

	// Incident operations
	CreateIncident(ctx context.Context, incident *models.Incident) error
	GetIncident(ctx context.Context, orgUID, uid string) (*models.Incident, error)
	// FindActiveIncidentByCheckUID returns the incident a check is participating in, whether
	// per-check (incidents.check_uid = $1) or via a group (incident_member_checks row exists
	// with currently_failing = true). Returns sql.ErrNoRows if none.
	FindActiveIncidentByCheckUID(ctx context.Context, checkUID string) (*models.Incident, error)
	FindRecentlyResolvedIncidentByCheckUID(ctx context.Context, checkUID string, since time.Time) (*models.Incident, error)
	// FindActiveIncidentByGroupUID returns the active group incident keyed on check_group_uid.
	FindActiveIncidentByGroupUID(ctx context.Context, groupUID string) (*models.Incident, error)
	// FindRecentlyResolvedIncidentByGroupUID returns the most recent resolved group incident
	// for a group resolved after `since`. Used for the reopen-within-cooldown path.
	FindRecentlyResolvedIncidentByGroupUID(ctx context.Context, groupUID string, since time.Time) (*models.Incident, error)
	ListIncidents(ctx context.Context, filter *models.ListIncidentsFilter) ([]*models.Incident, error)
	UpdateIncident(ctx context.Context, uid string, update *models.IncidentUpdate) error
	CountActiveIncidentsByCheckUID(ctx context.Context, checkUID string) (int, error)
	// ListExpiredSnoozedIncidents returns active incidents whose snoozed_until <= now.
	// Used by the auto-unsnooze sweeper.
	ListExpiredSnoozedIncidents(ctx context.Context, now time.Time) ([]*models.Incident, error)

	// On-call schedule operations
	CreateOnCallSchedule(ctx context.Context, schedule *models.OnCallSchedule) error
	GetOnCallSchedule(ctx context.Context, orgUID, scheduleUID string) (*models.OnCallSchedule, error)
	GetOnCallScheduleBySlug(ctx context.Context, orgUID, slug string) (*models.OnCallSchedule, error)
	GetOnCallScheduleByICalSecret(ctx context.Context, secret string) (*models.OnCallSchedule, error)
	ListOnCallSchedules(ctx context.Context, orgUID string) ([]*models.OnCallSchedule, error)
	UpdateOnCallSchedule(ctx context.Context, scheduleUID string, update *models.OnCallScheduleUpdate) error
	DeleteOnCallSchedule(ctx context.Context, scheduleUID string) error

	// On-call schedule users (roster) — replace-all is the typical write path
	ListOnCallScheduleUsers(ctx context.Context, scheduleUID string) ([]*models.OnCallScheduleUser, error)
	ReplaceOnCallScheduleUsers(ctx context.Context, scheduleUID string, userUIDs []string) error

	// On-call schedule overrides
	CreateOnCallScheduleOverride(ctx context.Context, override *models.OnCallScheduleOverride) error
	ListOnCallScheduleOverrides(
		ctx context.Context, scheduleUID string, from, until *time.Time,
	) ([]*models.OnCallScheduleOverride, error)
	GetOnCallScheduleOverride(ctx context.Context, overrideUID string) (*models.OnCallScheduleOverride, error)
	DeleteOnCallScheduleOverride(ctx context.Context, overrideUID string) error

	// Escalation policies (header)
	CreateEscalationPolicy(ctx context.Context, policy *models.EscalationPolicy) error
	GetEscalationPolicy(ctx context.Context, orgUID, policyUID string) (*models.EscalationPolicy, error)
	GetEscalationPolicyBySlug(ctx context.Context, orgUID, slug string) (*models.EscalationPolicy, error)
	ListEscalationPolicies(ctx context.Context, orgUID string) ([]*models.EscalationPolicy, error)
	UpdateEscalationPolicy(ctx context.Context, policyUID string, update *models.EscalationPolicyUpdate) error
	DeleteEscalationPolicy(ctx context.Context, policyUID string) error

	// Escalation policy steps (replace-all is the typical write path)
	ListEscalationPolicySteps(ctx context.Context, policyUID string) ([]*models.EscalationPolicyStep, error)
	ReplaceEscalationPolicySteps(
		ctx context.Context, policyUID string, steps []*models.EscalationPolicyStep,
		targetsByStepIdx map[int][]*models.EscalationPolicyTarget,
	) error
	ListEscalationPolicyTargets(ctx context.Context, stepUIDs []string) ([]*models.EscalationPolicyTarget, error)

	// Incident member operations (group incidents only)
	ListIncidentMemberChecks(ctx context.Context, incidentUID string) ([]*models.IncidentMemberCheck, error)
	GetIncidentMemberCheck(ctx context.Context, incidentUID, checkUID string) (*models.IncidentMemberCheck, error)
	UpsertIncidentMemberCheck(ctx context.Context, member *models.IncidentMemberCheck) error
	UpdateIncidentMemberCheck(ctx context.Context, incidentUID, checkUID string, update *models.IncidentMemberUpdate) error
	CountFailingIncidentMembers(ctx context.Context, incidentUID string) (int, error)

	// Check status update
	UpdateCheckStatus(
		ctx context.Context, checkUID string, status models.CheckStatus, streak int, changedAt *time.Time,
	) error

	// Event operations
	CreateEvent(ctx context.Context, event *models.Event) error
	ListEvents(ctx context.Context, filter *models.ListEventsFilter) ([]*models.Event, error)

	// Job operations
	CreateJob(ctx context.Context, job *models.Job) error
	GetJob(ctx context.Context, uid string) (*models.Job, error)
	ListJobs(ctx context.Context, orgUID *string, limit int) ([]*models.Job, error)
	UpdateJob(ctx context.Context, uid string, update models.JobUpdate) error
	DeleteJob(ctx context.Context, uid string) error

	// State Storage operations
	// GetStateEntry retrieves a state entry by organization and key.
	// Returns nil if not found (not an error). orgUID can be nil for global entries.
	GetStateEntry(ctx context.Context, orgUID *string, key string) (*models.StateEntry, error)
	// SetStateEntry creates or updates a state entry. TTL is optional (nil = never expires).
	// orgUID can be nil for global entries.
	SetStateEntry(ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration) error
	// DeleteStateEntry soft-deletes a state entry.
	DeleteStateEntry(ctx context.Context, orgUID *string, key string) error
	// ListStateEntries returns all entries matching the key prefix (using SQL LIKE).
	ListStateEntries(ctx context.Context, orgUID *string, keyPrefix string) ([]*models.StateEntry, error)
	// GetOrCreateStateEntry returns existing entry or creates new one.
	// Returns (entry, created, error) where created is true if a new entry was created.
	GetOrCreateStateEntry(
		ctx context.Context, orgUID *string, key string, defaultValue *models.JSONMap, ttl *time.Duration,
	) (*models.StateEntry, bool, error)
	// SetStateEntryIfNotExists creates entry only if key doesn't exist.
	// Returns (created, error) where created is true if entry was created.
	SetStateEntryIfNotExists(
		ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration,
	) (bool, error)
	// DeleteExpiredStateEntries removes entries past their expires_at.
	// Returns count of deleted entries.
	DeleteExpiredStateEntries(ctx context.Context) (int64, error)

	// Organization Parameter operations (organization_uid IS NOT NULL)
	// ListOrgParametersByKey returns all org-scoped parameters with a specific key.
	ListOrgParametersByKey(ctx context.Context, key string) ([]*models.Parameter, error)
	// GetOrgParameter retrieves an org-scoped parameter by orgUID and key, returns nil if not found.
	GetOrgParameter(ctx context.Context, orgUID, key string) (*models.Parameter, error)
	// SetOrgParameter creates or updates an org-scoped parameter.
	SetOrgParameter(ctx context.Context, orgUID, key string, value any, secret bool) error
	// DeleteOrgParameter soft-deletes an org-scoped parameter.
	DeleteOrgParameter(ctx context.Context, orgUID, key string) error

	// System Parameter operations (organization_uid IS NULL)
	// GetSystemParameter retrieves a system parameter by key, returns nil if not found.
	GetSystemParameter(ctx context.Context, key string) (*models.Parameter, error)
	// SetSystemParameter creates or updates a system parameter.
	SetSystemParameter(ctx context.Context, key string, value any, secret bool) error
	// DeleteSystemParameter soft-deletes a system parameter.
	DeleteSystemParameter(ctx context.Context, key string) error
	// ListSystemParameters returns all system parameters.
	ListSystemParameters(ctx context.Context) ([]*models.Parameter, error)

	// IntegrationConnection operations
	CreateIntegrationConnection(ctx context.Context, conn *models.IntegrationConnection) error
	GetIntegrationConnection(ctx context.Context, uid string) (*models.IntegrationConnection, error)
	GetIntegrationConnectionByProperty(
		ctx context.Context, connType, propertyName, propertyValue string,
	) (*models.IntegrationConnection, error)
	ListIntegrationConnections(
		ctx context.Context, filter *models.ListIntegrationConnectionsFilter,
	) ([]*models.IntegrationConnection, error)
	UpdateIntegrationConnection(ctx context.Context, uid string, update *models.IntegrationConnectionUpdate) error
	DeleteIntegrationConnection(ctx context.Context, uid string) error

	// CheckConnection operations
	CreateCheckConnection(ctx context.Context, conn *models.CheckConnection) error
	DeleteCheckConnection(ctx context.Context, checkUID, connectionUID string) error
	ListConnectionsForCheck(ctx context.Context, checkUID string) ([]*models.IntegrationConnection, error)
	SetCheckConnections(ctx context.Context, checkUID string, connectionUIDs []string) error
	ListDefaultConnections(ctx context.Context, orgUID string) ([]*models.IntegrationConnection, error)
	UpdateCheckConnection(ctx context.Context, checkUID, connectionUID string, update *models.CheckConnectionUpdate) error
	GetCheckConnection(ctx context.Context, checkUID, connectionUID string) (*models.CheckConnection, error)
	ListCheckConnectionsWithSettings(ctx context.Context, checkUID string) ([]*models.CheckConnection, error)

	// CheckGroup operations
	CreateCheckGroup(ctx context.Context, group *models.CheckGroup) error
	GetCheckGroup(ctx context.Context, orgUID, uid string) (*models.CheckGroup, error)
	GetCheckGroupBySlug(ctx context.Context, orgUID, slug string) (*models.CheckGroup, error)
	GetCheckGroupByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.CheckGroup, error)
	ListCheckGroups(ctx context.Context, orgUID string) ([]*models.CheckGroup, error)
	UpdateCheckGroup(ctx context.Context, orgUID, uid string, update *models.CheckGroupUpdate) error
	DeleteCheckGroup(ctx context.Context, uid string) error

	// StatusPage operations
	CreateStatusPage(ctx context.Context, page *models.StatusPage) error
	GetStatusPage(ctx context.Context, orgUID, uid string) (*models.StatusPage, error)
	GetStatusPageBySlug(ctx context.Context, orgUID, slug string) (*models.StatusPage, error)
	GetStatusPageByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.StatusPage, error)
	GetDefaultStatusPage(ctx context.Context, orgUID string) (*models.StatusPage, error)
	ListStatusPages(ctx context.Context, orgUID string) ([]*models.StatusPage, error)
	UpdateStatusPage(ctx context.Context, uid string, update *models.StatusPageUpdate) error
	DeleteStatusPage(ctx context.Context, uid string) error

	// StatusPageSection operations
	CreateStatusPageSection(ctx context.Context, section *models.StatusPageSection) error
	GetStatusPageSection(ctx context.Context, pageUID, uid string) (*models.StatusPageSection, error)
	GetStatusPageSectionBySlug(ctx context.Context, pageUID, slug string) (*models.StatusPageSection, error)
	ListStatusPageSections(ctx context.Context, pageUID string) ([]*models.StatusPageSection, error)
	UpdateStatusPageSection(ctx context.Context, uid string, update *models.StatusPageSectionUpdate) error
	DeleteStatusPageSection(ctx context.Context, uid string) error

	// StatusPageResource operations
	CreateStatusPageResource(ctx context.Context, resource *models.StatusPageResource) error
	GetStatusPageResource(ctx context.Context, sectionUID, uid string) (*models.StatusPageResource, error)
	ListStatusPageResources(ctx context.Context, sectionUID string) ([]*models.StatusPageResource, error)
	UpdateStatusPageResource(ctx context.Context, uid string, update *models.StatusPageResourceUpdate) error
	DeleteStatusPageResource(ctx context.Context, uid string) error

	// MaintenanceWindow operations
	CreateMaintenanceWindow(ctx context.Context, window *models.MaintenanceWindow) error
	GetMaintenanceWindow(ctx context.Context, orgUID, uid string) (*models.MaintenanceWindow, error)
	ListMaintenanceWindows(
		ctx context.Context, orgUID string, filter models.ListMaintenanceWindowsFilter,
	) ([]*models.MaintenanceWindow, error)
	UpdateMaintenanceWindow(ctx context.Context, uid string, update models.MaintenanceWindowUpdate) error
	DeleteMaintenanceWindow(ctx context.Context, orgUID, uid string) error
	SetMaintenanceWindowChecks(ctx context.Context, windowUID string, checkUIDs, checkGroupUIDs []string) error
	ListMaintenanceWindowChecks(ctx context.Context, windowUID string) ([]*models.MaintenanceWindowCheck, error)
	IsCheckInActiveMaintenance(ctx context.Context, checkUID string) (bool, error)

	// Close closes the database connection and cleans up resources
	io.Closer
}
