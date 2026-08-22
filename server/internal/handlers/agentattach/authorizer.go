// Package agentattach serves the deported agents' binary upload endpoint,
// POST /api/v1/agent/attachments (spec 2026-08-21-01 §6).
//
// It exists because the WS control channel is JSON: a PNG would have to be
// base64'd through the frame that carries results, which is exactly the design
// the screenshot spec rejected. Agents hold no S3 credentials by design, so
// the bytes come here and the server writes them.
//
// The endpoint is a SIBLING of the WS upgrade, not a variant of the org API:
// it authenticates the same way (Ed25519 over method|path|timestamp|nonce,
// ±5 min skew, replay-guarded), and it never accepts a bearer token, a
// session, or a PAT.
package agentattach

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Authorization failures. Each one is a REJECTION, never a downgrade: a topic
// this server cannot positively authorize is refused, not stored under a
// safer name.
var (
	// ErrTopicUnknown means no authorizer is registered for the topic's
	// entity segment. Fails closed: a new attachment kind must register a
	// validator before an agent can upload under it.
	ErrTopicUnknown = errors.New("unknown attachment topic")
	// ErrTopicMalformed means the topic is not `<entity>/<uid>/<kind>`.
	ErrTopicMalformed = errors.New("malformed attachment topic")
	// ErrTopicForbidden means the topic is well-formed and known, but names
	// something this agent may not attach to.
	ErrTopicForbidden = errors.New("attachment topic not authorized for this agent")
)

// UploaderScope is what the SERVER knows about the agent making the request,
// derived entirely from its authenticated identity row.
//
// NOTHING HERE COMES FROM THE REQUEST. That is the whole point: the topic is
// attacker-controlled input, so the org and the region it is checked against
// must come from the agent's binding instead. An org agent that asks to
// attach to another tenant's incident is rejected because its OrgUID does not
// match, not because it phrased the request wrongly.
type UploaderScope struct {
	// OrgUID is the agent's owning organization. Empty for a system agent,
	// which serves every org in one shared cloud region.
	OrgUID string
	// Region is the region slug the agent is hard-scoped to.
	Region string
	// System marks a platform-operated agent (region-scoped, org-agnostic).
	System bool
}

// Decision is a successful authorization: the org the file must be written
// under, resolved by the server rather than taken from the request.
type Decision struct {
	OrgUID string
}

// TopicAuthorizer validates one topic entity. The registry keys on the
// topic's FIRST segment, so `incidents/...` and a future `checks/...` are
// independent validators rather than branches of one growing function.
type TopicAuthorizer interface {
	// Authorize reports whether scope may attach under this topic. uid is the
	// topic's second segment and kind its third; both are already known to be
	// non-empty.
	Authorize(ctx context.Context, scope UploaderScope, uid, kind string) (Decision, error)
}

// Registry maps a topic entity to its authorizer.
type Registry struct {
	authorizers map[string]TopicAuthorizer
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{authorizers: map[string]TopicAuthorizer{}}
}

// Register installs the authorizer for one topic entity (`incidents`, ...).
func (r *Registry) Register(entity string, authorizer TopicAuthorizer) {
	r.authorizers[entity] = authorizer
}

// Authorize parses the topic and dispatches to the registered authorizer.
// Every failure path returns an error — there is no default-allow.
func (r *Registry) Authorize(
	ctx context.Context, scope UploaderScope, topic string,
) (Decision, error) {
	parts, err := ParseTopic(topic)
	if err != nil {
		return Decision{}, err
	}

	authorizer, ok := r.authorizers[parts.Entity]
	if !ok {
		return Decision{}, fmt.Errorf("%w: %s", ErrTopicUnknown, parts.Entity)
	}

	return authorizer.Authorize(ctx, scope, parts.UID, parts.Kind)
}

// maxTopicSegmentLen bounds each segment. A uid is 36 characters and a kind is
// a short word; anything longer is a probe, and letting it through would put
// unbounded attacker text into an indexed column.
const maxTopicSegmentLen = 64

// TopicParts is a parsed `<entity>/<uid>/<kind>` topic.
type TopicParts struct {
	// Entity is the first segment and selects the authorizer (`incidents`).
	Entity string
	// UID is the second segment: the entity this attachment belongs to.
	UID string
	// Kind is the third segment: what the attachment IS (`screenshot`).
	Kind string
}

// ParseTopic splits `<entity>/<uid>/<kind>` into its three segments, rejecting
// anything that is not exactly that shape.
//
// Strict on purpose. The topic is used to build a storage key and, in the
// incident case, to look a row up by uid; a segment carrying a slash, a `..`,
// or an empty string is a traversal attempt, not a typo.
func ParseTopic(topic string) (TopicParts, error) {
	segments := strings.Split(topic, "/")
	if len(segments) != 3 {
		return TopicParts{}, fmt.Errorf("%w: want <entity>/<uid>/<kind>", ErrTopicMalformed)
	}

	for _, segment := range segments {
		switch {
		case segment == "":
			return TopicParts{}, fmt.Errorf("%w: empty segment", ErrTopicMalformed)
		case segment == "." || segment == "..":
			return TopicParts{}, fmt.Errorf("%w: relative segment", ErrTopicMalformed)
		case len(segment) > maxTopicSegmentLen:
			return TopicParts{}, fmt.Errorf("%w: segment too long", ErrTopicMalformed)
		}
	}

	return TopicParts{Entity: segments[0], UID: segments[1], Kind: segments[2]}, nil
}

// IncidentLookup is the slice of the database the incident authorizer needs.
// An interface rather than db.Service so the authorizer's tests state exactly
// which two questions it asks.
type IncidentLookup interface {
	// GetIncidentAny returns an incident by UID with no org scoping — the
	// authorizer supplies the org check itself, because for a system agent
	// the org is not known until the incident is read.
	GetIncidentAny(ctx context.Context, uid string) (*models.Incident, error)
	// GetCheck returns a check within an org.
	GetCheck(ctx context.Context, orgUID, uid string) (*models.Check, error)
}

// IncidentAuthorizer authorizes `incidents/<uid>/<kind>` topics.
//
// Three questions, all answered server-side:
//  1. does the incident exist?
//  2. for an ORG agent — is it this agent's org? (a system agent has no org,
//     and legitimately serves every tenant in its shared region)
//  3. is the incident's check one this agent's REGION actually serves?
//
// (3) is what stops a compromised agent in one region from attaching evidence
// to an incident it could never have observed. A check with no explicit
// regions runs everywhere, so any agent may attach to it.
type IncidentAuthorizer struct {
	lookup IncidentLookup
	// kinds is the allowlist of attachment kinds an agent may upload under an
	// incident topic. Closed by default: a kind nobody has thought about is
	// not a kind an agent gets to invent.
	kinds map[string]bool
}

// NewIncidentAuthorizer builds the incident topic authorizer.
func NewIncidentAuthorizer(lookup IncidentLookup) *IncidentAuthorizer {
	return &IncidentAuthorizer{
		lookup: lookup,
		kinds:  map[string]bool{models.AttachmentKindScreenshot: true},
	}
}

// Authorize implements TopicAuthorizer.
func (a *IncidentAuthorizer) Authorize(
	ctx context.Context, scope UploaderScope, uid, kind string,
) (Decision, error) {
	if !a.kinds[kind] {
		return Decision{}, fmt.Errorf("%w: kind %q", ErrTopicForbidden, kind)
	}

	incident, err := a.lookup.GetIncidentAny(ctx, uid)
	if err != nil || incident == nil {
		// Deliberately the same answer as "not yours": telling an agent
		// whether an incident uid exists in some other tenant is a probe
		// oracle, and there is nothing it could legitimately do with it.
		return Decision{}, fmt.Errorf("%w: no such incident", ErrTopicForbidden)
	}

	if !scope.System && incident.OrganizationUID != scope.OrgUID {
		return Decision{}, fmt.Errorf("%w: incident belongs to another organization", ErrTopicForbidden)
	}

	check, err := a.lookup.GetCheck(ctx, incident.OrganizationUID, incident.CheckUID)
	if err != nil || check == nil {
		return Decision{}, fmt.Errorf("%w: incident check unavailable", ErrTopicForbidden)
	}

	if !regionServes(check.Regions, scope.Region) {
		return Decision{}, fmt.Errorf("%w: check is not served from %s", ErrTopicForbidden, scope.Region)
	}

	// The org is the INCIDENT's, never the request's. For an org agent it
	// equals scope.OrgUID by the check above; for a system agent this is the
	// only place it can come from.
	return Decision{OrgUID: incident.OrganizationUID}, nil
}

// regionServes reports whether an agent bound to `region` runs this check.
// An empty regions list means "everywhere", which is the default for a check
// nobody pinned.
func regionServes(regions []string, region string) bool {
	if len(regions) == 0 {
		return true
	}

	for _, candidate := range regions {
		if candidate == region {
			return true
		}
	}

	return false
}
