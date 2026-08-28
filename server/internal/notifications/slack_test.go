package notifications

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// ptr returns a pointer to v, for building model fields in tests.
func ptr[T any](v T) *T { return &v }

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

func (m *mockDBService) DeleteStateEntry(_ context.Context, _ *string, _ string) (bool, error) {
	return true, nil
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

func (m *mockDBService) RepairMigrationChecksums(_ context.Context) ([]migrationguard.RepairResult, error) {
	panic("not implemented")
}
func (m *mockDBService) DB() *bun.DB { panic("not implemented") }
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

func (m *mockDBService) AddOrganizationPreviousSlug(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) GetOrganizationByPreviousSlug(_ context.Context, _ string) (*models.Organization, error) {
	panic("not implemented")
}

func (m *mockDBService) ReleaseOrganizationPreviousSlug(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) ReleaseOrganizationPreviousSlugsForOrg(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) ListOrganizationPreviousSlugs(
	_ context.Context, _ string,
) ([]*models.OrganizationPreviousSlug, error) {
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

func (m *mockDBService) CountDanglingOrganizationProviders(_ context.Context) (int, error) {
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

func (m *mockDBService) CountOwnersByOrg(_ context.Context, _ string) (int, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateCheck(_ context.Context, _ *models.Check) error {
	panic("not implemented")
}

func (m *mockDBService) GetCheck(_ context.Context, _, _ string) (*models.Check, error) {
	panic("not implemented")
}

func (m *mockDBService) GetChecksByUIDs(_ context.Context, _ string, _ []string) (map[string]*models.Check, error) {
	panic("not implemented")
}

func (m *mockDBService) ListChecks(
	_ context.Context, _ string, _ *models.ListChecksFilter,
) ([]*models.Check, int64, error) {
	panic("not implemented")
}

func (m *mockDBService) ListChecksByTunnelCheckUID(
	_ context.Context, _, _ string,
) ([]*models.Check, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateCheck(_ context.Context, _ string, _ *models.CheckUpdate) error {
	panic("not implemented")
}
func (m *mockDBService) DeleteCheck(_ context.Context, _ string) error { panic("not implemented") }

func (m *mockDBService) ListChecksWithStaleJobPeriods(_ context.Context) ([]*models.Check, error) {
	panic("not implemented")
}

func (m *mockDBService) ListChecksWithStaleJobRegions(_ context.Context) ([]*models.Check, error) {
	panic("not implemented")
}

func (m *mockDBService) ListChecksReferencingRegion(_ context.Context, _ string) ([]*models.Check, error) {
	panic("not implemented")
}

func (m *mockDBService) MigrateCheckRegionSlug(_ context.Context, _, _ string) ([]*models.Check, error) {
	panic("not implemented")
}

func (m *mockDBService) ListCheckJobsByRegion(_ context.Context, _ string) ([]*models.CheckJob, error) {
	panic("not implemented")
}

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

func (m *mockDBService) UpsertAggregatedResult(_ context.Context, _ *models.Result) error {
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

func (m *mockDBService) RecentResultsPerCheck(
	_ context.Context, _ *models.RecentResultsPerCheckFilter,
) ([]*models.Result, error) {
	panic("not implemented")
}

func (m *mockDBService) CountResultsByPeriodType(_ context.Context) (map[string]int64, error) {
	panic("not implemented")
}

func (m *mockDBService) GetResultNeighbors(
	_ context.Context, _, _, _ string, _ []string, _ time.Time, _ string,
) (string, string, error) {
	panic("not implemented")
}

func (m *mockDBService) DeleteResults(_ context.Context, _ string, _ []string) (int64, error) {
	panic("not implemented")
}

func (m *mockDBService) CompactResults(
	_ context.Context, _ *models.ListResultsFilter, _ models.AggregateResultsFunc,
) (models.CompactResultsOutcome, error) {
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

func (m *mockDBService) GetIncidentByNumber(_ context.Context, _ string, _ int64) (*models.Incident, error) {
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

func (m *mockDBService) ListIncidents(
	_ context.Context, _ *models.ListIncidentsFilter,
) ([]*models.Incident, int64, error) {
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

func (m *mockDBService) CountEscalationPolicyStepsByPolicy(
	_ context.Context, _ []string,
) (map[string]int, error) {
	return map[string]int{}, nil
}

func (m *mockDBService) CountChecksByEscalationPolicy(
	_ context.Context, _ string,
) (map[string]int, error) {
	return map[string]int{}, nil
}

func (m *mockDBService) CountCheckGroupsByEscalationPolicy(
	_ context.Context, _ string,
) (map[string]int, error) {
	return map[string]int{}, nil
}

func (m *mockDBService) CountChecksInheritingOrgDefault(
	_ context.Context, _ string,
) (int, error) {
	return 0, nil
}

func (m *mockDBService) GetEscalationPolicyStep(
	_ context.Context, _ string,
) (*models.EscalationPolicyStep, error) {
	return nil, errMockNotImplemented
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

func (m *mockDBService) UpdateEventPayload(_ context.Context, _ string, _ models.JSONMap) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteEventsBefore(_ context.Context, _ time.Time, _ int) (int64, error) {
	panic("not implemented")
}

func (m *mockDBService) GetEvent(_ context.Context, _ string) (*models.Event, error) {
	panic("not implemented")
}

func (m *mockDBService) ListEvents(_ context.Context, _ *models.ListEventsFilter) ([]*models.Event, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateChannel(_ context.Context, _ *models.Integration) error {
	panic("not implemented")
}

func (m *mockDBService) GetChannel(_ context.Context, _ string) (*models.Integration, error) {
	panic("not implemented")
}

func (m *mockDBService) ListChannels(
	_ context.Context, _ *models.ListIntegrationsFilter,
) ([]*models.Integration, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateChannel(
	_ context.Context, _ string, _ *models.IntegrationUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteChannel(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateCheckConnection(_ context.Context, _ *models.CheckConnection) error {
	panic("not implemented")
}

func (m *mockDBService) ListChannelsForCheck(_ context.Context, _ string) ([]*models.Integration, error) {
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

func (m *mockDBService) ListDefaultChannels(_ context.Context, _ string) ([]*models.Integration, error) {
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
func (m *mockDBService) SoftDeleteFinishedJobs(_ context.Context, _ time.Time, _ int) (int64, error) {
	panic("not implemented")
}

func (m *mockDBService) DeleteSoftDeletedJobs(_ context.Context, _ time.Time, _ int) (int64, error) {
	panic("not implemented")
}

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

func (m *mockDBService) DeleteUserToken(_ context.Context, _ string) (bool, error) {
	panic("not implemented")
}

func (m *mockDBService) DeleteUserTokensByOrg(_ context.Context, _ string) (int, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateOAuthClient(_ context.Context, _ *models.OAuthClient) error {
	panic("not implemented")
}

func (m *mockDBService) GetOAuthClientByClientID(_ context.Context, _ string) (*models.OAuthClient, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateDeviceAuthRequest(_ context.Context, _ *models.DeviceAuthRequest) error {
	panic("not implemented")
}

func (m *mockDBService) GetDeviceAuthRequestByUserCode(
	_ context.Context, _ string,
) (*models.DeviceAuthRequest, error) {
	panic("not implemented")
}

func (m *mockDBService) GetDeviceAuthRequestByDeviceCode(
	_ context.Context, _ string,
) (*models.DeviceAuthRequest, error) {
	panic("not implemented")
}

func (m *mockDBService) ResolveDeviceAuthRequest(
	_ context.Context, _ string, _ *models.DeviceAuthResolution,
) (bool, error) {
	panic("not implemented")
}

func (m *mockDBService) TouchDeviceAuthPoll(_ context.Context, _ string, _ time.Time) error {
	panic("not implemented")
}

func (m *mockDBService) ConsumeDeviceAuthRequest(_ context.Context, _ string) (bool, error) {
	panic("not implemented")
}

func (m *mockDBService) PurgeExpiredDeviceAuthRequests(_ context.Context, _ time.Time) (int64, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateUserPasskey(_ context.Context, _ *models.UserPasskey) error {
	panic("not implemented")
}

func (m *mockDBService) GetUserPasskey(_ context.Context, _ string) (*models.UserPasskey, error) {
	panic("not implemented")
}

func (m *mockDBService) GetUserPasskeyByCredentialID(_ context.Context, _ []byte) (*models.UserPasskey, error) {
	panic("not implemented")
}

func (m *mockDBService) ListUserPasskeysByUser(_ context.Context, _ string) ([]*models.UserPasskey, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateUserPasskey(_ context.Context, _ string, _ models.UserPasskeyUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteUserPasskey(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) GetWorkerBySlug(_ context.Context, _ string) (*models.Worker, error) {
	panic("not implemented")
}

func (m *mockDBService) RegisterOrUpdateWorker(_ context.Context, _ *models.Worker) (*models.Worker, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateWorkerHeartbeat(_ context.Context, _ string, _ []string, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) ListLiveWorkers(_ context.Context, _ time.Time) ([]*models.Worker, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateAgentEnrollmentToken(_ context.Context, _ *models.AgentEnrollmentToken) error {
	panic("not implemented")
}

func (m *mockDBService) ListAgentEnrollmentTokens(
	_ context.Context, _ string,
) ([]*models.AgentEnrollmentToken, error) {
	panic("not implemented")
}

func (m *mockDBService) DeleteAgentEnrollmentToken(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) GetCheckJobByUID(_ context.Context, _ string) (*models.CheckJob, error) {
	panic("not implemented")
}

func (m *mockDBService) GetAgentEnrollmentTokenByHash(
	_ context.Context, _ string,
) (*models.AgentEnrollmentToken, error) {
	panic("not implemented")
}

func (m *mockDBService) EnrollAgent(
	_ context.Context, _, _, _, _, _ string,
) (*models.Agent, error) {
	panic("not implemented")
}

func (m *mockDBService) GetAgent(_ context.Context, _ string) (*models.Agent, error) {
	panic("not implemented")
}

func (m *mockDBService) UpsertSystemAgentEnrollmentToken(
	_ context.Context, _ *models.AgentEnrollmentToken,
) error {
	panic("not implemented")
}

func (m *mockDBService) ListSystemAgentEnrollmentTokens(
	_ context.Context,
) ([]*models.AgentEnrollmentToken, error) {
	panic("not implemented")
}

func (m *mockDBService) RevokeSystemAgentEnrollmentTokensExcept(
	_ context.Context, _ []string,
) (int64, error) {
	panic("not implemented")
}

func (m *mockDBService) ListStaleSystemAgents(_ context.Context, _ time.Time) ([]*models.Agent, error) {
	panic("not implemented")
}

func (m *mockDBService) RetireSystemAgent(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) RetireAgentWorkerRow(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CheckAndStoreAgentNonce(
	_ context.Context, _, _ string, _ time.Time, _ time.Duration,
) error {
	panic("not implemented")
}

func (m *mockDBService) PruneAgentNonces(_ context.Context, _ time.Time) (int64, error) {
	panic("not implemented")
}

func (m *mockDBService) ListAgents(_ context.Context, _ string) ([]*models.Agent, error) {
	panic("not implemented")
}

func (m *mockDBService) ListAllAgents(_ context.Context) ([]*models.Agent, error) {
	panic("not implemented")
}

func (m *mockDBService) ListActiveAgentsByRegion(
	_ context.Context, _, _ string,
) ([]*models.Agent, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateAgentLastSeen(_ context.Context, _ string, _ time.Time) error {
	panic("not implemented")
}

func (m *mockDBService) RevokeAgent(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) ListPurgeableRevokedAgents(_ context.Context, _ time.Time) ([]*models.Agent, error) {
	panic("not implemented")
}

func (m *mockDBService) PurgeAgent(_ context.Context, _ string) error {
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

func (m *mockDBService) ListIncidentMemberChecks(
	_ context.Context, _ string,
) ([]*models.IncidentMemberCheck, error) {
	panic("not implemented")
}

func (m *mockDBService) ListIncidentMemberChecksByIncidentUIDs(
	_ context.Context, _ []string,
) (map[string][]*models.IncidentMemberCheck, error) {
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

func (m *mockDBService) GetLastResultForChecks(
	_ context.Context, _ string, _ []string,
) (map[string]*models.Result, error) {
	panic("not implemented")
}

func (m *mockDBService) ReapAbandonedResults(_ context.Context) (models.ReapAbandonedResultsOutcome, error) {
	return models.ReapAbandonedResultsOutcome{}, errMockNotImplemented
}

func (m *mockDBService) SaveResultWithStatusTracking(_ context.Context, _ *models.Result) error {
	panic("not implemented")
}

func (m *mockDBService) HasRawResultWithMessageID(
	_ context.Context, _, _, _ string, _ time.Time,
) (bool, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateCheckStatusAndClocks(
	_ context.Context, _ string, _ models.CheckStatus, _ int, _ *time.Time, _ models.IncidentClockUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) UpdateCheckFlapState(
	_ context.Context, _ string, _ int, _ time.Time,
) error {
	panic("not implemented")
}

func (m *mockDBService) CreateSeverity(_ context.Context, _ *models.Severity) error {
	panic("not implemented")
}

func (m *mockDBService) GetSeverity(_ context.Context, _, _ string) (*models.Severity, error) {
	panic("not implemented")
}

func (m *mockDBService) ListSeverities(
	_ context.Context, _ *models.ListSeveritiesFilter,
) ([]*models.Severity, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateSeverity(_ context.Context, _ string, _ *models.SeverityUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteSeverity(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) ClearOrgDefaultSeverity(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) GetOrgDefaultSeverity(_ context.Context, _ string) (*models.Severity, error) {
	panic("not implemented")
}

func (m *mockDBService) GetSystemParameter(_ context.Context, _ string) (*models.Parameter, error) {
	panic("not implemented")
}

func (m *mockDBService) SetSystemParameter(_ context.Context, _ string, _ any, _ bool) error {
	panic("not implemented")
}

func (m *mockDBService) GetOrCreateSystemParameter(
	_ context.Context, _ string, _ any, _ bool,
) (*models.Parameter, bool, error) {
	panic("not implemented")
}

func (m *mockDBService) DeleteSystemParameter(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) ListSystemParameters(_ context.Context) ([]*models.Parameter, error) {
	panic("not implemented")
}

func (m *mockDBService) GetChannelByProperty(
	_ context.Context, _, _, _ string,
) (*models.Integration, error) {
	panic("not implemented")
}

func (m *mockDBService) GetChannelByPropertyForOrg(
	_ context.Context, _, _, _, _ string,
) (*models.Integration, error) {
	panic("not implemented")
}

func (m *mockDBService) ListChannelsByProperty(
	_ context.Context, _, _, _ string,
) ([]*models.Integration, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateStatusPage(_ context.Context, _ *models.StatusPage) error {
	panic("not implemented")
}

func (m *mockDBService) CreateStatusPageWithDefaultSection(
	_ context.Context, _ *models.StatusPage, _ *models.StatusPageSection, _ []*models.StatusPageResource,
) error {
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

func (m *mockDBService) UpdateStatusPageCustomDomain(
	_ context.Context, _ string, _ *models.StatusPageCustomDomainUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) UpdateStatusPageBranding(
	_ context.Context, _ string, _ *models.StatusPageBrandingUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) GetStatusPageByCustomDomain(_ context.Context, _ string) (*models.StatusPage, error) {
	panic("not implemented")
}

func (m *mockDBService) ListStatusPagesWithCustomDomain(_ context.Context) ([]*models.StatusPage, error) {
	panic("not implemented")
}

func (m *mockDBService) CountStatusPagesWithCustomDomain(_ context.Context, _ string) (int, error) {
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

func (m *mockDBService) MaxStatusPageSectionPosition(_ context.Context, _ string) (int, error) {
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

func (m *mockDBService) MaxStatusPageResourcePosition(_ context.Context, _ string) (int, error) {
	panic("not implemented")
}

func (m *mockDBService) ReorderStatusPageResources(_ context.Context, _ string, _ []string) error {
	panic("not implemented")
}

func (m *mockDBService) ReorderStatusPageSections(_ context.Context, _ string, _ []string) error {
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

func (m *mockDBService) GetCheckGroupsByUIDs(
	_ context.Context, _ string, _ []string,
) (map[string]*models.CheckGroup, error) {
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

func (m *mockDBService) GetCheckGroupStatusCounts(
	_ context.Context, _ string,
) (map[string]map[models.CheckStatus]int, error) {
	panic("not implemented")
}

func (m *mockDBService) GetCheckStatusCounts(
	_ context.Context, _ string,
) ([]models.CheckStatusCount, error) {
	panic("not implemented")
}

func (m *mockDBService) GetOrgAvailability24h(
	_ context.Context, _ string, _, _ time.Time,
) (models.AvailabilityCounts, error) {
	panic("not implemented")
}

func (m *mockDBService) ListCheckUIDsByGroup(_ context.Context, _, _ string) ([]string, error) {
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

func (m *mockDBService) ListMaintenanceWindowsForCheck(
	_ context.Context, _ string,
) ([]*models.MaintenanceWindow, error) {
	panic("not implemented")
}

// SLO + report-schedule operations (spec 2026-08-20-01). Notifications never
// touch either, so every one panics rather than pretending to answer.

func (m *mockDBService) CreateSLO(_ context.Context, _ *models.SLO) error {
	panic("not implemented")
}

func (m *mockDBService) GetSLO(_ context.Context, _, _ string) (*models.SLO, error) {
	panic("not implemented")
}

func (m *mockDBService) GetSLOBySlug(_ context.Context, _, _ string) (*models.SLO, error) {
	panic("not implemented")
}

func (m *mockDBService) ListSLOs(
	_ context.Context, _ string, _ models.ListSLOsFilter,
) ([]*models.SLO, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateSLO(_ context.Context, _ string, _ models.SLOUpdate) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteSLO(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CountSLOs(_ context.Context, _ string) (int, error) {
	panic("not implemented")
}

func (m *mockDBService) ListSLOsForChecks(
	_ context.Context, _ string, _ []string,
) ([]*models.SLO, error) {
	panic("not implemented")
}

// SLO burn-rate alert policies (spec 2026-08-21-08). Same rule: notifications
// read the burn numbers off the incident's details, never from the database.

func (m *mockDBService) CreateSLOAlertPolicy(_ context.Context, _ *models.SLOAlertPolicy) error {
	panic("not implemented")
}

func (m *mockDBService) GetSLOAlertPolicy(
	_ context.Context, _, _ string,
) (*models.SLOAlertPolicy, error) {
	panic("not implemented")
}

func (m *mockDBService) ListSLOAlertPolicies(
	_ context.Context, _ string,
) ([]*models.SLOAlertPolicy, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateSLOAlertPolicy(
	_ context.Context, _ string, _ *models.SLOAlertPolicyUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) ListEnabledSLOAlertPolicies(
	_ context.Context, _ int,
) ([]*models.SLOAlertPolicy, error) {
	panic("not implemented")
}

func (m *mockDBService) FindActiveBurnIncident(
	_ context.Context, _, _ string,
) (*models.Incident, error) {
	panic("not implemented")
}

func (m *mockDBService) ListActiveBurnIncidentsForSLOs(
	_ context.Context, _ string, _ []string,
) ([]*models.Incident, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateReportSchedule(_ context.Context, _ *models.ReportSchedule) error {
	panic("not implemented")
}

func (m *mockDBService) GetReportSchedule(
	_ context.Context, _, _ string,
) (*models.ReportSchedule, error) {
	panic("not implemented")
}

func (m *mockDBService) ListReportSchedules(
	_ context.Context, _ string,
) ([]*models.ReportSchedule, error) {
	panic("not implemented")
}

func (m *mockDBService) ListEnabledReportSchedules(
	_ context.Context,
) ([]*models.ReportSchedule, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateReportSchedule(
	_ context.Context, _ string, _ models.ReportScheduleUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteReportSchedule(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) MarkReportScheduleRun(
	_ context.Context, _ string, _, _ time.Time,
) (bool, error) {
	panic("not implemented")
}

func (m *mockDBService) RemoveRecipientFromReportSchedules(
	_ context.Context, _, _ string,
) (int, error) {
	panic("not implemented")
}

func (m *mockDBService) ListMaintenanceWindowsForCheckGroup(
	_ context.Context, _ string,
) ([]*models.MaintenanceWindow, error) {
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

func (m *mockDBService) DeleteFilesByTopicPrefix(_ context.Context, _, _ string) (int, error) {
	return 0, nil
}

func (m *mockDBService) ListAttachmentsByTopicPrefix(
	_ context.Context, _ string, _ time.Time, _ int,
) ([]*models.File, error) {
	return nil, nil
}

func (m *mockDBService) GetIncidentAny(_ context.Context, _ string) (*models.Incident, error) {
	return &models.Incident{}, nil
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

func (m *mockDBService) CreateCheckDependency(_ context.Context, _ *models.CheckDependency) error {
	panic("not implemented")
}

func (m *mockDBService) GetCheckDependency(
	_ context.Context, _, _ string,
) (*models.CheckDependency, error) {
	panic("not implemented")
}

func (m *mockDBService) ListCheckDependenciesByOrg(
	_ context.Context, _ string,
) ([]*models.CheckDependency, error) {
	panic("not implemented")
}

func (m *mockDBService) ListCheckDependencyParents(
	_ context.Context, _ string,
) ([]*models.CheckDependency, error) {
	panic("not implemented")
}

func (m *mockDBService) ListCheckDependencyChildren(
	_ context.Context, _ string,
) ([]*models.CheckDependency, error) {
	panic("not implemented")
}

func (m *mockDBService) FindCheckDependencyEdge(
	_ context.Context, _, _ string,
) (*models.CheckDependency, error) {
	panic("not implemented")
}

func (m *mockDBService) UpdateCheckDependency(
	_ context.Context, _ string, _ *models.CheckDependencyUpdate,
) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteCheckDependency(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteCheckDependenciesForCheck(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) ListSuppressedChildIncidents(
	_ context.Context, _ string,
) ([]*models.Incident, error) {
	panic("not implemented")
}

func (m *mockDBService) FindActiveIncidentsForChecksInWindow(
	_ context.Context, _ []string, _, _ time.Time,
) ([]*models.Incident, error) {
	panic("not implemented")
}

func (m *mockDBService) AttachIncidentToRollupParent(
	_ context.Context, _, _ string,
) (bool, error) {
	panic("not implemented")
}

func (m *mockDBService) GetOrgEntitlements(_ context.Context, _ string) (*models.OrgEntitlements, error) {
	return nil, nil //nolint:nilnil // mock
}

func (m *mockDBService) UpsertOrgEntitlements(
	_ context.Context, _ *models.OrgEntitlements, _ *models.OrgEntitlementAudit,
) error {
	panic("not implemented")
}

func (m *mockDBService) ListOrgEntitlementAudits(
	_ context.Context, _ models.ListOrgEntitlementAuditsFilter,
) ([]*models.OrgEntitlementAudit, error) {
	panic("not implemented")
}

func (m *mockDBService) CreateOrgEntitlementAudit(
	_ context.Context, _ *models.OrgEntitlementAudit,
) error {
	panic("not implemented")
}

func (m *mockDBService) DeleteOrgEntitlements(
	_ context.Context, _ string, _ *models.OrgEntitlementAudit,
) error {
	panic("not implemented")
}

func (m *mockDBService) CountMembersForOrg(_ context.Context, _ string) (int, error) {
	return 0, nil
}

func (m *mockDBService) ReserveMonthlyUsage(
	_ context.Context, _, _, _ string, _ int,
) (bool, error) {
	return true, nil
}

func (m *mockDBService) GetMonthlyUsage(_ context.Context, _, _, _ string) (int, error) {
	return 0, nil
}

func (m *mockDBService) IncrementUsageCounter(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockDBService) ListOrgCheckRates(_ context.Context, _ string) ([]models.CheckRate, error) {
	return nil, nil
}

func (m *mockDBService) CreateIncidentNotification(_ context.Context, _ *models.IncidentNotification) error {
	return nil
}

func (m *mockDBService) MarkIncidentNotificationSentByUID(_ context.Context, _ string, _ time.Time, _ string) error {
	return nil
}

func (m *mockDBService) MarkIncidentNotificationFailedByUID(_ context.Context, _ string, _ time.Time, _ string) error {
	return nil
}

func (m *mockDBService) MarkIncidentNotificationSentByJob(
	_ context.Context, _ string, _ time.Time, _ string, _ *models.DeliveryDetails,
) error {
	return nil
}

func (m *mockDBService) MarkIncidentNotificationFailedByJob(
	_ context.Context, _ string, _ time.Time, _ string, _ bool, _ *models.DeliveryDetails,
) error {
	return nil
}

func (m *mockDBService) CancelIncidentNotificationsForIncident(
	_ context.Context, _ string, _ time.Time,
) (int64, error) {
	return 0, nil
}

func (m *mockDBService) UpdateIncidentNotificationDeliveryByMessageID(
	_ context.Context, _, _ string, _ *models.DeliveryDetails,
) error {
	return nil
}

func (m *mockDBService) UpdateIncidentNotificationDeliveryByMessageIDAnyOrg(
	_ context.Context, _ string, _ *models.DeliveryDetails,
) error {
	return nil
}

func (m *mockDBService) ListIncidentNotifications(
	_ context.Context, _ string, _ db.ListIncidentNotificationsFilter,
) ([]*models.IncidentNotificationRow, error) {
	return nil, nil
}

func (m *mockDBService) GetIncidentNotification(
	_ context.Context, _, _, _ string,
) (*models.IncidentNotificationRow, error) {
	return nil, nil //nolint:nilnil // mock
}

func (m *mockDBService) GetOrgNotification(
	_ context.Context, _, _ string,
) (*models.IncidentNotificationRow, error) {
	return nil, nil //nolint:nilnil // mock
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
		Integration: &models.Integration{
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
		Integration: &models.Integration{
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
		Integration: &models.Integration{
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

// TestSlackSender_Send_ResolvedNoThreadStateDoesNotPost is the defense-in-depth
// guard: a resolved event with no stored thread state must NOT fall through to
// postNewMessage (which would call client.PostMessage against the real Slack
// API and emit a context-free top-level orphan "resolved" message). Send must
// return nil without posting. The mock returns no thread state, and a real
// PostMessage would require a live network call, so a nil return with the
// thread fetch having run is the observable proof the early guard fired.
func TestSlackSender_Send_ResolvedNoThreadStateDoesNotPost(t *testing.T) {
	t.Parallel()

	for _, eventType := range []string{eventTypeIncidentResolved, eventTypeIncidentReopened} {
		t.Run(eventType, func(t *testing.T) {
			t.Parallel()

			payload := &Payload{
				EventType: eventType,
				Incident: &models.Incident{
					UID:             "incident-123",
					OrganizationUID: "org-123",
				},
				Check: &models.Check{},
				Integration: &models.Integration{
					Settings: models.JSONMap{
						"access_token": "xoxb-test-token",
						"channel_id":   "C123",
					},
				},
			}

			db := &mockDBService{
				getStateEntryFunc: func(_ context.Context, _ *string, _ string) (*models.StateEntry, error) {
					return nil, nil //nolint:nilnil // no stored thread state
				},
			}
			jctx := &jobdef.JobContext{DBService: db, Logger: slog.Default()}

			sender := &SlackSender{}
			err := sender.Send(context.Background(), jctx, payload)

			r := require.New(t)
			r.NoError(err, "resolved/reopened with no thread state returns without posting")
			r.Len(db.getStateCalls, 1, "thread state was looked up before the guard fired")
			r.Empty(db.setStateCalls, "no new thread state is stored (no message was posted)")
		})
	}
}

func TestSlackSender_buildMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		eventType    string
		checkName    string
		failureCount int
		expectTexts  []string // Multiple text snippets to check in the fallback text
		// wantAttachments is false for the resolved/reopened thread replies, which
		// render the status line once as the top-level Text with no attachment.
		wantAttachments bool
	}{
		{
			name:            "incident created",
			eventType:       "incident.created",
			checkName:       "API Health",
			expectTexts:     []string{"New incident for", "API Health"},
			wantAttachments: true,
		},
		{
			name:      "incident resolved",
			eventType: "incident.resolved",
			checkName: "API Health",
			// Self-contained, single-render status line: monitor named, no
			// attachment duplicating the body.
			expectTexts:     []string{"incident resolved after", "API Health"},
			wantAttachments: false,
		},
		{
			name:            "incident escalated",
			eventType:       "incident.escalated",
			checkName:       "API Health",
			failureCount:    10,
			expectTexts:     []string{"Incident escalated", "API Health"},
			wantAttachments: true,
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
			if tt.wantAttachments {
				r.NotEmpty(msg.Attachments, "expected attachments with blocks")
				r.NotEmpty(msg.Attachments[0].Blocks, "expected blocks inside attachment")
			} else {
				r.Empty(msg.Attachments, "resolved/reopened reply must not carry an attachment")
			}
		})
	}
}

// TestSlackSender_buildIncidentResolvedThreadReply_RenderedOnce pins the Defect A
// fix for resolved messages: the status line is rendered exactly once (as the
// top-level Text, with NO attachment repeating it — the previous shape duplicated
// the body and added a useless green border), and the message is self-contained:
// it names the monitor and links it to the dashboard when a base URL is configured.
func TestSlackSender_buildIncidentResolvedThreadReply_RenderedOnce(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const (
		baseURL = "https://app.example.com"
		orgSlug = "acme"
		checkID = "chk-api-health"
	)
	checkName := "API Health"
	resolvedAt := time.Now()
	payload := &Payload{
		EventType:  eventTypeIncidentResolved,
		OrgSlug:    orgSlug,
		AppBaseURL: baseURL,
		Incident: &models.Incident{
			UID:        "incident-1",
			StartedAt:  resolvedAt.Add(-28 * time.Minute),
			ResolvedAt: &resolvedAt,
		},
		Check: &models.Check{UID: checkID, Name: &checkName},
	}

	sender := &SlackSender{}
	msg := sender.buildIncidentResolvedThreadReply(payload)
	r.NotNil(msg)

	// No attachment at all: the green-border duplicate is gone.
	r.Empty(msg.Attachments, "resolved reply must not carry an attachment")
	r.Empty(msg.Blocks, "resolved reply renders only the top-level Text")

	// The status line is present exactly once (it only lives in Text now).
	r.Contains(msg.Text, "incident resolved after 28 minutes")
	r.Equal(1, strings.Count(msg.Text, "incident resolved after"),
		"status line must render exactly once")

	// Self-contained: monitor named and linked.
	r.Contains(msg.Text, checkName, "resolved reply must name the monitor")
	checkLink := "<" + baseURL + "/dash0/orgs/" + orgSlug + "/checks/" + checkID + "|" + checkName + ">"
	r.Contains(msg.Text, checkLink, "resolved reply must link the monitor to its dashboard page")

	// The at-a-glance success cue is kept (aligned with the dash0 registry's
	// 🟢 for incident.resolved).
	r.Contains(msg.Text, ":large_green_circle:")
}

// TestSlackSender_buildIncidentResolvedThreadReply_NamesMonitorWithoutBaseURL
// verifies the monitor is still named (plain text, no link) when no dashboard
// base URL is configured, so a non-threaded resolved is never anonymous.
func TestSlackSender_buildIncidentResolvedThreadReply_NamesMonitorWithoutBaseURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	checkName := "API Health"
	resolvedAt := time.Now()
	payload := &Payload{
		EventType: eventTypeIncidentResolved,
		Incident: &models.Incident{
			StartedAt:  resolvedAt.Add(-2 * time.Minute),
			ResolvedAt: &resolvedAt,
		},
		Check: &models.Check{Name: &checkName},
	}

	sender := &SlackSender{}
	msg := sender.buildIncidentResolvedThreadReply(payload)
	r.NotNil(msg)
	r.Empty(msg.Attachments)
	r.Contains(msg.Text, checkName, "monitor name must be present even without a base URL")
	r.NotContains(msg.Text, "<http", "no link wrapper when base URL is absent")
}

// TestSlackSender_buildIncidentReopenedThreadReply_RenderedOnce pins the Defect A
// fix for reopened messages: single top-level render, no duplicating attachment,
// monitor named + linked, warning cue kept.
func TestSlackSender_buildIncidentReopenedThreadReply_RenderedOnce(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	const (
		baseURL = "https://app.example.com"
		orgSlug = "acme"
		checkID = "chk-api-health"
	)
	checkName := "API Health"
	payload := &Payload{
		EventType:  eventTypeIncidentReopened,
		OrgSlug:    orgSlug,
		AppBaseURL: baseURL,
		Incident: &models.Incident{
			UID:          "incident-1",
			StartedAt:    time.Now().Add(-5 * time.Minute),
			RelapseCount: 2,
		},
		Check: &models.Check{UID: checkID, Name: &checkName, RecoveryPeriodSeconds: 60},
	}

	sender := &SlackSender{}
	msg := sender.buildIncidentReopenedThreadReply(payload)
	r.NotNil(msg)

	r.Empty(msg.Attachments, "reopened reply must not carry an attachment")
	r.Empty(msg.Blocks, "reopened reply renders only the top-level Text")

	r.Contains(msg.Text, "incident reopened (relapse #2)")
	r.Equal(1, strings.Count(msg.Text, "incident reopened"),
		"status line must render exactly once")

	r.Contains(msg.Text, checkName, "reopened reply must name the monitor")
	checkLink := "<" + baseURL + "/dash0/orgs/" + orgSlug + "/checks/" + checkID + "|" + checkName + ">"
	r.Contains(msg.Text, checkLink, "reopened reply must link the monitor to its dashboard page")

	// Aligned with the dash0 registry's 🔁 for incident.reopened.
	r.Contains(msg.Text, ":repeat:")
}

// TestSlackSender_DMChannelID verifies that a destination_type="dm" setting
// with a Slack user ID (U…) is passed to PostMessage unchanged. This ensures
// the sender does not try to look up or transform user IDs — Slack's
// chat.postMessage API accepts user IDs directly for DMs.
func TestSlackSender_DMChannelID(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Build a settings map with destination_type="dm" and a user-ID channel.
	settings := models.SlackSettings{
		AccessToken:     "xoxb-test-token",
		ChannelID:       "U0345CDEFG",
		DestinationType: "dm",
		DisplayName:     "@alice",
	}

	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)

	// determineChannel should return the user ID unchanged.
	payload := &Payload{
		Integration: &models.Integration{
			Settings: settingsMap,
		},
	}

	parsed, err := models.SlackSettingsFromJSONMap(payload.Integration.Settings)
	r.NoError(err)
	r.Equal("U0345CDEFG", parsed.ChannelID, "channel_id must be the user ID for DMs")
	r.Equal("dm", parsed.DestinationType)
	r.Equal("@alice", parsed.DisplayName)

	// Verify determineChannel returns the user ID as-is (no transformation).
	sender := &SlackSender{}
	channel := sender.determineChannel(parsed, payload)
	r.Equal("U0345CDEFG", channel, "DM channel_id must be passed through to PostMessage unchanged")
}

// --- UserContact / UserNotificationRoute stubs ---

func (m *mockDBService) ListUserContactsWithRoutes(
	_ context.Context, _, _ string,
) ([]*models.UserNotificationRoute, error) {
	return nil, nil
}

func (m *mockDBService) EnsureDefaultEmailRoute(
	_ context.Context, _, _, _ string,
) error {
	return nil
}

func (m *mockDBService) UpsertUserContact(_ context.Context, _ *models.UserContact) error {
	return nil
}

func (m *mockDBService) DeleteUserContact(_ context.Context, _ string) error {
	return nil
}

func (m *mockDBService) GetUserContact(_ context.Context, _ string) (*models.UserContact, error) {
	return nil, nil //nolint:nilnil // test stub
}

func (m *mockDBService) SetUserContactVerifyState(
	_ context.Context, _ string, _ *string, _ *time.Time, _ int,
) error {
	return nil
}

func (m *mockDBService) MarkUserContactVerified(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *mockDBService) ClearUserContactVerified(_ context.Context, _ string) error {
	return nil
}

func (m *mockDBService) ListUserContactsByTypeValue(
	_ context.Context, _, _ string,
) ([]*models.UserContact, error) {
	return nil, nil
}

func (m *mockDBService) EnsureUserNotificationRoute(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockDBService) SetRouteEnabled(_ context.Context, _ string, _ bool) error {
	return nil
}

func (m *mockDBService) ReorderRoutes(_ context.Context, _, _ string, _ []string) error {
	return nil
}

func (m *mockDBService) GetSlackChannelForOrg(_ context.Context, _ string) (*models.Integration, error) {
	return nil, nil //nolint:nilnil
}

// --- UserIntegrationIdentity stubs ---

func (m *mockDBService) ListUserIntegrationIdentities(
	_ context.Context, _ string,
) ([]*models.UserIntegrationIdentity, error) {
	return nil, nil
}

func (m *mockDBService) GetUserIntegrationIdentity(
	_ context.Context, _, _ string,
) (*models.UserIntegrationIdentity, error) {
	return nil, nil //nolint:nilnil
}

func (m *mockDBService) UpsertUserIntegrationIdentity(
	_ context.Context, _ *models.UserIntegrationIdentity,
) error {
	return nil
}

func (m *mockDBService) DeleteUserIntegrationIdentity(_ context.Context, _, _ string) error {
	return nil
}

// --- StatusUpdate stubs ---

func (m *mockDBService) ListStatusUpdates(
	_ context.Context, _ string, _ models.StatusUpdatesFilter,
) ([]*models.StatusUpdate, error) {
	return nil, nil
}

func (m *mockDBService) CreateStatusUpdate(_ context.Context, _ *models.StatusUpdate) error {
	return nil
}

func (m *mockDBService) GetStatusUpdateByUID(
	_ context.Context, _ string,
) (*models.StatusUpdate, error) {
	return nil, nil //nolint:nilnil
}

func (m *mockDBService) UpdateStatusUpdate(_ context.Context, _ *models.StatusUpdate) error {
	return nil
}

func (m *mockDBService) SoftDeleteStatusUpdate(_ context.Context, _ string) error {
	return nil
}

func (m *mockDBService) ListPublicStatusUpdates(
	_ context.Context, _ string, _ int,
) ([]*db.PublicStatusUpdate, error) {
	return nil, nil
}

// --- IncidentPublication stubs (spec 2026-08-19-08) ---

func (m *mockDBService) CreateIncidentPublication(
	_ context.Context, _ *models.IncidentPublication,
) error {
	return nil
}

func (m *mockDBService) GetIncidentPublication(
	_ context.Context, _, _ string,
) (*models.IncidentPublication, error) {
	return nil, nil //nolint:nilnil
}

func (m *mockDBService) FindIncidentPublication(
	_ context.Context, _, _ string,
) (*models.IncidentPublication, error) {
	return nil, nil //nolint:nilnil
}

func (m *mockDBService) ListIncidentPublications(
	_ context.Context, _ *models.ListIncidentPublicationsFilter,
) ([]*models.IncidentPublication, error) {
	return nil, nil
}

func (m *mockDBService) UpdateIncidentPublication(
	_ context.Context, _ string, _ *models.IncidentPublicationUpdate,
) error {
	return nil
}

func (m *mockDBService) SoftDeleteIncidentPublication(_ context.Context, _ string) error {
	return nil
}

func (m *mockDBService) CountIncidentPublicationsForIncident(
	_ context.Context, _ string,
) (int, error) {
	return 0, nil
}

func (m *mockDBService) ListStatusPageTargetsForCheck(
	_ context.Context, _ string, _ *string,
) ([]*db.StatusPageTarget, error) {
	return nil, nil
}

func (m *mockDBService) CreateSubscriber(_ context.Context, _ *models.StatusPageSubscriber) error {
	return nil
}

func (m *mockDBService) GetSubscriber(
	_ context.Context, _, _ string,
) (*models.StatusPageSubscriber, error) {
	return nil, nil //nolint:nilnil // Test stub returns nil when no mock is set
}

func (m *mockDBService) GetSubscriberByConfirmToken(
	_ context.Context, _ string,
) (*models.StatusPageSubscriber, error) {
	return nil, nil //nolint:nilnil // Test stub returns nil when no mock is set
}

func (m *mockDBService) GetSubscriberByUnsubToken(
	_ context.Context, _ string,
) (*models.StatusPageSubscriber, error) {
	return nil, nil //nolint:nilnil // Test stub returns nil when no mock is set
}

func (m *mockDBService) FindLiveSubscriber(
	_ context.Context, _, _ string, _ models.SubscriberScope, _ *string,
) (*models.StatusPageSubscriber, error) {
	return nil, nil //nolint:nilnil // Test stub returns nil when no mock is set
}

func (m *mockDBService) FindAnySubscriber(
	_ context.Context, _, _ string, _ models.SubscriberScope, _ *string,
) (*models.StatusPageSubscriber, error) {
	return nil, nil //nolint:nilnil // Test stub returns nil when no mock is set
}

func (m *mockDBService) ConfirmSubscriber(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *mockDBService) ResubscribeSubscriber(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockDBService) UpdateSubscriberDelivery(
	_ context.Context, _ string, _ int, _ *time.Time,
) error {
	panic("not implemented")
}

func (m *mockDBService) SoftDeleteSubscriber(_ context.Context, _ string) error {
	return nil
}

func (m *mockDBService) ListConfirmedSubscribers(
	_ context.Context, _ string, _ *string,
) ([]*models.StatusPageSubscriber, error) {
	return nil, nil
}

func (m *mockDBService) ListSubscribers(
	_ context.Context, _ string,
) ([]*models.StatusPageSubscriber, error) {
	return nil, nil
}

func (m *mockDBService) GetAppSetting(_ context.Context, _ string) (string, error) {
	panic("not implemented")
}

func (m *mockDBService) SetAppSetting(_ context.Context, _, _ string) error {
	panic("not implemented")
}

// TLS asset storage (spec 2026-07-26-01) — never exercised by notification
// tests; present only to satisfy db.Service.

func (m *mockDBService) TLSStorageStore(_ context.Context, _ string, _ []byte) error {
	panic("not implemented")
}

func (m *mockDBService) TLSStorageLoad(_ context.Context, _ string) ([]byte, error) {
	panic("not implemented")
}

func (m *mockDBService) TLSStorageDelete(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) TLSStorageExists(_ context.Context, _ string) (bool, error) {
	panic("not implemented")
}

func (m *mockDBService) TLSStorageList(_ context.Context, _ string) ([]models.TLSStorageKeyInfo, error) {
	panic("not implemented")
}

func (m *mockDBService) TLSStorageStat(_ context.Context, _ string) (models.TLSStorageKeyInfo, error) {
	panic("not implemented")
}

func (m *mockDBService) TLSStorageAcquireLock(
	_ context.Context, _, _ string, _ time.Time,
) (bool, error) {
	panic("not implemented")
}

func (m *mockDBService) TLSStorageRefreshLock(
	_ context.Context, _, _ string, _ time.Time,
) (bool, error) {
	panic("not implemented")
}

func (m *mockDBService) TLSStorageReleaseLock(_ context.Context, _, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) CreateEmailSuppression(_ context.Context, _ *models.EmailSuppression) error {
	panic("not implemented")
}

func (m *mockDBService) ListEmailSuppressions(_ context.Context, _ string) ([]*models.EmailSuppression, error) {
	panic("not implemented")
}

func (m *mockDBService) GetEmailSuppression(_ context.Context, _, _ string) (*models.EmailSuppression, error) {
	panic("not implemented")
}

func (m *mockDBService) DeleteEmailSuppression(_ context.Context, _ string) error {
	panic("not implemented")
}

func (m *mockDBService) IsEmailSuppressed(_ context.Context, _, _, _ string) (bool, error) {
	panic("not implemented")
}

// Ensure mockDBService implements db.Service interface.
var _ db.Service = (*mockDBService)(nil)

// collectBlockText flattens all mrkdwn text from a message's attachment blocks
// (section field text, section text, and context element text) into one string
// so tests can assert on rendered link patterns regardless of block layout.
func collectBlockText(msg *slack.MessageResponse) string {
	var sb strings.Builder
	for _, att := range msg.Attachments {
		for _, block := range att.Blocks {
			if block.Text != nil {
				sb.WriteString(block.Text.Text)
				sb.WriteString("\n")
			}
			for _, field := range block.Fields {
				sb.WriteString(field.Text)
				sb.WriteString("\n")
			}
			for _, el := range block.Elements {
				if ce, ok := el.(slack.ContextElement); ok {
					sb.WriteString(ce.Text)
					sb.WriteString("\n")
				}
			}
		}
	}
	return sb.String()
}

func TestSlackSender_DashboardLinks(t *testing.T) {
	t.Parallel()

	const (
		baseURL = "https://app.example.com"
		orgSlug = "acme"
		checkID = "chk-api-health"
		incUID  = "incident-789"
	)

	checkName := "API Health"
	checkLink := "<" + baseURL + "/dash0/orgs/" + orgSlug + "/checks/" + checkID
	incidentLink := "<" + baseURL + "/dash0/orgs/" + orgSlug + "/incidents/" + incUID

	newPayload := func(eventType string) *Payload {
		return &Payload{
			EventType:  eventType,
			OrgSlug:    orgSlug,
			AppBaseURL: baseURL,
			Incident: &models.Incident{
				UID:          incUID,
				StartedAt:    time.Now().Add(-5 * time.Minute),
				FailureCount: 3,
			},
			Check: &models.Check{
				UID:  checkID,
				Name: &checkName,
			},
		}
	}

	tests := []struct {
		name      string
		build     func(s *SlackSender, p *Payload) *slack.MessageResponse
		eventType string
		// wantCheckLinks is the number of times the check link must appear
		// (Monitor field + Monitor context tag).
		wantCheckLinks int
		// wantIncidentLinks is the minimum number of incident links expected.
		wantIncidentLinks int
	}{
		{
			name:              "incident created",
			eventType:         eventTypeIncidentCreated,
			build:             (*SlackSender).buildIncidentCreatedMessage,
			wantCheckLinks:    2,
			wantIncidentLinks: 1,
		},
		{
			name:              "incident escalated",
			eventType:         eventTypeIncidentEscalated,
			build:             (*SlackSender).buildIncidentEscalatedMessage,
			wantCheckLinks:    1,
			wantIncidentLinks: 2,
		},
		{
			name:              "resolved update",
			eventType:         eventTypeIncidentResolved,
			build:             (*SlackSender).buildResolvedUpdateMessage,
			wantCheckLinks:    2,
			wantIncidentLinks: 1,
		},
		{
			name:              "reopened update",
			eventType:         eventTypeIncidentReopened,
			build:             (*SlackSender).buildReopenedUpdateMessage,
			wantCheckLinks:    2,
			wantIncidentLinks: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			sender := &SlackSender{}
			msg := tt.build(sender, newPayload(tt.eventType))
			r.NotNil(msg)

			text := collectBlockText(msg)
			r.GreaterOrEqual(strings.Count(text, checkLink), tt.wantCheckLinks,
				"expected at least %d check dashboard links, got text: %s", tt.wantCheckLinks, text)
			r.GreaterOrEqual(strings.Count(text, incidentLink), tt.wantIncidentLinks,
				"expected at least %d incident dashboard links, got text: %s", tt.wantIncidentLinks, text)

			// The monitor field links the check name explicitly.
			r.Contains(text, checkLink+"|"+checkName+">",
				"monitor field must link the check name")
		})
	}
}

func TestSlackSender_DashboardLinks_EmptyBaseURLFallback(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	checkName := "API Health"
	payload := &Payload{
		EventType:  eventTypeIncidentCreated,
		OrgSlug:    "acme",
		AppBaseURL: "", // no base URL -> no links
		Incident: &models.Incident{
			UID:       "incident-789",
			StartedAt: time.Now().Add(-5 * time.Minute),
		},
		Check: &models.Check{
			Name: &checkName,
			Slug: ptr("api-health"),
		},
	}

	sender := &SlackSender{}
	msg := sender.buildIncidentCreatedMessage(payload)
	r.NotNil(msg)

	text := collectBlockText(msg)
	// No mrkdwn link syntax should appear when the base URL is empty.
	r.NotContains(text, "<https://", "no dashboard links expected with empty base URL")
	r.NotContains(text, "/dash0/orgs/", "no dashboard URLs expected with empty base URL")
	// Plain-text monitor name and tags must still be present.
	r.Contains(text, "*Monitor:*\n"+checkName, "monitor name must remain as plain text")
	r.Contains(text, ":warning: Incident", "incident tag must remain as plain text")
	r.Contains(text, ":large_blue_circle: Monitor", "monitor tag must remain as plain text")
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
		expected string
	}{
		// Sub-24h regression guards.
		{name: "seconds", duration: 30 * time.Second, expected: "30 seconds"},
		{name: "one minute", duration: time.Minute, expected: "1 minute"},
		{name: "minutes", duration: 5 * time.Minute, expected: "5 minutes"},
		{name: "one hour", duration: time.Hour, expected: "1 hour"},
		{name: "one hour and minutes", duration: time.Hour + 30*time.Minute, expected: "1 hour 30 minutes"},
		{name: "hours only", duration: 5 * time.Hour, expected: "5 hours"},
		{name: "hours and minutes", duration: 5*time.Hour + 12*time.Minute, expected: "5 hours 12 minutes"},
		{name: "just under a day", duration: 23*time.Hour + 59*time.Minute, expected: "23 hours 59 minutes"},
		// Day-scale rows from the spec.
		{name: "exactly one day", duration: 24 * time.Hour, expected: "1 day"},
		{name: "one day one hour", duration: 25 * time.Hour, expected: "1 day 1 hour"},
		{name: "one day three hours", duration: 27 * time.Hour, expected: "1 day 3 hours"},
		{name: "ten days", duration: 240 * time.Hour, expected: "10 days"},
		{name: "239h36m rounds to 9 days 23 hours", duration: 239*time.Hour + 36*time.Minute, expected: "9 days 23 hours"},
		// Singular/plural boundary cases.
		{name: "two days zero hours", duration: 48 * time.Hour, expected: "2 days"},
		{name: "two days one hour", duration: 49 * time.Hour, expected: "2 days 1 hour"},
		{name: "two days five hours", duration: 53 * time.Hour, expected: "2 days 5 hours"},
		{name: "one day twenty-three hours", duration: 47 * time.Hour, expected: "1 day 23 hours"},
		// Minutes are dropped at the days scale.
		{name: "one day one hour drops minutes", duration: 25*time.Hour + 59*time.Minute, expected: "1 day 1 hour"},
		{name: "one day zero hours with minutes drops minutes", duration: 24*time.Hour + 30*time.Minute, expected: "1 day"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			r.Equal(tt.expected, formatDuration(tt.duration))
		})
	}
}

// TestSlackSender_IncidentHeadersCarryTheRef pins the short `#42` reference in
// Slack headers. It is the SAME identifier a Telegram `/ack #42` takes, so a
// Slack-only reader has to be able to read it off the alert; and an incident
// created before the numbers existed must render no ref rather than "#0".
func TestSlackSender_IncidentHeadersCarryTheRef(t *testing.T) {
	t.Parallel()

	checkName := "API Health"

	newPayload := func(eventType string, number int64) *Payload {
		return &Payload{
			EventType:  eventType,
			OrgSlug:    "acme",
			AppBaseURL: "https://app.example.com",
			Incident: &models.Incident{
				UID:          "incident-42",
				Number:       number,
				StartedAt:    time.Now().Add(-5 * time.Minute),
				RelapseCount: 1,
				ResolvedAt:   ptr(time.Now()),
			},
			Check: &models.Check{Name: &checkName, Slug: ptr("api-health")},
		}
	}

	builders := map[string]func(*SlackSender, *Payload) *slack.MessageResponse{
		"incident.created":   (*SlackSender).buildIncidentCreatedMessage,
		"incident.escalated": (*SlackSender).buildIncidentEscalatedMessage,
		"incident.resolved":  (*SlackSender).buildIncidentResolvedThreadReply,
		"incident.reopened":  (*SlackSender).buildIncidentReopenedThreadReply,
	}

	sender := &SlackSender{}

	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			// The resolved/reopened variants are plain-text thread replies with
			// no blocks, so both surfaces are folded together here.
			rendered := func(number int64) string {
				msg := build(sender, newPayload(eventTypeIncidentCreated, number))

				return msg.Text + "\n" + collectBlockText(msg)
			}

			r.Contains(rendered(42), "#42",
				"the incident reference must reach the %s message", name)
			r.NotContains(rendered(0), "#0",
				"an unnumbered incident must render no reference")
		})
	}
}

// TestSlackSender_Send_CommentNoThreadStateDoesNotPost is the orphan guard for
// comments. Beyond "context-free message": posting top-level would CLAIM the
// incident's thread mapping, so the eventual resolved notice would thread under
// a comment instead of under the alert.
func TestSlackSender_Send_CommentNoThreadStateDoesNotPost(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := &Payload{
		EventType: eventTypeIncidentComment,
		Incident:  &models.Incident{UID: "incident-c1", OrganizationUID: "org-c1"},
		Check:     &models.Check{},
		Integration: &models.Integration{
			Settings: models.JSONMap{"access_token": "xoxb-test-token", "channel_id": "C123"},
		},
		Comment: &CommentInfo{Text: "central DNS looks down", AuthorName: "Ada"},
	}

	db := &mockDBService{
		getStateEntryFunc: func(_ context.Context, _ *string, _ string) (*models.StateEntry, error) {
			return nil, nil //nolint:nilnil // no stored thread state
		},
	}
	jctx := &jobdef.JobContext{DBService: db, Logger: slog.Default()}

	r.NoError((&SlackSender{}).Send(context.Background(), jctx, payload))
	r.Empty(db.setStateCalls, "a comment must never claim the incident's thread mapping")
}

// TestSlackSender_buildCommentThreadReply pins the rendered shape: author line
// with the comment emoji, and the body quoted under it.
func TestSlackSender_buildCommentThreadReply(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	name := "api-health"
	payload := &Payload{
		EventType: eventTypeIncidentComment,
		Incident:  &models.Incident{UID: "inc-1", Number: 42},
		Check:     &models.Check{Name: &name},
		Comment: &CommentInfo{
			Text: "central DNS looks down\nchecking resolvers", AuthorName: "Ada", Source: "slack",
		},
	}

	msg := (&SlackSender{}).buildMessage(payload)
	r.NotNil(msg)
	r.Contains(msg.Text, "Ada")
	r.Contains(msg.Text, "#42")
	r.Contains(msg.Text, "api-health")
	r.Contains(msg.Text, "via Slack")
	// Every line of a multi-line comment is quoted, not just the first.
	r.Contains(msg.Text, "> central DNS looks down")
	r.Contains(msg.Text, "> checking resolvers")
}

// TestCommentAuthor_FallsBackToNeutralLabel keeps an unresolved author from
// rendering as an empty byline in somebody's channel.
func TestCommentAuthor_FallsBackToNeutralLabel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("Someone", commentAuthor(nil))
	r.Equal("Someone", commentAuthor(&CommentInfo{Text: "x"}))
	r.Equal("Ada", commentAuthor(&CommentInfo{AuthorName: " Ada ", Text: "x"}))
	r.Empty(commentText(nil))
}
