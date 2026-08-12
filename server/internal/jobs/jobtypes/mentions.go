package jobtypes

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

// mentionableEventTypes are the only events that may carry mentions. A resolved
// or reopened message is a status update, not a call to action — pinging people
// there trains them to mute the channel.
//
//nolint:gochecknoglobals // constant lookup set.
var mentionableEventTypes = map[string]bool{
	"incident.created":   true,
	"incident.escalated": true,
}

// integrationWantsMentions reports whether this integration is configured to
// mention the on-call person. Only Slack supports mentions today — Teams
// mentions are explicitly out of scope (spec 2026-08-12-03) — and the flag
// defaults to false, so every integration stored before the feature existed
// keeps its previous behavior with no migration.
func integrationWantsMentions(integration *models.Integration) bool {
	if integration == nil || integration.Type != models.ConnectionTypeSlack {
		return false
	}

	settings, err := models.SlackSettingsFromJSONMap(integration.Settings)
	if err != nil {
		return false
	}

	return settings.MentionOnCall
}

// ResolveOnCallMentions returns the humans the incident's effective escalation
// policy would page at step 1 — schedule targets resolved through the on-call
// resolver, plus direct `user` targets — deduplicated by user uid and ordered
// by display name so message content is stable and testable.
//
// It returns nil (no mentions) for every "we don't know" case: mentions off,
// wrong event type, no policy, no steps, no human targets, or any lookup
// failure. Callers treat nil as "render the message exactly as before", which
// is why a resolution failure can never break a send.
func ResolveOnCallMentions(
	ctx context.Context,
	jctx *jobdef.JobContext,
	log *slog.Logger,
	integration *models.Integration,
	check *models.Check,
	eventType string,
) []notifications.MentionTarget {
	if !mentionableEventTypes[eventType] || !integrationWantsMentions(integration) {
		return nil
	}

	if jctx == nil || jctx.DBService == nil || check == nil {
		return nil
	}

	// Belt and braces: mention rendering is a nicety, and no bug in it may cost
	// the operator their alert.
	defer func() {
		if rec := recover(); rec != nil && log != nil {
			log.WarnContext(ctx, "on-call mention resolution panicked — sending without mentions",
				"checkUid", check.UID, "recover", rec)
		}
	}()

	users := resolveStepOneUsers(ctx, jctx, log, check)
	if len(users) == 0 {
		return nil
	}

	return buildMentionTargets(ctx, jctx, log, integration, users)
}

// resolveStepOneUsers walks check → effective policy → first step and returns
// the distinct humans that step pages, in target order.
func resolveStepOneUsers(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, check *models.Check,
) []*models.User {
	policyUID := ResolveEscalationPolicyUID(ctx, jctx.DBService, check)
	if policyUID == "" {
		return nil
	}

	steps, err := jctx.DBService.ListEscalationPolicySteps(ctx, policyUID)
	if err != nil || len(steps) == 0 {
		return nil
	}

	first := firstStep(steps)
	if first == nil {
		return nil
	}

	targets, err := jctx.DBService.ListEscalationPolicyTargets(ctx, []string{first.UID})
	if err != nil || len(targets) == 0 {
		return nil
	}

	sort.SliceStable(targets, func(i, j int) bool { return targets[i].Position < targets[j].Position })

	seen := make(map[string]bool, len(targets))
	users := make([]*models.User, 0, len(targets))

	for _, target := range targets {
		user := resolveTargetUser(ctx, jctx, log, target)
		if user == nil || seen[user.UID] {
			continue
		}

		seen[user.UID] = true
		users = append(users, user)
	}

	return users
}

// firstStep returns the lowest-position step. The list is normally already
// ordered, but the mention text must not depend on that.
func firstStep(steps []*models.EscalationPolicyStep) *models.EscalationPolicyStep {
	var first *models.EscalationPolicyStep

	for _, step := range steps {
		if first == nil || step.Position < first.Position {
			first = step
		}
	}

	return first
}

// resolveTargetUser maps one step target to the human it pages, or nil when the
// target is not a person (a connection or all_admins fan-out) or cannot be
// resolved right now.
func resolveTargetUser(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, target *models.EscalationPolicyTarget,
) *models.User {
	if target.TargetUID == nil || *target.TargetUID == "" {
		return nil
	}

	switch target.TargetType {
	case models.EscalationTargetUser:
		user, err := jctx.DBService.GetUser(ctx, *target.TargetUID)
		if err != nil || user == nil || user.DeletedAt != nil {
			return nil
		}

		return user
	case models.EscalationTargetSchedule:
		user, err := resolveOnCallUser(ctx, jctx, *target.TargetUID, mentionResolveTime(jctx))
		if err != nil || user == nil || user.DeletedAt != nil {
			if err != nil && log != nil {
				log.DebugContext(ctx, "mention: on-call schedule did not resolve",
					"scheduleUid", *target.TargetUID, "error", err)
			}

			return nil
		}

		return user
	case models.EscalationTargetConnection, models.EscalationTargetAllAdmins:
		// A connection is not a person; all_admins is a broadcast, and
		// @-ing every admin on every alert is noise, not accountability.
		return nil
	}

	return nil
}

// mentionResolveTime is the instant the on-call rotation is evaluated at,
// honoring the injectable services clock so tests are deterministic.
func mentionResolveTime(jctx *jobdef.JobContext) time.Time {
	if jctx.Services != nil && jctx.Services.Clock != nil {
		return jctx.Services.Clock.Now()
	}

	return time.Now()
}

// buildMentionTargets attaches each user's identity on this integration, then
// orders the result by display name. A user with no identity keeps an empty
// ExternalID: the sender renders their name in plain text and pings nobody.
func buildMentionTargets(
	ctx context.Context,
	jctx *jobdef.JobContext,
	log *slog.Logger,
	integration *models.Integration,
	users []*models.User,
) []notifications.MentionTarget {
	targets := make([]notifications.MentionTarget, 0, len(users))

	for _, user := range users {
		target := notifications.MentionTarget{
			UserUID:     user.UID,
			DisplayName: userDisplayName(user),
		}

		identity, err := jctx.DBService.GetUserIntegrationIdentity(ctx, integration.UID, user.UID)
		if err != nil && log != nil {
			log.WarnContext(ctx, "mention: identity lookup failed — falling back to plain text",
				"userUid", user.UID, "integrationUid", integration.UID, "error", err)
		}

		if err == nil && identity != nil {
			target.ExternalID = identity.ExternalID
			if identity.DisplayName != "" {
				target.DisplayName = identity.DisplayName
			}
		}

		targets = append(targets, target)
	}

	sort.SliceStable(targets, func(i, j int) bool {
		left, right := targets[i].DisplayName, targets[j].DisplayName
		if !strings.EqualFold(left, right) {
			return strings.ToLower(left) < strings.ToLower(right)
		}

		return targets[i].UserUID < targets[j].UserUID
	})

	return targets
}

// userDisplayName is the human label used when no provider display name is
// available (and as the plain-text fallback for an unmapped user).
func userDisplayName(user *models.User) string {
	if name := strings.TrimSpace(user.Name); name != "" {
		return name
	}

	return user.Email
}
