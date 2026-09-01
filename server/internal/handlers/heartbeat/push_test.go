package heartbeat_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/heartbeat"
	"github.com/fclairamb/solidping/server/internal/heartbeatpush"
)

// mustJSON renders a value as JSON so a test can assert a secret appears
// NOWHERE in it, whatever nesting it might have hidden in.
func mustJSON(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	require.NoError(t, err)

	return string(encoded)
}

// handle parses a beat line and pushes it through the push ingest, returning
// whether it was accepted.
func (s *heartbeatSetup) handle(t *testing.T, line string) bool {
	t.Helper()

	beat, err := heartbeatpush.ParseLine([]byte(line))
	require.NoError(t, err, "line must parse: %q", line)

	accepted, err := s.svc.HandleBeat(t.Context(), beat, "203.0.113.7", "udp")
	require.NoError(t, err)

	return accepted
}

// beatCount returns how many raw results the setup's check has ACQUIRED from
// beats, so a test can prove a rejected beat marked NOTHING alive rather than
// merely returning false. The lifecycle marker CreateCheck writes at creation
// time is excluded — it is not a beat.
func (s *heartbeatSetup) beatCount(t *testing.T) int {
	t.Helper()

	response, err := s.dbSvc.ListResults(t.Context(), &models.ListResultsFilter{
		OrganizationUID: s.org.UID,
		CheckUIDs:       []string{s.check.UID},
		PeriodTypes:     []string{"raw"},
		ExcludeStatuses: []int{int(models.ResultStatusCreated)},
	})
	require.NoError(t, err)

	return len(response.Results)
}

// checkSlug is the check's URL identifier, exactly what a device puts on the
// wire after the slash.
func (s *heartbeatSetup) checkSlug() string {
	if s.check.Slug == nil {
		return s.check.UID
	}

	return *s.check.Slug
}

func sp1(org, identifier, token, annotation string) string {
	line := "SP1 " + org + "/" + identifier + " " + token
	if annotation != "" {
		line += " " + annotation
	}

	return line
}

// TestHandleBeatAcceptsSP1 is the positive control every SP1 negative below is
// a one-field mutation of.
func TestHandleBeatAcceptsSP1(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	r.True(s.handle(t, sp1(s.org.Slug, s.checkSlug(), testToken, "")))
	r.Equal(1, s.beatCount(t))

	output := s.lastOutput(t)
	r.Equal("Heartbeat received", output["message"])
	r.Equal("203.0.113.7", output["remoteAddr"])

	data, ok := output["data"].(map[string]any)
	r.True(ok)
	r.Equal("udp", data["transport"])
}

// TestHandleBeatRejectsWrongToken — the plaintext form still has to check the
// token, and a rejection must mark nothing alive.
func TestHandleBeatRejectsWrongToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	r.False(s.handle(t, sp1(s.org.Slug, s.checkSlug(), "not-the-token", "")))
	r.Zero(s.beatCount(t), "a rejected beat must not record a result")

	// Positive control: the same line with the right token IS accepted, so the
	// assertion above cannot pass because the whole path is broken.
	r.True(s.handle(t, sp1(s.org.Slug, s.checkSlug(), testToken, "")))
	r.Equal(1, s.beatCount(t))
}

// TestHandleBeatRejectsSP1WhenRequireHMAC is the core of the option: on a
// check that demands signatures, a plaintext-token beat is refused and marks
// NOTHING alive — otherwise turning the option on would be cosmetic.
func TestHandleBeatRejectsSP1WhenRequireHMAC(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	// Positive control first: with the option off, this exact line is accepted.
	r.True(s.handle(t, sp1(s.org.Slug, s.checkSlug(), testToken, "")))
	r.Equal(1, s.beatCount(t))

	s.check.Config["require_hmac"] = true
	cfg := s.check.Config
	r.NoError(s.dbSvc.UpdateCheck(t.Context(), s.check.UID, &models.CheckUpdate{Config: &cfg}))

	r.False(s.handle(t, sp1(s.org.Slug, s.checkSlug(), testToken, "")))
	r.Equal(1, s.beatCount(t), "the SP1 beat must not have marked the check alive")

	// And SP2 still works on the same check, so require_hmac refuses the weak
	// form rather than breaking the check.
	r.True(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, 0, 1, "")))
	r.Equal(2, s.beatCount(t))
}

// TestHandleBeatRejectsUnreadableRequireHMACStrictly — a config whose
// require_hmac cannot be read is treated as STRICT. The safe direction for an
// unreadable security setting is to refuse the weaker message form.
func TestHandleBeatRejectsUnreadableRequireHMACStrictly(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	s.check.Config["require_hmac"] = "true" // a string, not a boolean
	cfg := s.check.Config
	r.NoError(s.dbSvc.UpdateCheck(t.Context(), s.check.UID, &models.CheckUpdate{Config: &cfg}))

	r.False(s.handle(t, sp1(s.org.Slug, s.checkSlug(), testToken, "")))
	r.Zero(s.beatCount(t))

	// Positive control: SP2 on the same unreadable config is still accepted,
	// proving the rejection is about the message form and not a dead path.
	r.True(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, 0, 1, "")))
	r.Equal(1, s.beatCount(t))
}

// TestHandleBeatAcceptsSP2 is the SP2 positive control.
func TestHandleBeatAcceptsSP2(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	line := heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, time.Now().Unix(), 42, "")
	r.True(s.handle(t, line))
	r.Equal(1, s.beatCount(t))
}

// TestHandleBeatRejectsReplayedSP2 is the replay negative: the SAME signed
// datagram, resent, is refused because its counter is no longer strictly
// greater. This is what SP2 buys over SP1.
func TestHandleBeatRejectsReplayedSP2(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	line := heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, 0, 100, "")

	r.True(s.handle(t, line), "positive control: the beat is valid the first time")
	r.Equal(1, s.beatCount(t))

	r.False(s.handle(t, line), "a byte-identical replay must be refused")
	r.Equal(1, s.beatCount(t), "the replay must not have marked the check alive")

	// An older counter is refused too.
	r.False(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, 0, 99, "")))
	r.Equal(1, s.beatCount(t))

	// Positive control: moving forward still works.
	r.True(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, 0, 101, "")))
	r.Equal(2, s.beatCount(t))
}

// TestHandleBeatRejectsBadMAC covers a forged signature and a signature made
// with the wrong key.
func TestHandleBeatRejectsBadMAC(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	r.False(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), "wrong-key", 0, 5, "")))
	r.Zero(s.beatCount(t))

	// A tampered annotation on an otherwise valid line breaks the MAC, because
	// the MAC is the LAST token and therefore covers the annotation.
	valid := heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, 0, 5, "volts=3.71")
	tampered := valid[:len(valid)-33] // drop " <mac>"
	tampered = tampered[:len(tampered)-len("volts=3.71")] + "volts=9.99" + valid[len(valid)-33:]
	r.False(s.handle(t, tampered))
	r.Zero(s.beatCount(t))

	// Positive control.
	r.True(s.handle(t, valid))
	r.Equal(1, s.beatCount(t))
}

// TestHandleBeatRejectsAFailedMACWithoutMovingTheCounter proves the ordering
// of the SP2 checks: an unauthenticated caller must never be able to advance a
// check's replay counter, which would lock the real device out until it
// rebooted.
func TestHandleBeatRejectsAFailedMACWithoutMovingTheCounter(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	r.False(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), "wrong-key", 0, 1_000_000, "")))

	_, ok, err := s.dbSvc.GetHeartbeatCounter(t.Context(), s.check.UID)
	r.NoError(err)
	r.False(ok, "a forged beat must not create counter state")

	// The real device, far below the forged counter, is still accepted.
	r.True(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, 0, 7, "")))
	r.Equal(1, s.beatCount(t))
}

// TestHandleBeatRejectsStaleTimestamp bounds the damage of lost counter state.
func TestHandleBeatRejectsStaleTimestamp(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)
	s.svc.SetPushTimestampWindow(time.Minute)

	stale := time.Now().Add(-10 * time.Minute).Unix()
	r.False(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, stale, 1, "")))
	r.Zero(s.beatCount(t))

	// A far-future timestamp is refused symmetrically.
	future := time.Now().Add(10 * time.Minute).Unix()
	r.False(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, future, 2, "")))
	r.Zero(s.beatCount(t))

	// Positive control: a fresh timestamp is accepted, and so is ts=0, which
	// is how a device with no clock at all reports.
	r.True(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, time.Now().Unix(), 3, "")))
	r.True(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, s.checkSlug(), testToken, 0, 4, "")))
	r.Equal(2, s.beatCount(t))
}

// TestHandleBeatRejectsUnknownTargets — an unknown org and an unknown check
// must be refused exactly like a bad MAC, with no result written and no error
// that a caller could observe.
func TestHandleBeatRejectsUnknownTargets(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	r.False(s.handle(t, sp1("no-such-org", s.checkSlug(), testToken, "")))
	r.False(s.handle(t, sp1(s.org.Slug, "no-such-check", testToken, "")))
	r.False(s.handle(t, heartbeatpush.SignSP2("no-such-org", s.checkSlug(), testToken, 0, 1, "")))
	r.False(s.handle(t, heartbeatpush.SignSP2(s.org.Slug, "no-such-check", testToken, 0, 1, "")))
	r.Zero(s.beatCount(t))

	// Positive control.
	r.True(s.handle(t, sp1(s.org.Slug, s.checkSlug(), testToken, "")))
}

// TestHandleBeatRejectsNonHeartbeatCheck — the push transports must not become
// a way to write results onto an HTTP or TCP check.
func TestHandleBeatRejectsNonHeartbeatCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	other := models.NewCheck(s.org.UID, "not-a-heartbeat", "http")
	other.Config["token"] = testToken
	r.NoError(s.dbSvc.CreateCheck(t.Context(), other))

	beat, err := heartbeatpush.ParseLine([]byte(sp1(s.org.Slug, other.UID, testToken, "")))
	r.NoError(err)

	accepted, err := s.svc.HandleBeat(t.Context(), beat, "203.0.113.7", "udp")
	r.NoError(err)
	r.False(accepted)
}

// TestHandleBeatStoresAnnotation proves the split: numeric fields become
// first-class metrics, everything else lands under output.data.annotation.
func TestHandleBeatStoresAnnotation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	r.True(s.handle(t, sp1(s.org.Slug, s.checkSlug(), testToken, "started volts=3.71 rssi=-67 fw=1.4.2")))

	result := s.lastResult(t)
	r.InDelta(3.71, result.Metrics["volts"], 0.0001)
	r.InDelta(-67.0, result.Metrics["rssi"], 0.0001)
	r.NotContains(result.Metrics, "fw", "a non-numeric field is not a time series")

	data, ok := result.Output["data"].(map[string]any)
	r.True(ok)

	annotation, ok := data["annotation"].(map[string]any)
	r.True(ok)
	r.Equal("started", annotation["status"])

	fields, ok := annotation["fields"].(map[string]any)
	r.True(ok)
	r.Equal("1.4.2", fields["fw"])
}

// TestHandleBeatAcceptsAMalformedAnnotation is the aliveness-first rule: a
// firmware typo in a key name must never make a healthy device look dead.
func TestHandleBeatAcceptsAMalformedAnnotation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	r.True(s.handle(t, sp1(s.org.Slug, s.checkSlug(), testToken, "Volts=3.71 oops")))
	r.Equal(1, s.beatCount(t), "the beat still counts")

	result := s.lastResult(t)
	r.Empty(result.Metrics)

	data, _ := result.Output["data"].(map[string]any)
	annotation, ok := data["annotation"].(map[string]any)
	r.True(ok)
	r.Equal("Volts=3.71 oops", annotation["raw"])
}

// TestHandleBeatNeverStoresTheToken — the beat line carries the token under
// SP1, and nothing that reaches the results table may echo it. A result row is
// visible to every member of the organization and travels into exports.
func TestHandleBeatNeverStoresTheToken(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s := newHeartbeatSetup(t)

	r.True(s.handle(t, sp1(s.org.Slug, s.checkSlug(), testToken, "started volts=3.7")))

	result := s.lastResult(t)
	r.NotContains(mustJSON(t, result.Output), testToken)
	r.NotContains(mustJSON(t, result.Metrics), testToken)
}

// TestHandleBeatIsAnnotationParityWithHTTP — the same annotation string sent
// over HTTPS lands in exactly the same two places, so the field means one
// thing across all three transports.
func TestHandleBeatIsAnnotationParityWithHTTP(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	push := newHeartbeatSetup(t)
	r.True(push.handle(t, sp1(push.org.Slug, push.checkSlug(), testToken, "started volts=3.71 fw=1.4.2")))
	pushResult := push.lastResult(t)

	viaHTTP := newHeartbeatSetup(t)
	r.NoError(viaHTTP.svc.Receive(t.Context(), &heartbeat.Request{
		OrgSlug:    viaHTTP.org.Slug,
		Identifier: viaHTTP.check.UID,
		Token:      testToken,
		HTTPMethod: "GET",
		Annotation: "started volts=3.71 fw=1.4.2",
	}))
	httpResult := viaHTTP.lastResult(t)

	r.Equal(pushResult.Metrics, httpResult.Metrics)

	pushData, _ := pushResult.Output["data"].(map[string]any)
	httpData, _ := httpResult.Output["data"].(map[string]any)
	r.Equal(pushData["annotation"], httpData["annotation"])
}
