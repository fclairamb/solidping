package scantypes_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/discovery/scantypes"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// codeOf extracts the DiscoveryError code from err, or "" if not coded.
func codeOf(err error) string {
	var de *scantypes.DiscoveryError
	if errors.As(err, &de) {
		return de.Code
	}

	return ""
}

func TestLANBuildJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		params   string
		wantType string
		wantCode string
	}{
		{
			name:     "valid cidr",
			params:   `{"cidrs":["192.168.1.0/24"]}`,
			wantType: string(jobdef.JobTypeNetworkDiscoveryPlan),
		},
		{
			name:     "valid large fan-out",
			params:   `{"cidrs":["10.0.0.0/18"]}`,
			wantType: string(jobdef.JobTypeNetworkDiscoveryPlan),
		},
		{
			name:     "missing cidrs",
			params:   `{}`,
			wantCode: scantypes.CodeInvalidParameters,
		},
		{
			name:     "malformed json",
			params:   `{nope`,
			wantCode: scantypes.CodeInvalidParameters,
		},
		{
			name:     "range too large",
			params:   `{"cidrs":["0.0.0.0/7"]}`,
			wantCode: "DISCOVERY_RANGE_TOO_LARGE",
		},
	}

	def := &scantypes.LANDefinition{}
	r0 := require.New(t)
	r0.Equal("lan", def.Type())
	r0.Equal(models.DiscoverySourceLAN, def.Source())

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			jobType, cfg, err := def.BuildJob(t.Context(), scantypes.Deps{}, "org-1", json.RawMessage(tc.params))

			if tc.wantCode != "" {
				r.Error(err)
				r.Equal(tc.wantCode, codeOf(err))

				return
			}

			r.NoError(err)
			r.Equal(tc.wantType, jobType)
			r.NotEmpty(cfg)
		})
	}
}

// fakeFreebox starts a fake Freebox API answering login + an empty LAN browser.
func fakeFreebox(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/login/", func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(freebox.LoginChallenge{Challenge: "ch"})
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: body})
	})
	mux.HandleFunc("/api/v4/login/session/", func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(freebox.SessionResult{SessionToken: "tok"})
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: body})
	})
	mux.HandleFunc("/api/v4/lan/browser/pub/", func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal([]freebox.RawLanHost{})
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: body})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func newFreeboxChannel(t *testing.T, dbSvc db.Service, orgUID, baseURL, status string) string {
	t.Helper()

	r := require.New(t)

	settings := &models.FreeboxSettings{BaseURL: baseURL, AppID: freebox.DefaultAppID, Status: status}
	m, err := settings.ToJSONMap()
	r.NoError(err)
	m["appToken"] = "permanent-token"

	conn := models.NewIntegration(orgUID, models.ConnectionTypeFreebox, "Freebox")
	conn.Settings = m
	r.NoError(dbSvc.CreateChannel(t.Context(), conn))

	return conn.UID
}

func freeboxFixture(t *testing.T, status string) (scantypes.Deps, string, string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(nil, nil)
	r.NoError(err)

	org := models.NewOrganization("fb-st", "Freebox Scantypes Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	srv := fakeFreebox(t)
	channelUID := newFreeboxChannel(t, dbSvc, org.UID, srv.URL, status)

	return scantypes.Deps{DBService: dbSvc, Creds: creds}, org.UID, channelUID
}

func TestFreeboxBuildJobGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	deps, orgUID, channelUID := freeboxFixture(t, models.FreeboxStatusGranted)

	def := &scantypes.FreeboxDefinition{}
	r.Equal("freebox", def.Type())
	r.Equal(models.DiscoverySourceFreebox, def.Source())

	params, _ := json.Marshal(map[string]string{"channelUid": channelUID})
	jobType, cfg, err := def.BuildJob(t.Context(), deps, orgUID, params)
	r.NoError(err)
	r.Equal(string(jobdef.JobTypeFreeboxLanDiscovery), jobType)
	r.Contains(string(cfg), channelUID)
}

func TestFreeboxBuildJobNotGranted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	deps, orgUID, channelUID := freeboxFixture(t, models.FreeboxStatusPairing)

	def := &scantypes.FreeboxDefinition{}
	params, _ := json.Marshal(map[string]string{"channelUid": channelUID})
	_, _, err := def.BuildJob(t.Context(), deps, orgUID, params)
	r.Error(err)
	r.Equal(scantypes.CodeFreeboxNotGranted, codeOf(err))
}

func TestFreeboxBuildJobMissingChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	deps, orgUID, _ := freeboxFixture(t, models.FreeboxStatusGranted)

	def := &scantypes.FreeboxDefinition{}
	_, _, err := def.BuildJob(t.Context(), deps, orgUID, json.RawMessage(`{"channelUid":"missing"}`))
	r.Error(err)
	r.Equal(scantypes.CodeInvalidParameters, codeOf(err))
}

func TestFreeboxBuildJobMissingParam(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	def := &scantypes.FreeboxDefinition{}
	_, _, err := def.BuildJob(t.Context(), scantypes.Deps{}, "org-1", json.RawMessage(`{}`))
	r.Error(err)
	r.Equal(scantypes.CodeInvalidParameters, codeOf(err))
}

func TestRegistryGetAndList(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	lan, ok := scantypes.Get("lan")
	r.True(ok)
	r.Equal("lan", lan.Type())

	fb, ok := scantypes.Get("freebox")
	r.True(ok)
	r.Equal("freebox", fb.Type())

	_, ok = scantypes.Get("nope")
	r.False(ok)

	all := scantypes.List()
	r.GreaterOrEqual(len(all), 2)
	// Sorted by Type(): freebox before lan.
	r.Equal("freebox", all[0].Type())
	r.Equal("lan", all[1].Type())
}

// TestEveryScanTypeHasJobDefinition is the activation guard from the spec risk
// log: a source registers in BOTH the scantypes registry AND the job-layer
// registry. Every type's BuildJob can only return a job type the job layer can
// run.
func TestEveryScanTypeHasJobDefinition(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Each registered scan type must enqueue a job type the job registry knows.
	wantJobType := map[string]jobdef.JobType{
		"lan":     jobdef.JobTypeNetworkDiscoveryPlan,
		"freebox": jobdef.JobTypeFreeboxLanDiscovery,
	}

	for _, def := range scantypes.List() {
		jt, ok := wantJobType[def.Type()]
		r.True(ok, "scan type %q has no expected job type mapping in this test", def.Type())

		_, defOK := jobtypes.GetJobDefinition(jt)
		r.True(defOK, "scan type %q enqueues job type %q with no JobDefinition", def.Type(), jt)
	}
}
