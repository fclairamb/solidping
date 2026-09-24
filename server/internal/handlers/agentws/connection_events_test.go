package agentws_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Spec 2026-09-25-05 §3: an org agent's connection leaves a trace.

// agentEvents returns the org's events of one type, newest first.
func (e *env) agentEvents(eventType models.EventType) []*models.Event {
	e.t.Helper()

	events, err := e.dbSvc.ListEvents(e.t.Context(), &models.ListEventsFilter{
		OrganizationUID: e.org.UID,
		EventTypes:      []models.EventType{eventType},
	})
	require.NoError(e.t, err)

	return events
}

// waitForEvent waits for the (asynchronous, best-effort) event write.
func (e *env) waitForEvent(eventType models.EventType) []*models.Event {
	e.t.Helper()

	require.Eventually(e.t, func() bool {
		return len(e.agentEvents(eventType)) > 0
	}, 5*time.Second, 20*time.Millisecond, "expected a %s event", eventType)

	return e.agentEvents(eventType)
}

func TestConnectAndRevokedDisconnectAreRecorded(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	ctx := t.Context()

	conn, _, enrolled := e.enroll(e.mintToken(), "office-1")

	connected := e.waitForEvent(models.EventTypeAgentConnected)
	r.Len(connected, 1)
	r.Equal(testRegion, connected[0].Payload[models.AgentEventPayloadRegion])
	r.Equal(enrolled.AgentUID, connected[0].Payload["target_uid"])
	r.Empty(e.agentEvents(models.EventTypeAgentDisconnected), "a live connection has not disconnected")

	// Revoke it, then send a frame: the server closes the socket as revoked.
	r.NoError(e.dbSvc.RevokeAgent(ctx, e.org.UID, enrolled.AgentUID))

	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_ = wsjson.Write(writeCtx, conn, agentcrypto.ClientFrame{Type: agentcrypto.MsgTypeClaim, ID: "c1", MaxJobs: 1})

	var frame agentcrypto.ServerFrame

	readErr := wsjson.Read(writeCtx, conn, &frame)
	r.Error(readErr)
	r.Equal(websocket.StatusCode(4403), websocket.CloseStatus(readErr))

	disconnected := e.waitForEvent(models.EventTypeAgentDisconnected)
	r.Len(disconnected, 1, "exactly one disconnect event")
	r.Equal(models.AgentDisconnectReasonRevoked, disconnected[0].Payload[models.AgentEventPayloadReason])
	r.Equal(testRegion, disconnected[0].Payload[models.AgentEventPayloadRegion])
	r.Equal("office-1", disconnected[0].Payload["target_name"])
}

func TestAgentGoingAwayIsRecordedAsError(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	conn, _, _ := e.enroll(e.mintToken(), "office-1")
	e.waitForEvent(models.EventTypeAgentConnected)

	r.NoError(conn.Close(websocket.StatusNormalClosure, "agent stopping"))

	disconnected := e.waitForEvent(models.EventTypeAgentDisconnected)
	r.Len(disconnected, 1)
	r.Equal(models.AgentDisconnectReasonError, disconnected[0].Payload[models.AgentEventPayloadReason])
}

// fakeEnsurer records the post-enrollment re-ensure calls.
type fakeEnsurer struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeEnsurer) EnsurePrivateLocationMonitor(_ context.Context, orgUID, slug string) (*models.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, orgUID+"/"+slug)

	return nil, nil //nolint:nilnil // nothing created
}

func TestEnrollmentReEnsuresTheLivenessMonitor(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	ensurer := &fakeEnsurer{}
	e.handler.SetLivenessMonitors(ensurer)

	e.enroll(e.mintToken(), "office-1")

	ensurer.mu.Lock()
	defer ensurer.mu.Unlock()

	r.Equal([]string{e.org.UID + "/dc1"}, ensurer.calls)
}
