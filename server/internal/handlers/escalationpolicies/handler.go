package escalationpolicies

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
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

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrPolicyNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Escalation policy not found")
	case errors.Is(err, ErrInvalidTargetType),
		errors.Is(err, ErrTargetUIDRequired),
		errors.Is(err, ErrTargetUIDForbidden),
		errors.Is(err, ErrRepeatRequiresAfter),
		errors.Is(err, ErrRepeatMaxNegative),
		errors.Is(err, ErrDelayNegative):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	default:
		return h.WriteInternalError(writer, err)
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
	DelayMinutes int          `json:"delayMinutes"`
	Targets      []targetJSON `json:"targets"`
}

type policyJSON struct {
	UID                string     `json:"uid"`
	Slug               string     `json:"slug"`
	Name               string     `json:"name"`
	Description        *string    `json:"description,omitempty"`
	RepeatMax          int        `json:"repeatMax"`
	RepeatAfterMinutes *int       `json:"repeatAfterMinutes,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	Steps              []stepJSON `json:"steps,omitempty"`
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
			DelayMinutes: step.Step.DelayMinutes,
			Targets:      targets,
		})
	}

	return policyJSON{
		UID:                policy.UID,
		Slug:               policy.Slug,
		Name:               policy.Name,
		Description:        policy.Description,
		RepeatMax:          policy.RepeatMax,
		RepeatAfterMinutes: policy.RepeatAfterMinutes,
		CreatedAt:          policy.CreatedAt,
		UpdatedAt:          policy.UpdatedAt,
		Steps:              steps,
	}
}

func toPolicyHeaderJSON(policy *models.EscalationPolicy) policyJSON {
	return toPolicyJSON(&PolicyDetail{Policy: policy})
}

// ListPolicies handles GET /api/v1/orgs/:org/escalation-policies.
func (h *Handler) ListPolicies(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")

	policies, err := h.svc.ListPolicies(req.Context(), orgUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	out := make([]policyJSON, 0, len(policies))
	for _, policy := range policies {
		out = append(out, toPolicyHeaderJSON(policy))
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{jsonDataKey: out})
}

// targetBody mirrors the request shape for one target.
type targetBody struct {
	Type string `json:"type"`
	UID  string `json:"uid,omitempty"`
}

// stepBody mirrors the request shape for one step.
type stepBody struct {
	DelayMinutes int          `json:"delayMinutes"`
	Targets      []targetBody `json:"targets"`
}

// CreatePolicyBody is the POST body.
type CreatePolicyBody struct {
	Slug               string     `json:"slug"`
	Name               string     `json:"name"`
	Description        string     `json:"description"`
	RepeatMax          int        `json:"repeatMax"`
	RepeatAfterMinutes *int       `json:"repeatAfterMinutes"`
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
			DelayMinutes: step.DelayMinutes,
			Targets:      targets,
		})
	}

	return out
}

// CreatePolicy handles POST /api/v1/orgs/:org/escalation-policies.
func (h *Handler) CreatePolicy(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")

	var body CreatePolicyBody
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	policy, err := h.svc.CreatePolicy(req.Context(), &CreatePolicyInput{
		OrganizationUID:    orgUID,
		Slug:               body.Slug,
		Name:               body.Name,
		Description:        body.Description,
		RepeatMax:          body.RepeatMax,
		RepeatAfterMinutes: body.RepeatAfterMinutes,
		Steps:              toStepInputs(body.Steps),
	})
	if err != nil {
		return h.handleError(writer, err)
	}

	detail, err := h.svc.GetPolicyBySlug(req.Context(), orgUID, policy.Slug)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, toPolicyJSON(detail))
}

// GetPolicy handles GET /api/v1/orgs/:org/escalation-policies/:slug.
func (h *Handler) GetPolicy(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	detail, err := h.svc.GetPolicyBySlug(req.Context(), orgUID, slug)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, toPolicyJSON(detail))
}

// UpdatePolicyBody is the PATCH body.
type UpdatePolicyBody struct {
	Slug               *string     `json:"slug"`
	Name               *string     `json:"name"`
	Description        *string     `json:"description"`
	RepeatMax          *int        `json:"repeatMax"`
	RepeatAfterMinutes *int        `json:"repeatAfterMinutes"`
	Steps              *[]stepBody `json:"steps"`
}

// UpdatePolicy handles PATCH /api/v1/orgs/:org/escalation-policies/:slug.
func (h *Handler) UpdatePolicy(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	var body UpdatePolicyBody
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	input := &UpdatePolicyInput{
		Slug:               body.Slug,
		Name:               body.Name,
		Description:        body.Description,
		RepeatMax:          body.RepeatMax,
		RepeatAfterMinutes: body.RepeatAfterMinutes,
	}

	if body.Steps != nil {
		converted := toStepInputs(*body.Steps)
		input.Steps = &converted
	}

	detail, err := h.svc.UpdatePolicy(req.Context(), orgUID, slug, input)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, toPolicyJSON(detail))
}

// DeletePolicy handles DELETE /api/v1/orgs/:org/escalation-policies/:slug.
func (h *Handler) DeletePolicy(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	if err := h.svc.DeletePolicy(req.Context(), orgUID, slug); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}
