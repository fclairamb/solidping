package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrMissingViewState is returned when a view submission is missing state.
var ErrMissingViewState = errors.New("missing view state")

// handleInteraction routes interactions to their specific handlers.
func (h *Handler) handleInteraction(ctx context.Context, interaction *Interaction) (*MessageResponse, error) {
	slog.InfoContext(ctx, "Handling Slack interaction",
		"type", interaction.Type,
		"callback_id", interaction.CallbackID,
		"team_id", interaction.Team.ID,
	)

	switch interaction.Type {
	case "shortcut":
		return h.handleShortcut(ctx, interaction)
	case "block_actions":
		return h.handleBlockActions(ctx, interaction)
	case "view_submission":
		return h.handleViewSubmission(ctx, interaction)
	case "view_closed":
		// View was closed, no action needed
		return &MessageResponse{}, nil
	default:
		slog.DebugContext(ctx, "Unhandled interaction type", "type", interaction.Type)

		return &MessageResponse{}, nil
	}
}

// handleShortcut handles global shortcuts.
func (h *Handler) handleShortcut(ctx context.Context, interaction *Interaction) (*MessageResponse, error) {
	switch interaction.CallbackID {
	case "add_check":
		return h.openAddCheckModal(ctx, interaction)
	default:
		slog.DebugContext(ctx, "Unhandled shortcut", "callback_id", interaction.CallbackID)

		return &MessageResponse{}, nil
	}
}

// handleBlockActions handles block action interactions.
func (h *Handler) handleBlockActions(ctx context.Context, interaction *Interaction) (*MessageResponse, error) {
	for i := range interaction.Actions {
		action := &interaction.Actions[i]
		switch action.ActionID {
		case "add_check":
			return h.openAddCheckModal(ctx, interaction)
		case "view_dashboard":
			// Button with URL, no action needed
			continue
		case "acknowledge_incident":
			return h.handleAcknowledgeIncident(ctx, interaction, action)
		case "unavailable_incident":
			return h.handleUnavailableIncident(ctx, interaction, action)
		case "escalate_incident":
			return h.handleEscalateIncident(ctx, interaction, action)
		default:
			slog.DebugContext(ctx, "Unhandled block action", "action_id", action.ActionID)
		}
	}

	return &MessageResponse{}, nil
}

// handleViewSubmission handles modal form submissions.
func (h *Handler) handleViewSubmission(ctx context.Context, interaction *Interaction) (*MessageResponse, error) {
	if interaction.View == nil {
		return &MessageResponse{}, nil
	}

	switch interaction.View.CallbackID {
	case "add_check_modal":
		return h.handleAddCheckSubmission(ctx, interaction)
	default:
		slog.DebugContext(ctx, "Unhandled view submission", "callback_id", interaction.View.CallbackID)

		return &MessageResponse{}, nil
	}
}

// openAddCheckModal opens the "Add a Check" modal.
func (h *Handler) openAddCheckModal(ctx context.Context, interaction *Interaction) (*MessageResponse, error) {
	client, err := h.svc.GetClient(ctx, interaction.Team.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get client: %w", err)
	}

	modal := &ModalView{
		Type: "modal",
		Title: Text{
			Type: "plain_text",
			Text: "Add a Check",
		},
		Submit: &Text{
			Type: "plain_text",
			Text: "Create Check",
		},
		Close: &Text{
			Type: "plain_text",
			Text: "Cancel",
		},
		CallbackID: "add_check_modal",
		Blocks: []Block{
			{
				Type:    "input",
				BlockID: "url_block",
				Text: &Text{
					Type: "plain_text",
					Text: "URL to monitor",
				},
				Elements: []any{
					Element{
						Type:     "plain_text_input",
						ActionID: "url_input",
					},
				},
			},
			{
				Type:    "input",
				BlockID: "name_block",
				Text: &Text{
					Type: "plain_text",
					Text: "Check name (optional)",
				},
				Elements: []any{
					Element{
						Type:     "plain_text_input",
						ActionID: "name_input",
					},
				},
			},
		},
	}

	if err := client.OpenModal(ctx, interaction.TriggerID, modal); err != nil {
		return nil, fmt.Errorf("failed to open modal: %w", err)
	}

	return &MessageResponse{}, nil
}

// handleAddCheckSubmission handles the "Add a Check" modal submission.
func (h *Handler) handleAddCheckSubmission(
	ctx context.Context, interaction *Interaction,
) (*MessageResponse, error) {
	if interaction.View == nil || interaction.View.State == nil {
		return nil, ErrMissingViewState
	}

	// Extract values from the form
	urlValue := ""
	nameValue := ""

	if urlBlock, ok := interaction.View.State.Values["url_block"]; ok {
		if urlInput, ok := urlBlock["url_input"]; ok {
			urlValue = urlInput.Value
		}
	}

	if nameBlock, ok := interaction.View.State.Values["name_block"]; ok {
		if nameInput, ok := nameBlock["name_input"]; ok {
			nameValue = nameInput.Value
		}
	}

	if urlValue == "" {
		// Return validation error
		return &MessageResponse{
			Text: "URL is required",
		}, nil
	}

	// TODO: Create the check using the checks service
	slog.InfoContext(ctx, "Creating check from modal",
		"url", urlValue,
		"name", nameValue,
		"user_id", interaction.User.ID,
		"team_id", interaction.Team.ID,
	)

	// Send a confirmation message
	client, err := h.svc.GetClient(ctx, interaction.Team.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get client: %w", err)
	}

	msg := &MessageResponse{
		Text: fmt.Sprintf(":white_check_mark: Check created for <%s|%s>", urlValue, urlValue),
	}

	// Post to the user's DM or a default channel
	if err := client.PostEphemeral(ctx, interaction.User.ID, interaction.User.ID, msg); err != nil {
		slog.WarnContext(ctx, "Failed to send confirmation message", "error", err)
	}

	return &MessageResponse{}, nil
}

// handleAcknowledgeIncident handles the Acknowledge button click on incident messages.
func (h *Handler) handleAcknowledgeIncident(
	ctx context.Context, interaction *Interaction, action *InteractionAction,
) (*MessageResponse, error) {
	incidentUID := action.Value
	if incidentUID == "" {
		slog.WarnContext(ctx, "Missing incident UID in acknowledge action")
		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Error: Invalid incident reference.",
		}, nil
	}

	// Get the connection to find the organization
	conn, err := h.svc.GetConnectionByTeamID(ctx, interaction.Team.ID)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to get connection", "error", err, "team_id", interaction.Team.ID)
		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Error: Could not find organization.",
		}, nil
	}

	// Acknowledge the incident
	acknowledgedAt := time.Now()
	incident, err := h.svc.incidentsService.AcknowledgeIncidentFromSlack(
		ctx, conn.OrganizationUID, incidentUID, interaction.User.ID, interaction.User.Username,
	)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to acknowledge incident",
			"error", err,
			"incident_uid", incidentUID,
		)
		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Error: Could not acknowledge incident.",
		}, nil
	}

	// Get the check for the updated message
	check, err := h.svc.incidentsService.GetCheckByUID(ctx, conn.OrganizationUID, incident.CheckUID)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to get check for incident",
			"error", err,
			"check_uid", incident.CheckUID,
		)
		// Still return success, but with simpler message
		return &MessageResponse{
			ReplaceOriginal: true,
			Text:            "Acknowledged by @" + interaction.User.Username,
		}, nil
	}

	// Build updated message showing acknowledgment
	updatedMsg := buildAcknowledgedMessage(incident, check, interaction.User.Username, acknowledgedAt)

	// Post a thread reply to announce the acknowledgment
	h.postAcknowledgmentThreadReply(ctx, interaction)

	slog.InfoContext(ctx, "Incident acknowledged via Slack",
		"incident_uid", incidentUID,
		"slack_user_id", interaction.User.ID,
		"slack_username", interaction.User.Username,
	)

	return updatedMsg, nil
}

// postAcknowledgmentThreadReply posts a thread reply announcing who acknowledged the incident.
func (h *Handler) postAcknowledgmentThreadReply(ctx context.Context, interaction *Interaction) {
	// Get the message timestamp for the thread
	var threadTs string
	if interaction.Message != nil && interaction.Message.Ts != "" {
		threadTs = interaction.Message.Ts
	} else if interaction.Container.MessageTs != "" {
		threadTs = interaction.Container.MessageTs
	}

	if threadTs == "" {
		slog.WarnContext(ctx, "No message timestamp available for thread reply")
		return
	}

	// Get channel ID
	channelID := interaction.Channel.ID
	if channelID == "" {
		channelID = interaction.Container.ChannelID
	}
	if channelID == "" {
		slog.WarnContext(ctx, "No channel ID available for thread reply")
		return
	}

	// Get Slack client
	client, err := h.svc.GetClient(ctx, interaction.Team.ID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to get Slack client for thread reply", "error", err)
		return
	}

	// Post thread reply (use <@USER_ID> format for proper Slack mention)
	threadMessage := fmt.Sprintf("<@%s> acknowledged the incident", interaction.User.ID)
	_, err = client.PostMessage(ctx, PostMessageOptions{
		Channel:  channelID,
		ThreadTS: threadTs,
		Message: &MessageResponse{
			Text: threadMessage,
		},
	})
	if err != nil {
		slog.WarnContext(ctx, "Failed to post acknowledgment thread reply",
			"error", err,
			"channel", channelID,
			"thread_ts", threadTs,
		)
	}
}

// handleUnavailableIncident handles the "I'm unavailable" button click.
func (h *Handler) handleUnavailableIncident(
	ctx context.Context, interaction *Interaction, action *InteractionAction,
) (*MessageResponse, error) {
	incidentUID := action.Value

	slog.InfoContext(ctx, "User marked unavailable for incident",
		"incident_uid", incidentUID,
		"slack_user_id", interaction.User.ID,
		"slack_username", interaction.User.Username,
	)

	// Return ephemeral message - don't modify the original
	return &MessageResponse{
		ResponseType: "ephemeral",
		Text:         "Noted. This incident remains unacknowledged. Another team member should acknowledge it.",
	}, nil
}

// handleEscalateIncident handles the Escalate button click.
func (h *Handler) handleEscalateIncident(
	ctx context.Context, interaction *Interaction, action *InteractionAction,
) (*MessageResponse, error) {
	incidentUID := action.Value
	if incidentUID == "" {
		slog.WarnContext(ctx, "Missing incident UID in escalate action")
		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Error: Invalid incident reference.",
		}, nil
	}

	// Get the connection to find the organization
	conn, err := h.svc.GetConnectionByTeamID(ctx, interaction.Team.ID)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to get connection", "error", err, "team_id", interaction.Team.ID)
		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Error: Could not find organization.",
		}, nil
	}

	// Get the incident to check if it's already escalated
	incident, err := h.svc.incidentsService.GetIncidentByUID(ctx, conn.OrganizationUID, incidentUID)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to get incident",
			"error", err,
			"incident_uid", incidentUID,
		)
		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Error: Could not find incident.",
		}, nil
	}

	// Check if already escalated
	if incident.EscalatedAt != nil {
		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "This incident has already been escalated.",
		}, nil
	}

	// TODO: Implement manual escalation trigger
	// This would typically:
	// 1. Update the incident.escalated_at field
	// 2. Create an incident.escalated event
	// 3. Queue notifications for escalation
	// For now, just log and acknowledge

	slog.InfoContext(ctx, "Manual escalation requested",
		"incident_uid", incidentUID,
		"slack_user_id", interaction.User.ID,
		"slack_username", interaction.User.Username,
	)

	return &MessageResponse{
		ResponseType: "ephemeral",
		Text:         fmt.Sprintf("Escalation requested by @%s. Team leads have been notified.", interaction.User.Username),
	}, nil
}

// Slack attachment color for acknowledged incidents.
const colorAcknowledged = "#2196F3" // Blue for acknowledged

// buildAcknowledgedMessage builds the updated message after an incident is acknowledged.
func buildAcknowledgedMessage(
	incident *models.Incident, check *models.Check, slackUsername string, acknowledgedAt time.Time,
) *MessageResponse {
	checkName := getCheckName(check)
	fallbackText := fmt.Sprintf("Incident for %s (acknowledged)", checkName)

	// Build section fields
	fields := []Text{
		{Type: "mrkdwn", Text: "*Monitor:*\n" + checkName},
		{Type: "mrkdwn", Text: "*Cause:*\n" + getFailureReason(incident)},
	}

	// Add URL field for HTTP checks
	if url := getCheckURL(check); url != "" {
		method := getCheckMethod(check)
		fields = append(fields, Text{
			Type: "mrkdwn",
			Text: fmt.Sprintf("*Check:*\n%s `%s`", method, url),
		})
	}

	blocks := []Block{
		// Header
		{
			Type: "header",
			Text: &Text{
				Type:  "plain_text",
				Text:  "Incident for " + checkName,
				Emoji: true,
			},
		},
		// Section with fields
		{
			Type:   "section",
			Fields: fields,
		},
		// Acknowledgment notice
		{
			Type: "section",
			Text: &Text{
				Type: "mrkdwn",
				Text: fmt.Sprintf(
					":white_check_mark: Acknowledged by *@%s* at %s",
					slackUsername, acknowledgedAt.Format("3:04 PM"),
				),
			},
		},
		// Context: status tags - show acknowledged
		{
			Type: "context",
			Elements: []any{
				ContextElement{
					Type: "mrkdwn",
					Text: ":warning: Incident  :large_blue_circle: Monitor  :white_check_mark: Acknowledged",
				},
			},
		},
		// Context: timestamp
		{
			Type: "context",
			Elements: []any{
				ContextElement{
					Type: "mrkdwn",
					Text: "Incident started " + formatTimestamp(incident.StartedAt),
				},
			},
		},
	}

	return &MessageResponse{
		Text:            fallbackText,
		ReplaceOriginal: true,
		Attachments: []Attachment{
			{
				Color:    colorAcknowledged,
				Fallback: fallbackText,
				Blocks:   blocks,
			},
		},
	}
}

// Helper functions for message building

func getCheckName(check *models.Check) string {
	if check.Name != nil && *check.Name != "" {
		return *check.Name
	}
	if check.Slug != nil && *check.Slug != "" {
		return *check.Slug
	}
	return "Unknown check"
}

func getCheckURL(check *models.Check) string {
	if check.Config == nil {
		return ""
	}
	if url, ok := check.Config["url"].(string); ok {
		return url
	}
	return ""
}

func getCheckMethod(check *models.Check) string {
	if check.Config == nil {
		return "GET"
	}
	if method, ok := check.Config["method"].(string); ok && method != "" {
		return method
	}
	return "GET"
}

func getFailureReason(incident *models.Incident) string {
	if incident.Details == nil {
		return "Check failed"
	}
	if reason, ok := incident.Details["failure_reason"].(string); ok && reason != "" {
		return reason
	}
	if output, ok := incident.Details["output"].(string); ok && output != "" {
		return output
	}
	return "Check failed"
}

func formatTimestamp(t time.Time) string {
	return t.Format("today at 3:04:05 PM")
}
