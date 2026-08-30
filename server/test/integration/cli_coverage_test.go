package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// cliCovOrgUID matches the primary test organization created by testhelper.
const cliCovOrgUID = "10000000-0000-0000-0000-000000000001"

// cliCovAuthedClient logs in as the seeded test user and returns the client.
func cliCovAuthedClient(ctx context.Context, t *testing.T, ts *TestServer) *client.SolidPingClient {
	t.Helper()

	apiClient := ts.NewClient()
	_, err := apiClient.Login(ctx, TestOrgSlug, TestUserEmail, TestUserPassword)
	require.NoError(t, err)

	return apiClient
}

// cliCovSeedCheck inserts a check directly for use as a fixture.
func cliCovSeedCheck(
	ctx context.Context, t *testing.T, ts *TestServer, uid, slug, checkType string, cfg models.JSONMap,
) {
	t.Helper()

	name := slug
	now := time.Now()
	check := &models.Check{
		UID:             uid,
		OrganizationUID: cliCovOrgUID,
		Name:            &name,
		Slug:            &slug,
		Type:            checkType,
		Config:          cfg,
		Enabled:         true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, ts.Server.DBService().CreateCheck(ctx, check))
}

// TestCLICoverage_IncidentLifecycle exercises the ack/snooze/unsnooze/unack/resolve
// generated-client operations end to end against the real handlers.
func TestCLICoverage_IncidentLifecycle(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	checkUID := "40000000-0000-0000-0000-000000000001"
	cliCovSeedCheck(ctx, t, ts, checkUID, "cli-inc-check", "http", models.JSONMap{"url": "https://example.com"})

	incUIDStr := "40000000-0000-0000-0000-000000000002"
	title := "CLI coverage incident"
	now := time.Now()
	incident := &models.Incident{
		UID:             incUIDStr,
		Kind:            models.IncidentKindCheck,
		OrganizationUID: cliCovOrgUID,
		CheckUID:        checkUID,
		Title:           &title,
		State:           models.IncidentStateActive,
		FailureCount:    2,
		StartedAt:       now.Add(-1 * time.Hour),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	r.NoError(ts.Server.DBService().CreateIncident(ctx, incident))

	apiClient := cliCovAuthedClient(ctx, t, ts)
	incUID := uuid.MustParse(incUIDStr)

	note := "ack via cli test"
	ackResp, err := apiClient.AcknowledgeIncidentWithResponse(
		ctx, TestOrgSlug, incUID, client.AcknowledgeIncidentJSONRequestBody{Note: &note})
	r.NoError(err)
	r.Equal(200, ackResp.StatusCode())
	r.NotNil(ackResp.JSON200)

	dur := "1h"
	snoozeResp, err := apiClient.SnoozeIncidentWithResponse(
		ctx, TestOrgSlug, incUID, client.SnoozeIncidentJSONRequestBody{Duration: &dur})
	r.NoError(err)
	r.Equal(200, snoozeResp.StatusCode())

	unsnoozeResp, err := apiClient.UnsnoozeIncidentWithResponse(ctx, TestOrgSlug, incUID)
	r.NoError(err)
	r.Equal(200, unsnoozeResp.StatusCode())

	unackResp, err := apiClient.UnacknowledgeIncidentWithResponse(ctx, TestOrgSlug, incUID)
	r.NoError(err)
	r.Equal(200, unackResp.StatusCode())

	resolveNote := "resolved via cli test"
	resolveResp, err := apiClient.ResolveIncidentWithResponse(
		ctx, TestOrgSlug, incUID, client.ResolveIncidentJSONRequestBody{Note: &resolveNote})
	r.NoError(err)
	r.Equal(200, resolveResp.StatusCode())
	r.NotNil(resolveResp.JSON200)
	r.NotNil(resolveResp.JSON200.State)
	r.Equal("resolved", string(*resolveResp.JSON200.State))
}

// TestCLICoverage_ValidateCheck covers the validateCheck operation.
func TestCLICoverage_ValidateCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	validResp, err := apiClient.ValidateCheckWithResponse(ctx, TestOrgSlug, client.ValidateCheckJSONRequestBody{
		Type:   "http",
		Config: map[string]interface{}{"url": "https://example.com"},
	})
	r.NoError(err)
	r.Equal(200, validResp.StatusCode())
	r.NotNil(validResp.JSON200)
	r.True(validResp.JSON200.Valid)

	invalidResp, err := apiClient.ValidateCheckWithResponse(ctx, TestOrgSlug, client.ValidateCheckJSONRequestBody{
		Type:   "not-a-real-type",
		Config: map[string]interface{}{},
	})
	r.NoError(err)
	r.Equal(200, invalidResp.StatusCode())
	r.NotNil(invalidResp.JSON200)
	r.False(invalidResp.JSON200.Valid)
	r.NotNil(invalidResp.JSON200.Fields)
}

// TestCLICoverage_CloneCheck covers the cloneCheck operation.
func TestCLICoverage_CloneCheck(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	srcUID := "40000000-0000-0000-0000-000000000010"
	cliCovSeedCheck(ctx, t, ts, srcUID, "cli-clone-src", "http", models.JSONMap{"url": "https://example.com"})

	apiClient := cliCovAuthedClient(ctx, t, ts)

	newSlug := "cli-clone-dst"
	resp, err := apiClient.CloneCheckWithResponse(ctx, TestOrgSlug, "cli-clone-src", client.CloneCheckJSONRequestBody{
		Slug: &newSlug,
	})
	r.NoError(err)
	r.Equal(201, resp.StatusCode())
	r.NotNil(resp.JSON201)
	r.NotNil(resp.JSON201.Slug)
	r.Equal(newSlug, *resp.JSON201.Slug)
}

// TestCLICoverage_OrgDependencyGraph covers getOrgDependencyGraph. The edge is
// created through the real create-dependency handler.
func TestCLICoverage_OrgDependencyGraph(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	parentUID := "40000000-0000-0000-0000-000000000020"
	childUID := "40000000-0000-0000-0000-000000000021"
	cliCovSeedCheck(ctx, t, ts, parentUID, "cli-dep-parent", "http", models.JSONMap{"url": "https://parent.example.com"})
	cliCovSeedCheck(ctx, t, ts, childUID, "cli-dep-child", "http", models.JSONMap{"url": "https://child.example.com"})

	apiClient := cliCovAuthedClient(ctx, t, ts)

	_, err := apiClient.AddCheckDependency(ctx, TestOrgSlug, "cli-dep-child", client.CreateDependencyBody{
		ParentCheckUID: parentUID,
		Kind:           "hard",
	})
	r.NoError(err)

	resp, err := apiClient.GetOrgDependencyGraphWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, resp.StatusCode())
	r.NotNil(resp.JSON200)
	r.NotNil(resp.JSON200.Data)
	r.NotNil(resp.JSON200.Data.Nodes)
	r.NotNil(resp.JSON200.Data.Edges)
	r.GreaterOrEqual(len(*resp.JSON200.Data.Nodes), 2)
	r.GreaterOrEqual(len(*resp.JSON200.Data.Edges), 1)
}

// TestCLICoverage_UpdateCheckDependency covers the updateCheckDependency
// PATCH-single-edge operation. The edge is created through the real
// create-dependency handler, then patched via the generated client.
func TestCLICoverage_UpdateCheckDependency(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	parentUID := "40000000-0000-0000-0000-000000000050"
	childUID := "40000000-0000-0000-0000-000000000051"
	cliCovSeedCheck(ctx, t, ts, parentUID, "cli-depupd-parent", "http",
		models.JSONMap{"url": "https://parent.example.com"})
	cliCovSeedCheck(ctx, t, ts, childUID, "cli-depupd-child", "http",
		models.JSONMap{"url": "https://child.example.com"})

	apiClient := cliCovAuthedClient(ctx, t, ts)

	edge, err := apiClient.AddCheckDependency(ctx, TestOrgSlug, "cli-depupd-child", client.CreateDependencyBody{
		ParentCheckUID: parentUID,
		Kind:           "hard",
	})
	r.NoError(err)
	r.NotNil(edge)
	r.Equal("hard", edge.Kind)
	r.Nil(edge.Description)

	depUID := uuid.MustParse(edge.UID)
	newKind := client.UpdateDependencyRequestKindSoft
	newDesc := "patched via cli test"
	resp, err := apiClient.UpdateCheckDependencyWithResponse(
		ctx, TestOrgSlug, "cli-depupd-child", depUID, client.UpdateCheckDependencyJSONRequestBody{
			Kind:        &newKind,
			Description: &newDesc,
		})
	r.NoError(err)
	r.Equal(200, resp.StatusCode())
	r.NotNil(resp.JSON200)
	r.Equal(edge.UID, resp.JSON200.Uid.String())
	r.Equal("soft", string(resp.JSON200.Kind))
	r.NotNil(resp.JSON200.Description)
	r.Equal(newDesc, *resp.JSON200.Description)
}

// TestCLICoverage_GetOrgResult covers the single-result getOrgResult operation.
func TestCLICoverage_GetOrgResult(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	dbService := ts.Server.DBService()

	region := "us-east-1"
	worker := &models.Worker{
		UID:       "40000000-0000-0000-0000-000000000030",
		Slug:      "cli-cov-worker",
		Name:      "CLI Coverage Worker",
		Region:    &region,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	r.NoError(dbService.CreateWorker(ctx, worker))

	checkUID := "40000000-0000-0000-0000-000000000031"
	cliCovSeedCheck(ctx, t, ts, checkUID, "cli-result-check", "http", models.JSONMap{"url": "https://example.com"})

	resultUIDStr := "40000000-0000-0000-0000-000000000032"
	workerUID := worker.UID
	status := int(models.ResultStatusUp)
	duration := float32(42.0)
	result := &models.Result{
		UID:             resultUIDStr,
		OrganizationUID: cliCovOrgUID,
		CheckUID:        checkUID,
		WorkerUID:       &workerUID,
		Region:          &region,
		PeriodType:      "raw",
		PeriodStart:     time.Now().Add(-5 * time.Minute),
		Status:          &status,
		Duration:        &duration,
		Output:          models.JSONMap{"message": "OK"},
		CreatedAt:       time.Now(),
	}
	r.NoError(dbService.CreateResult(ctx, result))

	apiClient := cliCovAuthedClient(ctx, t, ts)

	resp, err := apiClient.GetOrgResultWithResponse(
		ctx, TestOrgSlug, "cli-result-check", uuid.MustParse(resultUIDStr), &client.GetOrgResultParams{})
	r.NoError(err)
	r.Equal(200, resp.StatusCode())
	r.NotNil(resp.JSON200)
	r.NotNil(resp.JSON200.Uid)
	r.Equal(resultUIDStr, resp.JSON200.Uid.String())
}

// TestCLICoverage_Discovery covers listDiscoveryTypes and listDiscoveryScans.
func TestCLICoverage_Discovery(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	apiClient := cliCovAuthedClient(ctx, t, ts)

	typesResp, err := apiClient.ListDiscoveryTypesWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, typesResp.StatusCode())
	r.NotNil(typesResp.JSON200)

	scansResp, err := apiClient.ListDiscoveryScansWithResponse(ctx, TestOrgSlug)
	r.NoError(err)
	r.Equal(200, scansResp.StatusCode())
	r.NotNil(scansResp.JSON200)
}

// TestCLICoverage_SendHeartbeat covers the SendHeartbeat client wrapper against
// the public ingestion route.
func TestCLICoverage_SendHeartbeat(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ts := NewTestServer(t)
	ctx := t.Context()

	const token = "cli-cov-secret"
	cliCovSeedCheck(ctx, t, ts, "40000000-0000-0000-0000-000000000040", "cli-hb-check", "heartbeat",
		models.JSONMap{"token": token})

	apiClient := ts.NewClient()

	// Correct token succeeds.
	err := apiClient.SendHeartbeat(ctx, TestOrgSlug, "cli-hb-check", client.HeartbeatOptions{
		Token:   token,
		Status:  "up",
		Message: "cli coverage ping",
	})
	r.NoError(err)

	// Wrong token is rejected.
	err = apiClient.SendHeartbeat(ctx, TestOrgSlug, "cli-hb-check", client.HeartbeatOptions{
		Token: "wrong",
	})
	r.Error(err)
}
