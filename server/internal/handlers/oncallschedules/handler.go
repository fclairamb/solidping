package oncallschedules

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// Handler exposes the on-call schedule REST API.
type Handler struct {
	base.HandlerBase
	svc   *Service
	dbSvc db.Service
}

// NewHandler builds the handler.
func NewHandler(svc *Service, dbSvc db.Service, cfg *config.Config) *Handler {
	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		svc:         svc,
		dbSvc:       dbSvc,
	}
}

// jsonDataKey is the wrapper key used for list responses, per the project
// convention "never return an array directly".
const jsonDataKey = "data"

// scheduleResponse is the JSON shape returned to the client.
type scheduleResponse struct {
	UID             string    `json:"uid"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Description     *string   `json:"description,omitempty"`
	Timezone        string    `json:"timezone"`
	RotationType    string    `json:"rotationType"`
	HandoffTime     string    `json:"handoffTime"`
	HandoffWeekday  *int      `json:"handoffWeekday,omitempty"`
	StartAt         time.Time `json:"startAt"`
	ICalEnabled     bool      `json:"icalEnabled"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	UserUIDs        []string  `json:"userUids,omitempty"`
	CurrentlyOnCall *userRef  `json:"currentlyOnCall,omitempty"`
}

type userRef struct {
	UID   string `json:"uid"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func toUserRef(user *models.User) *userRef {
	if user == nil {
		return nil
	}

	return &userRef{UID: user.UID, Name: user.Name, Email: user.Email}
}

func (h *Handler) toScheduleResponse(
	schedule *models.OnCallSchedule, userUIDs []string, currentlyOnCall *models.User,
) *scheduleResponse {
	return &scheduleResponse{
		UID:             schedule.UID,
		Slug:            schedule.Slug,
		Name:            schedule.Name,
		Description:     schedule.Description,
		Timezone:        schedule.Timezone,
		RotationType:    string(schedule.RotationType),
		HandoffTime:     schedule.HandoffTime,
		HandoffWeekday:  schedule.HandoffWeekday,
		StartAt:         schedule.StartAt,
		ICalEnabled:     schedule.ICalSecret != nil,
		CreatedAt:       schedule.CreatedAt,
		UpdatedAt:       schedule.UpdatedAt,
		UserUIDs:        userUIDs,
		CurrentlyOnCall: toUserRef(currentlyOnCall),
	}
}

func (h *Handler) handleError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrScheduleNotFound):
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "On-call schedule not found")
	case errors.Is(err, ErrInvalidTimezone),
		errors.Is(err, ErrInvalidHandoffTime),
		errors.Is(err, ErrWeekdayRequired),
		errors.Is(err, ErrWeekdayUnused),
		errors.Is(err, ErrInvalidWeekday),
		errors.Is(err, ErrInvalidRotationType),
		errors.Is(err, ErrOverrideEndBeforeStart):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, err.Error())
	case errors.Is(err, ErrScheduleHasNoUsers),
		errors.Is(err, ErrScheduleNotYetActive):
		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeValidationError, err.Error())
	default:
		return h.WriteInternalError(writer, err)
	}
}

func (h *Handler) currentUserUID(req bunrouter.Request) string {
	if user, ok := middleware.GetUserFromContext(req.Context()); ok && user != nil {
		return user.UID
	}

	return ""
}

func (h *Handler) currentlyOnCall(req bunrouter.Request, scheduleUID string) *models.User {
	user, err := h.svc.Resolve(req.Context(), scheduleUID, time.Now())
	if err != nil {
		return nil
	}

	return user
}

func (h *Handler) userUIDs(req bunrouter.Request, scheduleUID string) []string {
	roster, err := h.svc.ListUsers(req.Context(), scheduleUID)
	if err != nil {
		return nil
	}

	uids := make([]string, 0, len(roster))
	for _, entry := range roster {
		uids = append(uids, entry.UserUID)
	}

	return uids
}

// ListSchedules handles GET /api/v1/orgs/:org/on-call-schedules.
func (h *Handler) ListSchedules(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")

	schedules, err := h.svc.ListSchedules(req.Context(), orgUID)
	if err != nil {
		return h.handleError(writer, err)
	}

	out := make([]*scheduleResponse, 0, len(schedules))
	for _, schedule := range schedules {
		out = append(out, h.toScheduleResponse(
			schedule,
			h.userUIDs(req, schedule.UID),
			h.currentlyOnCall(req, schedule.UID),
		))
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{jsonDataKey: out})
}

// CreateScheduleBody mirrors the POST body documented in the spec.
type CreateScheduleBody struct {
	Slug           string    `json:"slug"`
	Name           string    `json:"name"`
	Description    string    `json:"description"`
	Timezone       string    `json:"timezone"`
	RotationType   string    `json:"rotationType"`
	HandoffTime    string    `json:"handoffTime"`
	HandoffWeekday *int      `json:"handoffWeekday"`
	StartAt        time.Time `json:"startAt"`
	UserUIDs       []string  `json:"userUids"`
}

// CreateSchedule handles POST /api/v1/orgs/:org/on-call-schedules.
func (h *Handler) CreateSchedule(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")

	var body CreateScheduleBody
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	schedule, err := h.svc.CreateSchedule(req.Context(), &CreateScheduleInput{
		OrganizationUID: orgUID,
		Slug:            body.Slug,
		Name:            body.Name,
		Description:     body.Description,
		Timezone:        body.Timezone,
		RotationType:    models.RotationType(body.RotationType),
		HandoffTime:     body.HandoffTime,
		HandoffWeekday:  body.HandoffWeekday,
		StartAt:         body.StartAt,
		UserUIDs:        body.UserUIDs,
	})
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusCreated, h.toScheduleResponse(
		schedule,
		body.UserUIDs,
		h.currentlyOnCall(req, schedule.UID),
	))
}

// GetSchedule handles GET /api/v1/orgs/:org/on-call-schedules/:slug.
func (h *Handler) GetSchedule(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	schedule, err := h.svc.GetScheduleBySlug(req.Context(), orgUID, slug)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, h.toScheduleResponse(
		schedule,
		h.userUIDs(req, schedule.UID),
		h.currentlyOnCall(req, schedule.UID),
	))
}

// UpdateScheduleBody is the partial-update payload.
type UpdateScheduleBody struct {
	Slug           *string    `json:"slug"`
	Name           *string    `json:"name"`
	Description    *string    `json:"description"`
	Timezone       *string    `json:"timezone"`
	RotationType   *string    `json:"rotationType"`
	HandoffTime    *string    `json:"handoffTime"`
	HandoffWeekday *int       `json:"handoffWeekday"`
	StartAt        *time.Time `json:"startAt"`
	UserUIDs       *[]string  `json:"userUids"`
}

// UpdateSchedule handles PATCH /api/v1/orgs/:org/on-call-schedules/:slug.
func (h *Handler) UpdateSchedule(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	schedule, err := h.svc.GetScheduleBySlug(req.Context(), orgUID, slug)
	if err != nil {
		return h.handleError(writer, err)
	}

	var body UpdateScheduleBody
	if decodeErr := json.NewDecoder(req.Body).Decode(&body); decodeErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	input := &UpdateScheduleInput{
		Slug:           body.Slug,
		Name:           body.Name,
		Description:    body.Description,
		Timezone:       body.Timezone,
		HandoffTime:    body.HandoffTime,
		HandoffWeekday: body.HandoffWeekday,
		StartAt:        body.StartAt,
		UserUIDs:       body.UserUIDs,
	}

	if body.RotationType != nil {
		rt := models.RotationType(*body.RotationType)
		input.RotationType = &rt
	}

	updated, err := h.svc.UpdateSchedule(req.Context(), orgUID, schedule.UID, input)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, h.toScheduleResponse(
		updated,
		h.userUIDs(req, updated.UID),
		h.currentlyOnCall(req, updated.UID),
	))
}

// DeleteSchedule handles DELETE /api/v1/orgs/:org/on-call-schedules/:slug.
func (h *Handler) DeleteSchedule(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	schedule, err := h.svc.GetScheduleBySlug(req.Context(), orgUID, slug)
	if err != nil {
		return h.handleError(writer, err)
	}

	if err := h.svc.DeleteSchedule(req.Context(), orgUID, schedule.UID); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// PreviewSchedule handles GET /api/v1/orgs/:org/on-call-schedules/:slug/preview.
func (h *Handler) PreviewSchedule(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	schedule, err := h.svc.GetScheduleBySlug(req.Context(), orgUID, slug)
	if err != nil {
		return h.handleError(writer, err)
	}

	from := time.Now()
	if v := req.URL.Query().Get("from"); v != "" {
		parsed, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid 'from' timestamp")
		}

		from = parsed
	}

	days := 14
	if v := req.URL.Query().Get("days"); v != "" {
		parsed, perr := strconv.Atoi(v)
		if perr != nil || parsed < 1 || parsed > 365 {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid 'days' parameter")
		}

		days = parsed
	}

	slots, err := h.svc.Preview(req.Context(), schedule.UID, from, time.Duration(days)*24*time.Hour)
	if err != nil {
		return h.handleError(writer, err)
	}

	type previewSlotJSON struct {
		UserUID string    `json:"userUid"`
		From    time.Time `json:"from"`
		To      time.Time `json:"to"`
	}

	out := make([]previewSlotJSON, 0, len(slots))
	for i := range slots {
		out = append(out, previewSlotJSON{
			UserUID: slots[i].UserUID,
			From:    slots[i].From,
			To:      slots[i].To,
		})
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{jsonDataKey: out})
}

// ListOverrides handles GET /api/v1/orgs/:org/on-call-schedules/:slug/overrides.
func (h *Handler) ListOverrides(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	schedule, err := h.svc.GetScheduleBySlug(req.Context(), orgUID, slug)
	if err != nil {
		return h.handleError(writer, err)
	}

	var from, until *time.Time

	if v := req.URL.Query().Get("from"); v != "" {
		parsed, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid 'from'")
		}

		from = &parsed
	}

	if v := req.URL.Query().Get("until"); v != "" {
		parsed, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid 'until'")
		}

		until = &parsed
	}

	overrides, err := h.svc.ListOverrides(req.Context(), schedule.UID, from, until)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{jsonDataKey: overrides})
}

// CreateOverrideBody is the POST body for an override.
type CreateOverrideBody struct {
	UserUID string    `json:"userUid"`
	StartAt time.Time `json:"startAt"`
	EndAt   time.Time `json:"endAt"`
	Reason  string    `json:"reason"`
}

// CreateOverride handles POST /api/v1/orgs/:org/on-call-schedules/:slug/overrides.
func (h *Handler) CreateOverride(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	schedule, err := h.svc.GetScheduleBySlug(req.Context(), orgUID, slug)
	if err != nil {
		return h.handleError(writer, err)
	}

	var body CreateOverrideBody
	if decodeErr := json.NewDecoder(req.Body).Decode(&body); decodeErr != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	override, createErr := h.svc.CreateOverride(req.Context(), &CreateOverrideInput{
		ScheduleUID:  schedule.UID,
		UserUID:      body.UserUID,
		StartAt:      body.StartAt,
		EndAt:        body.EndAt,
		Reason:       body.Reason,
		CreatedByUID: h.currentUserUID(req),
	})
	if createErr != nil {
		return h.handleError(writer, createErr)
	}

	return h.WriteJSON(writer, http.StatusCreated, override)
}

// DeleteOverride handles DELETE /api/v1/orgs/:org/on-call-schedules/:slug/overrides/:overrideUid.
func (h *Handler) DeleteOverride(writer http.ResponseWriter, req bunrouter.Request) error {
	overrideUID := req.Param("overrideUid")

	if err := h.svc.DeleteOverride(req.Context(), overrideUID); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// EnableICalFeed handles POST /api/v1/orgs/:org/on-call-schedules/:slug/ical-feed/enable.
func (h *Handler) EnableICalFeed(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	schedule, err := h.svc.GetScheduleBySlug(req.Context(), orgUID, slug)
	if err != nil {
		return h.handleError(writer, err)
	}

	secret, err := h.svc.EnableICalFeed(req.Context(), orgUID, schedule.UID)
	if err != nil {
		return h.handleError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]any{
		"secret": secret,
		"url":    "/api/v1/on-call-schedules/" + secret + "/feed.ics",
	})
}

// DisableICalFeed handles POST /api/v1/orgs/:org/on-call-schedules/:slug/ical-feed/disable.
func (h *Handler) DisableICalFeed(writer http.ResponseWriter, req bunrouter.Request) error {
	orgUID := req.Param("org")
	slug := req.Param("slug")

	schedule, err := h.svc.GetScheduleBySlug(req.Context(), orgUID, slug)
	if err != nil {
		return h.handleError(writer, err)
	}

	if err := h.svc.DisableICalFeed(req.Context(), orgUID, schedule.UID); err != nil {
		return h.handleError(writer, err)
	}

	writer.WriteHeader(http.StatusNoContent)

	return nil
}
