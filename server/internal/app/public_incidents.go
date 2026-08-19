package app

import (
	"context"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
)

// publicIncidentAdapter converts incidentpublications' public projection into
// the statuspages wire type.
//
// The two structs are field-identical by design. They are separate types so the
// import edge stays one-way: statuspages declares the small provider interface
// it needs, incidentpublications knows nothing about statuspages, and neither
// imports the other. The cost is this adapter; the alternative is an import
// cycle or one package reaching into the other's response shapes.
type publicIncidentAdapter struct {
	svc *incidentpublications.Service
}

var _ statuspages.PublicIncidentProvider = publicIncidentAdapter{}

// ListPublicIncidents implements statuspages.PublicIncidentProvider.
func (a publicIncidentAdapter) ListPublicIncidents(
	ctx context.Context, page *models.StatusPage, activeOnly bool,
) ([]statuspages.PublicIncident, error) {
	incidents, err := a.svc.ListPublicIncidents(ctx, page, activeOnly)
	if err != nil {
		return nil, err
	}

	out := make([]statuspages.PublicIncident, 0, len(incidents))

	for i := range incidents {
		inc := &incidents[i]
		entry := statuspages.PublicIncident{
			UID:               inc.UID,
			Title:             inc.Title,
			State:             inc.State,
			Severity:          inc.Severity,
			StartedAt:         inc.StartedAt,
			ResolvedAt:        inc.ResolvedAt,
			AffectedResources: inc.AffectedResources,
		}

		for j := range inc.Updates {
			upd := &inc.Updates[j]
			entry.Updates = append(entry.Updates, statuspages.PublicIncidentUpdate{
				UID:          upd.UID,
				Kind:         upd.Kind,
				Title:        upd.Title,
				BodyMarkdown: upd.BodyMarkdown,
				PublishedAt:  upd.PublishedAt,
			})
		}

		out = append(out, entry)
	}

	return out, nil
}
