package eventfeeds

import (
	"context"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/atomfeed"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// Render builds the Atom document of a feed: one entry per event in scope,
// newest first, within the events feed bounds. baseURL prefixes the
// dashboard links. The document never repeats the secret: the feed ID is the
// feed's UID, stable across rotations.
func (s *Service) Render(
	ctx context.Context, feed *models.OrgEventFeed, baseURL, lang string,
) (*atomfeed.Feed, error) {
	org, err := s.db.GetOrganization(ctx, feed.OrganizationUID)
	if err != nil || org == nil {
		return nil, ErrUnknownSecret
	}

	rows, err := s.events.ListFeedEvents(ctx, org.UID, feed.EventTypes)
	if err != nil {
		return nil, err
	}

	checkNames := s.checkNames(ctx, org.UID, rows)
	loc := i18n.New(lang, "")

	orgName := org.Name
	if orgName == "" {
		orgName = org.Slug
	}

	dashboard := strings.TrimRight(baseURL, "/") + config.DashboardBasePath + "/orgs/" + org.Slug

	updated := feed.CreatedAt
	if len(rows) > 0 {
		updated = rows[0].CreatedAt
	}

	doc := &atomfeed.Feed{
		Title:    loc.T("feed.title", "org", orgName),
		ID:       "urn:uuid:" + feed.UID,
		Updated:  atomfeed.Timestamp(updated),
		Subtitle: loc.T("feed.subtitle", "org", orgName) + " (" + feed.Name + ")",
		Links:    []atomfeed.Link{{Href: dashboard + "/events", Rel: "alternate"}},
		Entries:  make([]atomfeed.Entry, 0, len(rows)),
	}

	for _, evt := range rows {
		doc.Entries = append(doc.Entries, entry(loc, evt, dashboard, checkNames))
	}

	return doc, nil
}

// entry renders one event: the Events page label, the check it is about, and
// a link to the incident, else the check, else the events page.
func entry(loc i18n.Locale, evt *models.Event, dashboard string, checkNames map[string]string) atomfeed.Entry {
	label := string(evt.EventType)
	if key := "feed.types." + label; loc.Has(key) {
		label = loc.T(key)
	}

	subject := ""
	if evt.CheckUID != nil {
		subject = checkNames[*evt.CheckUID]
	}

	if subject == "" {
		subject, _ = evt.Payload["target_name"].(string)
	}

	title := label
	if subject != "" {
		title = label + " — " + subject
	}

	link := dashboard + "/events"
	lines := []string{label}

	switch {
	case evt.IncidentUID != nil && *evt.IncidentUID != "":
		link = dashboard + "/incidents/" + *evt.IncidentUID
	case evt.CheckUID != nil && *evt.CheckUID != "":
		link = dashboard + "/checks/" + *evt.CheckUID
	}

	if subject != "" && evt.CheckUID != nil {
		lines = append(lines, fmt.Sprintf("%s: %s", loc.T("feed.check"), subject))
	}

	if evt.IncidentUID != nil && *evt.IncidentUID != "" {
		lines = append(lines, fmt.Sprintf("%s: %s", loc.T("feed.incident"), *evt.IncidentUID))
	}

	lines = append(lines, loc.T("feed.open")+": "+link)

	stamp := atomfeed.Timestamp(evt.CreatedAt)

	return atomfeed.Entry{
		Title:     title,
		ID:        "urn:uuid:" + evt.UID,
		Updated:   stamp,
		Published: stamp,
		Link:      atomfeed.Link{Href: link},
		Content:   atomfeed.Text{Type: "text", Body: strings.Join(lines, "\n")},
	}
}

// checkNames resolves the names of the checks the events are about, in one
// query. A deleted check is simply absent (the payload's target_name, when
// there is one, stands in).
func (s *Service) checkNames(ctx context.Context, orgUID string, rows []*models.Event) map[string]string {
	uids := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))

	for _, evt := range rows {
		if evt.CheckUID == nil || *evt.CheckUID == "" {
			continue
		}

		if _, ok := seen[*evt.CheckUID]; ok {
			continue
		}

		seen[*evt.CheckUID] = struct{}{}
		uids = append(uids, *evt.CheckUID)
	}

	names := make(map[string]string, len(uids))
	if len(uids) == 0 {
		return names
	}

	checks, err := s.db.GetChecksByUIDs(ctx, orgUID, uids)
	if err != nil {
		return names
	}

	for uid, check := range checks {
		switch {
		case check.Name != nil && *check.Name != "":
			names[uid] = *check.Name
		case check.Slug != nil:
			names[uid] = *check.Slug
		}
	}

	return names
}
