package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/buildinfo"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// memTestStore is a no-op DEKStore so the credentials service can cache DEKs
// without a real database.
type memTestStore struct{}

func (memTestStore) LoadDEK(context.Context, string) ([]byte, bool, error) { return nil, false, nil }
func (memTestStore) SaveDEK(context.Context, string, []byte) error         { return nil }

func TestBuildMemorySnapshot(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// 32-byte key so encryption is enabled and DEKs get cached.
	key := make([]byte, 32)
	cred, err := credentials.NewService(key, memTestStore{})
	r.NoError(err)
	r.NoError(cred.EnsureOrgKey(context.Background(), "org-a"))
	r.NoError(cred.EnsureOrgKey(context.Background(), "org-b"))

	ev := notifier.NewLocalEventNotifier()
	_ = ev.Listen("check.created")
	_ = ev.Listen("check.created")
	_ = ev.Listen("incident.opened")

	srv := &Server{
		services: &services.Registry{
			Credentials:   cred,
			EventNotifier: ev,
		},
		rateLimiter: middleware.NewRateLimiter(config.RateLimitConfig{}, context.Background()),
	}

	snap := srv.buildMemorySnapshot(prometheus.DefaultGatherer)

	// Runtime fields are populated from the live process.
	r.Positive(snap.Runtime.NumGoroutine)
	r.Positive(snap.Runtime.SysBytes)

	// Subsystems reflect the wired sources.
	r.Equal(2, snap.Subsystems.DEKCacheEntries)
	r.Equal(3, snap.Subsystems.EventListeners)
	r.Equal(0, snap.Subsystems.RateLimitEntries) // no traffic yet

	// Build facts match this binary.
	r.Equal(buildinfo.CGOEnabled, snap.Build.CGOEnabled)
	r.Equal(buildinfo.SQLiteDriver(), snap.Build.SQLiteDriver)
	r.Equal(runtime.Version(), snap.Build.GoVersion)
}

func TestMemorySnapshotJSONShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &Server{}
	snap := srv.buildMemorySnapshot(prometheus.DefaultGatherer)

	data, err := json.Marshal(memorySnapshotResponse{Data: snap})
	r.NoError(err)

	// Decode into a generic map to assert the {data} envelope and camelCase keys.
	var decoded map[string]json.RawMessage
	r.NoError(json.Unmarshal(data, &decoded))
	r.Contains(decoded, "data")

	var dataObj map[string]json.RawMessage
	r.NoError(json.Unmarshal(decoded["data"], &dataObj))
	for _, key := range []string{"runtime", "process", "subsystems", "build"} {
		r.Contains(dataObj, key)
	}

	var rt map[string]json.RawMessage
	r.NoError(json.Unmarshal(dataObj["runtime"], &rt))
	// camelCase, not snake_case.
	r.Contains(rt, "heapInuseBytes")
	r.Contains(rt, "numGoroutine")
	r.Contains(rt, "goMemLimitBytes")
	r.Contains(rt, "goMaxProcs")
	r.NotContains(rt, "heap_inuse_bytes")

	var build map[string]json.RawMessage
	r.NoError(json.Unmarshal(dataObj["build"], &build))
	r.Contains(build, "cgoEnabled")
	r.Contains(build, "sqliteDriver")
}

// TestGetMemoryAuthMatrix verifies the route's RequireSuperAdmin gate: viewer /
// regular / org-admin (non-super) users get 403; a super admin gets 200 with
// the snapshot. Mirrors TestRequireOrgAdmin_AuthMatrix.
func TestGetMemoryAuthMatrix(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	mkUser := func(email string, super bool) *models.User {
		u := models.NewUser(email)
		u.SuperAdmin = super
		r.NoError(dbSvc.CreateUser(ctx, u))
		return u
	}

	orgAdmin := mkUser("orgadmin@example.com", false)
	regular := mkUser("user@example.com", false)
	viewer := mkUser("viewer@example.com", false)
	super := mkUser("super@example.com", true)

	srv := &Server{}
	authMw := middleware.NewAuthMiddleware(nil, dbSvc, &config.Config{})
	guarded := authMw.RequireSuperAdmin(srv.getMemory)

	requestWithUser := func(u *models.User) *http.Request {
		c := context.WithValue(context.Background(), base.ContextKeyUser, u)
		req := httptest.NewRequestWithContext(c, http.MethodGet, "/api/mgmt/memory", http.NoBody)
		return req
	}

	tests := []struct {
		name       string
		user       *models.User
		wantStatus int
	}{
		{"super admin allowed", super, http.StatusOK},
		{"org admin forbidden", orgAdmin, http.StatusForbidden},
		{"regular user forbidden", regular, http.StatusForbidden},
		{"viewer forbidden", viewer, http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			w := httptest.NewRecorder()
			_ = guarded(w, requestWithUser(tc.user))
			rr.Equal(tc.wantStatus, w.Code)

			if tc.wantStatus == http.StatusOK {
				var resp memorySnapshotResponse
				rr.NoError(json.Unmarshal(w.Body.Bytes(), &resp))
				rr.Equal(buildinfo.SQLiteDriver(), resp.Data.Build.SQLiteDriver)
			}
		})
	}
}

// TestMemorySnapshotBreakdownShape pins the 1a additions: the new sections are
// present, camelCase, and — on a machine with no /proc and no cgroup (macOS
// dev, which is where this test usually runs) — report present=false rather
// than failing the request.
func TestMemorySnapshotBreakdownShape(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &Server{}
	snap := srv.buildMemorySnapshot(prometheus.DefaultGatherer)

	data, err := json.Marshal(memorySnapshotResponse{Data: snap})
	r.NoError(err)

	var decoded struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	r.NoError(json.Unmarshal(data, &decoded))

	// Existing sections must survive untouched — this endpoint has consumers.
	for _, key := range []string{"runtime", "process", "subsystems", "build"} {
		r.Contains(decoded.Data, key)
	}
	// ...alongside the new ones.
	for _, key := range []string{"cgroup", "derived", "sample"} {
		r.Contains(decoded.Data, key)
	}

	var proc map[string]json.RawMessage
	r.NoError(json.Unmarshal(decoded.Data["process"], &proc))
	r.Contains(proc, "rssBytes") // the original field keeps its name
	r.Contains(proc, "status")
	r.Contains(proc, "smaps")

	var status map[string]json.RawMessage
	r.NoError(json.Unmarshal(proc["status"], &status))
	for _, key := range []string{"present", "rssAnonBytes", "rssFileBytes", "vmHwmBytes", "threads"} {
		r.Contains(status, key)
	}
	r.NotContains(status, "rss_anon_bytes")

	var rt map[string]json.RawMessage
	r.NoError(json.Unmarshal(decoded.Data["runtime"], &rt))
	r.Contains(rt, "classes")

	var classes map[string]json.RawMessage
	r.NoError(json.Unmarshal(rt["classes"], &classes))
	for _, key := range []string{"totalBytes", "heapObjectsBytes", "heapReleasedBytes", "osStacksBytes", "heapLiveBytes"} {
		r.Contains(classes, key)
	}

	// runtime/metrics works everywhere, so the Go side of the off-heap
	// subtraction is always populated — even where /proc is not.
	r.Positive(snap.Runtime.Classes.TotalBytes)
	r.Equal(snap.Runtime.Classes.TotalBytes-snap.Runtime.Classes.HeapReleasedBytes, snap.Derived.GoResidentBytes)

	if !snap.Process.Status.Present {
		// No /proc: the derived off-heap number must declare itself unknown
		// rather than pass a fabricated zero off as a measurement.
		r.False(snap.Derived.OffHeapKnown)
		r.Zero(snap.Derived.OffHeapBytes)
	}
}

// TestMemorySnapshotFloorMode pins ?gc=1: the sample mode is reported, and the
// two modes are distinguishable in the payload so nobody compares a floor
// reading with a steady-state one by accident.
func TestMemorySnapshotFloorMode(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	srv := &Server{}

	steady := srv.buildMemorySnapshotMode(prometheus.DefaultGatherer, false)
	r.Equal("steady", steady.Sample.Mode)
	r.False(steady.Sample.GCForced)
	r.False(steady.Sample.TakenAt.IsZero())

	floor := srv.buildMemorySnapshotMode(prometheus.DefaultGatherer, true)
	r.Equal("floor", floor.Sample.Mode)
	r.True(floor.Sample.GCForced)
	// A forced GC ran, so the cycle counter must have advanced.
	r.Greater(floor.Runtime.NumGC, steady.Runtime.NumGC)
}

func TestWantsFloorMode(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, query := range []string{"gc=1", "gc=true", "gc=yes"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/mgmt/memory?"+query, nil)
		r.True(wantsFloorMode(req), query)
	}
	for _, query := range []string{"", "gc=0", "gc=false", "gc=maybe"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/mgmt/memory?"+query, nil)
		r.False(wantsFloorMode(req), query)
	}
	r.False(wantsFloorMode(nil))
}
