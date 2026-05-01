// Package slack provides Slack integration functionality for SolidPing.
//
//nolint:tagliatelle // Slack API uses snake_case JSON field names
package slack

// Slack Block Kit element types.
const (
	BlockTypeMrkdwn    = "mrkdwn"
	BlockTypePlainText = "plain_text"
	BlockTypeHeader    = "header"
	BlockTypeSection   = "section"
	BlockTypeContext   = "context"
	BlockTypeButton    = "button"

	// ResponseTypeEphemeral is the Slack ephemeral response type.
	ResponseTypeEphemeral = "ephemeral"
)

// OAuthResponse represents the response from Slack's OAuth token exchange.
type OAuthResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	BotUserID   string `json:"bot_user_id"`
	AppID       string `json:"app_id"`
	Team        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
	Enterprise      interface{} `json:"enterprise"`
	AuthedUser      AuthedUser  `json:"authed_user"`
	IncomingWebhook interface{} `json:"incoming_webhook,omitempty"`
}

// AuthedUser represents the authenticated user in OAuth response.
type AuthedUser struct {
	ID          string `json:"id"`
	Scope       string `json:"scope"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// UserProfile represents a Slack user's profile with email.
type UserProfile struct {
	Email                 string `json:"email"`
	RealName              string `json:"real_name"`
	RealNameNormalized    string `json:"real_name_normalized"`
	DisplayName           string `json:"display_name"`
	DisplayNameNormalized string `json:"display_name_normalized"`
	Image24               string `json:"image_24"`
	Image32               string `json:"image_32"`
	Image48               string `json:"image_48"`
	Image72               string `json:"image_72"`
	Image192              string `json:"image_192"`
	Image512              string `json:"image_512"`
}

// UserDetails represents detailed Slack user information including profile.
type UserDetails struct {
	ID       string      `json:"id"`
	TeamID   string      `json:"team_id"`
	Name     string      `json:"name"`
	RealName string      `json:"real_name"`
	Profile  UserProfile `json:"profile"`
	IsAdmin  bool        `json:"is_admin"`
	IsOwner  bool        `json:"is_owner"`
}

// Event represents an incoming Slack event.
type Event struct {
	Token     string `json:"token"`
	TeamID    string `json:"team_id"`
	APIAppID  string `json:"api_app_id"`
	Type      string `json:"type"`
	Challenge string `json:"challenge,omitempty"` // For URL verification

	Event        EventPayload `json:"event,omitempty"`
	EventID      string       `json:"event_id,omitempty"`
	EventTime    int64        `json:"event_time,omitempty"`
	EventContext string       `json:"event_context,omitempty"`
}

// EventPayload represents the event data within a Slack event.
type EventPayload struct {
	Type        string `json:"type"`
	User        string `json:"user"`
	Channel     string `json:"channel,omitempty"`
	Text        string `json:"text,omitempty"`
	Ts          string `json:"ts,omitempty"`
	ThreadTs    string `json:"thread_ts,omitempty"` // For threaded messages
	EventTs     string `json:"event_ts,omitempty"`
	ChannelType string `json:"channel_type,omitempty"`
	Tab         string `json:"tab,omitempty"`        // For app_home_opened
	Links       []Link `json:"links,omitempty"`      // For link_shared
	MessageTs   string `json:"message_ts,omitempty"` // For link_shared
	Subtype     string `json:"subtype,omitempty"`
	BotID       string `json:"bot_id,omitempty"`
}

// Link represents a shared link in link_shared events.
type Link struct {
	Domain string `json:"domain"`
	URL    string `json:"url"`
}

// Command represents an incoming slash command.
type Command struct {
	Token          string `form:"token"`
	TeamID         string `form:"team_id"`
	TeamDomain     string `form:"team_domain"`
	EnterpriseID   string `form:"enterprise_id"`
	EnterpriseName string `form:"enterprise_name"`
	ChannelID      string `form:"channel_id"`
	ChannelName    string `form:"channel_name"`
	UserID         string `form:"user_id"`
	UserName       string `form:"user_name"`
	Command        string `form:"command"`
	Text           string `form:"text"`
	ResponseURL    string `form:"response_url"`
	TriggerID      string `form:"trigger_id"`
	APIAppID       string `form:"api_app_id"`
	ThreadTS       string `form:"thread_ts"` // Present if command invoked from a thread
}

// InteractionMessage represents the message where the interaction originated.
type InteractionMessage struct {
	Ts       string `json:"ts"`
	ThreadTs string `json:"thread_ts,omitempty"`
}

// InteractionContainer represents the container where the interaction originated.
type InteractionContainer struct {
	Type      string `json:"type"`
	MessageTs string `json:"message_ts"`
	ChannelID string `json:"channel_id"`
	ThreadTs  string `json:"thread_ts,omitempty"`
}

// Interaction represents an incoming interaction payload.
type Interaction struct {
	Type        string               `json:"type"`
	Token       string               `json:"token"`
	ActionTs    string               `json:"action_ts"`
	Team        Team                 `json:"team"`
	User        User                 `json:"user"`
	Channel     Channel              `json:"channel,omitempty"`
	Message     *InteractionMessage  `json:"message,omitempty"`
	Container   InteractionContainer `json:"container,omitempty"`
	CallbackID  string               `json:"callback_id"`
	TriggerID   string               `json:"trigger_id"`
	ResponseURL string               `json:"response_url,omitempty"`
	Actions     []InteractionAction  `json:"actions,omitempty"`
	View        *View                `json:"view,omitempty"`
}

// Team represents a Slack team/workspace.
type Team struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
}

// User represents a Slack user.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	TeamID   string `json:"team_id"`
}

// Channel represents a Slack channel.
type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// View represents a Slack modal view.
type View struct {
	ID              string     `json:"id"`
	TeamID          string     `json:"team_id"`
	Type            string     `json:"type"`
	Title           *Text      `json:"title"`
	CallbackID      string     `json:"callback_id"`
	State           *ViewState `json:"state,omitempty"`
	PrivateMetadata string     `json:"private_metadata,omitempty"`
	Hash            string     `json:"hash,omitempty"`
}

// Text represents a Slack text object.
type Text struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Emoji bool   `json:"emoji,omitempty"`
}

// ViewState represents the state of a modal view.
type ViewState struct {
	Values map[string]map[string]InputValue `json:"values"`
}

// InputValue represents an input value in a view state.
type InputValue struct {
	Type                 string  `json:"type"`
	Value                string  `json:"value,omitempty"`
	SelectedOption       *Option `json:"selected_option,omitempty"`
	SelectedUser         string  `json:"selected_user,omitempty"`
	SelectedChannel      string  `json:"selected_channel,omitempty"`
	SelectedConversation string  `json:"selected_conversation,omitempty"`
}

// Option represents a select option.
type Option struct {
	Text  Text   `json:"text"`
	Value string `json:"value"`
}

// InteractionAction represents an action in an interaction.
type InteractionAction struct {
	ActionID string `json:"action_id"`
	BlockID  string `json:"block_id"`
	Type     string `json:"type"`
	Value    string `json:"value,omitempty"`
	Text     *Text  `json:"text,omitempty"`
}

// MessageResponse represents a Slack message response.
type MessageResponse struct {
	ResponseType    string       `json:"response_type,omitempty"` // "in_channel" or "ephemeral"
	Text            string       `json:"text,omitempty"`
	Blocks          []Block      `json:"blocks,omitempty"`
	Attachments     []Attachment `json:"attachments,omitempty"`
	ReplaceOriginal bool         `json:"replace_original,omitempty"`
	DeleteOriginal  bool         `json:"delete_original,omitempty"`
}

// Attachment represents a Slack message attachment with optional colored sidebar.
type Attachment struct {
	Color    string  `json:"color,omitempty"`    // Hex color code (e.g., "#FF0000") or named color
	Fallback string  `json:"fallback,omitempty"` // Plain text summary for notifications
	Blocks   []Block `json:"blocks,omitempty"`   // Block Kit blocks within the attachment
}

// Block represents a Slack Block Kit block.
type Block struct {
	Type      string   `json:"type"`
	Text      *Text    `json:"text,omitempty"`
	BlockID   string   `json:"block_id,omitempty"`
	Elements  []any    `json:"elements,omitempty"` // Can be Element or ContextElement
	Fields    []Text   `json:"fields,omitempty"`
	Accessory *Element `json:"accessory,omitempty"`
}

// Element represents a block element.
type Element struct {
	Type     string `json:"type"`
	Text     *Text  `json:"text,omitempty"`
	ActionID string `json:"action_id,omitempty"`
	URL      string `json:"url,omitempty"`
	Value    string `json:"value,omitempty"`
	Style    string `json:"style,omitempty"`
}

// ContextElement represents a text element in a context block.
// Context blocks use a simpler text format where "text" is a string, not an object.
type ContextElement struct {
	Type string `json:"type"` // "mrkdwn" or "plain_text"
	Text string `json:"text"`
}

// UnfurlRequest represents a link unfurl request.
type UnfurlRequest struct {
	Channel string            `json:"channel"`
	Ts      string            `json:"ts"`
	Unfurls map[string]Unfurl `json:"unfurls"`
}

// Unfurl represents an unfurled link preview.
type Unfurl struct {
	Blocks []Block `json:"blocks,omitempty"`
	Text   string  `json:"text,omitempty"`
}

// AppHomeView represents the App Home tab view.
type AppHomeView struct {
	Type   string  `json:"type"` // "home"
	Blocks []Block `json:"blocks"`
}

// ModalView represents a modal view.
type ModalView struct {
	Type            string  `json:"type"` // "modal"
	Title           Text    `json:"title"`
	Submit          *Text   `json:"submit,omitempty"`
	Close           *Text   `json:"close,omitempty"`
	CallbackID      string  `json:"callback_id"`
	PrivateMetadata string  `json:"private_metadata,omitempty"`
	Blocks          []Block `json:"blocks"`
}
