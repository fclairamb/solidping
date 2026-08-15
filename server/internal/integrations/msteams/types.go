// Package msteams implements the two-way Microsoft Teams bot integration
// (connection type `msteams-bot`), built on the Azure Bot Service / Bot
// Framework. It is the Teams counterpart of internal/integrations/slack and
// is deliberately separate from internal/notifications/msteams.go, which is
// the one-way Teams Workflow webhook (connection type `msteams`).
//
// Transport note: Bot Framework has no Socket-Mode equivalent. Microsoft
// pushes activities to a publicly reachable HTTPS endpoint, so the whole
// integration is gated behind `msteams.enabled` (default false) and a
// self-hosted instance must be reachable from the internet to use it.
package msteams

import "strings"

// Activity types we handle. The Bot Framework schema defines many more; the
// default branch of DispatchActivity ignores everything not listed here.
const (
	// ActivityTypeMessage is a user message (including @mentions of the bot).
	ActivityTypeMessage = "message"
	// ActivityTypeInstallationUpdate is emitted when the app is installed in
	// or removed from a team/chat. This is the Teams analog of Slack's
	// OAuth install callback and `app_uninstalled` event.
	ActivityTypeInstallationUpdate = "installationUpdate"
	// ActivityTypeConversationUpdate fires when members (including the bot)
	// are added to or removed from a conversation. Teams sends it for the
	// channel the app was installed into, which is how a channel becomes a
	// notification destination.
	ActivityTypeConversationUpdate = "conversationUpdate"
)

// installationUpdate actions.
const (
	// InstallActionAdd marks an install.
	InstallActionAdd = "add"
	// InstallActionRemove marks an uninstall. Teams also sends the legacy
	// "remove-upgrade"/"add-upgrade" pair during an app upgrade; only a plain
	// "remove" tears the connection down.
	InstallActionRemove = "remove"
)

// DestinationTypeChannel is the only destination kind supported in this
// phase. Personal-scope DMs are phase 2 (see the spec's capability map).
const DestinationTypeChannel = "channel"

// ChannelAccount identifies a user or bot inside a conversation.
type ChannelAccount struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	AadObjID string `json:"aadObjectId,omitempty"`
}

// ConversationAccount identifies the conversation an activity belongs to.
// For a channel-scoped Teams install, ID is the channel's conversation id and
// ConversationType is "channel".
type ConversationAccount struct {
	ID               string `json:"id"`
	Name             string `json:"name,omitempty"`
	ConversationType string `json:"conversationType,omitempty"`
	TenantID         string `json:"tenantId,omitempty"`
	IsGroup          bool   `json:"isGroup,omitempty"`
}

// TeamsChannelInfo is the `channelData.channel` object.
type TeamsChannelInfo struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

// TeamsTeamInfo is the `channelData.team` object.
type TeamsTeamInfo struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	AadGroup string `json:"aadGroupId,omitempty"`
}

// TeamsTenantInfo is the `channelData.tenant` object — the authoritative
// tenant identity on an inbound activity.
type TeamsTenantInfo struct {
	ID string `json:"id,omitempty"`
}

// ChannelData carries the Teams-specific envelope of an activity.
type ChannelData struct {
	Channel   *TeamsChannelInfo `json:"channel,omitempty"`
	Team      *TeamsTeamInfo    `json:"team,omitempty"`
	Tenant    *TeamsTenantInfo  `json:"tenant,omitempty"`
	EventType string            `json:"eventType,omitempty"`
}

// Mention is an `entities[]` entry of type "mention". Teams inlines the
// mention in `text` as `<at>Display Name</at>` and describes it here; the
// parser strips the inline form before tokenizing (see parser.go).
type Mention struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Mentioned *ChannelAccount `json:"mentioned,omitempty"`
}

// Attachment is a Bot Framework attachment. For our purposes it always
// carries an Adaptive Card.
type Attachment struct {
	ContentType string `json:"contentType"`
	Content     any    `json:"content,omitempty"`
}

// AdaptiveCardContentType is the attachment content type for Adaptive Cards.
const AdaptiveCardContentType = "application/vnd.microsoft.card.adaptive"

// Activity is the Bot Framework activity envelope, narrowed to the fields
// this integration reads or writes.
type Activity struct {
	Type           string               `json:"type"`
	ID             string               `json:"id,omitempty"`
	Timestamp      string               `json:"timestamp,omitempty"`
	ServiceURL     string               `json:"serviceUrl,omitempty"`
	ChannelID      string               `json:"channelId,omitempty"`
	From           *ChannelAccount      `json:"from,omitempty"`
	Recipient      *ChannelAccount      `json:"recipient,omitempty"`
	Conversation   *ConversationAccount `json:"conversation,omitempty"`
	Text           string               `json:"text,omitempty"`
	TextFormat     string               `json:"textFormat,omitempty"`
	Locale         string               `json:"locale,omitempty"`
	Action         string               `json:"action,omitempty"`
	ReplyToID      string               `json:"replyToId,omitempty"`
	Entities       []Mention            `json:"entities,omitempty"`
	Attachments    []Attachment         `json:"attachments,omitempty"`
	MembersAdded   []ChannelAccount     `json:"membersAdded,omitempty"`
	MembersRemoved []ChannelAccount     `json:"membersRemoved,omitempty"`
	ChannelData    *ChannelData         `json:"channelData,omitempty"`
}

// TenantID resolves the activity's tenant from channelData (authoritative)
// falling back to the conversation object. Returns "" when neither is set,
// which callers treat as "cannot route".
func (a *Activity) TenantID() string {
	if a.ChannelData != nil && a.ChannelData.Tenant != nil && a.ChannelData.Tenant.ID != "" {
		return a.ChannelData.Tenant.ID
	}

	if a.Conversation != nil {
		return a.Conversation.TenantID
	}

	return ""
}

// ConversationID returns the conversation this activity belongs to, or "".
func (a *Activity) ConversationID() string {
	if a.Conversation == nil {
		return ""
	}

	return a.Conversation.ID
}

// IsPersonalConversation reports whether the activity came from a 1:1 chat
// with the bot rather than a shared channel or group chat.
func (a *Activity) IsPersonalConversation() bool {
	if a.Conversation == nil {
		return false
	}

	return strings.EqualFold(a.Conversation.ConversationType, "personal")
}

// TeamID returns the owning Teams team id, or "" for a non-team conversation.
func (a *Activity) TeamID() string {
	if a.ChannelData == nil || a.ChannelData.Team == nil {
		return ""
	}

	return a.ChannelData.Team.ID
}

// ConversationName derives a human label for the conversation: the Teams
// channel name when present (channelData.channel.name is empty for the
// team's "General" channel, so fall back to the team name), then the
// conversation's own name.
func (a *Activity) ConversationName() string {
	if a.ChannelData != nil {
		if a.ChannelData.Channel != nil && a.ChannelData.Channel.Name != "" {
			return a.ChannelData.Channel.Name
		}

		if a.ChannelData.Team != nil && a.ChannelData.Team.Name != "" {
			// Teams omits the channel name for General; the team name is the
			// closest honest label rather than a blank picker entry.
			return a.ChannelData.Team.Name
		}
	}

	if a.Conversation != nil && a.Conversation.Name != "" {
		return a.Conversation.Name
	}

	return ""
}

// TeamName returns the owning team's display name, or "".
func (a *Activity) TeamName() string {
	if a.ChannelData == nil || a.ChannelData.Team == nil {
		return ""
	}

	return a.ChannelData.Team.Name
}

// AdaptiveCard is the subset of the Adaptive Card schema this package emits.
type AdaptiveCard struct {
	Type    string             `json:"type"`
	Schema  string             `json:"$schema,omitempty"`
	Version string             `json:"version"`
	Body    []CardElement      `json:"body,omitempty"`
	Actions []CardAction       `json:"actions,omitempty"`
	MSTeams *CardTeamsSettings `json:"msteams,omitempty"`
}

// CardTeamsSettings carries Teams-specific card rendering hints.
type CardTeamsSettings struct {
	Width string `json:"width,omitempty"`
}

// CardElement doubles as a TextBlock and a FactSet — the only two element
// kinds the cards here need. omitempty keeps the marshaled JSON minimal.
type CardElement struct {
	Type     string     `json:"type"`
	Text     string     `json:"text,omitempty"`
	Size     string     `json:"size,omitempty"`
	Weight   string     `json:"weight,omitempty"`
	Color    string     `json:"color,omitempty"`
	Wrap     bool       `json:"wrap,omitempty"`
	IsSubtle bool       `json:"isSubtle,omitempty"`
	Facts    []CardFact `json:"facts,omitempty"`
}

// CardFact is a FactSet row.
type CardFact struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

// CardAction is an Adaptive Card action; only Action.OpenUrl is used.
type CardAction struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}

// Adaptive Card constants.
const (
	CardTypeAdaptive   = "AdaptiveCard"
	CardSchema         = "http://adaptivecards.io/schemas/adaptive-card.json"
	CardVersion        = "1.4"
	CardElementText    = "TextBlock"
	CardElementFactSet = "FactSet"
	CardActionOpenURL  = "Action.OpenUrl"
	CardColorAttention = "Attention"
	CardColorGood      = "Good"
	CardColorWarning   = "Warning"
	// CardColorAccent is the neutral-informational Adaptive Card color, used for
	// non-severity content such as incident comments.
	CardColorAccent = "Accent"
	CardWidthFull   = "Full"
)

// NewCardMessage wraps an Adaptive Card in a message activity, with `text` as
// the plain-text fallback used in notifications and activity feeds.
func NewCardMessage(fallback string, card *AdaptiveCard) *Activity {
	return &Activity{
		Type: ActivityTypeMessage,
		Text: fallback,
		Attachments: []Attachment{
			{ContentType: AdaptiveCardContentType, Content: card},
		},
	}
}

// NewTextMessage builds a plain-text message activity.
func NewTextMessage(text string) *Activity {
	return &Activity{Type: ActivityTypeMessage, Text: text}
}

// SimpleCard builds a one-TextBlock card, the shape used for every command
// reply that isn't a fact table.
func SimpleCard(title, body, color string) *AdaptiveCard {
	elements := make([]CardElement, 0, 2)

	if title != "" {
		elements = append(elements, CardElement{
			Type:   CardElementText,
			Text:   title,
			Size:   "Large",
			Weight: "Bolder",
			Color:  color,
			Wrap:   true,
		})
	}

	if body != "" {
		elements = append(elements, CardElement{Type: CardElementText, Text: body, Wrap: true})
	}

	return &AdaptiveCard{
		Type:    CardTypeAdaptive,
		Schema:  CardSchema,
		Version: CardVersion,
		Body:    elements,
		MSTeams: &CardTeamsSettings{Width: CardWidthFull},
	}
}
