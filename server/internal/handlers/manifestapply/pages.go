package manifestapply

import (
	"context"
	"fmt"
	"slices"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
)

// planPages plans the manifest's status pages, matched on slug.
//
//nolint:cyclop,funlen // validate → match → diff → closures
func (s *Service) planPages(
	ctx context.Context, org *models.Organization, doc *checks.ExportDocument, manifest string, refs *refSets,
) (*kindPlan, error) {
	plan := &kindPlan{}
	if len(doc.StatusPages) == 0 {
		return plan, nil
	}

	rows, err := s.db.ListStatusPages(ctx, org.UID)
	if err != nil {
		return nil, fmt.Errorf("list status pages: %w", err)
	}

	bySlug := make(map[string]*models.StatusPage, len(rows))
	for _, row := range rows {
		bySlug[row.Slug] = row
	}

	seen := map[string]bool{}

	for i := range doc.StatusPages {
		page := doc.StatusPages[i]

		if err := validatePage(&page, seen, refs); err != nil {
			return nil, err
		}

		existing := bySlug[page.Slug]
		entry := checks.ApplyPlanEntry{Kind: checks.KindStatusPage, Slug: page.Slug}

		if existing == nil {
			entry.Action = checks.ApplyActionCreate
			plan.upserts = append(plan.upserts, op{entry: entry, do: s.createPage(org, &page, manifest)})

			continue
		}

		changes, err := s.diffPage(ctx, existing, &page, refs)
		if err != nil {
			return nil, err
		}

		switch {
		case !ownedBy(existing.ManagedBy, manifest):
			entry.Action = checks.ApplyActionUnmanaged
			entry.Reason = "slug exists without the managed marker for this manifest"
			entry.Changes = changes
		case len(changes) == 0:
			entry.Action = checks.ApplyActionUnchanged
		default:
			entry.Action = checks.ApplyActionUpdate
			entry.Changes = changes
		}

		plan.upserts = append(plan.upserts, op{entry: entry, do: s.updatePage(org, existing.UID, &page, manifest)})
	}

	for slug, row := range bySlug {
		if ownedBy(row.ManagedBy, manifest) && !seen[slug] {
			uid := row.UID
			plan.deletes = append(plan.deletes, op{
				entry: checks.ApplyPlanEntry{
					Kind: checks.KindStatusPage, Slug: slug, Action: checks.ApplyActionDelete,
					Reason: "managed status page absent from manifest",
				},
				do: func(ctx context.Context) error {
					return s.pages.DeleteStatusPage(ctx, org.Slug, uid)
				},
			})
		}
	}

	sortDeletes(plan)

	return plan, nil
}

func validatePage(page *checks.ExportStatusPage, seen map[string]bool, refs *refSets) error {
	if err := validateSlug(checks.KindStatusPage, page.Slug, seen); err != nil {
		return err
	}

	if page.Name == "" {
		return invalidf("status page %q needs a name", page.Slug)
	}

	sectionSeen := map[string]bool{}

	for i := range page.Sections {
		section := &page.Sections[i]

		if err := validateSlug("status page "+page.Slug+" section", section.Slug, sectionSeen); err != nil {
			return err
		}

		if section.Name == "" {
			return invalidf("status page %q section %q needs a name", page.Slug, section.Slug)
		}

		for _, slug := range section.Checks {
			if !refs.known(slug) {
				return fmt.Errorf("%w: status page %q section %q references check %q",
					ErrUnknownCheckRef, page.Slug, section.Slug, slug)
			}
		}
	}

	return nil
}

func (s *Service) createPage(
	org *models.Organization, page *checks.ExportStatusPage, manifest string,
) func(context.Context) error {
	req := statuspages.CreateStatusPageRequest{
		Name: page.Name, Slug: page.Slug, ManagedBy: ptr(manifest), IsDefault: page.IsDefault,
		ShowAvailability: page.ShowAvailability, ShowResponseTime: page.ShowResponseTime,
	}
	if page.Description != "" {
		req.Description = ptr(page.Description)
	}
	if page.Visibility != "" {
		req.Visibility = ptr(page.Visibility)
	}
	if page.Language != "" {
		req.Language = ptr(page.Language)
	}

	return func(ctx context.Context) error {
		created, err := s.pages.CreateStatusPage(ctx, org.Slug, &req)
		if err != nil {
			return err
		}

		if page.Enabled != nil && !*page.Enabled {
			if _, err := s.pages.UpdateStatusPage(ctx, org.Slug, created.UID,
				&statuspages.UpdateStatusPageRequest{Enabled: page.Enabled}); err != nil {
				return err
			}
		}

		return s.reconcileSections(ctx, org, created.UID, page.Sections)
	}
}

func (s *Service) updatePage(
	org *models.Organization, uid string, page *checks.ExportStatusPage, manifest string,
) func(context.Context) error {
	req := statuspages.UpdateStatusPageRequest{
		Name: ptr(page.Name), ManagedBy: ptr(manifest), Description: ptr(page.Description),
		Enabled: page.Enabled, IsDefault: page.IsDefault,
		ShowAvailability: page.ShowAvailability, ShowResponseTime: page.ShowResponseTime,
	}
	if page.Visibility != "" {
		req.Visibility = ptr(page.Visibility)
	}
	if page.Language != "" {
		req.Language = ptr(page.Language)
	}

	return func(ctx context.Context) error {
		if _, err := s.pages.UpdateStatusPage(ctx, org.Slug, uid, &req); err != nil {
			return err
		}

		return s.reconcileSections(ctx, org, uid, page.Sections)
	}
}

// reconcileSections makes the page's sections (and their check resources) match
// the manifest. A nil list leaves the page's sections alone.
//
//nolint:cyclop,funlen // sections then resources, each: update/create/delete/reorder
func (s *Service) reconcileSections(
	ctx context.Context, org *models.Organization, pageUID string, desired []checks.ExportStatusPageSection,
) error {
	if desired == nil {
		return nil
	}

	current, err := s.db.ListStatusPageSections(ctx, pageUID)
	if err != nil {
		return err
	}

	bySlug := make(map[string]*models.StatusPageSection, len(current))
	for _, section := range current {
		bySlug[section.Slug] = section
	}

	wanted := map[string]bool{}
	ordered := make([]string, 0, len(desired))
	sectionUIDs := make([]string, len(desired))

	for i := range desired {
		want := &desired[i]
		wanted[want.Slug] = true

		if existing := bySlug[want.Slug]; existing != nil {
			sectionUIDs[i] = existing.UID

			if existing.Name != want.Name {
				if _, err := s.pages.UpdateSection(ctx, org.Slug, pageUID, existing.UID,
					statuspages.UpdateSectionRequest{Name: ptr(want.Name)}); err != nil {
					return err
				}
			}
		} else {
			created, err := s.pages.CreateSection(ctx, org.Slug, pageUID, statuspages.CreateSectionRequest{
				Name: want.Name, Slug: want.Slug, Position: ptr(i),
			})
			if err != nil {
				return err
			}

			sectionUIDs[i] = created.UID
		}

		ordered = append(ordered, sectionUIDs[i])
	}

	for _, section := range current {
		if !wanted[section.Slug] {
			if err := s.pages.DeleteSection(ctx, org.Slug, pageUID, section.UID); err != nil {
				return err
			}
		}
	}

	if len(ordered) > 1 {
		if err := s.pages.ReorderSections(ctx, org.Slug, pageUID, ordered); err != nil {
			return err
		}
	}

	for i := range desired {
		if err := s.reconcileResources(ctx, org, pageUID, sectionUIDs[i], desired[i].Checks); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) reconcileResources(
	ctx context.Context, org *models.Organization, pageUID, sectionUID string, checkSlugs []string,
) error {
	uids, err := s.resolveCheckUIDs(ctx, org.UID, checkSlugs)
	if err != nil {
		return err
	}

	want := make(map[string]bool, len(uids))
	for _, uid := range uids {
		want[uid] = true
	}

	rows, err := s.db.ListStatusPageResources(ctx, sectionUID)
	if err != nil {
		return err
	}

	have := map[string]bool{}

	for _, row := range rows {
		if row.CheckUID == nil {
			continue // a group resource is not part of the manifest: left alone
		}

		if !want[*row.CheckUID] {
			if err := s.pages.DeleteResource(ctx, org.Slug, pageUID, sectionUID, row.UID); err != nil {
				return err
			}

			continue
		}

		have[*row.CheckUID] = true
	}

	for _, uid := range uids {
		if have[uid] {
			continue
		}

		if _, err := s.pages.CreateResource(ctx, org.Slug, pageUID, sectionUID,
			statuspages.CreateResourceRequest{CheckUID: uid}); err != nil {
			return err
		}
	}

	rows, err = s.db.ListStatusPageResources(ctx, sectionUID)
	if err != nil {
		return err
	}

	byCheck := map[string]string{}
	rest := []string{}

	for _, row := range rows {
		if row.CheckUID != nil && want[*row.CheckUID] {
			byCheck[*row.CheckUID] = row.UID
		} else {
			rest = append(rest, row.UID)
		}
	}

	order := make([]string, 0, len(rows))
	for _, uid := range uids {
		order = append(order, byCheck[uid])
	}

	order = append(order, rest...)
	if len(order) < 2 {
		return nil
	}

	return s.pages.ReorderResources(ctx, org.Slug, pageUID, sectionUID, order)
}

func (s *Service) diffPage(
	ctx context.Context, existing *models.StatusPage, page *checks.ExportStatusPage, refs *refSets,
) ([]checks.CheckFieldChange, error) {
	var changes []checks.CheckFieldChange

	add := func(field, from, to string) {
		if from != to {
			changes = append(changes, checks.CheckFieldChange{Field: field, From: from, To: to})
		}
	}
	addBool := func(field string, from bool, to *bool) {
		if to != nil && from != *to {
			changes = append(changes, boolChange(field, from, *to))
		}
	}

	add("name", existing.Name, page.Name)
	add("description", derefOr(existing.Description, ""), page.Description)

	if page.Visibility != "" {
		add("visibility", existing.Visibility, page.Visibility)
	}

	if page.Language != "" {
		add("language", derefOr(existing.Language, ""), page.Language)
	}

	addBool("enabled", existing.Enabled, page.Enabled)
	addBool("isDefault", existing.IsDefault, page.IsDefault)
	addBool("showAvailability", existing.ShowAvailability, page.ShowAvailability)
	addBool("showResponseTime", existing.ShowResponseTime, page.ShowResponseTime)

	if page.Sections == nil {
		return changes, nil
	}

	have, err := s.currentSections(ctx, existing.UID, refs)
	if err != nil {
		return nil, err
	}

	want := make([]checks.ExportStatusPageSection, len(page.Sections))
	for i, section := range page.Sections {
		want[i] = checks.ExportStatusPageSection{Slug: section.Slug, Name: section.Name, Checks: section.Checks}
		if want[i].Checks == nil {
			want[i].Checks = []string{}
		}
	}

	if !slices.EqualFunc(have, want, func(a, b checks.ExportStatusPageSection) bool {
		return a.Slug == b.Slug && a.Name == b.Name && slices.Equal(a.Checks, b.Checks)
	}) {
		add("sections", renderSections(have), renderSections(want))
	}

	return changes, nil
}

// currentSections reads a page's sections as manifest entries (check rows only).
func (s *Service) currentSections(
	ctx context.Context, pageUID string, refs *refSets,
) ([]checks.ExportStatusPageSection, error) {
	sections, err := s.db.ListStatusPageSections(ctx, pageUID)
	if err != nil {
		return nil, err
	}

	out := make([]checks.ExportStatusPageSection, 0, len(sections))

	for _, section := range sections {
		rows, err := s.db.ListStatusPageResources(ctx, section.UID)
		if err != nil {
			return nil, err
		}

		slugs := []string{}

		for _, row := range rows {
			if row.CheckUID != nil {
				if slug, ok := refs.uidToSlug[*row.CheckUID]; ok {
					slugs = append(slugs, slug)
				}
			}
		}

		out = append(out, checks.ExportStatusPageSection{Slug: section.Slug, Name: section.Name, Checks: slugs})
	}

	return out, nil
}

func renderSections(sections []checks.ExportStatusPageSection) string {
	out := ""

	for i := range sections {
		if i > 0 {
			out += "; "
		}

		out += fmt.Sprintf("%s%v", sections[i].Slug, sections[i].Checks)
	}

	return out
}
