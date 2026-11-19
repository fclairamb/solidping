package app

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// TestLogReady covers the ready line and the first-login line: the password
// is printed only while the seeded admin still has to rotate it and only when
// it is the public defaults.Password (spec 2026-11-19-01), and a deported
// agent prints nothing.
func TestLogReady(t *testing.T) {
	t.Parallel()

	listenAddr := &net.TCPAddr{IP: net.IPv6unspecified, Port: 4972}

	testCases := []struct {
		name          string
		role          string
		withDB        bool
		seedAdmin     bool
		mustChange    bool
		password      string
		adminPassword string
		wantReady     bool
		wantFirstLine bool
		wantPassword  bool
		wantHint      string
	}{
		{"admin must change password", "", true, true, true, defaults.Password, "", true, true, true, ""},
		{
			"generated password is never reprinted", "", true, true, true, "Kx7mPq2RtW9vLz4NcHb3", "", true, true, false,
			"SP_ADMIN_PASSWORD set",
		},
		{
			"configured password is not printed", "", true, true, true, "acme-first-pass", "acme-first-pass", true, true, false,
			"value of SP_ADMIN_PASSWORD",
		},
		{"admin already rotated", "", true, true, false, defaults.Password, "", true, false, false, ""},
		{"no admin seeded", "", true, false, false, "", "", true, false, false, ""},
		{"no database service", "", false, false, false, "", "", true, false, false, ""},
		{"agent mode logs nothing", config.NodeRoleAgent, true, true, true, defaults.Password, "", false, false, false, ""},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			r := require.New(t)

			srv := &Server{config: &config.Config{
				Node:     config.NodeConfig{Role: tc.role},
				Database: config.DatabaseConfig{Type: config.DatabaseTypeSQLite, Dir: t.TempDir()},
				Auth:     config.AuthConfig{AdminPassword: tc.adminPassword},
			}}

			if tc.withDB {
				dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
				r.NoError(err)
				r.NoError(dbSvc.Initialize(ctx))
				t.Cleanup(func() { _ = dbSvc.Close() })

				if tc.seedAdmin {
					user := models.NewUser(defaults.Email)
					user.MustChangePassword = tc.mustChange

					hash, hashErr := passwords.HashBcrypt(tc.password)
					r.NoError(hashErr)
					user.PasswordHash = &hash

					r.NoError(dbSvc.CreateUser(ctx, user))
				}

				srv.dbService = dbSvc
			}

			var out bytes.Buffer

			srv.logReady(ctx, slog.New(slog.NewTextHandler(&out, nil)), listenAddr)

			logged := out.String()
			if tc.wantReady {
				r.Contains(logged, `msg="SolidPing is ready"`)
				r.Contains(logged, "url=http://localhost:4972")
			} else {
				r.NotContains(logged, "SolidPing is ready")
			}

			if tc.wantFirstLine {
				r.Contains(logged, "First login")
				r.Contains(logged, "email="+defaults.Email)
			} else {
				r.NotContains(logged, "First login")
			}

			if tc.wantPassword {
				r.Contains(logged, "password="+defaults.Password)
			} else {
				r.NotContains(logged, "password=")
			}

			if tc.wantHint != "" {
				r.Contains(logged, tc.wantHint)
			}

			if tc.password != "" && tc.password != defaults.Password {
				r.NotContains(logged, tc.password, "a non-default password must never reach the log")
			}
		})
	}
}
