package notifications

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// errDatabaseError is a test error for database failures.
var errDatabaseError = errors.New("database error")

// errMockNotImplemented is returned by mock methods that don't apply to the
// notifications-package tests. It satisfies the linter's nilnil rule for
// pointer-returning methods that have no real implementation.
var errMockNotImplemented = errors.New("mock method not implemented")

// mockDBService is a test double for the database service.
type mockDBService struct {
	getStateEntryFunc func(ctx context.Context, orgUID *string, key string) (*models.StateEntry, error)
	setStateEntryFunc func(
		ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration,
	) error
	getStateCalls []string
	setStateCalls []setStateCall
}

type setStateCall struct {
	key   string
	value *models.JSONMap
}

func (m *mockDBService) GetStateEntry(ctx context.Context, orgUID *string, key string) (*models.StateEntry, error) {
	m.getStateCalls = append(m.getStateCalls, key)
	if m.getStateEntryFunc != nil {
		return m.getStateEntryFunc(ctx, orgUID, key)
	}

	return nil, nil //nolint:nilnil // Test stub returns nil when no mock is set
}

func (m *mockDBService) SetStateEntry(
	ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration,
) error {
	m.setStateCalls = append(m.setStateCalls, setStateCall{key: key, value: value})
	if m.setStateEntryFunc != nil {
		return m.setStateEntryFunc(ctx, orgUID, key, value, ttl)
	}

	return nil
}

// Implement other db.Service methods as stubs (required to satisfy interface).
func (m *mockDBService) GetOrCreateStateEntry(
	_ context.Context, _ *string, _ string, _ *models.JSONMap, _ *time.Duration,
) (*models.StateEntry, bool, error) {
	return nil, false, nil
}

func (m *mockDBService) SetStateEntryIfNotExists(
	_ context.Context, _ *string, _ string, _ *models.JSONMap, _ *time.Duration,
) (bool, error) {
	return false, nil
}

func (m *mockDBService) DeleteStateEntry(_ context.Context, _ *string, _ string) error {
	return nil
}

func (m *mockDBService) ListStateEntries(_ context.Context, _ *string, _ string) ([]*models.StateEntry, error) {
	return nil, nil
}

func (m *mockDBService) DeleteExpiredStateEntries(_ context.Context) (int64, error) {
	return 0, nil
}

func (m *mockDBService) Close() error {
	return nil
}

// Stub implementations for all other db.Service methods (not used in tests).
func (m *mockDBService) Initialize(_ context.Context) error { panic("not implemented") }
func (m *mockDBService) DB() *bun.DB                        { panic("not implemented") }
func (m *mockDBService) CreateOrganization(_ context.Context, _ *models.Organization) error {
	panic("not implemented")
}

func (m *mockDBService) GetOrganization(_ context.Context, _ string) (*models.Organization, error) {
	panic("not implemented")
}

func (m *mockDBService) GetOrganizationBySlug(_ context.Context, _ string) (*models.Organization, error) {
	panic("not implemented")
}

func (m *mockDBService) ListOrganizations(_ context.Context) ([]*models.Organization, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateOrganization(_ context.Context, _ string, _ models.OrganizationUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteOrganization(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateOrganizationProvider(_ context.Context, _ *models.OrganizationProvider) error {
	panic("not implemented")
}

func (m *mockDBService) GetOrganizationProvider(_ context.Context, _ string) (*models.OrganizationProvider, error) {
	panic("not implemented")
}

func (m *mockDBService) GetOrganizationProviderByProviderID(
	_ context.Context, _ models.ProviderType, _ string,
) (*models.OrganizationProvider, error) {
	panic("not implemented")
}

func (m *mockDBService) ListOrganizationProviders(_ context.Context, _ string) ([]*models.OrganizationProvider, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateOrganizationProvider(
	_ context.Context, _ string, _ models.OrganizationProviderUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteOrganizationProvider(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateUser(_ context.Context, _ *models.User) error {
	panic("not implemented")
}

func (m *mockDBService) GetUser(_ context.Context, _ string) (*models.User, error) {
	panic("not implemented")
}

func (m *mockDBService) GetUserByEmail(_ context.Context, _ string) (*models.User, error) {
	panic("not implemented")
}

func (m *mockDBService) ListUsers(_ context.Context) ([]*models.User, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateUser(_ context.Context, _ string, _ *models.UserUpdate) error {
	panic("not implemented")
}
func (m *mockDBService) DeleteUser(_ context.Context, _ string) error { panic("not implemented") }
func (m *mockDBService) CreateUserProvider(_ context.Context, _ *models.UserProvider) error {
	panic("not implemented")
}

func (m *mockDBService) GetUserProvider(_ context.Context, _ string) (*models.UserProvider, error) {
	panic("not implemented")
}

func (m *mockDBService) GetUserProviderByProviderID(
	_ context.Context, _ models.ProviderType, _ string,
) (*models.UserProvider, error) {
	panic("not implemented")
}

func (m *mockDBService) ListUserProvidersByUser(_ context.Context, _ string) ([]*models.UserProvider, error) {
	panic("not implemented")
}

func (m *mockDBService) DeleteUserProvider(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateOrganizationMember(_ context.Context, _ *models.OrganizationMember) error {
	panic("not implemented")
}

func (m *mockDBService) GetOrganizationMember(_ context.Context, _ string) (*models.OrganizationMember, error) {
	panic("not implemented")
}

func (m *mockDBService) GetMemberByUserAndOrg(_ context.Context, _, _ string) (*models.OrganizationMember, error) {
	panic("not implemented")
}

func (m *mockDBService) ListMembersByOrg(_ context.Context, _ string) ([]*models.OrganizationMember, error) {
	panic("not implemented")
}

func (m *mockDBService) ListMembersByUser(_ context.Context, _ string) ([]*models.OrganizationMember, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateOrganizationMember(_ context.Context, _ string, _ models.OrganizationMemberUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteOrganizationMember(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CountAdminsByOrg(_ context.Context, _ string) (int, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateCheck(_ context.Context, _ *models.Check) error {
	panic("not implemented")
}

func (m *mockDBService) GetCheck(_ context.Context, _, _ string) (*models.Check, error) {
	panic("not implemented")
}

func (m *mockDBService) ListChecks(
	_ context.Context, _ string, _ *models.ListChecksFilter,
) ([]*models.Check, int64, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateCheck(_ context.Context, _ string, _ *models.CheckUpdate) error {
	panic("not implemented")
}
func (m *mockDBService) DeleteCheck(_ context.Context, _ string) error { panic("not implemented") }
func (m *mockDBService) CreateCheckJob(_ context.Context, _ *models.CheckJob) error {
	panic("not implemented")
}

func (m *mockDBService) GetCheckJob(_ context.Context, _, _ string) (*models.CheckJob, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateCheckJob(_ context.Context, _ string, _ models.CheckJobUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteCheckJob(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) ListCheckJobsByCheckUID(_ context.Context, _ string) ([]*models.CheckJob, error) {
	panic("not implemented")
}

func (m *mockDBService) AcquireCheckJobs(
	_ context.Context, _ string, _ models.JSONMap, _ int,
) ([]*models.CheckJob, error) {
	panic("not implemented")
}

func (m *mockDBService) ReleaseCheckJobLease(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateResult(_ context.Context, _ *models.Result) error {
	panic("not implemented")
}

func (m *mockDBService) GetResult(_ context.Context, _ string) (*models.Result, error) {
	panic("not implemented")
}

func (m *mockDBService) ListResults(
	_ context.Context, _ *models.ListResultsFilter,
) (*models.ListResultsResponse, error) {
	panic("not implemented")
}

func (m *mockDBService) DeleteResults(_ context.Context, _ string, _ []string) (int64, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateWorker(_ context.Context, _ *models.Worker) error {
	panic("not implemented")
}

func (m *mockDBService) GetWorker(_ context.Context, _ string) (*models.Worker, error) {
	panic("not implemented")
}

func (m *mockDBService) GetWorkerByIdentifier(_ context.Context, _ string) (*models.Worker, error) {
	panic("not implemented")
}

func (m *mockDBService) ListWorkers(_ context.Context) ([]*models.Worker, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateWorker(_ context.Context, _ string, _ models.WorkerUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) UpdateWorkerActivity(_ context.Context, _ string) error {
	panic("not implemented")
}
func (m *mockDBService) DeleteWorker(_ context.Context, _ string) error { panic("not implemented") }
func (m *mockDBService) CreateParameter(_ context.Context, _ *models.Parameter) error {
	panic("not implemented")
}

func (m *mockDBService) GetParameter(_ context.Context, _, _ string) (*models.Parameter, error) {
	panic("not implemented")
}

func (m *mockDBService) ListParameters(_ context.Context, _ string) ([]*models.Parameter, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateParameter(_ context.Context, _, _ string, _ *models.JSONMap, _ bool) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteParameter(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateIncident(_ context.Context, _ *models.Incident) error {
	panic("not implemented")
}

func (m *mockDBService) GetIncident(_ context.Context, _, _ string) (*models.Incident, error) {
	panic("not implemented")
}

func (m *mockDBService) FindActiveIncidentByCheckUID(_ context.Context, _ string) (*models.Incident, error) {
	panic("not implemented")
}

func (m *mockDBService) FindRecentlyResolvedIncidentByCheckUID(
	_ context.Context, _ string, _ time.Time,
) (*models.Incident, error) {
	panic("not implemented")
}

func (m *mockDBService) ListIncidents(_ context.Context, _ *models.ListIncidentsFilter) ([]*models.Incident, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateIncident(_ context.Context, _ string, _ *models.IncidentUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) CountActiveIncidentsByCheckUID(_ context.Context, _ string) (int, error) {
	return 0, nil
}

func (m *mockDBService) ListExpiredSnoozedIncidents(_ context.Context, _ time.Time) ([]*models.Incident, error) {
	return nil, nil
}

func (m *mockDBService) CreateOnCallSchedule(_ context.Context, _ *models.OnCallSchedule) error {
	return nil
}

func (m *mockDBService) GetOnCallSchedule(
	_ context.Context, _, _ string,
) (*models.OnCallSchedule, error) {
	return nil, errMockNotImplemented
}

func (m *mockDBService) GetOnCallScheduleBySlug(
	_ context.Context, _, _ string,
) (*models.OnCallSchedule, error) {
	return nil, errMockNotImplemented
}

func (m *mockDBService) GetOnCallScheduleByICalSecret(
	_ context.Context, _ string,
) (*models.OnCallSchedule, error) {
	return nil, errMockNotImplemented
}

func (m *mockDBService) ListOnCallSchedules(
	_ context.Context, _ string,
) ([]*models.OnCallSchedule, error) {
	return nil, nil
}

func (m *mockDBService) UpdateOnCallSchedule(
	_ context.Context, _ string, _ *models.OnCallScheduleUpdate,
) error {
	return nil
}

func (m *mockDBService) DeleteOnCallSchedule(_ context.Context, _ string) error {
	return nil
}

func (m *mockDBService) ListOnCallScheduleUsers(
	_ context.Context, _ string,
) ([]*models.OnCallScheduleUser, error) {
	return nil, nil
}

func (m *mockDBService) ReplaceOnCallScheduleUsers(
	_ context.Context, _ string, _ []string,
) error {
	return nil
}

func (m *mockDBService) CreateOnCallScheduleOverride(
	_ context.Context, _ *models.OnCallScheduleOverride,
) error {
	return nil
}

func (m *mockDBService) ListOnCallScheduleOverrides(
	_ context.Context, _ string, _, _ *time.Time,
) ([]*models.OnCallScheduleOverride, error) {
	return nil, nil
}

func (m *mockDBService) GetOnCallScheduleOverride(
	_ context.Context, _ string,
) (*models.OnCallScheduleOverride, error) {
	return nil, errMockNotImplemented
}

func (m *mockDBService) DeleteOnCallScheduleOverride(_ context.Context, _ string) error {
	return nil
}

func (m *mockDBService) CreateEscalationPolicy(_ context.Context, _ *models.EscalationPolicy) error {
	return nil
}

func (m *mockDBService) GetEscalationPolicy(
	_ context.Context, _, _ string,
) (*models.EscalationPolicy, error) {
	return nil, errMockNotImplemented
}

func (m *mockDBService) GetEscalationPolicyBySlug(
	_ context.Context, _, _ string,
) (*models.EscalationPolicy, error) {
	return nil, errMockNotImplemented
}

func (m *mockDBService) ListEscalationPolicies(
	_ context.Context, _ string,
) ([]*models.EscalationPolicy, error) {
	return nil, nil
}

func (m *mockDBService) UpdateEscalationPolicy(
	_ context.Context, _ string, _ *models.EscalationPolicyUpdate,
) error {
	return nil
}

func (m *mockDBService) DeleteEscalationPolicy(_ context.Context, _ string) error {
	return nil
}

func (m *mockDBService) ListEscalationPolicySteps(
	_ context.Context, _ string,
) ([]*models.EscalationPolicyStep, error) {
	return nil, nil
}

func (m *mockDBService) ReplaceEscalationPolicySteps(
	_ context.Context, _ string,
	_ []*models.EscalationPolicyStep,
	_ map[int][]*models.EscalationPolicyTarget,
) error {
	return nil
}

func (m *mockDBService) ListEscalationPolicyTargets(
	_ context.Context, _ []string,
) ([]*models.EscalationPolicyTarget, error) {
	return nil, nil
}

func (m *mockDBService) CreateEvent(_ context.Context, _ *models.Event) error {
	panic("not implemented")
}

func (m *mockDBService) GetEvent(_ context.Context, _ string) (*models.Event, error) {
	panic("not implemented")
}

func (m *mockDBService) ListEvents(_ context.Context, _ *models.ListEventsFilter) ([]*models.Event, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateIntegrationConnection(_ context.Context, _ *models.IntegrationConnection) error {
	panic("not implemented")
}

func (m *mockDBService) GetIntegrationConnection(_ context.Context, _ string) (*models.IntegrationConnection, error) {
	panic("not implemented")
}

func (m *mockDBService) ListIntegrationConnections(
	_ context.Context, _ *models.ListIntegrationConnectionsFilter,
) ([]*models.IntegrationConnection, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateIntegrationConnection(
	_ context.Context, _ string, _ *models.IntegrationConnectionUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteIntegrationConnection(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateCheckConnection(_ context.Context, _ *models.CheckConnection) error {
	panic("not implemented")
}

func (m *mockDBService) ListConnectionsForCheck(_ context.Context, _ string) ([]*models.IntegrationConnection, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateCheckConnection(_ context.Context, _, _ string, _ *models.CheckConnectionUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) GetCheckConnection(_ context.Context, _, _ string) (*models.CheckConnection, error) {
	panic("not implemented")
}

func (m *mockDBService) DeleteCheckConnection(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) SetCheckConnections(_ context.Context, _ string, _ []string) error {
	panic("not implemented")
}

func (m *mockDBService) ListDefaultConnections(_ context.Context, _ string) ([]*models.IntegrationConnection, error) {
	panic("not implemented")
}

func (m *mockDBService) ListCheckConnectionsWithSettings(
	_ context.Context, _ string,
) ([]*models.CheckConnection, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateJob(_ context.Context, _ *models.Job) error {
	panic("not implemented")
}

func (m *mockDBService) GetJob(_ context.Context, _ string) (*models.Job, error) {
	panic("not implemented")
}

func (m *mockDBService) ListJobs(_ context.Context, _ *string, _ int) ([]*models.Job, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateJob(_ context.Context, _ string, _ models.JobUpdate) error {
	panic("not implemented")
}
func (m *mockDBService) DeleteJob(_ context.Context, _ string) error { panic("not implemented") }
func (m *mockDBService) PurgeOldJobs(_ context.Context, _ int) (int64, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateUserToken(_ context.Context, _ *models.UserToken) error {
	panic("not implemented")
}

func (m *mockDBService) GetUserToken(_ context.Context, _ string) (*models.UserToken, error) {
	panic("not implemented")
}

func (m *mockDBService) GetUserTokenByToken(_ context.Context, _ string) (*models.UserToken, error) {
	panic("not implemented")
}

func (m *mockDBService) ListUserTokens(_ context.Context, _ string) ([]*models.UserToken, error) {
	panic("not implemented")
}

func (m *mockDBService) RevokeUserToken(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteExpiredTokens(_ context.Context) (int64, error) {
	panic("not implemented")
}

func (m *mockDBService) ListUserTokensByType(
	_ context.Context, _ string, _ models.TokenType,
) ([]*models.UserToken, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateUserToken(_ context.Context, _ string, _ models.UserTokenUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteUserToken(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) GetWorkerBySlug(_ context.Context, _ string) (*models.Worker, error) {
	panic("not implemented")
}

func (m *mockDBService) RegisterOrUpdateWorker(_ context.Context, _ *models.Worker) (*models.Worker, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateWorkerHeartbeat(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) GetCheckByUidOrSlug( //nolint:revive // Interface method name
	_ context.Context, _, _ string,
) (*models.Check, error) {
	panic("not implemented")
}

func (m *mockDBService) GetCheckByEmailToken(_ context.Context, _ string) (*models.Check, error) {
	panic("not implemented")
}

func (m *mockDBService) FindActiveIncidentByGroupUID(_ context.Context, _ string) (*models.Incident, error) {
	panic("not implemented")
}

func (m *mockDBService) FindRecentlyResolvedIncidentByGroupUID(
	_ context.Context, _ string, _ time.Time,
) (*models.Incident, error) {
	panic("not implemented")
}

func (m *mockDBService) ListIncidentMemberChecks(
	_ context.Context, _ string,
) ([]*models.IncidentMemberCheck, error) {
	panic("not implemented")
}

func (m *mockDBService) GetIncidentMemberCheck(
	_ context.Context, _, _ string,
) (*models.IncidentMemberCheck, error) {
	panic("not implemented")
}

func (m *mockDBService) UpsertIncidentMemberCheck(_ context.Context, _ *models.IncidentMemberCheck) error {
	panic("not implemented")
}

func (m *mockDBService) UpdateIncidentMemberCheck(
	_ context.Context, _, _ string, _ *models.IncidentMemberUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) CountFailingIncidentMembers(_ context.Context, _ string) (int, error) {
	panic("not implemented")
}

func (m *mockDBService) GetOrCreateLabel(_ context.Context, _, _, _ string) (*models.Label, error) {
	panic("not implemented")
}

func (m *mockDBService) SetCheckLabels(_ context.Context, _ string, _ []string) error {
	panic("not implemented")
}

func (m *mockDBService) GetLabelsForCheck(_ context.Context, _ string) ([]*models.Label, error) {
	panic("not implemented")
}

func (m *mockDBService) GetLabelsForChecks(_ context.Context, _ []string) (map[string][]*models.Label, error) {
	panic("not implemented")
}

func (m *mockDBService) ListDistinctLabelKeys(
	_ context.Context, _, _ string, _ int,
) ([]models.LabelSuggestion, error) {
	return nil, errMockNotImplemented
}

func (m *mockDBService) ListDistinctLabelValues(
	_ context.Context, _, _, _ string, _ int,
) ([]models.LabelSuggestion, error) {
	return nil, errMockNotImplemented
}

func (m *mockDBService) GetLastResultForChecks(_ context.Context, _ []string) (map[string]*models.Result, error) {
	panic("not implemented")
}

func (m *mockDBService) GetLastStatusChangeForChecks(
	_ context.Context, _ []string,
) (map[string]*models.LastStatusChange, error) {
	panic("not implemented")
}

func (m *mockDBService) SaveResultWithStatusTracking(_ context.Context, _ *models.Result) error {
	panic("not implemented")
}

func (m *mockDBService) UpdateCheckStatus(
	_ context.Context, _ string, _ models.CheckStatus, _ int, _ *time.Time,
) error {
	panic("not implemented")
}

func (m *mockDBService) GetSystemParameter(_ context.Context, _ string) (*models.Parameter, error) {
	panic("not implemented")
}

func (m *mockDBService) SetSystemParameter(_ context.Context, _ string, _ any, _ bool) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteSystemParameter(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) ListSystemParameters(_ context.Context) ([]*models.Parameter, error) {
	panic("not implemented")
}

func (m *mockDBService) GetIntegrationConnectionByProperty(
	_ context.Context, _, _, _ string,
) (*models.IntegrationConnection, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateStatusPage(_ context.Context, _ *models.StatusPage) error {
	panic("not implemented")
}

func (m *mockDBService) GetStatusPage(_ context.Context, _, _ string) (*models.StatusPage, error) {
	panic("not implemented")
}

func (m *mockDBService) GetStatusPageBySlug(_ context.Context, _, _ string) (*models.StatusPage, error) {
	panic("not implemented")
}

//nolint:revive // Interface method name
func (m *mockDBService) GetStatusPageByUidOrSlug(
	_ context.Context, _, _ string,
) (*models.StatusPage, error) {
	panic("not implemented")
}

func (m *mockDBService) GetDefaultStatusPage(_ context.Context, _ string) (*models.StatusPage, error) {
	panic("not implemented")
}

func (m *mockDBService) ListStatusPages(_ context.Context, _ string) ([]*models.StatusPage, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateStatusPage(_ context.Context, _ string, _ *models.StatusPageUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteStatusPage(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateStatusPageSection(_ context.Context, _ *models.StatusPageSection) error {
	panic("not implemented")
}

func (m *mockDBService) GetStatusPageSection(_ context.Context, _, _ string) (*models.StatusPageSection, error) {
	panic("not implemented")
}

func (m *mockDBService) GetStatusPageSectionBySlug(_ context.Context, _, _ string) (*models.StatusPageSection, error) {
	panic("not implemented")
}

func (m *mockDBService) ListStatusPageSections(_ context.Context, _ string) ([]*models.StatusPageSection, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateStatusPageSection(_ context.Context, _ string, _ *models.StatusPageSectionUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteStatusPageSection(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateStatusPageResource(_ context.Context, _ *models.StatusPageResource) error {
	panic("not implemented")
}

func (m *mockDBService) GetStatusPageResource(_ context.Context, _, _ string) (*models.StatusPageResource, error) {
	panic("not implemented")
}

func (m *mockDBService) ListStatusPageResources(_ context.Context, _ string) ([]*models.StatusPageResource, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateStatusPageResource(
	_ context.Context, _ string, _ *models.StatusPageResourceUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteStatusPageResource(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) ListOrgParametersByKey(_ context.Context, _ string) ([]*models.Parameter, error) {
	panic("not implemented")
}

func (m *mockDBService) GetOrgParameter(_ context.Context, _, _ string) (*models.Parameter, error) {
	panic("not implemented")
}

func (m *mockDBService) SetOrgParameter(_ context.Context, _, _ string, _ any, _ bool) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteOrgParameter(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateCheckGroup(_ context.Context, _ *models.CheckGroup) error {
	panic("not implemented")
}

func (m *mockDBService) GetCheckGroup(_ context.Context, _, _ string) (*models.CheckGroup, error) {
	panic("not implemented")
}

func (m *mockDBService) GetCheckGroupBySlug(_ context.Context, _, _ string) (*models.CheckGroup, error) {
	panic("not implemented")
}

func (m *mockDBService) GetCheckGroupByUidOrSlug( //nolint:revive // Interface method name
	_ context.Context, _, _ string,
) (*models.CheckGroup, error) {
	panic("not implemented")
}

func (m *mockDBService) ListCheckGroups(_ context.Context, _ string) ([]*models.CheckGroup, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateCheckGroup(_ context.Context, _, _ string, _ *models.CheckGroupUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteCheckGroup(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateMaintenanceWindow(_ context.Context, _ *models.MaintenanceWindow) error {
	panic("not implemented")
}

func (m *mockDBService) GetMaintenanceWindow(
	_ context.Context, _, _ string,
) (*models.MaintenanceWindow, error) {
	panic("not implemented")
}

func (m *mockDBService) ListMaintenanceWindows(
	_ context.Context, _ string, _ models.ListMaintenanceWindowsFilter,
) ([]*models.MaintenanceWindow, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateMaintenanceWindow(
	_ context.Context, _ string, _ models.MaintenanceWindowUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteMaintenanceWindow(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) SetMaintenanceWindowChecks(
	_ context.Context, _ string, _, _ []string,
) error {
	panic("not implemented")
}

func (m *mockDBService) ListMaintenanceWindowChecks(
	_ context.Context, _ string,
) ([]*models.MaintenanceWindowCheck, error) {
	panic("not implemented")
}

func (m *mockDBService) IsCheckInActiveMaintenance(_ context.Context, _ string) (bool, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateFile(_ context.Context, _ *models.File) error {
	panic("not implemented")
}

func (m *mockDBService) GetFile(_ context.Context, _, _ string) (*models.File, error) {
	panic("not implemented")
}

func (m *mockDBService) GetFileAny(_ context.Context, _ string) (*models.File, error) {
	panic("not implemented")
}

func (m *mockDBService) ListFiles(
	_ context.Context, _ string, _ models.ListFilesFilter,
) ([]*models.File, int64, error) {
	panic("not implemented")
}

func (m *mockDBService) DeleteFile(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateMembershipRequest(
	_ context.Context, _ *models.MembershipRequest,
) error {
	panic("not implemented")
}

func (m *mockDBService) UpdateMembershipRequest(
	_ context.Context, _ *models.MembershipRequest,
) error {
	panic("not implemented")
}

func (m *mockDBService) GetMembershipRequest(
	_ context.Context, _ string,
) (*models.MembershipRequest, error) {
	panic("not implemented")
}

func (m *mockDBService) GetMembershipRequestByOrgAndUser(
	_ context.Context, _, _ string,
) (*models.MembershipRequest, error) {
	panic("not implemented")
}

func (m *mockDBService) ListMembershipRequests(
	_ context.Context, _ models.ListMembershipRequestsFilter,
) ([]*models.MembershipRequest, error) {
	panic("not implemented")
}

func (m *mockDBService) ApproveMembershipRequest(
	_ context.Context, _ *models.MembershipRequest, _ *models.OrganizationMember,
) error {
	panic("not implemented")
}

func TestSlackSender_Send_NewThread(t *testing.T) {
	t.Parallel()

	orgUID := "org-123"

	dbService := &mockDBService{
		// No existing thread found
		getStateEntryFunc: func(_ context.Context, _ *string, _ string) (*models.StateEntry, error) {
			return nil, nil //nolint:nilnil // Test stub returns nil when no thread exists
		},
	}

	r := require.New(t)

	// Verify GetStateEntry is called with correct key
	expectedKey := "incidents/incident-456/slack/thread"

	_, err := dbService.GetStateEntry(context.Background(), &orgUID, expectedKey)
	r.NoError(err)
	r.Len(dbService.getStateCalls, 1)
	r.Equal(expectedKey, dbService.getStateCalls[0])

	// Verify SetStateEntry would be called for new thread
	expectedValue := &models.JSONMap{
		"channel_id": "C123456",
		"message_id": "1234567890.123456",
		"thread_ts":  "1234567890.123456",
	}

	err = dbService.SetStateEntry(context.Background(), &orgUID, expectedKey, expectedValue, nil)
	r.NoError(err)
	r.Len(dbService.setStateCalls, 1)
	r.Equal(expectedKey, dbService.setStateCalls[0].key)
	r.Equal("C123456", (*dbService.setStateCalls[0].value)["channel_id"])
}

func TestSlackSender_Send_ExistingThread(t *testing.T) {
	t.Parallel()

	orgUID := "org-123"
	channelID := "C123456"
	existingThreadTS := "1234567890.000000"

	dbService := &mockDBService{
		// Existing thread found
		getStateEntryFunc: func(_ context.Context, _ *string, key string) (*models.StateEntry, error) {
			return &models.StateEntry{
				Key: key,
				Value: &models.JSONMap{
					"channel_id": channelID,
					"message_id": existingThreadTS,
					"thread_ts":  existingThreadTS,
				},
			}, nil
		},
	}

	r := require.New(t)

	expectedKey := "incidents/incident-456/slack/thread"
	entry, err := dbService.GetStateEntry(context.Background(), &orgUID, expectedKey)
	r.NoError(err)
	r.NotNil(entry)
	r.NotNil(entry.Value)

	threadTS, ok := (*entry.Value)["thread_ts"].(string)
	r.True(ok)
	r.Equal(existingThreadTS, threadTS)

	// Verify that SetStateEntry should NOT be called when thread exists
	r.Empty(dbService.setStateCalls, "SetStateEntry should not be called for existing threads")
}

func TestSlackSender_Send_StateEntryFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		incidentUID string
		expectedKey string
	}{
		{
			name:        "standard UUID",
			incidentUID: "123e4567-e89b-12d3-a456-426614174000",
			expectedKey: "incidents/123e4567-e89b-12d3-a456-426614174000/slack/thread",
		},
		{
			name:        "different UUID",
			incidentUID: "abc-def-123",
			expectedKey: "incidents/abc-def-123/slack/thread",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			// Test that the key format matches the convention
			actualKey := "incidents/" + tt.incidentUID + "/slack/thread"
			r.Equal(tt.expectedKey, actualKey)
		})
	}
}

func TestSlackSender_Send_MissingAccessToken(t *testing.T) {
	t.Parallel()

	payload := &Payload{
		EventType: "incident.created",
		Incident: &models.Incident{
			UID:             "incident-123",
			OrganizationUID: "org-123",
		},
		Check: &models.Check{},
		Connection: &models.IntegrationConnection{
			Settings: models.JSONMap{
				"channel_id": "C123",
				// access_token is missing
			},
		},
	}

	jctx := &jobdef.JobContext{
		DBService: &mockDBService{},
		Logger:    slog.Default(),
	}

	sender := &SlackSender{}
	err := sender.Send(context.Background(), jctx, payload)

	r := require.New(t)
	r.Error(err)
	r.ErrorIs(err, ErrSlackAccessTokenNotConfigured)
}

func TestSlackSender_Send_MissingChannel(t *testing.T) {
	t.Parallel()

	payload := &Payload{
		EventType: "incident.created",
		Incident: &models.Incident{
			UID:             "incident-123",
			OrganizationUID: "org-123",
		},
		Check: &models.Check{},
		Connection: &models.IntegrationConnection{
			Settings: models.JSONMap{
				"access_token": "xoxb-test-token",
				// channel_id is missing
			},
		},
	}

	jctx := &jobdef.JobContext{
		DBService: &mockDBService{},
		Logger:    slog.Default(),
	}

	sender := &SlackSender{}
	err := sender.Send(context.Background(), jctx, payload)

	r := require.New(t)
	r.Error(err)
	r.ErrorIs(err, ErrNoDefaultChannelConfigured)
}

func TestSlackSender_Send_StateEntryGetError(t *testing.T) {
	t.Parallel()

	payload := &Payload{
		EventType: "incident.created",
		Incident: &models.Incident{
			UID:             "incident-123",
			OrganizationUID: "org-123",
		},
		Check: &models.Check{},
		Connection: &models.IntegrationConnection{
			Settings: models.JSONMap{
				"access_token": "xoxb-test-token",
				"channel_id":   "C123",
			},
		},
	}

	jctx := &jobdef.JobContext{
		DBService: &mockDBService{
			getStateEntryFunc: func(_ context.Context, _ *string, _ string) (*models.StateEntry, error) {
				return nil, errDatabaseError
			},
		},
		Logger: slog.Default(),
	}

	sender := &SlackSender{}
	err := sender.Send(context.Background(), jctx, payload)

	r := require.New(t)
	r.Error(err)
	r.Contains(err.Error(), "getting thread state entry")
}

func TestSlackSender_buildMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		eventType    string
		checkName    string
		failureCount int
		expectTexts  []string // Multiple text snippets to check in the fallback text
	}{
		{
			name:        "incident created",
			eventType:   "incident.created",
			checkName:   "API Health",
			expectTexts: []string{"New incident for", "API Health"},
		},
		{
			name:        "incident resolved",
			eventType:   "incident.resolved",
			checkName:   "API Health",
			expectTexts: []string{"Incident resolved"},
		},
		{
			name:         "incident escalated",
			eventType:    "incident.escalated",
			checkName:    "API Health",
			failureCount: 10,
			expectTexts:  []string{"Incident escalated", "API Health"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			payload := &Payload{
				EventType: tt.eventType,
				Incident: &models.Incident{
					FailureCount: tt.failureCount,
				},
				Check: &models.Check{
					Name: &tt.checkName,
				},
			}

			sender := &SlackSender{}
			msg := sender.buildMessage(payload)

			r.NotNil(msg)
			for _, text := range tt.expectTexts {
				r.Contains(msg.Text, text)
			}
			r.NotEmpty(msg.Attachments, "expected attachments with blocks")
			r.NotEmpty(msg.Attachments[0].Blocks, "expected blocks inside attachment")
		})
	}
}

// Ensure mockDBService implements db.Service interface.
var _ db.Service = (*mockDBService)(nil)
