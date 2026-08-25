package audit

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// auth.login_failed is the one event type an unauthenticated stranger can
// cause at will, which makes a naive one-row-per-attempt implementation a
// write amplifier: a credential-stuffing run against a single org would insert
// millions of rows into the same table the org's real audit trail lives in,
// and the trail would be destroyed by the very attack it is supposed to
// record. Two independent brakes, both required:
//
//  1. FOLDING. Repeats of the same (org, email, source IP) inside
//     DefaultFoldWindow update the counter on the row already written instead
//     of writing another. One row then says "this address tried this account
//     47 times between 09:02 and 09:11", which is also a better artifact to
//     read than 47 rows.
//
//  2. A PER-ORG HOURLY CEILING on rows CREATED (folds are free — they touch a
//     row that already exists). A distributed attempt that rotates the email
//     or the IP on every try defeats folding by design; the ceiling is what
//     bounds it anyway. Once the hour's budget is spent, further first-sightings
//     are dropped with a single WARN.
//
// Both live in process memory. That is a deliberate limit, not an oversight:
// the state is a rate-limiter bucket, it is worthless after a restart, and
// pushing it into the database would reintroduce exactly the write volume this
// exists to avoid. A multi-replica deployment therefore enforces the ceiling
// per replica — still a hard bound, just N times the configured one.
const (
	// DefaultFoldWindow is how long repeats keep folding into one row.
	DefaultFoldWindow = 10 * time.Minute
	// DefaultMaxPerOrgPerHour caps newly CREATED auth.login_failed rows per
	// org per hour.
	DefaultMaxPerOrgPerHour = 60
	// PayloadKeyCount is the fold counter.
	PayloadKeyCount = "count"
	// PayloadKeyFirstAt / PayloadKeyLastAt bound the folded window.
	PayloadKeyFirstAt = "first_at"
	// PayloadKeyLastAt is the timestamp of the most recent folded attempt.
	PayloadKeyLastAt = "last_at"
	// PayloadKeyEmail is the account name that was tried.
	PayloadKeyEmail = "email"
	// PayloadKeyReason is a coarse machine reason ("invalid_credentials", …).
	// Never anything that distinguishes "no such user" from "wrong password"
	// to the outside world — this is a stored field, not a response.
	PayloadKeyReason = "reason"
)

type foldEntry struct {
	eventUID string
	count    int
	firstAt  time.Time
	lastAt   time.Time
}

type hourBucket struct {
	hourStart time.Time
	created   int
	warned    bool
}

// FailedLoginFolder implements the two brakes described above.
type FailedLoginFolder struct {
	mu               sync.Mutex
	window           time.Duration
	maxPerOrgPerHour int
	entries          map[string]*foldEntry
	buckets          map[string]*hourBucket
	now              func() time.Time
}

// NewFailedLoginFolder builds a folder. A non-positive window or ceiling falls
// back to the package default, so a mis-set config knob cannot accidentally
// disable a security control.
func NewFailedLoginFolder(window time.Duration, maxPerOrgPerHour int) *FailedLoginFolder {
	if window <= 0 {
		window = DefaultFoldWindow
	}

	if maxPerOrgPerHour <= 0 {
		maxPerOrgPerHour = DefaultMaxPerOrgPerHour
	}

	return &FailedLoginFolder{
		window:           window,
		maxPerOrgPerHour: maxPerOrgPerHour,
		entries:          make(map[string]*foldEntry),
		buckets:          make(map[string]*hourBucket),
		now:              time.Now,
	}
}

// SetClock injects a clock. Tests only — a wall-clock-dependent test of a
// windowing rule is a flake waiting to happen.
func (f *FailedLoginFolder) SetClock(now func() time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = now
}

// Outcome describes what Record did, so callers (and tests) can tell the three
// cases apart without re-reading the table.
type Outcome int

const (
	// OutcomeCreated means a new event row was written.
	OutcomeCreated Outcome = iota
	// OutcomeFolded means an existing row's counter was bumped.
	OutcomeFolded
	// OutcomeDropped means the per-org hourly ceiling refused the row.
	OutcomeDropped
)

// Record registers one failed login attempt and returns what it did.
//
// orgUID may be empty when the attempt could not even be resolved to an org;
// there is no org-scoped trail to write in that case and the attempt is
// dropped (the org-scoped events table has nowhere to put it).
func (f *FailedLoginFolder) Record(
	ctx context.Context,
	store EventStore,
	orgUID, email, reason string,
) Outcome {
	if store == nil || orgUID == "" {
		return OutcomeDropped
	}

	actor := ActorFromContext(ctx)
	key := foldKey(orgUID, email, actor.SourceIP)

	f.mu.Lock()

	now := f.now()
	f.evictExpiredLocked(now)

	if entry, ok := f.entries[key]; ok && now.Sub(entry.firstAt) < f.window && entry.eventUID != "" {
		entry.count++
		entry.lastAt = now
		payload := foldPayload(email, reason, entry)
		eventUID := entry.eventUID
		f.mu.Unlock()

		if err := store.UpdateEventPayload(ctx, eventUID, payload); err != nil {
			slog.ErrorContext(ctx, "Failed to fold auth.login_failed event",
				"error", err, "eventUid", eventUID)
		}

		return OutcomeFolded
	}

	if !f.allowCreateLocked(ctx, orgUID, now) {
		f.mu.Unlock()

		return OutcomeDropped
	}

	entry := &foldEntry{count: 1, firstAt: now, lastAt: now}
	f.entries[key] = entry
	f.mu.Unlock()

	event := Record(ctx, store, orgUID, models.EventTypeAuthLoginFailed,
		Target{Type: "user", Name: email}, foldPayload(email, reason, entry))

	f.mu.Lock()
	defer f.mu.Unlock()

	if event == nil {
		// The insert failed; forget the entry so the next attempt tries again
		// rather than folding into a row that does not exist.
		delete(f.entries, key)

		return OutcomeDropped
	}

	if current, ok := f.entries[key]; ok {
		current.eventUID = event.UID
	}

	return OutcomeCreated
}

// allowCreateLocked implements the per-org hourly ceiling. Caller holds the mutex.
func (f *FailedLoginFolder) allowCreateLocked(ctx context.Context, orgUID string, now time.Time) bool {
	hourStart := now.Truncate(time.Hour)

	bucket, ok := f.buckets[orgUID]
	if !ok || !bucket.hourStart.Equal(hourStart) {
		bucket = &hourBucket{hourStart: hourStart}
		f.buckets[orgUID] = bucket
	}

	if bucket.created >= f.maxPerOrgPerHour {
		if !bucket.warned {
			bucket.warned = true

			slog.WarnContext(ctx,
				"auth.login_failed audit ceiling reached for this org and hour; further events dropped",
				"orgUid", orgUID, "ceiling", f.maxPerOrgPerHour)
		}

		return false
	}

	bucket.created++

	return true
}

// evictExpiredLocked keeps the maps from growing without bound. Caller holds
// the mutex.
func (f *FailedLoginFolder) evictExpiredLocked(now time.Time) {
	for key, entry := range f.entries {
		if now.Sub(entry.firstAt) >= f.window {
			delete(f.entries, key)
		}
	}

	hourStart := now.Truncate(time.Hour)
	for orgUID, bucket := range f.buckets {
		if !bucket.hourStart.Equal(hourStart) {
			delete(f.buckets, orgUID)
		}
	}
}

func foldPayload(email, reason string, entry *foldEntry) models.JSONMap {
	payload := models.JSONMap{
		PayloadKeyEmail:      truncate(email, maxValueLen),
		PayloadKeyCount:      entry.count,
		PayloadKeyFirstAt:    entry.firstAt.UTC().Format(time.RFC3339),
		PayloadKeyLastAt:     entry.lastAt.UTC().Format(time.RFC3339),
		PayloadKeyTargetType: "user",
		PayloadKeyTargetName: truncate(email, maxValueLen),
	}

	if reason != "" {
		payload[PayloadKeyReason] = reason
	}

	return payload
}

func foldKey(orgUID, email, sourceIP string) string {
	return orgUID + "\x00" + strings.ToLower(email) + "\x00" + sourceIP
}

// defaultFolder is the process-wide folder used by RecordFailedLogin.
// by ConfigureDefaultFolder at boot; see the file comment for why it is not
// per-request state.
//
//nolint:gochecknoglobals // process-wide rate-limiter state, replaced wholesale
var (
	defaultFolderMu sync.RWMutex
	defaultFolder   = NewFailedLoginFolder(DefaultFoldWindow, DefaultMaxPerOrgPerHour)
)

// ConfigureDefaultFolder re-creates the process-wide folder from config. Called
// once from app wiring; replacing rather than mutating means the two knobs can
// never be observed half-applied. Any state accumulated by the previous folder
// is discarded, which is correct — it is a rate-limiter bucket, not data.
func ConfigureDefaultFolder(window time.Duration, maxPerOrgPerHour int) {
	folder := NewFailedLoginFolder(window, maxPerOrgPerHour)

	defaultFolderMu.Lock()
	defer defaultFolderMu.Unlock()
	defaultFolder = folder
}

// DefaultFolder returns the process-wide folder.
func DefaultFolder() *FailedLoginFolder {
	defaultFolderMu.RLock()
	defer defaultFolderMu.RUnlock()

	return defaultFolder
}

// RecordFailedLogin registers a failed login through the process-wide folder.
func RecordFailedLogin(ctx context.Context, store EventStore, orgUID, email, reason string) Outcome {
	return DefaultFolder().Record(ctx, store, orgUID, email, reason)
}
