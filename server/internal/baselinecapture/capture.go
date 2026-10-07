// Package baselinecapture stores the change-detection baseline a dns run asks
// for (spec 2026-10-03-04). It runs in the server-side result path shared by
// in-process workers (checkworker/backend.DirectBackend) and agent results
// (handlers/workers.Service), so a check run on a private agent captures too.
package baselinecapture

import (
	"context"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/checkers/checkdns/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Payload keys of the check.baseline_captured event.
const (
	EventPayloadRegion     = "region"
	EventPayloadValueCount = "valueCount"
	eventPayloadCheckUID   = "checkUid"
	eventPayloadCheckSlug  = "checkSlug"
	eventPayloadCheckName  = "checkName"
)

// Store is the slice of db.Service the capture needs.
type Store interface {
	CaptureCheckConfigBaseline(ctx context.Context, checkUID, key string, values []string) (bool, error)
	CreateEvent(ctx context.Context, event *models.Event) error
}

// Capture stores output's baseline_capture as the check's baseline for
// region, when the result belongs to a dns check and that region has none
// yet, and records a check.baseline_captured event. check may be nil (the
// event then carries the check UID only). Best-effort: a failure is logged,
// never returned, because it must not cost the result that carried it.
func Capture(
	ctx context.Context, store Store, job *models.CheckJob, check *models.Check, region *string, output map[string]any,
) {
	if job == nil || job.Type != string(checkerdef.CheckTypeDNS) {
		return
	}

	values, ok := captureValues(output)
	if !ok {
		return
	}

	if len(values) > config.MaxBaselineValues {
		slog.WarnContext(ctx, "DNS baseline capture skipped: too many values",
			"check_uid", job.CheckUID, "values", len(values))

		return
	}

	regionName := ""
	if region != nil {
		regionName = *region
	}

	key := config.BaselineKey(regionName)

	written, err := store.CaptureCheckConfigBaseline(ctx, job.CheckUID, key, values)
	if err != nil {
		slog.WarnContext(ctx, "Failed to store the DNS baseline",
			"error", err, "check_uid", job.CheckUID, "region", key)

		return
	}

	if !written {
		return
	}

	event := models.NewEvent(job.OrganizationUID, models.EventTypeCheckBaselineCaptured, models.ActorTypeSystem)
	event.CheckUID = &job.CheckUID
	event.JobUID = &job.UID
	event.Payload = models.JSONMap{
		EventPayloadRegion:     key,
		EventPayloadValueCount: len(values),
		eventPayloadCheckUID:   job.CheckUID,
	}

	if check != nil {
		if check.Slug != nil {
			event.Payload[eventPayloadCheckSlug] = *check.Slug
		}

		if check.Name != nil {
			event.Payload[eventPayloadCheckName] = *check.Name
		}
	}

	if err := store.CreateEvent(ctx, event); err != nil {
		slog.WarnContext(ctx, "Failed to record a check.baseline_captured event",
			"error", err, "check_uid", job.CheckUID)
	}
}

// captureValues reads baseline_capture from an in-process result ([]string)
// or a JSON-decoded agent result ([]any). An empty or malformed value is no
// capture.
func captureValues(output map[string]any) ([]string, bool) {
	switch typed := output[config.OutputKeyBaselineCapture].(type) {
	case []string:
		return typed, len(typed) > 0
	case []any:
		values := make([]string, 0, len(typed))

		for _, raw := range typed {
			value, ok := raw.(string)
			if !ok {
				return nil, false
			}

			values = append(values, value)
		}

		return values, len(values) > 0
	default:
		return nil, false
	}
}
