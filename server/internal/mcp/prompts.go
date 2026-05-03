package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Prompt names exposed by the server.
const (
	promptTriageIncident     = "triage_incident"
	promptSummarizeOrgHealth = "summarize_org_health"
	promptDraftStatusUpdate  = "draft_status_update"

	promptArgIncidentUID = "incidentUid"
	promptArgTone        = "tone"

	toneTechnical    = "technical"
	toneNonTechnical = "non-technical"

	roleUser = "user"
)

// errPromptArgMissing is returned when a required prompt argument is absent.
var errPromptArgMissing = errors.New("required prompt argument missing")

// errUnknownPrompt is returned when prompts/get references a name that isn't
// registered.
var errUnknownPrompt = errors.New("unknown prompt")

// listPrompts returns the static set of prompts the server registers. Order is
// stable so tests can rely on indices.
func listPrompts() []PromptDefinition {
	return []PromptDefinition{
		{
			Name: promptTriageIncident,
			Description: "Review an incident and produce a triage summary. The model fetches " +
				"the incident, its event timeline, the affected check's recent results, and " +
				"returns a paragraph summarizing what's happening, the likely cause, and what " +
				"to check next.",
			Arguments: []PromptArgument{
				{
					Name: promptArgIncidentUID,
					Description: "Incident UID to triage. Use list_incidents to discover one " +
						"if you don't have it.",
					Required: true,
				},
			},
		},
		{
			Name: promptSummarizeOrgHealth,
			Description: "Produce a one-paragraph status summary of the organization's " +
				"monitoring posture: which checks are failing, which incidents are active, " +
				"overall posture.",
			Arguments: nil,
		},
		{
			Name: promptDraftStatusUpdate,
			Description: "Draft a customer-facing status page update for an incident. The " +
				"output is a draft for human review, not auto-published.",
			Arguments: []PromptArgument{
				{
					Name:        promptArgIncidentUID,
					Description: "Incident UID to summarize for the public update.",
					Required:    true,
				},
				{
					Name: promptArgTone,
					Description: "Audience tone. Allowed: \"technical\", \"non-technical\". " +
						"Default \"non-technical\".",
					Required: false,
				},
			},
		},
	}
}

// renderPrompt renders the named prompt with the given arguments. Returns
// (description, message text, error). Description matches the prompt's
// definition; message text is the conversation seed shown to the LLM.
func renderPrompt(name string, args map[string]string) (string, string, error) {
	switch name {
	case promptTriageIncident:
		return renderTriageIncident(args)
	case promptSummarizeOrgHealth:
		return renderSummarizeOrgHealth()
	case promptDraftStatusUpdate:
		return renderDraftStatusUpdate(args)
	default:
		return "", "", errUnknownPrompt
	}
}

func renderTriageIncident(args map[string]string) (string, string, error) {
	uid := args[promptArgIncidentUID]
	if uid == "" {
		return "", "", errPromptArgMissing
	}
	body := strings.ReplaceAll(triageIncidentTemplate, "{{incidentUid}}", uid)
	return "Triage incident " + uid, body, nil
}

func renderSummarizeOrgHealth() (string, string, error) {
	return "Organization health summary", summarizeOrgHealthTemplate, nil
}

func renderDraftStatusUpdate(args map[string]string) (string, string, error) {
	uid := args[promptArgIncidentUID]
	if uid == "" {
		return "", "", errPromptArgMissing
	}
	tone := args[promptArgTone]
	if tone == "" {
		tone = toneNonTechnical
	}
	tonePhrase := "Avoid jargon (no \"5xx\", no \"DNS SOA\")."
	if tone == toneTechnical {
		tonePhrase = "Technical detail is welcome."
	}
	body := strings.ReplaceAll(draftStatusUpdateTemplate, "{{incidentUid}}", uid)
	body = strings.ReplaceAll(body, "{{tone}}", tone)
	body = strings.ReplaceAll(body, "{{tonePhrase}}", tonePhrase)
	return "Draft status update for incident " + uid, body, nil
}

const triageIncidentTemplate = `Triage incident {{incidentUid}}.

Steps:
1. Call get_incident with uid="{{incidentUid}}" and with="check,events" to get
   the incident, the underlying check, and the timeline of events.
2. Call diagnose_check with identifier=<the check from step 1> to get recent
   results across regions.
3. Produce a triage summary in 3-5 sentences:
   - What is failing (check name, regions, error message).
   - When it started and how long it's been failing.
   - The most likely cause based on the error output.
   - One concrete next step (e.g. "check the upstream DNS", "verify the cert
     hasn't expired", "see if a deploy rolled out around the start time").
   Do not pad with caveats. If the data is ambiguous, say so in one phrase.`

const summarizeOrgHealthTemplate = `Summarize the current health of this organization's monitoring.

Steps:
1. Call list_incidents with state="active" to get all open incidents.
2. Call list_checks to get the full check inventory.
3. Produce a one-paragraph summary:
   - How many checks total, how many currently failing.
   - The active incidents (by check name and how long they've been open).
   - One sentence on overall posture ("everything green", "one minor incident",
     "multiple regions degraded").
   No bullet lists. One paragraph. Be direct.`

// handlePromptsList responds to the prompts/list MCP method.
func (h *Handler) handlePromptsList(req *Request) (*Response, int) {
	resp := successResponse(req.ID, PromptsListResult{Prompts: listPrompts()})
	return &resp, http.StatusOK
}

// handlePromptsGet responds to the prompts/get MCP method by rendering the
// requested prompt with its arguments.
func (h *Handler) handlePromptsGet(req *Request) (*Response, int) {
	var params PromptGetParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		resp := errorResponse(req.ID, CodeInvalidParams, "Invalid params")
		return &resp, http.StatusOK
	}

	description, body, err := renderPrompt(params.Name, params.Arguments)
	if err != nil {
		switch {
		case errors.Is(err, errUnknownPrompt):
			resp := errorResponse(req.ID, CodeMethodNotFound, "Unknown prompt: "+params.Name)
			return &resp, http.StatusOK
		case errors.Is(err, errPromptArgMissing):
			resp := errorResponse(req.ID, CodeInvalidParams, "Required argument missing for prompt "+params.Name)
			return &resp, http.StatusOK
		default:
			resp := errorResponse(req.ID, CodeInternalError, err.Error())
			return &resp, http.StatusOK
		}
	}

	resp := successResponse(req.ID, PromptGetResult{
		Description: description,
		Messages: []PromptMessage{
			{
				Role:    roleUser,
				Content: ContentBlock{Type: contentTypeText, Text: body},
			},
		},
	})
	return &resp, http.StatusOK
}

const draftStatusUpdateTemplate = `Draft a status page update for incident {{incidentUid}}.

Tone: {{tone}}.

Steps:
1. Call get_incident with uid="{{incidentUid}}" and with="check,events" for
   the incident metadata and timeline.
2. Write a draft status update with:
   - Title: 2-6 words, customer-facing (e.g. "Investigating elevated API
     errors", "Resolved: brief outage on EU region").
   - Body: 2-4 sentences. State what's affected, what's known, what's being
     done. {{tonePhrase}}
   - Status: pick one — investigating | identified | monitoring | resolved.

Output the draft formatted as:

  TITLE: ...
  STATUS: ...
  BODY: ...

Do not publish — this is a draft for human review.`
