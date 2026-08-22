package agentattach

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	agentcrypto "github.com/fclairamb/solidping/server/internal/agents"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// uploadPath is the route this endpoint serves. It is part of the signed
// challenge, so it must match what the agent signs byte for byte.
const uploadPath = "/api/v1/agent/attachments"

// pngBody is a minimal payload carrying the real PNG magic. The sniffer looks
// at these bytes, so a fixture that lies about its type would not get through
// — which is the point of several cases below.
//
//nolint:gochecknoglobals // an immutable fixture Go cannot express as const
var pngBody = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'p', 'i', 'x'}

// recordingWriter captures what the handler asked to store.
type recordingWriter struct {
	topic   string
	orgUID  string
	body    []byte
	details models.JSONMap
	calls   int
}

func (w *recordingWriter) CreateAttachment(
	_ context.Context, orgUID uuid.UUID, _, _, topic string,
	details models.JSONMap, body []byte,
) (string, error) {
	w.calls++
	w.orgUID = orgUID.String()
	w.topic = topic
	w.details = details
	w.body = body

	return "file-abc", nil
}

// fixture is one fully wired endpoint plus the database behind it.
type fixture struct {
	handler *Handler
	writer  *recordingWriter
	dbSvc   *sqlite.Service
	org     *models.Organization
	check   *models.Check
	inc     *models.Incident
	agent   *models.Agent
	keys    *agentcrypto.AgentKeys
}

// newFixture builds an org with a browser check pinned to region "eu-west",
// an open incident on it, and an ORG agent bound to that same org and region —
// i.e. the one shape that is supposed to be allowed.
func newFixture(t *testing.T) *fixture {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "shop", "browser")
	check.Regions = []string{"eu-west"}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	inc := models.NewIncident(org.UID, check.UID, time.Now(), "shop is down")
	r.NoError(dbSvc.CreateIncident(ctx, inc))

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	agent := models.NewAgent(
		org.UID, "eu-west", "acme-agent",
		keys.Ed25519PublicKey, keys.X25519Recipient,
		agentcrypto.KeyFingerprint(keys.Ed25519PublicKey),
	)
	insertAgent(t, dbSvc, agent)

	registry := NewRegistry()
	registry.Register(models.AttachmentEntityIncidents, NewIncidentAuthorizer(dbSvc))

	writer := &recordingWriter{}
	handler := NewHandler(
		&config.Config{}, dbSvc, writer, registry,
		agentcrypto.NewStoredNonceGuard(dbSvc, db.ErrAgentNonceReplayed), nil,
	)

	return &fixture{
		handler: handler, writer: writer, dbSvc: dbSvc,
		org: org, check: check, inc: inc, agent: agent, keys: keys,
	}
}

// insertAgent writes an agent row directly — enrollment goes through a
// one-shot token the endpoint under test has nothing to do with.
func insertAgent(t *testing.T, dbSvc *sqlite.Service, agent *models.Agent) {
	t.Helper()

	_, err := dbSvc.DB().ExecContext(t.Context(),
		`insert into agents (uid, organization_uid, kind, region, name,
		     ed25519_public_key, x25519_public_key, fingerprint, status)
		 values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agent.UID, agent.OrganizationUID, agent.Kind, agent.Region, agent.Name,
		agent.Ed25519PublicKey, agent.X25519PublicKey, agent.Fingerprint, agent.Status,
	)
	require.NoError(t, err)
}

// requestOpts is the set of things a test wants to bend away from a
// known-good, correctly-signed request.
type requestOpts struct {
	topic       string
	body        []byte
	contentType string
	// nonce reuses an exact nonce, for the replay case.
	nonce string
	// timestamp overrides the signed timestamp, for the skew case.
	timestamp string
	// corruptSignature replaces the signature with a valid-looking wrong one.
	corruptSignature bool
	// agentUID overrides which agent the request claims to be.
	agentUID string
}

// signedRequest builds a request that is correct in every way the caller did
// not deliberately break.
func (f *fixture) signedRequest(t *testing.T, opts requestOpts) *http.Request {
	t.Helper()

	r := require.New(t)

	if opts.topic == "" {
		opts.topic = models.AttachmentTopic(
			models.AttachmentEntityIncidents, f.inc.UID, models.AttachmentKindScreenshot,
		)
	}

	if opts.body == nil {
		opts.body = pngBody
	}

	if opts.contentType == "" {
		opts.contentType = mimeTypePNG
	}

	if opts.nonce == "" {
		opts.nonce = uuid.New().String()
	}

	if opts.timestamp == "" {
		opts.timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	agentUID := opts.agentUID
	if agentUID == "" {
		agentUID = f.agent.UID
	}

	signature, err := f.keys.Sign(http.MethodPost, uploadPath, opts.timestamp, opts.nonce)
	r.NoError(err)

	if opts.corruptSignature {
		// A syntactically valid signature over a DIFFERENT challenge: this is
		// what a replayed-from-another-route capture looks like, and it must
		// be rejected by verification rather than by a decode error.
		signature, err = f.keys.Sign(http.MethodGet, "/api/v1/agent/ws", opts.timestamp, opts.nonce)
		r.NoError(err)
	}

	req := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, uploadPath, bytes.NewReader(opts.body),
	)
	req.Header.Set(headerAgentUID, agentUID)
	req.Header.Set(headerTimestamp, opts.timestamp)
	req.Header.Set(headerNonce, opts.nonce)
	req.Header.Set(headerSignature, signature)
	req.Header.Set(headerTopic, opts.topic)
	req.Header.Set("Content-Type", opts.contentType)

	return req
}

// do runs one request through the handler.
func (f *fixture) do(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	require.NoError(t, f.handler.Upload(rec, req))

	return rec
}

// TestUploadHappyPath is the positive control every rejection below depends
// on: a correctly signed agent uploading a PNG to an incident of its own org,
// in a region its check is served from, gets a 201 and a file uid.
func TestUploadHappyPath(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFixture(t)

	rec := f.do(t, f.signedRequest(t, requestOpts{}))
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())

	var resp UploadResponse
	r.NoError(json.Unmarshal(rec.Body.Bytes(), &resp))
	r.Equal("file-abc", resp.FileUID)

	r.Equal(1, f.writer.calls)
	r.Equal(pngBody, f.writer.body)
	// The org is the INCIDENT's, resolved server-side — never the request's.
	r.Equal(f.org.UID, f.writer.orgUID)
	r.Equal(
		models.AttachmentTopic(models.AttachmentEntityIncidents, f.inc.UID, models.AttachmentKindScreenshot),
		f.writer.topic,
	)
	// The region is stamped from the AGENT's binding, not from the request.
	r.Equal("eu-west", f.writer.details[models.AttachmentDetailRegion])
	r.Equal(models.AttachmentTriggerAgentUpload, f.writer.details[models.AttachmentDetailTrigger])
}

// TestUploadRejectsBadSignature covers the whole authentication surface: a
// missing header, a signature over a different challenge, an out-of-skew
// timestamp, and an unknown agent are each rejected before anything is stored.
func TestUploadRejectsBadSignature(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// build returns the (deliberately broken) request to send.
		build func(t *testing.T, f *fixture) *http.Request
		want  int
	}{
		{"missing headers", func(t *testing.T, f *fixture) *http.Request {
			t.Helper()

			req := f.signedRequest(t, requestOpts{})
			req.Header.Del(headerSignature)

			return req
		}, http.StatusUnauthorized},
		{"signature over another challenge", func(t *testing.T, f *fixture) *http.Request {
			t.Helper()

			return f.signedRequest(t, requestOpts{corruptSignature: true})
		}, http.StatusUnauthorized},
		{"timestamp outside the skew", func(t *testing.T, f *fixture) *http.Request {
			t.Helper()

			return f.signedRequest(t, requestOpts{
				timestamp: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
			})
		}, http.StatusUnauthorized},
		{"unknown agent", func(t *testing.T, f *fixture) *http.Request {
			t.Helper()

			return f.signedRequest(t, requestOpts{agentUID: uuid.New().String()})
		}, http.StatusUnauthorized},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f := newFixture(t)

			rec := f.do(t, tc.build(t, f))
			r.Equal(tc.want, rec.Code, rec.Body.String())
			r.Zero(f.writer.calls, "nothing may be stored for a rejected request")
		})
	}
}

// TestUploadRejectsReplay proves the nonce guard is real: the exact same
// signed request, sent twice, succeeds once.
func TestUploadRejectsReplay(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFixture(t)

	nonce := uuid.New().String()
	timestamp := time.Now().UTC().Format(time.RFC3339)

	first := f.do(t, f.signedRequest(t, requestOpts{nonce: nonce, timestamp: timestamp}))
	r.Equal(http.StatusCreated, first.Code, "positive control: the first use works")

	second := f.do(t, f.signedRequest(t, requestOpts{nonce: nonce, timestamp: timestamp}))
	r.Equal(http.StatusUnauthorized, second.Code)
	r.Equal(1, f.writer.calls, "the replay stored nothing")
}

// TestUploadRejectsForeignAndMalformedTopics is the authorization core: the
// topic is attacker-controlled, so every shape of "not yours" is refused —
// and all of them with the SAME status and code, so the endpoint is not an
// oracle for which incident uids exist elsewhere.
func TestUploadRejectsForeignAndMalformedTopics(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFixture(t)

	// Another tenant's incident, on a check in the same region.
	other := models.NewOrganization("other", "Other")
	r.NoError(f.dbSvc.CreateOrganization(t.Context(), other))

	otherCheck := models.NewCheck(other.UID, "site", "browser")
	otherCheck.Regions = []string{"eu-west"}
	r.NoError(f.dbSvc.CreateCheck(t.Context(), otherCheck))

	otherIncident := models.NewIncident(other.UID, otherCheck.UID, time.Now(), "site is down")
	r.NoError(f.dbSvc.CreateIncident(t.Context(), otherIncident))

	// Our own org, but a check served only from a region this agent is not in.
	elsewhereCheck := models.NewCheck(f.org.UID, "eu-only", "browser")
	elsewhereCheck.Regions = []string{"us-east"}
	r.NoError(f.dbSvc.CreateCheck(t.Context(), elsewhereCheck))

	elsewhereIncident := models.NewIncident(f.org.UID, elsewhereCheck.UID, time.Now(), "down")
	r.NoError(f.dbSvc.CreateIncident(t.Context(), elsewhereIncident))

	incidentTopic := func(uid string) string {
		return models.AttachmentTopic(models.AttachmentEntityIncidents, uid, models.AttachmentKindScreenshot)
	}

	cases := []struct{ name, topic string }{
		{"another tenant's incident", incidentTopic(otherIncident.UID)},
		{"an incident this region does not serve", incidentTopic(elsewhereIncident.UID)},
		{"an incident that does not exist", incidentTopic(uuid.New().String())},
		{"an unregistered entity", "checks/" + f.check.UID + "/screenshot"},
		{"a kind nobody allowlisted", "incidents/" + f.inc.UID + "/har"},
		{"too few segments", "incidents/" + f.inc.UID},
		{"too many segments", "incidents/" + f.inc.UID + "/screenshot/extra"},
		{"an empty segment", "incidents//screenshot"},
		{"a traversal attempt", "incidents/../screenshot"},
		{"an empty topic", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rr := require.New(t)
			fx := newFixture(t)

			req := fx.signedRequest(t, requestOpts{topic: "placeholder/x/y"})
			req.Header.Set(headerTopic, tc.topic)

			rec := fx.do(t, req)
			rr.Equal(http.StatusForbidden, rec.Code, rec.Body.String())
			rr.Contains(rec.Body.String(), string(errCodeBadTopic),
				"every topic failure answers identically — no oracle")
			rr.Zero(fx.writer.calls)
		})
	}
}

// TestUploadRejectsBadContent proves the format allowlist is enforced against
// the BYTES. A declared image/png carrying HTML is the case that matters:
// believing the header would store a scriptable document that our own origin
// later serves back.
func TestUploadRejectsBadContent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		body        []byte
		contentType string
		want        int
	}{
		{
			"html lying about being a png", []byte("<html><script>alert(1)</script>"), "image/png",
			http.StatusUnsupportedMediaType,
		},
		{
			"a type that is not on the allowlist", pngBody, "image/svg+xml",
			http.StatusUnsupportedMediaType,
		},
		{
			"real png bytes under the wrong declared type", pngBody, "application/octet-stream",
			http.StatusUnsupportedMediaType,
		},
		{"an empty body", []byte{}, mimeTypePNG, http.StatusBadRequest},
		{
			"a body over the cap", make([]byte, MaxUploadBytes+1), "image/png",
			http.StatusRequestEntityTooLarge,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f := newFixture(t)

			rec := f.do(t, f.signedRequest(t, requestOpts{
				body: tc.body, contentType: tc.contentType,
			}))
			r.Equal(tc.want, rec.Code, rec.Body.String())
			r.Zero(f.writer.calls)
		})
	}
}

// TestUploadRateLimitsPerAgent proves the bucket exists and is keyed on the
// authenticated identity. Beyond the burst the endpoint answers 429 without
// storing anything.
func TestUploadRateLimitsPerAgent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFixture(t)

	for i := range uploadBurst {
		rec := f.do(t, f.signedRequest(t, requestOpts{}))
		r.Equal(http.StatusCreated, rec.Code, "request %d must be inside the burst", i)
	}

	rec := f.do(t, f.signedRequest(t, requestOpts{}))
	r.Equal(http.StatusTooManyRequests, rec.Code)
	r.Equal(uploadBurst, f.writer.calls, "the throttled request stored nothing")
}

// TestRevokedAgentCannotUpload: revocation is not only a claim-path control.
func TestRevokedAgentCannotUpload(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFixture(t)

	_, err := f.dbSvc.DB().ExecContext(t.Context(),
		`update agents set status = ? where uid = ?`, models.AgentStatusRevoked, f.agent.UID)
	r.NoError(err)

	rec := f.do(t, f.signedRequest(t, requestOpts{}))
	r.Equal(http.StatusForbidden, rec.Code)
	r.Zero(f.writer.calls)
}

// TestSystemAgentMayAttachAcrossOrgs pins the other legitimate shape: a
// platform agent has no owning org and serves a shared cloud region for every
// tenant, so the org must be resolved FROM the incident rather than from a
// binding it does not have. The region check still applies.
func TestSystemAgentMayAttachAcrossOrgs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFixture(t)

	keys, err := agentcrypto.GenerateAgentKeys()
	r.NoError(err)

	system := models.NewSystemAgent(
		"eu-west", "fly-eu", keys.Ed25519PublicKey, keys.X25519Recipient,
		agentcrypto.KeyFingerprint(keys.Ed25519PublicKey),
	)
	insertAgent(t, f.dbSvc, system)

	f.agent = system
	f.keys = keys

	rec := f.do(t, f.signedRequest(t, requestOpts{}))
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
	r.Equal(f.org.UID, f.writer.orgUID, "the org comes from the incident, not the agent")
}

// TestRegionlessCheckIsServedEverywhere: a check nobody pinned to a region
// runs everywhere, so any agent may attach to its incident. This is the
// permissive branch of regionServes, stated so a future tightening is a
// deliberate change rather than a silent one.
func TestRegionlessCheckIsServedEverywhere(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newFixture(t)

	_, err := f.dbSvc.DB().ExecContext(t.Context(),
		`update checks set regions = null where uid = ?`, f.check.UID)
	r.NoError(err)

	rec := f.do(t, f.signedRequest(t, requestOpts{}))
	r.Equal(http.StatusCreated, rec.Code, rec.Body.String())
}
