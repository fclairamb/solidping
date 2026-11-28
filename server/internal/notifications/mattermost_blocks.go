package notifications

import (
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Mattermost Blocks wire constants. The block/action vocabulary is part of
// Mattermost's API, so these literals are repeated on purpose; naming them
// keeps the sender readable and stops golangci's goconst threshold from
// counting Mattermost fields as accidental duplication.
const (
	mattermostBlockTypeText      = "text"
	mattermostBlockTypeContainer = "container"
	mattermostBlockTypeColumnSet = "column_set"
	mattermostBlockTypeColumn    = "column"
	mattermostBlockTypeDivider   = "divider"
	mattermostBlockTypeButton    = "button"

	mattermostActionTypeExternal = "external"
	mattermostActionTypeOpenURL  = "openURL"
	mattermostContextToken       = "token"
	mattermostContextAction      = "action"

	mattermostPriorityUrgent    = "urgent"
	mattermostPriorityImportant = "important"

	mattermostAccentDanger  = "danger"
	mattermostAccentGood    = "good"
	mattermostAccentWarning = "warning"
	mattermostAccentPrimary = "primary"
	mattermostAccentDefault = "default"

	mattermostStylePrimary  = "primary"
	mattermostTextSizeSmall = "small"

	mattermostButtonAcknowledge = "Acknowledge"
	mattermostButtonOpen        = "Open incident"
)

// Mattermost Blocks wire types. The API is intentionally loose (a block is a
// JSON object whose fields depend on its type), so one flat struct with
// omitempty fields keeps the encoder simple and the payload readable in tests.
//
//nolint:tagliatelle // field names are Mattermost's snake_case wire format
type (
	// MattermostPriority is the incoming webhook's top-level priority object.
	MattermostPriority struct {
		Priority string `json:"priority"`
	}

	// MattermostAction is one entry of props.mm_blocks_actions.
	MattermostAction struct {
		Type    string            `json:"type"`
		URL     string            `json:"url,omitempty"`
		Context map[string]string `json:"context,omitempty"`
	}

	// MattermostColumn is a column inside a column_set.
	MattermostColumn struct {
		Type  string            `json:"type"`
		Width string            `json:"width,omitempty"`
		Gap   string            `json:"gap,omitempty"`
		Items []MattermostBlock `json:"items"`
	}

	// MattermostBlock is one node of props.mm_blocks. Fields that do not apply
	// to the block's Type are omitted by the encoder.
	MattermostBlock struct {
		Type        string             `json:"type"`
		Text        string             `json:"text,omitempty"`
		IsSubtle    bool               `json:"is_subtle,omitempty"`
		Size        string             `json:"size,omitempty"`
		ActionID    string             `json:"action_id,omitempty"`
		Style       string             `json:"style,omitempty"`
		Tooltip     string             `json:"tooltip,omitempty"`
		Disabled    bool               `json:"disabled,omitempty"`
		Content     []MattermostBlock  `json:"content,omitempty"`
		Header      []MattermostBlock  `json:"header,omitempty"`
		Collapsed   bool               `json:"collapsed,omitempty"`
		Border      bool               `json:"border,omitempty"`
		AccentColor string             `json:"accent_color,omitempty"`
		Background  string             `json:"background,omitempty"`
		Flow        string             `json:"flow,omitempty"`
		Gap         string             `json:"gap,omitempty"`
		MaxHeight   string             `json:"max_height,omitempty"`
		Columns     []MattermostColumn `json:"columns,omitempty"`
		Width       string             `json:"width,omitempty"`
		Items       []MattermostBlock  `json:"items,omitempty"`
	}

	// MattermostProps is the post props bag carrying the block tree and its
	// action registry.
	MattermostProps struct {
		Blocks  []MattermostBlock           `json:"mm_blocks"`
		Actions map[string]MattermostAction `json:"mm_blocks_actions,omitempty"`
	}

	// MattermostBlocksPost is the incoming-webhook body for the blocks format.
	MattermostBlocksPost struct {
		Channel  string              `json:"channel,omitempty"`
		Username string              `json:"username,omitempty"`
		IconURL  string              `json:"icon_url,omitempty"` //nolint:tagliatelle // Mattermost API format
		Props    MattermostProps     `json:"props"`
		Priority *MattermostPriority `json:"priority,omitempty"`
	}

	// MattermostBlocksRenderOptions carries the per-post values the sender and
	// the action callback need but the event payload does not: display
	// overrides and the signed action tokens.
	MattermostBlocksRenderOptions struct {
		Channel       string
		Username      string
		IconURL       string
		ActionURL     string
		AckToken      string
		EnableActions bool
	}
)

// MattermostActionURL is the public callback endpoint embedded in every
// interactive block. Empty base URL means "no actions": the card still renders,
// it just cannot call back.
func MattermostActionURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}

	return baseURL + "/api/v1/integrations/mattermost/actions"
}

// BuildMattermostBlocksPost renders the card for one payload. It is exported
// because the inbound action handler re-renders the post in place after an
// acknowledgment.
func BuildMattermostBlocksPost(
	payload *Payload, opts *MattermostBlocksRenderOptions,
) *MattermostBlocksPost {
	checkName := getCheckName(payload.Check)
	color, title := (&MattermostSender{}).eventColorAndTitle(payload, checkName)
	linkedTitle := mattermostTitleWithCheckLink(payload, title, checkName)
	fields := (&MattermostSender{}).buildFields(payload, checkName)

	blocks, actions := buildMattermostBlocks(payload, linkedTitle, fields, color, opts)

	return &MattermostBlocksPost{
		Channel:  opts.Channel,
		Username: opts.Username,
		IconURL:  opts.IconURL,
		Props: MattermostProps{
			Blocks:  blocks,
			Actions: actions,
		},
		Priority: mattermostPriorityFor(payload),
	}
}

// mattermostTitleWithCheckLink replaces the trailing check name with a link to
// the check page. The title builders in eventColorAndTitle end with the check
// name, so this stays correct for every event without a second title switch.
func mattermostTitleWithCheckLink(payload *Payload, title, checkName string) string {
	if checkName == "" || !strings.HasSuffix(title, checkName) {
		return "**" + title + "**"
	}

	// The prefix is SolidPing's own tag and emoji shortcode (":red_circle:
	// [DOWN] "), kept as is; only the check name is a user value.
	prefix := strings.TrimSuffix(title, checkName)
	checkURL := checkDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Check)

	return "**" + prefix + mattermostLink(checkURL, checkName) + "**"
}

// mattermostLink is Mattermost's markdown link form. Slack's `<url|text>` is
// deliberately not reused: it renders literally in Mattermost. The text is
// raw and escaped here, so a check name cannot close the link and add its own;
// the URL is one SolidPing built.
func mattermostLink(url, text string) string {
	text = escapeMattermostMarkdown(text)
	if url == "" {
		return text
	}

	return "[" + text + "](" + url + ")"
}

// buildMattermostBlocks wraps the event fields and actions in the card
// container. Long fields (cause, comment, reason) become their own text
// blocks; consecutive short fields become a column_set so Check/Type/Failures
// stay on one line.
func buildMattermostBlocks(
	payload *Payload, title string, fields []mattermostField, color string, opts *MattermostBlocksRenderOptions,
) ([]MattermostBlock, map[string]MattermostAction) {
	content := make([]MattermostBlock, 0, len(fields)+4)
	content = append(content, MattermostBlock{Type: mattermostBlockTypeText, Text: title})

	if fieldBlocks := mattermostFieldBlocks(fields); len(fieldBlocks) > 0 {
		content = append(content, MattermostBlock{Type: mattermostBlockTypeDivider})
		content = append(content, fieldBlocks...)
	}

	actionBlocks, actions := buildMattermostActions(payload, opts)
	content = append(content, actionBlocks...)
	content = append(content, MattermostBlock{
		Type: mattermostBlockTypeText, Text: mattermostFooter(payload), IsSubtle: true, Size: mattermostTextSizeSmall,
	})

	return []MattermostBlock{{
		Type:        "container",
		Border:      true,
		AccentColor: mattermostAccent(color),
		Gap:         "small",
		Content:     content,
	}}, actions
}

// mattermostFieldBlocks renders the existing event fields without changing
// their order. Grouping is per run of short fields, so a long Cause between
// two metric pairs still reads in the same order as the attachments format.
func mattermostFieldBlocks(fields []mattermostField) []MattermostBlock {
	blocks := make([]MattermostBlock, 0, len(fields))
	short := make([]mattermostField, 0, 3)

	flush := func() {
		if len(short) == 0 {
			return
		}

		columns := make([]MattermostColumn, 0, len(short))
		for i := range short {
			columns = append(columns, MattermostColumn{
				Type:  mattermostBlockTypeColumn,
				Width: "stretch",
				Items: []MattermostBlock{{
					Type: mattermostBlockTypeText, Text: "**" + short[i].Title + "**\n" + escapeMattermostMarkdown(short[i].Value),
				}},
			})
		}

		blocks = append(blocks, MattermostBlock{Type: mattermostBlockTypeColumnSet, Gap: "medium", Columns: columns})
		short = short[:0]
	}

	for i := range fields {
		if fields[i].Short && len(short) < 3 {
			short = append(short, fields[i])

			continue
		}

		flush()

		if fields[i].Short {
			short = append(short, fields[i])

			continue
		}

		blocks = append(blocks, MattermostBlock{
			Type: mattermostBlockTypeText, Text: "**" + fields[i].Title + "**\n" + escapeMattermostMarkdown(fields[i].Value),
		})
	}

	flush()

	return blocks
}

// buildMattermostActions adds only the controls that are valid for this event
// and whose signed token exists. The registry is built in the same pass, so it
// can never contain an unused entry — Mattermost rejects those.
func buildMattermostActions(
	payload *Payload, opts *MattermostBlocksRenderOptions,
) ([]MattermostBlock, map[string]MattermostAction) {
	if !opts.EnableActions || payload.Incident == nil {
		return nil, nil
	}

	buttons := make([]MattermostBlock, 0, 2)
	actions := make(map[string]MattermostAction, 3)

	if canAcknowledge(payload) && opts.ActionURL != "" && opts.AckToken != "" {
		buttons = append(buttons, MattermostBlock{
			Type: mattermostBlockTypeButton, Text: mattermostButtonAcknowledge,
			Style: mattermostStylePrimary, ActionID: mattermostActionIDAck,
		})
		actions[mattermostActionIDAck] = MattermostAction{
			Type: mattermostActionTypeExternal, URL: opts.ActionURL,
			Context: map[string]string{
				mattermostContextToken:  opts.AckToken,
				mattermostContextAction: MattermostActionAcknowledge,
			},
		}
	}

	if openURL := incidentDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident); openURL != "" {
		buttons = append(buttons, MattermostBlock{
			Type: mattermostBlockTypeButton, Text: mattermostButtonOpen, ActionID: mattermostActionIDOpen,
		})
		actions[mattermostActionIDOpen] = MattermostAction{Type: mattermostActionTypeOpenURL, URL: openURL}
	}

	blocks := make([]MattermostBlock, 0, 2)
	if len(buttons) > 0 {
		blocks = append(blocks, MattermostBlock{
			Type: mattermostBlockTypeContainer, Flow: "horizontal", Gap: "small", Content: buttons,
		})
	}

	if len(actions) == 0 {
		return nil, nil
	}

	return blocks, actions
}

// canAcknowledge mirrors the Slack/Discord eligibility rule: an open,
// unacknowledged incident can be taken from the alert. Comment and resolved
// notices deliberately do not offer it.
func canAcknowledge(payload *Payload) bool {
	if payload == nil || payload.Incident == nil {
		return false
	}

	if payload.Incident.State == models.IncidentStateResolved || payload.Incident.AcknowledgedAt != nil {
		return false
	}

	switch payload.EventType {
	case eventTypeIncidentCreated, eventTypeIncidentEscalated, eventTypeIncidentReopened,
		eventTypeIncidentUnacknowledged, eventTypeIncidentReminder:
		return true
	default:
		return false
	}
}

// mattermostPriorityFor maps the event to Mattermost's priority. Only events
// that ask a human to act are raised; a recovery or a comment must not buzz a
// phone the way a new outage does. requested_ack stays unset: Mattermost's own
// checkmark is a different concept from a SolidPing incident ack.
func mattermostPriorityFor(payload *Payload) *MattermostPriority {
	if payload == nil {
		return nil
	}

	switch payload.EventType {
	case eventTypeIncidentCreated:
		if isDegradedIncident(payload.Incident) {
			return &MattermostPriority{Priority: mattermostPriorityImportant}
		}

		return &MattermostPriority{Priority: mattermostPriorityUrgent}
	case eventTypeIncidentEscalated, eventTypeIncidentReopened, eventTypeIncidentReminder:
		return &MattermostPriority{Priority: mattermostPriorityUrgent}
	default:
		return nil
	}
}

// mattermostFooter replaces the attachment footer/timestamp pair with one
// subtle line. StartedAt is the incident's real start, not the event time, so
// an acknowledgment card still says when the outage began.
func mattermostFooter(payload *Payload) string {
	if payload.Incident != nil && !payload.Incident.StartedAt.IsZero() {
		return fmt.Sprintf("%s · started %s", productName, mattermostTime(payload.Incident.StartedAt))
	}

	return productName
}

// mattermostTime is the compact timestamp chat readers expect. The time keeps
// its zone rather than being silently converted.
func mattermostTime(when time.Time) string {
	return when.Format("Jan 2 15:04 MST")
}

// mattermostAccent maps the attachment color to the theme-aware container
// accent. Hex colors would work too, but semantic names follow the reader's
// light/dark theme.
func mattermostAccent(color string) string {
	switch color {
	case mattermostColorRed:
		return mattermostAccentDanger
	case mattermostColorGreen:
		return mattermostAccentGood
	case mattermostColorOrange, mattermostColorYellow:
		return mattermostAccentWarning
	case mattermostColorBlue:
		return mattermostAccentPrimary
	default:
		return mattermostAccentDefault
	}
}
