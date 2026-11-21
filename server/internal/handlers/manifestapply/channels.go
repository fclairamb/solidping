package manifestapply

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/secretref"
)

// maskedValue stands in for a secret setting in a plan diff.
const maskedValue = "***"

// planChannels plans the manifest's channels. Secret references in the
// settings are resolved here (and again by the service on write) because the
// notification senders do not resolve references at dispatch time: the stored
// value is the resolved one, encrypted by the integrations service like any
// other channel secret. The plan never prints a resolved value.
//
//nolint:cyclop,funlen // validate → match → diff → closures
func (s *Service) planChannels(
	ctx context.Context, org *models.Organization, doc *checks.ExportDocument, manifest string,
	resolve secretref.ResolveFunc,
) (*kindPlan, error) {
	plan := &kindPlan{}
	if len(doc.Channels) == 0 {
		return plan, nil
	}

	rows, err := s.db.ListChannels(ctx, &models.ListIntegrationsFilter{OrganizationUID: org.UID})
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}

	bySlug := map[string]*models.Integration{}
	byName := map[string][]*models.Integration{}

	for _, row := range rows {
		if row.Slug != nil {
			bySlug[*row.Slug] = row
		} else if row.ManagedBy == nil {
			byName[row.Name] = append(byName[row.Name], row)
		}
	}

	seen := map[string]bool{}

	for i := range doc.Channels {
		ch := doc.Channels[i]

		if err := validateSlug(checks.KindChannel, ch.Slug, seen); err != nil {
			return nil, err
		}

		if ch.Name == "" || ch.Type == "" {
			return nil, invalidf("channel %q needs a name and a type", ch.Slug)
		}

		settings, _, resolveErr := secretref.ResolveConfig(ctx, ch.Settings, resolve)
		if resolveErr != nil {
			return nil, fmt.Errorf("channel %q: %w", ch.Slug, resolveErr)
		}

		existing := bySlug[ch.Slug]
		if existing == nil {
			// A bootstrap export gives a slug to a channel that has none; the
			// first apply adopts it by its exact name (and type) when that is
			// unambiguous, instead of creating a duplicate.
			if cands := byName[ch.Name]; len(cands) == 1 && string(cands[0].Type) == ch.Type {
				existing = cands[0]
			}
		}

		entry := checks.ApplyPlanEntry{Kind: checks.KindChannel, Slug: ch.Slug}

		if existing == nil {
			entry.Action = checks.ApplyActionCreate
			plan.upserts = append(plan.upserts, op{entry: entry, do: s.createChannel(org, &ch, settings, manifest)})

			continue
		}

		if string(existing.Type) != ch.Type {
			return nil, invalidf("channel %q: type cannot change from %s to %s (use a new slug)",
				ch.Slug, existing.Type, ch.Type)
		}

		changes, err := s.diffChannel(ctx, existing, &ch, settings)
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

		plan.upserts = append(plan.upserts, op{entry: entry, do: s.updateChannel(org, existing.UID, &ch, settings, manifest)})
	}

	for slug, row := range bySlug {
		if ownedBy(row.ManagedBy, manifest) && !seen[slug] {
			uid := row.UID
			plan.deletes = append(plan.deletes, op{
				entry: checks.ApplyPlanEntry{
					Kind: checks.KindChannel, Slug: slug, Action: checks.ApplyActionDelete,
					Reason: "managed channel absent from manifest",
				},
				do: func(ctx context.Context) error {
					return s.integrations.DeleteIntegration(ctx, org.Slug, uid)
				},
			})
		}
	}

	sortDeletes(plan)

	return plan, nil
}

func sortDeletes(plan *kindPlan) {
	sort.Slice(plan.deletes, func(a, b int) bool { return plan.deletes[a].entry.Slug < plan.deletes[b].entry.Slug })
}

func (s *Service) createChannel(
	org *models.Organization, ch *checks.ExportChannel, settings map[string]any, manifest string,
) func(context.Context) error {
	req := integrations.CreateIntegrationRequest{
		Type: ch.Type, Name: ch.Name, Enabled: ch.Enabled, IsDefault: ch.IsDefault,
		Settings: settings, Slug: ptr(ch.Slug), ManagedBy: ptr(manifest),
	}

	return func(ctx context.Context) error {
		_, err := s.integrations.CreateIntegration(ctx, org.Slug, req)

		return err
	}
}

func (s *Service) updateChannel(
	org *models.Organization, uid string, ch *checks.ExportChannel, settings map[string]any, manifest string,
) func(context.Context) error {
	req := integrations.UpdateIntegrationRequest{
		Name: ptr(ch.Name), Enabled: ch.Enabled, IsDefault: ch.IsDefault,
		Settings: settings, ManagedBy: ptr(manifest), Slug: ptr(ch.Slug),
	}

	return func(ctx context.Context) error {
		_, err := s.integrations.UpdateIntegration(ctx, org.Slug, uid, req)

		return err
	}
}

// diffChannel compares the manifest entry with the stored channel. Only the
// settings keys the manifest names are compared (the server adds others, e.g. a
// webhook signing secret); secret values are masked in the diff.
func (s *Service) diffChannel(
	ctx context.Context, existing *models.Integration, ch *checks.ExportChannel, settings map[string]any,
) ([]checks.CheckFieldChange, error) {
	var changes []checks.CheckFieldChange

	if existing.Name != ch.Name {
		changes = append(changes, checks.CheckFieldChange{Field: "name", From: existing.Name, To: ch.Name})
	}

	if ch.Enabled != nil && existing.Enabled != *ch.Enabled {
		changes = append(changes, boolChange("enabled", existing.Enabled, *ch.Enabled))
	}

	if ch.IsDefault != nil && existing.IsDefault != *ch.IsDefault {
		changes = append(changes, boolChange("isDefault", existing.IsDefault, *ch.IsDefault))
	}

	if len(settings) == 0 {
		return changes, nil
	}

	current, err := s.integrations.EffectiveSettings(ctx, existing)
	if err != nil {
		return nil, fmt.Errorf("channel %q: read stored settings: %w", ch.Slug, err)
	}

	secrets := map[string]bool{}
	for _, key := range credentials.ConnectionSecretFields(existing.Type) {
		secrets[key] = true
	}

	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if jsonEqual(current[key], settings[key]) {
			continue
		}

		change := checks.CheckFieldChange{
			Field: "settings." + key, From: renderValue(current[key]), To: renderValue(settings[key]),
		}
		if secrets[key] {
			change.From, change.To = maskedValue, maskedValue
		}

		changes = append(changes, change)
	}

	return changes, nil
}

func boolChange(field string, from, to bool) checks.CheckFieldChange {
	return checks.CheckFieldChange{Field: field, From: fmt.Sprint(from), To: fmt.Sprint(to)}
}

func jsonEqual(a, b any) bool {
	left, errL := json.Marshal(a)
	right, errR := json.Marshal(b)

	return errL == nil && errR == nil && string(left) == string(right)
}

func renderValue(v any) string {
	if v == nil {
		return ""
	}

	if str, ok := v.(string); ok {
		return str
	}

	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}

	return string(raw)
}
