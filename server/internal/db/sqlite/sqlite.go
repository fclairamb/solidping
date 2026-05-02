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
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sloghook"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

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

	// Build connection string with SQLite parameters
	connStr := dbPath
	if !cfg.InMemory {
		// Only add parameters for file-based databases (not :memory:)
		connStr = fmt.Sprintf("file:%s?cache=shared&mode=rwc&_journal_mode=WAL&_busy_timeout=30000", dbPath)
	}

	sqldb, err := sql.Open(sqliteshim.ShimName, connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Configure connection pool for SQLite's single-writer model
	sqldb.SetMaxOpenConns(1)    // SQLite performs better with a single writer
	sqldb.SetMaxIdleConns(1)    // Keep one connection alive
	sqldb.SetConnMaxLifetime(0) // No connection expiration

	// Enable foreign keys
	if _, err := sqldb.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
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

	// Add SQL logging hook if enabled
	if cfg.LogSQL {
		bunDB.AddQueryHook(sloghook.New(true))
	}

	return &Service{
		db:      bunDB,
		dataDir: cfg.DataDir,
	}, nil
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

func (s *Service) CreateOrganization(ctx context.Context, org *models.Organization) error {
	_, err := s.db.NewInsert().Model(org).Exec(ctx)
	return err
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

	_, err := query.Exec(ctx)

	return err
}

func (s *Service) DeleteOrganization(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.Organization)(nil)).
		Where("uid = ?", uid).
		Set("deleted_at = ?", time.Now()).
		Exec(ctx)

	return err
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

func (s *Service) CountAdminsByOrg(ctx context.Context, orgUID string) (int, error) {
	count, err := s.db.NewSelect().
		Model((*models.OrganizationMember)(nil)).
		Where("organization_uid = ?", orgUID).
		Where("role = ?", models.MemberRoleAdmin).
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

func (s *Service) DeleteUserToken(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.UserToken)(nil)).
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
		lastForStatus := true
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
			LastForStatus:   &lastForStatus,
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
		checkJob.ScheduledAt = &now
		if _, err := tx.NewInsert().Model(checkJob).Exec(ctx); err != nil {
			return err
		}

		return nil
	}

	// Multi-region: create one job per region with period splitting
	n := len(check.Regions)
	splitPeriod := timeutils.Duration(basePeriod * time.Duration(n))

	for i, region := range check.Regions {
		scheduledAt := now.Add(basePeriod * time.Duration(i))
		regionCopy := region

		checkJob := models.NewCheckJob(check.OrganizationUID, check.UID, splitPeriod)
		checkJob.Type = check.Type
		checkJob.Config = check.Config
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

func (s *Service) ListChecks(
	ctx context.Context, orgUID string, filter *models.ListChecksFilter,
) ([]*models.Check, int64, error) {
	var checks []*models.Check

	query := s.db.NewSelect().
		Model(&checks).
		Where("organization_uid = ?", orgUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC", "uid DESC")

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

		// Apply cursor
		if filter.CursorCreatedAt != nil && filter.CursorUID != nil {
			query = query.Where(
				"(created_at < ? OR (created_at = ? AND uid < ?))",
				*filter.CursorCreatedAt, *filter.CursorCreatedAt, *filter.CursorUID,
			)
		}

		// Apply limit
		if filter.Limit > 0 {
			query = query.Limit(filter.Limit + 1)
		}
	}

	total, err := countQuery.Count(ctx)
	if err != nil {
		return nil, 0, err
	}

	err = query.Scan(ctx)

	return checks, int64(total), err
}

func (s *Service) UpdateCheck(ctx context.Context, uid string, update *models.CheckUpdate) error {
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

	if update.ReopenCooldownMultiplier != nil {
		query = query.Set("reopen_cooldown_multiplier = ?", *update.ReopenCooldownMultiplier)
	}

	if update.MaxAdaptiveIncrease != nil {
		query = query.Set("max_adaptive_increase = ?", *update.MaxAdaptiveIncrease)
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

// Result operations

func (s *Service) CreateResult(ctx context.Context, result *models.Result) error {
	_, err := s.db.NewInsert().Model(result).Exec(ctx)
	return err
}

func (s *Service) SaveResultWithStatusTracking(ctx context.Context, result *models.Result) error {
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Clear previous last_for_status for this check+status combination
		_, err := tx.NewUpdate().
			Model((*models.Result)(nil)).
			Set("last_for_status = NULL").
			Where("check_uid = ?", result.CheckUID).
			Where("status = ?", result.Status).
			Where("last_for_status = true").
			Exec(ctx)
		if err != nil {
			return err
		}

		// Insert new result with last_for_status = true
		_, err = tx.NewInsert().Model(result).Exec(ctx)
		return err
	})
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

	query := s.db.NewSelect().
		Model(&results).
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

	err := query.Scan(ctx)
	if err != nil {
		return nil, err
	}

	// For now, we don't calculate total count as it's expensive
	// It can be added later as an optional feature
	return &models.ListResultsResponse{
		Results: results,
		Total:   0,
	}, nil
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

func (s *Service) GetLastResultForChecks(ctx context.Context, checkUIDs []string) (map[string]*models.Result, error) {
	if len(checkUIDs) == 0 {
		return make(map[string]*models.Result), nil
	}

	var results []*models.Result

	// For SQLite, we need to use a subquery to get the latest result per check
	err := s.db.NewSelect().
		Model(&results).
		Where("check_uid IN (?)", bun.List(checkUIDs)).
		Where("period_type = ?", "raw").
		Order("check_uid", "period_start DESC").
		Scan(ctx)
	if err != nil {
		return nil, err
	}

	// Convert to map for easy lookup, keeping only the first (latest) result per check
	resultMap := make(map[string]*models.Result)
	for _, result := range results {
		if _, exists := resultMap[result.CheckUID]; !exists {
			resultMap[result.CheckUID] = result
		}
	}

	return resultMap, nil
}

//nolint:lll // Function signature clarity
func (s *Service) GetLastStatusChangeForChecks(ctx context.Context, checkUIDs []string) (map[string]*models.LastStatusChange, error) {
	if len(checkUIDs) == 0 {
		return make(map[string]*models.LastStatusChange), nil
	}

	// Query to find the last status change for each check
	// A status change is when the status transitions from one value to another
	type statusChangeResult struct {
		CheckUID    string    `bun:"check_uid"`
		PeriodStart time.Time `bun:"period_start"`
		Status      int       `bun:"status"`
	}

	var results []statusChangeResult

	// Use a CTE with LAG to find status transitions
	// SQLite supports window functions since version 3.25.0
	query := `
		WITH status_changes AS (
			SELECT
				check_uid,
				period_start,
				status,
				LAG(status) OVER (PARTITION BY check_uid ORDER BY period_start) AS prev_status
			FROM results
			WHERE check_uid IN (?)
				AND period_type = 'raw'
				AND status IS NOT NULL
		),
		last_changes AS (
			SELECT
				check_uid,
				period_start,
				status,
				ROW_NUMBER() OVER (PARTITION BY check_uid ORDER BY period_start DESC) AS rn
			FROM status_changes
			WHERE status != prev_status OR prev_status IS NULL
		)
		SELECT check_uid, period_start, status
		FROM last_changes
		WHERE rn = 1
	`

	err := s.db.NewRaw(query, bun.List(checkUIDs)).Scan(ctx, &results)
	if err != nil {
		return nil, err
	}

	// Convert to map with LastStatusChange struct
	resultMap := make(map[string]*models.LastStatusChange)
	for i := range results {
		resultMap[results[i].CheckUID] = &models.LastStatusChange{
			Time:   results[i].PeriodStart,
			Status: models.StatusToString(results[i].Status),
		}
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

// Incident operations

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
	return query
}

func (s *Service) CreateIncident(ctx context.Context, incident *models.Incident) error {
	_, err := s.db.NewInsert().Model(incident).Exec(ctx)
	return err
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

func (s *Service) ListIncidents(ctx context.Context, filter *models.ListIncidentsFilter) ([]*models.Incident, error) {
	var incidents []*models.Incident

	query := s.db.NewSelect().
		Model(&incidents).
		Where("organization_uid = ?", filter.OrganizationUID).
		Where("deleted_at IS NULL").
		Order("started_at DESC")

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

	err := query.Scan(ctx)

	return incidents, err
}

func (s *Service) UpdateIncident(ctx context.Context, uid string, update *models.IncidentUpdate) error {
	query := s.db.NewUpdate().
		Model((*models.Incident)(nil)).
		Where("uid = ?", uid).
		Where("deleted_at IS NULL").
		Set("updated_at = ?", time.Now())

	if update.Region != nil {
		query = query.Set("region = ?", *update.Region)
	}

	if update.State != nil {
		query = query.Set("state = ?", *update.State)
	}

	if update.ResolvedAt != nil {
		query = query.Set("resolved_at = ?", *update.ResolvedAt)
	}

	if update.ResolvedBy != nil {
		query = query.Set("resolved_by = ?", *update.ResolvedBy)
	}

	if update.ResolutionType != nil {
		query = query.Set("resolution_type = ?", *update.ResolutionType)
	}

	if update.EscalatedAt != nil {
		query = query.Set("escalated_at = ?", *update.EscalatedAt)
	}

	if update.AcknowledgedAt != nil {
		query = query.Set("acknowledged_at = ?", *update.AcknowledgedAt)
	}

	if update.AcknowledgedBy != nil {
		query = query.Set("acknowledged_by = ?", *update.AcknowledgedBy)
	}

	if update.SnoozedUntil != nil {
		query = query.Set("snoozed_until = ?", *update.SnoozedUntil)
	}

	if update.SnoozedBy != nil {
		query = query.Set("snoozed_by = ?", *update.SnoozedBy)
	}

	if update.SnoozeReason != nil {
		query = query.Set("snooze_reason = ?", *update.SnoozeReason)
	}

	if update.FailureCount != nil {
		query = query.Set("failure_count = ?", *update.FailureCount)
	}

	if update.RelapseCount != nil {
		query = query.Set("relapse_count = ?", *update.RelapseCount)
	}

	if update.LastReopenedAt != nil {
		query = query.Set("last_reopened_at = ?", *update.LastReopenedAt)
	}

	if update.Title != nil {
		query = query.Set("title = ?", *update.Title)
	}

	if update.Description != nil {
		query = query.Set("description = ?", *update.Description)
	}

	if update.Details != nil {
		query = query.Set("details = ?", *update.Details)
	}

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

func (s *Service) UpdateCheckStatus(
	ctx context.Context, checkUID string, status models.CheckStatus, streak int, changedAt *time.Time,
) error {
	query := s.db.NewUpdate().
		Model((*models.Check)(nil)).
		Where("uid = ?", checkUID).
		Where("deleted_at IS NULL").
		Set("status = ?", status).
		Set("status_streak = ?", streak).
		Set("updated_at = ?", time.Now())

	if changedAt != nil {
		query = query.Set("status_changed_at = ?", *changedAt)
	}

	_, err := query.Exec(ctx)

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
func (s *Service) SetStateEntry(
	ctx context.Context, orgUID *string, key string, value *models.JSONMap, ttl *time.Duration,
) error {
	now := time.Now()
	entry := models.NewStateEntry(orgUID, key)
	entry.Value = value

	if ttl != nil {
		expiresAt := now.Add(*ttl)
		entry.ExpiresAt = &expiresAt
	}

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

// DeleteStateEntry soft-deletes a state entry.
func (s *Service) DeleteStateEntry(ctx context.Context, orgUID *string, key string) error {
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

	_, err := query.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to delete state entry: %w", err)
	}

	return nil
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
	jsonValue := models.JSONMap{"value": value}

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
	jsonValue := models.JSONMap{"value": value}

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

// CreateIntegrationConnection creates a new integration connection.
func (s *Service) CreateIntegrationConnection(ctx context.Context, conn *models.IntegrationConnection) error {
	_, err := s.db.NewInsert().Model(conn).Exec(ctx)
	return err
}

// GetIntegrationConnection retrieves an integration connection by UID.
func (s *Service) GetIntegrationConnection(ctx context.Context, uid string) (*models.IntegrationConnection, error) {
	conn := new(models.IntegrationConnection)

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

// GetIntegrationConnectionByProperty retrieves a connection by a settings property.
func (s *Service) GetIntegrationConnectionByProperty(
	ctx context.Context, connType, propertyName, propertyValue string,
) (*models.IntegrationConnection, error) {
	conn := new(models.IntegrationConnection)

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

// ListIntegrationConnections lists integration connections with optional filtering.
func (s *Service) ListIntegrationConnections(
	ctx context.Context, filter *models.ListIntegrationConnectionsFilter,
) ([]*models.IntegrationConnection, error) {
	var connections []*models.IntegrationConnection

	query := s.db.NewSelect().
		Model(&connections).
		Where("organization_uid = ?", filter.OrganizationUID).
		Where("deleted_at IS NULL").
		Order("created_at DESC")

	if filter.Type != nil {
		query = query.Where("type = ?", *filter.Type)
	}

	if filter.Enabled != nil {
		query = query.Where("enabled = ?", *filter.Enabled)
	}

	err := query.Scan(ctx)

	return connections, err
}

// UpdateIntegrationConnection updates an integration connection.
func (s *Service) UpdateIntegrationConnection(
	ctx context.Context, uid string, update *models.IntegrationConnectionUpdate,
) error {
	query := s.db.NewUpdate().
		Model((*models.IntegrationConnection)(nil)).
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

	_, err := query.Exec(ctx)

	return err
}

// DeleteIntegrationConnection soft-deletes an integration connection.
func (s *Service) DeleteIntegrationConnection(ctx context.Context, uid string) error {
	_, err := s.db.NewUpdate().
		Model((*models.IntegrationConnection)(nil)).
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
		Where("connection_uid = ?", connectionUID).
		Exec(ctx)

	return err
}

// ListConnectionsForCheck returns all connections associated with a check.
func (s *Service) ListConnectionsForCheck(
	ctx context.Context, checkUID string,
) ([]*models.IntegrationConnection, error) {
	var connections []*models.IntegrationConnection

	err := s.db.NewSelect().
		Model(&connections).
		Join("INNER JOIN check_connections cc ON cc.connection_uid = integration_connection.uid").
		Where("cc.check_uid = ?", checkUID).
		Where("integration_connection.deleted_at IS NULL").
		Order("integration_connection.created_at DESC").
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

// ListDefaultConnections returns all default connections for an organization.
func (s *Service) ListDefaultConnections(
	ctx context.Context, orgUID string,
) ([]*models.IntegrationConnection, error) {
	var connections []*models.IntegrationConnection

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
		Where("connection_uid = ?", connectionUID).
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
		Where("connection_uid = ?", connectionUID).
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

	if update.Language != nil {
		query = query.Set("language = ?", *update.Language)
	}

	_, err := query.Exec(ctx)

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

// IsCheckInActiveMaintenance checks if a check is currently in an active maintenance window.
// It checks both direct check associations and check group associations.
func (s *Service) IsCheckInActiveMaintenance(ctx context.Context, checkUID string) (bool, error) {
	now := time.Now()

	// Find all maintenance windows linked to this check (directly or via group)
	var windows []*models.MaintenanceWindow

	err := s.db.NewSelect().
		Model(&windows).
		Where("deleted_at IS NULL").
		Where("start_at <= ?", now).
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
		return false, err
	}

	for _, window := range windows {
		if models.IsActiveAt(window, now) {
			return true, nil
		}
	}

	return false, nil
}
