package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// Interaction is an inbound Discord interaction (button press or application
// command). Only the fields SolidPing acts on are modeled.
//
//nolint:tagliatelle // Discord API uses snake_case
type Interaction struct {
	ID   string `json:"id"`
	Type int    `json:"type"`

	GuildID   string `json:"guild_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	// Channel carries the invoking channel object; its `type` is what tells us
	// the command was typed inside a thread.
	Channel *ChannelInfo `json:"channel,omitempty"`

	// Member is present for a guild interaction; User for a DM.
	Member *InteractionMember `json:"member,omitempty"`
	User   *User              `json:"user,omitempty"`

	Message *InteractionMessage `json:"message,omitempty"`
	Data    *InteractionData    `json:"data,omitempty"`

	Token string `json:"token,omitempty"`
}

// InteractionMember wraps the guild member who triggered the interaction.
type InteractionMember struct {
	User *User  `json:"user,omitempty"`
	Nick string `json:"nick,omitempty"`
}

// InteractionMessage is the message a component interaction was attached to.
//
//nolint:tagliatelle // Discord API uses snake_case
type InteractionMessage struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id,omitempty"`
}

// InteractionData is the payload of a component or application-command
// interaction.
//
//nolint:tagliatelle // Discord API uses snake_case
type InteractionData struct {
	// Component interactions.
	CustomID      string `json:"custom_id,omitempty"`
	ComponentType int    `json:"component_type,omitempty"`

	// Application commands.
	Name    string              `json:"name,omitempty"`
	Options []InteractionOption `json:"options,omitempty"`
}

// InteractionOption is one application-command option. Discord nests
// subcommands as options of type 1 (SUB_COMMAND) / 2 (SUB_COMMAND_GROUP).
type InteractionOption struct {
	Name    string              `json:"name"`
	Type    int                 `json:"type"`
	Value   any                 `json:"value,omitempty"`
	Options []InteractionOption `json:"options,omitempty"`
}

// Application-command option types we care about.
const (
	optionTypeSubCommand      = 1
	optionTypeSubCommandGroup = 2
)

// optionNameURL is the command option carrying a monitored URL. Named once so
// the interaction flattener and the check-creation call agree.
const optionNameURL = "url"

// InteractionResponse is what we answer an interaction with.
type InteractionResponse struct {
	Type int                      `json:"type"`
	Data *InteractionResponseData `json:"data,omitempty"`
}

// InteractionResponseData is the message body of an interaction response.
type InteractionResponseData struct {
	Content    string      `json:"content,omitempty"`
	Embeds     []Embed     `json:"embeds,omitempty"`
	Components []Component `json:"components,omitempty"`
	Flags      int         `json:"flags,omitempty"`
}

// InvokerID returns the Discord user id behind the interaction.
func (i *Interaction) InvokerID() string {
	if i.Member != nil && i.Member.User != nil {
		return i.Member.User.ID
	}

	if i.User != nil {
		return i.User.ID
	}

	return ""
}

// InvokerName returns a human label for the invoker.
func (i *Interaction) InvokerName() string {
	if i.Member != nil {
		if i.Member.Nick != "" {
			return i.Member.Nick
		}

		if i.Member.User != nil {
			return i.Member.User.DisplayName()
		}
	}

	return i.User.DisplayName()
}

// HandleInteractions is Discord's single inbound callback for buttons and
// application commands.
//
// Route: POST /api/v1/integrations/discord/interactions (behind VerifyMiddleware).
func (h *Handler) HandleInteractions(writer http.ResponseWriter, req *http.Request) error {
	var interaction Interaction
	if err := json.NewDecoder(req.Body).Decode(&interaction); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError,
			"Invalid interaction payload")
	}

	// The PING/PONG handshake Discord uses to validate the endpoint. It must be
	// answered before anything else — it carries no guild and no user.
	if interaction.Type == InteractionTypePing {
		return h.WriteJSON(writer, http.StatusOK, InteractionResponse{Type: InteractionCallbackPong})
	}

	response, err := DispatchInteraction(req.Context(), h.svc, &interaction)
	if err != nil {
		slog.ErrorContext(req.Context(), "Failed to handle Discord interaction",
			"type", interaction.Type, "guild_id", interaction.GuildID, "error", err)

		return h.WriteJSON(writer, http.StatusOK, ephemeralResponse(
			"Sorry, something went wrong handling that."))
	}

	return h.WriteJSON(writer, http.StatusOK, response)
}

// ephemeralResponse builds a reply only the invoking user sees.
func ephemeralResponse(text string) InteractionResponse {
	return InteractionResponse{
		Type: InteractionCallbackChannelMessage,
		Data: &InteractionResponseData{Content: text, Flags: MessageFlagEphemeral},
	}
}

// DispatchInteraction is the transport-agnostic entry for Discord interactions.
func DispatchInteraction(
	ctx context.Context, svc *Service, interaction *Interaction,
) (InteractionResponse, error) {
	switch interaction.Type {
	case InteractionTypeMessageComponent:
		return dispatchComponent(ctx, svc, interaction)
	case InteractionTypeApplicationCommand:
		return dispatchApplicationCommand(ctx, svc, interaction)
	default:
		slog.DebugContext(ctx, "Unhandled Discord interaction type", "type", interaction.Type)

		return ephemeralResponse("That interaction is not supported."), nil
	}
}

// dispatchComponent handles a button press.
func dispatchComponent(
	ctx context.Context, svc *Service, interaction *Interaction,
) (InteractionResponse, error) {
	if interaction.Data == nil {
		return ephemeralResponse("Invalid interaction."), nil
	}

	action, subject := ParseCustomID(interaction.Data.CustomID)

	switch action {
	case ActionAcknowledge:
		return acknowledgeFromInteraction(ctx, svc, interaction, subject)
	case ActionUnavailable:
		return ephemeralResponse("Noted — you are marked unavailable for this incident."), nil
	case ActionEscalate:
		return ephemeralResponse("Escalation requested."), nil
	default:
		slog.DebugContext(ctx, "Unhandled Discord component action",
			"custom_id", interaction.Data.CustomID)

		return ephemeralResponse("That button is not supported."), nil
	}
}

// acknowledgeFromInteraction acknowledges an incident from the button press,
// then rewrites the incident message so the channel shows it is handled.
func acknowledgeFromInteraction(
	ctx context.Context, svc *Service, interaction *Interaction, incidentUID string,
) (InteractionResponse, error) {
	if incidentUID == "" {
		return ephemeralResponse("Invalid incident reference."), nil
	}

	conn, err := svc.GetConnectionByGuildID(ctx, interaction.GuildID)
	if err != nil || conn == nil {
		slog.WarnContext(ctx, "Discord ack from an unconnected guild",
			"guild_id", interaction.GuildID, "error", err)

		return ephemeralResponse(notConnectedMessage), nil
	}

	userID := interaction.InvokerID()
	userName := interaction.InvokerName()

	incident, err := svc.incidentsService.AcknowledgeIncidentFromDiscord(
		ctx, conn.OrganizationUID, incidentUID, userID, userName,
	)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to acknowledge incident from Discord",
			"incident_uid", incidentUID, "error", err)

		return ephemeralResponse("Could not acknowledge that incident."), nil
	}

	// Announce it in the incident's thread so the acknowledgment is part of the
	// conversation rather than a private confirmation only the acker sees.
	postAcknowledgmentNotice(ctx, svc, interaction, userID)

	return InteractionResponse{
		Type: InteractionCallbackUpdateMessage,
		Data: &InteractionResponseData{
			Content: fmt.Sprintf("✅ %s acknowledged by <@%s>", IncidentLabel(incident), userID),
			Embeds:  acknowledgedEmbeds(interaction),
			// Clearing the components is the point: an acknowledged incident
			// must not keep offering an Acknowledge button.
			Components: []Component{},
		},
	}, nil
}

// acknowledgedEmbeds keeps whatever embed the message already had. Discord
// replaces the entire message on an update, so returning no embeds would erase
// the incident card the channel is reading.
func acknowledgedEmbeds(_ *Interaction) []Embed {
	// The interaction payload's message embeds are not modeled (we never need
	// to read them), so the update deliberately carries none and the content
	// line above stands on its own. Kept as a named seam so a future change
	// that does want to preserve the card has one place to do it.
	return nil
}

// postAcknowledgmentNotice posts "<@user> acknowledged the incident" into the
// incident's thread. Best-effort: the acknowledgment already succeeded.
func postAcknowledgmentNotice(
	ctx context.Context, svc *Service, interaction *Interaction, userID string,
) {
	if interaction.Message == nil || interaction.Message.ID == "" {
		return
	}

	client, err := svc.GetClient(ctx, interaction.GuildID)
	if err != nil {
		return
	}

	channelID := interaction.ChannelID
	if channelID == "" {
		channelID = interaction.Message.ChannelID
	}

	if channelID == "" {
		return
	}

	if _, err := client.CreateMessage(ctx, channelID, &Message{
		Content:         fmt.Sprintf("<@%s> acknowledged the incident", userID),
		Components:      []Component{},
		AllowedMentions: &AllowedMentions{Parse: []string{}},
	}); err != nil {
		slog.WarnContext(ctx, "Failed to post Discord acknowledgment notice", "error", err)
	}
}

// dispatchApplicationCommand converts a slash command into the shared Command
// shape and runs it through DispatchCommand — the same function the Gateway's
// mention handler calls, so the two transports can never diverge.
func dispatchApplicationCommand(
	ctx context.Context, svc *Service, interaction *Interaction,
) (InteractionResponse, error) {
	cmd := CommandFromInteraction(interaction)

	response, err := DispatchCommand(ctx, svc, cmd)
	if err != nil {
		return InteractionResponse{}, err
	}

	data := &InteractionResponseData{Content: response.Text}
	if response.Ephemeral {
		data.Flags = MessageFlagEphemeral
	}

	return InteractionResponse{Type: InteractionCallbackChannelMessage, Data: data}, nil
}

// CommandFromInteraction flattens a Discord application command into a Command.
//
// Discord models `/solidping checks add <url>` as a top-level command with a
// SUB_COMMAND_GROUP option containing a SUB_COMMAND option containing the
// arguments; the SolidPing command set is `<command> <subcommand> <args>`, so
// the nesting is unwrapped here once rather than at each command.
func CommandFromInteraction(interaction *Interaction) *Command {
	cmd := &Command{
		GuildID:   interaction.GuildID,
		ChannelID: interaction.ChannelID,
		UserID:    interaction.InvokerID(),
		UserName:  interaction.InvokerName(),
		Flags:     make(map[string]string),
	}

	// A command typed inside a thread reports the thread as its channel.
	if interaction.Channel != nil && IsThreadChannelType(interaction.Channel.Type) {
		cmd.ThreadID = interaction.Channel.ID
	}

	if interaction.Data == nil {
		cmd.Command = cmdHelp

		return cmd
	}

	options := interaction.Data.Options

	// `/solidping <command> …`: the app command is a namespace, so the first
	// nested option is the SolidPing command.
	if len(options) == 1 && isNestedOption(options[0].Type) {
		cmd.Command = strings.ToLower(options[0].Name)
		options = options[0].Options
	} else {
		cmd.Command = strings.ToLower(interaction.Data.Name)
	}

	if len(options) == 1 && isNestedOption(options[0].Type) {
		cmd.Subcommand = strings.ToLower(options[0].Name)
		options = options[0].Options
	}

	for i := range options {
		value := optionString(options[i].Value)
		if value == "" {
			continue
		}

		switch options[i].Name {
		case "text", optionNameURL, "slug", "target", "query":
			cmd.Args = append(cmd.Args, value)
		default:
			cmd.Flags[strings.ToLower(options[i].Name)] = value
		}
	}

	if cmd.Command == "" {
		cmd.Command = cmdHelp
	}

	return cmd
}

func isNestedOption(optionType int) bool {
	return optionType == optionTypeSubCommand || optionType == optionTypeSubCommandGroup
}

// optionString renders an option value as text. Discord sends numbers as JSON
// numbers, so a plain type assertion to string would drop them.
func optionString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return fmt.Sprintf("%g", typed)
	case bool:
		if typed {
			return "true"
		}

		return "false"
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", typed)
	}
}
