package jobtypes

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/identitylink"
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
// mention the on-call person. Slack and the Discord bot support it; Teams
// mentions are explicitly out of scope (spec 2026-08-12-03). In both supported
// cases the flag defaults to false, so every integration stored before the
// feature existed keeps its previous behavior with no migration.
func integrationWantsMentions(integration *models.Integration) bool {
	if integration == nil {
		return false
	}

	if integration.Type == models.ConnectionTypeSlack {
		settings, err := models.SlackSettingsFromJSONMap(integration.Settings)
		if err != nil {
			return false
		}

		return settings.MentionOnCall
	}

	if integration.Type == models.ConnectionTypeDiscord {
		settings, err := models.DiscordSettingsFromJSONMap(integration.Settings)
		if err != nil {
			return false
		}

		// A legacy webhook integration can never mention anyone: a webhook post
		// has no identity mapping behind it and cannot ping a user id.
		return settings.MentionOnCall && settings.UsesBot()
	}

	return false
}

// ResolveOnCallMentions returns the humans the incident's effective escalation
// policy is paging — schedule targets resolved through the on-call resolver,
// plus direct `user` targets — deduplicated by user uid and ordered by display
// name so message content is stable and testable.
//
// stepUID names the escalation step that actually fired, and is the difference
// between naming the on-call person and naming somebody else. An
// `incident.escalated` message is posted BECAUSE a particular step paged
// somebody; resolving step 1 there would confidently name a human who is not
// being paged, which the escalation resolver's own comment calls worse than no
// mention at all. `incident.created` passes "" and keeps step 1.
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
	stepUID string,
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

	users := resolveStepUsers(ctx, jctx, log, check, stepUID)
	if len(users) == 0 {
		return nil
	}

	return buildMentionTargets(ctx, jctx, log, integration, users)
}

// resolveStepUsers walks check → effective policy → the step that fired and
// returns the distinct humans that step pages, in target order.
//
// Two rules beyond "read the step":
//
//   - stepUID, when it names a step of THIS policy, selects that step. An
//     unknown or foreign uid falls back to the lowest-position step rather than
//     resolving nothing: a stale uid is a reason to say less confidently who is
//     on call, not a reason to go silent.
//   - when the fired step names no human at all (a step whose only target is a
//     Slack connection, the shape the spec was filed about), the remaining
//     steps are walked in position order and the first one that DOES name a
//     human is used. Without that, the extremely common "step 1 posts to the
//     channel, step 2 pages the on-call schedule" policy never names anybody.
func resolveStepUsers(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, check *models.Check, stepUID string,
) []*models.User {
	policyUID := ResolveEscalationPolicyUID(ctx, jctx.DBService, check)
	if policyUID == "" {
		return nil
	}

	steps, err := jctx.DBService.ListEscalationPolicySteps(ctx, policyUID)
	if err != nil || len(steps) == 0 {
		return nil
	}

	ordered := stepsByPosition(steps)

	for _, step := range orderStepsFrom(ordered, stepUID) {
		if users := stepUsers(ctx, jctx, log, step); len(users) > 0 {
			return users
		}
	}

	return nil
}

// stepsByPosition returns the steps sorted by position. The list is normally
// already ordered, but the mention text must not depend on that.
func stepsByPosition(steps []*models.EscalationPolicyStep) []*models.EscalationPolicyStep {
	ordered := make([]*models.EscalationPolicyStep, 0, len(steps))

	for _, step := range steps {
		if step != nil {
			ordered = append(ordered, step)
		}
	}

	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Position < ordered[j].Position })

	return ordered
}

// orderStepsFrom returns the steps to try, in order: the fired step first when
// stepUID names one, then every step by position (the fired one is harmless to
// revisit — it already returned no humans, or we never got here).
func orderStepsFrom(
	ordered []*models.EscalationPolicyStep, stepUID string,
) []*models.EscalationPolicyStep {
	if stepUID == "" {
		return ordered
	}

	for _, step := range ordered {
		if step.UID == stepUID {
			return append([]*models.EscalationPolicyStep{step}, ordered...)
		}
	}

	return ordered
}

// stepUsers resolves one step's targets to the distinct humans it pages.
func stepUsers(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger, step *models.EscalationPolicyStep,
) []*models.User {
	targets, err := jctx.DBService.ListEscalationPolicyTargets(ctx, []string{step.UID})
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
//
// Identity precedence, per user:
//
//  1. the `user_integration_identities` row — an admin's explicit mapping, and
//     the only source that also carries a provider display name;
//  2. failing that, whatever the member declared for themselves — a `slack_user`
//     contact or a Slack sign-in, resolved WORKSPACE-SCOPED by
//     identitylink.DeclaredSlackIdentity; or a `discord` contact or a Discord
//     sign-in, resolved by identitylink.DeclaredDiscordIdentity, which has no
//     scoping to do because a snowflake is global.
//
// The order is what makes "an admin mapping always wins" true: a member who
// declared the wrong handle cannot override the admin's correction.
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

		if target.ExternalID == "" {
			target.ExternalID = declaredIdentityFor(ctx, jctx, integration, user.UID)
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

// declaredIdentityFor resolves the member's own declared provider id for this
// integration's type, or "".
//
// A switch on the integration type rather than calling both resolvers: each one
// self-gates on the type anyway, but making the dispatch explicit here is what
// stops the next provider being added to identitylink and silently never
// consulted — which is exactly how the Discord half of this came to be missing.
func declaredIdentityFor(
	ctx context.Context, jctx *jobdef.JobContext,
	integration *models.Integration, userUID string,
) string {
	switch integration.Type {
	case models.ConnectionTypeSlack:
		if declared := identitylink.DeclaredSlackIdentity(
			ctx, jctx.DBService, integration, userUID); declared != nil {
			return declared.ExternalID
		}
	case models.ConnectionTypeDiscord:
		if declared := identitylink.DeclaredDiscordIdentity(
			ctx, jctx.DBService, integration, userUID); declared != nil {
			return declared.ExternalID
		}
	}

	return ""
}

// userDisplayName is the human label used when no provider display name is
// available (and as the plain-text fallback for an unmapped user).
func userDisplayName(user *models.User) string {
	if name := strings.TrimSpace(user.Name); name != "" {
		return name
	}

	return user.Email
}
