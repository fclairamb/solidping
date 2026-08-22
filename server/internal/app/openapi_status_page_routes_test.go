package app

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// openAPIPathIndex is the minimal shape needed to ask "which methods does the
// spec declare on this path" without pulling in a full OpenAPI parser.
type openAPIPathIndex struct {
	Paths map[string]map[string]struct {
		OperationID string                    `yaml:"operationId"`
		Responses   map[string]map[string]any `yaml:"responses"`
	} `yaml:"paths"`
	Components struct {
		Responses map[string]yaml.Node `yaml:"responses"`
		Schemas   map[string]struct {
			Properties map[string]yaml.Node `yaml:"properties"`
			AllOf      []yaml.Node          `yaml:"allOf"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

func loadOpenAPIPaths(t *testing.T) openAPIPathIndex {
	t.Helper()

	raw, err := openAPIFiles.ReadFile("openapi/openapi.yaml")
	require.NoError(t, err)

	var spec openAPIPathIndex

	require.NoError(t, yaml.Unmarshal(raw, &spec))
	require.NotEmpty(t, spec.Paths, "the embedded spec must have parsed")

	return spec
}

// TestOpenAPI_StatusPageSubscriberAndUnlockRoutesDocumented pins the routes
// spec 2026-08-21-07 added onto the published API surface.
//
// This is not a tidiness test. `server/pkg/client` is GENERATED from this file
// and `web/docs/docs/api/*` is rendered from it, so an endpoint the server
// serves but the spec omits is an endpoint no generated client can call and no
// reader of the API reference knows exists. The feature shipped once in exactly
// that state: the subscribers path declared only `get`, so the operator-side
// creator — the headline of deliverable 4 — was invisible to both.
func TestOpenAPI_StatusPageSubscriberAndUnlockRoutesDocumented(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	spec := loadOpenAPIPaths(t)

	cases := []struct {
		path        string
		method      string
		operationID string
	}{
		{
			"/api/v1/orgs/{org}/status-pages/{statusPageUid}/subscribers",
			"post", "subscribeToStatusPage",
		},
		{
			"/api/v1/orgs/{org}/status-pages/{statusPageUid}/subscribers/endpoints",
			"post", "createStatusPageEndpointSubscriber",
		},
		{"/api/v1/status-pages/{org}/{slug}/unlock", "post", "unlockStatusPage"},
		{"/api/v1/status-pages/{org}/unlock", "post", "unlockDefaultStatusPage"},
	}

	for _, tc := range cases {
		item, ok := spec.Paths[tc.path]
		r.True(ok, "the spec must declare %s", tc.path)

		op, ok := item[tc.method]
		r.True(ok, "%s must declare %s", tc.path, tc.method)
		r.Equal(tc.operationID, op.OperationID,
			"the operationId names the generated client method and the docs page filename")
	}

	// The list route keeps its GET — a failure above must read as "the POST is
	// missing", not "the whole path vanished".
	r.Contains(spec.Paths["/api/v1/orgs/{org}/status-pages/{statusPageUid}/subscribers"], "get")
}

// TestOpenAPI_GatedPublicReadsDeclareTheLockedResponse pins the 401 on every
// public status-page read.
//
// A caller generated from a spec that documents only 200/404 has no branch for
// "locked", and the failure looks to them like the page disappeared — which is
// precisely the confusion between `private` and `password` the feature exists
// to avoid.
func TestOpenAPI_GatedPublicReadsDeclareTheLockedResponse(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	spec := loadOpenAPIPaths(t)

	r.Contains(spec.Components.Responses, "StatusPageLocked",
		"the shared 401 response must exist")

	gated := []struct {
		path   string
		method string
	}{
		{"/api/v1/status-pages/{org}", "get"},
		{"/api/v1/status-pages/{org}/{slug}", "get"},
		{"/api/v1/status-pages/{org}/{slug}/summary", "get"},
		{"/api/v1/status-pages/{org}/{slug}/badge", "get"},
		{"/api/v1/status-pages/{org}/{slug}/incidents", "get"},
		{"/api/v1/status-pages/{org}/{slug}/feed.xml", "get"},
		{"/api/v1/orgs/{org}/status-pages/{statusPageUid}/subscribers", "post"},
	}

	for _, tc := range gated {
		item, ok := spec.Paths[tc.path]
		r.True(ok, "the spec must declare %s", tc.path)

		op, ok := item[tc.method]
		r.True(ok, "%s must declare %s", tc.path, tc.method)

		resp, ok := op.Responses["401"]
		r.True(ok, "%s %s must document the locked 401", tc.method, tc.path)
		r.Equal("#/components/responses/StatusPageLocked", resp["$ref"],
			"%s %s must use the shared locked response, not an ad-hoc 401", tc.method, tc.path)
	}
}

// TestOpenAPI_SubscriberSchemasCarryTheChannelFields pins the response and
// request halves of deliverable 4 onto the schemas the client is generated
// from — including the show-once signing secret, which is the whole reason the
// create response is a distinct type.
func TestOpenAPI_SubscriberSchemasCarryTheChannelFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	spec := loadOpenAPIPaths(t)

	subscriber, ok := spec.Components.Schemas["StatusPageSubscriber"]
	r.True(ok)

	for _, field := range []string{"channel", "endpoint", "failureCount", "disabled"} {
		r.Contains(subscriber.Properties, field, "StatusPageSubscriber.%s must be documented", field)
	}

	// The real URL must NOT be a documented field anywhere on the list shape —
	// it is a credential, and only the masked `endpoint` may be echoed.
	r.NotContains(subscriber.Properties, "url")
	r.NotContains(subscriber.Properties, "signingSecret")

	create, ok := spec.Components.Schemas["CreateStatusPageEndpointSubscriberRequest"]
	r.True(ok)

	for _, field := range []string{"channel", "url", "signingSecret"} {
		r.Contains(create.Properties, field)
	}

	// The create RESPONSE is the one place the secret appears.
	endpointSub, ok := spec.Components.Schemas["StatusPageEndpointSubscriber"]
	r.True(ok)
	r.NotEmpty(endpointSub.AllOf, "it must compose StatusPageSubscriber rather than restate it")

	unlock, ok := spec.Components.Schemas["StatusPageUnlockRequest"]
	r.True(ok)
	r.Contains(unlock.Properties, "password")
}
