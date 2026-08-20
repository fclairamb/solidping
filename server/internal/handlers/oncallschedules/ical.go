package oncallschedules

// iCal feed for on-call schedules. Public route (no auth) — the secret in
// the URL is what authorizes access. Each rotation slot becomes a VEVENT;
// clients (Google Calendar, Apple Calendar, etc.) subscribe to the feed and
// see the upcoming on-call rotation in their calendar.
//
// Per spec: window is [now-1month, now+12months], generated on demand,
// cached at the HTTP layer for 15 minutes (slots a year out don't change
// minute-to-minute). The arran4/golang-ical library writes RFC5545-correct
// output (proper CRLF, line folding, escapes) — hand-rolling text/calendar
// is a known footgun that breaks silently in Google Calendar.

import (
	"context"
	"fmt"
	"net/http"
	"time"

	ics "github.com/arran4/golang-ical"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

const (
	icalLookbackWindow  = 30 * 24 * time.Hour
	icalLookaheadWindow = 365 * 24 * time.Hour
	icalCacheMaxAge     = 900 // seconds — 15 minutes per spec
)

// ServeICalFeed handles GET /api/v1/on-call-schedules/:secret/feed.ics. It
// is wired outside the auth middleware: the secret is the credential.
func (h *Handler) ServeICalFeed(writer http.ResponseWriter, req *http.Request) error {
	secret := httpx.Param(req, "secret")

	schedule, err := h.svc.LookupScheduleByICalSecret(req.Context(), secret)
	if err != nil {
		return writeICalGone(writer)
	}

	roster, err := h.svc.ListUsers(req.Context(), schedule.UID)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	usersByUID := h.loadUsersByUID(req.Context(), roster)

	now := time.Now()
	from := now.Add(-icalLookbackWindow)
	if from.Before(schedule.StartAt) {
		from = schedule.StartAt
	}

	slots, err := h.svc.Preview(req.Context(), schedule.UID, from, icalLookbackWindow+icalLookaheadWindow)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	cal := buildOnCallCalendar(schedule, slots, usersByUID)

	writer.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	writer.Header().Set("Cache-Control", fmt.Sprintf("max-age=%d", icalCacheMaxAge))
	_, _ = writer.Write([]byte(cal.Serialize()))

	return nil
}

// writeICalGone responds with 410 — the right signal for "feed has been
// disabled or rotated"; calendar clients stop polling. Centralized so both
// the secret-not-found and (future) per-schedule disable paths use the same
// shape, and the body stays a true sentence (no JSON in a text/calendar
// path).
func writeICalGone(writer http.ResponseWriter) error {
	writer.WriteHeader(http.StatusGone)
	_, _ = writer.Write([]byte("This iCal feed has been disabled or rotated."))

	return nil
}

// loadUsersByUID resolves the roster's user UIDs into User records via the
// db service. Soft-deleted or missing users are silently skipped — the iCal
// event for that slot will fall back to the UID as display name.
func (h *Handler) loadUsersByUID(
	ctx context.Context, roster []*models.OnCallScheduleUser,
) map[string]*models.User {
	out := make(map[string]*models.User, len(roster))

	for _, entry := range roster {
		if _, seen := out[entry.UserUID]; seen {
			continue
		}

		user, err := h.dbSvc.GetUser(ctx, entry.UserUID)
		if err != nil {
			// Best-effort — schedules survive missing users (the resolver
			// skips them; the iCal feed should not 500 either).
			continue
		}

		out[entry.UserUID] = user
	}

	return out
}

// buildOnCallCalendar turns rotation slots into a VCALENDAR. Each slot is
// one VEVENT with the on-call user as SUMMARY. The slot UID embeds the
// schedule UID + start time so calendar clients can dedupe across refreshes.
func buildOnCallCalendar(
	schedule *models.OnCallSchedule, slots []PreviewSlot, usersByUID map[string]*models.User,
) *ics.Calendar {
	cal := ics.NewCalendar()
	cal.SetMethod(ics.MethodPublish)
	cal.SetProductId("-//solidping//on-call-schedule//EN")
	cal.SetName(schedule.Name + " — on-call")
	cal.SetXWRCalName(schedule.Name + " — on-call")
	cal.SetDescription(scheduleICalDescription(schedule))
	cal.SetTimezoneId(schedule.Timezone)

	now := time.Now().UTC()

	for i := range slots {
		slot := &slots[i]
		// Stable per-slot UID — clients use it for dedup across refreshes.
		// Format: <scheduleUID>-<unix-start> so a given slot keeps its UID
		// even when the surrounding window shifts on the next fetch.
		uid := fmt.Sprintf("%s-%d@solidping", schedule.UID, slot.From.Unix())

		event := cal.AddEvent(uid)
		event.SetCreatedTime(now)
		event.SetDtStampTime(now)
		event.SetModifiedAt(now)
		event.SetStartAt(slot.From)
		event.SetEndAt(slot.To)
		event.SetSummary(displayName(usersByUID, slot.UserUID) + " — on-call")

		if user := usersByUID[slot.UserUID]; user != nil && user.Email != "" {
			event.SetDescription("On-call: " + user.Name + " <" + user.Email + ">")
		}
	}

	return cal
}

// displayName returns the user's name when known, falling back to the UID
// so the calendar event still has a useful summary even if the user has
// been soft-deleted since the slot was generated.
func displayName(usersByUID map[string]*models.User, userUID string) string {
	if user := usersByUID[userUID]; user != nil && user.Name != "" {
		return user.Name
	}

	return userUID
}

// scheduleICalDescription builds the calendar-level DESCRIPTION shown in
// most clients alongside the calendar name.
func scheduleICalDescription(schedule *models.OnCallSchedule) string {
	if schedule.Description != nil && *schedule.Description != "" {
		return *schedule.Description
	}

	return fmt.Sprintf("On-call rotation for %s (%s)", schedule.Name, schedule.Timezone)
}
