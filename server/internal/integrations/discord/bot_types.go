package discord

import "strings"

// Discord API constants shared by the bot client, the interactions endpoint
// and the Gateway supervisor.
const (
	// APIBaseURL is the pinned Discord REST API base. Pinned to a major
	// version rather than floating so a Discord rollout cannot silently
	// change payload semantics under a running deployment.
	APIBaseURL = "https://discord.com/api/v10"

	// OAuthAuthorizeURL is where a bot install starts.
	OAuthAuthorizeURL = "https://discord.com/oauth2/authorize"
	// OAuthTokenURL is the code→token exchange endpoint.
	OAuthTokenURL = "https://discord.com/api/oauth2/token"
)

// Discord channel types. Only the text-ish ones can receive a notification,
// which is what the destinations picker filters on.
const (
	ChannelTypeGuildText          = 0
	ChannelTypeGuildVoice         = 2
	ChannelTypeGuildCategory      = 4
	ChannelTypeGuildAnnouncement  = 5
	ChannelTypeAnnouncementThread = 10
	ChannelTypePublicThread       = 11
	ChannelTypePrivateThread      = 12
)

// IsPostableChannelType reports whether a guild channel of this type can
// receive a top-level incident message. Voice and category rows are returned
// by Discord's channel list and would otherwise show up in the picker as
// destinations that silently fail at send time.
func IsPostableChannelType(t int) bool {
	return t == ChannelTypeGuildText || t == ChannelTypeGuildAnnouncement
}

// IsThreadChannelType reports whether a channel id refers to a thread.
func IsThreadChannelType(t int) bool {
	return t == ChannelTypePublicThread ||
		t == ChannelTypePrivateThread ||
		t == ChannelTypeAnnouncementThread
}

// Message component types (Discord "message components").
const (
	ComponentTypeActionRow = 1
	ComponentTypeButton    = 2
)

// Button styles.
const (
	ButtonStylePrimary   = 1
	ButtonStyleSecondary = 2
	ButtonStyleSuccess   = 3
	ButtonStyleDanger    = 4
	ButtonStyleLink      = 5
)

// Interaction types delivered to the interactions endpoint.
const (
	InteractionTypePing               = 1
	InteractionTypeApplicationCommand = 2
	InteractionTypeMessageComponent   = 3
	InteractionTypeAutocomplete       = 4
	InteractionTypeModalSubmit        = 5
)

// Interaction callback (response) types.
const (
	InteractionCallbackPong                 = 1
	InteractionCallbackChannelMessage       = 4
	InteractionCallbackDeferredChannelMsg   = 5
	InteractionCallbackDeferredUpdateMsg    = 6
	InteractionCallbackUpdateMessage        = 7
	InteractionCallbackAutocompleteResponse = 8
)

// MessageFlagEphemeral marks an interaction reply as visible only to the
// invoking user — the Discord equivalent of Slack's ephemeral response type.
const MessageFlagEphemeral = 1 << 6

// autoArchiveDurationOneWeek is the longest auto-archive window Discord
// offers. Threads still archive, which is why the sender un-archives before
// every late follow-up; asking for the maximum simply makes that rarer.
const autoArchiveDurationOneWeek = 10080

// Message is an outbound bot message (REST `POST /channels/{id}/messages` and
// `PATCH .../{message_id}`).
//
//nolint:tagliatelle // Discord API uses snake_case
type Message struct {
	Content         string           `json:"content,omitempty"`
	Embeds          []Embed          `json:"embeds,omitempty"`
	Components      []Component      `json:"components"`
	AllowedMentions *AllowedMentions `json:"allowed_mentions,omitempty"`
}

// AllowedMentions bounds who a message may ping. SolidPing only ever pings the
// specific users it resolved from the escalation policy, so `parse` stays empty
// (which disables @everyone/@here/role pings) and `users` carries the explicit
// allow-list.
//
//nolint:tagliatelle // Discord API uses snake_case
type AllowedMentions struct {
	Parse []string `json:"parse"`
	Users []string `json:"users,omitempty"`
}

// Component is a message component. Only action rows and buttons are used;
// the shape is flat rather than a union so the JSON stays simple.
//
//nolint:tagliatelle // Discord API uses snake_case
type Component struct {
	Type       int         `json:"type"`
	Style      int         `json:"style,omitempty"`
	Label      string      `json:"label,omitempty"`
	CustomID   string      `json:"custom_id,omitempty"`
	URL        string      `json:"url,omitempty"`
	Disabled   bool        `json:"disabled,omitempty"`
	Components []Component `json:"components,omitempty"`
}

// MessageResult is the subset of a created/fetched message we keep.
//
//nolint:tagliatelle // Discord API uses snake_case
type MessageResult struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
}

// ChannelInfo is a guild channel or thread as returned by the REST API.
//
//nolint:tagliatelle // Discord API uses snake_case
type ChannelInfo struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Type     int             `json:"type"`
	ParentID string          `json:"parent_id,omitempty"`
	Position int             `json:"position,omitempty"`
	Metadata *ThreadMetadata `json:"thread_metadata,omitempty"`
}

// ThreadMetadata carries a thread's archive state.
//
//nolint:tagliatelle // Discord API uses snake_case
type ThreadMetadata struct {
	Archived            bool `json:"archived"`
	Locked              bool `json:"locked"`
	AutoArchiveDuration int  `json:"auto_archive_duration,omitempty"`
}

// Guild is the subset of a guild object we need.
type Guild struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// User is the subset of a Discord user object we need.
//
//nolint:tagliatelle // Discord API uses snake_case
type User struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name,omitempty"`
	Bot        bool   `json:"bot,omitempty"`
}

// DisplayName prefers the global (display) name over the raw username.
func (u *User) DisplayName() string {
	if u == nil {
		return ""
	}

	if u.GlobalName != "" {
		return u.GlobalName
	}

	return u.Username
}

// Message-component custom ids. Discord gives a component a single opaque
// string, so the action and its subject are packed into one value with a `:`
// separator — the Discord counterpart of Slack's action_id + value pair.
const (
	// ActionAcknowledge acknowledges an incident.
	ActionAcknowledge = "ack"
	// ActionUnavailable declares the presser unavailable.
	ActionUnavailable = "unavailable"
	// ActionEscalate escalates the incident immediately.
	ActionEscalate = "escalate"

	// customIDSeparator splits the action from its subject.
	customIDSeparator = ":"
)

// BuildCustomID packs an action and its subject into a component custom id.
func BuildCustomID(action, subject string) string {
	return action + customIDSeparator + subject
}

// ParseCustomID splits a component custom id back into its action and subject.
// A malformed id yields ("", "") so a caller can reject it without a panic.
func ParseCustomID(customID string) (string, string) {
	action, subject, found := strings.Cut(customID, customIDSeparator)
	if !found || action == "" || subject == "" {
		return "", ""
	}

	return action, subject
}

// IncidentActionRow builds the action row attached to an active incident
// message. Kept here rather than in the notifications package so the sender
// (which writes the buttons) and the interactions endpoint (which reads their
// custom ids) share one definition and cannot drift.
func IncidentActionRow(incidentUID string) Component {
	return Component{
		Type: ComponentTypeActionRow,
		Components: []Component{
			{
				Type:     ComponentTypeButton,
				Style:    ButtonStyleSuccess,
				Label:    "Acknowledge",
				CustomID: BuildCustomID(ActionAcknowledge, incidentUID),
			},
			{
				Type:     ComponentTypeButton,
				Style:    ButtonStyleSecondary,
				Label:    "I'm unavailable",
				CustomID: BuildCustomID(ActionUnavailable, incidentUID),
			},
			{
				Type:     ComponentTypeButton,
				Style:    ButtonStyleDanger,
				Label:    "Escalate",
				CustomID: BuildCustomID(ActionEscalate, incidentUID),
			},
		},
	}
}

// State-entry key schema shared between the notification sender (which writes
// the reverse thread mapping when it opens an incident's thread) and the
// Gateway (which resolves an inbound reply back to its incident).
const (
	reverseThreadStatePrefix = "discord/threads/"
	commentDedupeStatePrefix = "discord/comments/"

	// ThreadIncidentUIDKey / ThreadOrgUIDKey are the value keys of a reverse
	// thread entry. Exported so the writer and the reader cannot drift.
	ThreadIncidentUIDKey = "incident_uid"
	ThreadOrgUIDKey      = "organization_uid"
)

// ReverseThreadStateKey maps a Discord thread (guild, thread) back to its
// incident. Stored as a global (org-nil) entry: the guild's home org need not
// own the incident, so the value carries the incident's org UID rather than
// scoping the key to it.
func ReverseThreadStateKey(guildID, threadID string) string {
	return reverseThreadStatePrefix + guildID + "/" + threadID
}

// CommentDedupeStateKey builds the per-message idempotency marker key that
// stops a Gateway redelivery (a RESUME replaying events, a reconnect) from
// recording the same comment twice.
func CommentDedupeStateKey(guildID, channelID, messageID string) string {
	return commentDedupeStatePrefix + guildID + "/" + channelID + "/" + messageID
}
