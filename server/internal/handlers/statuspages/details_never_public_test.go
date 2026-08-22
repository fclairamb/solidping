package statuspages

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/attachments"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// isForbiddenPublicFieldName reports JSON keys that must never appear anywhere in a
// PUBLIC status-page payload. `details` is the incidents.details JSONB column,
// which since spec 2026-08-20-01 can carry the probe's failure capture —
// response bodies with internal hostnames, stack traces or PII.
//
// `attachments` / `downloadurl` / `topic` join the list with spec
// 2026-08-21-01: an attachment is org-operational evidence of the same kind,
// and a browser check's screenshot is a picture of whatever the monitored
// page was rendering. Its signed download URL is no safer than the bytes —
// the signature IS the authorization, so publishing the URL publishes the
// image.
func isForbiddenPublicFieldName(name string) bool {
	switch name {
	case "details", "failureresponse", "firstresult", "first_result",
		"lastfailure", "last_failure", "output", "diagnostics",
		"attachments", "attachment", "downloadurl", "signedurl", "topic",
		"screenshot", "screenshoturl":
		return true
	default:
		return false
	}
}

// forbiddenPublicFieldTypes are Go types that carry arbitrary operator/probe
// data. A public response struct must not embed one, no matter what it is
// named — that is what stops a future `Extra models.JSONMap` from quietly
// becoming a leak.
func isForbiddenPublicType(typ reflect.Type) bool {
	switch typ {
	case reflect.TypeOf(models.JSONMap{}),
		reflect.TypeOf(models.Incident{}),
		reflect.TypeOf(map[string]any{}),
		// attachments.View carries a signed download URL for org-only
		// evidence. Pinned by TYPE as well as by field name so renaming the
		// field cannot smuggle it onto a public page.
		reflect.TypeOf(attachments.View{}),
		reflect.TypeOf(models.File{}):
		return true
	default:
		return false
	}
}

// TestPublicStatusPagePayloadCarriesNoIncidentDetails walks the whole
// StatusPageResponse type graph — including the activeIncidents[] block added
// by the auto-publication work (spec 2026-08-19-08) — and fails if any field
// could carry incident details.
//
// This is a STRUCTURAL pin rather than a value assertion on purpose: it fails
// the moment somebody adds the field, not only when a test happens to populate
// it.
func TestPublicStatusPagePayloadCarriesNoIncidentDetails(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	seen := map[reflect.Type]bool{}
	visited := 0

	var walk func(typ reflect.Type, path string)

	walk = func(typ reflect.Type, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
			typ = typ.Elem()
		}

		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}

		seen[typ] = true

		for i := range typ.NumField() {
			field := typ.Field(i)
			visited++

			name := strings.ToLower(strings.Split(field.Tag.Get("json"), ",")[0])
			if name == "" {
				name = strings.ToLower(field.Name)
			}

			r.False(isForbiddenPublicFieldName(name),
				"%s.%s (json %q) would expose incident details on a public status page", path, field.Name, name)
			r.False(isForbiddenPublicType(field.Type),
				"%s.%s carries an untyped/operator payload that could leak incident details", path, field.Name)

			walk(field.Type, path+"."+field.Name)
		}
	}

	walk(reflect.TypeOf(StatusPageResponse{}), "StatusPageResponse")

	// Positive control: the walk actually traversed a real graph. A silently
	// empty walk would make every assertion above vacuous.
	r.Greater(visited, 30, "the type walk must have visited the real response graph")
	r.True(seen[reflect.TypeOf(PublicIncident{})], "activeIncidents[] must be part of the walked graph")
	r.True(seen[reflect.TypeOf(PublicIncidentUpdate{})])
	r.True(seen[reflect.TypeOf(StatusPageSectionResponse{})])
	r.True(seen[reflect.TypeOf(StatusPageResourceResponse{})])
	r.True(seen[reflect.TypeOf(StatusUpdatePublicResponse{})])
}

// TestPublicIncidentSerializationDropsDetails is the value-level companion: a
// PublicIncident built from an incident whose details carry a captured
// response body serializes none of it.
func TestPublicIncidentSerializationDropsDetails(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const marker = "acme-origin-stack-trace-marker"

	// The incident as it exists in the database, capture and all.
	incident := models.Incident{
		UID:       "inc-1",
		StartedAt: time.Now(),
		Details: models.JSONMap{
			"failure_reason": "unexpected status code 503",
			"failureResponse": map[string]any{
				"statusCode": 503,
				"body":       "<html>" + marker + "</html>",
			},
		},
	}

	// Positive control: the marker really is in the incident we are publishing.
	rawIncident, err := json.Marshal(incident.Details)
	r.NoError(err)
	r.Contains(string(rawIncident), marker)

	// The public projection is built field-by-field from operator-authored
	// values; there is no path from Details into it.
	public := PublicIncident{
		UID:               incident.UID,
		Title:             "Investigating elevated errors",
		State:             "active",
		StartedAt:         incident.StartedAt,
		AffectedResources: []string{"API"},
		Updates: []PublicIncidentUpdate{{
			UID:          "upd-1",
			Kind:         "investigating",
			Title:        "Investigating",
			BodyMarkdown: "We are looking into it.",
			PublishedAt:  time.Now(),
		}},
	}

	response := StatusPageResponse{
		UID:             "page-1",
		Name:            "Acme Status",
		Slug:            testPublicSlug,
		ActiveIncidents: []PublicIncident{public},
	}

	rawPublic, err := json.Marshal(response)
	r.NoError(err)
	// Positive control: the payload is non-empty and really does carry the
	// incident block, so the absence assertions below mean something.
	r.Contains(string(rawPublic), "activeIncidents")
	r.Contains(string(rawPublic), "Investigating elevated errors")

	r.NotContains(string(rawPublic), marker)
	r.NotContains(string(rawPublic), "failureResponse")
	r.NotContains(string(rawPublic), "failure_reason")
	r.NotContains(string(rawPublic), `"details"`)
}

// TestViewStatusPageNeverLeaksIncidentDetails drives the real public view
// against a database holding an incident with a capture, and asserts the
// rendered payload contains none of it.
func TestViewStatusPageNeverLeaksIncidentDetails(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	const marker = "acme-origin-stack-trace-marker"

	check := models.NewCheck(org.UID, "api", "http")
	check.Status = models.CheckStatusDown
	r.NoError(svc.db.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now(), "api is down")
	incident.Details = models.JSONMap{
		"failure_reason": "unexpected status code 503",
		"failureResponse": map[string]any{
			"statusCode": 503,
			"body":       "<html>" + marker + "</html>",
			"headers":    map[string]any{"Server": "acme-internal-lb-07"},
		},
	}
	r.NoError(svc.db.CreateIncident(ctx, incident))

	// Positive control: the capture really is persisted and readable.
	stored, err := svc.db.GetIncident(ctx, org.UID, incident.UID)
	r.NoError(err)
	storedRaw, err := json.Marshal(stored.Details)
	r.NoError(err)
	r.Contains(string(storedRaw), marker)

	page := models.NewStatusPage(org.UID, "Acme Status", testPublicSlug)
	page.Enabled = true
	page.Visibility = visibilityPublic
	r.NoError(svc.db.CreateStatusPage(ctx, page))

	section, err := svc.CreateSection(ctx, org.Slug, page.Slug, CreateSectionRequest{Name: "Core"})
	r.NoError(err)

	_, err = svc.CreateResource(ctx, org.Slug, page.Slug, section.Slug, CreateResourceRequest{
		CheckUID: check.UID,
	})
	r.NoError(err)

	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)

	rawView, err := json.Marshal(view)
	r.NoError(err)

	// Positive control: this is a real, populated page render.
	r.Contains(string(rawView), "Acme Status")
	r.Contains(string(rawView), "Core")

	r.NotContains(string(rawView), marker)
	r.NotContains(string(rawView), "acme-internal-lb-07")
	r.NotContains(string(rawView), "failureResponse")
	r.NotContains(string(rawView), "failure_reason")
	r.NotContains(string(rawView), `"details"`)
}

// TestViewStatusPageNeverLeaksIncidentAttachments is the attachments-rail
// companion (spec 2026-08-21-01): an incident with a real screenshot
// attachment renders a public page carrying neither the attachment nor its
// signed URL.
//
// Value-level rather than structural, because the structural pin above only
// proves the TYPE is absent — this proves the bytes and the link are too,
// even though the same incident is the one being published.
func TestViewStatusPageNeverLeaksIncidentAttachments(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	const (
		fileMarker = "acme-incident-screenshot-file-uid"
		urlMarker  = "acme-signed-download-marker"
	)

	check := models.NewCheck(org.UID, "shop", "browser")
	check.Status = models.CheckStatusDown
	r.NoError(svc.db.CreateCheck(ctx, check))

	incident := models.NewIncident(org.UID, check.UID, time.Now(), "shop is down")
	r.NoError(svc.db.CreateIncident(ctx, incident))

	// The attachment as the org-only API would see it, complete with a signed
	// link. Nothing on the public side may reproduce either field.
	topic := models.AttachmentTopic(
		models.AttachmentEntityIncidents, incident.UID, models.AttachmentKindScreenshot,
	)
	view := attachments.View{
		UID:         fileMarker,
		Name:        "shop-screenshot.png",
		Kind:        attachments.Kind(&topic),
		MimeType:    "image/png",
		Size:        4096,
		DownloadURL: "https://status.acme.com/pub/files/" + fileMarker + "?sig=" + urlMarker,
		Details: models.JSONMap{
			models.AttachmentDetailRegion:  "eu-west",
			models.AttachmentDetailTrigger: models.AttachmentTriggerIncidentOpen,
		},
		CreatedAt: time.Now(),
	}

	// Positive control: the view really does carry both markers, so their
	// absence below is a property of the public payload and not of the fixture.
	rawView, err := json.Marshal(view)
	r.NoError(err)
	r.Contains(string(rawView), fileMarker)
	r.Contains(string(rawView), urlMarker)

	page := models.NewStatusPage(org.UID, "Acme Status", testPublicSlug)
	page.Enabled = true
	page.Visibility = visibilityPublic
	r.NoError(svc.db.CreateStatusPage(ctx, page))

	section, err := svc.CreateSection(ctx, org.Slug, page.Slug, CreateSectionRequest{Name: "Core"})
	r.NoError(err)

	_, err = svc.CreateResource(ctx, org.Slug, page.Slug, section.Slug, CreateResourceRequest{
		CheckUID: check.UID,
	})
	r.NoError(err)

	public, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)

	rawPublic, err := json.Marshal(public)
	r.NoError(err)

	// Positive control: this is a real, populated page render.
	r.Contains(string(rawPublic), "Acme Status")
	r.Contains(string(rawPublic), "Core")

	r.NotContains(string(rawPublic), fileMarker)
	r.NotContains(string(rawPublic), urlMarker)
	r.NotContains(string(rawPublic), `"attachments"`)
	r.NotContains(string(rawPublic), "downloadUrl")
	r.NotContains(string(rawPublic), "/pub/files/")
}
