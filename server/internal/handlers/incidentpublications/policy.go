package incidentpublications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// eligiblePage is one status page that should publish a given incident, with
// the resolved policy already applied.
type eligiblePage struct {
	page *models.StatusPage
	// publicName is the resource's public display name on that page — the ONLY
	// incident-derived string that reaches a public field.
	publicName string
}

// OnIncidentOpened is the auto-publish trigger. It runs after an incident
// opens (create, reopen, group create, group reopen) and decides, per status
// page, whether and when the incident becomes public.
//
// It is deliberately best-effort and never returns an error to the incident
// state machine: a status page that failed to publish is a missing
// announcement; an incident that failed to open is an outage nobody is paged
// for.
func (s *Service) OnIncidentOpened(ctx context.Context, incident *models.Incident) {
	if incident == nil {
		return
	}

	pages := s.eligiblePages(ctx, incident)
	now := s.clock.Now()

	if len(pages) == 0 {
		// Worth a line: "my outage never reached the status page" is the single
		// most likely support question this feature will generate, and the
		// answer is almost always one of the policy skips.
		s.logger.DebugContext(ctx, "auto-publish: no eligible status pages for incident",
			"incidentUid", incident.UID, "checkUid", incident.CheckUID,
			"pagingSuppressed", incident.PagingSuppressed)

		return
	}

	for _, target := range pages {
		delay := time.Duration(target.page.AutoPublishDelaySeconds) * time.Second

		// Delay 0 is legal and means "publish immediately" — the operator has
		// explicitly said they want no debounce.
		if delay <= 0 || s.scheduler == nil {
			if err := s.AutoPublish(ctx, incident.OrganizationUID, incident.UID, target.page.UID); err != nil {
				s.logger.WarnContext(ctx, "auto-publish failed",
					"incidentUid", incident.UID, "statusPageUid", target.page.UID, "error", err)
			}

			continue
		}

		if err := s.scheduler.SchedulePublish(
			ctx, incident.OrganizationUID, incident.UID, target.page.UID, now.Add(delay),
		); err != nil {
			s.logger.WarnContext(ctx, "failed to schedule auto-publish",
				"incidentUid", incident.UID, "statusPageUid", target.page.UID, "error", err)
		}
	}
}

// eligiblePages resolves every status page that should publish this incident.
// The three skips are the whole policy:
//
//   - auto_publish resolves to false (resource override wins over the page);
//   - the incident is paging_suppressed — it is rolled up under a parent, and
//     the PARENT publishes; a member never does, or one outage would produce a
//     wall of public incidents;
//   - the check is inside an active maintenance window — planned work is not an
//     incident, and announcing it as one is worse than saying nothing.
func (s *Service) eligiblePages(ctx context.Context, incident *models.Incident) []eligiblePage {
	if incident.PagingSuppressed {
		return nil
	}

	if s.checkInMaintenance(ctx, incident.CheckUID) {
		return nil
	}

	// The group comes from the CHECK, not from incidents.check_group_uid — that
	// column is NULL on every per-check incident, and reading it would make a
	// status page that displays a group as ONE resource silently stop
	// publishing outages for the checks behind it (spec 2026-08-24-14).
	targets, err := s.db.ListStatusPageTargetsForCheck(ctx, incident.CheckUID, s.incidentGroupUID(ctx, incident))
	if err != nil {
		s.logger.WarnContext(ctx, "auto-publish: failed to resolve status page targets",
			"checkUid", incident.CheckUID, "error", err)

		return nil
	}

	seen := make(map[string]struct{}, len(targets))
	out := make([]eligiblePage, 0, len(targets))

	for _, target := range targets {
		if _, dup := seen[target.PageUID]; dup {
			continue
		}

		// Resource override wins; nil means inherit the page.
		if target.ResourceAutoPublish != nil && !*target.ResourceAutoPublish {
			continue
		}

		page, pageErr := s.db.GetStatusPage(ctx, incident.OrganizationUID, target.PageUID)
		if pageErr != nil || page == nil || !page.Enabled {
			continue
		}

		if target.ResourceAutoPublish == nil && !page.AutoPublish {
			continue
		}

		seen[target.PageUID] = struct{}{}

		name := ""
		if target.ResourcePublicName != nil {
			name = *target.ResourcePublicName
		}

		out = append(out, eligiblePage{page: page, publicName: name})
	}

	return out
}

// checkInMaintenance reports whether the check is inside an active maintenance
// window right now. It reads the window definitions directly rather than
// reusing the incidents service's cached copy — the auto-publish path is not
// the per-result hot path, and a stale cache here would publish a planned
// maintenance as an outage.
func (s *Service) checkInMaintenance(ctx context.Context, checkUID string) bool {
	windows, err := s.db.ListMaintenanceWindowsForCheck(ctx, checkUID)
	if err != nil {
		s.logger.WarnContext(ctx, "auto-publish: failed to load maintenance windows",
			"checkUid", checkUID, "error", err)

		return false
	}

	now := s.clock.Now()

	for _, window := range windows {
		if models.IsActiveAt(window, now) {
			return true
		}
	}

	return false
}

// AutoPublish creates (or reuses) the publication for one incident on one page.
// It is the body of the debounce job AND the delay-0 inline path, so every
// eligibility decision that must be re-evaluated at FIRE time lives here:
//
//   - the incident resolved during the debounce → publish nothing, ever. A
//     40-second blip is exactly what the delay exists to swallow;
//   - the incident became paging-suppressed, or the check entered a
//     maintenance window, in the meantime;
//   - a publication already exists (a concurrent job fire, or an operator who
//     published by hand while the timer ran) → reuse it, never mint a second.
//
// Idempotency is belt-and-braces: the read below catches the common case, and
// the partial unique index catches the racing writers the read cannot.
//
//nolint:cyclop // a flat list of fire-time guards; nesting them would hide them.
func (s *Service) AutoPublish(ctx context.Context, orgUID, incidentUID, statusPageUID string) error {
	incident, err := s.db.GetIncident(ctx, orgUID, incidentUID)
	if err != nil || incident == nil {
		return fmt.Errorf("auto-publish: incident %s not found: %w", incidentUID, err)
	}

	// The blip guard. Everything else in this function is bookkeeping; this is
	// the line that keeps a short outage off the public page.
	if incident.State == models.IncidentStateResolved {
		return nil
	}

	if incident.PagingSuppressed {
		return nil
	}

	if s.checkInMaintenance(ctx, incident.CheckUID) {
		return nil
	}

	// A page that vanished, or was disabled, during the debounce is not an
	// error worth retrying the job over — there is simply nowhere to publish.
	page, pageErr := s.db.GetStatusPage(ctx, orgUID, statusPageUID)
	if pageErr != nil || page == nil || !page.Enabled {
		return nil //nolint:nilerr // a missing/disabled page means "nothing to publish", not a failure.
	}

	// The page's own settings are re-read at fire time too: an operator who
	// turned auto-publish off during the debounce meant it.
	if !s.pageStillEligible(ctx, incident, statusPageUID) {
		return nil
	}

	existing, findErr := s.db.FindIncidentPublication(ctx, incidentUID, statusPageUID)

	switch {
	case findErr == nil && existing != nil:
		// Already published — a concurrent job fire, or an operator who
		// published by hand while the timer ran. Reuse, never duplicate.
		return nil
	case findErr != nil && !errors.Is(findErr, sql.ErrNoRows):
		return fmt.Errorf("auto-publish: look up existing publication: %w", findErr)
	}

	// One group, one public entry per page (spec 2026-08-24-14). Members now
	// each get their own incident — internally that is correct, publicly it
	// would be six near-identical notices for one outage. The first member to
	// publish owns the entry; this one appends an "also affecting X" note to it
	// instead of minting a second.
	if groupUID := s.incidentGroupUID(ctx, incident); groupUID != nil {
		if s.consolidateIntoSibling(ctx, page, incident, *groupUID) {
			return nil
		}
	}

	title := s.templatedTitle(ctx, orgUID, page, incident)
	now := s.clock.Now()

	pub := models.NewIncidentPublication(orgUID, page.UID, title, now)
	pub.IncidentUID = &incident.UID
	pub.AutoCreated = true

	if err := s.db.CreateIncidentPublication(ctx, pub); err != nil {
		if isUniqueViolation(err) {
			// Lost the race to a concurrent fire or a manual publish. The other
			// writer's row is the publication; ours never existed.
			return nil
		}

		return fmt.Errorf("auto-publish: create publication: %w", err)
	}

	tpl := templatesFor(page.Language)

	name := s.affectedName(ctx, orgUID, page, incident)
	if name == "" {
		name = tpl.GroupOpenedTitle
	}

	s.postUpdate(ctx, page, pub, models.StatusUpdateKindInvestigating,
		title, fmt.Sprintf(tpl.OpenedBody, name), nil)

	s.emit(ctx, orgUID, models.EventTypeStatusPageIncidentPublished, pub, "")
	s.publishHint(ctx, orgUID)

	return nil
}

// pageStillEligible re-runs the policy for one page at job fire time.
func (s *Service) pageStillEligible(ctx context.Context, incident *models.Incident, pageUID string) bool {
	for _, target := range s.eligiblePages(ctx, incident) {
		if target.page.UID == pageUID {
			return true
		}
	}

	return false
}

// OnIncidentResolved syncs every auto-created publication of an incident when
// the incident resolves. The page's auto_resolve policy crossed with
// human_touched_at decides what happens:
//
//   - never              → nothing at all;
//   - always             → resolve, whoever wrote it;
//   - if_untouched, clean → resolve with a templated `resolved` update;
//   - if_untouched, touched → post a templated `monitoring` note saying the
//     component recovered, and leave the final resolve to the human who took
//     the narrative over. Auto-resolving under them would overwrite a
//     deliberate editorial decision with a machine's opinion.
//
// Hand-authored publications (auto_created = false) are never touched.
func (s *Service) OnIncidentResolved(ctx context.Context, incident *models.Incident) {
	if incident == nil {
		return
	}

	groupUID := s.incidentGroupUID(ctx, incident)

	// A consolidated public entry closes when the LAST member recovers. Closing
	// it on the first recovery would announce "resolved" while five sibling
	// checks are still down — the public page would be actively wrong, which is
	// worse than being late (spec 2026-08-24-14).
	if groupUID != nil && s.groupHasOtherActiveIncident(ctx, incident, *groupUID) {
		s.logger.DebugContext(ctx, "auto-resolve: group still has active members, publication stays open",
			"incidentUid", incident.UID, "checkGroupUid", *groupUID)

		return
	}

	pubs, err := s.db.ListIncidentPublications(ctx, &models.ListIncidentPublicationsFilter{
		OrganizationUID: incident.OrganizationUID,
		IncidentUID:     incident.UID,
		Limit:           100,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "auto-resolve: failed to list publications",
			"incidentUid", incident.UID, "error", err)

		return
	}

	// The entry may hang off a SIBLING's incident: consolidation gave ownership
	// to whichever member published first, and that one may well have recovered
	// before this one. Without this the last member to recover would find no
	// publication of its own and the public entry would never close.
	if groupUID != nil {
		pubs = append(pubs, s.groupSiblingPublications(ctx, incident, *groupUID)...)
	}

	for _, pub := range pubs {
		if !pub.AutoCreated || pub.IsResolved() {
			continue
		}

		page, pageErr := s.db.GetStatusPage(ctx, incident.OrganizationUID, pub.StatusPageUID)
		if pageErr != nil || page == nil {
			continue
		}

		s.applyResolvePolicy(ctx, page, pub, incident)
	}
}

func (s *Service) applyResolvePolicy(
	ctx context.Context, page *models.StatusPage,
	pub *models.IncidentPublication, incident *models.Incident,
) {
	policy := models.AutoResolvePolicy(page.AutoResolve)
	if !policy.IsValid() {
		policy = models.AutoResolveIfUntouched
	}

	if policy == models.AutoResolveNever {
		return
	}

	touched := pub.HumanTouchedAt != nil
	tpl := templatesFor(page.Language)

	name := s.affectedName(ctx, incident.OrganizationUID, page, incident)
	if name == "" {
		name = tpl.GroupOpenedTitle
	}

	now := s.clock.Now()

	if policy == models.AutoResolveIfUntouched && touched {
		s.postUpdate(ctx, page, pub, models.StatusUpdateKindMonitoring,
			fmt.Sprintf(tpl.MonitoringTitle, name), tpl.MonitoringBody, nil)

		s.emit(ctx, incident.OrganizationUID, models.EventTypeStatusPageIncidentUpdated, pub, "")
		s.publishHint(ctx, incident.OrganizationUID)

		return
	}

	resolved := models.PublicationStateResolved
	if err := s.db.UpdateIncidentPublication(ctx, pub.UID, &models.IncidentPublicationUpdate{
		PublicState: &resolved,
		ResolvedAt:  &now,
	}); err != nil {
		s.logger.WarnContext(ctx, "auto-resolve: failed to resolve publication",
			"publicationUid", pub.UID, "error", err)

		return
	}

	pub.PublicState = resolved
	pub.ResolvedAt = &now

	s.postUpdate(ctx, page, pub, models.StatusUpdateKindResolved,
		fmt.Sprintf(tpl.ResolvedTitle, name), fmt.Sprintf(tpl.ResolvedBody, name), nil)

	s.emit(ctx, incident.OrganizationUID, models.EventTypeStatusPageIncidentResolved, pub, "")
	s.publishHint(ctx, incident.OrganizationUID)
}

// OnIncidentReopened handles a relapse: the SAME publication is reopened
// (resolved_at cleared, state back to investigating, a templated update posted)
// rather than a second public entry being minted. A customer watching a flappy
// service should see one incident with a history, not five.
//
// A publication that was never resolved is left alone — the incident relapsed
// while the page still showed it as ongoing, which is already correct.
func (s *Service) OnIncidentReopened(ctx context.Context, incident *models.Incident) {
	if incident == nil {
		return
	}

	pubs, err := s.db.ListIncidentPublications(ctx, &models.ListIncidentPublicationsFilter{
		OrganizationUID: incident.OrganizationUID,
		IncidentUID:     incident.UID,
		Limit:           100,
	})
	if err != nil {
		s.logger.WarnContext(ctx, "relapse: failed to list publications",
			"incidentUid", incident.UID, "error", err)

		return
	}

	if len(pubs) == 0 {
		// Nothing published yet — a relapse inside the reopen cooldown of an
		// incident that never made it to a page goes through the normal open
		// path (debounce included).
		s.OnIncidentOpened(ctx, incident)

		return
	}

	for _, pub := range pubs {
		if !pub.AutoCreated || !pub.IsResolved() {
			continue
		}

		page, pageErr := s.db.GetStatusPage(ctx, incident.OrganizationUID, pub.StatusPageUID)
		if pageErr != nil || page == nil {
			continue
		}

		investigating := models.PublicationStateInvestigating
		if err := s.db.UpdateIncidentPublication(ctx, pub.UID, &models.IncidentPublicationUpdate{
			PublicState:     &investigating,
			ClearResolvedAt: true,
		}); err != nil {
			s.logger.WarnContext(ctx, "relapse: failed to reopen publication",
				"publicationUid", pub.UID, "error", err)

			continue
		}

		pub.PublicState = investigating
		pub.ResolvedAt = nil

		tpl := templatesFor(page.Language)

		name := s.affectedName(ctx, incident.OrganizationUID, page, incident)
		if name == "" {
			name = tpl.GroupOpenedTitle
		}

		s.postUpdate(ctx, page, pub, models.StatusUpdateKindInvestigating,
			fmt.Sprintf(tpl.RelapseTitle, name), fmt.Sprintf(tpl.RelapseBody, name), nil)

		s.emit(ctx, incident.OrganizationUID, models.EventTypeStatusPageIncidentUpdated, pub, "")
		s.publishHint(ctx, incident.OrganizationUID)
	}
}
