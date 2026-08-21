package backend_test

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/agents/capturecache"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// pngMarker is deliberately ASCII so its ABSENCE from the wire can be asserted
// both raw and base64-encoded — a JSON encoder that ever learned to serialize
// the bytes would produce one or the other.
const pngMarker = "SCREENSHOT-BYTES-MUST-NOT-SHIP"

// bodyMarker plays the same role for the positive control: it rides
// failureResponse, which DOES serialize, so its presence proves the frame was
// not simply empty.
const bodyMarker = "FAILURE-BODY-MUST-SHIP"

const uploadTopic = "incidents/inc-1/screenshot"

func capturePNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte(pngMarker)...)
}

// diagnosticsWithCapture is what a failing browser check hands the worker: a
// textual failure capture AND the screenshot bytes.
func diagnosticsWithCapture() *checkerdef.Diagnostics {
	return &checkerdef.Diagnostics{
		FailureResponse: &checkerdef.FailureResponse{
			URL:        "https://example.com",
			StatusCode: 503,
			Body:       bodyMarker,
		},
		Screenshot: &checkerdef.Screenshot{
			PNG:        capturePNG(),
			CapturedAt: time.Now().UTC(),
		},
	}
}

// uploadRecord is one attachment POST the fake endpoint received.
type uploadRecord struct {
	topic     string
	body      []byte
	agentUID  string
	signature string
	timestamp string
	nonce     string
}

// captureServer is a fake master that speaks BOTH halves of the capture
// protocol: the WS control channel (where the marker arrives) and the
// attachment endpoint (where the bytes arrive). Having one origin serve both is
// the point — the agent derives the upload URL from its WS server URL.
type captureServer struct {
	server *httptest.Server

	mu sync.Mutex
	// rawResults holds the VERBATIM JSON of every result frame, so a test can
	// assert about what actually crossed the socket rather than about a
	// re-encoding of a decoded struct.
	rawResults [][]byte
	uploads    []uploadRecord
	agentPub   ed25519.PublicKey
	// askTwice makes the server send the same upload request a second time,
	// to prove the agent serves a capture at most once.
	askTwice bool
	uploaded chan struct{}
}

func newCaptureServer(t *testing.T) *captureServer {
	t.Helper()

	fake := &captureServer{uploaded: make(chan struct{}, 4)}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/ws", fake.serveWS)
	mux.HandleFunc("/api/v1/agent/attachments", fake.serveUpload)

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *captureServer) serveUpload(writer http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(req.Body, 8<<20))

	f.mu.Lock()
	f.uploads = append(f.uploads, uploadRecord{
		topic:     req.URL.Query().Get("topic"),
		body:      body,
		agentUID:  req.Header.Get("X-Sp-Agent-Uid"),
		signature: req.Header.Get("X-Sp-Signature"),
		timestamp: req.Header.Get("X-Sp-Timestamp"),
		nonce:     req.Header.Get("X-Sp-Nonce"),
	})
	f.mu.Unlock()

	writer.WriteHeader(http.StatusCreated)
	_, _ = writer.Write([]byte(`{"fileUid":"file-1"}`))

	select {
	case f.uploaded <- struct{}{}:
	default:
	}
}

func (f *captureServer) serveWS(writer http.ResponseWriter, req *http.Request) {
	conn, err := websocket.Accept(writer, req, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}

	conn.SetReadLimit(256 * 1024)

	ctx := context.Background()

	if err := wsjson.Write(ctx, conn, agents.ServerFrame{
		Type: agents.MsgTypeHello, Protocol: agents.ProtocolVersion,
		ServerTime: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return
	}

	var enroll agents.ClientFrame
	if err := wsjson.Read(ctx, conn, &enroll); err != nil {
		return
	}

	if pub, decErr := agents.ParsePublicKey(enroll.Ed25519PublicKey); decErr == nil {
		f.mu.Lock()
		f.agentPub = pub
		f.mu.Unlock()
	}

	if err := wsjson.Write(ctx, conn, agents.ServerFrame{
		Type: agents.MsgTypeEnrolled, ID: enroll.ID,
		AgentUID: "agent-1", WorkerUID: "worker-1", Region: "@dc1", Fingerprint: "fp-1",
	}); err != nil {
		return
	}

	if err := wsjson.Write(ctx, conn, agents.ServerFrame{
		Type: agents.MsgTypeHello, AgentUID: "agent-1", WorkerUID: "worker-1", Region: "@dc1",
	}); err != nil {
		return
	}

	f.readLoop(ctx, conn)
}

// readLoop acks results and, when one advertises a capture, mirrors what the
// incident pipeline does: ask for the upload with a SERVER-generated topic.
func (f *captureServer) readLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			return
		}

		var frame agents.ClientFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			return
		}

		if frame.Type != agents.MsgTypeResult {
			continue
		}

		f.mu.Lock()
		f.rawResults = append(f.rawResults, raw)
		askTwice := f.askTwice
		f.mu.Unlock()

		_ = wsjson.Write(ctx, conn, agents.ServerFrame{Type: agents.MsgTypeAck, ID: frame.ID})

		if frame.Diagnostics == nil || frame.Diagnostics.Screenshot == nil ||
			!frame.Diagnostics.Screenshot.Available {
			continue
		}

		ask := agents.ServerFrame{
			Type:      agents.MsgTypeUploadRequest,
			CaptureID: frame.Diagnostics.Screenshot.CaptureID,
			Topic:     uploadTopic,
		}

		_ = wsjson.Write(ctx, conn, ask)

		if askTwice {
			_ = wsjson.Write(ctx, conn, ask)
		}
	}
}

func (f *captureServer) lastResult(t *testing.T) []byte {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.rawResults, "the server must have received a result frame")

	return f.rawResults[len(f.rawResults)-1]
}

func (f *captureServer) uploadSnapshot() ([]uploadRecord, ed25519.PublicKey) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]uploadRecord(nil), f.uploads...), f.agentPub
}

// newCaptureBackend builds a WSBackend pointed at the capture server.
func newCaptureBackend(t *testing.T, fake *captureServer, opts ...backend.Option) *backend.WSBackend {
	t.Helper()

	keys, err := agents.GenerateAgentKeys()
	require.NoError(t, err)

	return backend.NewWSBackend(
		fake.server.URL, "spe_token", "agent",
		&backend.Identity{AgentKeys: *keys}, nil, opts...,
	)
}

func testJob() *models.CheckJob {
	return &models.CheckJob{UID: "job-1", CheckUID: "check-1", Type: "browser"}
}

func captureSubmitReq(diagnostics *checkerdef.Diagnostics) *backend.SubmitResultRequest {
	return &backend.SubmitResultRequest{
		Status:          int(models.ResultStatusDown),
		Duration:        12,
		Output:          map[string]any{"error": "keyword not found"},
		Diagnostics:     diagnostics,
		NextScheduledAt: time.Now().Add(time.Minute),
	}
}

// TestResultFrameCarriesMarkerNotBytes is the marker round-trip, asserted
// against the VERBATIM JSON the socket carried.
//
// It has a built-in positive control: failureResponse — which is meant to
// serialize — must be present with its body. Without that, "the PNG is not in
// the frame" would pass just as happily on an empty frame, on a dropped
// Diagnostics block, or on a result that never reached the server.
func TestResultFrameCarriesMarkerNotBytes(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	fake := newCaptureServer(t)
	wsBackend := newCaptureBackend(t, fake)

	diagnostics := diagnosticsWithCapture()
	r.NoError(wsBackend.SubmitResult(ctx, testJob(), "worker-1", captureSubmitReq(diagnostics)))

	raw := string(fake.lastResult(t))

	// POSITIVE CONTROL: the diagnostics block really did cross, in full.
	r.Contains(raw, "failureResponse")
	r.Contains(raw, bodyMarker, "the textual capture must still serialize")

	// The marker: what the agent holds, not what it holds it for.
	r.Contains(raw, `"available":true`)
	r.Contains(raw, `"captureId":"`)

	// THE NEGATIVE: no bytes, in any encoding a JSON encoder could reach for.
	r.NotContains(raw, pngMarker, "raw PNG bytes must never ride the control channel")
	r.NotContains(raw, base64.StdEncoding.EncodeToString(capturePNG()),
		"base64 PNG bytes must never ride the control channel either")
	r.NotContains(raw, `"png"`)
	r.NotContains(raw, `"PNG"`)

	// And the caller's own Result is untouched: stashing is a copy, so whoever
	// else holds this Diagnostics still sees what the checker produced.
	r.Equal(capturePNG(), diagnostics.Screenshot.PNG)
	r.False(diagnostics.Screenshot.Available)
	r.Empty(diagnostics.Screenshot.CaptureID)
}

// TestUploadRequestPostsTheCapture is the agent's half of the round trip: the
// bytes it withheld reach the attachment endpoint, under the topic the SERVER
// named, signed with the agent's own identity.
func TestUploadRequestPostsTheCapture(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	fake := newCaptureServer(t)
	wsBackend := newCaptureBackend(t, fake)

	r.NoError(wsBackend.SubmitResult(ctx, testJob(), "worker-1", captureSubmitReq(diagnosticsWithCapture())))

	waitForUpload(t, fake, 1)

	uploads, pub := fake.uploadSnapshot()
	r.Len(uploads, 1)
	r.Equal(uploadTopic, uploads[0].topic, "the topic is the server's, echoed nowhere else")
	r.Equal(capturePNG(), uploads[0].body, "the bytes arrive out-of-band, intact")
	r.Equal("agent-1", uploads[0].agentUID)

	// The upload is authenticated by the SAME Ed25519 challenge the WS
	// reconnect uses — no second credential exists for an agent.
	sig, err := base64.StdEncoding.DecodeString(uploads[0].signature)
	r.NoError(err)
	r.NotNil(pub)
	r.True(ed25519.Verify(pub,
		agents.SignatureChallenge(http.MethodPost, "/api/v1/agent/attachments",
			uploads[0].timestamp, uploads[0].nonce), sig),
		"the upload must carry a valid agent signature")
}

// TestCaptureIsServedAtMostOnce is the "nothing is retried forever" negative,
// from the agent's side: the same upload request delivered twice produces
// exactly ONE POST, because reading the capture removes it.
func TestCaptureIsServedAtMostOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	fake := newCaptureServer(t)

	fake.mu.Lock()
	fake.askTwice = true
	fake.mu.Unlock()

	wsBackend := newCaptureBackend(t, fake)

	r.NoError(wsBackend.SubmitResult(ctx, testJob(), "worker-1", captureSubmitReq(diagnosticsWithCapture())))

	waitForUpload(t, fake, 1)

	// Give the (never-arriving) second upload every chance to show up.
	time.Sleep(300 * time.Millisecond)

	uploads, _ := fake.uploadSnapshot()
	r.Len(uploads, 1, "a capture is uploadable exactly once; a repeated ask must not re-send it")
}

// TestUnknownCaptureIDUploadsNothing covers the agent that no longer holds what
// the server is asking for — the capture was evicted, expired, or the agent
// restarted. It must simply do nothing.
func TestUnknownCaptureIDUploadsNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	fake := newCaptureServer(t)

	// A cache with a TTL that has already elapsed for anything ever stored.
	wsBackend := newCaptureBackend(t, fake, backend.WithCaptureCache(
		capturecache.New(4, 1<<20, time.Nanosecond)))

	r.NoError(wsBackend.SubmitResult(ctx, testJob(), "worker-1", captureSubmitReq(diagnosticsWithCapture())))

	raw := string(fake.lastResult(t))
	r.Contains(raw, `"available":true`, "the marker is still advertised")

	time.Sleep(300 * time.Millisecond)

	uploads, _ := fake.uploadSnapshot()
	r.Empty(uploads, "an expired capture is lost, not reconstructed")
}

// TestOversizedCaptureAdvertisesNothing pins the degradation: a capture the
// local cache cannot hold must not be advertised, because an unfulfillable
// marker would make the server ask for an upload that can never arrive.
func TestOversizedCaptureAdvertisesNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	fake := newCaptureServer(t)
	wsBackend := newCaptureBackend(t, fake, backend.WithCaptureCache(
		capturecache.New(4, 8, time.Minute)))

	r.NoError(wsBackend.SubmitResult(ctx, testJob(), "worker-1", captureSubmitReq(diagnosticsWithCapture())))

	raw := string(fake.lastResult(t))
	r.NotContains(raw, `"available":true`)
	r.NotContains(raw, "screenshot")
	r.Contains(raw, bodyMarker, "the rest of the diagnostics block is unaffected")

	time.Sleep(200 * time.Millisecond)

	uploads, _ := fake.uploadSnapshot()
	r.Empty(uploads)
}

// TestResultWithoutCaptureIsUnchanged is the no-op control: a result carrying no
// screenshot at all must reach the server exactly as before, and provoke no
// upload request.
func TestResultWithoutCaptureIsUnchanged(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	fake := newCaptureServer(t)
	wsBackend := newCaptureBackend(t, fake)

	r.NoError(wsBackend.SubmitResult(ctx, testJob(), "worker-1", captureSubmitReq(&checkerdef.Diagnostics{
		FailureResponse: &checkerdef.FailureResponse{Body: bodyMarker},
	})))

	raw := string(fake.lastResult(t))
	r.Contains(raw, bodyMarker)
	r.NotContains(raw, "screenshot")

	time.Sleep(200 * time.Millisecond)

	uploads, _ := fake.uploadSnapshot()
	r.Empty(uploads)
}

// waitForUpload blocks until `want` uploads have landed, or fails the test.
func waitForUpload(t *testing.T, fake *captureServer, want int) {
	t.Helper()

	deadline := time.After(10 * time.Second)

	for {
		uploads, _ := fake.uploadSnapshot()
		if len(uploads) >= want {
			return
		}

		select {
		case <-fake.uploaded:
		case <-deadline:
			t.Fatalf("timed out waiting for %d upload(s), got %d", want, len(uploads))
		}
	}
}
