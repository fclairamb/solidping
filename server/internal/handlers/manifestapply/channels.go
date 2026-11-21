package manifestapply

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

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
		channel := doc.Channels[i]

		if err := validateSlug(checks.KindChannel, channel.Slug, seen); err != nil {
			return nil, err
		}

		if channel.Name == "" || channel.Type == "" {
			return nil, invalidf("channel %q needs a name and a type", channel.Slug)
		}

		settings, _, resolveErr := secretref.ResolveConfig(ctx, channel.Settings, resolve)
		if resolveErr != nil {
			return nil, fmt.Errorf("channel %q: %w", channel.Slug, resolveErr)
		}

		existing := bySlug[channel.Slug]
		if existing == nil {
			// A bootstrap export gives a slug to a channel that has none; the
			// first apply adopts it by its exact name (and type) when that is
			// unambiguous, instead of creating a duplicate.
			if cands := byName[channel.Name]; len(cands) == 1 && string(cands[0].Type) == channel.Type {
				existing = cands[0]
			}
		}

		entry := checks.ApplyPlanEntry{Kind: checks.KindChannel, Slug: channel.Slug}

		if existing == nil {
			entry.Action = checks.ApplyActionCreate
			plan.upserts = append(plan.upserts, op{entry: entry, do: s.createChannel(org, &channel, settings, manifest)})

			continue
		}

		if string(existing.Type) != channel.Type {
			return nil, invalidf("channel %q: type cannot change from %s to %s (use a new slug)",
				channel.Slug, existing.Type, channel.Type)
		}

		changes, err := s.diffChannel(ctx, existing, &channel, settings)
		if err != nil {
			return nil, err
		}

		classify(&entry, existing.ManagedBy, manifest, changes)

		update := s.updateChannel(org, existing.UID, &channel, settings, manifest)
		plan.upserts = append(plan.upserts, op{entry: entry, do: update})
	}

	plan.deletes = deleteOps(checks.KindChannel, "managed channel absent from manifest", manifest, bySlug, seen,
		func(row *models.Integration) *string { return row.ManagedBy },
		func(row *models.Integration) string { return row.UID },
		func(ctx context.Context, uid string) error {
			return s.integrations.DeleteIntegration(ctx, org.Slug, uid)
		})

	return plan, nil
}

func (s *Service) createChannel(
	org *models.Organization, channel *checks.ExportChannel, settings map[string]any, manifest string,
) func(context.Context) error {
	req := integrations.CreateIntegrationRequest{
		Type: channel.Type, Name: channel.Name, Enabled: channel.Enabled, IsDefault: channel.IsDefault,
		Settings: settings, Slug: ptr(channel.Slug), ManagedBy: ptr(manifest),
	}

	return func(ctx context.Context) error {
		_, err := s.integrations.CreateIntegration(ctx, org.Slug, req)

		return err
	}
}

func (s *Service) updateChannel(
	org *models.Organization, uid string, channel *checks.ExportChannel, settings map[string]any, manifest string,
) func(context.Context) error {
	req := integrations.UpdateIntegrationRequest{
		Name: ptr(channel.Name), Enabled: channel.Enabled, IsDefault: channel.IsDefault,
		Settings: settings, ManagedBy: ptr(manifest), Slug: ptr(channel.Slug),
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
	ctx context.Context, existing *models.Integration, channel *checks.ExportChannel, settings map[string]any,
) ([]checks.CheckFieldChange, error) {
	var changes []checks.CheckFieldChange

	if existing.Name != channel.Name {
		changes = append(changes, checks.CheckFieldChange{Field: "name", From: existing.Name, To: channel.Name})
	}

	if channel.Enabled != nil && existing.Enabled != *channel.Enabled {
		changes = append(changes, boolChange("enabled", existing.Enabled, *channel.Enabled))
	}

	if channel.IsDefault != nil && existing.IsDefault != *channel.IsDefault {
		changes = append(changes, boolChange("isDefault", existing.IsDefault, *channel.IsDefault))
	}

	if len(settings) == 0 {
		return changes, nil
	}

	current, err := s.integrations.EffectiveSettings(ctx, existing)
	if err != nil {
		return nil, fmt.Errorf("channel %q: read stored settings: %w", channel.Slug, err)
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
	return checks.CheckFieldChange{Field: field, From: strconv.FormatBool(from), To: strconv.FormatBool(to)}
}

func jsonEqual(a, b any) bool {
	left, errL := json.Marshal(a)
	right, errR := json.Marshal(b)

	return errL == nil && errR == nil && bytes.Equal(left, right)
}

func renderValue(value any) string {
	if value == nil {
		return ""
	}

	if str, ok := value.(string); ok {
		return str
	}

	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}

	return string(raw)
}
