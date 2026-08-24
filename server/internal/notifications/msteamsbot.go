package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/msteams"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

var (
	// ErrMSTeamsBotNotConfigured is returned when the instance has no Entra
	// app credentials, so no Bot Connector token can be minted.
	ErrMSTeamsBotNotConfigured = errors.New("microsoft teams bot app credentials are not configured")
	// ErrMSTeamsBotDisabled is returned when msteams.enabled is false.
	// Disabling the feature must stop outbound traffic too, not just close
	// the inbound endpoint — otherwise "disabled" would still be posting into
	// customers' Teams channels.
	ErrMSTeamsBotDisabled = errors.New("the microsoft teams bot is disabled on this instance")
	// ErrMSTeamsBotNoDestination is returned when the connection has no
	// default conversation to post into.
	ErrMSTeamsBotNoDestination = errors.New("no default channel configured for the microsoft teams bot connection")
	// ErrMSTeamsBotUninstalled is returned when the tenant removed the app.
	ErrMSTeamsBotUninstalled = errors.New("microsoft teams app is uninstalled for this tenant")
	// ErrMSTeamsBotUnknownDestination is returned when the resolved
	// conversation is not one the bot was added to. The write paths already
	// validate this; re-checking at send time means a row that predates the
	// validation (or was written straight to the database) still cannot make
	// the shared bot credential post into a foreign tenant's channel.
	ErrMSTeamsBotUnknownDestination = errors.New(
		"microsoft teams conversation is not a captured destination for this connection")
)

// State keys for the incident → Teams card mapping.
const (
	msTeamsBotKeyConversationID = "conversation_id"
	msTeamsBotKeyActivityID     = "activity_id"
	msTeamsBotKeyServiceURL     = "service_url"
)

// msTeamsBotConversationOverrideKey is the per-check settings key that
// redirects one check's notifications to a different captured conversation.
const msTeamsBotConversationOverrideKey = "conversation_id"

// MSTeamsBotSender delivers incident notifications through the Microsoft
// Teams bot (Azure Bot / Bot Framework), the two-way `msteams-bot`
// integration.
//
// It is the Teams analog of SlackSender's threading behavior:
//   - `incident.created` posts an Adaptive Card and records its activity id;
//   - `incident.escalated` UPDATES that same card in place rather than adding
//     a second one;
//   - `incident.resolved` / `incident.reopened` update the card AND post a
//     reply under it, so a channel shows one incident as one grouped
//     conversation instead of a stream of unrelated messages.
//
// Distinct from MSTeamsSender (msteams.go), which is the one-way Teams
// Workflow webhook and has no notion of updating what it already sent.
type MSTeamsBotSender struct {
	// newClient builds the Bot Connector client. Tests override it to point
	// at an httptest fake connector; nil means the real one.
	newClient func(appID, appSecret, serviceURL string) *msteams.Client
}

// Send delivers the notification.
func (s *MSTeamsBotSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	settings, err := models.MSTeamsBotSettingsFromJSONMap(payload.Integration.Settings)
	if err != nil {
		return fmt.Errorf("parsing microsoft teams bot settings: %w", err)
	}

	if settings.UninstalledAt != "" {
		return ErrMSTeamsBotUninstalled
	}

	if jctx == nil || jctx.AppConfig == nil || !jctx.AppConfig.MSTeams.Enabled {
		return ErrMSTeamsBotDisabled
	}

	appID, appSecret := s.credentials(jctx)
	if appID == "" || appSecret == "" {
		return ErrMSTeamsBotNotConfigured
	}

	stateKey := "incidents/" + payload.Incident.UID + "/msteams/thread"

	entry, err := jctx.DBService.GetStateEntry(ctx, &payload.Incident.OrganizationUID, stateKey)
	if err != nil {
		return fmt.Errorf("getting teams card state entry: %w", err)
	}

	// Same defense-in-depth guard as SlackSender: a resolved/reopened event
	// with no recorded card has nothing to update or reply under, so a
	// standalone message would be a context-free orphan. Skip instead.
	if isFollowUpEvent(payload.EventType) && !hasCard(entry) {
		return nil
	}

	// A comment or an acknowledgment must never REPLACE the incident card —
	// that card shows live incident state and someone reading the channel
	// would lose it. Both are posted as a reply under the existing card
	// instead, and only when one exists (same orphan rule as
	// resolved/reopened).
	if isCardReplyEvent(payload.EventType) {
		if !hasCard(entry) {
			return nil
		}

		return s.replyUnderCard(ctx, jctx, appID, appSecret, entry, payload)
	}

	if hasCard(entry) {
		return s.updateExistingCard(ctx, jctx, appID, appSecret, entry, payload)
	}

	return s.postNewCard(ctx, jctx, appID, appSecret, settings, payload, stateKey)
}

// isCardReplyEvent reports whether an event is posted as a REPLY under the
// incident's card rather than as an update of it.
//
// Both members are commentary on a STILL-LIVE incident: the card must keep
// showing the incident's own state, so neither may overwrite it.
func isCardReplyEvent(eventType string) bool {
	return eventType == eventTypeIncidentComment ||
		eventType == eventTypeIncidentAcknowledged
}

// cardReplyText renders the one-line body of such a reply.
func cardReplyText(payload *Payload) string {
	if payload.EventType == eventTypeIncidentAcknowledged {
		return ackEmoji + " " + ackPlainBody(payload)
	}

	return commentEmoji + " " + commentPlainBody(payload)
}

// replyUnderCard posts a comment or an acknowledgment as a reply under the
// incident's existing card, leaving both the card and its stored reference
// untouched.
func (s *MSTeamsBotSender) replyUnderCard(
	ctx context.Context,
	jctx *jobdef.JobContext,
	appID, appSecret string,
	entry *models.StateEntry,
	payload *Payload,
) error {
	ref := cardRef(entry)

	client := s.client(appID, appSecret, ref.ServiceURL, s.pinnedTenant(jctx))

	reply := msteams.NewTextMessage(cardReplyText(payload))

	result, err := client.ReplyToActivity(ctx, ref.ConversationID, ref.ActivityID, reply)
	if err != nil {
		return fmt.Errorf("replying under the microsoft teams incident card: %w", err)
	}

	if result != nil {
		payload.MessageID = result.ID
	}

	return nil
}

// credentials resolves the instance-level Entra app credentials.
func (s *MSTeamsBotSender) credentials(jctx *jobdef.JobContext) (string, string) {
	if jctx == nil || jctx.AppConfig == nil {
		return "", ""
	}

	return jctx.AppConfig.MSTeams.AppID, jctx.AppConfig.MSTeams.AppSecret
}

// pinnedTenant returns the configured single-tenant pin, which selects the
// single-tenant OAuth token endpoint for outbound calls.
func (s *MSTeamsBotSender) pinnedTenant(jctx *jobdef.JobContext) string {
	if jctx == nil || jctx.AppConfig == nil {
		return ""
	}

	return jctx.AppConfig.MSTeams.TenantID
}

// client builds a Bot Connector client, honoring the test override.
func (s *MSTeamsBotSender) client(appID, appSecret, serviceURL, tenantID string) *msteams.Client {
	if s.newClient != nil {
		return s.newClient(appID, appSecret, serviceURL)
	}

	return msteams.NewClientForTenant(appID, appSecret, serviceURL, tenantID)
}

// isFollowUpEvent reports whether the event can only make sense as an update
// to an already-posted card.
func isFollowUpEvent(eventType string) bool {
	return eventType == eventTypeIncidentResolved || eventType == eventTypeIncidentReopened
}

// hasCard reports whether a state entry holds a usable card reference.
func hasCard(entry *models.StateEntry) bool {
	if entry == nil || entry.Value == nil {
		return false
	}

	conversationID, _ := (*entry.Value)[msTeamsBotKeyConversationID].(string)
	activityID, _ := (*entry.Value)[msTeamsBotKeyActivityID].(string)

	return conversationID != "" && activityID != ""
}

// cardReference is the stored pointer to an incident's already-posted card.
type cardReference struct {
	ConversationID string
	ActivityID     string
	ServiceURL     string
}

// cardRef reads the stored card reference.
func cardRef(entry *models.StateEntry) cardReference {
	conversationID, _ := (*entry.Value)[msTeamsBotKeyConversationID].(string)
	activityID, _ := (*entry.Value)[msTeamsBotKeyActivityID].(string)
	serviceURL, _ := (*entry.Value)[msTeamsBotKeyServiceURL].(string)

	return cardReference{
		ConversationID: conversationID,
		ActivityID:     activityID,
		ServiceURL:     serviceURL,
	}
}

// destination resolves the conversation to post into: a per-check override
// when present, otherwise the connection's default channel.
func (s *MSTeamsBotSender) destination(
	settings *models.MSTeamsBotSettings, payload *Payload,
) (string, string, error) {
	conversationID := settings.ChannelID
	serviceURL := settings.ServiceURL

	if payload.CheckConnectionSettings != nil {
		if override, ok := (*payload.CheckConnectionSettings)[msTeamsBotConversationOverrideKey].(string); ok &&
			override != "" {
			conversationID = override
		}
	}

	if conversationID == "" {
		return "", "", ErrMSTeamsBotNoDestination
	}

	// Defense in depth: only conversations this connection actually captured
	// from Bot Framework are addressable, whatever a stored selection claims.
	if !settings.HasDestination(conversationID) {
		return "", "", ErrMSTeamsBotUnknownDestination
	}

	// A destination captured from a different regional connector carries its
	// own service URL; prefer it over the connection-level one.
	for i := range settings.Destinations {
		if settings.Destinations[i].ID == conversationID && settings.Destinations[i].ServiceURL != "" {
			serviceURL = settings.Destinations[i].ServiceURL

			break
		}
	}

	if serviceURL == "" {
		return "", "", msteams.ErrMissingServiceURL
	}

	return conversationID, serviceURL, nil
}

// postNewCard posts the first card for an incident and records its id so
// later events can update it in place.
func (s *MSTeamsBotSender) postNewCard(
	ctx context.Context,
	jctx *jobdef.JobContext,
	appID, appSecret string,
	settings *models.MSTeamsBotSettings,
	payload *Payload,
	stateKey string,
) error {
	conversationID, serviceURL, err := s.destination(settings, payload)
	if err != nil {
		return err
	}

	client := s.client(appID, appSecret, serviceURL, s.pinnedTenant(jctx))

	result, err := client.SendToConversation(ctx, conversationID, s.buildCardActivity(payload))
	if err != nil {
		return fmt.Errorf("posting microsoft teams card: %w", err)
	}

	payload.MessageID = result.ID

	value := &models.JSONMap{
		msTeamsBotKeyConversationID: conversationID,
		msTeamsBotKeyActivityID:     result.ID,
		msTeamsBotKeyServiceURL:     serviceURL,
	}

	if err := jctx.DBService.SetStateEntry(
		ctx, &payload.Incident.OrganizationUID, stateKey, value, nil,
	); err != nil {
		return fmt.Errorf("storing teams card state entry: %w", err)
	}

	return nil
}

// updateExistingCard rewrites the incident's card in place and, for a
// terminal event, also posts a reply under it so the channel gets the
// Slack-thread-like grouping.
func (s *MSTeamsBotSender) updateExistingCard(
	ctx context.Context,
	jctx *jobdef.JobContext,
	appID, appSecret string,
	entry *models.StateEntry,
	payload *Payload,
) error {
	ref := cardRef(entry)

	client := s.client(appID, appSecret, ref.ServiceURL, s.pinnedTenant(jctx))

	if _, err := client.UpdateActivity(
		ctx, ref.ConversationID, ref.ActivityID, s.buildCardActivity(payload),
	); err != nil {
		return fmt.Errorf("updating microsoft teams card: %w", err)
	}

	payload.MessageID = ref.ActivityID

	if !isFollowUpEvent(payload.EventType) {
		return nil
	}

	reply := msteams.NewTextMessage(s.followUpText(payload))

	if _, err := client.ReplyToActivity(ctx, ref.ConversationID, ref.ActivityID, reply); err != nil {
		// The card itself is already correct; a failed thread reply degrades
		// grouping, it does not lose the notification.
		slog.WarnContext(ctx, "Failed to post Microsoft Teams follow-up reply",
			"incident_uid", payload.Incident.UID, "error", err)
	}

	return nil
}

// followUpText is the short thread reply that accompanies a card update.
func (s *MSTeamsBotSender) followUpText(payload *Payload) string {
	checkName := getCheckName(payload.Check)

	if payload.EventType == eventTypeIncidentReopened {
		return fmt.Sprintf("🔁 %s is down again (relapse #%d).", checkName, payload.Incident.RelapseCount)
	}

	if payload.Incident.ResolvedAt != nil {
		return fmt.Sprintf("🟢 %s recovered after %s.",
			checkName, formatDuration(payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)))
	}

	return "🟢 " + checkName + " recovered."
}

// buildCardActivity renders the incident card for the current event.
func (s *MSTeamsBotSender) buildCardActivity(payload *Payload) *msteams.Activity {
	checkName := getCheckName(payload.Check)
	title, color := s.titleAndColor(payload, checkName)

	card := &msteams.AdaptiveCard{
		Type:    msteams.CardTypeAdaptive,
		Schema:  msteams.CardSchema,
		Version: msteams.CardVersion,
		MSTeams: &msteams.CardTeamsSettings{Width: msteams.CardWidthFull},
		Body: []msteams.CardElement{
			{
				Type:   msteams.CardElementText,
				Text:   title,
				Size:   "Large",
				Weight: "Bolder",
				Color:  color,
				Wrap:   true,
			},
			{Type: msteams.CardElementFactSet, Facts: s.buildFacts(payload, checkName)},
		},
		Actions: s.buildActions(payload),
	}

	return msteams.NewCardMessage(title, card)
}

// titleAndColor maps the event to the card's headline and semantic color.
//
// The emoji here are the ones dash0's per-event-type registry
// (web/dash0/src/components/dashboard/event-display.tsx, EVENT_TYPE_REGISTRY)
// and the Telegram integration are kept aligned to: one emoji per event type,
// product-wide. Changing one here means changing it everywhere.
func (s *MSTeamsBotSender) titleAndColor(payload *Payload, checkName string) (string, string) {
	switch payload.EventType {
	case eventTypeIncidentCreated:
		return "🔴 " + checkName + " is DOWN", msteams.CardColorAttention
	case eventTypeIncidentResolved:
		return "🟢 " + checkName + " has RECOVERED", msteams.CardColorGood
	case eventTypeIncidentEscalated:
		return "⚠️ " + checkName + " ESCALATED", msteams.CardColorWarning
	case eventTypeIncidentReopened:
		return fmt.Sprintf("🔁 %s REOPENED (relapse #%d)", checkName, payload.Incident.RelapseCount),
			msteams.CardColorAttention
	default:
		return "ℹ️ " + checkName, ""
	}
}

// buildFacts builds the card's FactSet.
func (s *MSTeamsBotSender) buildFacts(payload *Payload, checkName string) []msteams.CardFact {
	facts := []msteams.CardFact{{Title: "Monitor", Value: checkName}}

	if url := getCheckURL(payload.Check); url != "" {
		facts = append(facts, msteams.CardFact{Title: "Check", Value: getCheckMethod(payload.Check) + " " + url})
	}

	switch {
	case payload.EventType == eventTypeIncidentResolved && payload.Incident.ResolvedAt != nil:
		facts = append(facts, msteams.CardFact{
			Title: "Duration",
			Value: formatDuration(payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)),
		})
	case payload.EventType != eventTypeIncidentResolved:
		facts = append(facts,
			msteams.CardFact{Title: mmFieldCause, Value: getFailureReason(payload.Incident)},
			msteams.CardFact{Title: "Started", Value: payload.Incident.StartedAt.UTC().Format(time.RFC1123)},
		)
	}

	return facts
}

// buildActions adds dashboard deep links when the URLs can be built.
func (s *MSTeamsBotSender) buildActions(payload *Payload) []msteams.CardAction {
	actions := make([]msteams.CardAction, 0, 2)

	if url := incidentDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident); url != "" {
		actions = append(actions, msteams.CardAction{
			Type: msteams.CardActionOpenURL, Title: "View incident", URL: url,
		})
	}

	if url := checkDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Check); url != "" {
		actions = append(actions, msteams.CardAction{
			Type: msteams.CardActionOpenURL, Title: "View monitor", URL: url,
		})
	}

	return actions
}
