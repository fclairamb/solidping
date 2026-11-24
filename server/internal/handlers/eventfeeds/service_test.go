package eventfeeds_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/eventfeeds"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

func newService(t *testing.T) (*eventfeeds.Service, db.Service, *models.Organization) {
	t.Helper()

	svc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })
	require.NoError(t, svc.Initialize(t.Context()))

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, svc.CreateOrganization(t.Context(), org))

	return eventfeeds.NewService(svc, events.NewService(svc)), svc, org
}

func addEvent(t *testing.T, svc db.Service, orgUID string, eventType models.EventType, at time.Time) {
	t.Helper()

	evt := models.NewEvent(orgUID, eventType, models.ActorTypeSystem)
	evt.CreatedAt = at
	require.NoError(t, svc.CreateEvent(t.Context(), evt))
}

func TestCreateValidatesScope(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	feeds, _, org := newService(t)

	for _, scope := range [][]string{{"member.*"}, {"auth.*"}, {"integration.created"}, {"incident"}} {
		_, err := feeds.Create(t.Context(), org.Slug, "", &eventfeeds.CreateRequest{Name: "x", EventTypes: scope})
		r.ErrorIs(err, eventfeeds.ErrInvalidScope, "scope %v", scope)
	}

	res, err := feeds.Create(t.Context(), org.Slug, "", &eventfeeds.CreateRequest{
		Name: "automation", EventTypes: []string{"status_update.*", "incident.*", "incident.*"},
	})
	r.NoError(err)
	r.Equal([]string{"incident.*", "status_update.*"}, res.EventTypes, "deduplicated, in choice order")

	_, err = feeds.Create(t.Context(), org.Slug, "", &eventfeeds.CreateRequest{Name: strings.Repeat("n", 101)})
	r.ErrorIs(err, eventfeeds.ErrInvalidName)
}

func TestResolveRefusesWithoutLookingUp(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	feeds, _, _ := newService(t)

	for _, secret := range []string{"", "spf_", "nope", "spf_" + strings.Repeat("a", 200)} {
		_, err := feeds.Resolve(t.Context(), secret)
		r.ErrorIs(err, eventfeeds.ErrUnknownSecret)
	}
}

// TestRenderBounds: the feed holds the scope only, within 30 days and 100
// entries, whichever is smaller.
func TestRenderBounds(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	feeds, svc, org := newService(t)

	now := time.Now()
	for i := range events.FeedMaxEntries + 5 {
		addEvent(t, svc, org.UID, models.EventTypeIncidentResolved, now.Add(-time.Duration(i)*time.Minute))
	}

	addEvent(t, svc, org.UID, models.EventTypeStatusUpdateCreated, now)

	res, err := feeds.Create(t.Context(), org.Slug, "", &eventfeeds.CreateRequest{Name: "wall"})
	r.NoError(err)

	feed, err := feeds.Resolve(t.Context(), res.Secret)
	r.NoError(err)

	doc, err := feeds.Render(t.Context(), feed, "https://sp.acme.test", "en")
	r.NoError(err)
	r.Len(doc.Entries, events.FeedMaxEntries, "capped at 100 entries")

	for _, entry := range doc.Entries {
		r.Equal("Incident Resolved", entry.Title, "status_update is not in the default scope")
	}

	// Only old events: the 30-day window wins over the 100-entry cap.
	other := models.NewOrganization("acme-old", "Acme Old")
	r.NoError(svc.CreateOrganization(t.Context(), other))
	addEvent(t, svc, other.UID, models.EventTypeIncidentCreated, now.Add(-31*24*time.Hour))
	addEvent(t, svc, other.UID, models.EventTypeIncidentCreated, now.Add(-29*24*time.Hour))

	oldRes, err := feeds.Create(t.Context(), other.Slug, "", &eventfeeds.CreateRequest{Name: "old"})
	r.NoError(err)
	oldFeed, err := feeds.Resolve(t.Context(), oldRes.Secret)
	r.NoError(err)
	doc, err = feeds.Render(t.Context(), oldFeed, "https://sp.acme.test", "en")
	r.NoError(err)
	r.Len(doc.Entries, 1, "events older than 30 days are left out")
}

// TestEveryScopedEventTypeHasAFeedLabel: each event type a scope choice can
// carry has a title in the feed catalog, so no entry falls back to the raw
// type. The authoritative list is the EventType constants in models.
func TestEveryScopedEventTypeHasAFeedLabel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	src, err := os.ReadFile("../../db/models/event.go")
	r.NoError(err)

	matches := regexp.MustCompile(`EventType\w+\s+EventType = "([^"]+)"`).FindAllStringSubmatch(string(src), -1)
	r.NotEmpty(matches)

	loc := i18n.New("en", "")

	for _, match := range matches {
		eventType := match[1]

		for _, choice := range eventfeeds.ScopeChoices {
			family, isFamily := strings.CutSuffix(choice, events.FeedScopeFamilySuffix)
			if (isFamily && strings.HasPrefix(eventType, family+".")) || choice == eventType {
				r.True(loc.Has("feed.types."+eventType), "missing feed label for %s", eventType)
			}
		}
	}
}
