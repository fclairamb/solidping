package oncallschedules_test

// HTTP-level tests of the override endpoints: an override or a user is only
// reachable through the organization and the schedule it belongs to.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/oncallschedules"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// tenant is one organization with a schedule, a member and an override.
type tenant struct {
	org      *models.Organization
	user     *models.User
	schedule *models.OnCallSchedule
	override *models.OnCallScheduleOverride
}

type handlerSetup struct {
	svc    *oncallschedules.Service
	dbSvc  db.Service
	router *httpx.Router
	acme   *tenant
	other  *tenant
}

func newTenant(
	t *testing.T, svc *oncallschedules.Service, dbSvc db.Service, org *models.Organization, userName string,
) *tenant {
	t.Helper()

	r := require.New(t)
	user := newTestUser(t, dbSvc, org.UID, userName)

	schedule, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Name:            "Primary",
		Timezone:        "UTC",
		RotationType:    models.RotationTypeDaily,
		HandoffTime:     "09:00",
		StartAt:         time.Now().Add(-24 * time.Hour),
		UserUIDs:        []string{user.UID},
	})
	r.NoError(err)

	now := time.Now()
	override, err := svc.CreateOverride(t.Context(), &oncallschedules.CreateOverrideInput{
		OrganizationUID: org.UID,
		ScheduleUID:     schedule.UID,
		UserUID:         user.UID,
		StartAt:         now,
		EndAt:           now.Add(time.Hour),
	})
	r.NoError(err)

	return &tenant{org: org, user: user, schedule: schedule, override: override}
}

func newHandlerSetup(t *testing.T) *handlerSetup {
	t.Helper()

	svc, dbSvc, org := newTestService(t)

	otherOrg := models.NewOrganization("other", "")
	require.NoError(t, dbSvc.CreateOrganization(t.Context(), otherOrg))

	handler := oncallschedules.NewHandler(svc, dbSvc, &config.Config{})

	router := httpx.New()
	router.PATCH("/api/v1/orgs/:org/on-call-schedules/:uid", handler.UpdateSchedule)
	router.GET("/api/v1/orgs/:org/on-call-schedules/:uid/overrides", handler.ListOverrides)
	router.POST("/api/v1/orgs/:org/on-call-schedules/:uid/overrides", handler.CreateOverride)
	router.DELETE("/api/v1/orgs/:org/on-call-schedules/:uid/overrides/:overrideUid", handler.DeleteOverride)

	return &handlerSetup{
		svc:    svc,
		dbSvc:  dbSvc,
		router: router,
		acme:   newTenant(t, svc, dbSvc, org, "alice"),
		other:  newTenant(t, svc, dbSvc, otherOrg, "bob"),
	}
}

func (h *handlerSetup) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var bodyBytes []byte

	if body != nil {
		var err error

		bodyBytes, err = json.Marshal(body)
		require.NoError(t, err)
	}

	req := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewBuffer(bodyBytes))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)

	return rec
}

func overridePath(org *models.Organization, scheduleUID, overrideUID string) string {
	return "/api/v1/orgs/" + org.Slug + "/on-call-schedules/" + scheduleUID + "/overrides/" + overrideUID
}

// overrideExists reports whether the override is still on its schedule.
func (h *handlerSetup) overrideExists(t *testing.T, tn *tenant) bool {
	t.Helper()

	overrides, err := h.svc.ListOverrides(t.Context(), tn.schedule.UID, nil, nil)
	require.NoError(t, err)

	for _, override := range overrides {
		if override.UID == tn.override.UID {
			return true
		}
	}

	return false
}

func TestHandlerDeleteOverrideScopedToScheduleAndOrg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path func(h *handlerSetup) string
	}{
		{
			name: "through the other org and its own schedule",
			path: func(h *handlerSetup) string {
				return overridePath(h.other.org, h.other.schedule.UID, h.acme.override.UID)
			},
		},
		{
			name: "through the other org and the owning schedule",
			path: func(h *handlerSetup) string {
				return overridePath(h.other.org, h.acme.schedule.UID, h.acme.override.UID)
			},
		},
		{
			name: "through the owning org and a schedule of the other org",
			path: func(h *handlerSetup) string {
				return overridePath(h.acme.org, h.other.schedule.UID, h.acme.override.UID)
			},
		},
		{
			name: "through an unknown override uid",
			path: func(h *handlerSetup) string {
				return overridePath(h.acme.org, h.acme.schedule.UID, "00000000-0000-0000-0000-000000000000")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			h := newHandlerSetup(t)

			rec := h.do(t, http.MethodDelete, tc.path(h), nil)
			r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())
			r.True(h.overrideExists(t, h.acme), "the override must survive a foreign delete")
		})
	}
}

func TestHandlerDeleteOverrideByOwner(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	rec := h.do(t, http.MethodDelete,
		overridePath(h.acme.org, h.acme.schedule.UID, h.acme.override.UID), nil)
	r.Equal(http.StatusNoContent, rec.Code, rec.Body.String())
	r.False(h.overrideExists(t, h.acme), "the owner deletes its own override")
	r.True(h.overrideExists(t, h.other), "the other org's override is untouched")

	rec = h.do(t, http.MethodDelete,
		overridePath(h.acme.org, h.acme.schedule.UID, h.acme.override.UID), nil)
	r.Equal(http.StatusNotFound, rec.Code, "a second delete finds nothing")
}

func TestHandlerListOverridesOfForeignSchedule(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)

	rec := h.do(t, http.MethodGet,
		"/api/v1/orgs/"+h.other.org.Slug+"/on-call-schedules/"+h.acme.schedule.UID+"/overrides", nil)
	r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())

	rec = h.do(t, http.MethodGet,
		"/api/v1/orgs/"+h.acme.org.Slug+"/on-call-schedules/"+h.acme.schedule.UID+"/overrides", nil)
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
	r.Contains(rec.Body.String(), h.acme.override.UID)
}

func TestHandlerCreateOverrideScoped(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name     string
		path     func(h *handlerSetup) string
		userUID  func(h *handlerSetup) string
		wantCode int
	}{
		{
			name: "on a schedule of another org",
			path: func(h *handlerSetup) string {
				return "/api/v1/orgs/" + h.other.org.Slug + "/on-call-schedules/" + h.acme.schedule.UID + "/overrides"
			},
			userUID:  func(h *handlerSetup) string { return h.other.user.UID },
			wantCode: http.StatusNotFound,
		},
		{
			name: "for a user of another org",
			path: func(h *handlerSetup) string {
				return "/api/v1/orgs/" + h.acme.org.Slug + "/on-call-schedules/" + h.acme.schedule.UID + "/overrides"
			},
			userUID:  func(h *handlerSetup) string { return h.other.user.UID },
			wantCode: http.StatusBadRequest,
		},
		{
			name: "for a member on its own schedule",
			path: func(h *handlerSetup) string {
				return "/api/v1/orgs/" + h.acme.org.Slug + "/on-call-schedules/" + h.acme.schedule.UID + "/overrides"
			},
			userUID:  func(h *handlerSetup) string { return h.acme.user.UID },
			wantCode: http.StatusCreated,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			h := newHandlerSetup(t)

			rec := h.do(t, http.MethodPost, tc.path(h), map[string]any{
				"userUid": tc.userUID(h),
				"startAt": now.Add(2 * time.Hour),
				"endAt":   now.Add(3 * time.Hour),
			})
			r.Equal(tc.wantCode, rec.Code, rec.Body.String())

			overrides, err := h.svc.ListOverrides(t.Context(), h.acme.schedule.UID, nil, nil)
			r.NoError(err)

			if tc.wantCode == http.StatusCreated {
				r.Len(overrides, 2)
			} else {
				r.Len(overrides, 1, "a rejected create adds nothing")
			}
		})
	}
}

func TestHandlerUpdateScheduleRosterRejectsForeignUser(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHandlerSetup(t)
	path := "/api/v1/orgs/" + h.acme.org.Slug + "/on-call-schedules/" + h.acme.schedule.UID

	rec := h.do(t, http.MethodPatch, path, map[string]any{"userUids": []string{h.other.user.UID}})
	r.Equal(http.StatusBadRequest, rec.Code, rec.Body.String())

	roster, err := h.svc.ListUsers(t.Context(), h.acme.schedule.UID)
	r.NoError(err)
	r.Len(roster, 1)
	r.Equal(h.acme.user.UID, roster[0].UserUID, "a rejected update keeps the roster")

	rec = h.do(t, http.MethodPatch, path, map[string]any{"userUids": []string{h.acme.user.UID}})
	r.Equal(http.StatusOK, rec.Code, rec.Body.String())
}

func TestServiceCreateScheduleRejectsForeignUser(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, org := newTestService(t)

	otherOrg := models.NewOrganization("other", "")
	r.NoError(dbSvc.CreateOrganization(t.Context(), otherOrg))

	stranger := newTestUser(t, dbSvc, otherOrg.UID, "stranger")

	_, err := svc.CreateSchedule(t.Context(), &oncallschedules.CreateScheduleInput{
		OrganizationUID: org.UID,
		Name:            "Primary",
		Timezone:        "UTC",
		RotationType:    models.RotationTypeDaily,
		HandoffTime:     "09:00",
		StartAt:         time.Now(),
		UserUIDs:        []string{stranger.UID},
	})
	r.ErrorIs(err, oncallschedules.ErrUserNotInOrganization)

	schedules, err := svc.ListSchedules(t.Context(), org.UID)
	r.NoError(err)
	r.Empty(schedules)
}
