package maintenancewindows

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func ts(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, time.UTC)
}

func TestConvertWindowToResponse_NonRecurringUpcoming(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	window := &models.MaintenanceWindow{
		UID:        "w1",
		Title:      "deploy",
		StartAt:    ts(2026, time.June, 20, 12),
		EndAt:      ts(2026, time.June, 20, 13),
		Recurrence: "none",
	}

	now := ts(2026, time.June, 15, 12)
	resp := convertWindowToResponse(window, now)

	r.Equal("upcoming", resp.Status)
	r.Len(resp.NextOccurrences, 1)
	r.Equal(window.StartAt, resp.NextOccurrences[0].StartAt)
	r.Equal(window.EndAt, resp.NextOccurrences[0].EndAt)
}

func TestConvertWindowToResponse_PastOneOffHasNoOccurrences(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	window := &models.MaintenanceWindow{
		UID:        "w2",
		Title:      "old",
		StartAt:    ts(2026, time.June, 1, 12),
		EndAt:      ts(2026, time.June, 1, 13),
		Recurrence: "none",
	}

	now := ts(2026, time.June, 15, 12)
	resp := convertWindowToResponse(window, now)

	r.Equal("past", resp.Status)
	r.Empty(resp.NextOccurrences)
}

func TestConvertWindowToResponse_RecurringCapsAtThree(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	window := &models.MaintenanceWindow{
		UID:        "w3",
		Title:      "nightly",
		StartAt:    ts(2026, time.June, 1, 22),
		EndAt:      ts(2026, time.June, 1, 23),
		Recurrence: "daily",
	}

	now := ts(2026, time.June, 10, 12)
	resp := convertWindowToResponse(window, now)

	// Default count is 3, even though the daily window has many future slots.
	r.Len(resp.NextOccurrences, nextOccurrencesCount)
	// Chronological.
	for i := 1; i < len(resp.NextOccurrences); i++ {
		r.True(resp.NextOccurrences[i].StartAt.After(resp.NextOccurrences[i-1].StartAt))
	}
}

func TestConvertWindowToResponse_RecurringActiveStatus(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	window := &models.MaintenanceWindow{
		UID:        "w4",
		Title:      "active-now",
		StartAt:    ts(2026, time.June, 1, 11),
		EndAt:      ts(2026, time.June, 1, 13),
		Recurrence: "daily",
	}

	now := ts(2026, time.June, 10, 12) // inside the daily 11:00-13:00 slot
	resp := convertWindowToResponse(window, now)

	r.Equal("active", resp.Status)
	r.NotEmpty(resp.NextOccurrences)
	// The currently-active occurrence is first.
	r.Equal(ts(2026, time.June, 10, 11), resp.NextOccurrences[0].StartAt)
}

// TestValidateCreateRequest_RejectsRRULE locks the docs to behavior: an iCalendar
// RRULE value is rejected with the invalid-recurrence error (the MCP tool no longer
// advertises RRULE, and the service never accepted it).
func TestValidateCreateRequest_RejectsRRULE(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	req := &CreateRequest{
		Title:      "x",
		StartAt:    ts(2026, time.June, 1, 22),
		EndAt:      ts(2026, time.June, 1, 23),
		Recurrence: "FREQ=WEEKLY;BYDAY=MO",
	}
	err := validateCreateRequest(req)
	r.ErrorIs(err, ErrInvalidRecurrence)
}

func TestValidateCreateRequest_AcceptsEnumValues(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, rec := range []string{"", "none", "daily", "weekly", "monthly"} {
		req := &CreateRequest{
			Title:      "x",
			StartAt:    ts(2026, time.June, 1, 22),
			EndAt:      ts(2026, time.June, 1, 23),
			Recurrence: rec,
		}
		r.NoError(validateCreateRequest(req), "recurrence %q should be valid", rec)
	}
}

func TestIsValidRecurrence(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(isValidRecurrence("none"))
	r.True(isValidRecurrence("daily"))
	r.True(isValidRecurrence("weekly"))
	r.True(isValidRecurrence("monthly"))
	r.False(isValidRecurrence("FREQ=MONTHLY;BYMONTHDAY=1"))
	r.False(isValidRecurrence("yearly"))
	r.False(isValidRecurrence(""))
}
