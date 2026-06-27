package jobtypes_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
)

// startFakeDocker stands up an httptest Docker API serving the negotiation ping
// and GET /containers/json, returning a tcp:// endpoint for the Docker client.
func startFakeDocker(t *testing.T, summaries []container.Summary) string {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(summaries)
		default:
			http.NotFound(w, r)
		}
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return "tcp://" + strings.TrimPrefix(srv.URL, "http://")
}

// containerDiscoveryFixture is an in-memory db + org + a container_discovery job
// row to satisfy discovered_checks foreign keys.
type containerDiscoveryFixture struct {
	dbSvc  db.Service
	org    *models.Organization
	jobUID string
}

func newContainerDiscoveryFixture(t *testing.T) *containerDiscoveryFixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("ct-disc", "Container Discovery Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	job := models.NewJob(&org.UID, string(jobdef.JobTypeContainerDiscovery))
	r.NoError(dbSvc.CreateJob(ctx, job))

	return &containerDiscoveryFixture{dbSvc: dbSvc, org: org, jobUID: job.UID}
}

func (f *containerDiscoveryFixture) jctx() *jobdef.JobContext {
	return &jobdef.JobContext{
		OrganizationUID: &f.org.UID,
		Job:             &models.Job{UID: f.jobUID},
		DB:              f.dbSvc.DB(),
		DBService:       f.dbSvc,
		Services:        &services.Registry{},
		Logger:          slog.Default(),
	}
}

func runContainerJob(t *testing.T, f *containerDiscoveryFixture, cfg jobtypes.ContainerDiscoveryConfig) error {
	t.Helper()

	def := &jobtypes.ContainerDiscoveryJobDefinition{}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	runner, err := def.CreateJobRun(raw)
	require.NoError(t, err)

	return runner.Run(context.Background(), f.jctx())
}

func (f *containerDiscoveryFixture) listChecks(t *testing.T) []*models.DiscoveredCheck {
	t.Helper()

	var checks []*models.DiscoveredCheck
	err := f.dbSvc.DB().NewSelect().
		Model(&checks).
		Where("organization_uid = ?", f.org.UID).
		Where("deleted_at IS NULL").
		Order("group_key ASC", "slug ASC").
		Scan(t.Context())
	require.NoError(t, err)

	return checks
}

func TestContainerDiscoveryPersistsGroupedChecks(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	endpoint := startFakeDocker(t, []container.Summary{
		{
			ID:     "web123",
			Names:  []string{"/web"},
			Image:  "nginx:latest",
			State:  "running",
			Status: "Up 2 hours (healthy)",
			Ports: []container.Port{
				{PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
				{PrivatePort: 9000, PublicPort: 0, Type: "tcp"}, // exposed-only -> no suggestion
			},
		},
		{
			ID:     "db456",
			Names:  []string{"/db"},
			Image:  "postgres:16",
			State:  "running",
			Status: "Up 5 minutes",
		},
	})

	f := newContainerDiscoveryFixture(t)
	r.NoError(runContainerJob(t, f, jobtypes.ContainerDiscoveryConfig{Hosts: []string{endpoint}}))

	checks := f.listChecks(t)

	for _, c := range checks {
		r.Equal(models.DiscoverySourceContainer, c.Source)
		r.Equal(f.jobUID, c.JobUID)
	}

	byGroup := map[string][]*models.DiscoveredCheck{}
	for _, c := range checks {
		byGroup[c.GroupKey] = append(byGroup[c.GroupKey], c)
	}

	// web: a docker check + one http check for the published 80->8080 (the
	// exposed-only 9000 yields nothing).
	web := byGroup["web123"]
	r.Len(web, 2)

	types := map[string]bool{}
	for _, c := range web {
		r.Equal("web", c.GroupLabel)
		types[c.Type] = true
		r.Contains(string(c.Metadata), "nginx:latest")
	}
	r.True(types["docker"], "web must always get a docker check")
	r.True(types["http"], "published 80 must get an http check")

	// db: only the docker check (no published ports).
	dbChecks := byGroup["db456"]
	r.Len(dbChecks, 1)
	r.Equal("docker", dbChecks[0].Type)
	r.Equal("db", dbChecks[0].GroupLabel)

	// The docker check config carries host + containerName for promotion.
	var dockerCfg struct {
		Host          string `json:"host"`
		ContainerName string `json:"containerName"`
	}
	r.NoError(json.Unmarshal(dbChecks[0].Config, &dockerCfg))
	r.Equal(endpoint, dockerCfg.Host)
	r.Equal("db", dockerCfg.ContainerName)
}

func TestContainerDiscoveryUpsertIsIdempotent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	endpoint := startFakeDocker(t, []container.Summary{
		{ID: "web123", Names: []string{"/web"}, Image: "nginx", State: "running", Status: "Up"},
	})

	f := newContainerDiscoveryFixture(t)
	cfg := jobtypes.ContainerDiscoveryConfig{Hosts: []string{endpoint}}
	r.NoError(runContainerJob(t, f, cfg))
	r.NoError(runContainerJob(t, f, cfg))

	checks := f.listChecks(t)
	r.Len(checks, 1, "re-running must update in place, not duplicate")
}

func TestContainerDiscoverySkipsUnreachableEndpoint(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	good := startFakeDocker(t, []container.Summary{
		{ID: "web123", Names: []string{"/web"}, Image: "nginx", State: "running", Status: "Up"},
	})

	f := newContainerDiscoveryFixture(t)
	// One unreachable endpoint and one good one: the run must not abort; the good
	// endpoint's container is still recorded.
	cfg := jobtypes.ContainerDiscoveryConfig{Hosts: []string{"tcp://127.0.0.1:1", good}}
	r.NoError(runContainerJob(t, f, cfg))

	checks := f.listChecks(t)
	r.Len(checks, 1)
	r.Equal("web123", checks[0].GroupKey)
}

func TestContainerDiscoveryRequiresHosts(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	def := &jobtypes.ContainerDiscoveryJobDefinition{}
	_, err := def.CreateJobRun(json.RawMessage(`{}`))
	r.Error(err)

	_, err = def.CreateJobRun(json.RawMessage(`{"hosts":[]}`))
	r.Error(err)
}
