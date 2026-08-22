// Package db provides the database abstraction layer for solidping.
package db

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrEnrollmentTokenInvalid is returned by EnrollAgent when the presented
// enrollment token hash does not match a live (unused, unexpired, not deleted)
// token — including the single-use race where a concurrent enrollment consumed
// it first.
var ErrEnrollmentTokenInvalid = errors.New("enrollment token is invalid, expired, or already used")

// ErrAgentNonceReplayed is returned by CheckAndStoreAgentNonce when a reconnect
// nonce was already consumed inside the retention window — a replayed
// signature, rejected cluster-wide rather than per API replica.
var ErrAgentNonceReplayed = errors.New("agent reconnect nonce already used")

// UsedEnrollmentTokenListWindow is how long a consumed enrollment token stays
// visible in ListAgentEnrollmentTokens after use. The register-an-agent wizard
// polls that list to learn its token's fate: without this window a token used
// the moment the agent starts would simply vanish, indistinguishable from an
// admin canceling it. View-only — a used token can never enroll again.
const UsedEnrollmentTokenListWindow = time.Hour

// StatusPageTarget pairs a status page with the resource on it that displays a
// particular check. Returned by ListStatusPageTargetsForCheck, which is the
// entry point of the incident auto-publish policy: given a failing check, which
// public pages are supposed to say something about it?
//
// ResourceAutoPublish is the resource-level override and is deliberately
// three-state: nil means "inherit the page", which is NOT the same as an
// explicit false.
type StatusPageTarget struct {
	PageUID             string
	SectionUID          string
	ResourceUID         string
	ResourceAutoPublish *bool
	ResourcePublicName  *string
	// ViaGroup reports whether the resource targets the check's GROUP rather
	// than the check itself. A group resource renders as one public component,
	// so its members are never named individually.
	ViaGroup bool
}

// PublicStatusUpdate holds a status update row for public status page display.
// This type is used by ListPublicStatusUpdates and is independent of the admin
// models so the DB layer does not need to know about the full status_updates model.
type PublicStatusUpdate struct {
	// PublicationUID threads the update under an incident publication (spec
	// 2026-08-19-08). nil for the loose operator-authored updates that predate
	// publications.
	PublicationUID *string
	UID            string
	SectionUID     *string
	CheckUID       *string
	IncidentUID    *string
	Title          string
	BodyMarkdown   string
	LinkURL        *string
	Kind           string
	PublishedAt    time.Time
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

	// RepairMigrationChecksums re-records checksums for every applied
	// migration this binary ships, without running any migration. Backs the
	// `solidping migrate repair` CLI command; see internal/db/migrationguard.
	RepairMigrationChecksums(ctx context.Context) ([]migrationguard.RepairResult, error)

	// DB returns the underlying bun.DB instance for direct queries
	DB() *bun.DB

	// Organization operations
	CreateOrganization(ctx context.Context, org *models.Organization) error
	GetOrganization(ctx context.Context, uid string) (*models.Organization, error)
	GetOrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error)
	ListOrganizations(ctx context.Context) ([]*models.Organization, error)
	UpdateOrganization(ctx context.Context, uid string, update models.OrganizationUpdate) error
	DeleteOrganization(ctx context.Context, uid string) error
	// GetOrganizationByLogoFileUID resolves the live organization whose current
	// logo is the given file. It is the authorization rule behind the unsigned
	// /pub/org-logos/:uid route: a file that is not some org's current logo is
	// simply not served there.
	GetOrganizationByLogoFileUID(ctx context.Context, fileUID string) (*models.Organization, error)

	// Organization previous-slug (rename alias) operations. See
	// models.OrganizationPreviousSlug for the semantics; the invariant is that
	// a live organizations.slug always wins over an alias, and that an alias
	// never resolves a soft-deleted organization.
	AddOrganizationPreviousSlug(ctx context.Context, orgUID, slug string) error
	GetOrganizationByPreviousSlug(ctx context.Context, slug string) (*models.Organization, error)
	ReleaseOrganizationPreviousSlug(ctx context.Context, slug string) error
	ReleaseOrganizationPreviousSlugsForOrg(ctx context.Context, orgUID string) error
	ListOrganizationPreviousSlugs(ctx context.Context, orgUID string) ([]*models.OrganizationPreviousSlug, error)

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
	// CountAdminsByOrg counts members holding at least the admin role (owners
	// included — they outrank admins).
	CountAdminsByOrg(ctx context.Context, orgUID string) (int, error)
	// CountOwnersByOrg counts the organization's live owners.
	CountOwnersByOrg(ctx context.Context, orgUID string) (int, error)

	// UserToken operations
	CreateUserToken(ctx context.Context, token *models.UserToken) error
	GetUserToken(ctx context.Context, uid string) (*models.UserToken, error)
	GetUserTokenByToken(ctx context.Context, token string) (*models.UserToken, error)
	ListUserTokens(ctx context.Context, userUID string) ([]*models.UserToken, error)
	ListUserTokensByType(ctx context.Context, userUID string, tokenType models.TokenType) ([]*models.UserToken, error)
	UpdateUserToken(ctx context.Context, uid string, update models.UserTokenUpdate) error
	// DeleteUserToken soft-deletes a token, returning whether a live row
	// existed to delete (compare-and-set on deleted_at IS NULL). Callers
	// relying on single-use rotation semantics (the OAuth refresh-grant
	// exchange) use the bool to detect a replay racing a concurrent
	// redemption; plain revocation callers may ignore it.
	DeleteUserToken(ctx context.Context, uid string) (bool, error)
	// DeleteUserTokensByOrg soft-deletes every token scoped to an organization
	// and returns the number of rows killed. Used when an organization is
	// deleted so no surviving session keeps org-scoped access.
	DeleteUserTokensByOrg(ctx context.Context, orgUID string) (int, error)

	// OAuth (MCP authorization server) operations. Only the client registry
	// has dedicated storage: authorization codes are issued/redeemed through
	// the generic State Storage operations below (state_entries, keyed by
	// oauth.authCodeKeyPrefix + a random suffix, scoped to the granting org),
	// and refresh grants are UserToken rows with type oauth_refresh managed
	// via the UserToken operations above.
	CreateOAuthClient(ctx context.Context, client *models.OAuthClient) error
	GetOAuthClientByClientID(ctx context.Context, clientID string) (*models.OAuthClient, error)

	// Device authorization operations (RFC 8628, spec 2026-08-08-02). The two
	// lookups return only live (unexpired) rows, so an expired request is
	// indistinguishable from an unknown one at the storage layer.
	CreateDeviceAuthRequest(ctx context.Context, req *models.DeviceAuthRequest) error
	GetDeviceAuthRequestByUserCode(ctx context.Context, userCode string) (*models.DeviceAuthRequest, error)
	GetDeviceAuthRequestByDeviceCode(ctx context.Context, deviceCode string) (*models.DeviceAuthRequest, error)
	// ResolveDeviceAuthRequest is a compare-and-set from pending to
	// approved/denied on a live row; it reports false when the request was
	// already resolved or has expired, so two concurrent approvals cannot both
	// mint a usable grant.
	ResolveDeviceAuthRequest(ctx context.Context, uid string, res *models.DeviceAuthResolution) (bool, error)
	// TouchDeviceAuthPoll records a poll timestamp so the token endpoint can
	// enforce the advertised interval (RFC 8628 slow_down).
	TouchDeviceAuthPoll(ctx context.Context, uid string, at time.Time) error
	// ConsumeDeviceAuthRequest hard-deletes a request, reporting whether THIS
	// caller won the delete. Exactly-once PAT delivery hangs off that boolean.
	ConsumeDeviceAuthRequest(ctx context.Context, uid string) (bool, error)
	// PurgeExpiredDeviceAuthRequests removes rows whose human never showed up.
	PurgeExpiredDeviceAuthRequests(ctx context.Context, before time.Time) (int64, error)

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
	// ListLiveWorkers returns every non-deleted worker whose last_active_at is
	// at or after since. It is the input to region capability aggregation: a
	// region advertises a family only while a LIVE worker there reports it, so
	// a stale worker's answer stops counting on its own (spec 2026-08-15-11).
	ListLiveWorkers(ctx context.Context, since time.Time) ([]*models.Worker, error)
	UpdateWorker(ctx context.Context, uid string, update models.WorkerUpdate) error
	DeleteWorker(ctx context.Context, uid string) error
	// RegisterOrUpdateWorker finds a worker by slug, creates it if not found, or updates it if exists.
	// Returns the registered/updated worker.
	RegisterOrUpdateWorker(ctx context.Context, worker *models.Worker) (*models.Worker, error)
	// UpdateWorkerHeartbeat updates the worker's last_active_at and updated_at
	// timestamps, and refreshes its self-reported capability set and build
	// version.
	//
	// THE CAPABILITY SET IS THREE-STATE. A NIL slice means "not reported" and
	// leaves the stored column exactly as it was — an executor that cannot
	// answer never overwrites a known set with a guess. A NON-NIL EMPTY slice
	// is a different statement, "I reported, and I have none of them", and IS
	// written. Anything that collapses the two turns "unknown" into "no".
	//
	// version IS TWO-STATE (spec 2026-08-19-07): an empty string means "not
	// reported" and leaves the stored value untouched, same "do not overwrite
	// a known answer with a guess" rule as capabilities — but unlike
	// capabilities there is no "reported and empty" state to protect, because
	// a real build version is never the empty string.
	UpdateWorkerHeartbeat(ctx context.Context, workerUID string, capabilities []string, version string) error

	// Deported-agent operations (spec 2026-07-16-02).
	// CreateAgentEnrollmentToken persists a one-shot enrollment token.
	CreateAgentEnrollmentToken(ctx context.Context, token *models.AgentEnrollmentToken) error
	// ListAgentEnrollmentTokens lists an org's live (unused, unexpired) enrollment
	// tokens, plus tokens used within UsedEnrollmentTokenListWindow so the UI can
	// report a consumed token's outcome instead of it just vanishing. Used tokens
	// are view-only: the enrollment-side lookups below still require unused.
	ListAgentEnrollmentTokens(ctx context.Context, orgUID string) ([]*models.AgentEnrollmentToken, error)
	// DeleteAgentEnrollmentToken soft-deletes an enrollment token (admin cancel).
	DeleteAgentEnrollmentToken(ctx context.Context, orgUID, uid string) error
	// GetAgentEnrollmentTokenByHash returns the live, still-usable token with the
	// given hash, or ErrEnrollmentTokenInvalid. Kind-aware: an org token must be
	// unused, a system token must have uses left. Non-consuming — used for the
	// pre-upgrade WS handshake check; EnrollAgent does the atomic consume.
	GetAgentEnrollmentTokenByHash(ctx context.Context, tokenHash string) (*models.AgentEnrollmentToken, error)
	// EnrollAgent consumes a valid enrollment token and creates the bound agent
	// row, returning the new agent. Org tokens are single-use under concurrency;
	// system tokens are multi-use within their (optional) max_uses budget, so a
	// whole platform fleet can enroll on boot with per-machine keypairs.
	EnrollAgent(ctx context.Context, tokenHash, name, ed25519Pub, x25519Pub, fingerprint string) (*models.Agent, error)
	// UpsertSystemAgentEnrollmentToken inserts or refreshes a seeded platform
	// enrollment token (SP_SYSTEM_AGENT_ENROLLMENT_TOKENS), idempotently.
	UpsertSystemAgentEnrollmentToken(ctx context.Context, token *models.AgentEnrollmentToken) error
	// ListSystemAgentEnrollmentTokens returns every live platform token. Never
	// exposed through the org-admin API.
	ListSystemAgentEnrollmentTokens(ctx context.Context) ([]*models.AgentEnrollmentToken, error)
	// RevokeSystemAgentEnrollmentTokensExcept soft-deletes live system tokens
	// whose hash is not listed — removing a token from the environment revokes
	// it on the next boot.
	RevokeSystemAgentEnrollmentTokensExcept(ctx context.Context, keepHashes []string) (int64, error)
	// ListStaleSystemAgents returns live system agents last seen before cutoff
	// (never-connected rows are judged on enrolled_at). Org agents are excluded.
	ListStaleSystemAgents(ctx context.Context, cutoff time.Time) ([]*models.Agent, error)
	// RetireSystemAgent revokes and soft-deletes one system agent (agent_gc).
	RetireSystemAgent(ctx context.Context, uid string) error
	// CheckAndStoreAgentNonce records a reconnect nonce, returning
	// ErrAgentNonceReplayed when it was already consumed within retain.
	CheckAndStoreAgentNonce(ctx context.Context, agentUID, nonce string, now time.Time, retain time.Duration) error
	// PruneAgentNonces deletes consumed nonces older than cutoff.
	PruneAgentNonces(ctx context.Context, cutoff time.Time) (int64, error)
	// GetAgent returns an agent by UID (any status).
	GetAgent(ctx context.Context, uid string) (*models.Agent, error)
	// ListAgents lists an org's agents (active and revoked, not deleted).
	ListAgents(ctx context.Context, orgUID string) ([]*models.Agent, error)
	// ListAllAgents lists every non-deleted agent across all organizations, both
	// org and system kind, for the fleet-wide operator view.
	ListAllAgents(ctx context.Context) ([]*models.Agent, error)
	// ListActiveAgentsByRegion returns the active agents bound to a fully-qualified
	// private region — the recipients credentials are sealed to.
	ListActiveAgentsByRegion(ctx context.Context, orgUID, region string) ([]*models.Agent, error)
	// UpdateAgentLastSeen sets an agent's last_seen_at.
	UpdateAgentLastSeen(ctx context.Context, uid string, at time.Time) error
	// RevokeAgent marks an agent revoked (it can no longer authenticate).
	RevokeAgent(ctx context.Context, orgUID, uid string) error
	// ListPurgeableRevokedAgents returns live agents (any kind) revoked before
	// cutoff (falling back to updated_at when revoked_at is NULL) — the
	// agent_gc job's purge candidates.
	ListPurgeableRevokedAgents(ctx context.Context, cutoff time.Time) ([]*models.Agent, error)
	// PurgeAgent soft-deletes a revoked agent. Scoped to status='revoked' so it
	// can never touch a live agent.
	PurgeAgent(ctx context.Context, uid string) error

	// Check operations
	CreateCheck(ctx context.Context, check *models.Check) error
	GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error)
	// GetChecksByUIDs returns the requested checks keyed by UID, in a single
	// batched query (absent UIDs simply have no entry) — replaces one GetCheck
	// call per row with a single IN(...) query for a whole response page.
	GetChecksByUIDs(ctx context.Context, orgUID string, checkUIDs []string) (map[string]*models.Check, error)
	GetCheckByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.Check, error)
	// GetCheckByEmailToken finds an email-type check by its config.token across all
	// organizations. The token alone is unique because it's 24 random bytes.
	GetCheckByEmailToken(ctx context.Context, token string) (*models.Check, error)
	ListChecks(ctx context.Context, orgUID string, filter *models.ListChecksFilter) ([]*models.Check, int64, error)
	// ListChecksByTunnelCheckUID returns the org's non-deleted checks that dial
	// through the given SSH check (`config.tunnelCheckUid`). Backs the delete
	// guard: removing a bastion that other checks tunnel through would silently
	// break them, so the API answers 409 with the dependents instead.
	ListChecksByTunnelCheckUID(ctx context.Context, orgUID, tunnelCheckUID string) ([]*models.Check, error)
	UpdateCheck(ctx context.Context, uid string, update *models.CheckUpdate) error
	DeleteCheck(ctx context.Context, uid string) error
	// ListChecksWithStaleJobPeriods returns enabled, non-deleted checks that
	// have at least one check_job whose period no longer matches the check's
	// period — the one-shot startup reconcile target (spec 2026-07-20-05).
	ListChecksWithStaleJobPeriods(ctx context.Context) ([]*models.Check, error)

	// CheckJob operations
	ListCheckJobsByCheckUID(ctx context.Context, checkUID string) ([]*models.CheckJob, error)
	DeleteCheckJob(ctx context.Context, uid string) error
	CreateCheckJob(ctx context.Context, job *models.CheckJob) error
	// GetCheckJobByUID returns one check job by UID.
	GetCheckJobByUID(ctx context.Context, uid string) (*models.CheckJob, error)

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
	// UpsertAggregatedResult writes an aggregated (non-raw) result idempotently:
	// it replaces any existing row for the same bucket key
	// (organization_uid, check_uid, coalesce(region,''), period_type,
	// period_start) inside one transaction, so re-running an aggregation for the
	// same bucket yields exactly one row instead of a duplicate. The NULL-proof
	// results_aggregated_unique_idx is the backstop (spec 2026-07-11-16).
	UpsertAggregatedResult(ctx context.Context, result *models.Result) error
	GetResult(ctx context.Context, uid string) (*models.Result, error)
	ListResults(ctx context.Context, filter *models.ListResultsFilter) (*models.ListResultsResponse, error)
	// CountResultsByPeriodType returns the total row count in `results` grouped
	// by period_type, across every organization. Table-wide and uncached —
	// only the aggregation-job-cadence gauge sampler may call this, never a
	// request path (spec 2026-08-17-04 §3).
	CountResultsByPeriodType(ctx context.Context) (map[string]int64, error)
	// GetResultNeighbors returns the UID of the next-older (prevUID) and
	// next-newer (nextUID) row in the same organization+check+periodType
	// series (optionally narrowed to regions), relative to the pivot
	// (pivotStart, pivotUID). Either UID is "" when no such neighbor exists
	// (the pivot is the oldest/newest row in scope). Ties on period_start are
	// broken by uid so same-timestamp rows get a stable total order.
	GetResultNeighbors(
		ctx context.Context, orgUID, checkUID, periodType string, regions []string,
		pivotStart time.Time, pivotUID string,
	) (prevUID, nextUID string, err error)
	// GetLastResultForChecks returns the newest raw result per requested
	// check (absent from the map when the check has no raw row), as one
	// index descent per check — never a scan of the organization's raw
	// history. There is deliberately no GetLastStatusChangeForChecks
	// companion: "when did this check last change status" is answered by
	// checks.status / checks.status_changed_at, which the incident path
	// maintains, not by re-deriving transitions from the raw rows that
	// survived retention (spec 2026-08-09-07).
	GetLastResultForChecks(
		ctx context.Context, orgUID string, checkUIDs []string,
	) (map[string]*models.Result, error)
	// ReapAbandonedResults finalizes raw results still sitting in
	// ResultStatusCreated well past any plausible execution window for their
	// check — models.AbandonedResultThreshold, "the check's period plus the
	// worker lease timeout, with a generous multiplier" (spec 2026-08-18-03).
	// Each eligible row is atomically flipped to ResultStatusAbandoned — the
	// dedicated terminal status that is excluded from availability everywhere
	// (spec 2026-08-18-10) — with an output explaining the worker never
	// reported, re-asserting the row's current status in the guard so a
	// legitimate finish racing the sweep is a no-op rather than a clobber.
	// ResultStatusRunning is deliberately left alone: heartbeat checks use it
	// as a legitimate long-lived status with no period/lease relationship.
	// Global, not per-org — mirrors ReapStuckJobs.
	ReapAbandonedResults(ctx context.Context) (models.ReapAbandonedResultsOutcome, error)
	DeleteResults(ctx context.Context, orgUID string, resultUIDs []string) (int64, error)
	// CompactResults atomically compacts one source bucket into a single
	// aggregated row inside one transaction: it fetches the source rows matching
	// filter, computes the rollup via aggregate (pure Go), upserts the rollup
	// idempotently on the bucket key, and deletes exactly the source UIDs
	// aggregate selected. On ANY error the whole transaction rolls back, so the
	// bucket stays fully raw and a later run retries cleanly — never a rollup+raw
	// hybrid (spec 2026-07-22-03). A marker-only bucket (aggregate returns no
	// sourceUIDs) commits without writing and reports Compacted=false.
	CompactResults(
		ctx context.Context, filter *models.ListResultsFilter, aggregate models.AggregateResultsFunc,
	) (models.CompactResultsOutcome, error)
	// SaveResultWithStatusTracking inserts a single new result row. (It formerly
	// also maintained the now-removed last_for_status flag; the name is kept to
	// avoid churn across its callers.)
	SaveResultWithStatusTracking(ctx context.Context, result *models.Result) error

	// Incident operations
	CreateIncident(ctx context.Context, incident *models.Incident) error
	GetIncident(ctx context.Context, orgUID, uid string) (*models.Incident, error)
	// GetIncidentAny looks an incident up by UID with NO org scoping. Its only
	// caller is the attachment topic authorizer (spec 2026-08-21-01), which has
	// no org to scope by BY DESIGN: the incident row is what names the
	// organization, so that a caller cannot pick one by forging a topic.
	GetIncidentAny(ctx context.Context, uid string) (*models.Incident, error)
	// GetIncidentByNumber resolves the short per-org `#42` reference — the form
	// humans type into Telegram and read in Slack. Returns sql.ErrNoRows if none.
	GetIncidentByNumber(ctx context.Context, orgUID string, number int64) (*models.Incident, error)
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
	// ListIncidents returns incidents matching filter plus the total count of
	// matching rows ignoring Limit/cursor (mirrors ListChecks).
	ListIncidents(ctx context.Context, filter *models.ListIncidentsFilter) ([]*models.Incident, int64, error)
	UpdateIncident(ctx context.Context, uid string, update *models.IncidentUpdate) error
	CountActiveIncidentsByCheckUID(ctx context.Context, checkUID string) (int, error)
	// ListExpiredSnoozedIncidents returns active incidents whose snoozed_until <= now.
	// Used by the auto-unsnooze sweeper.
	ListExpiredSnoozedIncidents(ctx context.Context, now time.Time) ([]*models.Incident, error)

	// On-call schedule operations
	CreateOnCallSchedule(ctx context.Context, schedule *models.OnCallSchedule) error
	GetOnCallSchedule(ctx context.Context, orgUID, scheduleUID string) (*models.OnCallSchedule, error)
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
	ListEscalationPolicies(ctx context.Context, orgUID string) ([]*models.EscalationPolicy, error)
	UpdateEscalationPolicy(ctx context.Context, policyUID string, update *models.EscalationPolicyUpdate) error
	DeleteEscalationPolicy(ctx context.Context, policyUID string) error

	// CountEscalationPolicyStepsByPolicy returns step counts keyed by policy
	// UID for the given policies (zero-step policies simply have no entry). Used
	// to badge "0 steps — silent" policies without loading every step row.
	CountEscalationPolicyStepsByPolicy(ctx context.Context, policyUIDs []string) (map[string]int, error)
	// CountChecksByEscalationPolicy returns, per policy UID, how many of the
	// org's live checks reference it directly. Used for the delete-guard usage
	// count and any inheritance UI.
	CountChecksByEscalationPolicy(ctx context.Context, orgUID string) (map[string]int, error)
	// CountCheckGroupsByEscalationPolicy returns, per policy UID, how many of the
	// org's live check groups reference it directly.
	CountCheckGroupsByEscalationPolicy(ctx context.Context, orgUID string) (map[string]int, error)
	// CountChecksInheritingOrgDefault returns how many of the org's live checks
	// resolve to no policy of their own (no direct policy, and either no group or
	// a group with no policy) — i.e. the blast radius of setting/changing the
	// org default escalation policy.
	CountChecksInheritingOrgDefault(ctx context.Context, orgUID string) (int, error)

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
	// ListIncidentMemberChecksByIncidentUIDs returns member rows for several
	// group incidents at once, grouped by incident UID — one batched query
	// instead of one ListIncidentMemberChecks call per group incident on a
	// response page.
	ListIncidentMemberChecksByIncidentUIDs(
		ctx context.Context, incidentUIDs []string,
	) (map[string][]*models.IncidentMemberCheck, error)
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

	// UpdateCheckFlapState persists the rolling flap counter and the
	// last-outage timestamp on a check. Called only on the rare incident
	// open/reopen (never on the per-result hot path) — spec 2026-06-30-07.
	UpdateCheckFlapState(
		ctx context.Context,
		checkUID string,
		flapCount int,
		lastOutageAt time.Time,
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
	// UpdateIncidentNotificationDeliveryByMessageID sets delivery_details on the
	// notification row whose message_id matches within the org. No-op (no error)
	// when nothing matches. Used by the Twilio delivery-status callback.
	UpdateIncidentNotificationDeliveryByMessageID(
		ctx context.Context, orgUID, messageID string, details *models.DeliveryDetails,
	) error
	// UpdateIncidentNotificationDeliveryByMessageIDAnyOrg is the org-agnostic
	// variant, for providers whose callbacks carry no organization context.
	// Meta's WhatsApp webhook is instance-level: it authenticates with the
	// app secret, not an org-scoped connection, and its `wamid.…` message ids
	// are globally unique, so matching on the id alone is unambiguous.
	// No-op (no error) when nothing matches.
	UpdateIncidentNotificationDeliveryByMessageIDAnyOrg(
		ctx context.Context, messageID string, details *models.DeliveryDetails,
	) error
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
	// SoftDeleteFinishedJobs marks up to `limit` terminal jobs
	// (success/retried/failed) still live (deleted_at IS NULL) whose updated_at
	// is before `before` as soft-deleted (deleted_at = now). Stage 1 of the
	// jobs_cleanup retention lifecycle. Returns the number of rows affected;
	// callers loop until a short batch to drain a backlog without a long txn.
	SoftDeleteFinishedJobs(ctx context.Context, before time.Time, limit int) (int64, error)
	// DeleteSoftDeletedJobs physically deletes up to `limit` jobs soft-deleted
	// before `before`, excluding any still referenced by another job's
	// previous_job_uid (the retry-chain FK guard — chains drain tail-first over
	// consecutive runs). Stage 2 of the jobs_cleanup retention lifecycle.
	// Returns the number of rows deleted.
	DeleteSoftDeletedJobs(ctx context.Context, before time.Time, limit int) (int64, error)

	// State Storage operations
	// GetStateEntry retrieves a state entry by organization and key.
	// Returns nil if not found (not an error). orgUID can be nil for global entries.
	GetStateEntry(ctx context.Context, orgUID *string, key string) (*models.StateEntry, error)
	// SetStateEntry creates or updates a state entry. TTL is optional (nil = never expires).
	// orgUID can be nil for global entries.
	SetStateEntry(ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration) error
	// DeleteStateEntry soft-deletes a state entry, returning whether a live
	// (not already deleted, not expired-out) row existed to delete. This is an
	// atomic compare-and-set: callers relying on single-use semantics (e.g. the
	// OAuth authorization-code consume step) use the returned bool to detect a
	// replay racing a concurrent redemption.
	DeleteStateEntry(ctx context.Context, orgUID *string, key string) (bool, error)
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
	// GetOrCreateSystemParameter returns the existing system parameter for key,
	// or atomically creates it with value. The bool reports whether THIS caller
	// created it.
	//
	// Atomicity is the whole point: several API pods booting together must all
	// end up on the SAME derived value (e.g. the Telegram webhook secret). A
	// read-then-write would let each pod generate its own, the last writer would
	// win, and every loser would then validate inbound requests against a secret
	// the third party no longer holds.
	GetOrCreateSystemParameter(
		ctx context.Context, key string, value any, secret bool,
	) (*models.Parameter, bool, error)
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
	// GetChannelByPropertyForOrg is the org-scoped variant of
	// GetChannelByProperty. Used by the Slack install flow so a workspace
	// (team_id) already connected to another org does not silently update
	// that org's row — each org gets its own connection for the same team.
	GetChannelByPropertyForOrg(
		ctx context.Context, orgUID, connType, propertyName, propertyValue string,
	) (*models.Integration, error)
	// ListChannelsByProperty returns every non-deleted connection matching
	// (connType, propertyName, propertyValue) ACROSS ALL ORGS, ordered
	// created_at ASC (oldest first). Used by the Slack inbound-routing
	// deterministic fallback (oldest connection wins when no home org is
	// recorded) and by app_uninstalled fan-out (every org's connection for a
	// team_id must be cleaned up, since they all share the same revoked bot
	// token). Returns an empty slice, not an error, when nothing matches.
	ListChannelsByProperty(
		ctx context.Context, connType, propertyName, propertyValue string,
	) ([]*models.Integration, error)
	// ListChannels lists connections matching filter. An empty
	// filter.OrganizationUID lists across ALL organizations — every other
	// field (Type, Enabled) still applies. Used with an empty org UID by
	// CountInstalledTeams for its global distinct-team-id count.
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
	// GetCheckGroupsByUIDs returns the requested check groups keyed by UID, in
	// a single batched query (absent UIDs simply have no entry).
	GetCheckGroupsByUIDs(ctx context.Context, orgUID string, groupUIDs []string) (map[string]*models.CheckGroup, error)
	GetCheckGroupBySlug(ctx context.Context, orgUID, slug string) (*models.CheckGroup, error)
	GetCheckGroupByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.CheckGroup, error)
	ListCheckGroups(ctx context.Context, orgUID string) ([]*models.CheckGroup, error)
	UpdateCheckGroup(ctx context.Context, orgUID, uid string, update *models.CheckGroupUpdate) error
	DeleteCheckGroup(ctx context.Context, uid string) error
	// GetCheckGroupStatusCounts returns, for every group in the org, the
	// per-status count of its enabled, non-deleted member checks (check_group_uid
	// -> status -> count). Ungrouped checks and groups with no enabled members
	// are simply absent from the map. Feeds models.RollupGroupStatus and the
	// memberStatusCounts API field (spec 2026-08-01-01).
	GetCheckGroupStatusCounts(ctx context.Context, orgUID string) (map[string]map[models.CheckStatus]int, error)
	// GetCheckStatusCounts returns the org-wide (status, enabled) histogram of
	// non-deleted, non-internal checks — one GROUP BY, never a
	// load-all-and-count, so the dashboard's KPI counters stay correct past the
	// checks list's 100-row page clamp (spec 2026-08-02-06). The non-internal
	// predicate mirrors the list endpoint's `internal=false` default so the
	// counters describe exactly the checks the operator can see in the list.
	GetCheckStatusCounts(ctx context.Context, orgUID string) ([]models.CheckStatusCount, error)
	// ListCheckUIDsByGroup returns the UIDs of the group's enabled, non-deleted
	// member checks — the same member set GetCheckGroupStatusCounts rolls up, so
	// a group's public status and its aggregated availability always describe
	// the same checks (spec 2026-08-01-03).
	ListCheckUIDsByGroup(ctx context.Context, orgUID, groupUID string) ([]string, error)

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
	// UpdateSubscriberDelivery records a webhook/Slack delivery outcome: the
	// consecutive-failure counter and the circuit-breaker timestamp (nil
	// clears it).
	UpdateSubscriberDelivery(ctx context.Context, uid string, failureCount int, disabledAt *time.Time) error
	ListConfirmedSubscribers(
		ctx context.Context, statusPageUID string, incidentUID *string,
	) ([]*models.StatusPageSubscriber, error)
	ListSubscribers(ctx context.Context, statusPageUID string) ([]*models.StatusPageSubscriber, error)

	// StatusPage operations
	CreateStatusPage(ctx context.Context, page *models.StatusPage) error
	GetStatusPage(ctx context.Context, orgUID, uid string) (*models.StatusPage, error)
	GetStatusPageBySlug(ctx context.Context, orgUID, slug string) (*models.StatusPage, error)
	GetStatusPageByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.StatusPage, error)
	// GetStatusPageByCustomDomain resolves the single live page bound to a
	// custom domain (the domain column is globally unique among live rows).
	GetStatusPageByCustomDomain(ctx context.Context, domain string) (*models.StatusPage, error)
	GetDefaultStatusPage(ctx context.Context, orgUID string) (*models.StatusPage, error)
	ListStatusPages(ctx context.Context, orgUID string) ([]*models.StatusPage, error)
	// ListStatusPagesWithCustomDomain lists every live page (across all orgs)
	// that has a custom domain set — the input to the periodic re-verify job.
	ListStatusPagesWithCustomDomain(ctx context.Context) ([]*models.StatusPage, error)
	// CountStatusPagesWithCustomDomain counts an org's live pages with a custom
	// domain set — the usage number enforced against MaxCustomDomains.
	CountStatusPagesWithCustomDomain(ctx context.Context, orgUID string) (int, error)
	UpdateStatusPage(ctx context.Context, uid string, update *models.StatusPageUpdate) error
	// UpdateStatusPageCustomDomain overwrites all custom-domain columns in one
	// write (set/clear/verify/re-verify all go through here).
	UpdateStatusPageCustomDomain(ctx context.Context, uid string, update *models.StatusPageCustomDomainUpdate) error
	// UpdateStatusPageBranding overwrites BOTH brand-asset columns in one
	// write, so set/replace/clear share a single shape (spec 2026-08-21-07).
	UpdateStatusPageBranding(ctx context.Context, uid string, update *models.StatusPageBrandingUpdate) error
	// GetStatusPageByAssetFileUID resolves the live, ENABLED status page whose
	// current logo or favicon is the given file. This is the whole
	// authorization rule behind the unsigned /pub/status-page-assets/:uid
	// route: replacing, clearing or disabling a page un-publishes the blob in
	// the same write.
	GetStatusPageByAssetFileUID(ctx context.Context, fileUID string) (*models.StatusPage, error)
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

	// Incident publication operations (spec 2026-08-19-08). The publication
	// overlay is what makes an incident visible on a status page; see
	// models.IncidentPublication.
	CreateIncidentPublication(ctx context.Context, pub *models.IncidentPublication) error
	// GetIncidentPublication reads one live publication, scoped to the org.
	GetIncidentPublication(ctx context.Context, orgUID, uid string) (*models.IncidentPublication, error)
	// FindIncidentPublication returns the live publication for an
	// (incident, page) pair, or sql.ErrNoRows. This is the read half of the
	// idempotency guarantee the partial unique index enforces.
	FindIncidentPublication(
		ctx context.Context, incidentUID, statusPageUID string,
	) (*models.IncidentPublication, error)
	ListIncidentPublications(
		ctx context.Context, filter *models.ListIncidentPublicationsFilter,
	) ([]*models.IncidentPublication, error)
	UpdateIncidentPublication(
		ctx context.Context, uid string, update *models.IncidentPublicationUpdate,
	) error
	SoftDeleteIncidentPublication(ctx context.Context, uid string) error
	// CountIncidentPublicationsForIncident is the retention guard: the incident
	// reaper must never delete an incident row a publication still points at,
	// or a public status page would lose the incident it is narrating.
	CountIncidentPublicationsForIncident(ctx context.Context, incidentUID string) (int, error)
	// ListStatusPageTargetsForCheck returns every live status-page resource
	// that displays the given check — directly, or through the check's group
	// (status_page_resources reference checks AND check groups). It is the
	// reverse of the page→resource walk the public renderer does, and the
	// entry point of the auto-publish policy.
	ListStatusPageTargetsForCheck(
		ctx context.Context, checkUID string, checkGroupUID *string,
	) ([]*StatusPageTarget, error)

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
	// ListMaintenanceWindowsForCheckGroup returns every non-deleted maintenance
	// window that puts the GROUP in maintenance: one targeting the group
	// directly, or one targeting any of its member checks. Recurrence is not
	// evaluated — callers decide active/inactive via models.IsActiveAt. Feeds
	// the status page group component (spec 2026-08-01-03).
	ListMaintenanceWindowsForCheckGroup(ctx context.Context, groupUID string) ([]*models.MaintenanceWindow, error)

	// SLO operations (spec 2026-08-20-01)
	CreateSLO(ctx context.Context, slo *models.SLO) error
	GetSLO(ctx context.Context, orgUID, uid string) (*models.SLO, error)
	GetSLOBySlug(ctx context.Context, orgUID, slug string) (*models.SLO, error)
	ListSLOs(ctx context.Context, orgUID string, filter models.ListSLOsFilter) ([]*models.SLO, error)
	UpdateSLO(ctx context.Context, uid string, update models.SLOUpdate) error
	DeleteSLO(ctx context.Context, orgUID, uid string) error
	// CountSLOs counts an org's live SLOs. Feeds the maxSlos entitlement.
	CountSLOs(ctx context.Context, orgUID string) (int, error)
	// ListSLOsForChecks returns the live SLOs scoped directly to any of the
	// given checks. Powers the "covered by an SLO" chip on check detail.
	ListSLOsForChecks(ctx context.Context, orgUID string, checkUIDs []string) ([]*models.SLO, error)

	// SLO burn-rate alert policies (spec 2026-08-21-08)
	CreateSLOAlertPolicy(ctx context.Context, policy *models.SLOAlertPolicy) error
	GetSLOAlertPolicy(ctx context.Context, orgUID, uid string) (*models.SLOAlertPolicy, error)
	ListSLOAlertPolicies(ctx context.Context, sloUID string) ([]*models.SLOAlertPolicy, error)
	UpdateSLOAlertPolicy(ctx context.Context, uid string, update *models.SLOAlertPolicyUpdate) error
	// ListEnabledSLOAlertPolicies is the burn evaluator's work queue: every
	// enabled policy across every org whose SLO is itself live and enabled,
	// oldest-evaluated first so a large install still makes progress under a
	// bounded per-sweep limit.
	ListEnabledSLOAlertPolicies(ctx context.Context, limit int) ([]*models.SLOAlertPolicy, error)
	// FindActiveBurnIncident returns the open burn incident for one
	// (SLO, policy) pair, if any. sql.ErrNoRows when there is none.
	FindActiveBurnIncident(ctx context.Context, sloUID, policyUID string) (*models.Incident, error)
	// ListActiveBurnIncidentsForSLOs powers the "burning" badge on the SLO
	// list: one query for the whole page rather than one per row.
	ListActiveBurnIncidentsForSLOs(ctx context.Context, orgUID string, sloUIDs []string) ([]*models.Incident, error)

	// ReportSchedule operations (spec 2026-08-20-01)
	CreateReportSchedule(ctx context.Context, schedule *models.ReportSchedule) error
	GetReportSchedule(ctx context.Context, orgUID, uid string) (*models.ReportSchedule, error)
	ListReportSchedules(ctx context.Context, orgUID string) ([]*models.ReportSchedule, error)
	// ListEnabledReportSchedules returns every enabled, non-deleted schedule
	// across all orgs — the report job's work list.
	ListEnabledReportSchedules(ctx context.Context) ([]*models.ReportSchedule, error)
	UpdateReportSchedule(ctx context.Context, uid string, update models.ReportScheduleUpdate) error
	DeleteReportSchedule(ctx context.Context, orgUID, uid string) error
	// MarkReportScheduleRun records the period a schedule was last reported
	// for. It is conditional on the stored last_period_start being NULL or
	// strictly older than periodStart, so two replicas racing on the same
	// closed period produce exactly one report: the loser updates 0 rows and
	// skips. Returns whether this caller won.
	MarkReportScheduleRun(ctx context.Context, uid string, periodStart, runAt time.Time) (bool, error)
	// RemoveRecipientFromReportSchedules drops an address from every one of the
	// org's report schedules. It is what makes an unsubscribe stick: leaving
	// the address on the schedule would keep re-suppressing the same send
	// forever, and the operator editing the schedule would silently re-enable
	// mail to someone who asked to stop. Returns how many schedules changed.
	RemoveRecipientFromReportSchedules(ctx context.Context, orgUID, email string) (int, error)

	// File operations
	CreateFile(ctx context.Context, file *models.File) error
	GetFile(ctx context.Context, orgUID, uid string) (*models.File, error)
	GetFileAny(ctx context.Context, uid string) (*models.File, error)
	ListFiles(
		ctx context.Context, orgUID string, filter models.ListFilesFilter,
	) ([]*models.File, int64, error)
	DeleteFile(ctx context.Context, orgUID, uid string) error
	// DeleteFilesByTopicPrefix soft-deletes every live attachment of an org
	// whose topic starts with prefix, and reports how many rows changed. This
	// is the entity-deletion reaper and the replace-on-reopen primitive
	// (spec 2026-08-21-01) — a no-match is NOT an error, unlike DeleteFile.
	DeleteFilesByTopicPrefix(ctx context.Context, orgUID, prefix string) (int, error)
	// ListAttachmentsByTopicPrefix returns live attachment rows across ALL
	// organizations whose topic starts with prefix and which were created
	// before `before`, capped at limit. Cross-org by design: its only caller
	// is the GC sweep, which has no org to scope to.
	ListAttachmentsByTopicPrefix(
		ctx context.Context, prefix string, before time.Time, limit int,
	) ([]*models.File, error)

	// CheckDependency operations
	CreateCheckDependency(ctx context.Context, dep *models.CheckDependency) error
	GetCheckDependency(ctx context.Context, orgUID, depUID string) (*models.CheckDependency, error)
	ListCheckDependenciesByOrg(ctx context.Context, orgUID string) ([]*models.CheckDependency, error)
	ListCheckDependencyParents(ctx context.Context, childCheckUID string) ([]*models.CheckDependency, error)
	ListCheckDependencyChildren(ctx context.Context, parentCheckUID string) ([]*models.CheckDependency, error)
	FindCheckDependencyEdge(ctx context.Context, parentUID, childUID string) (*models.CheckDependency, error)
	UpdateCheckDependency(ctx context.Context, depUID string, update *models.CheckDependencyUpdate) error
	DeleteCheckDependency(ctx context.Context, depUID string) error
	// DeleteCheckDependenciesForCheck soft-deletes every edge where checkUID is
	// either the parent or the child. Called when a check is deleted so its
	// dependency edges don't linger and resolve to an empty check ref later.
	DeleteCheckDependenciesForCheck(ctx context.Context, checkUID string) error
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
	// CountMembersForOrg counts every organization member, regardless of
	// how they joined. Used by the entitlements service to enforce
	// MaxUsers.
	CountMembersForOrg(ctx context.Context, orgUID string) (int, error)

	// ReserveMonthlyUsage atomically claims one unit of the (orgUID, kind,
	// periodStart) monthly counter provided the current count is below limit.
	// It returns true when a unit was reserved (the counter was incremented),
	// false when the monthly cap is already reached. Callers must gate limit<=0
	// themselves. periodStart is an ISO date string (first day of the month).
	ReserveMonthlyUsage(
		ctx context.Context, orgUID, kind, periodStart string, limit int,
	) (bool, error)

	// GetMonthlyUsage returns the current count for (orgUID, kind, periodStart),
	// or 0 when no row exists.
	GetMonthlyUsage(ctx context.Context, orgUID, kind, periodStart string) (int, error)
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
	// with the Contact relation eagerly loaded. A route is returned ONLY if its contact
	// joined — soft-deleted and missing contacts are both excluded — so every returned
	// route is guaranteed to carry a non-nil Contact.
	ListUserContactsWithRoutes(ctx context.Context, userUID, orgUID string) ([]*models.UserNotificationRoute, error)

	// EnsureDefaultEmailRoute idempotently creates one email contact and one enabled route
	// for the user in the org. Safe to call concurrently — uses INSERT … ON CONFLICT DO NOTHING.
	//
	// Deliberately a NO-OP when a contact matching (user, org, email) exists but is
	// soft-deleted: the user removed that method on purpose, and this is called on
	// every notification-list load, so re-seeding would make the email method
	// undeletable. Re-adding the address explicitly (UpsertUserContact) still revives it.
	EnsureDefaultEmailRoute(ctx context.Context, userUID, orgUID, email string) error

	// UpsertUserContact creates or restores a contact. On conflict (same user+org+type+value)
	// it undeletes the row and updates the label.
	//
	// Writes the CANONICAL uid back into c: a restore keeps the uid of the row already
	// in the table, so c.UID after this call is the uid that actually exists — not the
	// one the caller generated, which on the restore path was never inserted.
	UpsertUserContact(ctx context.Context, c *models.UserContact) error

	// GetUserContact returns a single non-deleted contact by UID.
	GetUserContact(ctx context.Context, uid string) (*models.UserContact, error)

	// SetUserContactVerifyState writes the in-flight verification columns
	// (code hash, expiry, attempt count). nil codeHash/expiresAt clears the
	// pending code while preserving the attempt count.
	SetUserContactVerifyState(
		ctx context.Context, uid string, codeHash *string, expiresAt *time.Time, attempts int,
	) error

	// MarkUserContactVerified stamps verified_at and clears the pending
	// verification columns.
	MarkUserContactVerified(ctx context.Context, uid string, at time.Time) error

	// ClearUserContactVerified removes the verified_at stamp from a contact,
	// leaving the row in place. Used when a provider tells us a destination is
	// permanently unreachable (a Telegram user blocked the bot): the contact
	// must stop being paged, but deleting it would lose the user's intent and
	// hide the fact that a reconnect is needed.
	ClearUserContactVerified(ctx context.Context, uid string) error

	// ListUserContactsByTypeValue returns every live contact with the given
	// type and value, across ALL users and organizations. Inbound provider
	// callbacks (a Telegram /stop, a block notification) identify the contact
	// only by its destination, with no org context — one chat can legitimately
	// be linked in several orgs, and an opt-out must reach all of them.
	ListUserContactsByTypeValue(
		ctx context.Context, contactType, value string,
	) ([]*models.UserContact, error)

	// DeleteUserContact soft-deletes a contact by UID and, in the SAME transaction,
	// hard-deletes the user_notification_routes row pointing at it.
	//
	// Removing the route is part of the contract, not an implementation detail:
	// the FK's `on delete cascade` only fires on a HARD delete, so a soft delete
	// alone would strand the route forever as an undeletable ghost row in the
	// dashboard. user_notification_routes has no deleted_at — hard delete is the
	// design for that table.
	DeleteUserContact(ctx context.Context, uid string) error

	// EnsureUserNotificationRoute idempotently creates an enabled route for an
	// existing contact, appended after the user's current routes. Safe to call
	// repeatedly (INSERT … ON CONFLICT (contact_uid) DO NOTHING), which is what
	// makes re-connecting an already-connected Telegram chat a no-op instead of
	// a duplicate.
	EnsureUserNotificationRoute(ctx context.Context, userUID, orgUID, contactUID string) error

	// SetRouteEnabled toggles the enabled flag on a route.
	SetRouteEnabled(ctx context.Context, routeUID string, enabled bool) error

	// ReorderRoutes sets the position of each route to its index in routeUIDs.
	// Only routes belonging to the given user+org are affected; unknown UIDs are ignored.
	ReorderRoutes(ctx context.Context, userUID, orgUID string, routeUIDs []string) error

	// GetSlackChannelForOrg returns the first enabled Slack channel for the org.
	// Returns nil, nil when no Slack channel is configured.
	GetSlackChannelForOrg(ctx context.Context, orgUID string) (*models.Integration, error)

	// --- UserIntegrationIdentities ---
	//
	// "Who is this member on this integration instance" — used for mentions and
	// attribution only. Deliberately separate from UserContacts, which answer
	// "how to page me" and carry verification state.

	// ListUserIntegrationIdentities returns every identity mapped on one
	// integration, ordered by display name.
	ListUserIntegrationIdentities(
		ctx context.Context, integrationUID string,
	) ([]*models.UserIntegrationIdentity, error)

	// GetUserIntegrationIdentity returns one member's identity on an
	// integration. Returns nil, nil when the member has no identity there —
	// "unmapped" is a normal state, not an error.
	GetUserIntegrationIdentity(
		ctx context.Context, integrationUID, userUID string,
	) (*models.UserIntegrationIdentity, error)

	// UpsertUserIntegrationIdentity writes an identity keyed on
	// (integration_uid, user_uid), so re-syncing never duplicates rows.
	UpsertUserIntegrationIdentity(ctx context.Context, identity *models.UserIntegrationIdentity) error

	// DeleteUserIntegrationIdentity removes a member's identity on an
	// integration. Hard delete — the unique (integration_uid, external_id)
	// index must be freed immediately so the id can be reassigned.
	DeleteUserIntegrationIdentity(ctx context.Context, integrationUID, userUID string) error

	// AppSettings operations

	// GetAppSetting returns the value for the given key.
	// Returns sql.ErrNoRows (wrapped) if the key does not exist.
	GetAppSetting(ctx context.Context, key string) (string, error)

	// SetAppSetting creates or updates a key/value pair (upsert).
	SetAppSetting(ctx context.Context, key, value string) error

	// --- TLS asset storage (spec 2026-07-26-01) ---
	//
	// Backing store for the certmagic.Storage implementation in
	// internal/tlsedge: ACME account keys, certificates, PRIVATE KEYS, OCSP
	// staples and distributed-challenge tokens. Keys are certmagic's own
	// path-like namespace (slash-separated, no leading/trailing slash).
	//
	// SECURITY: values contain private keys. Only internal/tlsedge may call
	// these; never surface them through an API, export, or debug endpoint.

	// TLSStorageStore upserts an asset and refreshes its modification time.
	TLSStorageStore(ctx context.Context, key string, value []byte) error

	// TLSStorageLoad returns the stored bytes, or sql.ErrNoRows (wrapped) when
	// the key does not exist.
	TLSStorageLoad(ctx context.Context, key string) ([]byte, error)

	// TLSStorageDelete removes the key and every key nested under it
	// ("<key>/..."). Deleting a missing key is not an error.
	TLSStorageDelete(ctx context.Context, key string) error

	// TLSStorageExists reports whether the key exists as a stored value.
	TLSStorageExists(ctx context.Context, key string) (bool, error)

	// TLSStorageList returns value-free metadata for the key itself and every
	// key nested under it, sorted by key. An empty prefix lists everything.
	TLSStorageList(ctx context.Context, prefix string) ([]models.TLSStorageKeyInfo, error)

	// TLSStorageStat returns metadata for one key, or sql.ErrNoRows (wrapped)
	// when it does not exist.
	TLSStorageStat(ctx context.Context, key string) (models.TLSStorageKeyInfo, error)

	// TLSStorageAcquireLock atomically claims the named lock for owner until
	// expiresAt, succeeding when the lock is free or its current lease has
	// expired. Returns false (not an error) when a live holder owns it.
	TLSStorageAcquireLock(ctx context.Context, key, owner string, expiresAt time.Time) (bool, error)

	// TLSStorageRefreshLock extends the lease of a lock this owner still holds.
	// Returns false when the lock was lost, so the caller stops refreshing.
	TLSStorageRefreshLock(ctx context.Context, key, owner string, expiresAt time.Time) (bool, error)

	// TLSStorageReleaseLock drops a lock this owner holds. A no-op when the
	// lock is already gone or was taken over.
	TLSStorageReleaseLock(ctx context.Context, key, owner string) error

	// --- EmailSuppressions (spec 2026-07-05-10, D4) ---

	// CreateEmailSuppression inserts a new suppression row.
	CreateEmailSuppression(ctx context.Context, sup *models.EmailSuppression) error

	// ListEmailSuppressions returns every suppression row for an org, newest
	// first — backs the dashboard suppression list.
	ListEmailSuppressions(ctx context.Context, orgUID string) ([]*models.EmailSuppression, error)

	// GetEmailSuppression returns a single suppression row scoped to an org.
	// Returns sql.ErrNoRows when it does not exist within the given org.
	GetEmailSuppression(ctx context.Context, orgUID, uid string) (*models.EmailSuppression, error)

	// DeleteEmailSuppression hard-deletes a suppression row by UID (the
	// "re-subscribe" action). Callers scope by org first via
	// GetEmailSuppression.
	DeleteEmailSuppression(ctx context.Context, uid string) error

	// IsEmailSuppressed reports whether (org, email) is currently suppressed
	// for checkUID — checking both the check-specific row and the org-wide
	// (check_uid IS NULL) row. checkUID may be "" to check only the org-wide
	// row.
	IsEmailSuppressed(ctx context.Context, orgUID, email, checkUID string) (bool, error)

	// Close closes the database connection and cleans up resources
	io.Closer
}
