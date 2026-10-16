package statuspages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

// tenancyFixture is a victim org (acme) holding one component, and an attacker
// org (evilco) with its own page, section and check. Resource UIDs are public
// on the status page, so the attacker knows the victim's resource UID.
type tenancyFixture struct {
	svc           *Service
	victim        *models.Organization
	victimPage    StatusPageResponse
	victimSection StatusPageSectionResponse
	victimCheck   *models.Check
	victimRes     StatusPageResourceResponse
	attacker      *models.Organization
	attackerPage  StatusPageResponse
	attackerSect  StatusPageSectionResponse
	attackerCheck *models.Check
}

func setupResourceTenancy(t *testing.T) *tenancyFixture {
	t.Helper()

	r := require.New(t)
	ctx, svc, victim := setupStatusPagesTest(t)

	victimPage, victimSection := seedPageWithSection(ctx, t, svc, victim.Slug, "")

	victimCheck := models.NewCheck(victim.UID, "victim-api", "http")
	r.NoError(svc.db.CreateCheck(ctx, victimCheck))

	victimName := "Victim API"
	victimRes, err := svc.CreateResource(ctx, victim.Slug, victimPage.UID, victimSection.UID,
		CreateResourceRequest{CheckUID: victimCheck.UID, PublicName: &victimName})
	r.NoError(err)

	attacker := models.NewOrganization("evilco", "Evilco")
	r.NoError(svc.db.CreateOrganization(ctx, attacker))

	attackerPage, attackerSect := seedPageWithSection(ctx, t, svc, attacker.Slug, "")

	attackerCheck := models.NewCheck(attacker.UID, "evil-check", "http")
	r.NoError(svc.db.CreateCheck(ctx, attackerCheck))

	return &tenancyFixture{
		svc:    svc,
		victim: victim, victimPage: victimPage, victimSection: victimSection,
		victimCheck: victimCheck, victimRes: victimRes,
		attacker: attacker, attackerPage: attackerPage, attackerSect: attackerSect,
		attackerCheck: attackerCheck,
	}
}

// assertVictimUntouched reads the victim's component back through the victim's
// own section and checks nothing the attacker sent landed.
func (f *tenancyFixture) assertVictimUntouched(t *testing.T) {
	t.Helper()

	ctx := t.Context()

	r := require.New(t)

	resources, err := f.svc.ListResources(ctx, f.victim.Slug, f.victimPage.UID, f.victimSection.UID)
	r.NoError(err)
	r.Len(resources, 1, "the victim's component must still be listed")

	got := resources[0]
	r.Equal(f.victimRes.UID, got.UID)
	r.NotNil(got.PublicName)
	r.Equal("Victim API", *got.PublicName)
	r.Nil(got.Explanation)
	r.NotNil(got.CheckUID)
	r.Equal(f.victimCheck.UID, *got.CheckUID)
}

func hijackRequest(checkUID string) UpdateResourceRequest {
	name := "pwned"
	explanation := "attacker text"

	return UpdateResourceRequest{PublicName: &name, Explanation: &explanation, CheckUID: &checkUID}
}

// TestResourceTenancy_CrossOrg pins finding N3 (security scan 2026-10-12): a
// member of evilco addressing acme's component through evilco's own page and
// section neither rewrites nor deletes it.
func TestResourceTenancy_CrossOrg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(ctx context.Context, f *tenancyFixture) error
	}{
		{
			name: "update",
			call: func(ctx context.Context, f *tenancyFixture) error {
				_, err := f.svc.UpdateResource(ctx, f.attacker.Slug, f.attackerPage.UID, f.attackerSect.UID,
					f.victimRes.UID, hijackRequest(f.attackerCheck.UID))

				return err
			},
		},
		{
			name: "delete",
			call: func(ctx context.Context, f *tenancyFixture) error {
				return f.svc.DeleteResource(ctx, f.attacker.Slug, f.attackerPage.UID, f.attackerSect.UID,
					f.victimRes.UID)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := setupResourceTenancy(t)
			require.ErrorIs(t, tc.call(t.Context(), f), ErrStatusPageResourceNotFound)
			f.assertVictimUntouched(t)
		})
	}
}

// TestResourceTenancy_WrongSectionSamePage proves the section predicate itself,
// not just the org check: a component of section A addressed through section B
// of the same page is not found.
func TestResourceTenancy_WrongSectionSamePage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		call func(ctx context.Context, f *tenancyFixture, sectionUID string) error
	}{
		{
			name: "update",
			call: func(ctx context.Context, f *tenancyFixture, sectionUID string) error {
				_, err := f.svc.UpdateResource(ctx, f.victim.Slug, f.victimPage.UID, sectionUID,
					f.victimRes.UID, hijackRequest(f.victimCheck.UID))

				return err
			},
		},
		{
			name: "delete",
			call: func(ctx context.Context, f *tenancyFixture, sectionUID string) error {
				return f.svc.DeleteResource(ctx, f.victim.Slug, f.victimPage.UID, sectionUID, f.victimRes.UID)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f := setupResourceTenancy(t)
			ctx := t.Context()
			other, err := f.svc.CreateSection(ctx, f.victim.Slug, f.victimPage.UID,
				CreateSectionRequest{Name: "Other", Slug: "other"})
			require.NoError(t, err)

			require.ErrorIs(t, tc.call(t.Context(), f, other.UID), ErrStatusPageResourceNotFound)
			f.assertVictimUntouched(t)
		})
	}
}

// TestResourceTenancy_OwnResource is the positive control: the owner updates
// and deletes its own component through its own section.
func TestResourceTenancy_OwnResource(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	f := setupResourceTenancy(t)
	ctx := t.Context()

	name := "Renamed"
	updated, err := f.svc.UpdateResource(ctx, f.victim.Slug, f.victimPage.UID, f.victimSection.UID,
		f.victimRes.UID, UpdateResourceRequest{PublicName: &name})
	r.NoError(err)
	r.NotNil(updated.PublicName)
	r.Equal(name, *updated.PublicName)

	listed, err := f.svc.ListResources(ctx, f.victim.Slug, f.victimPage.UID, f.victimSection.UID)
	r.NoError(err)
	r.Len(listed, 1)
	r.Equal(name, *listed[0].PublicName)

	r.NoError(f.svc.DeleteResource(ctx, f.victim.Slug, f.victimPage.UID, f.victimSection.UID, f.victimRes.UID))

	listed, err = f.svc.ListResources(ctx, f.victim.Slug, f.victimPage.UID, f.victimSection.UID)
	r.NoError(err)
	r.Empty(listed)

	// A delete of the now-gone component is a 404, not a silent success.
	r.ErrorIs(f.svc.DeleteResource(ctx, f.victim.Slug, f.victimPage.UID, f.victimSection.UID, f.victimRes.UID),
		ErrStatusPageResourceNotFound)
}

// TestResourceTenancy_HandlerAnswers404 pins the wire answer: PATCH and DELETE
// on a foreign component answer 404 STATUS_PAGE_RESOURCE_NOT_FOUND, never the
// 500 (update committed, re-read failed) or 204 (deleted) they used to.
func TestResourceTenancy_HandlerAnswers404(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		body   string
	}{
		{method: http.MethodPatch, body: `{"publicName":"pwned","explanation":"attacker text"}`},
		{method: http.MethodDelete},
	}

	for _, tc := range tests {
		t.Run(tc.method, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			f := setupResourceTenancy(t)
			router := newCoverRouter(NewHandler(f.svc, &config.Config{}))

			path := "/orgs/evilco/status-pages/" + f.attackerPage.UID + "/sections/" + f.attackerSect.UID +
				"/resources/" + f.victimRes.UID
			req := httptest.NewRequestWithContext(t.Context(), tc.method, path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			r.Equal(http.StatusNotFound, rec.Code, rec.Body.String())

			var body struct {
				Code string `json:"code"`
			}
			r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
			r.Equal(string(base.ErrorCodeStatusPageResourceNotFound), body.Code)

			f.assertVictimUntouched(t)
		})
	}
}
