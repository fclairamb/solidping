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
)

// TestLogReady covers the ready line and the first-login line: the password
// is printed only while the seeded admin still has to rotate it, and a
// deported agent prints nothing.
func TestLogReady(t *testing.T) {
	t.Parallel()

	listenAddr := &net.TCPAddr{IP: net.IPv6unspecified, Port: 4972}

	testCases := []struct {
		name          string
		role          string
		withDB        bool
		seedAdmin     bool
		mustChange    bool
		wantReady     bool
		wantFirstLine bool
	}{
		{"admin must change password", "", true, true, true, true, true},
		{"admin already rotated", "", true, true, false, true, false},
		{"no admin seeded", "", true, false, false, true, false},
		{"no database service", "", false, false, false, true, false},
		{"agent mode logs nothing", config.NodeRoleAgent, true, true, true, false, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			r := require.New(t)

			srv := &Server{config: &config.Config{
				Node:     config.NodeConfig{Role: tc.role},
				Database: config.DatabaseConfig{Type: config.DatabaseTypeSQLite, Dir: t.TempDir()},
			}}

			if tc.withDB {
				dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
				r.NoError(err)
				r.NoError(dbSvc.Initialize(ctx))
				t.Cleanup(func() { _ = dbSvc.Close() })

				if tc.seedAdmin {
					user := models.NewUser(defaults.Email)
					user.MustChangePassword = tc.mustChange
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
				r.Contains(logged, "email="+defaults.Email)
				r.Contains(logged, "password="+defaults.Password)
			} else {
				r.NotContains(logged, "password=")
			}
		})
	}
}
