package audit_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeStore is a slice-backed audit.EventStore. Deliberately not a mock: every
// assertion below is about what actually landed in a row, so the test needs the
// rows, not a record of calls.
type fakeStore struct {
	mu       sync.Mutex
	events   []*models.Event
	failNext bool
}

func (f *fakeStore) CreateEvent(_ context.Context, event *models.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failNext {
		f.failNext = false

		return errors.New("insert refused")
	}

	f.events = append(f.events, event)

	return nil
}

func (f *fakeStore) UpdateEventPayload(_ context.Context, uid string, payload models.JSONMap) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, event := range f.events {
		if event.UID == uid {
			event.Payload = payload

			return nil
		}
	}

	return errors.New("no such event")
}

func (f *fakeStore) all() []*models.Event {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]*models.Event, len(f.events))
	copy(out, f.events)

	return out
}

const testOrg = "org-1"

// TestRedactDropsSecretsAndKeepsEverythingElse is the core redaction guard.
//
// The positive control is not decoration: a Redact that returned an empty map
// would pass every "the secret is gone" assertion on its own, so the same test
// insists the ordinary fields survive intact.
func TestRedactDropsSecretsAndKeepsEverythingElse(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	out := audit.Redact(models.JSONMap{
		"name":           "acme webhook",
		"integration_ty": "webhook",
		"enabled":        true,
		"count":          7,
		"password":       "hunter2",
		"auth_token":     "AC-secret",
		"webhook_url":    "https://hooks.example.com/T000/B000/XXXX",
		"api_key":        "sk-live-123",
		"bot_token":      "xoxb-1-2-3",
		"privateKey":     "-----BEGIN PRIVATE KEY-----",
		"password_hash":  "$argon2id$v=19$...",
		"session_cookie": "abc",
		"signature":      "v1,deadbeef",
	})

	// Positive control: the non-sensitive half is all still there.
	r.Equal("acme webhook", out["name"])
	r.Equal("webhook", out["integration_ty"])
	r.Equal(true, out["enabled"])
	r.Equal(7, out["count"])

	for _, key := range []string{
		"password", "auth_token", "webhook_url", "api_key", "bot_token",
		"privateKey", "password_hash", "session_cookie", "signature",
	} {
		_, present := out[key]
		r.Falsef(present, "%s must never reach the audit trail", key)
	}

	// And no value survived under a different key either — the strongest form
	// of "the secret is not stored".
	for _, value := range out {
		text, ok := value.(string)
		if !ok {
			continue
		}

		r.NotContains(text, "hunter2")
		r.NotContains(text, "AC-secret")
		r.NotContains(text, "sk-live-123")
		r.NotContains(text, "xoxb-1-2-3")
	}
}

// TestRedactReachesNestedSecrets proves the walk is recursive. A denylist that
// only inspected top-level keys would be defeated by one level of nesting,
// which is exactly the shape an integration's settings map has.
func TestRedactReachesNestedSecrets(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	out := audit.Redact(models.JSONMap{
		"settings": map[string]any{
			"region":     "eu-west",
			"auth_token": "AC-secret",
			"nested": map[string]any{
				"client_secret": "shhh",
				"kept":          "visible",
			},
		},
	})

	settings, ok := out["settings"].(map[string]any)
	r.True(ok, "the settings map must survive as a map")
	// Positive control.
	r.Equal("eu-west", settings["region"])

	_, present := settings["auth_token"]
	r.False(present)

	nested, ok := settings["nested"].(map[string]any)
	r.True(ok)
	r.Equal("visible", nested["kept"])

	_, present = nested["client_secret"]
	r.False(present)
}

// TestRedactKeepsTokenIdentityFields pins the spec's exception: token events
// store the token's NAME and PREFIX. Those key names contain "token", so
// without the allowlist the denylist would eat exactly the fields the spec
// asks for — and the failure would be silent.
func TestRedactKeepsTokenIdentityFields(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	out := audit.Redact(models.JSONMap{
		"token_name":   "ci-deploy",
		"token_prefix": "pat_abcd1234",
		"token_kind":   "pat",
		"token":        "pat_abcd1234_THE_ACTUAL_SECRET",
		"token_value":  "pat_abcd1234_THE_ACTUAL_SECRET",
	})

	r.Equal("ci-deploy", out["token_name"])
	r.Equal("pat_abcd1234", out["token_prefix"])
	r.Equal("pat", out["token_kind"])

	// The value itself, under any spelling, never lands.
	_, present := out["token"]
	r.False(present)
	_, present = out["token_value"]
	r.False(present)
}

// TestRedactTruncatesLongValues stops a whole config payload from being
// smuggled in under an innocuous key name.
func TestRedactTruncatesLongValues(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	long := strings.Repeat("x", 5000)
	out := audit.Redact(models.JSONMap{"note": long})

	stored, ok := out["note"].(string)
	r.True(ok)
	r.Less(len(stored), len(long))
	r.LessOrEqual(len(stored), 256)
}

// TestChangesReportsSensitiveFieldNamesWithoutValues is the rule that lets an
// audit trail say "the password was rotated" without saying what to.
func TestChangesReportsSensitiveFieldNamesWithoutValues(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	changed, safe := audit.Changes(
		map[string]any{"name": "old", "password_hash": "hash-a", "enabled": true},
		map[string]any{"name": "new", "password_hash": "hash-b", "enabled": true},
	)

	r.Equal([]string{"name", "password_hash"}, changed)

	// Positive control: the non-sensitive change carries its values.
	r.Equal(map[string]any{"from": "old", "to": "new"}, safe["name"])

	// The sensitive one names itself and nothing more.
	_, present := safe["password_hash"]
	r.False(present, "a sensitive field must never carry a from/to pair")

	// An untouched field is not a change.
	r.NotContains(changed, "enabled")
}

// TestChangesSkipsNonScalarValues keeps "full config payloads" out of the
// trail even when the field name is perfectly innocent.
func TestChangesSkipsNonScalarValues(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	changed, safe := audit.Changes(
		map[string]any{"config": map[string]any{"url": "https://a"}},
		map[string]any{"config": map[string]any{"url": "https://b"}},
	)

	r.Equal([]string{"config"}, changed)
	r.Nil(safe["config"])
}

// TestRecordAttachesActorAndProvenance covers the happy path end to end.
func TestRecordAttachesActorAndProvenance(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	store := &fakeStore{}

	ctx := audit.WithUser(
		audit.WithRequest(t.Context(), "203.0.113.7", "curl/8.7"),
		"user-1", models.ActorTypeAPIToken,
	)

	event := audit.Record(ctx, store, testOrg, models.EventTypeIntegrationCreated,
		audit.Target{Type: "integration", UID: "int-1", Name: "acme webhook"},
		models.JSONMap{"integration_type": "webhook"})

	r.NotNil(event)
	r.Equal(models.ActorTypeAPIToken, event.ActorType)
	r.Equal("user-1", *event.ActorUID)
	r.Equal("203.0.113.7", *event.SourceIP)
	r.Equal("curl/8.7", *event.UserAgent)
	r.Equal("integration", event.Payload[audit.PayloadKeyTargetType])
	r.Equal("int-1", event.Payload[audit.PayloadKeyTargetUID])
	r.Equal("acme webhook", event.Payload[audit.PayloadKeyTargetName])
	r.Len(store.all(), 1)
}

// TestRecordHonorsCaptureIPKnob proves audit.capture_ip actually suppresses the
// address rather than merely hiding it from the API — a GDPR-sensitive
// self-hoster's whole reason for the switch is that the value is not STORED.
//
// The positive control is the first half: with capture on, the same call does
// record the IP, so a Record that never wrote one would not pass.
func TestRecordHonorsCaptureIPKnob(t *testing.T) {
	// Not parallel: this flips a process-wide setting.
	r := require.New(t)

	original := audit.CaptureIPEnabled()
	t.Cleanup(func() { audit.SetCaptureIP(original) })

	ctx := audit.WithUser(
		audit.WithRequest(t.Context(), "203.0.113.7", "curl/8.7"),
		"user-1", models.ActorTypeUser,
	)

	audit.SetCaptureIP(true)

	with := audit.Record(ctx, &fakeStore{}, testOrg, models.EventTypeAuthLogout, audit.Target{}, nil)
	r.NotNil(with)
	r.NotNil(with.SourceIP, "positive control: capture on must record the address")
	r.Equal("203.0.113.7", *with.SourceIP)

	audit.SetCaptureIP(false)

	without := audit.Record(ctx, &fakeStore{}, testOrg, models.EventTypeAuthLogout, audit.Target{}, nil)
	r.NotNil(without)
	r.Nil(without.SourceIP, "capture off must leave source_ip unset")
	// The user agent is far less identifying and is deliberately unaffected.
	r.NotNil(without.UserAgent)
}

// TestRecordDefaultsToSystemWithNoActor makes sure an internal caller with no
// request context still produces a schema-valid row.
func TestRecordDefaultsToSystemWithNoActor(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	event := audit.Record(t.Context(), &fakeStore{}, testOrg,
		models.EventTypeConfigApplied, audit.Target{}, nil)

	r.NotNil(event)
	r.Equal(models.ActorTypeSystem, event.ActorType)
	r.True(event.ActorType.IsValid())
	r.Nil(event.ActorUID)
}

// TestRecordSwallowsStoreFailures — a failed audit write must never be able to
// fail the operation that caused it.
func TestRecordSwallowsStoreFailures(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	store := &fakeStore{failNext: true}

	r.NotPanics(func() {
		event := audit.Record(t.Context(), store, testOrg,
			models.EventTypeMemberRemoved, audit.Target{}, nil)
		r.Nil(event)
	})

	r.Empty(store.all())
}

// clockAt returns a fixed-time clock that a test can advance by hand. A
// windowing rule tested against the wall clock is a flake waiting for a slow CI
// box; this makes the window boundaries exact.
func clockAt(start time.Time) (func() time.Time, func(time.Duration)) {
	var (
		mu  sync.Mutex
		now = start
	)

	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()

			return now
		}, func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			now = now.Add(d)
		}
}

func failedLoginCtx(t *testing.T, ip string) context.Context {
	t.Helper()

	return audit.WithRequest(t.Context(), ip, "curl/8.7")
}

// TestFailedLoginFoldsRepeatsIntoOneEvent is the flood-control guard the spec
// asks for: repeats of the same (org, email, IP) inside the window must collapse
// into ONE row carrying a counter.
//
// The positive control is the last block: a different email, and a different
// IP, each still produce their own row. Without it a folder that simply dropped
// everything after the first attempt would pass.
func TestFailedLoginFoldsRepeatsIntoOneEvent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	store := &fakeStore{}

	now, advance := clockAt(time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC))
	folder := audit.NewFailedLoginFolder(10*time.Minute, 100)
	folder.SetClock(now)

	ctx := failedLoginCtx(t, "203.0.113.7")

	r.Equal(audit.OutcomeCreated, folder.Record(ctx, store, testOrg, "alice@acme.com", "invalid_credentials"))

	for i := 0; i < 46; i++ {
		advance(5 * time.Second)
		r.Equal(audit.OutcomeFolded,
			folder.Record(ctx, store, testOrg, "alice@acme.com", "invalid_credentials"))
	}

	events := store.all()
	r.Len(events, 1, "47 attempts on one (org,email,ip) must be ONE row")
	r.Equal(47, events[0].Payload[audit.PayloadKeyCount])
	r.NotEqual(events[0].Payload[audit.PayloadKeyFirstAt], events[0].Payload[audit.PayloadKeyLastAt])

	// Positive controls: folding is keyed, not a blanket mute.
	r.Equal(audit.OutcomeCreated,
		folder.Record(ctx, store, testOrg, "bob@acme.com", "invalid_credentials"))
	r.Equal(audit.OutcomeCreated,
		folder.Record(failedLoginCtx(t, "198.51.100.9"), store, testOrg, "alice@acme.com", "invalid_credentials"))
	r.Len(store.all(), 3)
}

// TestFailedLoginFoldingIsCaseInsensitiveOnEmail — "Alice@acme.com" and
// "alice@acme.com" are the same account, so an attacker cannot defeat folding
// by rotating the case.
func TestFailedLoginFoldingIsCaseInsensitiveOnEmail(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	store := &fakeStore{}

	now, advance := clockAt(time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC))
	folder := audit.NewFailedLoginFolder(10*time.Minute, 100)
	folder.SetClock(now)

	ctx := failedLoginCtx(t, "203.0.113.7")

	r.Equal(audit.OutcomeCreated, folder.Record(ctx, store, testOrg, "alice@acme.com", ""))
	advance(time.Second)
	r.Equal(audit.OutcomeFolded, folder.Record(ctx, store, testOrg, "ALICE@ACME.COM", ""))
	r.Len(store.all(), 1)
}

// TestFailedLoginWindowExpires proves the fold window is a window: past it, a
// new row starts, so a slow-drip attack does not hide forever inside one event.
func TestFailedLoginWindowExpires(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	store := &fakeStore{}

	now, advance := clockAt(time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC))
	folder := audit.NewFailedLoginFolder(10*time.Minute, 100)
	folder.SetClock(now)

	ctx := failedLoginCtx(t, "203.0.113.7")

	r.Equal(audit.OutcomeCreated, folder.Record(ctx, store, testOrg, "alice@acme.com", ""))

	advance(9 * time.Minute)
	r.Equal(audit.OutcomeFolded, folder.Record(ctx, store, testOrg, "alice@acme.com", ""))

	advance(2 * time.Minute) // now 11 minutes past the first sighting
	r.Equal(audit.OutcomeCreated, folder.Record(ctx, store, testOrg, "alice@acme.com", ""))

	r.Len(store.all(), 2)
}

// TestFailedLoginPerOrgHourlyCeilingBites is the second brake: an attacker who
// rotates the email on every attempt defeats folding by design, so the ceiling
// must bound the damage anyway.
//
// The positive control is the first loop — every attempt UNDER the ceiling is
// recorded — so a folder that simply refused everything would fail here.
func TestFailedLoginPerOrgHourlyCeilingBites(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	store := &fakeStore{}

	now, advance := clockAt(time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC))
	folder := audit.NewFailedLoginFolder(10*time.Minute, 5)
	folder.SetClock(now)

	ctx := failedLoginCtx(t, "203.0.113.7")

	// Positive control: under the ceiling, every distinct attempt lands.
	for i := 0; i < 5; i++ {
		r.Equalf(audit.OutcomeCreated,
			folder.Record(ctx, store, testOrg, uniqueEmail(i), ""),
			"attempt %d is under the ceiling and must be recorded", i)
	}

	r.Len(store.all(), 5)

	// Over it, nothing more is written.
	for i := 5; i < 40; i++ {
		r.Equal(audit.OutcomeDropped, folder.Record(ctx, store, testOrg, uniqueEmail(i), ""))
	}

	r.Len(store.all(), 5, "the hourly ceiling must bound rows CREATED, not merely slow them")

	// A different org has its own budget — one org under attack must not
	// silence another org's trail.
	r.Equal(audit.OutcomeCreated, folder.Record(ctx, store, "org-2", uniqueEmail(99), ""))

	// And the budget refills on the next hour.
	advance(time.Hour)
	r.Equal(audit.OutcomeCreated, folder.Record(ctx, store, testOrg, uniqueEmail(100), ""))
}

// TestFailedLoginFoldsDespiteTheCeiling — folding must stay free once a row
// exists, or a sustained attack against one account would stop being counted
// the moment the org's hourly budget ran out.
func TestFailedLoginFoldsDespiteTheCeiling(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	store := &fakeStore{}

	now, advance := clockAt(time.Date(2026, time.August, 21, 9, 0, 0, 0, time.UTC))
	folder := audit.NewFailedLoginFolder(30*time.Minute, 1)
	folder.SetClock(now)

	ctx := failedLoginCtx(t, "203.0.113.7")

	r.Equal(audit.OutcomeCreated, folder.Record(ctx, store, testOrg, "alice@acme.com", ""))
	r.Equal(audit.OutcomeDropped, folder.Record(ctx, store, testOrg, "bob@acme.com", ""))

	for i := 0; i < 10; i++ {
		advance(time.Second)
		r.Equal(audit.OutcomeFolded, folder.Record(ctx, store, testOrg, "alice@acme.com", ""))
	}

	events := store.all()
	r.Len(events, 1)
	r.Equal(11, events[0].Payload[audit.PayloadKeyCount])
}

// TestFailedLoginNeedsAnOrg — the events table is org-scoped, so an attempt
// that cannot be attributed to an org has nowhere to be recorded and must not
// invent a bucket.
func TestFailedLoginNeedsAnOrg(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	store := &fakeStore{}
	folder := audit.NewFailedLoginFolder(time.Minute, 10)

	r.Equal(audit.OutcomeDropped,
		folder.Record(failedLoginCtx(t, "203.0.113.7"), store, "", "alice@acme.com", ""))
	r.Empty(store.all())
}

// TestFailedLoginNeverStoresTheAttemptedPassword — the folded payload is
// assembled by this package, so the guard belongs here: whatever else it
// carries, a credential is not among it.
func TestFailedLoginNeverStoresTheAttemptedPassword(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	store := &fakeStore{}
	folder := audit.NewFailedLoginFolder(time.Minute, 10)

	r.Equal(audit.OutcomeCreated,
		folder.Record(failedLoginCtx(t, "203.0.113.7"), store, testOrg, "alice@acme.com", "invalid_credentials"))

	events := store.all()
	r.Len(events, 1)

	// Positive control: the fields the trail is FOR are present.
	r.Equal("alice@acme.com", events[0].Payload[audit.PayloadKeyEmail])
	r.Equal("invalid_credentials", events[0].Payload[audit.PayloadKeyReason])

	for key := range events[0].Payload {
		r.False(audit.IsSensitiveKey(key), "payload key %q would carry a secret", key)
	}
}

func uniqueEmail(i int) string {
	return "user" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + itoa(i) + "@acme.com"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	var digits []byte

	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}

	return string(digits)
}
