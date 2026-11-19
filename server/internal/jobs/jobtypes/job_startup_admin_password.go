package jobtypes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// seedPasswordSource says where the seeded super admin's first password comes
// from (spec 2026-11-19-01).
type seedPasswordSource int

const (
	// seedPasswordConfigured is auth.admin_password (SP_ADMIN_PASSWORD).
	seedPasswordConfigured seedPasswordSource = iota
	// seedPasswordDefault is defaults.Password, kept by the test and demo
	// run modes so the E2E suites and the public demo log in with it.
	seedPasswordDefault
	// seedPasswordGenerated is a random password, printed once at seed time.
	seedPasswordGenerated
)

// Run modes that keep the public defaults.Password for the seeded admin.
const (
	runModeTest = "test"
	runModeDemo = "demo"
)

// seedAdminPassword picks the first password of the seeded super admin:
// auth.admin_password when set; defaults.Password in the test and demo run
// modes; a random one otherwise, the production default, so a fresh install
// reachable from the internet has no public credential pair.
func seedAdminPassword(cfg *config.Config) (string, seedPasswordSource, error) {
	if cfg != nil {
		if cfg.Auth.AdminPassword != "" {
			return cfg.Auth.AdminPassword, seedPasswordConfigured, nil
		}

		if cfg.RunMode == runModeTest || cfg.RunMode == runModeDemo {
			return defaults.Password, seedPasswordDefault, nil
		}
	}

	generated, err := passwords.Generate(passwords.GeneratedLength)
	if err != nil {
		return "", seedPasswordGenerated, fmt.Errorf("failed to generate admin password: %w", err)
	}

	return generated, seedPasswordGenerated, nil
}

// resetSeedAdminPassword is the recovery path for a lost first-boot password:
// while the seeded super admin still carries must_change_password, a boot with
// auth.admin_password set re-hashes that one account's password to it. It
// never touches another account, and never runs once the password was rotated
// (the forced change clears the flag).
func (r *StartupJobRun) resetSeedAdminPassword(ctx context.Context, jctx *jobdef.JobContext) error {
	if jctx.AppConfig == nil || jctx.AppConfig.Auth.AdminPassword == "" {
		return nil
	}

	log := jctx.Logger
	email := seedAdminEmail(jctx.AppConfig)

	user, err := jctx.DBService.GetUserByEmail(ctx, email)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && user == nil) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to look up seeded admin: %w", err)
	}

	if !user.MustChangePassword || !user.SuperAdmin {
		return nil
	}

	password := jctx.AppConfig.Auth.AdminPassword
	if user.PasswordHash != nil && passwords.Verify(password, *user.PasswordHash) {
		return nil
	}

	hash, err := passwords.HashBcrypt(password)
	if err != nil {
		return fmt.Errorf("failed to hash admin password: %w", err)
	}

	if err := jctx.DBService.UpdateUser(ctx, user.UID, &models.UserUpdate{PasswordHash: &hash}); err != nil {
		return fmt.Errorf("failed to reset seeded admin password: %w", err)
	}

	log.WarnContext(ctx, "Reset the seeded admin password to SP_ADMIN_PASSWORD, "+
		"you will be asked to change it at first login", "email", email)

	return nil
}
