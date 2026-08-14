// Package sqlite provides a SQLite implementation of the db.Service interface.
//
//nolint:revive // Methods implement db.Service interface
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"
	"github.com/uptrace/bun/migrate"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sloghook"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// backendLabel is the value used for the "backend" label on
// SQLite-flavored Prometheus metrics.
const backendLabel = "sqlite"

//go:embed migrations/*.sql
var migrationsFS embed.FS

// errResourceNotInSection signals that a status-page reorder request referenced
// a resource UID that is not part of the targeted section.
var errResourceNotInSection = errors.New("resource not in section")

// errSectionNotInPage signals that a status-page section reorder request
// referenced a section UID that is not part of the targeted page.
var errSectionNotInPage = errors.New("section not in status page")

// errForeignKeysNotEnabled signals that a freshly opened SQLite connection is
// not enforcing foreign keys, which would let a hard delete leave orphaned
// results rows and permanently halt aggregation (spec 2026-07-12-01 §4).
var errForeignKeysNotEnabled = errors.New("sqlite foreign key enforcement is not active on a fresh connection")

// Config holds SQLite configuration.
type Config struct {
	// DataDir is the directory where the database file will be stored
	DataDir string

	// InMemory creates an in-memory database (for testing)
	InMemory bool

	// LogSQL enables SQL query logging using slog
	LogSQL bool

	// RunMode determines the database filename suffix (e.g., "test" -> "solidping-test.db")
	RunMode string

	// Reset deletes the database file before creating (only for test/demo run modes)
	Reset bool
}

// Service implements db.Service for SQLite.
type Service struct {
	db      *bun.DB
	dataDir string

	// compactFailpoint, when non-nil, is invoked inside CompactResults after the
	// rollup upsert and before the source delete. It exists only so same-package
	// tests can prove the transaction rolls the upsert back when a later step
	// fails; production never sets it.
	compactFailpoint func(context.Context) error
}

// Compile-time assertion that Service implements db.Service.
var _ db.Service = (*Service)(nil)

const dataDirPerm = 0o750

// resolveDatabasePath determines the database path and handles reset if needed.
func resolveDatabasePath(cfg Config) (string, error) {
	if cfg.InMemory {
		return ":memory:", nil
	}

	if err := os.MkdirAll(cfg.DataDir, dataDirPerm); err != nil {
		return "", fmt.Errorf("failed to create data directory: %w", err)
	}

	// Determine database filename based on run mode
	dbFilename := "solidping.db"
	if cfg.RunMode != "" {
		dbFilename = fmt.Sprintf("solidping-%s.db", cfg.RunMode)
	}

	dbPath := filepath.Join(cfg.DataDir, dbFilename)

	// Reset database if requested and in test/demo mode
	if cfg.Reset && (cfg.RunMode == "test" || cfg.RunMode == "demo") {
		if err := resetSQLiteDatabase(dbPath); err != nil {
			return "", fmt.Errorf("failed to reset database: %w", err)
		}
	}

	return dbPath, nil
}

// New creates a new SQLite service.
func New(ctx context.Context, cfg Config) (*Service, error) {
	dbPath, err := resolveDatabasePath(cfg)
	if err != nil {
		return nil, err
	}

	// Build connection string with SQLite parameters. Foreign keys go into the
	// DSN so EVERY connection — including ones database/sql re-opens after an
	// error — enforces them; a per-pool one-shot PRAGMA is silently skipped by
	// such recycled connections and let hard deletes orphan results rows (spec
	// 2026-07-12-01 §4). The :memory: DSN carries no query string, so the
	// one-shot PRAGMA below is its (single-connection) belt-and-braces.
	connStr := dbPath
	if !cfg.InMemory {
		// Only add parameters for file-based databases (not :memory:)
		connStr = fmt.Sprintf("file:%s?cache=shared&mode=rwc&_journal_mode=WAL&_busy_timeout=30000&%s",
			dbPath, foreignKeysDSNParam())
	}

	sqldb, err := sql.Open(sqliteshim.ShimName, connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Configure connection pool for SQLite's single-writer model
	sqldb.SetMaxOpenConns(1)    // SQLite performs better with a single writer
	sqldb.SetMaxIdleConns(1)    // Keep one connection alive
	sqldb.SetConnMaxLifetime(0) // No connection expiration

	// Enable foreign keys (belt-and-braces; the DSN param above is the
	// connection-safe enforcement for file-based databases).
	if _, err := sqldb.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Fail fast if foreign keys are not actually being enforced.
	if err := verifyForeignKeysEnabled(ctx, sqldb, cfg.InMemory); err != nil {
		return nil, err
	}

	// Vacuum to reclaim unused space on startup
	if !cfg.InMemory {
		slog.InfoContext(ctx, "Vacuuming SQLite database...")
		vacuumStart := time.Now()

		if _, err := sqldb.ExecContext(ctx, "VACUUM"); err != nil {
			slog.WarnContext(ctx, "Failed to vacuum SQLite database", "error", err)
		} else {
			slog.InfoContext(ctx, "SQLite vacuum complete", "duration", time.Since(vacuumStart))
		}
	}

	bunDB := bun.NewDB(sqldb, sqlitedialect.New())

	// Query hook is always installed: it emits Prometheus histograms
	// (solidping_db_query_duration_seconds) regardless of cfg.LogSQL.
	// Verbose slog logging stays opt-in to avoid leaking secrets.
	bunDB.AddQueryHook(sloghook.New(cfg.LogSQL, backendLabel))

	// Expose connection-pool stats via /metrics. Cheap — the collector
	// only calls sql.DB.Stats() at scrape time. Idempotent per backend
	// label, so a test re-opening the DB after a reset is safe.
	prommetrics.RegisterDB(sqldb, backendLabel)

	return &Service{
		db:      bunDB,
		dataDir: cfg.DataDir,
	}, nil
}

// foreignKeysDSNParam returns the connection-string parameter that turns on
// foreign-key enforcement for whichever SQLite driver sqliteshim resolved at
// build time. The Cgo mattn driver (registered as "sqlite3") reads
// `_foreign_keys=on`; the pure-Go modernc driver (registered as "sqlite")
// applies `_pragma=foreign_keys(1)` on every connection it opens. Selecting by
// driver name keeps enforcement working regardless of the build target.
func foreignKeysDSNParam() string {
	if sqliteshim.DriverName() == "sqlite3" {
		return "_foreign_keys=on"
	}

	return "_pragma=foreign_keys(1)"
}

// verifyForeignKeysEnabled reads PRAGMA foreign_keys back and fails fast if
// enforcement is off — the exact silent state that let hard deletes orphan
// results rows and halt aggregation (spec 2026-07-12-01 §4).
//
// For a file-based database it first drops idle connections so the read lands
// on a genuinely fresh connection, exercising the DSN-level enforcement rather
// than the one-shot PRAGMA the pool's current connection already ran — a
// malformed DSN param is caught here. For :memory: the read stays on the
// existing connection: each :memory: connection is a separate database, so
// dropping it would both destroy the schema and (there being no DSN param for
// :memory:) read back a fresh connection with foreign keys off.
func verifyForeignKeysEnabled(ctx context.Context, sqldb *sql.DB, inMemory bool) error {
	if !inMemory {
		sqldb.SetMaxIdleConns(0)
		defer sqldb.SetMaxIdleConns(1)
	}

	var enabled int
	if err := sqldb.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled); err != nil {
		return fmt.Errorf("failed to read foreign_keys pragma: %w", err)
	}

	if enabled != 1 {
		return fmt.Errorf("%w (PRAGMA foreign_keys=%d)", errForeignKeysNotEnabled, enabled)
	}

	return nil
}

// resetSQLiteDatabase removes the database file and its WAL/SHM files to reset the database.
func resetSQLiteDatabase(dbPath string) error {
	// Remove the main database file and WAL/SHM files
	filesToRemove := []string{
		dbPath,
		dbPath + "-wal",
		dbPath + "-shm",
	}

	for _, file := range filesToRemove {
		if _, err := os.Stat(file); err == nil {
			slog.Info("Resetting database: removing file", "file", file)
			if err := os.Remove(file); err != nil {
				return fmt.Errorf("failed to remove %s: %w", file, err)
			}
		}
	}

	slog.Info("Database reset complete", "path", dbPath)

	return nil
}

// Initialize sets up the database schema using migrations.
func (s *Service) Initialize(ctx context.Context) error {
	migrations := migrate.NewMigrations()
	if err := migrations.Discover(migrationsFS); err != nil {
		return fmt.Errorf("failed to discover migrations: %w", err)
	}

	migrator := migrate.NewMigrator(s.db, migrations)
	if err := migrator.Init(ctx); err != nil {
		return fmt.Errorf("failed to init migrator: %w", err)
	}

	if _, err := migrator.Migrate(ctx); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

// DB returns the underlying bun.DB instance.
func (s *Service) DB() *bun.DB {
	return s.db
}

// Close closes the database connection.
func (s *Service) Close() error {
	return s.db.Close()
}

// Organization operations

// CreateOrganization inserts an organization and releases any rename alias
// still holding its slug.
//
// The release lives here, at the single choke point every creation path goes
// through (the API, and every connector's zero-member bootstrap), rather than
// in one caller: a slug held only as an alias is claimable, and once claimed it
// must never resolve to the renamed organization again — not even later, if the
// claiming organization is itself deleted (spec 2026-08-08-12).
func (s *Service) CreateOrganization(ctx context.Context, org *models.Organization) error {
	if _, err := s.db.NewInsert().Model(org).Exec(ctx); err != nil {
		return err
	}

	return s.ReleaseOrganizationPreviousSlug(ctx, org.Slug)
}

func (s *Service) GetOrganization(ctx context.Context, uid string) (*models.Organization, error) {
	org := new(models.Organization)

	err := s.db.NewSelect().
		Model(org).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return org, nil
}

func (s *Service) GetOrganizationBySlug(ctx context.Context, slug string) (*models.Organization, error) {
	org := new(models.Organization)

	err := s.db.NewSelect().
		Model(org).
		Where("slug = ?", slug).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return org, nil
}

func (s *Service) ListOrganizations(ctx context.Context) ([]*models.Organization, error) {
	var orgs []*models.Organization

	err := s.db.NewSelect().
		Model(&orgs).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)

	return orgs, err
}

func (s *Service) UpdateOrganization(ctx context.Context, uid string, update models.OrganizationUpdate) error {
	query := s.db.NewUpdate().
		Model((*models.Organization)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Slug != nil {
		query = query.Set("slug = ?", *update.Slug)
	}

	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}

	switch {
	case update.ClearDefaultEscalationPolicyUID:
		query = query.Set("default_escalation_policy_uid = NULL")
	case update.DefaultEscalationPolicyUID != nil:
		query = query.Set("default_escalation_policy_uid = ?", *update.DefaultEscalationPolicyUID)
	}

	switch {
	case update.ClearLogoURL:
		query = query.Set("logo_url = NULL")
	case update.LogoURL != nil:
		query = query.Set("logo_url = ?", *update.LogoURL)
	}

	switch {
	case update.ClearLogoFileUID:
		query = query.Set("logo_file_uid = NULL")
	case update.LogoFileUID != nil:
		query = query.Set("logo_file_uid = ?", *update.LogoFileUID)
	}

	if _, err := query.Exec(ctx); err != nil {
		return err
	}

	// Same rule as CreateOrganization: an organization now living on this slug
	// outranks any alias that still held it.
	if update.Slug != nil {
		return s.ReleaseOrganizationPreviousSlug(ctx, *update.Slug)
	}

	return nil
}

func (s *Service) DeleteOrganization(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.Organization)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// GetOrganizationByLogoFileUID returns the live organization whose current logo
// is the given file. This is the authorization rule behind the unsigned
// /pub/org-logos/:uid route: only a file that is some live org's CURRENT logo
// is public, so retiring a logo (replacing or clearing it) immediately
// un-publishes the old blob.
func (s *Service) GetOrganizationByLogoFileUID(ctx context.Context, fileUID string) (*models.Organization, error) {
	org := new(models.Organization)

	err := s.db.NewSelect().
		Model(org).
		Where("logo_file_uid = ?", fileUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return org, nil
}

// AddOrganizationPreviousSlug records a slug an organization has just renamed
// away from. Any live alias already holding that slug is released first: the
// partial unique index allows a single live alias per slug, and the newest
// claim is the truthful one.
func (s *Service) AddOrganizationPreviousSlug(ctx context.Context, orgUID, slug string) error {
	if err := s.ReleaseOrganizationPreviousSlug(ctx, slug); err != nil {
		return err
	}

	alias := models.NewOrganizationPreviousSlug(orgUID, slug)

	_, err := s.db.NewInsert().Model(alias).Exec(ctx)

	return err
}

// GetOrganizationByPreviousSlug resolves a rename alias to its organization.
//
// It deliberately delegates the second hop to GetOrganization, which filters
// `deleted_at IS NULL`: a soft-deleted organization must never be reachable
// through its previous slug (spec 2026-08-08-11 — a deleted org's slug 404s
// immediately, with no alias, tombstone or redirect).
//
// Callers must try GetOrganizationBySlug FIRST; a live org always wins over an
// alias.
func (s *Service) GetOrganizationByPreviousSlug(ctx context.Context, slug string) (*models.Organization, error) {
	alias := new(models.OrganizationPreviousSlug)

	err := s.db.NewSelect().
		Model(alias).
		Where("slug = ?", slug).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return s.GetOrganization(ctx, alias.OrganizationUID)
}

// ReleaseOrganizationPreviousSlug drops every live alias on a slug, whichever
// org holds it. Called when a slug is claimed by a real organization (creation
// or rename) so an alias can never resolve across tenants once reclaimed.
func (s *Service) ReleaseOrganizationPreviousSlug(ctx context.Context, slug string) error {
	_, err := s.db.NewUpdate().
		Model((*models.OrganizationPreviousSlug)(nil)).
		Where("slug = ?", slug).
		Where("deleted_at IS NULL").
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// ReleaseOrganizationPreviousSlugsForOrg drops every alias of an organization.
// Used on org deletion so nothing of the deleted org keeps holding slugs.
func (s *Service) ReleaseOrganizationPreviousSlugsForOrg(ctx context.Context, orgUID string) error {
	_, err := s.db.NewUpdate().
		Model((*models.OrganizationPreviousSlug)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// ListOrganizationPreviousSlugs returns an organization's live aliases, newest
// first.
func (s *Service) ListOrganizationPreviousSlugs(
	ctx context.Context, orgUID string,
) ([]*models.OrganizationPreviousSlug, error) {
	var aliases []*models.OrganizationPreviousSlug

	err := s.db.NewSelect().
		Model(&aliases).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)

	return aliases, err
}

// OrganizationProvider operations

func (s *Service) CreateOrganizationProvider(ctx context.Context, provider *models.OrganizationProvider) error {
	_, err := s.db.NewInsert().Model(provider).Exec(ctx)
	return err
}

func (s *Service) GetOrganizationProvider(ctx context.Context, uid string) (*models.OrganizationProvider, error) {
	provider := new(models.OrganizationProvider)

	err := s.db.NewSelect().
		Model(provider).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return provider, nil
}

func (s *Service) GetOrganizationProviderByProviderID(
	ctx context.Context, providerType models.ProviderType, providerID string,
) (*models.OrganizationProvider, error) {
	provider := new(models.OrganizationProvider)

	err := s.db.NewSelect().
		Model(provider).
		Where("provider_type = ?", providerType).
		Where("provider_id = ?", providerID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return provider, nil
}

func (s *Service) ListOrganizationProviders(
	ctx context.Context, orgUID string,
) ([]*models.OrganizationProvider, error) {
	var providers []*models.OrganizationProvider

	err := s.db.NewSelect().
		Model(&providers).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)

	return providers, err
}

func (s *Service) UpdateOrganizationProvider(
	ctx context.Context, uid string, update models.OrganizationProviderUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.OrganizationProvider)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.ProviderName != nil {
		query = query.Set("provider_name = ?", *update.ProviderName)
	}

	if update.Metadata != nil {
		query = query.Set("metadata = ?", *update.Metadata)
	}

	_, err := query.Exec(ctx)

	return err
}

func (s *Service) DeleteOrganizationProvider(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.OrganizationProvider)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// User operations

func (s *Service) CreateUser(ctx context.Context, user *models.User) error {
	_, err := s.db.NewInsert().Model(user).Exec(ctx)
	return err
}

func (s *Service) GetUser(ctx context.Context, uid string) (*models.User, error) {
	user := new(models.User)

	err := s.db.NewSelect().
		Model(user).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return user, nil
}

func (s *Service) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	user := new(models.User)

	err := s.db.NewSelect().
		Model(user).
		Where("lower(email) = lower(?)", email).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return user, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]*models.User, error) {
	var users []*models.User

	err := s.db.NewSelect().
		Model(&users).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)

	return users, err
}

func (s *Service) UpdateUser(ctx context.Context, uid string, update *models.UserUpdate) error {
	query := s.db.NewUpdate().
		Model((*models.User)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Email != nil {
		query = query.Set("email = ?", *update.Email)
	}

	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}

	if update.AvatarURL != nil {
		query = query.Set("avatar_url = ?", *update.AvatarURL)
	}

	if update.PasswordHash != nil {
		query = query.Set("password_hash = ?", *update.PasswordHash)
	}

	if update.EmailVerifiedAt != nil {
		query = query.Set("email_verified_at = ?", *update.EmailVerifiedAt)
	}

	if update.SuperAdmin != nil {
		query = query.Set("super_admin = ?", *update.SuperAdmin)
	}

	if update.TOTPSecret != nil {
		query = query.Set("totp_secret = ?", *update.TOTPSecret)
	}

	if update.TOTPEnabled != nil {
		query = query.Set("totp_enabled = ?", *update.TOTPEnabled)
	}

	if update.TOTPRecoveryCodes != nil {
		codesJSON, jsonErr := json.Marshal(*update.TOTPRecoveryCodes)
		if jsonErr != nil {
			return fmt.Errorf("failed to marshal recovery codes: %w", jsonErr)
		}

		query = query.Set("totp_recovery_codes = ?", string(codesJSON))
	}

	if update.LastActiveAt != nil {
		query = query.Set("last_active_at = ?", *update.LastActiveAt)
	}

	_, err := query.Exec(ctx)

	return err
}

func (s *Service) DeleteUser(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.User)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// UserProvider operations

func (s *Service) CreateUserProvider(ctx context.Context, provider *models.UserProvider) error {
	_, err := s.db.NewInsert().Model(provider).Exec(ctx)
	return err
}

func (s *Service) GetUserProvider(ctx context.Context, uid string) (*models.UserProvider, error) {
	provider := new(models.UserProvider)

	err := s.db.NewSelect().
		Model(provider).
		Where("uid = ?", uid).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return provider, nil
}

func (s *Service) GetUserProviderByProviderID(
	ctx context.Context, providerType models.ProviderType, providerID string,
) (*models.UserProvider, error) {
	provider := new(models.UserProvider)

	err := s.db.NewSelect().
		Model(provider).
		Where("provider_type = ?", providerType).
		Where("provider_id = ?", providerID).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return provider, nil
}

func (s *Service) ListUserProvidersByUser(ctx context.Context, userUID string) ([]*models.UserProvider, error) {
	var providers []*models.UserProvider

	err := s.db.NewSelect().
		Model(&providers).
		Where("user_uid = ?", userUID).
		Order("created_at DESC").
		Scan(ctx)

	return providers, err
}

func (s *Service) DeleteUserProvider(ctx context.Context, uid string) error {
	_, err := s.db.NewDelete().
		Model((*models.UserProvider)(nil)).
		Where("uid = ?", uid).
		Exec(ctx)

	return err
}

// OrganizationMember operations

func (s *Service) CreateOrganizationMember(ctx context.Context, member *models.OrganizationMember) error {
	_, err := s.db.NewInsert().Model(member).Exec(ctx)
	return err
}

func (s *Service) GetOrganizationMember(ctx context.Context, uid string) (*models.OrganizationMember, error) {
	member := new(models.OrganizationMember)

	err := s.db.NewSelect().
		Model(member).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return member, nil
}

func (s *Service) GetMemberByUserAndOrg(
	ctx context.Context, userUID, orgUID string,
) (*models.OrganizationMember, error) {
	member := new(models.OrganizationMember)

	err := s.db.NewSelect().
		Model(member).
		Where("user_uid = ?", userUID).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return member, nil
}

func (s *Service) ListMembersByOrg(ctx context.Context, orgUID string) ([]*models.OrganizationMember, error) {
	var members []*models.OrganizationMember

	err := s.db.NewSelect().
		Model(&members).
		Relation("User").
		Where("organization_member.organization_uid = ?", orgUID).
		Where("organization_member.deleted_at IS NULL").
		Order("organization_member.created_at DESC").
		Scan(ctx)

	return members, err
}

func (s *Service) ListMembersByUser(ctx context.Context, userUID string) ([]*models.OrganizationMember, error) {
	var members []*models.OrganizationMember

	err := s.db.NewSelect().
		Model(&members).
		Relation("Organization").
		Where("organization_member.user_uid = ?", userUID).
		Where("organization_member.deleted_at IS NULL").
		Order("organization_member.created_at DESC").
		Scan(ctx)

	return members, err
}

func (s *Service) UpdateOrganizationMember(
	ctx context.Context, uid string, update models.OrganizationMemberUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.OrganizationMember)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Role != nil {
		query = query.Set("role = ?", *update.Role)
	}

	if update.JoinedAt != nil {
		query = query.Set("joined_at = ?", *update.JoinedAt)
	}

	_, err := query.Exec(ctx)

	return err
}

func (s *Service) DeleteOrganizationMember(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.OrganizationMember)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// CountAdminsByOrg counts the members who hold at least the admin role. Owners
// are included deliberately: they outrank admins, so an org whose only
// privileged member is its owner must not read as "zero admins" to the
// last-admin guards.
func (s *Service) CountAdminsByOrg(ctx context.Context, orgUID string) (int, error) {
	count, err := s.db.NewSelect().
		Model((*models.OrganizationMember)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("role IN (?)", bun.List([]models.MemberRole{models.MemberRoleOwner, models.MemberRoleAdmin})).
		Where("deleted_at IS NULL").
		Where("joined_at IS NOT NULL").
		Count(ctx)

	return count, err
}

// CountOwnersByOrg counts the org's live owners. Used by the last-owner guard.
func (s *Service) CountOwnersByOrg(ctx context.Context, orgUID string) (int, error) {
	count, err := s.db.NewSelect().
		Model((*models.OrganizationMember)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("role = ?", models.MemberRoleOwner).
		Where("deleted_at IS NULL").
		Where("joined_at IS NOT NULL").
		Count(ctx)

	return count, err
}

// UserToken operations

func (s *Service) CreateUserToken(ctx context.Context, token *models.UserToken) error {
	_, err := s.db.NewInsert().Model(token).Exec(ctx)
	return err
}

func (s *Service) GetUserToken(ctx context.Context, uid string) (*models.UserToken, error) {
	token := new(models.UserToken)

	err := s.db.NewSelect().
		Model(token).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return token, nil
}

func (s *Service) GetUserTokenByToken(ctx context.Context, tokenValue string) (*models.UserToken, error) {
	token := new(models.UserToken)

	err := s.db.NewSelect().
		Model(token).
		Where("token = ?", tokenValue).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return token, nil
}

func (s *Service) ListUserTokens(ctx context.Context, userUID string) ([]*models.UserToken, error) {
	var tokens []*models.UserToken

	err := s.db.NewSelect().
		Model(&tokens).
		Where("user_uid = ?", userUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)

	return tokens, err
}

func (s *Service) ListUserTokensByType(
	ctx context.Context, userUID string, tokenType models.TokenType,
) ([]*models.UserToken, error) {
	var tokens []*models.UserToken

	err := s.db.NewSelect().
		Model(&tokens).
		Where("user_uid = ?", userUID).
		Where("type = ?", tokenType).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)

	return tokens, err
}

func (s *Service) UpdateUserToken(ctx context.Context, uid string, update models.UserTokenUpdate) error {
	query := s.db.NewUpdate().
		Model((*models.UserToken)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Properties != nil {
		query = query.Set("properties = ?", *update.Properties)
	}

	if update.ExpiresAt != nil {
		query = query.Set("expires_at = ?", *update.ExpiresAt)
	}

	if update.LastActiveAt != nil {
		query = query.Set("last_active_at = ?", *update.LastActiveAt)
	}

	_, err := query.Exec(ctx)

	return err
}

// DeleteUserToken soft-deletes a token, returning whether a live row existed
// to delete (compare-and-set on deleted_at IS NULL): false means the token
// was already deleted — for rotating OAuth refresh grants that is a replay
// racing a concurrent redemption.
func (s *Service) DeleteUserToken(ctx context.Context, uid string) (bool, error) {
	res, err := s.db.NewUpdate().
		Model((*models.UserToken)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)
	if err != nil {
		return false, err
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to read delete user token result: %w", err)
	}

	return affected > 0, nil
}

// DeleteUserTokensByOrg soft-deletes every token scoped to an organization
// (refresh tokens, PATs, OAuth grants) and returns how many rows it killed.
// Called when an organization is deleted so no surviving session can keep
// talking to it. Org-less tokens (organization_uid IS NULL) are untouched.
func (s *Service) DeleteUserTokensByOrg(ctx context.Context, orgUID string) (int, error) {
	res, err := s.db.NewUpdate().
		Model((*models.UserToken)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)
	if err != nil {
		return 0, err
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to read delete org tokens result: %w", err)
	}

	return int(affected), nil
}

// UserPasskey operations

// CreateUserPasskey inserts a new passkey row.
func (s *Service) CreateUserPasskey(ctx context.Context, passkey *models.UserPasskey) error {
	_, err := s.db.NewInsert().Model(passkey).Exec(ctx)

	return err
}

// GetUserPasskey returns a non-deleted passkey by uid.
func (s *Service) GetUserPasskey(ctx context.Context, uid string) (*models.UserPasskey, error) {
	row := new(models.UserPasskey)

	err := s.db.NewSelect().
		Model(row).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return row, nil
}

// GetUserPasskeyByCredentialID looks a passkey up by its WebAuthn
// credential ID. Used during the assertion-verification step.
func (s *Service) GetUserPasskeyByCredentialID(
	ctx context.Context, credentialID []byte,
) (*models.UserPasskey, error) {
	row := new(models.UserPasskey)

	err := s.db.NewSelect().
		Model(row).
		Where("credential_id = ?", credentialID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return row, nil
}

// ListUserPasskeysByUser returns all non-deleted passkeys for a user.
func (s *Service) ListUserPasskeysByUser(
	ctx context.Context, userUID string,
) ([]*models.UserPasskey, error) {
	var rows []*models.UserPasskey

	err := s.db.NewSelect().
		Model(&rows).
		Where("user_uid = ?", userUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)

	return rows, err
}

// UpdateUserPasskey applies a partial update.
func (s *Service) UpdateUserPasskey(
	ctx context.Context, uid string, update models.UserPasskeyUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.UserPasskey)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}

	if update.SignCount != nil {
		query = query.Set("sign_count = ?", *update.SignCount)
	}

	if update.LastUsedAt != nil {
		query = query.Set("last_used_at = ?", *update.LastUsedAt)
	}

	if update.BackupState != nil {
		query = query.Set("backup_state = ?", *update.BackupState)
	}

	_, err := query.Exec(ctx)

	return err
}

// DeleteUserPasskey soft-deletes a passkey.
func (s *Service) DeleteUserPasskey(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.UserPasskey)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// Worker operations

func (s *Service) CreateWorker(ctx context.Context, worker *models.Worker) error {
	_, err := s.db.NewInsert().Model(worker).Exec(ctx)
	return err
}

func (s *Service) GetWorker(ctx context.Context, uid string) (*models.Worker, error) {
	worker := new(models.Worker)

	err := s.db.NewSelect().
		Model(worker).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return worker, nil
}

func (s *Service) GetWorkerBySlug(ctx context.Context, slug string) (*models.Worker, error) {
	worker := new(models.Worker)

	err := s.db.NewSelect().
		Model(worker).
		Where("slug = ?", slug).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return worker, nil
}

func (s *Service) ListWorkers(ctx context.Context) ([]*models.Worker, error) {
	var workers []*models.Worker

	err := s.db.NewSelect().
		Model(&workers).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)

	return workers, err
}

func (s *Service) UpdateWorker(ctx context.Context, uid string, update models.WorkerUpdate) error {
	query := s.db.NewUpdate().
		Model((*models.Worker)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Slug != nil {
		query = query.Set("slug = ?", *update.Slug)
	}

	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}

	if update.Region != nil {
		query = query.Set("region = ?", *update.Region)
	}

	if update.LastActiveAt != nil {
		query = query.Set("last_active_at = ?", *update.LastActiveAt)
	}

	_, err := query.Exec(ctx)

	return err
}

func (s *Service) DeleteWorker(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.Worker)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

func (s *Service) RegisterOrUpdateWorker(ctx context.Context, worker *models.Worker) (*models.Worker, error) {
	// Try to find existing worker by slug
	var existing models.Worker
	err := s.db.NewSelect().
		Model(&existing).
		Where("slug = ?", worker.Slug).
		Where("deleted_at IS NULL").
		Scan(ctx)

	now := time.Now()

	if err != nil {
		// Worker doesn't exist, create it
		worker.CreatedAt = now
		worker.UpdatedAt = now
		worker.LastActiveAt = &now

		_, err = s.db.NewInsert().
			Model(worker).
			Exec(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to create worker: %w", err)
		}

		return worker, nil
	}

	// Worker exists, update last_active_at
	existing.LastActiveAt = &now
	existing.UpdatedAt = now

	_, err = s.db.NewUpdate().
		Model(&existing).
		Column("last_active_at", "updated_at").
		Where("uid = ?", existing.UID).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to update worker: %w", err)
	}

	return &existing, nil
}

func (s *Service) UpdateWorkerHeartbeat(ctx context.Context, workerUID string) error {
	now := time.Now()
	_, err := s.db.NewUpdate().
		Model((*models.Worker)(nil)).
		Set("last_active_at = ?", now).
		Set("updated_at = ?", now).
		Where("uid = ?", workerUID).
		Exec(ctx)

	return err
}

// Check operations

func (s *Service) CreateCheck(ctx context.Context, check *models.Check) error {
	// Insert check and create corresponding check_job(s) in a transaction
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Insert the check
		if _, err := tx.NewInsert().Model(check).Exec(ctx); err != nil {
			return err
		}

		// Create corresponding check_job(s)
		if check.Enabled {
			if err := createCheckJobs(ctx, tx, check); err != nil {
				return err
			}
		}

		// Create initial result to mark check creation
		initialStatus := int(models.ResultStatusCreated)
		initialResult := models.Result{
			UID:             uuid.Must(uuid.NewV7()).String(),
			OrganizationUID: check.OrganizationUID,
			CheckUID:        check.UID,
			PeriodType:      "raw",
			PeriodStart:     time.Now(),
			Status:          &initialStatus,
			Metrics:         make(models.JSONMap),
			Output:          models.JSONMap{"message": "Check created"},
			CreatedAt:       time.Now(),
		}
		if _, err := tx.NewInsert().Model(&initialResult).Exec(ctx); err != nil {
			return err
		}

		return nil
	})
}

// createCheckJobs creates check jobs for a check, one per region with period splitting.
func createCheckJobs(ctx context.Context, tx bun.Tx, check *models.Check) error {
	now := time.Now()
	basePeriod := time.Duration(check.Period)

	if len(check.Regions) == 0 {
		// No regions: create a single job without region
		checkJob := models.NewCheckJob(check.OrganizationUID, check.UID, check.Period)
		checkJob.Type = check.Type
		checkJob.Config = check.Config
		checkJob.ConfigPrivate = check.ConfigPrivate
		checkJob.ConfigPrivateKeys = check.ConfigPrivateKeys
		checkJob.ConfigSealed = check.ConfigSealed
		checkJob.Encrypted = check.ConfigPrivate != nil
		checkJob.ScheduledAt = &now
		if _, err := tx.NewInsert().Model(checkJob).Exec(ctx); err != nil {
			return err
		}

		return nil
	}

	// Multi-region: one job per region at the full check period (spec
	// 2026-07-20-05 — no more basePeriod×n split). Stagger the first tick by
	// the inter-region spread; region 0 stays at now so the check-created
	// express path fires a fast first result (see the postgres twin).
	n := len(check.Regions)
	spread := scheduling.RegionSpread(basePeriod, n, check.RegionSpreadDuration())

	for i, region := range check.Regions {
		scheduledAt := now.Add(spread * time.Duration(i))
		regionCopy := region

		checkJob := models.NewCheckJob(check.OrganizationUID, check.UID, check.Period)
		checkJob.Type = check.Type
		checkJob.Config = check.Config
		checkJob.ConfigPrivate = check.ConfigPrivate
		checkJob.ConfigPrivateKeys = check.ConfigPrivateKeys
		checkJob.ConfigSealed = check.ConfigSealed
		checkJob.Encrypted = check.ConfigPrivate != nil
		checkJob.Region = &regionCopy
		checkJob.ScheduledAt = &scheduledAt

		if _, err := tx.NewInsert().Model(checkJob).Exec(ctx); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) GetCheck(ctx context.Context, orgUID, checkUID string) (*models.Check, error) {
	check := new(models.Check)

	err := s.db.NewSelect().
		Model(check).
		Where("uid = ?", checkUID).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return check, nil
}

func (s *Service) GetCheckByUidOrSlug(ctx context.Context, orgUID, identifier string) (*models.Check, error) {
	check := new(models.Check)

	query := s.db.NewSelect().
		Model(check).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL")

	if _, err := uuid.Parse(identifier); err == nil {
		query = query.Where("uid = ?", identifier)
	} else {
		query = query.Where("slug = ?", identifier)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, err
	}

	return check, nil
}

// GetCheckByEmailToken looks up an email check by its random token. Tokens
// are 24 random bytes (48 hex chars), so org scoping is unnecessary —
// collisions are negligible globally.
func (s *Service) GetCheckByEmailToken(ctx context.Context, token string) (*models.Check, error) {
	check := new(models.Check)

	if err := s.db.NewSelect().
		Model(check).
		Where("type = ?", string(checkerdef.CheckTypeEmail)).
		Where("deleted_at IS NULL").
		Where("json_extract(config, '$.token') = ?", token).
		Scan(ctx); err != nil {
		return nil, err
	}

	return check, nil
}

// ListChecksByTunnelCheckUID returns the org's non-deleted checks whose config
// references the given SSH check as their tunnel. Mirrors the Postgres JSONB
// query with SQLite's json_extract.
func (s *Service) ListChecksByTunnelCheckUID(
	ctx context.Context, orgUID, tunnelCheckUID string,
) ([]*models.Check, error) {
	var checks []*models.Check

	if err := s.db.NewSelect().
		Model(&checks).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Where("json_extract(config, ?) = ?", "$."+checkerdef.TunnelCheckUIDConfigKey, tunnelCheckUID).
		Order("slug ASC").
		Scan(ctx); err != nil {
		return nil, err
	}

	return checks, nil
}

// applyCheckTypeFilter narrows a checks query to the requested types. It is
// extracted from ListChecks so the row query and the count query share one
// definition of the filter, and so ListChecks stays inside its complexity
// budget.
func applyCheckTypeFilter(query *bun.SelectQuery, types []string) *bun.SelectQuery {
	if len(types) == 0 {
		return query
	}

	return query.Where("type IN (?)", bun.List(types))
}

// groupSortKeyExpr is the effective group-ordering key for sort=group: the
// check's group sort_order, or a large sentinel for ungrouped checks so they
// sort strictly last (sort_order is int16, max 32767 < the sentinel). It is a
// correlated subquery referencing the outer checks.check_group_uid by bare
// column name — deliberately not qualified, since bun aliases the checks table
// as the reserved word "check". Kept byte-identical to the postgres twin so the
// composite cursor produced by one store is honored by the other.
const groupSortKeyExpr = `COALESCE((SELECT g.sort_order FROM check_groups AS g ` +
	`WHERE g.uid = check_group_uid), 2147483647)`

// targetHostSortKeyExpr is the effective ordering key for sort=targetHost: the
// check's config `host`, else `url`, else `target` (as raw text — NOT the
// hostname-parsed value the response's targetHost field carries, since
// portably parsing a hostname out of a URL in SQL is impractical across both
// backends), or a sentinel that sorts strictly last for checks with none of
// those fields. A same-host prefix (the common case: same scheme, same host,
// differing path) still sorts contiguously, which is all this ordering needs
// to do — clients bucket by the exact `targetHost` field from each response,
// not by this key. Kept byte-identical in spirit to the postgres twin
// (differs only in the json_extract vs JSONB accessor) for the same reason as
// groupSortKeyExpr.
const targetHostSortKeyExpr = `COALESCE(` +
	`NULLIF(json_extract(config, '$.host'), ''), ` +
	`NULLIF(json_extract(config, '$.url'), ''), ` +
	`NULLIF(json_extract(config, '$.target'), ''), ` +
	"'￿￿￿￿￿￿￿￿￿￿')"

// applyChecksCursor adds the keyset cursor predicate to the ListChecks row
// query. sort=group uses a composite (group key, created_at, uid) tuple;
// sort=targetHost uses a composite (target host key, name, uid) tuple; the
// default ordering keeps the two-part (created_at, uid) cursor.
func applyChecksCursor(query *bun.SelectQuery, filter *models.ListChecksFilter) *bun.SelectQuery {
	if filter.SortByGroup {
		if filter.CursorGroupSortKey == nil || filter.CursorCreatedAt == nil || filter.CursorUID == nil {
			return query
		}

		return query.Where(
			"("+groupSortKeyExpr+" > ?"+
				" OR ("+groupSortKeyExpr+" = ? AND created_at < ?)"+
				" OR ("+groupSortKeyExpr+" = ? AND created_at = ? AND uid < ?))",
			*filter.CursorGroupSortKey,
			*filter.CursorGroupSortKey, *filter.CursorCreatedAt,
			*filter.CursorGroupSortKey, *filter.CursorCreatedAt, *filter.CursorUID,
		)
	}

	if filter.SortByTargetHost {
		if filter.CursorTargetHostKey == nil || filter.CursorTargetHostName == nil || filter.CursorUID == nil {
			return query
		}

		return query.Where(
			"("+targetHostSortKeyExpr+" > ?"+
				" OR ("+targetHostSortKeyExpr+" = ? AND COALESCE(name, '') > ?)"+
				" OR ("+targetHostSortKeyExpr+" = ? AND COALESCE(name, '') = ? AND uid > ?))",
			*filter.CursorTargetHostKey,
			*filter.CursorTargetHostKey, *filter.CursorTargetHostName,
			*filter.CursorTargetHostKey, *filter.CursorTargetHostName, *filter.CursorUID,
		)
	}

	if filter.CursorCreatedAt != nil && filter.CursorUID != nil {
		return query.Where(
			"(created_at < ? OR (created_at = ? AND uid < ?))",
			*filter.CursorCreatedAt, *filter.CursorCreatedAt, *filter.CursorUID,
		)
	}

	return query
}

// applyChecksOrdering applies the row ordering. sort=group loads in display
// order (group sort_order asc, ungrouped last via the COALESCE sentinel, then
// created_at DESC / uid DESC within a bucket) and selects the effective key so
// the service can encode it into the composite cursor. sort=targetHost loads
// targetHost ascending (checks with none of host/url/target last via the
// COALESCE sentinel), then name ascending, then uid ascending as the final
// tiebreaker. Default is unchanged.
func applyChecksOrdering(query *bun.SelectQuery, filter *models.ListChecksFilter) *bun.SelectQuery {
	if filter != nil && filter.SortByGroup {
		return query.
			ColumnExpr("*").
			ColumnExpr(groupSortKeyExpr+" AS group_sort_key").
			OrderExpr(groupSortKeyExpr+" ASC").
			Order("created_at DESC", "uid DESC")
	}

	if filter != nil && filter.SortByTargetHost {
		return query.
			ColumnExpr("*").
			ColumnExpr(targetHostSortKeyExpr + " AS target_host_sort_key").
			OrderExpr(targetHostSortKeyExpr + " ASC").
			OrderExpr("COALESCE(name, '') ASC").
			Order("uid ASC")
	}

	return query.Order("created_at DESC", "uid DESC")
}

//nolint:funlen // List query builder handles many optional filters inline
func (s *Service) ListChecks(
	ctx context.Context, orgUID string, filter *models.ListChecksFilter,
) ([]*models.Check, int64, error) {
	var checks []*models.Check

	query := s.db.NewSelect().
		Model(&checks).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL")

	countQuery := s.db.NewSelect().
		Model((*models.Check)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL")

	if filter != nil { //nolint:nestif // Filter block handles multiple optional conditions
		// Apply label filtering
		for key, value := range filter.Labels {
			labelWhere := `uid IN (
				SELECT cl.check_uid FROM check_labels cl
				JOIN labels l ON l.uid = cl.label_uid
				WHERE l.organization_uid = ? AND l.key = ? AND l.value = ? AND l.deleted_at IS NULL
			)`
			query = query.Where(labelWhere, orgUID, key, value)
			countQuery = countQuery.Where(labelWhere, orgUID, key, value)
		}

		// Apply check group filter
		if filter.CheckGroupUID != nil {
			if *filter.CheckGroupUID == "none" {
				query = query.Where("check_group_uid IS NULL")
				countQuery = countQuery.Where("check_group_uid IS NULL")
			} else {
				query = query.Where("check_group_uid = ?", *filter.CheckGroupUID)
				countQuery = countQuery.Where("check_group_uid = ?", *filter.CheckGroupUID)
			}
		}

		// Apply search filter
		if filter.Query != "" {
			pattern := "%" + strings.ToLower(filter.Query) + "%"
			query = query.Where("(LOWER(name) LIKE ? OR LOWER(slug) LIKE ?)", pattern, pattern)
			countQuery = countQuery.Where("(LOWER(name) LIKE ? OR LOWER(slug) LIKE ?)", pattern, pattern)
		}

		query = applyCheckTypeFilter(query, filter.Types)
		countQuery = applyCheckTypeFilter(countQuery, filter.Types)

		// Apply internal filter (default: show only non-internal checks)
		internalVal := "false"
		if filter.Internal != nil {
			internalVal = *filter.Internal
		}

		switch internalVal {
		case "all":
			// No filter — show all checks
		case "true":
			query = query.Where("internal = TRUE")
			countQuery = countQuery.Where("internal = TRUE")
		default:
			// Default: hide internal checks
			query = query.Where("internal = FALSE")
			countQuery = countQuery.Where("internal = FALSE")
		}

		// Apply current-status filter
		if len(filter.Statuses) > 0 {
			query = query.Where("status IN (?)", bun.List(filter.Statuses))
			countQuery = countQuery.Where("status IN (?)", bun.List(filter.Statuses))
		}

		// Apply cursor (keyset) — composite for sort=group, two-part otherwise.
		query = applyChecksCursor(query, filter)

		// Apply limit
		if filter.Limit > 0 {
			query = query.Limit(filter.Limit + 1)
		}
	}

	query = applyChecksOrdering(query, filter)

	total, err := countQuery.Count(ctx)
	if err != nil {
		return nil, 0, err
	}

	err = query.Scan(ctx)

	return checks, int64(total), err
}

//nolint:cyclop // long but flat: one branch per optional column
func (s *Service) UpdateCheck( //nolint:funlen // PATCH builder spans many optional fields
	ctx context.Context, uid string, update *models.CheckUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.Check)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.CheckGroupUID != nil {
		if *update.CheckGroupUID == "" {
			query = query.Set("check_group_uid = NULL")
		} else {
			query = query.Set("check_group_uid = ?", *update.CheckGroupUID)
		}
	}

	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}

	if update.Slug != nil {
		query = query.Set("slug = ?", *update.Slug)
	}

	if update.Description != nil {
		query = query.Set("description = ?", *update.Description)
	}

	if update.Type != nil {
		query = query.Set("type = ?", *update.Type)
	}

	if update.Config != nil {
		query = query.Set("config = ?", *update.Config)
	}

	if update.ConfigPrivate != nil {
		query = query.Set("config_private = ?", *update.ConfigPrivate)
	} else if update.ClearConfigPrivate {
		query = query.Set("config_private = NULL")
	}

	if update.ConfigPrivateKeys != nil {
		query = query.Set("config_private_keys = ?", *update.ConfigPrivateKeys)
	} else if update.ClearConfigPrivate {
		query = query.Set("config_private_keys = NULL")
	}

	if update.ConfigSealed != nil {
		query = query.Set("config_sealed = ?", *update.ConfigSealed)
	} else if update.ClearConfigSealed {
		query = query.Set("config_sealed = NULL")
	}

	if update.Enabled != nil {
		query = query.Set("enabled = ?", *update.Enabled)
	}

	if update.Internal != nil {
		query = query.Set("internal = ?", *update.Internal)
	}

	if update.Period != nil {
		query = query.Set("period = ?", *update.Period)
	}

	if update.Regions != nil {
		regionsJSON, jsonErr := json.Marshal(*update.Regions)
		if jsonErr != nil {
			return fmt.Errorf("failed to marshal regions: %w", jsonErr)
		}
		query = query.Set("regions = ?", string(regionsJSON))
	}

	switch {
	case update.ClearRegionSpread:
		query = query.Set("region_spread = NULL")
	case update.RegionSpread != nil:
		query = query.Set("region_spread = ?", *update.RegionSpread)
	}

	if update.ReopenCooldownMultiplier != nil {
		query = query.Set("reopen_cooldown_multiplier = ?", *update.ReopenCooldownMultiplier)
	}

	if update.FlappingWindowSeconds != nil {
		query = query.Set("flapping_window_seconds = ?", *update.FlappingWindowSeconds)
	}

	if update.FlapBackoffFactor != nil {
		query = query.Set("flap_backoff_factor = ?", *update.FlapBackoffFactor)
	}

	if update.MaxRecoveryMultiplier != nil {
		query = query.Set("max_recovery_multiplier = ?", *update.MaxRecoveryMultiplier)
	}

	if update.ConfirmationPeriodSeconds != nil {
		query = query.Set("confirmation_period_seconds = ?", *update.ConfirmationPeriodSeconds)
	}
	if update.RecoveryPeriodSeconds != nil {
		query = query.Set("recovery_period_seconds = ?", *update.RecoveryPeriodSeconds)
	}

	switch {
	case update.ClearEscalationPolicyUID:
		query = query.Set("escalation_policy_uid = NULL")
	case update.EscalationPolicyUID != nil:
		query = query.Set("escalation_policy_uid = ?", *update.EscalationPolicyUID)
	}

	_, err := query.Exec(ctx)

	return err
}

func (s *Service) DeleteCheck(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.Check)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// CheckJob operations

func (s *Service) ListCheckJobsByCheckUID(ctx context.Context, checkUID string) ([]*models.CheckJob, error) {
	var jobs []*models.CheckJob

	err := s.db.NewSelect().
		Model(&jobs).
		Where("check_uid = ?", checkUID).
		Scan(ctx)

	return jobs, err
}

func (s *Service) DeleteCheckJob(ctx context.Context, uid string) error {
	_, err := s.db.NewDelete().
		Model((*models.CheckJob)(nil)).
		Where("uid = ?", uid).
		Exec(ctx)

	return err
}

func (s *Service) CreateCheckJob(ctx context.Context, job *models.CheckJob) error {
	_, err := s.db.NewInsert().Model(job).Exec(ctx)

	return err
}

// Label operations

func (s *Service) GetOrCreateLabel(ctx context.Context, orgUID, key, value string) (*models.Label, error) {
	// Try to get existing label
	label := new(models.Label)
	err := s.db.NewSelect().
		Model(label).
		Where("organization_uid = ?", orgUID).
		Where("key = ?", key).
		Where("value = ?", value).
		Where("deleted_at IS NULL").
		Scan(ctx)

	if err == nil {
		return label, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("failed to query label: %w", err)
	}

	// Create new label
	label = models.NewLabel(orgUID, key, value)
	_, err = s.db.NewInsert().Model(label).Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create label: %w", err)
	}

	return label, nil
}

func (s *Service) SetCheckLabels(ctx context.Context, checkUID string, labelUIDs []string) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Delete existing labels
		_, err := tx.NewDelete().
			Model((*models.CheckLabel)(nil)).
			Where("check_uid = ?", checkUID).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to delete existing check labels: %w", err)
		}

		// Insert new labels
		if len(labelUIDs) > 0 {
			checkLabels := make([]*models.CheckLabel, len(labelUIDs))
			for i, labelUID := range labelUIDs {
				checkLabels[i] = models.NewCheckLabel(checkUID, labelUID)
			}
			_, err = tx.NewInsert().Model(&checkLabels).Exec(ctx)
			if err != nil {
				return fmt.Errorf("failed to insert check labels: %w", err)
			}
		}
		return nil
	})
}

func (s *Service) GetLabelsForCheck(ctx context.Context, checkUID string) ([]*models.Label, error) {
	var labels []*models.Label
	err := s.db.NewSelect().
		Model(&labels).
		Join("JOIN check_labels cl ON cl.label_uid = label.uid").
		Where("cl.check_uid = ?", checkUID).
		Where("label.deleted_at IS NULL").
		Order("label.key", "label.value").
		Scan(ctx)

	return labels, err
}

func (s *Service) GetLabelsForChecks(ctx context.Context, checkUIDs []string) (map[string][]*models.Label, error) {
	if len(checkUIDs) == 0 {
		return make(map[string][]*models.Label), nil
	}

	type labelWithCheck struct {
		models.Label
		CheckUID string `bun:"check_uid"`
	}

	var results []labelWithCheck
	err := s.db.NewSelect().
		ColumnExpr("label.*").
		ColumnExpr("cl.check_uid").
		TableExpr("labels AS label").
		Join("JOIN check_labels cl ON cl.label_uid = label.uid").
		Where("cl.check_uid IN (?)", bun.List(checkUIDs)).
		Where("label.deleted_at IS NULL").
		Order("label.key", "label.value").
		Scan(ctx, &results)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels for checks: %w", err)
	}

	labelMap := make(map[string][]*models.Label)
	for i := range results {
		result := &results[i]
		label := &models.Label{
			UID:             result.UID,
			OrganizationUID: result.OrganizationUID,
			Key:             result.Key,
			Value:           result.Value,
			CreatedAt:       result.CreatedAt,
			DeletedAt:       result.DeletedAt,
		}
		labelMap[result.CheckUID] = append(labelMap[result.CheckUID], label)
	}

	return labelMap, nil
}

// ListDistinctLabelKeys returns distinct label keys used by checks in the org,
// sorted by usage count DESC then key ASC. SQLite uses LOWER(...) + LIKE since
// it has no ILIKE.
func (s *Service) ListDistinctLabelKeys(
	ctx context.Context, orgUID, query string, limit int,
) ([]models.LabelSuggestion, error) {
	type row struct {
		Value string `bun:"value"`
		Count int    `bun:"count"`
	}

	stmt := s.db.NewSelect().
		TableExpr("labels AS label").
		ColumnExpr("label.key AS value").
		ColumnExpr("COUNT(DISTINCT cl.check_uid) AS count").
		Join("JOIN check_labels cl ON cl.label_uid = label.uid").
		Where("label.organization_uid = ?", orgUID).
		Where("label.deleted_at IS NULL").
		GroupExpr("label.key").
		OrderExpr("count DESC, label.key ASC").
		Limit(limit)

	if query != "" {
		stmt = stmt.Where("LOWER(label.key) LIKE LOWER(?)", query+"%")
	}

	var rows []row
	if err := stmt.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list distinct label keys: %w", err)
	}

	out := make([]models.LabelSuggestion, len(rows))
	for i, r := range rows {
		out[i] = models.LabelSuggestion{Value: r.Value, Count: r.Count}
	}

	return out, nil
}

// ListDistinctLabelValues returns distinct values for a given label key in
// the org, sorted by usage count DESC then value ASC. SQLite variant.
func (s *Service) ListDistinctLabelValues(
	ctx context.Context, orgUID, key, query string, limit int,
) ([]models.LabelSuggestion, error) {
	type row struct {
		Value string `bun:"value"`
		Count int    `bun:"count"`
	}

	stmt := s.db.NewSelect().
		TableExpr("labels AS label").
		ColumnExpr("label.value AS value").
		ColumnExpr("COUNT(DISTINCT cl.check_uid) AS count").
		Join("JOIN check_labels cl ON cl.label_uid = label.uid").
		Where("label.organization_uid = ?", orgUID).
		Where("label.key = ?", key).
		Where("label.deleted_at IS NULL").
		GroupExpr("label.value").
		OrderExpr("count DESC, label.value ASC").
		Limit(limit)

	if query != "" {
		stmt = stmt.Where("LOWER(label.value) LIKE LOWER(?)", query+"%")
	}

	var rows []row
	if err := stmt.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to list distinct label values: %w", err)
	}

	out := make([]models.LabelSuggestion, len(rows))
	for i, r := range rows {
		out[i] = models.LabelSuggestion{Value: r.Value, Count: r.Count}
	}

	return out, nil
}

// Result operations

func (s *Service) CreateResult(ctx context.Context, result *models.Result) error {
	_, err := s.db.NewInsert().Model(result).Exec(ctx)
	return err
}

// UpsertAggregatedResult replaces any existing aggregated row for the same
// bucket key (organization_uid, check_uid, coalesce(region,”), period_type,
// period_start) with the given result, in one transaction. This keeps
// re-aggregation idempotent (exactly one row per bucket) even when region is
// NULL — where the unique index treats NULLs as distinct. See spec
// 2026-07-11-16.
func (s *Service) UpsertAggregatedResult(ctx context.Context, result *models.Result) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return upsertAggregatedResultTx(ctx, tx, result)
	})
}

// upsertAggregatedResultTx replaces any existing aggregated row for the same
// bucket key (organization_uid, check_uid, coalesce(region,”), period_type,
// period_start) with result, using the given transaction. Shared by
// UpsertAggregatedResult and CompactResults so both stay idempotent on the
// bucket key even when region is NULL (spec 2026-07-11-16).
func upsertAggregatedResultTx(ctx context.Context, tx bun.Tx, result *models.Result) error {
	if _, err := tx.NewDelete().
		Model((*models.Result)(nil)).
		Where("organization_uid = ?", result.OrganizationUID).
		Where("check_uid = ?", result.CheckUID).
		Where("period_type = ?", result.PeriodType).
		Where("period_start = ?", result.PeriodStart).
		Where("COALESCE(region, '') = COALESCE(?, '')", result.Region).
		Exec(ctx); err != nil {
		return err
	}

	_, err := tx.NewInsert().Model(result).Exec(ctx)

	return err
}

func (s *Service) SaveResultWithStatusTracking(ctx context.Context, result *models.Result) error {
	_, err := s.db.NewInsert().Model(result).Exec(ctx)

	return err
}

func (s *Service) GetResult(ctx context.Context, uid string) (*models.Result, error) {
	result := new(models.Result)

	err := s.db.NewSelect().
		Model(result).
		Where("uid = ?", uid).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (s *Service) ListResults(
	ctx context.Context, filter *models.ListResultsFilter,
) (*models.ListResultsResponse, error) {
	var results []*models.Result

	query := s.db.NewSelect().Model(&results)

	// Count-only readers opt out of the two JSON blob columns entirely (spec
	// 2026-07-24-02 §5): they are by far the widest part of a results row and
	// nothing in those code paths reads them.
	if filter.SkipBlobs {
		query = query.ExcludeColumn("metrics", "output")
	}

	query = applyResultsFilter(query, filter)

	if err := query.Scan(ctx); err != nil {
		return nil, err
	}

	// For now, we don't calculate total count as it's expensive
	// It can be added later as an optional feature
	return &models.ListResultsResponse{
		Results: results,
		Total:   0,
	}, nil
}

// applyResultsFilter narrows a results SELECT by filter. Shared by ListResults
// and CompactResults so both read exactly the same rows for a bucket.
func applyResultsFilter(query *bun.SelectQuery, filter *models.ListResultsFilter) *bun.SelectQuery {
	query = query.
		Where("organization_uid = ?", filter.OrganizationUID).
		Order("period_start DESC").
		Order("uid DESC")

	// Filter by multiple check UIDs
	if len(filter.CheckUIDs) > 0 {
		query = query.Where("check_uid IN (?)", bun.List(filter.CheckUIDs))
	}

	// Filter by check types (requires subquery to checks table)
	if len(filter.CheckTypes) > 0 {
		//nolint:lll // Long SQL query for readability
		query = query.Where("check_uid IN (SELECT uid FROM checks WHERE organization_uid = ? AND type IN (?) AND deleted_at IS NULL)",
			filter.OrganizationUID, bun.List(filter.CheckTypes))
	}

	// Filter by multiple regions
	if len(filter.Regions) > 0 {
		query = query.Where("region IN (?)", bun.List(filter.Regions))
	}
	// Filter by multiple period types
	if len(filter.PeriodTypes) > 0 {
		query = query.Where("period_type IN (?)", bun.List(filter.PeriodTypes))
	}

	// Filter by multiple statuses
	if len(filter.Statuses) > 0 {
		query = query.Where("status IN (?)", bun.List(filter.Statuses))
	}

	// Exclude statuses (e.g. lifecycle markers in aggregation discovery). A NULL
	// status is never a marker, so it stays included.
	if len(filter.ExcludeStatuses) > 0 {
		query = query.Where("(status IS NULL OR status NOT IN (?))", bun.List(filter.ExcludeStatuses))
	}

	// Skip rows whose check has been hard-deleted (FK-orphan rows). Soft-deleted
	// checks still have a row and stay included. See RequireCheckExists.
	if filter.RequireCheckExists {
		query = query.Where("check_uid IN (SELECT uid FROM checks)")
	}

	// Time range filters
	if filter.PeriodStartAfter != nil {
		query = query.Where("period_start >= ?", *filter.PeriodStartAfter)
	}

	if filter.PeriodEndBefore != nil {
		query = query.Where("period_start < ?", *filter.PeriodEndBefore)
	}

	// Cursor-based pagination
	if filter.CursorTimestamp != nil && filter.CursorUID != nil {
		// Results with period_start < cursor_timestamp OR (period_start = cursor_timestamp AND uid < cursor_uid)
		query = query.Where("(period_start < ?) OR (period_start = ? AND uid < ?)",
			*filter.CursorTimestamp, *filter.CursorTimestamp, *filter.CursorUID)
	}

	// Apply limit
	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}

	return query
}

// GetResultNeighbors returns the next-older and next-newer UIDs relative to
// the pivot, scoped to organization+check+periodType (and optionally
// regions). See db.Service for the full contract.
func (s *Service) GetResultNeighbors(
	ctx context.Context, orgUID, checkUID, periodType string, regions []string,
	pivotStart time.Time, pivotUID string,
) (string, string, error) {
	prevUID, prevErr := s.resultNeighborUID(
		ctx, orgUID, checkUID, periodType, regions,
		"(period_start < ?) OR (period_start = ? AND uid < ?)", pivotStart, pivotUID,
		"period_start DESC", "uid DESC")
	if prevErr != nil {
		return "", "", prevErr
	}

	nextUID, nextErr := s.resultNeighborUID(
		ctx, orgUID, checkUID, periodType, regions,
		"(period_start > ?) OR (period_start = ? AND uid > ?)", pivotStart, pivotUID,
		"period_start ASC", "uid ASC")
	if nextErr != nil {
		return "", "", nextErr
	}

	return prevUID, nextUID, nil
}

// resultNeighborUID runs one directional (older or newer) neighbor lookup —
// the shared half of GetResultNeighbors — and maps sql.ErrNoRows to an empty
// UID (boundary reached) rather than propagating it.
func (s *Service) resultNeighborUID(
	ctx context.Context, orgUID, checkUID, periodType string, regions []string,
	sideCondition string, pivotStart time.Time, pivotUID string,
	orderByStart, orderByUID string,
) (string, error) {
	query := s.db.NewSelect().
		Model((*models.Result)(nil)).
		Column("uid").
		Where("organization_uid = ?", orgUID).
		Where("check_uid = ?", checkUID).
		Where("period_type = ?", periodType).
		Where(sideCondition, pivotStart, pivotStart, pivotUID).
		Order(orderByStart).
		Order(orderByUID).
		Limit(1)

	if len(regions) > 0 {
		query = query.Where("region IN (?)", bun.List(regions))
	}

	var neighbor models.Result

	err := query.Scan(ctx, &neighbor)

	switch {
	case err == nil:
		return neighbor.UID, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	default:
		return "", err
	}
}

func (s *Service) DeleteResults(ctx context.Context, orgUID string, resultUIDs []string) (int64, error) {
	if len(resultUIDs) == 0 {
		return 0, nil
	}

	result, err := s.db.NewDelete().
		Model((*models.Result)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("uid IN (?)", bun.List(resultUIDs)).
		Exec(ctx)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected()
}

// CompactResults implements db.Service — see that interface for the full
// contract. Fetch → aggregate (Go) → upsert rollup → delete sources all run in
// one transaction, so any failure rolls the whole thing back and the bucket
// stays fully raw.
func (s *Service) CompactResults(
	ctx context.Context, filter *models.ListResultsFilter, aggregate models.AggregateResultsFunc,
) (models.CompactResultsOutcome, error) {
	var outcome models.CompactResultsOutcome

	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// 1. Read the source rows for this bucket inside the transaction, using
		// exactly the same filter as ListResults.
		var sources []*models.Result
		if scanErr := applyResultsFilter(tx.NewSelect().Model(&sources), filter).Scan(ctx); scanErr != nil {
			return scanErr
		}

		outcome = models.CompactResultsOutcome{Fetched: len(sources)}
		if len(sources) == 0 {
			return nil // Nothing to compact.
		}

		// 2. Compute the rollup and the source UIDs to delete (pure Go).
		rollup, sourceUIDs, aggErr := aggregate(sources)
		if aggErr != nil {
			return aggErr
		}

		outcome.SourceCount = len(sourceUIDs)

		// A marker-only bucket (no measurable rows) has nothing to roll up: leave
		// the sources in place and commit without writing.
		if rollup == nil || len(sourceUIDs) == 0 {
			return nil
		}

		// 3. Upsert the rollup idempotently on the bucket key.
		if upErr := upsertAggregatedResultTx(ctx, tx, rollup); upErr != nil {
			return upErr
		}

		// Test-only: prove the transaction rolls the upsert back on a later error.
		if s.compactFailpoint != nil {
			if fpErr := s.compactFailpoint(ctx); fpErr != nil {
				return fpErr
			}
		}

		// 4. Delete exactly the measurable source rows read in this transaction.
		res, delErr := tx.NewDelete().
			Model((*models.Result)(nil)).
			Where("organization_uid = ?", filter.OrganizationUID).
			Where("uid IN (?)", bun.List(sourceUIDs)).
			Exec(ctx)
		if delErr != nil {
			return delErr
		}

		deleted, raErr := res.RowsAffected()
		if raErr != nil {
			return raErr
		}

		outcome.DeletedCount = deleted
		outcome.Compacted = true

		return nil
	})
	if err != nil {
		return models.CompactResultsOutcome{}, err
	}

	return outcome, nil
}

// lastResultForChecksQuery is the SQL behind GetLastResultForChecks, kept as a
// package constant so the index-usage test can EXPLAIN QUERY PLAN exactly what
// production runs.
const lastResultForChecksQuery = `
	WITH winners AS (
		SELECT (
			SELECT r2.uid
			FROM results r2
			WHERE r2.organization_uid = ?
				AND r2.check_uid = c.uid
				AND r2.period_type = 'raw'
			ORDER BY r2.period_start DESC
			LIMIT 1
		) AS uid
		FROM checks c
		WHERE c.uid IN (?)
	)
	SELECT r.*
	FROM results r
	JOIN winners w ON w.uid = r.uid
`

func (s *Service) GetLastResultForChecks(
	ctx context.Context, orgUID string, checkUIDs []string,
) (map[string]*models.Result, error) {
	if len(checkUIDs) == 0 {
		return make(map[string]*models.Result), nil
	}

	var results []*models.Result

	// SQLite has neither DISTINCT ON nor LATERAL, so the Postgres per-check
	// index descent (spec 2026-08-09-07) is mirrored with a correlated scalar
	// subquery: for each requested check, pick the uid of its newest raw row,
	// then join those winners back to the base table so the final SELECT can
	// use r.* without leaking helper columns into the models.Result scan
	// target. The correlated subquery's (organization_uid, check_uid)
	// equality + ORDER BY period_start DESC LIMIT 1 matches results_raw_idx
	// (organization_uid, check_uid, period_start desc) WHERE period_type =
	// 'raw', so SQLite satisfies both the filter and the ordering from the
	// index instead of ranking every raw row of the organization. Same rows
	// as before (sync-pg-to-sqlite convention): the newest raw row per check,
	// at most one each.
	//
	// The driver side reads the requested uids from `checks` (soft-deleted
	// rows are still present, so a deleted check's last result stays
	// reachable exactly as it was) because SQLite has no unnest().
	err := s.db.NewRaw(lastResultForChecksQuery, orgUID, bun.List(checkUIDs)).Scan(ctx, &results)
	if err != nil {
		return nil, err
	}

	// Convert to map for easy lookup — one winning uid per check already
	// guarantees at most one row per check_uid.
	resultMap := make(map[string]*models.Result)
	for _, result := range results {
		resultMap[result.CheckUID] = result
	}

	return resultMap, nil
}

// Job operations

func (s *Service) CreateJob(ctx context.Context, job *models.Job) error {
	_, err := s.db.NewInsert().Model(job).Exec(ctx)
	return err
}

func (s *Service) GetJob(ctx context.Context, uid string) (*models.Job, error) {
	job := new(models.Job)

	err := s.db.NewSelect().
		Model(job).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return job, nil
}

func (s *Service) ListJobs(ctx context.Context, orgUID *string, limit int) ([]*models.Job, error) {
	var jobs []*models.Job

	query := s.db.NewSelect().
		Model(&jobs).
		Where("deleted_at IS NULL").
		Order("created_at DESC")

	if orgUID != nil {
		query = query.Where("organization_uid = ?", *orgUID)
	}

	if limit > 0 {
		query = query.Limit(limit)
	}

	err := query.Scan(ctx)

	return jobs, err
}

func (s *Service) UpdateJob(ctx context.Context, uid string, update models.JobUpdate) error {
	query := s.db.NewUpdate().
		Model((*models.Job)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Config != nil {
		query = query.Set("config = ?", *update.Config)
	}

	if update.RetryCount != nil {
		query = query.Set("retry_count = ?", *update.RetryCount)
	}

	if update.ScheduledAt != nil {
		query = query.Set("scheduled_at = ?", *update.ScheduledAt)
	}

	if update.Status != nil {
		query = query.Set("status = ?", *update.Status)
	}

	if update.Output != nil {
		query = query.Set("output = ?", *update.Output)
	}

	if update.PreviousJobUID != nil {
		query = query.Set("previous_job_uid = ?", *update.PreviousJobUID)
	}

	_, err := query.Exec(ctx)

	return err
}

func (s *Service) DeleteJob(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.Job)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// SoftDeleteFinishedJobs marks up to `limit` terminal jobs done before `before`
// as soft-deleted (jobs_cleanup stage 1). Mirrors the Postgres implementation
// (sync-pg-to-sqlite). Select-then-update keeps the batch bounded.
func (s *Service) SoftDeleteFinishedJobs(ctx context.Context, before time.Time, limit int) (int64, error) {
	var uids []string

	err := s.db.NewSelect().
		Model((*models.Job)(nil)).
		Column("uid").
		Where("status IN (?)", bun.List(models.FinishedJobStatuses())).
		Where("deleted_at IS NULL").
		Where("updated_at < ?", before).
		Limit(limit).
		Scan(ctx, &uids)
	if err != nil {
		return 0, fmt.Errorf("failed to select finished jobs to soft-delete: %w", err)
	}

	if len(uids) == 0 {
		return 0, nil
	}

	res, err := s.db.NewUpdate().
		Model((*models.Job)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("uid IN (?)", bun.List(uids)).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to soft-delete finished jobs: %w", err)
	}

	count, _ := res.RowsAffected()

	return count, nil
}

// DeleteSoftDeletedJobs physically deletes up to `limit` jobs soft-deleted
// before `before`, skipping any still referenced by another job's
// previous_job_uid so the retry-chain FK never trips (jobs_cleanup stage 2).
// Mirrors the Postgres implementation (sync-pg-to-sqlite).
func (s *Service) DeleteSoftDeletedJobs(ctx context.Context, before time.Time, limit int) (int64, error) {
	var uids []string

	err := s.db.NewSelect().
		Model((*models.Job)(nil)).
		Column("uid").
		Where("deleted_at IS NOT NULL").
		Where("deleted_at < ?", before).
		Where("uid NOT IN (SELECT previous_job_uid FROM jobs WHERE previous_job_uid IS NOT NULL)").
		Limit(limit).
		Scan(ctx, &uids)
	if err != nil {
		return 0, fmt.Errorf("failed to select soft-deleted jobs to delete: %w", err)
	}

	if len(uids) == 0 {
		return 0, nil
	}

	res, err := s.db.NewDelete().
		Model((*models.Job)(nil)).
		Where("uid IN (?)", bun.List(uids)).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to delete soft-deleted jobs: %w", err)
	}

	count, _ := res.RowsAffected()

	return count, nil
}

// Incident operations

// applyIncidentSetFields walks the non-clear pointer fields and writes the
// corresponding Set() calls. Split out of UpdateIncident so the caller stays
// under the cyclop limit.
func applyIncidentSetFields(query *bun.UpdateQuery, update *models.IncidentUpdate) *bun.UpdateQuery {
	type setter struct {
		col string
		val any
	}

	setters := []setter{
		{"region", update.Region},
		{"state", update.State},
		{"resolved_at", update.ResolvedAt},
		{"resolved_by", update.ResolvedBy},
		{"resolution_type", update.ResolutionType},
		{"escalated_at", update.EscalatedAt},
		{"acknowledged_at", update.AcknowledgedAt},
		{"acknowledged_by", update.AcknowledgedBy},
		{"snoozed_until", update.SnoozedUntil},
		{"snoozed_by", update.SnoozedBy},
		{"snooze_reason", update.SnoozeReason},
		{"failure_count", update.FailureCount},
		{"relapse_count", update.RelapseCount},
		{"last_reopened_at", update.LastReopenedAt},
		{"title", update.Title},
		{"description", update.Description},
		{"details", update.Details},
		{"caused_by_incident_uid", update.CausedByIncidentUID},
		{"paging_suppressed", update.PagingSuppressed},
	}

	for i := range setters {
		query = applyIncidentSet(query, setters[i].col, setters[i].val)
	}

	return query
}

// applyIncidentSet writes a single Set() if the pointer is non-nil. The
// switch covers every concrete pointer type used in IncidentUpdate.
func applyIncidentSet(query *bun.UpdateQuery, column string, value any) *bun.UpdateQuery {
	switch typed := value.(type) {
	case *string:
		if typed != nil {
			return query.Set(column+" = ?", *typed)
		}
	case *time.Time:
		if typed != nil {
			return query.Set(column+" = ?", *typed)
		}
	case *int:
		if typed != nil {
			return query.Set(column+" = ?", *typed)
		}
	case *models.IncidentState:
		if typed != nil {
			return query.Set(column+" = ?", *typed)
		}
	case *models.JSONMap:
		if typed != nil {
			return query.Set(column+" = ?", *typed)
		}
	case *bool:
		if typed != nil {
			return query.Set(column+" = ?", *typed)
		}
	}

	return query
}

// applyClearFields applies the Clear* boolean fields from IncidentUpdate to set columns to NULL.
func applyClearFields(query *bun.UpdateQuery, update *models.IncidentUpdate) *bun.UpdateQuery {
	if update.ClearResolvedAt {
		query = query.Set("resolved_at = NULL")
	}
	if update.ClearResolvedBy {
		query = query.Set("resolved_by = NULL")
	}
	if update.ClearResolutionType {
		query = query.Set("resolution_type = NULL")
	}
	if update.ClearAcknowledgedAt {
		query = query.Set("acknowledged_at = NULL")
	}
	if update.ClearAcknowledgedBy {
		query = query.Set("acknowledged_by = NULL")
	}
	if update.ClearSnoozedUntil {
		query = query.Set("snoozed_until = NULL")
	}
	if update.ClearSnoozedBy {
		query = query.Set("snoozed_by = NULL")
	}
	if update.ClearSnoozeReason {
		query = query.Set("snooze_reason = NULL")
	}
	if update.ClearCausedByIncidentUID {
		query = query.Set("caused_by_incident_uid = NULL")
	}
	return query
}

// CreateIncident inserts an incident, claiming its short per-org number first.
// Mirrors the Postgres implementation (sync-pg-to-sqlite): the retry-on-
// collision loop is shared so both engines behave identically.
func (s *Service) CreateIncident(ctx context.Context, incident *models.Incident) error {
	return db.CreateIncidentWithNumber(ctx, incident, s.nextIncidentNumber, func(ctx context.Context) error {
		_, err := s.db.NewInsert().Model(incident).Exec(ctx)
		return err
	})
}

// nextIncidentNumber returns MAX(number)+1 for an organization. Soft-deleted
// rows are deliberately counted: a number is never reused.
func (s *Service) nextIncidentNumber(ctx context.Context, orgUID string) (int64, error) {
	var next int64

	err := s.db.NewSelect().
		Model((*models.Incident)(nil)).
		ColumnExpr("COALESCE(MAX(number), 0) + 1").
		Where("organization_uid = ?", orgUID).
		Scan(ctx, &next)

	return next, err
}

// GetIncidentByNumber resolves the short `#42` reference within an
// organization.
func (s *Service) GetIncidentByNumber(ctx context.Context, orgUID string, number int64) (*models.Incident, error) {
	incident := new(models.Incident)

	err := s.db.NewSelect().
		Model(incident).
		Where("organization_uid = ?", orgUID).
		Where("number = ?", number).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return incident, nil
}

func (s *Service) GetIncident(ctx context.Context, orgUID, uid string) (*models.Incident, error) {
	incident := new(models.Incident)

	err := s.db.NewSelect().
		Model(incident).
		Where("organization_uid = ?", orgUID).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return incident, nil
}

func (s *Service) FindActiveIncidentByCheckUID(ctx context.Context, checkUID string) (*models.Incident, error) {
	incident := new(models.Incident)

	err := s.db.NewSelect().
		Model(incident).
		Where("state = ?", models.IncidentStateActive).
		Where("deleted_at IS NULL").
		Where(
			"(check_uid = ? OR uid IN ("+
				"SELECT incident_uid FROM incident_member_checks "+
				"WHERE check_uid = ? AND currently_failing = 1))",
			checkUID, checkUID,
		).
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return incident, nil
}

func (s *Service) FindRecentlyResolvedIncidentByCheckUID(
	ctx context.Context, checkUID string, since time.Time,
) (*models.Incident, error) {
	incident := new(models.Incident)

	err := s.db.NewSelect().
		Model(incident).
		Where("check_uid = ?", checkUID).
		Where("check_group_uid IS NULL").
		Where("state = ?", models.IncidentStateResolved).
		Where("resolved_at >= ?", since).
		Where("deleted_at IS NULL").
		Order("resolved_at DESC").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return incident, nil
}

// FindActiveIncidentByGroupUID returns the active group incident keyed on check_group_uid.
func (s *Service) FindActiveIncidentByGroupUID(ctx context.Context, groupUID string) (*models.Incident, error) {
	incident := new(models.Incident)

	err := s.db.NewSelect().
		Model(incident).
		Where("check_group_uid = ?", groupUID).
		Where("state = ?", models.IncidentStateActive).
		Where("deleted_at IS NULL").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return incident, nil
}

// FindRecentlyResolvedIncidentByGroupUID returns the most recent resolved group incident
// for a group resolved after `since`. Used to reopen within cooldown.
func (s *Service) FindRecentlyResolvedIncidentByGroupUID(
	ctx context.Context, groupUID string, since time.Time,
) (*models.Incident, error) {
	incident := new(models.Incident)

	err := s.db.NewSelect().
		Model(incident).
		Where("check_group_uid = ?", groupUID).
		Where("state = ?", models.IncidentStateResolved).
		Where("resolved_at >= ?", since).
		Where("deleted_at IS NULL").
		Order("resolved_at DESC").
		Limit(1).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return incident, nil
}

// ListIncidentMemberChecks returns all member rows for a group incident.
func (s *Service) ListIncidentMemberChecks(
	ctx context.Context, incidentUID string,
) ([]*models.IncidentMemberCheck, error) {
	var members []*models.IncidentMemberCheck

	err := s.db.NewSelect().
		Model(&members).
		Where("incident_uid = ?", incidentUID).
		Order("first_failure_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return members, nil
}

// GetIncidentMemberCheck returns a single member row, or sql.ErrNoRows.
func (s *Service) GetIncidentMemberCheck(
	ctx context.Context, incidentUID, checkUID string,
) (*models.IncidentMemberCheck, error) {
	member := new(models.IncidentMemberCheck)

	err := s.db.NewSelect().
		Model(member).
		Where("incident_uid = ?", incidentUID).
		Where("check_uid = ?", checkUID).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return member, nil
}

// UpsertIncidentMemberCheck inserts or updates a member row.
func (s *Service) UpsertIncidentMemberCheck(ctx context.Context, member *models.IncidentMemberCheck) error {
	_, err := s.db.NewInsert().
		Model(member).
		On("CONFLICT (incident_uid, check_uid) DO UPDATE").
		Set("last_failure_at = EXCLUDED.last_failure_at").
		Set("failure_count = EXCLUDED.failure_count").
		Set("currently_failing = EXCLUDED.currently_failing").
		Set("last_recovery_at = EXCLUDED.last_recovery_at").
		Exec(ctx)

	return err
}

// UpdateIncidentMemberCheck applies a partial update to a member row.
func (s *Service) UpdateIncidentMemberCheck(
	ctx context.Context, incidentUID, checkUID string, update *models.IncidentMemberUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.IncidentMemberCheck)(nil)).
		Where("incident_uid = ?", incidentUID).
		Where("check_uid = ?", checkUID)

	hasUpdate := false

	if update.LastFailureAt != nil {
		query = query.Set("last_failure_at = ?", *update.LastFailureAt)
		hasUpdate = true
	}

	if update.LastRecoveryAt != nil {
		query = query.Set("last_recovery_at = ?", *update.LastRecoveryAt)
		hasUpdate = true
	}

	if update.FailureCount != nil {
		query = query.Set("failure_count = ?", *update.FailureCount)
		hasUpdate = true
	}

	if update.CurrentlyFailing != nil {
		query = query.Set("currently_failing = ?", *update.CurrentlyFailing)
		hasUpdate = true
	}

	if !hasUpdate {
		return nil
	}

	_, err := query.Exec(ctx)

	return err
}

// CountFailingIncidentMembers returns the number of members with currently_failing = true.
func (s *Service) CountFailingIncidentMembers(ctx context.Context, incidentUID string) (int, error) {
	count, err := s.db.NewSelect().
		Model((*models.IncidentMemberCheck)(nil)).
		Where("incident_uid = ?", incidentUID).
		Where("currently_failing = ?", true).
		Count(ctx)

	return count, err
}

// applyIncidentsFilter applies every ListIncidentsFilter predicate except
// cursor and limit, so the list query and the COUNT query built from the
// same filter can never drift apart.
func applyIncidentsFilter(query *bun.SelectQuery, filter *models.ListIncidentsFilter) *bun.SelectQuery {
	query = query.
		Where("organization_uid = ?", filter.OrganizationUID).
		Where("deleted_at IS NULL")

	if len(filter.CheckUIDs) > 0 {
		query = query.Where("check_uid IN (?)", bun.List(filter.CheckUIDs))
	}

	if filter.CheckGroupUID != "" {
		query = query.Where("check_group_uid = ?", filter.CheckGroupUID)
	}

	if filter.MemberCheckUID != "" {
		query = query.Where(
			"(check_uid = ? OR uid IN ("+
				"SELECT incident_uid FROM incident_member_checks WHERE check_uid = ?))",
			filter.MemberCheckUID, filter.MemberCheckUID,
		)
	}

	if len(filter.States) > 0 {
		query = query.Where("state IN (?)", bun.List(filter.States))
	}

	if filter.Since != nil {
		query = query.Where("started_at >= ?", *filter.Since)
	}

	if filter.Until != nil {
		query = query.Where("started_at < ?", *filter.Until)
	}

	if filter.HideSuppressed {
		query = query.Where("paging_suppressed = ?", false)
	}

	if filter.AckedOnly {
		// Acked = active with non-NULL acknowledged_at. Snoozed-but-not-yet
		// expired counts as snoozed, not just acked, so exclude it here.
		query = query.
			Where("acknowledged_at IS NOT NULL").
			Where("(snoozed_until IS NULL OR snoozed_until <= ?)", time.Now())
	}

	if filter.SnoozedOnly {
		query = query.Where("snoozed_until > ?", time.Now())
	}

	if filter.CausedByUID != "" {
		query = query.Where("caused_by_incident_uid = ?", filter.CausedByUID)
	}

	return query
}

func (s *Service) ListIncidents(
	ctx context.Context, filter *models.ListIncidentsFilter,
) ([]*models.Incident, int64, error) {
	var incidents []*models.Incident

	query := applyIncidentsFilter(
		s.db.NewSelect().Model(&incidents).Order("started_at DESC"),
		filter,
	)

	countQuery := applyIncidentsFilter(s.db.NewSelect().Model((*models.Incident)(nil)), filter)

	if filter.CursorTimestamp != nil {
		if filter.CursorUID != nil {
			query = query.Where("(started_at < ? OR (started_at = ? AND uid < ?))",
				*filter.CursorTimestamp, *filter.CursorTimestamp, *filter.CursorUID)
		} else {
			query = query.Where("started_at < ?", *filter.CursorTimestamp)
		}
	}

	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}

	total, err := countQuery.Count(ctx)
	if err != nil {
		return nil, 0, err
	}

	err = query.Scan(ctx)

	return incidents, int64(total), err
}

func (s *Service) UpdateIncident(ctx context.Context, uid string, update *models.IncidentUpdate) error {
	query := s.db.NewUpdate().
		Model((*models.Incident)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	query = applyIncidentSetFields(query, update)
	query = applyClearFields(query, update)

	_, err := query.Exec(ctx)

	return err
}

func (s *Service) CountActiveIncidentsByCheckUID(ctx context.Context, checkUID string) (int, error) {
	count, err := s.db.NewSelect().
		Model((*models.Incident)(nil)).
		Where("check_uid = ?", checkUID).
		Where("state = ?", models.IncidentStateActive).
		Where("deleted_at IS NULL").
		Count(ctx)

	return count, err
}

func (s *Service) ListExpiredSnoozedIncidents(ctx context.Context, now time.Time) ([]*models.Incident, error) {
	var incidents []*models.Incident

	err := s.db.NewSelect().
		Model(&incidents).
		Where("state = ?", models.IncidentStateActive).
		Where("snoozed_until IS NOT NULL").
		Where("snoozed_until <= ?", now).
		Where("deleted_at IS NULL").
		Order("snoozed_until ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list expired snoozed incidents: %w", err)
	}

	return incidents, nil
}

// UpdateCheckStatusAndClocks writes the check's status, streak,
// status_changed_at and both incident clocks in a single atomic UPDATE,
// replacing the former separate UpdateCheckStatus + UpdateCheckIncidentClocks
// round-trips. statusChangedAt is written only when non-nil. The clock fields
// use IncidentClockUpdate's tri-state (nil + !clear leaves the column
// untouched, nil + clear writes NULL, non-nil writes the value). updated_at is
// written once.
func (s *Service) UpdateCheckStatusAndClocks(
	ctx context.Context,
	checkUID string,
	status models.CheckStatus,
	streak int,
	statusChangedAt *time.Time,
	clocks models.IncidentClockUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.Check)(nil)).
		Where("uid = ?", checkUID).
		Where("deleted_at IS NULL").
		Set("status = ?", status).
		Set("status_streak = ?", streak).
		Set("updated_at = ?", time.Now())

	if statusChangedAt != nil {
		query = query.Set("status_changed_at = ?", *statusChangedAt)
	}

	if clocks.FirstFailureAt != nil {
		query = query.Set("first_failure_at = ?", *clocks.FirstFailureAt)
	} else if clocks.ClearFirstFailureAt {
		query = query.Set("first_failure_at = NULL")
	}
	if clocks.FirstSuccessSinceFailureAt != nil {
		query = query.Set("first_success_since_failure_at = ?", *clocks.FirstSuccessSinceFailureAt)
	} else if clocks.ClearFirstSuccessSinceFailureAt {
		query = query.Set("first_success_since_failure_at = NULL")
	}

	_, err := query.Exec(ctx)

	return err
}

// UpdateCheckFlapState persists the rolling flap counter and the last-outage
// timestamp on a check. Written only on the rare incident open/reopen — never
// on the per-result hot path. See spec 2026-06-30-07.
func (s *Service) UpdateCheckFlapState(
	ctx context.Context, checkUID string, flapCount int, lastOutageAt time.Time,
) error {
	_, err := s.db.NewUpdate().
		Model((*models.Check)(nil)).
		Where("uid = ?", checkUID).
		Where("deleted_at IS NULL").
		Set("flap_count = ?", flapCount).
		Set("last_outage_at = ?", lastOutageAt).
		Set("updated_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// Event operations

func (s *Service) CreateEvent(ctx context.Context, event *models.Event) error {
	_, err := s.db.NewInsert().Model(event).Exec(ctx)
	return err
}

func (s *Service) ListEvents(ctx context.Context, filter *models.ListEventsFilter) ([]*models.Event, error) {
	var events []*models.Event

	query := s.db.NewSelect().
		Model(&events).
		Where("organization_uid = ?", filter.OrganizationUID).
		Order("created_at DESC")

	if filter.IncidentUID != nil {
		query = query.Where("incident_uid = ?", *filter.IncidentUID)
	}

	if filter.CheckUID != nil {
		query = query.Where("check_uid = ?", *filter.CheckUID)
	}

	if len(filter.EventTypes) > 0 {
		query = query.Where("event_type IN (?)", bun.List(filter.EventTypes))
	}

	if filter.ActorType != nil {
		query = query.Where("actor_type = ?", *filter.ActorType)
	}

	if filter.Since != nil {
		query = query.Where("created_at >= ?", *filter.Since)
	}

	if filter.Until != nil {
		query = query.Where("created_at < ?", *filter.Until)
	}

	if filter.CursorTimestamp != nil {
		if filter.CursorUID != nil {
			query = query.Where("(created_at < ? OR (created_at = ? AND uid < ?))",
				*filter.CursorTimestamp, *filter.CursorTimestamp, *filter.CursorUID)
		} else {
			query = query.Where("created_at < ?", *filter.CursorTimestamp)
		}
	}

	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}

	err := query.Scan(ctx)

	return events, err
}

// GetStateEntry retrieves a state entry by organization and key.
func (s *Service) GetStateEntry(ctx context.Context, orgUID *string, key string) (*models.StateEntry, error) {
	entry := new(models.StateEntry)

	query := s.db.NewSelect().
		Model(entry).
		Where("key = ?", key).
		Where("deleted_at IS NULL").
		Where("(expires_at IS NULL OR expires_at > datetime('now'))")

	if orgUID != nil {
		query = query.Where("organization_uid = ?", *orgUID)
	} else {
		query = query.Where("organization_uid IS NULL")
	}

	err := query.Scan(ctx)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // Entry not found is not an error
	}

	if err != nil {
		return nil, fmt.Errorf("failed to get state entry: %w", err)
	}

	return entry, nil
}

// SetStateEntry creates or updates a state entry.
//
// SQLite (and PostgreSQL by default) treats NULL values as distinct in
// UNIQUE constraints, so an INSERT … ON CONFLICT(organization_uid, key)
// does not fire when organization_uid is NULL — duplicate global rows
// would silently accumulate. To keep callers' upsert intent honest we
// run an explicit UPDATE first when orgUID is nil; if no row matches we
// fall through to INSERT.
func (s *Service) SetStateEntry(
	ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration,
) error {
	now := time.Now()

	var expiresAt *time.Time
	if ttl != nil {
		ts := now.Add(*ttl)
		expiresAt = &ts
	}

	if orgUID == nil {
		res, err := s.db.NewUpdate().
			Model((*models.StateEntry)(nil)).
			Where("organization_uid IS NULL").
			Where("key = ?", key).
			Set("value = ?", value).
			Set("expires_at = ?", expiresAt).
			Set("updated_at = ?", now).
			Set("deleted_at = NULL").
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to update state entry: %w", err)
		}

		rows, _ := res.RowsAffected()
		if rows > 0 {
			return nil
		}
	}

	entry := models.NewStateEntry(orgUID, key)
	entry.Value = value
	entry.ExpiresAt = expiresAt

	_, err := s.db.NewInsert().
		Model(entry).
		On("CONFLICT (organization_uid, key) DO UPDATE").
		Set("value = EXCLUDED.value").
		Set("expires_at = EXCLUDED.expires_at").
		Set("updated_at = ?", now).
		Set("deleted_at = NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to set state entry: %w", err)
	}

	return nil
}

// DeleteStateEntry soft-deletes a state entry, returning whether a live row
// existed to delete (compare-and-set on deleted_at IS NULL): false means the
// entry was already deleted (or never existed) — a replay of a prior consume.
func (s *Service) DeleteStateEntry(ctx context.Context, orgUID *string, key string) (bool, error) {
	query := s.db.NewUpdate().
		Model((*models.StateEntry)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("key = ?", key).
		Where("deleted_at IS NULL")

	if orgUID != nil {
		query = query.Where("organization_uid = ?", *orgUID)
	} else {
		query = query.Where("organization_uid IS NULL")
	}

	res, err := query.Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to delete state entry: %w", err)
	}

	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("failed to read delete state entry result: %w", err)
	}

	return affected > 0, nil
}

// ListStateEntries returns all entries matching the key prefix.
func (s *Service) ListStateEntries(
	ctx context.Context, orgUID *string, keyPrefix string,
) ([]*models.StateEntry, error) {
	var entries []*models.StateEntry

	query := s.db.NewSelect().
		Model(&entries).
		Where("deleted_at IS NULL").
		Where("(expires_at IS NULL OR expires_at > datetime('now'))").
		Order("key")

	if orgUID != nil {
		query = query.Where("organization_uid = ?", *orgUID)
	} else {
		query = query.Where("organization_uid IS NULL")
	}

	if keyPrefix != "" {
		query = query.Where("key LIKE ?", keyPrefix+"%")
	}

	err := query.Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list state entries: %w", err)
	}

	return entries, nil
}

// GetOrCreateStateEntry returns existing entry or creates new one.
func (s *Service) GetOrCreateStateEntry(
	ctx context.Context, orgUID *string, key string, defaultValue *models.JSONMap, ttl *time.Duration,
) (*models.StateEntry, bool, error) {
	// First, try to get existing entry
	existing, err := s.GetStateEntry(ctx, orgUID, key)
	if err != nil {
		return nil, false, err
	}

	if existing != nil {
		return existing, false, nil
	}

	// Entry doesn't exist, create it
	now := time.Now()
	entry := models.NewStateEntry(orgUID, key)
	entry.Value = defaultValue

	if ttl != nil {
		expiresAt := now.Add(*ttl)
		entry.ExpiresAt = &expiresAt
	}

	// Use ON CONFLICT DO NOTHING to handle race conditions
	res, err := s.db.NewInsert().
		Model(entry).
		On("CONFLICT (organization_uid, key) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create state entry: %w", err)
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		// Another process created it, fetch the existing entry
		existing, err = s.GetStateEntry(ctx, orgUID, key)
		if err != nil {
			return nil, false, err
		}

		return existing, false, nil
	}

	return entry, true, nil
}

// SetStateEntryIfNotExists creates entry only if key doesn't exist.
func (s *Service) SetStateEntryIfNotExists(
	ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration,
) (bool, error) {
	now := time.Now()
	entry := models.NewStateEntry(orgUID, key)
	entry.Value = value

	if ttl != nil {
		expiresAt := now.Add(*ttl)
		entry.ExpiresAt = &expiresAt
	}

	res, err := s.db.NewInsert().
		Model(entry).
		On("CONFLICT (organization_uid, key) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to set state entry if not exists: %w", err)
	}

	rowsAffected, _ := res.RowsAffected()

	return rowsAffected > 0, nil
}

// DeleteExpiredStateEntries removes entries past their expires_at.
func (s *Service) DeleteExpiredStateEntries(ctx context.Context) (int64, error) {
	res, err := s.db.NewUpdate().
		Model((*models.StateEntry)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("expires_at < datetime('now')").
		Where("expires_at IS NOT NULL").
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to delete expired state entries: %w", err)
	}

	count, _ := res.RowsAffected()

	return count, nil
}

// GetSystemParameter retrieves a system parameter by key.
// Returns (nil, nil) if not found - this is intentional to distinguish
// "not found" from actual errors.
//
//nolint:nilnil // Returning (nil, nil) for "not found" is intentional
func (s *Service) GetSystemParameter(ctx context.Context, key string) (*models.Parameter, error) {
	param := new(models.Parameter)

	err := s.db.NewSelect().
		Model(param).
		Where("organization_uid IS NULL").
		Where("key = ?", key).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}

		return nil, fmt.Errorf("failed to get system parameter: %w", err)
	}

	return param, nil
}

// SetSystemParameter creates or updates a system parameter.
func (s *Service) SetSystemParameter(ctx context.Context, key string, value any, secret bool) error {
	// Check if parameter exists
	existing, err := s.GetSystemParameter(ctx, key)
	if err != nil {
		return err
	}

	now := time.Now()
	jsonValue := models.ParameterValue(value)

	if existing != nil {
		// Update existing parameter
		_, err = s.db.NewUpdate().
			Model((*models.Parameter)(nil)).
			Set("value = ?", jsonValue).
			Set("secret = ?", secret).
			Set("updated_at = ?", now).
			Where("uid = ?", existing.UID).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to update system parameter: %w", err)
		}

		return nil
	}

	// Create new parameter
	param := models.NewSystemParameter(key, jsonValue, secret)

	_, err = s.db.NewInsert().
		Model(param).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to create system parameter: %w", err)
	}

	return nil
}

// GetOrCreateSystemParameter returns the existing system parameter for key, or
// atomically creates it holding value. The bool reports whether this caller is
// the one that created it.
//
// The insert rides the partial unique index parameters_system_key_idx
// (key WHERE deleted_at IS NULL AND organization_uid IS NULL) with ON CONFLICT
// DO NOTHING, then re-reads on a lost race — the GetOrCreateStateEntry pattern.
// A read-then-write would let concurrent pods each persist their own generated
// value; here every loser adopts the winner's.
func (s *Service) GetOrCreateSystemParameter(
	ctx context.Context, key string, value any, secret bool,
) (*models.Parameter, bool, error) {
	existing, err := s.GetSystemParameter(ctx, key)
	if err != nil {
		return nil, false, err
	}

	if existing != nil {
		return existing, false, nil
	}

	param := models.NewSystemParameter(key, models.ParameterValue(value), secret)

	res, err := s.db.NewInsert().
		Model(param).
		On("CONFLICT (key) WHERE deleted_at IS NULL AND organization_uid IS NULL DO NOTHING").
		Exec(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create system parameter: %w", err)
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		// Another process won the race: adopt ITS value, never ours.
		existing, err = s.GetSystemParameter(ctx, key)
		if err != nil {
			return nil, false, err
		}

		return existing, false, nil
	}

	return param, true, nil
}

// DeleteSystemParameter soft-deletes a system parameter.
func (s *Service) DeleteSystemParameter(ctx context.Context, key string) error {
	res, err := s.db.NewUpdate().
		Model((*models.Parameter)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("organization_uid IS NULL").
		Where("key = ?", key).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete system parameter: %w", err)
	}

	rowsAffected, _ := res.RowsAffected()
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// ListSystemParameters returns all system parameters.
func (s *Service) ListSystemParameters(ctx context.Context) ([]*models.Parameter, error) {
	var params []*models.Parameter

	err := s.db.NewSelect().
		Model(&params).
		Where("organization_uid IS NULL").
		Where("deleted_at IS NULL").
		Order("key ASC").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list system parameters: %w", err)
	}

	return params, nil
}

// Organization Parameter operations

// ListOrgParametersByKey returns all org-scoped parameters with a specific key.
func (s *Service) ListOrgParametersByKey(ctx context.Context, key string) ([]*models.Parameter, error) {
	var params []*models.Parameter

	err := s.db.NewSelect().
		Model(&params).
		Where("organization_uid IS NOT NULL").
		Where("key = ?", key).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list org parameters by key: %w", err)
	}

	return params, nil
}

// GetOrgParameter retrieves an org-scoped parameter by orgUID and key.
func (s *Service) GetOrgParameter(ctx context.Context, orgUID, key string) (*models.Parameter, error) {
	param := new(models.Parameter)

	err := s.db.NewSelect().
		Model(param).
		Where("organization_uid = ?", orgUID).
		Where("key = ?", key).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // Not found is not an error
		}

		return nil, fmt.Errorf("failed to get org parameter: %w", err)
	}

	return param, nil
}

// SetOrgParameter creates or updates an org-scoped parameter.
func (s *Service) SetOrgParameter(ctx context.Context, orgUID, key string, value any, secret bool) error {
	existing, err := s.GetOrgParameter(ctx, orgUID, key)
	if err != nil {
		return err
	}

	now := time.Now()
	jsonValue := models.ParameterValue(value)

	if existing != nil {
		_, err = s.db.NewUpdate().
			Model((*models.Parameter)(nil)).
			Set("value = ?", jsonValue).
			Set("secret = ?", secret).
			Set("updated_at = ?", now).
			Where("uid = ?", existing.UID).
			Exec(ctx)
		if err != nil {
			return fmt.Errorf("failed to update org parameter: %w", err)
		}

		return nil
	}

	param := models.NewParameter(orgUID, key, jsonValue)
	param.Secret = &secret

	_, err = s.db.NewInsert().
		Model(param).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to create org parameter: %w", err)
	}

	return nil
}

// DeleteOrgParameter soft-deletes an org-scoped parameter.
func (s *Service) DeleteOrgParameter(ctx context.Context, orgUID, key string) error {
	_, err := s.db.NewUpdate().
		Model((*models.Parameter)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("organization_uid = ?", orgUID).
		Where("key = ?", key).
		Where("deleted_at IS NULL").
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete org parameter: %w", err)
	}

	return nil
}

// IntegrationConnection operations

// CreateChannel creates a new integration connection.
func (s *Service) CreateChannel(ctx context.Context, conn *models.Integration) error {
	_, err := s.db.NewInsert().Model(conn).Exec(ctx)
	return err
}

// GetChannel retrieves an integration connection by UID.
func (s *Service) GetChannel(ctx context.Context, uid string) (*models.Integration, error) {
	conn := new(models.Integration)

	err := s.db.NewSelect().
		Model(conn).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// GetChannelByProperty retrieves a connection by a settings property.
func (s *Service) GetChannelByProperty(
	ctx context.Context, connType, propertyName, propertyValue string,
) (*models.Integration, error) {
	conn := new(models.Integration)

	jsonPath := "$." + propertyName

	err := s.db.NewSelect().
		Model(conn).
		Where("type = ?", connType).
		Where("json_extract(settings, ?) = ?", jsonPath, propertyValue).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// GetChannelByPropertyForOrg is the org-scoped variant of
// GetChannelByProperty — same settings-property lookup, additionally
// filtered to a single organization so a workspace connected to several
// orgs resolves to that org's own row.
func (s *Service) GetChannelByPropertyForOrg(
	ctx context.Context, orgUID, connType, propertyName, propertyValue string,
) (*models.Integration, error) {
	conn := new(models.Integration)

	jsonPath := "$." + propertyName

	err := s.db.NewSelect().
		Model(conn).
		Where("organization_uid = ?", orgUID).
		Where("type = ?", connType).
		Where("json_extract(settings, ?) = ?", jsonPath, propertyValue).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// ListChannelsByProperty returns every non-deleted connection matching a
// settings property, across ALL organizations, ordered created_at ASC
// (oldest first). See db.Service for the callers (uninstall fan-out and the
// inbound-routing deterministic fallback).
func (s *Service) ListChannelsByProperty(
	ctx context.Context, connType, propertyName, propertyValue string,
) ([]*models.Integration, error) {
	conns := make([]*models.Integration, 0)

	jsonPath := "$." + propertyName

	err := s.db.NewSelect().
		Model(&conns).
		Where("type = ?", connType).
		Where("json_extract(settings, ?) = ?", jsonPath, propertyValue).
		Where("deleted_at IS NULL").
		Order("created_at ASC").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return conns, nil
}

// ListChannels lists integration connections with optional filtering. An
// empty filter.OrganizationUID lists across ALL organizations — used by
// CountInstalledTeams, which needs a global view of Slack connections.
// Every other caller passes a real org UID, so this is a no-op for them.
func (s *Service) ListChannels(
	ctx context.Context, filter *models.ListIntegrationsFilter,
) ([]*models.Integration, error) {
	var connections []*models.Integration

	query := s.db.NewSelect().
		Model(&connections).
		Where("deleted_at IS NULL").
		Order("created_at DESC")

	if filter.OrganizationUID != "" {
		query = query.Where("organization_uid = ?", filter.OrganizationUID)
	}

	if filter.Type != nil {
		query = query.Where("type = ?", *filter.Type)
	}

	if filter.Enabled != nil {
		query = query.Where("enabled = ?", *filter.Enabled)
	}

	err := query.Scan(ctx)

	return connections, err
}

// UpdateChannel updates an integration connection.
func (s *Service) UpdateChannel(
	ctx context.Context, uid string, update *models.IntegrationUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.Integration)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}

	if update.Enabled != nil {
		query = query.Set("enabled = ?", *update.Enabled)
	}

	if update.IsDefault != nil {
		query = query.Set("is_default = ?", *update.IsDefault)
	}

	if update.Settings != nil {
		query = query.Set("settings = ?", *update.Settings)
	}

	switch {
	case update.ClearSettingsPrivate:
		query = query.Set("settings_private = NULL").
			Set("settings_private_keys = NULL")
	case update.SettingsPrivate != nil:
		query = query.Set("settings_private = ?", *update.SettingsPrivate)
		if update.SettingsPrivateKeys != nil {
			query = query.Set("settings_private_keys = ?", *update.SettingsPrivateKeys)
		}
	}

	_, err := query.Exec(ctx)

	return err
}

// DeleteChannel soft-deletes an integration connection.
func (s *Service) DeleteChannel(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.Integration)(nil)).
		Set("deleted_at = ?", time.Now()).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Exec(ctx)

	return err
}

// CheckConnection operations

// CreateCheckConnection creates a new check-connection relationship.
func (s *Service) CreateCheckConnection(ctx context.Context, conn *models.CheckConnection) error {
	_, err := s.db.NewInsert().Model(conn).Exec(ctx)
	return err
}

// DeleteCheckConnection deletes a check-connection relationship.
func (s *Service) DeleteCheckConnection(ctx context.Context, checkUID, connectionUID string) error {
	_, err := s.db.NewDelete().
		Model((*models.CheckConnection)(nil)).
		Where("check_uid = ?", checkUID).
		Where("integration_uid = ?", connectionUID).
		Exec(ctx)

	return err
}

// ListChannelsForCheck returns all connections associated with a check.
func (s *Service) ListChannelsForCheck(
	ctx context.Context, checkUID string,
) ([]*models.Integration, error) {
	var connections []*models.Integration

	err := s.db.NewSelect().
		Model(&connections).
		Join("INNER JOIN check_channels cc ON cc.integration_uid = integration.uid").
		Where("cc.check_uid = ?", checkUID).
		Where("integration.deleted_at IS NULL").
		Order("integration.created_at DESC").
		Scan(ctx)

	return connections, err
}

// SetCheckConnections replaces all connections for a check.
func (s *Service) SetCheckConnections(ctx context.Context, checkUID string, connectionUIDs []string) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Get the check to retrieve organization_uid
		var check models.Check
		if err := tx.NewSelect().
			Model(&check).
			Where("uid = ?", checkUID).
			Scan(ctx); err != nil {
			return err
		}

		// Delete existing connections
		if _, err := tx.NewDelete().
			Model((*models.CheckConnection)(nil)).
			Where("check_uid = ?", checkUID).
			Exec(ctx); err != nil {
			return err
		}

		// Insert new connections
		for _, connUID := range connectionUIDs {
			checkConn := models.NewCheckConnection(checkUID, connUID, check.OrganizationUID)
			if _, err := tx.NewInsert().Model(checkConn).Exec(ctx); err != nil {
				return err
			}
		}

		return nil
	})
}

// ListDefaultChannels returns all default connections for an organization.
func (s *Service) ListDefaultChannels(
	ctx context.Context, orgUID string,
) ([]*models.Integration, error) {
	var connections []*models.Integration

	err := s.db.NewSelect().
		Model(&connections).
		Where("organization_uid = ?", orgUID).
		Where("is_default = ?", true).
		Where("enabled = ?", true).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)

	return connections, err
}

// UpdateCheckConnection updates settings for a check-connection.
func (s *Service) UpdateCheckConnection(
	ctx context.Context, checkUID, connectionUID string, update *models.CheckConnectionUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.CheckConnection)(nil)).
		Where("check_uid = ?", checkUID).
		Where("integration_uid = ?", connectionUID).
		Set("updated_at = ?", time.Now())

	if update.Settings != nil {
		query = query.Set("settings = ?", update.Settings)
	}

	result, err := query.Exec(ctx)
	if err != nil {
		return err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// GetCheckConnection retrieves a specific check-connection with settings.
func (s *Service) GetCheckConnection(
	ctx context.Context, checkUID, connectionUID string,
) (*models.CheckConnection, error) {
	var checkConn models.CheckConnection

	err := s.db.NewSelect().
		Model(&checkConn).
		Where("check_uid = ?", checkUID).
		Where("integration_uid = ?", connectionUID).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return &checkConn, nil
}

// ListCheckConnectionsWithSettings returns all check-connections for a check including settings.
func (s *Service) ListCheckConnectionsWithSettings(
	ctx context.Context, checkUID string,
) ([]*models.CheckConnection, error) {
	var ccs []*models.CheckConnection

	err := s.db.NewSelect().
		Model(&ccs).
		Where("check_uid = ?", checkUID).
		Order("created_at ASC").
		Scan(ctx)

	return ccs, err
}

// --- StatusPage operations ---

// CreateStatusPage inserts a new status page.
func (s *Service) CreateStatusPage(ctx context.Context, page *models.StatusPage) error {
	_, err := s.db.NewInsert().Model(page).Exec(ctx)

	return err
}

// GetStatusPage retrieves a status page by UID within an organization.
func (s *Service) GetStatusPage(ctx context.Context, orgUID, uid string) (*models.StatusPage, error) {
	page := new(models.StatusPage)

	err := s.db.NewSelect().
		Model(page).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return page, nil
}

// GetStatusPageBySlug retrieves a status page by slug within an organization.
func (s *Service) GetStatusPageBySlug(ctx context.Context, orgUID, slug string) (*models.StatusPage, error) {
	page := new(models.StatusPage)

	err := s.db.NewSelect().
		Model(page).
		Where("slug = ?", slug).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return page, nil
}

// GetStatusPageByUidOrSlug retrieves a status page by UID or slug.
func (s *Service) GetStatusPageByUidOrSlug(
	ctx context.Context, orgUID, identifier string,
) (*models.StatusPage, error) {
	page := new(models.StatusPage)

	query := s.db.NewSelect().
		Model(page).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL")

	if _, err := uuid.Parse(identifier); err == nil {
		query = query.Where("uid = ?", identifier)
	} else {
		query = query.Where("slug = ?", identifier)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, err
	}

	return page, nil
}

// GetStatusPageByCustomDomain retrieves the single live status page bound to a
// custom domain. The custom_domain column is globally unique among live rows,
// so at most one row matches.
func (s *Service) GetStatusPageByCustomDomain(ctx context.Context, domain string) (*models.StatusPage, error) {
	page := new(models.StatusPage)

	err := s.db.NewSelect().
		Model(page).
		Where("custom_domain = ?", domain).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return page, nil
}

// ListStatusPagesWithCustomDomain lists every live status page (all orgs) with
// a custom domain set — the periodic re-verify job's work list.
func (s *Service) ListStatusPagesWithCustomDomain(ctx context.Context) ([]*models.StatusPage, error) {
	var pages []*models.StatusPage

	err := s.db.NewSelect().
		Model(&pages).
		Where("custom_domain IS NOT NULL").
		Where("deleted_at IS NULL").
		Order("created_at ASC").
		Scan(ctx)

	return pages, err
}

// CountStatusPagesWithCustomDomain counts an org's live pages with a custom
// domain set.
func (s *Service) CountStatusPagesWithCustomDomain(ctx context.Context, orgUID string) (int, error) {
	return s.db.NewSelect().
		Model((*models.StatusPage)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("custom_domain IS NOT NULL").
		Where("deleted_at IS NULL").
		Count(ctx)
}

// GetDefaultStatusPage retrieves the default status page for an organization.
func (s *Service) GetDefaultStatusPage(ctx context.Context, orgUID string) (*models.StatusPage, error) {
	page := new(models.StatusPage)

	err := s.db.NewSelect().
		Model(page).
		Where("organization_uid = ?", orgUID).
		Where("is_default = ?", true).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return page, nil
}

// ListStatusPages lists all status pages for an organization.
func (s *Service) ListStatusPages(ctx context.Context, orgUID string) ([]*models.StatusPage, error) {
	var pages []*models.StatusPage

	err := s.db.NewSelect().
		Model(&pages).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Scan(ctx)

	return pages, err
}

// UpdateStatusPage updates a status page by UID.
func (s *Service) UpdateStatusPage(ctx context.Context, uid string, update *models.StatusPageUpdate) error {
	query := s.db.NewUpdate().
		Model((*models.StatusPage)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}

	if update.Slug != nil {
		query = query.Set("slug = ?", *update.Slug)
	}

	if update.Description != nil {
		query = query.Set("description = ?", *update.Description)
	}

	if update.Visibility != nil {
		query = query.Set("visibility = ?", *update.Visibility)
	}

	if update.IsDefault != nil {
		query = query.Set("is_default = ?", *update.IsDefault)
	}

	if update.Enabled != nil {
		query = query.Set("enabled = ?", *update.Enabled)
	}

	if update.ShowAvailability != nil {
		query = query.Set("show_availability = ?", *update.ShowAvailability)
	}

	if update.ShowResponseTime != nil {
		query = query.Set("show_response_time = ?", *update.ShowResponseTime)
	}

	if update.HistoryDays != nil {
		query = query.Set("history_days = ?", *update.HistoryDays)
	}

	if update.HistoryPeriod != nil {
		query = query.Set("history_period = ?", *update.HistoryPeriod)
	}

	if update.Language != nil {
		query = query.Set("language = ?", *update.Language)
	}

	if update.CustomCSS != nil {
		// An empty stylesheet is stored as NULL, not '': the appearance editor
		// clears the field by submitting an empty textarea, and "no custom CSS"
		// must read back as nil so status0 renders no <style> element at all.
		if *update.CustomCSS == "" {
			query = query.Set("custom_css = NULL")
		} else {
			query = query.Set("custom_css = ?", *update.CustomCSS)
		}
	}

	if update.Settings != nil {
		// The service layer has already applied the no-deep-merge
		// section-replace-or-reset semantics; this is a whole-column overwrite.
		query = query.Set("settings = ?", *update.Settings)
	}

	_, err := query.Exec(ctx)

	return err
}

// UpdateStatusPageCustomDomain overwrites every custom-domain column in one
// write. All lifecycle transitions (set, clear, verify-now, re-verify) go
// through here, so nil pointers write SQL NULL verbatim.
func (s *Service) UpdateStatusPageCustomDomain(
	ctx context.Context, uid string, update *models.StatusPageCustomDomainUpdate,
) error {
	_, err := s.db.NewUpdate().
		Model((*models.StatusPage)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("custom_domain = ?", update.Domain).
		Set("custom_domain_token = ?", update.Token).
		Set("custom_domain_verified_at = ?", update.VerifiedAt).
		Set("custom_domain_checked_at = ?", update.CheckedAt).
		Set("custom_domain_failures = ?", update.Failures).
		Set("updated_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// DeleteStatusPage soft-deletes a status page.
func (s *Service) DeleteStatusPage(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.StatusPage)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// --- StatusPageSection operations ---

// CreateStatusPageSection inserts a new section.
func (s *Service) CreateStatusPageSection(ctx context.Context, section *models.StatusPageSection) error {
	_, err := s.db.NewInsert().Model(section).Exec(ctx)

	return err
}

// GetStatusPageSection retrieves a section by UID within a status page.
func (s *Service) GetStatusPageSection(
	ctx context.Context, pageUID, uid string,
) (*models.StatusPageSection, error) {
	section := new(models.StatusPageSection)

	err := s.db.NewSelect().
		Model(section).
		Where("uid = ?", uid).
		Where("status_page_uid = ?", pageUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return section, nil
}

// GetStatusPageSectionBySlug retrieves a section by slug within a status page.
func (s *Service) GetStatusPageSectionBySlug(
	ctx context.Context, pageUID, slug string,
) (*models.StatusPageSection, error) {
	section := new(models.StatusPageSection)

	err := s.db.NewSelect().
		Model(section).
		Where("slug = ?", slug).
		Where("status_page_uid = ?", pageUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return section, nil
}

// ListStatusPageSections lists all sections for a status page, ordered by position.
func (s *Service) ListStatusPageSections(
	ctx context.Context, pageUID string,
) ([]*models.StatusPageSection, error) {
	var sections []*models.StatusPageSection

	err := s.db.NewSelect().
		Model(&sections).
		Where("status_page_uid = ?", pageUID).
		Where("deleted_at IS NULL").
		Order("position ASC", "created_at ASC").
		Scan(ctx)

	return sections, err
}

// MaxStatusPageSectionPosition returns the largest position currently used by
// any non-deleted section in the given status page, or 0 if no sections exist.
// Callers add 1 to append a new section at the end.
func (s *Service) MaxStatusPageSectionPosition(
	ctx context.Context, pageUID string,
) (int, error) {
	var maxPosition int

	err := s.db.NewSelect().
		Model((*models.StatusPageSection)(nil)).
		ColumnExpr("COALESCE(MAX(position), 0)").
		Where("status_page_uid = ?", pageUID).
		Where("deleted_at IS NULL").
		Scan(ctx, &maxPosition)

	return maxPosition, err
}

// UpdateStatusPageSection updates a section by UID.
func (s *Service) UpdateStatusPageSection(
	ctx context.Context, uid string, update *models.StatusPageSectionUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.StatusPageSection)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}

	if update.Slug != nil {
		query = query.Set("slug = ?", *update.Slug)
	}

	if update.Position != nil {
		query = query.Set("position = ?", *update.Position)
	}

	_, err := query.Exec(ctx)

	return err
}

// DeleteStatusPageSection soft-deletes a section.
func (s *Service) DeleteStatusPageSection(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.StatusPageSection)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// --- StatusPageResource operations ---

// CreateStatusPageResource inserts a new resource.
func (s *Service) CreateStatusPageResource(
	ctx context.Context, resource *models.StatusPageResource,
) error {
	_, err := s.db.NewInsert().Model(resource).Exec(ctx)

	return err
}

// GetStatusPageResource retrieves a resource by UID within a section.
func (s *Service) GetStatusPageResource(
	ctx context.Context, sectionUID, uid string,
) (*models.StatusPageResource, error) {
	resource := new(models.StatusPageResource)

	err := s.db.NewSelect().
		Model(resource).
		Where("uid = ?", uid).
		Where("section_uid = ?", sectionUID).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return resource, nil
}

// MaxStatusPageResourcePosition returns the largest position currently used by
// any resource in the given section, or 0 if no resources exist. Callers add 1
// to append a new resource at the end.
func (s *Service) MaxStatusPageResourcePosition(
	ctx context.Context, sectionUID string,
) (int, error) {
	var maxPosition int

	err := s.db.NewSelect().
		Model((*models.StatusPageResource)(nil)).
		ColumnExpr("COALESCE(MAX(position), 0)").
		Where("section_uid = ?", sectionUID).
		Scan(ctx, &maxPosition)

	return maxPosition, err
}

// ReorderStatusPageResources rewrites the position of every resource in the
// section so that orderedUIDs[i] gets position i+1. Done in a single
// transaction; the caller is responsible for validating that orderedUIDs
// exactly matches the section's current resource set.
func (s *Service) ReorderStatusPageResources(
	ctx context.Context, sectionUID string, orderedUIDs []string,
) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		now := time.Now()
		for i, uid := range orderedUIDs {
			res, err := tx.NewUpdate().
				Model((*models.StatusPageResource)(nil)).
				Where("uid = ?", uid).
				Where("section_uid = ?", sectionUID).
				Set("position = ?", i+1).
				Set("updated_at = ?", now).
				Exec(ctx)
			if err != nil {
				return err
			}
			rows, _ := res.RowsAffected()
			if rows == 0 {
				return fmt.Errorf("%w: resource %q section %q", errResourceNotInSection, uid, sectionUID)
			}
		}

		return nil
	})
}

// ReorderStatusPageSections rewrites the position of every section in the
// page so that orderedUIDs[i] gets position i+1. Done in a single
// transaction; the caller is responsible for validating that orderedUIDs
// exactly matches the page's current section set.
func (s *Service) ReorderStatusPageSections(
	ctx context.Context, statusPageUID string, orderedUIDs []string,
) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		now := time.Now()
		for i, uid := range orderedUIDs {
			res, err := tx.NewUpdate().
				Model((*models.StatusPageSection)(nil)).
				Where("uid = ?", uid).
				Where("status_page_uid = ?", statusPageUID).
				Set("position = ?", i+1).
				Set("updated_at = ?", now).
				Exec(ctx)
			if err != nil {
				return err
			}
			rows, _ := res.RowsAffected()
			if rows == 0 {
				return fmt.Errorf("%w: section %q page %q", errSectionNotInPage, uid, statusPageUID)
			}
		}

		return nil
	})
}

// ListStatusPageResources lists all resources for a section, ordered by position.
func (s *Service) ListStatusPageResources(
	ctx context.Context, sectionUID string,
) ([]*models.StatusPageResource, error) {
	var resources []*models.StatusPageResource

	err := s.db.NewSelect().
		Model(&resources).
		Where("section_uid = ?", sectionUID).
		Order("position ASC", "created_at ASC").
		Scan(ctx)

	return resources, err
}

// UpdateStatusPageResource updates a resource by UID.
func (s *Service) UpdateStatusPageResource(
	ctx context.Context, uid string, update *models.StatusPageResourceUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.StatusPageResource)(nil)).
		Where("uid = ?", uid).
		Set("updated_at = ?", time.Now())

	if update.PublicName != nil {
		query = query.Set("public_name = ?", *update.PublicName)
	}

	if update.Explanation != nil {
		query = query.Set("explanation = ?", *update.Explanation)
	}

	if update.Position != nil {
		query = query.Set("position = ?", *update.Position)
	}

	// Switching target kind always writes BOTH columns so the XOR constraint
	// holds (spec 2026-08-01-03).
	if update.SetTarget {
		query = query.
			Set("check_uid = ?", update.CheckUID).
			Set("check_group_uid = ?", update.CheckGroupUID)
	}

	_, err := query.Exec(ctx)

	return err
}

// DeleteStatusPageResource hard-deletes a resource.
func (s *Service) DeleteStatusPageResource(ctx context.Context, uid string) error {
	_, err := s.db.NewDelete().
		Model((*models.StatusPageResource)(nil)).
		Where("uid = ?", uid).
		Exec(ctx)

	return err
}

// CheckGroup operations

func (s *Service) CreateCheckGroup(ctx context.Context, group *models.CheckGroup) error {
	_, err := s.db.NewInsert().Model(group).Exec(ctx)

	return err
}

func (s *Service) GetCheckGroup(ctx context.Context, orgUID, uid string) (*models.CheckGroup, error) {
	group := new(models.CheckGroup)

	err := s.db.NewSelect().
		Model(group).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return group, nil
}

func (s *Service) GetCheckGroupBySlug(ctx context.Context, orgUID, slug string) (*models.CheckGroup, error) {
	group := new(models.CheckGroup)

	err := s.db.NewSelect().
		Model(group).
		Where("slug = ?", slug).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return group, nil
}

func (s *Service) GetCheckGroupByUidOrSlug(
	ctx context.Context, orgUID, identifier string,
) (*models.CheckGroup, error) {
	if _, err := uuid.Parse(identifier); err == nil {
		return s.GetCheckGroup(ctx, orgUID, identifier)
	}

	return s.GetCheckGroupBySlug(ctx, orgUID, identifier)
}

func (s *Service) ListCheckGroups(ctx context.Context, orgUID string) ([]*models.CheckGroup, error) {
	var groups []*models.CheckGroup

	err := s.db.NewSelect().
		Model(&groups).
		ColumnExpr("check_group.*").
		ColumnExpr(`(SELECT COUNT(*) FROM checks
			WHERE checks.check_group_uid = check_group.uid
			AND checks.deleted_at IS NULL) AS check_count`).
		Where("check_group.organization_uid = ?", orgUID).
		Where("check_group.deleted_at IS NULL").
		Order("check_group.sort_order ASC", "check_group.name ASC").
		Scan(ctx)

	return groups, err
}

// GetCheckGroupStatusCounts returns, per check group, a count of enabled
// non-deleted member checks by status (spec 2026-08-01-01). One GROUP BY
// query over checks, dialect-neutral (no ILIKE, no Postgres-only syntax) so
// the same implementation works against PostgreSQL.
func (s *Service) GetCheckGroupStatusCounts(
	ctx context.Context, orgUID string,
) (map[string]map[models.CheckStatus]int, error) {
	type row struct {
		CheckGroupUID string             `bun:"check_group_uid"`
		Status        models.CheckStatus `bun:"status"`
		Count         int                `bun:"count"`
	}

	var rows []row

	err := s.db.NewSelect().
		TableExpr("checks").
		ColumnExpr("check_group_uid").
		ColumnExpr("status").
		ColumnExpr("COUNT(*) AS count").
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Where("enabled = ?", true).
		Where("check_group_uid IS NOT NULL").
		GroupExpr("check_group_uid, status").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("get check group status counts: %w", err)
	}

	counts := make(map[string]map[models.CheckStatus]int)

	for i := range rows {
		r := &rows[i]
		if counts[r.CheckGroupUID] == nil {
			counts[r.CheckGroupUID] = make(map[models.CheckStatus]int)
		}

		counts[r.CheckGroupUID][r.Status] = r.Count
	}

	return counts, nil
}

// GetCheckStatusCounts returns the org-wide (status, enabled) histogram of
// checks (spec 2026-08-02-06) as a single GROUP BY — never a load-all-and-count,
// so it stays correct and cheap past the 100-row page clamp of the list
// endpoint. Dialect-neutral, byte-identical in intent to the PostgreSQL twin.
//
// The predicate deliberately mirrors what the dashboard's checks list shows:
// non-deleted AND non-internal. Internal checks are hidden by the list
// endpoint's default `internal=false` filter, so counting them here would make
// the KPI tiles disagree with the list the user can open. Disabled checks ARE
// counted (the enabled flag is a grouping dimension, not a filter) because the
// dashboard's down/hard-down tiles filter on status alone.
func (s *Service) GetCheckStatusCounts(
	ctx context.Context, orgUID string,
) ([]models.CheckStatusCount, error) {
	var rows []models.CheckStatusCount

	err := s.db.NewSelect().
		TableExpr("checks").
		ColumnExpr("status").
		ColumnExpr("enabled").
		ColumnExpr("COUNT(*) AS count").
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Where("internal = ?", false).
		GroupExpr("status, enabled").
		Scan(ctx, &rows)
	if err != nil {
		return nil, fmt.Errorf("get check status counts: %w", err)
	}

	return rows, nil
}

// ListCheckUIDsByGroup returns the UIDs of the group's enabled, non-deleted
// member checks — deliberately the same member predicate as
// GetCheckGroupStatusCounts so a group's rolled-up status and its aggregated
// availability always describe the same set of checks (spec 2026-08-01-03).
func (s *Service) ListCheckUIDsByGroup(
	ctx context.Context, orgUID, groupUID string,
) ([]string, error) {
	var uids []string

	err := s.db.NewSelect().
		TableExpr("checks").
		ColumnExpr("uid").
		Where("organization_uid = ?", orgUID).
		Where("check_group_uid = ?", groupUID).
		Where("deleted_at IS NULL").
		Where("enabled = ?", true).
		Scan(ctx, &uids)
	if err != nil {
		return nil, fmt.Errorf("list check uids by group: %w", err)
	}

	return uids, nil
}

func (s *Service) UpdateCheckGroup(
	ctx context.Context, orgUID, uid string, update *models.CheckGroupUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.CheckGroup)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Name != nil {
		query = query.Set("name = ?", *update.Name)
	}

	if update.Slug != nil {
		query = query.Set("slug = ?", *update.Slug)
	}

	if update.Description != nil {
		query = query.Set("description = ?", *update.Description)
	}

	if update.SortOrder != nil {
		query = query.Set("sort_order = ?", *update.SortOrder)
	}

	switch {
	case update.ClearEscalationPolicyUID:
		query = query.Set("escalation_policy_uid = NULL")
	case update.EscalationPolicyUID != nil:
		query = query.Set("escalation_policy_uid = ?", *update.EscalationPolicyUID)
	}

	if _, err := query.Exec(ctx); err != nil {
		return err
	}

	// If sort_order was changed, normalize all groups in the org with gap of 2
	if update.SortOrder != nil {
		return s.normalizeCheckGroupSortOrder(ctx, orgUID)
	}

	return nil
}

// normalizeCheckGroupSortOrder reassigns sort_order values with a gap of 2 for all groups in an org.
func (s *Service) normalizeCheckGroupSortOrder(ctx context.Context, orgUID string) error {
	var groups []*models.CheckGroup

	err := s.db.NewSelect().
		Model(&groups).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("sort_order ASC", "name ASC").
		Scan(ctx)
	if err != nil {
		return err
	}

	for i, group := range groups {
		newOrder := int16(i * 2)
		if group.SortOrder != newOrder {
			_, err = s.db.NewUpdate().
				Model((*models.CheckGroup)(nil)).
				Where("uid = ?", group.UID).
				Set("sort_order = ?", newOrder).
				Exec(ctx)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *Service) DeleteCheckGroup(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.CheckGroup)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// CreateMaintenanceWindow inserts a new maintenance window.
func (s *Service) CreateMaintenanceWindow(ctx context.Context, window *models.MaintenanceWindow) error {
	_, err := s.db.NewInsert().Model(window).Exec(ctx)

	return err
}

// GetMaintenanceWindow retrieves a maintenance window by UID within an organization.
func (s *Service) GetMaintenanceWindow(
	ctx context.Context, orgUID, uid string,
) (*models.MaintenanceWindow, error) {
	window := new(models.MaintenanceWindow)

	err := s.db.NewSelect().
		Model(window).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return window, nil
}

// ListMaintenanceWindows lists maintenance windows for an organization with optional filtering.
func (s *Service) ListMaintenanceWindows(
	ctx context.Context, orgUID string, filter models.ListMaintenanceWindowsFilter,
) ([]*models.MaintenanceWindow, error) {
	var windows []*models.MaintenanceWindow

	query := s.db.NewSelect().
		Model(&windows).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("start_at DESC")

	now := time.Now()

	switch filter.Status {
	case "active":
		query = query.
			Where("start_at <= ?", now).
			Where("end_at > ?", now)
	case "upcoming":
		query = query.Where("start_at > ?", now)
	case "past":
		query = query.Where("end_at <= ?", now)
	}

	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}

	err := query.Scan(ctx)

	return windows, err
}

// UpdateMaintenanceWindow updates a maintenance window by UID.
func (s *Service) UpdateMaintenanceWindow(
	ctx context.Context, uid string, update models.MaintenanceWindowUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.MaintenanceWindow)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Title != nil {
		query = query.Set("title = ?", *update.Title)
	}

	if update.Description != nil {
		query = query.Set("description = ?", *update.Description)
	}

	if update.StartAt != nil {
		query = query.Set("start_at = ?", *update.StartAt)
	}

	if update.EndAt != nil {
		query = query.Set("end_at = ?", *update.EndAt)
	}

	if update.Recurrence != nil {
		query = query.Set("recurrence = ?", *update.Recurrence)
	}

	if update.RecurrenceEnd != nil {
		query = query.Set("recurrence_end = ?", *update.RecurrenceEnd)
	}

	_, err := query.Exec(ctx)

	return err
}

// DeleteMaintenanceWindow soft-deletes a maintenance window.
func (s *Service) DeleteMaintenanceWindow(ctx context.Context, orgUID, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.MaintenanceWindow)(nil)).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
}

// SetMaintenanceWindowChecks replaces the check associations for a maintenance window.
func (s *Service) SetMaintenanceWindowChecks(
	ctx context.Context, windowUID string, checkUIDs, checkGroupUIDs []string,
) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Delete existing associations
		_, err := tx.NewDelete().
			Model((*models.MaintenanceWindowCheck)(nil)).
			Where("maintenance_window_uid = ?", windowUID).
			Exec(ctx)
		if err != nil {
			return err
		}

		// Insert new check associations
		for _, checkUID := range checkUIDs {
			assoc := &models.MaintenanceWindowCheck{
				UID:                  uuid.New().String(),
				MaintenanceWindowUID: windowUID,
				CheckUID:             &checkUID,
				CreatedAt:            time.Now(),
			}
			if _, err := tx.NewInsert().Model(assoc).Exec(ctx); err != nil {
				return err
			}
		}

		// Insert new check group associations
		for _, groupUID := range checkGroupUIDs {
			assoc := &models.MaintenanceWindowCheck{
				UID:                  uuid.New().String(),
				MaintenanceWindowUID: windowUID,
				CheckGroupUID:        &groupUID,
				CreatedAt:            time.Now(),
			}
			if _, err := tx.NewInsert().Model(assoc).Exec(ctx); err != nil {
				return err
			}
		}

		return nil
	})
}

// ListMaintenanceWindowChecks lists all check associations for a maintenance window.
func (s *Service) ListMaintenanceWindowChecks(
	ctx context.Context, windowUID string,
) ([]*models.MaintenanceWindowCheck, error) {
	var checks []*models.MaintenanceWindowCheck

	err := s.db.NewSelect().
		Model(&checks).
		Where("maintenance_window_uid = ?", windowUID).
		Order("created_at ASC").
		Scan(ctx)

	return checks, err
}

// ListMaintenanceWindowsForCheck returns every non-deleted maintenance window
// linked to the check directly or via its group. It does not filter by start
// time or evaluate recurrence — callers decide active/inactive via
// models.IsActiveAt so an in-process TTL cache can re-evaluate the same rows at
// a later clock (including windows that only become active after the fetch).
func (s *Service) ListMaintenanceWindowsForCheck(
	ctx context.Context, checkUID string,
) ([]*models.MaintenanceWindow, error) {
	var windows []*models.MaintenanceWindow

	err := s.db.NewSelect().
		Model(&windows).
		Where("deleted_at IS NULL").
		Where(`uid IN (
			SELECT mwc.maintenance_window_uid FROM maintenance_window_checks mwc
			WHERE mwc.check_uid = ?
			UNION
			SELECT mwc.maintenance_window_uid FROM maintenance_window_checks mwc
			JOIN checks c ON c.check_group_uid = mwc.check_group_uid
			WHERE c.uid = ? AND c.check_group_uid IS NOT NULL
		)`, checkUID, checkUID).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return windows, nil
}

// ListMaintenanceWindowsForCheckGroup returns every non-deleted maintenance
// window that puts the group in maintenance: one targeting the group directly,
// or one targeting any of its enabled, non-deleted member checks (spec
// 2026-08-01-03). Recurrence is not evaluated here — callers use
// models.IsActiveAt.
func (s *Service) ListMaintenanceWindowsForCheckGroup(
	ctx context.Context, groupUID string,
) ([]*models.MaintenanceWindow, error) {
	var windows []*models.MaintenanceWindow

	err := s.db.NewSelect().
		Model(&windows).
		Where("deleted_at IS NULL").
		Where(`uid IN (
			SELECT mwc.maintenance_window_uid FROM maintenance_window_checks mwc
			WHERE mwc.check_group_uid = ?
			UNION
			SELECT mwc.maintenance_window_uid FROM maintenance_window_checks mwc
			JOIN checks c ON c.uid = mwc.check_uid
			WHERE c.check_group_uid = ? AND c.deleted_at IS NULL AND c.enabled = ?
		)`, groupUID, groupUID, true).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return windows, nil
}

// CreateFile inserts a new file row.
func (s *Service) CreateFile(ctx context.Context, file *models.File) error {
	_, err := s.db.NewInsert().Model(file).Exec(ctx)

	return err
}

// GetFile retrieves a file by UID for an organization, excluding soft-deleted rows.
func (s *Service) GetFile(ctx context.Context, orgUID, uid string) (*models.File, error) {
	file := new(models.File)

	err := s.db.NewSelect().
		Model(file).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return file, nil
}

// GetFileAny retrieves a file by UID without org scoping. Used by the public
// signed-URL handler — the signature already proves authorization.
func (s *Service) GetFileAny(ctx context.Context, uid string) (*models.File, error) {
	file := new(models.File)

	err := s.db.NewSelect().
		Model(file).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return file, nil
}

// ListFiles returns files for an organization with optional substring search on name.
func (s *Service) ListFiles(
	ctx context.Context, orgUID string, filter models.ListFilesFilter,
) ([]*models.File, int64, error) {
	var files []*models.File

	query := s.db.NewSelect().
		Model(&files).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC")

	if filter.Q != "" {
		query = query.Where("name LIKE ?", "%"+filter.Q+"%")
	}

	total, err := query.Count(ctx)
	if err != nil {
		return nil, 0, err
	}

	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}

	if filter.Offset > 0 {
		query = query.Offset(filter.Offset)
	}

	err = query.Scan(ctx)

	return files, int64(total), err
}

// DeleteFile soft-deletes a file by UID, scoped to org.
func (s *Service) DeleteFile(ctx context.Context, orgUID, uid string) error {
	res, err := s.db.NewUpdate().
		Model((*models.File)(nil)).
		Where("uid = ?", uid).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)
	if err != nil {
		return err
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}

	if rows == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// CreateMembershipRequest inserts a new pending request.
func (s *Service) CreateMembershipRequest(ctx context.Context, request *models.MembershipRequest) error {
	_, err := s.db.NewInsert().Model(request).Exec(ctx)

	return err
}

// UpdateMembershipRequest persists status / decision changes.
func (s *Service) UpdateMembershipRequest(
	ctx context.Context, request *models.MembershipRequest,
) error {
	request.UpdatedAt = time.Now()

	_, err := s.db.NewUpdate().
		Model(request).
		WherePK().
		Exec(ctx)

	return err
}

// GetMembershipRequest fetches a request by UID with relations.
func (s *Service) GetMembershipRequest(
	ctx context.Context, uid string,
) (*models.MembershipRequest, error) {
	request := new(models.MembershipRequest)

	err := s.db.NewSelect().
		Model(request).
		Relation("Organization").
		Relation("User").
		Where("membership_request.uid = ?", uid).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return request, nil
}

// GetMembershipRequestByOrgAndUser returns the (org,user) row if any.
func (s *Service) GetMembershipRequestByOrgAndUser(
	ctx context.Context, orgUID, userUID string,
) (*models.MembershipRequest, error) {
	request := new(models.MembershipRequest)

	err := s.db.NewSelect().
		Model(request).
		Where("organization_uid = ?", orgUID).
		Where("user_uid = ?", userUID).
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	return request, nil
}

// ListMembershipRequests returns requests matching the filter, ordered by
// most recently created first.
func (s *Service) ListMembershipRequests(
	ctx context.Context, filter models.ListMembershipRequestsFilter,
) ([]*models.MembershipRequest, error) {
	var requests []*models.MembershipRequest

	query := s.db.NewSelect().
		Model(&requests).
		Relation("Organization").
		Relation("User").
		Order("membership_request.created_at DESC")

	if filter.OrganizationUID != "" {
		query = query.Where("membership_request.organization_uid = ?", filter.OrganizationUID)
	}
	if filter.UserUID != "" {
		query = query.Where("membership_request.user_uid = ?", filter.UserUID)
	}
	if filter.Status != "" {
		query = query.Where("membership_request.status = ?", filter.Status)
	}
	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		query = query.Offset(filter.Offset)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, err
	}

	return requests, nil
}

// ApproveMembershipRequest commits the request status change AND the new
// membership row in a single transaction.
func (s *Service) ApproveMembershipRequest(
	ctx context.Context,
	request *models.MembershipRequest,
	member *models.OrganizationMember,
) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		request.UpdatedAt = time.Now()

		if _, err := tx.NewUpdate().Model(request).WherePK().Exec(ctx); err != nil {
			return err
		}

		if _, err := tx.NewInsert().Model(member).Exec(ctx); err != nil {
			return err
		}

		return nil
	})
}

// GetOrgEntitlements fetches the entitlement row for an org. Returns
// (nil, nil) when no row exists — the resolver merges defaults instead
// of erroring.
func (s *Service) GetOrgEntitlements(
	ctx context.Context, orgUID string,
) (*models.OrgEntitlements, error) {
	row := new(models.OrgEntitlements)

	err := s.db.NewSelect().
		Model(row).
		Where("organization_uid = ?", orgUID).
		Scan(ctx)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // documented sentinel
		}

		return nil, err
	}

	return row, nil
}

// UpsertOrgEntitlements writes the entitlement row + audit row in one tx.
// The caller pre-populates the audit's BeforeSnapshot from a previous
// GetOrgEntitlements call.
func (s *Service) UpsertOrgEntitlements(
	ctx context.Context, ent *models.OrgEntitlements, audit *models.OrgEntitlementAudit,
) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		ent.UpdatedAt = time.Now()

		_, err := tx.NewInsert().
			Model(ent).
			On("CONFLICT (organization_uid) DO UPDATE").
			Set("payload = EXCLUDED.payload").
			Set("external_ref = EXCLUDED.external_ref").
			Set("expires_at = EXCLUDED.expires_at").
			Set("last_synced_at = EXCLUDED.last_synced_at").
			Set("metadata = EXCLUDED.metadata").
			Set("updated_at = EXCLUDED.updated_at").
			Exec(ctx)
		if err != nil {
			return err
		}

		if _, err := tx.NewInsert().Model(audit).Exec(ctx); err != nil {
			return err
		}

		return nil
	})
}

// ListOrgEntitlementAudits returns audit rows for an org, newest first.
func (s *Service) ListOrgEntitlementAudits(
	ctx context.Context, filter models.ListOrgEntitlementAuditsFilter,
) ([]*models.OrgEntitlementAudit, error) {
	var rows []*models.OrgEntitlementAudit

	query := s.db.NewSelect().
		Model(&rows).
		Order("org_entitlement_audit.created_at DESC")

	if filter.OrganizationUID != "" {
		query = query.Where("organization_uid = ?", filter.OrganizationUID)
	}
	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}
	if filter.Offset > 0 {
		query = query.Offset(filter.Offset)
	}

	if err := query.Scan(ctx); err != nil {
		return nil, err
	}

	return rows, nil
}

// CountMembersForOrg counts every organization member, regardless of how
// they joined (SSO, invitation, email). Used by the entitlements service
// to enforce MaxUsers at every membership-creation path.
func (s *Service) CountMembersForOrg(ctx context.Context, orgUID string) (int, error) {
	var count int

	err := s.db.NewSelect().
		Table("organization_members").
		ColumnExpr("COUNT(DISTINCT organization_members.user_uid)").
		Where("organization_members.organization_uid = ?", orgUID).
		Scan(ctx, &count)

	return count, err
}

// ListOrgCheckRates returns (enabled, period) for all non-deleted,
// non-internal checks of the given org. Used by the entitlements service
// to compute usage stats and enforce MaxChecks. No SQL arithmetic — the
// per-minute rate is summed in Go.
func (s *Service) ListOrgCheckRates(ctx context.Context, orgUID string) ([]models.CheckRate, error) {
	var rates []models.CheckRate

	err := s.db.NewSelect().
		Model((*models.Check)(nil)).
		Column("enabled", "period", "regions").
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Where("internal = ?", false).
		Scan(ctx, &rates)
	if err != nil {
		return nil, fmt.Errorf("list org check rates: %w", err)
	}

	return rates, nil
}

// ListChecksWithStaleJobPeriods returns enabled, non-deleted checks that have
// at least one check_job whose period no longer matches the check's own period
// (spec 2026-07-20-05). Byte-for-byte the postgres twin's shape so the startup
// reconcile behaves identically on both backends; period is the canonical
// HH:MM:SS text, so a split-period job never text-equals its check's period.
func (s *Service) ListChecksWithStaleJobPeriods(ctx context.Context) ([]*models.Check, error) {
	var uids []string

	if err := s.db.NewSelect().
		ColumnExpr("DISTINCT cj.check_uid").
		TableExpr("check_jobs AS cj").
		Join("JOIN checks AS c ON c.uid = cj.check_uid").
		Where("c.deleted_at IS NULL").
		Where("c.enabled = ?", true).
		Where("cj.period <> c.period").
		Scan(ctx, &uids); err != nil {
		return nil, fmt.Errorf("list stale check job periods: %w", err)
	}

	if len(uids) == 0 {
		return nil, nil
	}

	var checks []*models.Check
	if err := s.db.NewSelect().
		Model(&checks).
		Where("uid IN (?)", bun.List(uids)).
		Where("deleted_at IS NULL").
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("load stale checks: %w", err)
	}

	return checks, nil
}

// ListPublicStatusUpdates is implemented in status_update.go.
