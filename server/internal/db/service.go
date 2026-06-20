// Package db provides the database abstraction layer for solidping.
package db

import (
	"context"
	"io"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// PublicStatusUpdate holds a status update row for public status page display.
// This type is used by ListPublicStatusUpdates and is independent of the admin
// models so the DB layer does not need to know about the full status_updates model.
type PublicStatusUpdate struct {
	UID          string
	SectionUID   *string
	CheckUID     *string
	IncidentUID  *string
	Title        string
	BodyMarkdown string
	LinkURL      *string
	Kind         string
	PublishedAt  time.Time
}

// ListIncidentNotificationsFilter configures what to return from ListIncidentNotifications.
type ListIncidentNotificationsFilter struct {
	IncidentUID   string    // required for the per-incident endpoint; optional for user-scoped queries
	UserUID       string    // optional: restrict to rows where user_uid = UserUID
	ConnectionUID string    // optional: restrict to rows where connection_uid = ConnectionUID
	Status        string    // optional: e.g. "sent", "failed"
	Limit         int       // default 100, max 500
	Before        time.Time // cursor: return rows created before this time (zero means no bound)
}

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

	// OAuth (MCP authorization server) operations
	CreateOAuthClient(ctx context.Context, client *models.OAuthClient) error
	GetOAuthClientByClientID(ctx context.Context, clientID string) (*models.OAuthClient, error)
	CreateOAuthAuthCode(ctx context.Context, code *models.OAuthAuthCode) error
	GetOAuthAuthCode(ctx context.Context, code string) (*models.OAuthAuthCode, error)
	ConsumeOAuthAuthCode(ctx context.Context, code string, now time.Time) (bool, error)
	CreateOAuthRefreshToken(ctx context.Context, token *models.OAuthRefreshToken) error
	GetOAuthRefreshToken(ctx context.Context, token string) (*models.OAuthRefreshToken, error)
	RevokeOAuthRefreshToken(ctx context.Context, token string, now time.Time) (bool, error)
	RevokeOAuthRefreshTokensForUser(ctx context.Context, userUID string, now time.Time) error

	// UserPasskey operations
	CreateUserPasskey(ctx context.Context, passkey *models.UserPasskey) error
	GetUserPasskey(ctx context.Context, uid string) (*models.UserPasskey, error)
	GetUserPasskeyByCredentialID(ctx context.Context, credentialID []byte) (*models.UserPasskey, error)
	ListUserPasskeysByUser(ctx context.Context, userUID string) ([]*models.UserPasskey, error)
	UpdateUserPasskey(ctx context.Context, uid string, update models.UserPasskeyUpdate) error
	DeleteUserPasskey(ctx context.Context, uid string) error

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
	GetOnCallScheduleByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.OnCallSchedule, error)
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
	GetEscalationPolicyByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.EscalationPolicy, error)
	ListEscalationPolicies(ctx context.Context, orgUID string) ([]*models.EscalationPolicy, error)
	UpdateEscalationPolicy(ctx context.Context, policyUID string, update *models.EscalationPolicyUpdate) error
	DeleteEscalationPolicy(ctx context.Context, policyUID string) error

	// Escalation policy steps (replace-all is the typical write path)
	GetEscalationPolicyStep(ctx context.Context, stepUID string) (*models.EscalationPolicyStep, error)
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
	//
	// UpdateCheckStatusAndClocks writes the check's status, streak,
	// status_changed_at and both incident clocks (first_failure_at /
	// first_success_since_failure_at) in a single atomic UPDATE. statusChangedAt
	// is written only when non-nil. The clock fields use the IncidentClockUpdate
	// tri-state: nil + !clear leaves the column untouched, nil + clear writes
	// NULL, non-nil writes the value. updated_at is written once. See spec
	// 2026-06-05-02-check-result-hot-path-db-roundtrips.md.
	UpdateCheckStatusAndClocks(
		ctx context.Context,
		checkUID string,
		status models.CheckStatus,
		streak int,
		statusChangedAt *time.Time,
		clocks models.IncidentClockUpdate,
	) error

	// Event operations
	CreateEvent(ctx context.Context, event *models.Event) error
	ListEvents(ctx context.Context, filter *models.ListEventsFilter) ([]*models.Event, error)

	// --- IncidentNotifications ---
	CreateIncidentNotification(ctx context.Context, n *models.IncidentNotification) error
	MarkIncidentNotificationSentByUID(ctx context.Context, uid string, sentAt time.Time, messageID string) error
	MarkIncidentNotificationFailedByUID(ctx context.Context, uid string, failedAt time.Time, errMsg string) error
	MarkIncidentNotificationSentByJob(
		ctx context.Context, jobUID string, sentAt time.Time, messageID string, details *models.DeliveryDetails,
	) error
	MarkIncidentNotificationFailedByJob(
		ctx context.Context, jobUID string, failedAt time.Time, errMsg string, retryable bool,
		details *models.DeliveryDetails,
	) error
	CancelIncidentNotificationsForIncident(ctx context.Context, incidentUID string, canceledAt time.Time) (int64, error)
	ListIncidentNotifications(
		ctx context.Context, orgUID string, f ListIncidentNotificationsFilter,
	) ([]*models.IncidentNotificationRow, error)
	GetIncidentNotification(
		ctx context.Context, orgUID, incidentUID, notifUID string,
	) (*models.IncidentNotificationRow, error)
	// GetOrgNotification fetches a single notification scoped only by org UID
	// (no incident required). Returns sql.ErrNoRows when the notification does
	// not exist within the given org.
	GetOrgNotification(
		ctx context.Context, orgUID, notifUID string,
	) (*models.IncidentNotificationRow, error)

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
	CreateChannel(ctx context.Context, conn *models.Integration) error
	GetChannel(ctx context.Context, uid string) (*models.Integration, error)
	GetChannelByProperty(
		ctx context.Context, connType, propertyName, propertyValue string,
	) (*models.Integration, error)
	ListChannels(
		ctx context.Context, filter *models.ListIntegrationsFilter,
	) ([]*models.Integration, error)
	UpdateChannel(ctx context.Context, uid string, update *models.IntegrationUpdate) error
	DeleteChannel(ctx context.Context, uid string) error

	// CheckConnection operations
	CreateCheckConnection(ctx context.Context, conn *models.CheckConnection) error
	DeleteCheckConnection(ctx context.Context, checkUID, connectionUID string) error
	ListChannelsForCheck(ctx context.Context, checkUID string) ([]*models.Integration, error)
	SetCheckConnections(ctx context.Context, checkUID string, connectionUIDs []string) error
	ListDefaultChannels(ctx context.Context, orgUID string) ([]*models.Integration, error)
	UpdateCheckConnection(ctx context.Context, checkUID, connectionUID string, update *models.CheckConnectionUpdate) error
	GetCheckConnection(ctx context.Context, checkUID, connectionUID string) (*models.CheckConnection, error)
	ListCheckConnectionsWithSettings(ctx context.Context, checkUID string) ([]*models.CheckConnection, error)

	// Severity operations (per-org channel-set primitive — spec 2026-05-08-03).
	CreateSeverity(ctx context.Context, severity *models.Severity) error
	GetSeverity(ctx context.Context, orgUID, identifier string) (*models.Severity, error)
	ListSeverities(ctx context.Context, filter *models.ListSeveritiesFilter) ([]*models.Severity, error)
	UpdateSeverity(ctx context.Context, uid string, update *models.SeverityUpdate) error
	DeleteSeverity(ctx context.Context, uid string) error
	// ClearOrgDefaultSeverity unsets the is_default flag on whichever live
	// severity currently carries it for the org. Used right before promoting
	// a different row to default so the partial unique index doesn't trip.
	ClearOrgDefaultSeverity(ctx context.Context, orgUID string) error
	// GetOrgDefaultSeverity returns the org's default severity. The escalation
	// step runner falls back to it when a step has no explicit severity_uid
	// and targets a user / all_admins.
	GetOrgDefaultSeverity(ctx context.Context, orgUID string) (*models.Severity, error)

	// CheckGroup operations
	CreateCheckGroup(ctx context.Context, group *models.CheckGroup) error
	GetCheckGroup(ctx context.Context, orgUID, uid string) (*models.CheckGroup, error)
	GetCheckGroupBySlug(ctx context.Context, orgUID, slug string) (*models.CheckGroup, error)
	GetCheckGroupByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.CheckGroup, error)
	ListCheckGroups(ctx context.Context, orgUID string) ([]*models.CheckGroup, error)
	UpdateCheckGroup(ctx context.Context, orgUID, uid string, update *models.CheckGroupUpdate) error
	DeleteCheckGroup(ctx context.Context, uid string) error

	// StatusUpdate operations
	ListStatusUpdates(
		ctx context.Context, orgUID string, filter models.StatusUpdatesFilter,
	) ([]*models.StatusUpdate, error)
	CreateStatusUpdate(ctx context.Context, su *models.StatusUpdate) error
	GetStatusUpdateByUID(ctx context.Context, uid string) (*models.StatusUpdate, error)
	UpdateStatusUpdate(ctx context.Context, su *models.StatusUpdate) error
	SoftDeleteStatusUpdate(ctx context.Context, uid string) error

	// StatusPageSubscriber operations (public email/RSS subscriptions)
	CreateSubscriber(ctx context.Context, sub *models.StatusPageSubscriber) error
	GetSubscriber(ctx context.Context, statusPageUID, uid string) (*models.StatusPageSubscriber, error)
	GetSubscriberByConfirmToken(ctx context.Context, token string) (*models.StatusPageSubscriber, error)
	GetSubscriberByUnsubToken(ctx context.Context, token string) (*models.StatusPageSubscriber, error)
	FindLiveSubscriber(
		ctx context.Context, statusPageUID, email string, scope models.SubscriberScope, incidentUID *string,
	) (*models.StatusPageSubscriber, error)
	FindAnySubscriber(
		ctx context.Context, statusPageUID, email string, scope models.SubscriberScope, incidentUID *string,
	) (*models.StatusPageSubscriber, error)
	ConfirmSubscriber(ctx context.Context, uid string, confirmedAt time.Time) error
	ResubscribeSubscriber(ctx context.Context, uid, confirmToken, unsubscribeToken string) error
	SoftDeleteSubscriber(ctx context.Context, uid string) error
	ListConfirmedSubscribers(
		ctx context.Context, statusPageUID string, incidentUID *string,
	) ([]*models.StatusPageSubscriber, error)
	ListSubscribers(ctx context.Context, statusPageUID string) ([]*models.StatusPageSubscriber, error)

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
	MaxStatusPageSectionPosition(ctx context.Context, pageUID string) (int, error)
	UpdateStatusPageSection(ctx context.Context, uid string, update *models.StatusPageSectionUpdate) error
	DeleteStatusPageSection(ctx context.Context, uid string) error

	// StatusPageResource operations
	CreateStatusPageResource(ctx context.Context, resource *models.StatusPageResource) error
	GetStatusPageResource(ctx context.Context, sectionUID, uid string) (*models.StatusPageResource, error)
	ListStatusPageResources(ctx context.Context, sectionUID string) ([]*models.StatusPageResource, error)
	MaxStatusPageResourcePosition(ctx context.Context, sectionUID string) (int, error)
	ReorderStatusPageResources(ctx context.Context, sectionUID string, orderedUIDs []string) error
	ReorderStatusPageSections(ctx context.Context, statusPageUID string, orderedUIDs []string) error
	UpdateStatusPageResource(ctx context.Context, uid string, update *models.StatusPageResourceUpdate) error
	DeleteStatusPageResource(ctx context.Context, uid string) error

	// ListPublicStatusUpdates returns recent status updates for a status page within the given
	// history window. Returns an empty slice (not an error) when the status_updates table does
	// not yet exist (graceful degradation before the backend spec migration is applied).
	ListPublicStatusUpdates(ctx context.Context, statusPageUID string, historyDays int) ([]*PublicStatusUpdate, error)

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
	// ListMaintenanceWindowsForCheck returns every non-deleted maintenance
	// window linked to the check (directly or via its group), without
	// evaluating recurrence or filtering by start time. Callers decide
	// active/inactive via models.IsActiveAt. Returns the raw window rows so an
	// in-process TTL cache can re-evaluate them at the current clock without
	// re-querying. See spec 2026-06-05-02-check-result-hot-path-db-roundtrips.md.
	ListMaintenanceWindowsForCheck(ctx context.Context, checkUID string) ([]*models.MaintenanceWindow, error)

	// File operations
	CreateFile(ctx context.Context, file *models.File) error
	GetFile(ctx context.Context, orgUID, uid string) (*models.File, error)
	GetFileAny(ctx context.Context, uid string) (*models.File, error)
	ListFiles(
		ctx context.Context, orgUID string, filter models.ListFilesFilter,
	) ([]*models.File, int64, error)
	DeleteFile(ctx context.Context, orgUID, uid string) error

	// CheckDependency operations
	CreateCheckDependency(ctx context.Context, dep *models.CheckDependency) error
	GetCheckDependency(ctx context.Context, orgUID, depUID string) (*models.CheckDependency, error)
	ListCheckDependenciesByOrg(ctx context.Context, orgUID string) ([]*models.CheckDependency, error)
	ListCheckDependencyParents(ctx context.Context, childCheckUID string) ([]*models.CheckDependency, error)
	ListCheckDependencyChildren(ctx context.Context, parentCheckUID string) ([]*models.CheckDependency, error)
	FindCheckDependencyEdge(ctx context.Context, parentUID, childUID string) (*models.CheckDependency, error)
	UpdateCheckDependency(ctx context.Context, depUID string, update *models.CheckDependencyUpdate) error
	DeleteCheckDependency(ctx context.Context, depUID string) error
	ListSuppressedChildIncidents(ctx context.Context, parentIncidentUID string) ([]*models.Incident, error)
	FindActiveIncidentsForChecksInWindow(
		ctx context.Context, checkUIDs []string, since, until time.Time,
	) ([]*models.Incident, error)

	// Org entitlement operations
	GetOrgEntitlements(ctx context.Context, orgUID string) (*models.OrgEntitlements, error)
	UpsertOrgEntitlements(
		ctx context.Context, ent *models.OrgEntitlements, audit *models.OrgEntitlementAudit,
	) error
	ListOrgEntitlementAudits(
		ctx context.Context, filter models.ListOrgEntitlementAuditsFilter,
	) ([]*models.OrgEntitlementAudit, error)
	// CountSSOMembersForOrg counts org members linked to at least one
	// row in user_providers. Used by the entitlements service to enforce
	// MaxSSOUsers.
	CountSSOMembersForOrg(ctx context.Context, orgUID string) (int, error)
	// ListOrgCheckRates returns (enabled, period) for all non-deleted,
	// non-internal checks of the given org. Used to compute usage stats
	// (count + aggregate checks-per-minute) and to enforce MaxChecks.
	ListOrgCheckRates(ctx context.Context, orgUID string) ([]models.CheckRate, error)

	// Membership-request operations
	CreateMembershipRequest(ctx context.Context, request *models.MembershipRequest) error
	UpdateMembershipRequest(ctx context.Context, request *models.MembershipRequest) error
	GetMembershipRequest(ctx context.Context, uid string) (*models.MembershipRequest, error)
	GetMembershipRequestByOrgAndUser(
		ctx context.Context, orgUID, userUID string,
	) (*models.MembershipRequest, error)
	ListMembershipRequests(
		ctx context.Context, filter models.ListMembershipRequestsFilter,
	) ([]*models.MembershipRequest, error)
	ApproveMembershipRequest(
		ctx context.Context, request *models.MembershipRequest, member *models.OrganizationMember,
	) error

	// --- UserContacts / UserNotificationRoutes ---

	// ListUserContactsWithRoutes returns the ordered notification routes for a user in an org,
	// with the Contact relation eagerly loaded. Soft-deleted contacts are excluded.
	ListUserContactsWithRoutes(ctx context.Context, userUID, orgUID string) ([]*models.UserNotificationRoute, error)

	// EnsureDefaultEmailRoute idempotently creates one email contact and one enabled route
	// for the user in the org. Safe to call concurrently — uses INSERT … ON CONFLICT DO NOTHING.
	EnsureDefaultEmailRoute(ctx context.Context, userUID, orgUID, email string) error

	// UpsertUserContact creates or restores a contact. On conflict (same user+org+type+value)
	// it undeletes the row and updates the label.
	UpsertUserContact(ctx context.Context, c *models.UserContact) error

	// DeleteUserContact soft-deletes a contact by UID.
	DeleteUserContact(ctx context.Context, uid string) error

	// SetRouteEnabled toggles the enabled flag on a route.
	SetRouteEnabled(ctx context.Context, routeUID string, enabled bool) error

	// ReorderRoutes sets the position of each route to its index in routeUIDs.
	// Only routes belonging to the given user+org are affected; unknown UIDs are ignored.
	ReorderRoutes(ctx context.Context, userUID, orgUID string, routeUIDs []string) error

	// GetSlackChannelForOrg returns the first enabled Slack channel for the org.
	// Returns nil, nil when no Slack channel is configured.
	GetSlackChannelForOrg(ctx context.Context, orgUID string) (*models.Integration, error)

	// AppSettings operations

	// GetAppSetting returns the value for the given key.
	// Returns sql.ErrNoRows (wrapped) if the key does not exist.
	GetAppSetting(ctx context.Context, key string) (string, error)

	// SetAppSetting creates or updates a key/value pair (upsert).
	SetAppSetting(ctx context.Context, key, value string) error

	// Close closes the database connection and cleans up resources
	io.Closer
}
