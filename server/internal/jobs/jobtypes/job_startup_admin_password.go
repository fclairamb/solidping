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
	"github.com/fclairamb/solidping/server/internal/systemconfig"
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
		return r.breakGlassAdmin(ctx, jctx, email)
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

// paramOIDCOnlyBootstrap marks an instance whose first boot seeded no
// password admin (spec 2026-11-21-02). Only such an instance gets the
// break-glass admin of breakGlassAdmin.
const paramOIDCOnlyBootstrap = "auth.oidc_only_bootstrap"

// breakGlassAdmin is the documented way back into an OIDC-only instance
// whose IdP is gone (spec 2026-11-21-02): restarted with local login on and
// SP_ADMIN_PASSWORD set, an instance whose first boot seeded no admin gets
// that admin now, a super admin with the password to rotate at first login.
// It never runs on an instance that seeded its admin at first boot.
func (r *StartupJobRun) breakGlassAdmin(ctx context.Context, jctx *jobdef.JobContext, email string) error {
	param, err := jctx.DBService.GetSystemParameter(ctx, paramOIDCOnlyBootstrap)
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", paramOIDCOnlyBootstrap, err)
	}

	if param == nil || param.Value["value"] != true {
		return nil
	}

	if !systemconfig.ResolveLocalLogin(ctx, jctx.DBService, jctx.AppConfig).Enabled {
		return nil
	}

	hash, err := passwords.HashBcrypt(jctx.AppConfig.Auth.AdminPassword)
	if err != nil {
		return fmt.Errorf("failed to hash admin password: %w", err)
	}

	admin := models.NewUser(email)
	admin.PasswordHash = &hash
	admin.SuperAdmin = true
	admin.MustChangePassword = true

	if err := jctx.DBService.CreateUser(ctx, admin); err != nil {
		return fmt.Errorf("failed to create break-glass admin: %w", err)
	}

	jctx.Logger.WarnContext(ctx, "Break-glass: created a super admin with SP_ADMIN_PASSWORD on an "+
		"OIDC-only instance, you will be asked to change it at first login", "email", email)

	return nil
}
