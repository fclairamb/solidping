package crawltask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

// harness is one database with the crawl kind registered on a worker-task
// service, its result path and its report store, and one organization.
type harness struct {
	t     *testing.T
	db    db.Service
	tasks *workertasks.Service
	srv   *Server
	store *attachments.Service
	org   *models.Organization
	exec  *Executor
}

func newSQLite(t *testing.T) db.Service {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return dbSvc
}

func newHarness(t *testing.T, dbSvc db.Service, ent *entitlements.Service) *harness {
	t.Helper()

	localfs.Register()

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	store := attachments.NewService(files.NewService(dbSvc, cfg), dbSvc, cfg)

	tasks := workertasks.NewService(dbSvc.DB())
	tasks.SetStateStore(store)

	events := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = events.Close() })

	jobs := jobsvc.NewService(dbSvc.DB(), dbSvc, events, nil)
	results := workers.NewService(dbSvc, checkjobsvc.NewService(dbSvc.DB()),
		incidents.NewService(dbSvc, jobs, clock.Real{}, nil), scheduling.Params{})

	srv := Register(Deps{
		DB: dbSvc, Tasks: tasks, Results: results, Reports: store, Entitlements: ent, SliceBudget: 5 * time.Second,
	})

	org := models.NewOrganization("crawl-"+uuid.NewString()[:8], "Acme")
	require.NoError(t, dbSvc.CreateOrganization(t.Context(), org))

	return &harness{t: t, db: dbSvc, tasks: tasks, srv: srv, store: store, org: org, exec: NewExecutor()}
}

// useChecker makes both sides run checker for crawl.
func (h *harness) useChecker(checker checkerdef.Checker) {
	get := func(ct checkerdef.CheckType) (checkerdef.Checker, bool) {
		if ct == checkerdef.CheckTypeCrawl {
			return checker, true
		}

		return registry.GetChecker(ct)
	}

	h.srv.getChecker = get
	h.exec.getChecker = get
}

// crawl creates an enabled crawl check of org in region, its job due now.
func (h *harness) crawl(orgUID, region string, cfg models.JSONMap) *models.Check {
	h.t.Helper()

	check := models.NewCheck(orgUID, "crawl-"+uuid.NewString()[:8], "crawl")
	check.Config = cfg
	check.Period = timeutils.Duration(time.Hour)
	check.Regions = []string{region}
	require.NoError(h.t, h.db.CreateCheck(h.t.Context(), check))

	h.due(check)

	return check
}

func (h *harness) due(check *models.Check) {
	h.t.Helper()

	due := time.Now().Add(-time.Second).UTC()
	_, err := h.db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", due).Set("effective_scheduled_at = ?", due).
		Where("check_uid = ?", check.UID).Exec(h.t.Context())
	require.NoError(h.t, err)
}

func (h *harness) job(check *models.Check) *models.CheckJob {
	h.t.Helper()

	job := new(models.CheckJob)
	require.NoError(h.t, h.db.DB().NewSelect().Model(job).Where("check_uid = ?", check.UID).Scan(h.t.Context()))

	return job
}

func (h *harness) tasksOf(check *models.Check) []*models.WorkerTask {
	h.t.Helper()

	var rows []*models.WorkerTask
	require.NoError(h.t, h.db.DB().NewSelect().Model(&rows).
		Where("check_uid = ?", check.UID).OrderExpr("created_at ASC").Scan(h.t.Context()))

	return rows
}

func (h *harness) setTask(uid, column string, value any) {
	h.t.Helper()

	_, err := h.db.DB().NewUpdate().Model((*models.WorkerTask)(nil)).
		Set(column+" = ?", value).Where("uid = ?", uid).Exec(h.t.Context())
	require.NoError(h.t, err)
}

// results returns the result rows written for the check after since.
func (h *harness) results(check *models.Check, since time.Time) []*models.Result {
	h.t.Helper()

	var rows []*models.Result
	require.NoError(h.t, h.db.DB().NewSelect().Model(&rows).
		Where("check_uid = ?", check.UID).Where("created_at > ?", since).
		OrderExpr("created_at ASC").Scan(h.t.Context()))

	return rows
}

func cloudWorker(t *testing.T, dbSvc db.Service, region string) *workertasks.WorkerIdentity {
	t.Helper()

	worker := models.NewWorker("w-"+uuid.NewString()[:8], "Worker")
	worker.Region = &region
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(t.Context())
	require.NoError(t, err)

	return &workertasks.WorkerIdentity{UID: worker.UID, Region: region, Kinds: []string{Kind}, Slots: 1}
}

func agentWorker(orgUID, region string) *workertasks.WorkerIdentity {
	return &workertasks.WorkerIdentity{
		UID: uuid.NewString(), Region: region, OrgUID: orgUID, IsAgent: true, Kinds: []string{Kind}, Slots: 1,
	}
}

// reporter reports straight to the service as identity.
type reporter struct {
	svc    *workertasks.Service
	uid    string
	worker string
	// stepErr, when set, is returned by Step instead of reporting.
	stepErr error
}

func (r *reporter) Heartbeat(ctx context.Context) error {
	_, err := r.svc.Heartbeat(ctx, r.uid, r.worker)

	return err
}

func (r *reporter) Step(ctx context.Context, req workertasks.StepRequest) error {
	if r.stepErr != nil {
		return r.stepErr
	}

	return r.svc.Step(ctx, r.uid, r.worker, req)
}

func (r *reporter) Finish(ctx context.Context, body json.RawMessage) error {
	return r.svc.Finish(ctx, r.uid, r.worker, body)
}

func (r *reporter) Fail(ctx context.Context, code string) error {
	return r.svc.FailSlice(ctx, r.uid, r.worker, code)
}

// slice claims once as identity and runs the executor on the claim. It
// returns the claim, nil when there was nothing to claim.
func (h *harness) slice(identity *workertasks.WorkerIdentity) *workertasks.Claim {
	h.t.Helper()

	claim, err := h.tasks.Claim(h.t.Context(), identity)
	require.NoError(h.t, err)

	if claim == nil {
		return nil
	}

	rep := &reporter{svc: h.tasks, uid: claim.UID, worker: identity.UID}
	require.NoError(h.t, h.exec.Run(h.t.Context(), claim, rep))

	return claim
}

// runToEnd runs slices until the check has no active task, and returns the
// number of slices.
func (h *harness) runToEnd(identity *workertasks.WorkerIdentity, check *models.Check) int {
	h.t.Helper()

	for slices := 0; slices < 50; slices++ {
		if h.slice(identity) == nil {
			tasks := h.tasksOf(check)
			require.NotEmpty(h.t, tasks)
			require.False(h.t, tasks[len(tasks)-1].IsActive(), "the run is over")

			return slices
		}
	}

	h.t.Fatal("the run never ended")

	return 0
}

// site is an httptest website of pages linked from /, and one broken link.
func site(t *testing.T, pages int) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/html")

		if req.URL.Path == "/" {
			var body strings.Builder

			body.WriteString("<html><body>")

			for i := range pages {
				fmt.Fprintf(&body, `<a href="/page-%d">p%d</a>`, i, i)
			}

			body.WriteString(`<a href="/missing">gone</a></body></html>`)
			_, _ = fmt.Fprint(w, body.String())

			return
		}

		if req.URL.Path == "/missing" {
			http.NotFound(w, req)

			return
		}

		_, _ = fmt.Fprint(w, `<html><body><a href="/">home</a></body></html>`)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server
}

func siteConfig(url string) models.JSONMap {
	return models.JSONMap{
		"url": url + "/", "sitemap": "off", "delayMs": 0, "respectRobots": false,
		"checkExternalLinks": false, "maxPages": 100,
	}
}

// fakeChecker is a scripted StepChecker: each Step runs the next function of
// script and records its input.
type fakeChecker struct {
	mu     sync.Mutex
	inputs []checkerdef.StepInput
	script []func(in checkerdef.StepInput) (checkerdef.StepOutput, error)
}

var errFake = errors.New("fake slice failure")

func (f *fakeChecker) Type() checkerdef.CheckType           { return checkerdef.CheckTypeCrawl }
func (f *fakeChecker) Validate(*checkerdef.CheckSpec) error { return nil }
func (f *fakeChecker) UnitsPerSlice() int                   { return 5 }

func (f *fakeChecker) Execute(context.Context, checkerdef.Config) (*checkerdef.Result, error) {
	return &checkerdef.Result{Status: checkerdef.StatusUp}, nil
}

func (f *fakeChecker) Step(
	_ context.Context, _ checkerdef.Config, in checkerdef.StepInput,
) (checkerdef.StepOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.inputs = append(f.inputs, in)
	if len(f.script) == 0 {
		return checkerdef.StepOutput{}, errFake
	}

	next := f.script[0]
	f.script = f.script[1:]

	return next(in)
}

func (f *fakeChecker) calls() []checkerdef.StepInput {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]checkerdef.StepInput(nil), f.inputs...)
}

func partial(state string, units int) func(checkerdef.StepInput) (checkerdef.StepOutput, error) {
	return func(checkerdef.StepInput) (checkerdef.StepOutput, error) {
		return checkerdef.StepOutput{State: []byte(state), Units: units, Progress: map[string]any{"pagesDone": units}}, nil
	}
}

func done(units int) func(checkerdef.StepInput) (checkerdef.StepOutput, error) {
	return func(checkerdef.StepInput) (checkerdef.StepOutput, error) {
		return checkerdef.StepOutput{
			Done:   true,
			Units:  units,
			Result: &checkerdef.Result{Status: checkerdef.StatusUp, Output: map[string]any{"pagesCrawled": 3}},
			Report: []byte(`{"findings":[],"pagesCrawled":3}`),
		}, nil
	}
}

func failing(checkerdef.StepInput) (checkerdef.StepOutput, error) {
	return checkerdef.StepOutput{}, errFake
}
