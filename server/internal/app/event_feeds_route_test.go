package app

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/atomfeed"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

type eventFeedSecretBody struct {
	UID        string   `json:"uid"`
	Name       string   `json:"name"`
	EventTypes []string `json:"eventTypes"`
	Secret     string   `json:"secret"`
	Path       string   `json:"path"`
	URL        string   `json:"url"`
}

// seedFeedEvents writes an incident (never published to any status page) and
// its incident.created event, a check.created event, and a member.joined
// event that no feed scope may carry. It returns the incident and the check.
func seedFeedEvents(t *testing.T, env *membersRouteEnv, orgUID, slug string) (*models.Incident, *models.Check) {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()
	svc := env.server.dbService

	check := models.NewCheck(orgUID, slug, "http")
	name := "API " + slug
	check.Name = &name
	r.NoError(svc.CreateCheck(ctx, check))

	incident := models.NewIncident(orgUID, check.UID, time.Now().Add(-time.Hour), "API down")
	r.NoError(svc.CreateIncident(ctx, incident))

	created := models.NewEvent(orgUID, models.EventTypeIncidentCreated, models.ActorTypeSystem)
	created.IncidentUID = &incident.UID
	created.CheckUID = &check.UID
	r.NoError(svc.CreateEvent(ctx, created))

	checkCreated := models.NewEvent(orgUID, models.EventTypeCheckCreated, models.ActorTypeSystem)
	checkCreated.CheckUID = &check.UID
	checkCreated.CreatedAt = time.Now().Add(-2 * time.Hour)
	r.NoError(svc.CreateEvent(ctx, checkCreated))

	joined := models.NewEvent(orgUID, models.EventTypeMemberJoined, models.ActorTypeSystem)
	joined.Payload["target_name"] = "secret-member@" + slug + ".test"
	r.NoError(svc.CreateEvent(ctx, joined))

	return incident, check
}

// feedResponse is a feed read, its body already drained and closed.
type feedResponse struct {
	StatusCode int
	Header     http.Header
}

func readFeed(t *testing.T, env *membersRouteEnv, path string) (feedResponse, string) {
	t.Helper()

	resp := env.do(http.MethodGet, path, "", "", nil)
	body := readBody(t, resp)
	_ = resp.Body.Close()

	return feedResponse{StatusCode: resp.StatusCode, Header: resp.Header}, body
}

// TestOrgEventFeedRoutes covers spec 2026-11-24-12 through the real router:
// the admin gate, the secret shown once, the feed content (incidents never
// published, no membership events, nothing of another org), Atom validity,
// rotation and revocation answering 410.
func TestOrgEventFeedRoutes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMembersRouteEnv(t)
	base := "/api/v1/orgs/" + env.orgSlug + "/event-feeds"
	admin := env.jwts[models.MemberRoleAdmin]

	incident, check := seedFeedEvents(t, env, env.orgUID, "acme-api")

	other := models.NewOrganization("feeds-other-org", "Other Org")
	r.NoError(env.server.dbService.CreateOrganization(t.Context(), other))
	otherIncident, _ := seedFeedEvents(t, env, other.UID, "other-api")

	// --- Non-admins cannot list, create, rotate or revoke. ---
	for _, role := range []models.MemberRole{models.MemberRoleViewer, models.MemberRoleUser} {
		resp := env.do(http.MethodGet, base, env.jwts[role], "", nil)
		r.Equal(http.StatusForbidden, resp.StatusCode, "GET must be admin-only, role %s", role)
		_ = resp.Body.Close()

		resp = env.do(http.MethodPost, base, env.jwts[role], "application/json",
			strings.NewReader(`{"name":"wall"}`))
		r.Equal(http.StatusForbidden, resp.StatusCode, "POST must be admin-only, role %s", role)
		_ = resp.Body.Close()
	}

	// --- Scope validation: membership events are not offered. ---
	resp := env.do(http.MethodPost, base, admin, "application/json",
		strings.NewReader(`{"name":"bad","eventTypes":["member.*"]}`))
	r.Equal(http.StatusBadRequest, resp.StatusCode)
	_ = resp.Body.Close()

	resp = env.do(http.MethodPost, base, admin, "application/json", strings.NewReader(`{"name":"  "}`))
	r.Equal(http.StatusBadRequest, resp.StatusCode)
	_ = resp.Body.Close()

	// --- An admin creates a feed: the secret comes back once. ---
	resp = env.do(http.MethodPost, base, admin, "application/json", strings.NewReader(`{"name":"NOC wall"}`))
	body := readBody(t, resp)
	_ = resp.Body.Close()
	r.Equal(http.StatusCreated, resp.StatusCode, body)

	var created eventFeedSecretBody
	r.NoError(json.Unmarshal([]byte(body), &created))
	r.True(strings.HasPrefix(created.Secret, "spf_"))
	r.GreaterOrEqual(len(created.Secret), 4+43, "32 random bytes, base64url")
	r.Equal([]string{"incident.*", "check.created", "check.updated", "check.deleted"}, created.EventTypes)
	r.True(strings.HasSuffix(created.URL, created.Path))

	// The list never shows the secret.
	resp = env.do(http.MethodGet, base, admin, "", nil)
	body = readBody(t, resp)
	_ = resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Contains(body, "NOC wall")
	r.NotContains(body, created.Secret)
	r.NotContains(body, models.HashOrgEventFeedSecret(created.Secret))

	// --- The feed: no session, Atom, the unpublished incident, nothing else. ---
	feedResp, body := readFeed(t, env, created.Path)
	r.Equal(http.StatusOK, feedResp.StatusCode, body)
	r.Contains(feedResp.Header.Get("Content-Type"), "application/atom+xml")
	r.Equal("private, max-age=60", feedResp.Header.Get("Cache-Control"))
	r.Contains(feedResp.Header.Get("X-Robots-Tag"), "noindex")

	var parsed atomfeed.Feed
	r.NoError(xml.Unmarshal([]byte(body), &parsed), "the feed must parse back as Atom")
	r.Len(parsed.Entries, 2, body)
	r.Equal("Incident Created — API acme-api", parsed.Entries[0].Title, "newest first, Events page label")
	r.Contains(parsed.Entries[0].Link.Href, "/d/orgs/"+env.orgSlug+"/incidents/"+incident.UID)
	r.Contains(parsed.Entries[1].Link.Href, "/d/orgs/"+env.orgSlug+"/checks/"+check.UID)
	r.NotContains(body, "secret-member@", "membership events never reach a feed")
	r.NotContains(body, otherIncident.UID, "one org's secret never shows another org's events")
	r.NotContains(body, "other-api")
	r.NotContains(body, created.Secret, "the document never repeats the secret")

	// The feed follows Accept-Language.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, env.ts.URL+created.Path, nil)
	r.NoError(err)
	req.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")
	frResp, err := env.client.Do(req)
	r.NoError(err)
	frBody := readBody(t, frResp)
	_ = frResp.Body.Close()
	r.Contains(frBody, "Incident créé")

	// last_used_at is stamped.
	resp = env.do(http.MethodGet, base, admin, "", nil)
	body = readBody(t, resp)
	_ = resp.Body.Close()
	r.Contains(body, `"lastUsedAt"`)

	// --- Unknown and malformed secrets: 410, same body. ---
	unknown, unknownBody := readFeed(t, env, "/api/v1/org-feeds/spf_doesnotexist/feed.xml")
	r.Equal(http.StatusGone, unknown.StatusCode)
	malformed, malformedBody := readFeed(t, env, "/api/v1/org-feeds/nope/feed.xml")
	r.Equal(http.StatusGone, malformed.StatusCode)
	r.Equal(unknownBody, malformedBody)

	// --- Rotation: the old URL is gone, the new one works. ---
	resp = env.do(http.MethodPost, base+"/"+created.UID+"/rotate", admin, "", nil)
	body = readBody(t, resp)
	_ = resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode, body)

	var rotated eventFeedSecretBody
	r.NoError(json.Unmarshal([]byte(body), &rotated))
	r.NotEqual(created.Secret, rotated.Secret)

	old, oldBody := readFeed(t, env, created.Path)
	r.Equal(http.StatusGone, old.StatusCode)
	r.Equal(unknownBody, oldBody, "a rotated secret answers like an unknown one")

	fresh, _ := readFeed(t, env, rotated.Path)
	r.Equal(http.StatusOK, fresh.StatusCode)

	// --- A feed of another org is not reachable through this org. ---
	resp = env.do(http.MethodDelete, "/api/v1/orgs/"+other.Slug+"/event-feeds/"+created.UID, admin, "", nil)
	r.NotEqual(http.StatusNoContent, resp.StatusCode)
	_ = resp.Body.Close()

	// --- Revocation. ---
	resp = env.do(http.MethodDelete, base+"/"+created.UID, admin, "", nil)
	r.Equal(http.StatusNoContent, resp.StatusCode)
	_ = resp.Body.Close()

	revoked, revokedBody := readFeed(t, env, rotated.Path)
	r.Equal(http.StatusGone, revoked.StatusCode)
	r.Equal(unknownBody, revokedBody)

	// --- Every action is in the audit trail. ---
	auditRows, err := env.server.dbService.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: env.orgUID,
		EventTypes:      []models.EventType{models.EventTypeOrgSettingsUpdated},
		Limit:           20,
	})
	r.NoError(err)

	actions := map[string]bool{}
	for _, row := range auditRows {
		if row.Payload["target_type"] == "event_feed" {
			action, _ := row.Payload["action"].(string)
			actions[action] = true
		}
	}

	r.True(actions["event_feed_created"] && actions["event_feed_rotated"] && actions["event_feed_revoked"],
		"audit actions: %v", actions)
}

// TestOrgEventFeedCap pins the 10-feeds-per-org cap.
func TestOrgEventFeedCap(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	env := newMembersRouteEnv(t)
	base := "/api/v1/orgs/" + env.orgSlug + "/event-feeds"
	admin := env.jwts[models.MemberRoleAdmin]

	for i := range 10 {
		resp := env.do(http.MethodPost, base, admin, "application/json",
			bytes.NewReader([]byte(`{"name":"feed `+string(rune('a'+i))+`","eventTypes":["incident.*"]}`)))
		r.Equal(http.StatusCreated, resp.StatusCode)
		_ = resp.Body.Close()
	}

	resp := env.do(http.MethodPost, base, admin, "application/json", strings.NewReader(`{"name":"eleventh"}`))
	r.Equal(http.StatusConflict, resp.StatusCode)
	_ = resp.Body.Close()
}
