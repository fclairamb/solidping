// Package closeddb_test checks, for every handler service, that a lost
// database connection comes back as an error from each method instead of a
// panic or a silent success.
package closeddb_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/agents"
	"github.com/fclairamb/solidping/server/internal/handlers/availability"
	"github.com/fclairamb/solidping/server/internal/handlers/badges"
	"github.com/fclairamb/solidping/server/internal/handlers/checkchannels"
	"github.com/fclairamb/solidping/server/internal/handlers/checkdependencies"
	"github.com/fclairamb/solidping/server/internal/handlers/checkgroups"
	"github.com/fclairamb/solidping/server/internal/handlers/emailsuppressions"
	"github.com/fclairamb/solidping/server/internal/handlers/escalationpolicies"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentnotifications"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/handlers/labels"
	"github.com/fclairamb/solidping/server/internal/handlers/maintenancewindows"
	"github.com/fclairamb/solidping/server/internal/handlers/members"
	"github.com/fclairamb/solidping/server/internal/handlers/oncallschedules"
	"github.com/fclairamb/solidping/server/internal/handlers/orgparams"
	"github.com/fclairamb/solidping/server/internal/handlers/regions"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
	"github.com/fclairamb/solidping/server/internal/handlers/severities"
	"github.com/fclairamb/solidping/server/internal/handlers/slos"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
	"github.com/fclairamb/solidping/server/internal/handlers/system"
	"github.com/fclairamb/solidping/server/internal/handlers/uistate"
	"github.com/fclairamb/solidping/server/internal/handlers/unsubscribe"
	"github.com/fclairamb/solidping/server/internal/handlers/usernotifications"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

func TestServicesReportAClosedDatabase(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}

	newCreds := func(d db.Service) credentials.Service {
		creds, err := credentials.NewService(nil, credentials.NewParamStore(d))
		require.NoError(t, err)

		return creds
	}

	cases := []struct {
		name string
		make func(d db.Service) any
	}{
		{"agents", func(d db.Service) any { return agents.NewService(d, newCreds(d), nil) }},
		{"availability", func(d db.Service) any { return availability.NewService(d, cfg) }},
		{"badges", func(d db.Service) any { return badges.NewService(d, cfg) }},
		{"checkchannels", func(d db.Service) any { return checkchannels.NewService(d) }},
		{"checkdependencies", func(d db.Service) any { return checkdependencies.NewService(d, 0) }},
		{"checkgroups", func(d db.Service) any { return checkgroups.NewService(d) }},
		{"emailsuppressions", func(d db.Service) any { return emailsuppressions.NewService(d) }},
		{"escalationpolicies", func(d db.Service) any { return escalationpolicies.NewService(d) }},
		{"events", func(d db.Service) any { return events.NewService(d) }},
		{"files", func(d db.Service) any { return files.NewService(d, cfg) }},
		{"incidentnotifications", func(d db.Service) any { return incidentnotifications.NewService(d) }},
		{"incidentpublications", func(d db.Service) any {
			return incidentpublications.NewService(d, clock.Real{}, nil)
		}},
		{"integrations", func(d db.Service) any { return integrations.NewService(d, newCreds(d), nil, cfg) }},
		{"labels", func(d db.Service) any { return labels.NewService(d) }},
		{"maintenancewindows", func(d db.Service) any { return maintenancewindows.NewService(d) }},
		{"members", func(d db.Service) any { return members.NewService(d) }},
		{"oncallschedules", func(d db.Service) any { return oncallschedules.NewService(d) }},
		{"orgparams", func(d db.Service) any { return orgparams.NewService(d) }},
		{"regions", func(d db.Service) any { return regions.NewService(d) }},
		{"results", func(d db.Service) any { return results.NewService(d, cfg) }},
		{"severities", func(d db.Service) any { return severities.NewService(d) }},
		{"slos", func(d db.Service) any { return slos.NewService(d, cfg, nil) }},
		{"statuspages", func(d db.Service) any {
			return statuspages.NewService(d, cfg, entitlements.NewService(d, entitlements.DefaultsFor(""), 0))
		}},
		{"statussubscribers", func(d db.Service) any { return statussubscribers.NewService(d, newCreds(d)) }},
		{"statusupdates", func(d db.Service) any { return statusupdates.NewService(d) }},
		{"system", func(d db.Service) any { return system.NewService(d) }},
		{"uistate", func(d db.Service) any { return uistate.NewService(d) }},
		{"unsubscribe", func(d db.Service) any { return unsubscribe.NewService(d, []byte("secret")) }},
		{"usernotifications", func(d db.Service) any { return usernotifications.NewService(d, newCreds(d)) }},
		{"entitlements", func(d db.Service) any {
			return entitlements.NewService(d, entitlements.DefaultsFor(""), 0)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			ctx := t.Context()

			dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
			r.NoError(err)
			r.NoError(dbSvc.Initialize(ctx))

			svc := tc.make(dbSvc)

			r.NoError(dbSvc.DB().Close())

			called, failed := testsupport.CallErrorMethods(t, svc)
			r.Positive(called)
			r.Positive(failed, "no method of %s reported the closed database (%d called)", tc.name, called)
		})
	}
}
