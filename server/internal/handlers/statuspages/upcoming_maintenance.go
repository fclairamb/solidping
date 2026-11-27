package statuspages

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	// upcomingMaintenanceHorizon is how far ahead a public page announces a
	// maintenance window (spec 2026-11-27-06). Fixed, not configurable.
	upcomingMaintenanceHorizon = 7 * 24 * time.Hour
	// upcomingMaintenanceMax caps the entries returned on a page.
	upcomingMaintenanceMax = 10
	// upcomingOccurrencesPerWindow is how many occurrences of a window are
	// expanded before filtering by the horizon.
	upcomingOccurrencesPerWindow = 3
)

// PublicMaintenance is one announced maintenance occurrence on a public page.
type PublicMaintenance struct {
	// UID is the maintenance window UID (a recurring window repeats it).
	UID   string `json:"uid"`
	Title string `json:"title"`
	// Description is operator-authored plain text; omitted when empty.
	Description string    `json:"description,omitempty"`
	StartsAt    time.Time `json:"startsAt"`
	EndsAt      time.Time `json:"endsAt"`
	// Active is true while the occurrence is in progress.
	Active bool `json:"active"`
	// AffectedResources are the page's own public resource names the window
	// targets; a resource the page does not show never appears.
	AffectedResources []string `json:"affectedResources"`
}

// loadUpcomingMaintenance computes the page's upcoming-maintenance list from
// the already-enriched sections. A lookup failure never fails the page.
func (s *Service) loadUpcomingMaintenance(
	ctx context.Context, sections []StatusPageSectionResponse, now time.Time,
) []PublicMaintenance {
	byWindow := make(map[string]*models.MaintenanceWindow)
	names := make(map[string][]string)

	for i := range sections {
		for j := range sections[i].Resources {
			resource := &sections[i].Resources[j]
			if resource.Check == nil || resource.Check.disabled {
				continue
			}

			name := resourcePublicName(resource)

			var (
				windows []*models.MaintenanceWindow
				err     error
			)

			switch {
			case resource.CheckGroupUID != nil:
				windows, err = s.db.ListMaintenanceWindowsForCheckGroup(ctx, *resource.CheckGroupUID)
			case resource.CheckUID != nil:
				windows, err = s.db.ListMaintenanceWindowsForCheck(ctx, *resource.CheckUID)
			}

			if err != nil {
				logLoadFailure(ctx, slog.LevelError, "Failed to load maintenance windows for status page",
					err, "resourceUID", resource.UID)

				continue
			}

			for _, w := range windows {
				byWindow[w.UID] = w
				if name != "" && !containsString(names[w.UID], name) {
					names[w.UID] = append(names[w.UID], name)
				}
			}
		}
	}

	return buildUpcomingMaintenance(byWindow, names, now)
}

func buildUpcomingMaintenance(
	byWindow map[string]*models.MaintenanceWindow, names map[string][]string, now time.Time,
) []PublicMaintenance {
	horizon := now.Add(upcomingMaintenanceHorizon)

	var out []PublicMaintenance

	for uid, window := range byWindow {
		occurrences := models.NextOccurrences(window, now, upcomingOccurrencesPerWindow)
		for i := range occurrences {
			occ := &occurrences[i]
			active := !occ.StartAt.After(now)
			if !active && occ.StartAt.After(horizon) {
				continue
			}

			entry := PublicMaintenance{
				UID:               uid,
				Title:             window.Title,
				StartsAt:          occ.StartAt,
				EndsAt:            occ.EndAt,
				Active:            active,
				AffectedResources: append([]string{}, names[uid]...),
			}
			if window.Description != nil {
				entry.Description = *window.Description
			}

			out = append(out, entry)
		}
	}

	sort.Slice(out, func(a, b int) bool {
		if !out[a].StartsAt.Equal(out[b].StartsAt) {
			return out[a].StartsAt.Before(out[b].StartsAt)
		}

		return out[a].UID < out[b].UID
	})

	if len(out) > upcomingMaintenanceMax {
		out = out[:upcomingMaintenanceMax]
	}

	return out
}

func resourcePublicName(resource *StatusPageResourceResponse) string {
	if resource.PublicName != nil && *resource.PublicName != "" {
		return *resource.PublicName
	}

	if resource.Check != nil && resource.Check.Name != nil {
		return *resource.Check.Name
	}

	return ""
}

func containsString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}

	return false
}
