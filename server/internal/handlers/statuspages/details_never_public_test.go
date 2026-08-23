package statuspages

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/nettrace"
)

// isForbiddenPublicFieldName reports JSON keys that must never appear anywhere in a
// PUBLIC status-page payload. `details` is the incidents.details JSONB column,
// which since spec 2026-08-20-01 can carry the probe's failure capture —
// response bodies with internal hostnames, stack traces or PII.
//
// The attachment names (spec 2026-08-21-01) are here for the same reason and a
// sharper one: an incident screenshot is a PICTURE of whatever the failing page
// showed — an internal admin console, a stack trace, a customer's data — and
// `downloadurl` is a SIGNED url, so publishing one would hand every reader of a
// public status page a working link to that picture, with no login in the way.
func isForbiddenPublicFieldName(name string) bool {
	switch name {
	case "details", "failureresponse", "firstresult", "first_result",
		"lastfailure", "last_failure", "output", "diagnostics",
		"attachments", "attachment", "downloadurl", "screenshot", "signedurl",
		// Path diagnostics (spec 2026-08-21-10). A traceroute names every
		// router between a probe and its target — internal gateway addresses,
		// private PTR records, the shape of a customer's transit — which is
		// operator evidence and nobody else's business.
		"traceroute", "hops", "networkfailure":
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
		reflect.TypeOf(models.File{}),
		reflect.TypeOf(attachments.Response{}),
		reflect.TypeOf(map[string]any{}):
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

// TestPublicStatusPagePayloadCarriesNoAttachments is the value-level companion
// for spec 2026-08-21-01: an attachment rendered the way the incident DETAIL
// endpoint renders it — signed download URL and all — has no path into a public
// payload.
//
// The structural walk above already forbids the field and the type; this pins
// the concrete strings, because the failure mode that would actually hurt is a
// signed URL reaching a page anybody can load.
func TestPublicStatusPagePayloadCarriesNoAttachments(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const (
		fileUID   = "6f1b0f57-9b6b-4a9c-9a67-1cb2ee2f7f21"
		signature = "acme-signed-attachment-signature"
	)

	// What the OPERATOR-facing incident payload carries.
	attachment := attachments.Response{
		UID:         fileUID,
		Kind:        attachments.KindScreenshot,
		Name:        "incident-screenshot.png",
		MimeType:    "image/png",
		Size:        4096,
		DownloadURL: "/pub/files/" + fileUID + "?exp=1&sig=" + signature,
		Region:      "eu-west",
	}

	// Positive control: the operator payload really does carry the signed URL,
	// so its absence from the public one below means something.
	rawOperator, err := json.Marshal(attachment)
	r.NoError(err)
	r.Contains(string(rawOperator), signature)
	r.Contains(string(rawOperator), fileUID)

	response := StatusPageResponse{
		UID:  "page-1",
		Name: "Acme Status",
		Slug: testPublicSlug,
		ActiveIncidents: []PublicIncident{{
			UID:               "inc-1",
			Title:             "Investigating elevated errors",
			State:             "active",
			StartedAt:         time.Now(),
			AffectedResources: []string{"API"},
		}},
	}

	rawPublic, err := json.Marshal(response)
	r.NoError(err)

	// Positive control: this is a populated public payload, not an empty one.
	r.Contains(string(rawPublic), "activeIncidents")
	r.Contains(string(rawPublic), "Investigating elevated errors")

	r.NotContains(string(rawPublic), signature)
	r.NotContains(string(rawPublic), fileUID)
	r.NotContains(string(rawPublic), "downloadUrl")
	r.NotContains(string(rawPublic), "attachments")
	r.NotContains(string(rawPublic), "/pub/files/")
}

// TestPublicStatusPagePayloadCarriesNoPathTrace is the same audit for the
// traceroute kind (spec 2026-08-21-10).
//
// It is a SECOND value-level test rather than a parameter of the one above
// because what leaks is different in kind. A screenshot leaks a picture; a path
// trace leaks TOPOLOGY — the internal gateway a probe crosses, the PTR name of a
// customer's edge router, how many hops sit between two networks. Publishing
// that on a status page would hand a reader a partial map of somebody's
// internal network, indexed by the outage that revealed it.
func TestPublicStatusPagePayloadCarriesNoPathTrace(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const (
		fileUID     = "0a5f7b41-2b0c-4d7e-9d0e-8d4c3f2a1b09"
		signature   = "acme-signed-traceroute-signature"
		internalHop = "gw-internal.acme.example"
	)

	attachment := attachments.Response{
		UID:         fileUID,
		Kind:        attachments.KindTraceroute,
		Name:        "incident-traceroute.json",
		MimeType:    "application/json",
		Size:        1024,
		DownloadURL: "/pub/files/" + fileUID + "?exp=1&sig=" + signature,
		Region:      "eu-west",
	}

	// Positive control on the OPERATOR payload: it really does carry the signed
	// link and the traceroute kind, so their absence below means something.
	rawOperator, err := json.Marshal(attachment)
	r.NoError(err)
	r.Contains(string(rawOperator), signature)
	r.Contains(string(rawOperator), attachments.KindTraceroute)

	// And the capture itself really does carry hop topology, so "no hop names
	// on the public payload" is a claim about the payload and not about an
	// empty fixture.
	capture := &nettrace.Capture{
		Mode:                nettrace.ModeICMPRaw,
		HopAddressesVisible: true,
		Host:                "acme.com",
		Address:             "192.0.2.10",
		Family:              "ipv4",
		Hops: []nettrace.Hop{
			{TTL: 1, Address: "10.0.0.1", Hostname: internalHop, Sent: 3, Received: 3},
		},
	}

	rawCapture, err := capture.Marshal()
	r.NoError(err)
	r.Contains(string(rawCapture), internalHop)

	response := StatusPageResponse{
		UID:  "page-1",
		Name: "Acme Status",
		Slug: testPublicSlug,
		ActiveIncidents: []PublicIncident{{
			UID:               "inc-1",
			Title:             "Investigating connectivity",
			State:             "active",
			StartedAt:         time.Now(),
			AffectedResources: []string{"API"},
		}},
	}

	rawPublic, err := json.Marshal(response)
	r.NoError(err)

	r.Contains(string(rawPublic), "activeIncidents")
	r.Contains(string(rawPublic), "Investigating connectivity")

	r.NotContains(string(rawPublic), signature)
	r.NotContains(string(rawPublic), fileUID)
	r.NotContains(string(rawPublic), internalHop)
	r.NotContains(string(rawPublic), "traceroute")
	r.NotContains(string(rawPublic), "hops")
}
