package manifestapply

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// Export returns every kind of the organization as one manifest document, a
// starting point to hand-edit and apply back. Channel secrets are never
// exported (the document is marked `secrets: stripped`): put a `${env:…}` or
// `${param:…}` reference where one is needed. A channel or window that has no
// slug yet gets one derived from its name; the first apply adopts it by that
// exact name.
func (s *Service) Export(ctx context.Context, orgSlug string) (*checks.ExportDocument, error) {
	doc, err := s.checks.ExportChecks(ctx, orgSlug, checks.ListChecksOptions{})
	if err != nil {
		return nil, err
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, checks.ErrOrganizationNotFound
	}

	refs, err := s.loadRefs(ctx, org.UID, doc)
	if err != nil {
		return nil, err
	}

	doc.Secrets = checks.SecretsMarkerStripped

	if err := s.exportChannels(ctx, org, doc); err != nil {
		return nil, err
	}

	if err := s.exportWindows(ctx, org, doc, refs); err != nil {
		return nil, err
	}

	if err := s.exportPages(ctx, org, doc, refs); err != nil {
		return nil, err
	}

	return doc, nil
}

func (s *Service) exportChannels(ctx context.Context, org *models.Organization, doc *checks.ExportDocument) error {
	channels, err := s.db.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: org.UID})
	if err != nil {
		return fmt.Errorf("list channels: %w", err)
	}

	used := map[string]bool{}

	for _, row := range channels {
		if row.Slug != nil {
			used[*row.Slug] = true
		}
	}

	// ListChannels is newest first: export oldest first.
	for i := len(channels) - 1; i >= 0; i-- {
		row := channels[i]

		slug := deref(row.Slug)
		if slug == "" {
			slug = uniqueSlug(row.Name, used)
		}

		settings := make(map[string]any, len(row.Settings))
		for key, value := range row.Settings {
			settings[key] = value
		}

		// Dropping the declared secret keys is enough: apply refuses a
		// `${param:}` / `${env:}` reference under any other key (see
		// channelRefKeys), so a non-secret key never carries a resolved value.
		for _, key := range credentials.ConnectionSecretFields(row.Type) {
			delete(settings, key)
		}

		doc.Channels = append(doc.Channels, checks.ExportChannel{
			Slug: slug, Name: row.Name, Type: string(row.Type), Enabled: ptr(row.Enabled),
			IsDefault: ptr(row.IsDefault), Settings: settings,
		})
	}

	return nil
}

func (s *Service) exportWindows(
	ctx context.Context, org *models.Organization, doc *checks.ExportDocument, refs *refSets,
) error {
	windows, err := s.db.ListMaintenanceWindows(ctx, org.UID, models.ListMaintenanceWindowsFilter{})
	if err != nil {
		return fmt.Errorf("list maintenance windows: %w", err)
	}

	used := map[string]bool{}

	for _, row := range windows {
		if row.Slug != nil {
			used[*row.Slug] = true
		}
	}

	for _, row := range windows {
		slug := deref(row.Slug)
		if slug == "" {
			slug = uniqueSlug(row.Title, used)
		}

		assocs, err := s.db.ListMaintenanceWindowChecks(ctx, row.UID)
		if err != nil {
			return fmt.Errorf("list maintenance window checks: %w", err)
		}

		slugs := []string{}

		for _, assoc := range assocs {
			if assoc.CheckUID == nil {
				continue
			}

			if checkSlug, ok := refs.uidToSlug[*assoc.CheckUID]; ok {
				slugs = append(slugs, checkSlug)
			}
		}

		doc.MaintenanceWindows = append(doc.MaintenanceWindows, checks.ExportMaintenanceWindow{
			Slug: slug, Title: row.Title, Description: deref(row.Description),
			StartAt: row.StartAt.UTC().Truncate(time.Second), EndAt: row.EndAt.UTC().Truncate(time.Second),
			Recurrence: row.Recurrence, RecurrenceEnd: row.RecurrenceEnd, Checks: slugs,
		})
	}

	return nil
}

func (s *Service) exportPages(
	ctx context.Context, org *models.Organization, doc *checks.ExportDocument, refs *refSets,
) error {
	pages, err := s.db.ListStatusPages(ctx, org.UID)
	if err != nil {
		return fmt.Errorf("list status pages: %w", err)
	}

	for _, row := range pages {
		sections, err := s.currentSections(ctx, row.UID, refs)
		if err != nil {
			return err
		}

		doc.StatusPages = append(doc.StatusPages, checks.ExportStatusPage{
			Slug: row.Slug, Name: row.Name, Description: deref(row.Description),
			Visibility: row.Visibility, Enabled: ptr(row.Enabled), IsDefault: ptr(row.IsDefault),
			ShowAvailability: ptr(row.ShowAvailability), ShowResponseTime: ptr(row.ShowResponseTime),
			Language: deref(row.Language), Sections: sections,
		})
	}

	return nil
}

// uniqueSlug derives a manifest key from a display name, unique within used.
func uniqueSlug(name string, used map[string]bool) string {
	var builder strings.Builder

	dash := false

	for _, r := range strings.ToLower(name) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			builder.WriteRune(r)

			dash = false
		} else if !dash && builder.Len() > 0 {
			builder.WriteByte('-')

			dash = true
		}
	}

	base := strings.Trim(builder.String(), "-")
	if base == "" {
		base = "item"
	}

	if len(base) > 90 {
		base = base[:90]
	}

	slug := base
	for n := 2; used[slug]; n++ {
		slug = fmt.Sprintf("%s-%d", base, n)
	}

	used[slug] = true

	return slug
}
