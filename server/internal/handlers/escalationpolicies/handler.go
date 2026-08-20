package escalationpolicies

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// jsonDataKey wraps list responses per the project convention.
const jsonDataKey = "data"

// Handler exposes the escalation-policy REST API.
type Handler struct {
	base.HandlerBase
	svc *Service
}

// NewHandler builds a handler.
func NewHandler(svc *Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
	}
}

func (h *Handler) handleError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrOrgNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeOrganizationNotFound, "Organization not found")
	case errors.Is(err, ErrPolicyNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Escalation policy not found")
	case errors.Is(err, ErrPolicyInUse):
		return h.WriteError(writer, http.StatusConflict, "ESCALATION_POLICY_IN_USE",
			"Escalation policy is referenced by an open incident — resolve the incident first")
	case errors.Is(err, ErrInvalidTargetType),
		errors.Is(err, ErrTargetUIDRequired),
		errors.Is(err, ErrTargetUIDForbidden),
		errors.Is(err, ErrRepeatRequiresAfter),
		errors.Is(err, ErrRepeatMaxNegative),
		errors.Is(err, ErrDelayNegative),
		errors.Is(err, ErrDelayTooLarge):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

type targetJSON struct {
	UID        string `json:"uid"`
	TargetType string `json:"type"`
	TargetUID  string `json:"targetUid,omitempty"`
	Position   int    `json:"position"`
}

type stepJSON struct {
	UID          string       `json:"uid"`
	Position     int          `json:"position"`
	DelaySeconds int          `json:"delaySeconds"`
	Targets      []targetJSON `json:"targets"`
}

type policyJSON struct {
	UID                string     `json:"uid"`
	Name               string     `json:"name"`
	Description        *string    `json:"description,omitempty"`
	RepeatMax          int        `json:"repeatMax"`
	RepeatAfterSeconds *int       `json:"repeatAfterSeconds,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	Steps              []stepJSON `json:"steps,omitempty"`
	// StepCount is always present so a zero-step ("silent") policy is legible
	// even on the light list response (which omits the expanded steps).
	StepCount int `json:"stepCount"`
	// UsageCheckCount / UsageGroupCount count the checks and groups that
	// directly reference this policy. Present only on the list response (they
	// drive the delete-guard confirmation); nil/omitted on the detail response.
	UsageCheckCount *int `json:"usageCheckCount,omitempty"`
	UsageGroupCount *int `json:"usageGroupCount,omitempty"`
}

func toPolicyJSON(detail *PolicyDetail) policyJSON {
	policy := detail.Policy

	steps := make([]stepJSON, 0, len(detail.Steps))
	for i := range detail.Steps {
		step := detail.Steps[i]
		targets := make([]targetJSON, 0, len(step.Targets))
		for j := range step.Targets {
			target := step.Targets[j]
			tgt := targetJSON{
				UID:        target.UID,
				TargetType: string(target.TargetType),
				Position:   target.Position,
			}

			if target.TargetUID != nil {
				tgt.TargetUID = *target.TargetUID
			}

			targets = append(targets, tgt)
		}

		steps = append(steps, stepJSON{
			UID:          step.Step.UID,
			Position:     step.Step.Position,
			DelaySeconds: step.Step.DelaySeconds,
			Targets:      targets,
		})
	}

	return policyJSON{
		UID:                policy.UID,
		Name:               policy.Name,
		Description:        policy.Description,
		RepeatMax:          policy.RepeatMax,
		RepeatAfterSeconds: policy.RepeatAfterSeconds,
		CreatedAt:          policy.CreatedAt,
		UpdatedAt:          policy.UpdatedAt,
		Steps:              steps,
		StepCount:          len(steps),
	}
}

func toPolicyHeaderJSON(policy *models.EscalationPolicy) policyJSON {
	return toPolicyJSON(&PolicyDetail{Policy: policy})
}

// ListPolicies handles GET /api/v1/orgs/:org/escalation-policies.
func (h *Handler) ListPolicies(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	items, err := h.svc.ListPoliciesWithCounts(req.Context(), orgUID)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	out := make([]policyJSON, 0, len(items))
	for _, item := range items {
		row := toPolicyHeaderJSON(item.Policy)
		row.StepCount = item.StepCount
		checkCount := item.UsageCheckCount
		groupCount := item.UsageGroupCount
		row.UsageCheckCount = &checkCount
		row.UsageGroupCount = &groupCount
		out = append(out, row)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{jsonDataKey: out})
}

// targetBody mirrors the request shape for one target.
type targetBody struct {
	Type string `json:"type"`
	UID  string `json:"targetUid,omitempty"`
}

// stepBody mirrors the request shape for one step.
type stepBody struct {
	DelaySeconds int          `json:"delaySeconds"`
	Targets      []targetBody `json:"targets"`
}

// CreatePolicyBody is the POST body.
type CreatePolicyBody struct {
	Name               string     `json:"name"`
	Description        string     `json:"description"`
	RepeatMax          int        `json:"repeatMax"`
	RepeatAfterSeconds *int       `json:"repeatAfterSeconds"`
	Steps              []stepBody `json:"steps"`
}

func toStepInputs(steps []stepBody) []StepInput {
	out := make([]StepInput, 0, len(steps))
	for i := range steps {
		step := &steps[i]
		targets := make([]TargetInput, 0, len(step.Targets))
		for j := range step.Targets {
			target := &step.Targets[j]
			targets = append(targets, TargetInput{
				Type: models.EscalationTargetType(target.Type),
				UID:  target.UID,
			})
		}

		out = append(out, StepInput{
			DelaySeconds: step.DelaySeconds,
			Targets:      targets,
		})
	}

	return out
}

// CreatePolicy handles POST /api/v1/orgs/:org/escalation-policies.
func (h *Handler) CreatePolicy(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, req, err)
	}

	var body CreatePolicyBody
	if decodeErr := json.NewDecoder(req.Body).Decode(&body); decodeErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	policy, err := h.svc.CreatePolicy(req.Context(), &CreatePolicyInput{
		OrganizationUID:    orgUID,
		Name:               body.Name,
		Description:        body.Description,
		RepeatMax:          body.RepeatMax,
		RepeatAfterSeconds: body.RepeatAfterSeconds,
		Steps:              toStepInputs(body.Steps),
	})
	if err != nil {
		return h.handleError(writer, req, err)
	}

	detail, err := h.svc.GetPolicy(req.Context(), orgUID, policy.UID)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, toPolicyJSON(detail))
}

// GetPolicy handles GET /api/v1/orgs/:org/escalation-policies/:uid.
func (h *Handler) GetPolicy(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, req, err)
	}
	uid := httpx.Param(req, "uid")

	detail, err := h.svc.GetPolicy(req.Context(), orgUID, uid)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, toPolicyJSON(detail))
}

// UpdatePolicyBody is the PATCH body.
type UpdatePolicyBody struct {
	Name               *string     `json:"name"`
	Description        *string     `json:"description"`
	RepeatMax          *int        `json:"repeatMax"`
	RepeatAfterSeconds *int        `json:"repeatAfterSeconds"`
	Steps              *[]stepBody `json:"steps"`
}

// UpdatePolicy handles PATCH /api/v1/orgs/:org/escalation-policies/:uid.
func (h *Handler) UpdatePolicy(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, req, err)
	}
	uid := httpx.Param(req, "uid")

	var body UpdatePolicyBody
	if decodeErr := json.NewDecoder(req.Body).Decode(&body); decodeErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	input := &UpdatePolicyInput{
		Name:               body.Name,
		Description:        body.Description,
		RepeatMax:          body.RepeatMax,
		RepeatAfterSeconds: body.RepeatAfterSeconds,
	}

	if body.Steps != nil {
		converted := toStepInputs(*body.Steps)
		input.Steps = &converted
	}

	detail, err := h.svc.UpdatePolicy(req.Context(), orgUID, uid, input)
	if err != nil {
		return h.handleError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, toPolicyJSON(detail))
}

// DeletePolicy handles DELETE /api/v1/orgs/:org/escalation-policies/:uid.
func (h *Handler) DeletePolicy(writer http.ResponseWriter, req *http.Request) error {
	orgUID, err := h.svc.ResolveOrgUID(req.Context(), httpx.Param(req, "org"))
	if err != nil {
		return h.handleError(writer, req, err)
	}
	uid := httpx.Param(req, "uid")

	if err := h.svc.DeletePolicy(req.Context(), orgUID, uid); err != nil {
		return h.handleError(writer, req, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}
