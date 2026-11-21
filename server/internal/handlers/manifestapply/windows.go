package manifestapply

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/maintenancewindows"
)

// planWindows plans the manifest's maintenance windows.
func (s *Service) planWindows(
	ctx context.Context, org *models.Organization, doc *checks.ExportDocument, manifest string, refs *refSets,
) (*kindPlan, error) {
	plan := &kindPlan{}
	if len(doc.MaintenanceWindows) == 0 {
		return plan, nil
	}

	rows, err := s.db.ListMaintenanceWindows(ctx, org.UID, models.ListMaintenanceWindowsFilter{})
	if err != nil {
		return nil, fmt.Errorf("list maintenance windows: %w", err)
	}

	bySlug := map[string]*models.MaintenanceWindow{}
	byTitle := map[string][]*models.MaintenanceWindow{}

	for _, row := range rows {
		if row.Slug != nil {
			bySlug[*row.Slug] = row
		} else if row.ManagedBy == nil {
			byTitle[row.Title] = append(byTitle[row.Title], row)
		}
	}

	seen := map[string]bool{}

	for i := range doc.MaintenanceWindows {
		win := doc.MaintenanceWindows[i]

		if err := validateWindow(&win, seen, refs); err != nil {
			return nil, err
		}

		existing := bySlug[win.Slug]
		if existing == nil {
			// Adopt a slug-less window by its exact title when unambiguous (the
			// bootstrap export gives such windows a derived slug).
			if cands := byTitle[win.Title]; len(cands) == 1 {
				existing = cands[0]
			}
		}

		entry := checks.ApplyPlanEntry{Kind: checks.KindMaintenanceWindow, Slug: win.Slug}

		if existing == nil {
			entry.Action = checks.ApplyActionCreate
			plan.upserts = append(plan.upserts, op{entry: entry, do: s.createWindow(org, &win, manifest)})

			continue
		}

		changes, err := s.diffWindow(ctx, existing, &win, refs)
		if err != nil {
			return nil, err
		}

		classify(&entry, existing.ManagedBy, manifest, changes)

		plan.upserts = append(plan.upserts, op{entry: entry, do: s.updateWindow(org, existing.UID, &win, manifest)})
	}

	plan.deletes = deleteOps(checks.KindMaintenanceWindow, "managed maintenance window absent from manifest",
		manifest, bySlug, seen,
		func(row *models.MaintenanceWindow) *string { return row.ManagedBy },
		func(row *models.MaintenanceWindow) string { return row.UID },
		func(ctx context.Context, uid string) error {
			return s.windows.DeleteMaintenanceWindow(ctx, org.Slug, uid)
		})

	return plan, nil
}

func validateWindow(win *checks.ExportMaintenanceWindow, seen map[string]bool, refs *refSets) error {
	if err := validateSlug(checks.KindMaintenanceWindow, win.Slug, seen); err != nil {
		return err
	}

	if win.Title == "" {
		return invalidf("maintenance window %q needs a title", win.Slug)
	}

	if win.StartAt.IsZero() || !win.EndAt.After(win.StartAt) {
		return invalidf("maintenance window %q: endAt must be after startAt", win.Slug)
	}

	switch win.Recurrence {
	case "", models.RecurrenceNone, models.RecurrenceDaily, models.RecurrenceWeekly, models.RecurrenceMonthly:
	default:
		return invalidf("maintenance window %q: invalid recurrence %q", win.Slug, win.Recurrence)
	}

	for _, slug := range win.Checks {
		if !refs.known(slug) {
			return fmt.Errorf("%w: maintenance window %q references check %q", ErrUnknownCheckRef, win.Slug, slug)
		}
	}

	return nil
}

func recurrenceOf(r string) string {
	if r == "" {
		return models.RecurrenceNone
	}

	return r
}

func (s *Service) createWindow(
	org *models.Organization, win *checks.ExportMaintenanceWindow, manifest string,
) func(context.Context) error {
	req := maintenancewindows.CreateRequest{
		Slug: ptr(win.Slug), ManagedBy: ptr(manifest), Title: win.Title,
		StartAt: win.StartAt, EndAt: win.EndAt, Recurrence: recurrenceOf(win.Recurrence),
		RecurrenceEnd: win.RecurrenceEnd,
	}
	if win.Description != "" {
		req.Description = ptr(win.Description)
	}

	slugs := win.Checks

	return func(ctx context.Context) error {
		created, err := s.windows.CreateMaintenanceWindow(ctx, org.Slug, &req)
		if err != nil {
			return err
		}

		return s.setWindowChecks(ctx, org, created.UID, slugs)
	}
}

func (s *Service) updateWindow(
	org *models.Organization, uid string, win *checks.ExportMaintenanceWindow, manifest string,
) func(context.Context) error {
	req := maintenancewindows.UpdateRequest{
		ManagedBy: ptr(manifest), Slug: ptr(win.Slug), Title: ptr(win.Title),
		StartAt: ptr(win.StartAt), EndAt: ptr(win.EndAt),
		Recurrence: ptr(recurrenceOf(win.Recurrence)), RecurrenceEnd: win.RecurrenceEnd,
		Description: ptr(win.Description),
	}
	slugs := win.Checks

	return func(ctx context.Context) error {
		if _, err := s.windows.UpdateMaintenanceWindow(ctx, org.Slug, uid, req); err != nil {
			return err
		}

		return s.setWindowChecks(ctx, org, uid, slugs)
	}
}

// setWindowChecks replaces the window's checks with the named ones, keeping any
// check group the window already covers (groups are not part of the manifest).
func (s *Service) setWindowChecks(
	ctx context.Context, org *models.Organization, uid string, slugs []string,
) error {
	uids, err := s.resolveCheckUIDs(ctx, org.UID, slugs)
	if err != nil {
		return err
	}

	current, err := s.db.ListMaintenanceWindowChecks(ctx, uid)
	if err != nil {
		return err
	}

	groups := []string{}
	for _, assoc := range current {
		if assoc.CheckGroupUID != nil {
			groups = append(groups, *assoc.CheckGroupUID)
		}
	}

	return s.windows.SetChecks(ctx, org.Slug, uid, maintenancewindows.SetChecksRequest{
		CheckUIDs: uids, CheckGroupUIDs: groups,
	})
}

func (s *Service) diffWindow(
	ctx context.Context, existing *models.MaintenanceWindow, win *checks.ExportMaintenanceWindow, refs *refSets,
) ([]checks.CheckFieldChange, error) {
	var changes []checks.CheckFieldChange

	add := func(field, from, to string) {
		if from != to {
			changes = append(changes, checks.CheckFieldChange{Field: field, From: from, To: to})
		}
	}

	add("title", existing.Title, win.Title)
	add("description", deref(existing.Description), win.Description)
	add("startAt", existing.StartAt.UTC().Format(time.RFC3339), win.StartAt.UTC().Format(time.RFC3339))
	add("endAt", existing.EndAt.UTC().Format(time.RFC3339), win.EndAt.UTC().Format(time.RFC3339))
	add("recurrence", existing.Recurrence, recurrenceOf(win.Recurrence))
	add("recurrenceEnd", fmtTime(existing.RecurrenceEnd), fmtTime(win.RecurrenceEnd))

	assocs, err := s.db.ListMaintenanceWindowChecks(ctx, existing.UID)
	if err != nil {
		return nil, fmt.Errorf("maintenance window %q: list checks: %w", win.Slug, err)
	}

	have := []string{}
	for _, assoc := range assocs {
		if assoc.CheckUID != nil {
			have = append(have, refs.uidToSlug[*assoc.CheckUID])
		}
	}

	if !slices.Equal(sortedCopy(have), sortedCopy(win.Checks)) {
		add("checks", fmt.Sprint(sortedCopy(have)), fmt.Sprint(sortedCopy(win.Checks)))
	}

	return changes, nil
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}

	return t.UTC().Format(time.RFC3339)
}
