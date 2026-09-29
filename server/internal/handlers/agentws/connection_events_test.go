package agentws_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// Spec 2026-09-28-03 §4: every terminal reason stays distinct, and a shutdown
// writes its disconnect before the process can exit.

// TestReadErrorIsJoinedByWaitForEvents: the agent's socket failing is recorded
// as `error`, and WaitForEvents joins the detached write — the event exists
// the moment it returns, with no polling.
func TestReadErrorIsJoinedByWaitForEvents(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	conn, _, _ := e.enroll(e.mintToken(), "office-1")
	r.NoError(conn.CloseNow())

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	r.NoError(e.handler.WaitForEvents(ctx))

	r.Len(e.agentEvents(models.EventTypeAgentConnected), 1)

	disconnected := e.agentEvents(models.EventTypeAgentDisconnected)
	r.Len(disconnected, 1)
	r.Equal(models.AgentDisconnectReasonError, disconnected[0].Payload[models.AgentEventPayloadReason])
}

// TestHandlerCloseRecordsServerShutdown is the production shutdown path:
// hijacked agent sockets are invisible to http.Server.Shutdown, so Close ends
// them and WaitForEvents holds the exit until their disconnect has landed.
func TestHandlerCloseRecordsServerShutdown(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	conn, _, _ := e.enroll(e.mintToken(), "office-1")
	e.waitForEvent(models.EventTypeAgentConnected)

	e.handler.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	r.NoError(e.handler.WaitForEvents(ctx))

	disconnected := e.agentEvents(models.EventTypeAgentDisconnected)
	r.Len(disconnected, 1)
	r.Equal(models.AgentDisconnectReasonServerShutdown, disconnected[0].Payload[models.AgentEventPayloadReason])

	// The agent sees its connection end.
	var frame agentcrypto.ServerFrame
	r.Error(wsjson.Read(ctx, conn, &frame))

	// A closed handler takes no new connection: the socket is ended before any
	// connect event is written.
	late, _, _ := e.enrollOrClosed(e.mintToken(), "office-2")
	r.Error(wsjson.Read(ctx, late, &frame))
	r.NoError(e.handler.WaitForEvents(ctx))
	r.Len(e.agentEvents(models.EventTypeAgentConnected), 1, "no connect event after Close")
}

// enrollOrClosed runs the enrollment handshake up to the enrolled frame only
// (a closed handler never sends the identity hello).
func (e *env) enrollOrClosed(token, name string) (*websocket.Conn, *agentcrypto.AgentKeys, agentcrypto.ServerFrame) {
	e.t.Helper()
	r := require.New(e.t)
	ctx := e.t.Context()

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	conn, _, err := e.dialWith(&websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization": []string{"Bearer " + token},
	}})
	r.NoError(err)
	e.t.Cleanup(func() { _ = conn.CloseNow() })

	var hello agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &hello))

	r.NoError(wsjson.Write(ctx, conn, agentcrypto.ClientFrame{
		Type:             agentcrypto.MsgTypeEnroll,
		ID:               "enroll-1",
		Name:             name,
		Ed25519PublicKey: keys.Ed25519PublicKey,
		X25519PublicKey:  keys.X25519Recipient,
	}))

	var enrolled agentcrypto.ServerFrame
	r.NoError(wsjson.Read(ctx, conn, &enrolled))
	r.Equal(agentcrypto.MsgTypeEnrolled, enrolled.Type)

	return conn, keys, enrolled
}

// cancelableRequests serves inner with a request context that is also canceled
// once stop is closed, and closes served once inner returns.
func cancelableRequests(stop <-chan struct{}, inner http.Handler, served chan<- struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()

		go func() {
			select {
			case <-stop:
				cancel()
			case <-ctx.Done():
			}
		}()

		inner.ServeHTTP(w, req.WithContext(ctx))
		close(served)
	})
}

// TestCanceledRequestRecordsServerShutdownSynchronously: a canceled request
// context ends the connection as server_shutdown, and that event is written
// synchronously — it exists the instant the handler returns, with nothing
// left in flight for a process exit to lose.
func TestCanceledRequestRecordsServerShutdownSynchronously(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	stopRequests := make(chan struct{})

	served := make(chan struct{})
	inner := e.server.Config.Handler

	wrapped := httptest.NewServer(cancelableRequests(stopRequests, inner, served))
	t.Cleanup(wrapped.Close)

	e.server = wrapped

	e.enroll(e.mintToken(), "office-1")
	e.waitForEvent(models.EventTypeAgentConnected)

	close(stopRequests)

	select {
	case <-served:
	case <-time.After(10 * time.Second):
		r.FailNow("the handler did not return after its request context was canceled")
	}

	disconnected := e.agentEvents(models.EventTypeAgentDisconnected)
	r.Len(disconnected, 1, "written before the handler returned")
	r.Equal(models.AgentDisconnectReasonServerShutdown, disconnected[0].Payload[models.AgentEventPayloadReason])
}
