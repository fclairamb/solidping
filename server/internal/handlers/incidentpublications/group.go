package incidentpublications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Consolidating group members at the PUBLICATION layer (spec 2026-08-24-14).
//
// Incidents are per-check now: six members of a "RabbitMQ" group going down
// produce six incidents, and that is deliberate — prod and nonprod deserve
// distinct paging. But a status page is not a pager. A reader who sees one
// component named "RabbitMQ" must see ONE public entry for it, however many
// internal checks are behind it, or the page turns an outage into a wall of
// near-identical notices that says less than a single line would.
//
// So the "N/M checks down" framing keeps exactly one home, and this is it:
// the first member to publish owns the public entry, later members append a
// rate-limited "also affecting X" note to it, and the entry closes when the
// LAST member recovers rather than when the first one does.
//
// Nothing here reads incident.CheckGroupUID as the source of truth. That
// column is historical: it is set only on group incidents written before the
// migration, and a per-check incident for a grouped check has it NULL. The
// group comes from the CHECK, which is where it has always actually lived.

// groupSiblingIncidentLimit bounds the sibling scan. A check group with more
// than this many incidents in flight is pathological; consolidating the first
// hundred is still the right public answer.
const groupSiblingIncidentLimit = 100

// incidentGroupUID resolves the check group an incident belongs to, from the
// CHECK rather than from incidents.check_group_uid.
//
// The distinction is the whole point of the spec: after per-check incidents,
// an incident for a grouped check carries check_group_uid = NULL, and reading
// the column would report "not in a group" for every check in every group.
// Status pages that display a group as one resource would stop publishing
// their outages entirely — a silent regression on the customer-facing surface.
//
// The legacy column still wins when it is set, so historical group incidents
// resolve to their group without a second query.
func (s *Service) incidentGroupUID(ctx context.Context, incident *models.Incident) *string {
	if incident == nil {
		return nil
	}

	if incident.CheckGroupUID != nil {
		return incident.CheckGroupUID
	}

	if incident.CheckUID == "" {
		return nil
	}

	check, err := s.db.GetCheck(ctx, incident.OrganizationUID, incident.CheckUID)
	if err != nil || check == nil {
		return nil
	}

	return check.CheckGroupUID
}

// groupMemberCheckUIDs lists every check in a group, enabled or not. Disabled
// members cannot have an active incident anyway, so filtering them would only
// add a branch.
func (s *Service) groupMemberCheckUIDs(ctx context.Context, orgUID, groupUID string) []string {
	checks, _, err := s.db.ListChecks(ctx, orgUID, &models.ListChecksFilter{CheckGroupUID: &groupUID})
	if err != nil {
		s.logger.WarnContext(ctx, "group consolidation: failed to list group members",
			"checkGroupUid", groupUID, "error", err)

		return nil
	}

	out := make([]string, 0, len(checks))
	for _, check := range checks {
		out = append(out, check.UID)
	}

	return out
}

// groupSiblingIncidents returns the other incidents of the same check group,
// oldest first, optionally restricted to the active ones.
//
// Oldest-first is load-bearing: it makes "which incident owns the public
// entry" a stable answer (the first member to fail) instead of a race between
// two debounce jobs firing in whatever order the scheduler picked.
func (s *Service) groupSiblingIncidents(
	ctx context.Context, incident *models.Incident, groupUID string, activeOnly bool,
) []*models.Incident {
	memberUIDs := s.groupMemberCheckUIDs(ctx, incident.OrganizationUID, groupUID)
	if len(memberUIDs) == 0 {
		return nil
	}

	filter := &models.ListIncidentsFilter{
		OrganizationUID: incident.OrganizationUID,
		CheckUIDs:       memberUIDs,
		Kinds:           []string{models.IncidentKindCheck},
		Limit:           groupSiblingIncidentLimit,
	}
	if activeOnly {
		filter.States = []models.IncidentState{models.IncidentStateActive}
	}

	incidents, _, err := s.db.ListIncidents(ctx, filter)
	if err != nil {
		s.logger.WarnContext(ctx, "group consolidation: failed to list sibling incidents",
			"checkGroupUid", groupUID, "error", err)

		return nil
	}

	out := make([]*models.Incident, 0, len(incidents))

	for _, sibling := range incidents {
		if sibling == nil || sibling.UID == incident.UID {
			continue
		}

		out = append(out, sibling)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].UID < out[j].UID
		}

		return out[i].StartedAt.Before(out[j].StartedAt)
	})

	return out
}

// groupHasOtherActiveIncident reports whether a sibling member of the same
// group is still down. It is what keeps a consolidated public entry open until
// the LAST member recovers: closing it when the first one does would tell
// readers the outage is over while five checks are still failing.
func (s *Service) groupHasOtherActiveIncident(
	ctx context.Context, incident *models.Incident, groupUID string,
) bool {
	return len(s.groupSiblingIncidents(ctx, incident, groupUID, true)) > 0
}

// consolidateIntoSibling is the AutoPublish branch that keeps one group to one
// public entry per page. If an active auto-created publication already exists
// on this page for a sibling incident of the same group, the new member does
// not mint a second one — it appends the rate-limited "also affecting X" note
// to the existing entry and reports true.
//
// Hand-authored publications are never consolidated into: an operator writing
// their own narrative owns it, and a machine appending to it would be exactly
// the editorial override auto_resolve's human_touched_at rule exists to avoid.
func (s *Service) consolidateIntoSibling(
	ctx context.Context, page *models.StatusPage, incident *models.Incident, groupUID string,
) bool {
	for _, sibling := range s.groupSiblingIncidents(ctx, incident, groupUID, true) {
		pub, err := s.db.FindIncidentPublication(ctx, sibling.UID, page.UID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				s.logger.WarnContext(ctx, "group consolidation: failed to look up sibling publication",
					"incidentUid", sibling.UID, "statusPageUid", page.UID, "error", err)
			}

			continue
		}

		if pub == nil || !pub.AutoCreated || pub.IsResolved() {
			continue
		}

		s.logger.DebugContext(ctx, "group consolidation: reusing sibling publication",
			"incidentUid", incident.UID, "siblingIncidentUid", sibling.UID,
			"publicationUid", pub.UID, "checkGroupUid", groupUID)

		s.noteAlsoAffecting(ctx, page, pub, incident.OrganizationUID, incident.CheckUID)

		return true
	}

	return false
}

// groupSiblingPublications returns the still-open, incident-linked publications
// that belong to OTHER incidents of the same group.
//
// Resolution needs them because consolidation deliberately breaks the
// one-incident-one-publication assumption: the public entry hangs off the
// FIRST member's incident, so when the last member recovers, listing
// publications by its own incident UID finds nothing and the entry would stay
// open forever.
//
// Its only caller is OnIncidentResolved, so the filter here is auto-resolve
// scope and nothing else — and it uses the same rule that function does
// ("linked to an incident", never "created by a machine"). Filtering on
// auto_created would have re-created, for the consolidated-group case alone,
// exactly the hole spec 2026-09-02-05 closed everywhere else: a hand-published
// entry owning a group outage would never close when a SIBLING was the last
// member to recover.
func (s *Service) groupSiblingPublications(
	ctx context.Context, incident *models.Incident, groupUID string,
) []*models.IncidentPublication {
	var out []*models.IncidentPublication

	for _, sibling := range s.groupSiblingIncidents(ctx, incident, groupUID, false) {
		pubs, err := s.db.ListIncidentPublications(ctx, &models.ListIncidentPublicationsFilter{
			OrganizationUID: incident.OrganizationUID,
			IncidentUID:     sibling.UID,
			ActiveOnly:      true,
			Limit:           groupSiblingIncidentLimit,
		})
		if err != nil {
			continue
		}

		for _, pub := range pubs {
			if pub == nil || pub.IncidentUID == nil || pub.IsResolved() {
				continue
			}

			out = append(out, pub)
		}
	}

	return out
}

// groupMemberNoteInterval is the minimum spacing between two "also affecting"
// notes on the same publication. A group whose members fail one after another
// within seconds must read as one incident, not as a wall of near-identical
// posts — so the notes are coalesced rather than queued.
const groupMemberNoteInterval = 5 * time.Minute

// noteAlsoAffecting appends the rate-limited "also affecting X" note for a
// member check that joined an already-published group outage.
//
// It is driven from inside this package now rather than pushed in by the
// incident state machine (the PublicationHook lost OnGroupMemberJoined with
// the group write path): consolidation is a publication decision, and only
// this package knows whether a sibling entry exists on this page.
func (s *Service) noteAlsoAffecting(
	ctx context.Context, page *models.StatusPage, pub *models.IncidentPublication, orgUID, checkUID string,
) {
	// nil group UID ON PURPOSE: this asks "does the JOINING CHECK have a
	// component of its own on this page?", not "is its group displayed here?".
	// Passing the group through would match the group resource and announce
	// "also affecting <group>" — naming the component the publication is
	// already about, which tells the reader nothing and reads as a stutter. A
	// group resource renders as ONE component and never lists its members, so
	// a member that is only visible through it has nothing new to say.
	names := s.resourceNamesOnPage(ctx, orgUID, page.UID, checkUID, nil)
	if len(names) == 0 {
		return
	}

	if s.recentlyNotedMember(ctx, pub) {
		return
	}

	tpl := templatesFor(page.Language)

	s.postUpdate(ctx, page, pub, models.StatusUpdateKindInfo,
		fmt.Sprintf(tpl.AlsoAffectingTitle, names[0]),
		fmt.Sprintf(tpl.AlsoAffectingBody, names[0]), nil)

	s.emit(ctx, orgUID, models.EventTypeStatusPageIncidentUpdated, pub, "")
}

// recentlyNotedMember reports whether an "also affecting" note was already
// posted on this publication inside groupMemberNoteInterval.
func (s *Service) recentlyNotedMember(ctx context.Context, pub *models.IncidentPublication) bool {
	updates, err := s.db.ListStatusUpdates(ctx, pub.OrganizationUID, models.StatusUpdatesFilter{
		StatusPageUID:          pub.StatusPageUID,
		IncidentPublicationUID: &pub.UID,
		Limit:                  50,
	})
	if err != nil {
		return false
	}

	cutoff := s.clock.Now().Add(-groupMemberNoteInterval)

	for _, upd := range updates {
		if upd.Kind == models.StatusUpdateKindInfo && upd.PublishedAt.After(cutoff) {
			return true
		}
	}

	return false
}
