package incidentpublications

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

const (
	// orphanSweepPageSize is the page size of the open-publication scan (the
	// DB layer caps a page at 200).
	orphanSweepPageSize = 200
	// orphanSweepMaxCandidates bounds one sweep. Open publications are a
	// handful per page in practice; this only stops a pathological table from
	// turning a startup fixup into a long scan.
	orphanSweepMaxCandidates = 10_000
)

// CloseOrphanedPublications resolves the open publications that nothing will
// ever close (spec 2026-11-05-05): their incident is resolved and its check is
// deleted (soft-deleted or gone), so OnIncidentResolved already fired, or never
// will, and no recovery can follow. That is the state the checks deleted
// before the check-deletion hook existed left behind, and the one a crash
// between the soft delete and the notification would leave too.
//
// A consolidated group entry stays open while another member it covers is
// still down, the same rule as a recovery. Free-form publications (no
// incident_uid) are never touched. Idempotent: a second run closes nothing.
// Returns the number of publications it resolved.
func (s *Service) CloseOrphanedPublications(ctx context.Context) (int, error) {
	candidates, err := s.listOpenIncidentPublications(ctx)
	if err != nil {
		return 0, err
	}

	closed := 0

	for _, pub := range candidates {
		if ctx.Err() != nil {
			return closed, ctx.Err()
		}

		incident := s.orphanedIncident(ctx, pub)
		if incident == nil {
			continue
		}

		var activeSiblings []*models.Incident
		if groupUID := s.incidentGroupUID(ctx, incident); groupUID != nil {
			activeSiblings = s.groupSiblingIncidents(ctx, incident, *groupUID, true)
		}

		if s.publicationCoversActiveSibling(ctx, incident, pub, activeSiblings) {
			continue
		}

		page, pageErr := s.db.GetStatusPage(ctx, pub.OrganizationUID, pub.StatusPageUID)
		if pageErr != nil || page == nil {
			continue
		}

		if s.resolveRemoved(ctx, page, pub, incident) {
			closed++
		}
	}

	if closed > 0 {
		s.logger.InfoContext(ctx, "closed status-page entries of deleted checks", "count", closed)
	}

	return closed, nil
}

// listOpenIncidentPublications collects every unresolved publication linked to
// an incident, across organizations. Collected before any is resolved, because
// resolving shrinks the ActiveOnly set and would shift the offset paging.
func (s *Service) listOpenIncidentPublications(ctx context.Context) ([]*models.IncidentPublication, error) {
	var out []*models.IncidentPublication

	for offset := 0; offset < orphanSweepMaxCandidates; offset += orphanSweepPageSize {
		page, err := s.db.ListIncidentPublications(ctx, &models.ListIncidentPublicationsFilter{
			ActiveOnly: true,
			Limit:      orphanSweepPageSize,
			Offset:     offset,
		})
		if err != nil {
			return nil, fmt.Errorf("list open publications: %w", err)
		}

		for _, pub := range page {
			if pub != nil && pub.IncidentUID != nil && !pub.IsResolved() {
				out = append(out, pub)
			}
		}

		if len(page) < orphanSweepPageSize {
			break
		}
	}

	return out, nil
}

// orphanedIncident returns the publication's incident when it is resolved and
// its check is deleted, nil otherwise (including on any lookup error: the
// sweep only closes what it is sure about).
func (s *Service) orphanedIncident(ctx context.Context, pub *models.IncidentPublication) *models.Incident {
	incident, err := s.db.GetIncident(ctx, pub.OrganizationUID, *pub.IncidentUID)
	if err != nil || incident == nil || incident.State != models.IncidentStateResolved {
		return nil
	}

	if incident.CheckUID == "" {
		return nil
	}

	check, err := s.db.GetCheckIncludingDeleted(ctx, incident.OrganizationUID, incident.CheckUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return incident
		}

		return nil
	}

	if check == nil || check.DeletedAt != nil {
		return incident
	}

	return nil
}
