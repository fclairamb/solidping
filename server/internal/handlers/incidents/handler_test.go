package incidents_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

const ackHandlerTestSecret = "ack-handler-test-secret"

// ackHandlerFixture bundles an in-memory db.Service with an org, a check, and
// an unacknowledged incident, wired behind a real bunrouter so tests hit
// AcknowledgeIncidentByLink exactly the way a mail client's browser
// navigation would (through routing, not by calling the Go method directly).
type ackHandlerFixture struct {
	dbSvc    *sqlite.Service
	org      *models.Organization
	check    *models.Check
	incident *models.Incident
}

func newAckHandlerFixture(t *testing.T) *ackHandlerFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	r.NoError(dbSvc.Initialize(ctx))

	org := models.NewOrganization("ack-handler-test-org", "")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "ack-handler-test-check", "http")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	inc := models.NewIncident(org.UID, check.UID, time.Now().Add(-5*time.Minute), "ack-handler-test incident")
	r.NoError(dbSvc.CreateIncident(ctx, inc))

	return &ackHandlerFixture{dbSvc: dbSvc, org: org, check: check, incident: inc}
}

func newAckTestRouter(f *ackHandlerFixture) *bunrouter.Router {
	jobs := jobsvc.NewService(f.dbSvc.DB(), f.dbSvc, notifier.NewLocalEventNotifier(), nil)
	svc := incidents.NewService(f.dbSvc, jobs, clock.Real{}, nil)

	cfg := &config.Config{Auth: config.AuthConfig{JWTSecret: ackHandlerTestSecret}}
	handler := incidents.NewHandler(svc, cfg)

	router := bunrouter.New()
	router.GET("/orgs/:org/incidents/:uid/ack", handler.AcknowledgeIncidentByLink)

	return router
}

func doAckRequest(t *testing.T, router *bunrouter.Router, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	return rec
}

// TestHandler_AcknowledgeIncidentByLink_UnsubscribeTokenRejected is the
// handler-level companion (reverse direction) to
// unsubscribe.TestHandler_OneClickUnsubscribe_AckTokenRejected and to
// incidentlinks.TestPurposeSeparation_UnsubscribeTokenRejectedByVerify:
// spec 2026-07-05-10's acceptance criterion 5 requires BOTH cross-purpose
// directions be rejected, not just verified at the incidentlinks.verify()
// unit level. A validly-signed unsubscribe-purpose token must never be
// accepted by the ack endpoint just because it carries a correct HMAC over
// the same secret.
//
// This depends on handler.go's errors.Is(err, ErrAckTokenPurposeMismatch)
// branch (handler.go ~L214-223) bucketing ErrPurposeMismatch into the same
// 400 "invalid" HTML page as a tampered/malformed token. Without that
// branch, VerifyAckToken's ErrPurposeMismatch would fall through to the
// `default` case and this test would observe 500, not 400 — so the 400
// assertion here genuinely exercises the purpose-check, not just "any
// rejection".
func TestHandler_AcknowledgeIncidentByLink_UnsubscribeTokenRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newAckHandlerFixture(t)
	router := newAckTestRouter(f)

	unsubToken := incidentlinks.SignUnsubscribe(
		[]byte(ackHandlerTestSecret), f.org.Slug, "alice@example.com", f.check.UID, time.Now().Add(time.Hour))

	rec := doAckRequest(t, router, "/orgs/"+f.org.Slug+"/incidents/"+f.incident.UID+"/ack?token="+unsubToken)
	r.Equal(http.StatusBadRequest, rec.Code, "an unsubscribe token must never be accepted by the ack endpoint")

	got, err := f.dbSvc.GetIncident(t.Context(), f.org.UID, f.incident.UID)
	r.NoError(err)
	r.Nil(got.AcknowledgedAt, "a rejected token must not acknowledge the incident as a side effect")
	r.Nil(got.AcknowledgedBy)
}

// TestHandler_AcknowledgeIncidentByLink_ValidAckToken is the positive-path
// sibling: a correctly-purposed, validly-signed ack token for this exact
// incident succeeds and does record the acknowledgement. Kept alongside the
// purpose-mismatch test so a future refactor can't satisfy the 400 assertion
// above by making the endpoint reject everything.
func TestHandler_AcknowledgeIncidentByLink_ValidAckToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newAckHandlerFixture(t)
	router := newAckTestRouter(f)

	ackToken := incidentlinks.Sign(
		[]byte(ackHandlerTestSecret), f.incident.UID, "alice@example.com", time.Now().Add(time.Hour))

	rec := doAckRequest(t, router, "/orgs/"+f.org.Slug+"/incidents/"+f.incident.UID+"/ack?token="+ackToken)
	r.Equal(http.StatusOK, rec.Code)

	got, err := f.dbSvc.GetIncident(t.Context(), f.org.UID, f.incident.UID)
	r.NoError(err)
	r.NotNil(got.AcknowledgedAt, "a valid ack token must acknowledge the incident")
}

// TestHandler_AcknowledgeIncidentByLink_TamperedToken confirms a corrupted
// signature is rejected the same way (400), consistent with the neighboring
// unsubscribe handler's tampered-token coverage.
func TestHandler_AcknowledgeIncidentByLink_TamperedToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := newAckHandlerFixture(t)
	router := newAckTestRouter(f)

	ackToken := incidentlinks.Sign(
		[]byte(ackHandlerTestSecret), f.incident.UID, "alice@example.com", time.Now().Add(time.Hour))
	tampered := ackToken[:len(ackToken)-4] + "abcd"

	rec := doAckRequest(t, router, "/orgs/"+f.org.Slug+"/incidents/"+f.incident.UID+"/ack?token="+tampered)
	r.Equal(http.StatusBadRequest, rec.Code)

	got, err := f.dbSvc.GetIncident(t.Context(), f.org.UID, f.incident.UID)
	r.NoError(err)
	r.Nil(got.AcknowledgedAt)
}
