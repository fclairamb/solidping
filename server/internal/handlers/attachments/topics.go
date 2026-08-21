// Package attachments is the generic attachment rail (spec 2026-08-21-01): the
// `files.topic` naming rules, the authorizer registry that decides whether a
// caller may write under a given topic, and the agent-facing upload endpoint
// that is its first untrusted writer.
//
// The rail is deliberately generic. Incident screenshots are its first
// consumer; HTTP-check screenshots, HAR files and packet captures are meant to
// ride the same endpoint with a new topic prefix and a new authorizer, not a
// new upload path.
package attachments

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Topic grammar. A topic is `<entity>/<uid>/<kind>` — see models.File.Topic.
const (
	// EntityIncidents is the entity segment for incident attachments.
	EntityIncidents = "incidents"
	// KindScreenshot is the kind segment for a page capture.
	KindScreenshot = "screenshot"
)

// MaxTopicLength bounds a topic. Topics are machine-generated
// (`incidents/<uuid>/screenshot` is 55 chars) but the agent endpoint accepts
// one off the wire, so the length is checked rather than assumed.
const MaxTopicLength = 200

// Errors returned by topic parsing and authorization.
var (
	// ErrInvalidTopic means the string is not a well-formed attachment topic.
	ErrInvalidTopic = errors.New("invalid attachment topic")
	// ErrUnknownTopicEntity means no authorizer is registered for the topic's
	// entity segment. Fails CLOSED: an unrecognized entity is rejected, never
	// waved through.
	ErrUnknownTopicEntity = errors.New("no authorizer registered for this attachment topic")
	// ErrTopicForbidden means the caller may not write under this topic.
	ErrTopicForbidden = errors.New("caller may not write under this attachment topic")
)

// IncidentTopicPrefix returns the reap prefix for one incident:
// `incidents/<uid>/`. The trailing slash is load-bearing — without it the
// prefix of incident `abc` would also match incident `abcdef`.
func IncidentTopicPrefix(incidentUID string) string {
	return EntityIncidents + "/" + incidentUID + "/"
}

// IncidentScreenshotTopic returns the exact topic an incident's screenshot is
// stored under.
func IncidentScreenshotTopic(incidentUID string) string {
	return IncidentTopicPrefix(incidentUID) + KindScreenshot
}

// ParsedTopic is a topic split into its three segments.
type ParsedTopic struct {
	Entity    string
	EntityUID string
	Kind      string
}

// ParseTopic validates and splits a topic. It is strict on purpose: this is the
// only thing standing between a wire-supplied string and a storage key, so
// anything ambiguous (empty segment, path traversal, extra segments, wildcards)
// is rejected rather than normalized.
func ParseTopic(topic string) (ParsedTopic, error) {
	if topic == "" || len(topic) > MaxTopicLength {
		return ParsedTopic{}, fmt.Errorf("%w: length", ErrInvalidTopic)
	}

	parts := strings.Split(topic, "/")
	if len(parts) != 3 {
		return ParsedTopic{}, fmt.Errorf("%w: want <entity>/<uid>/<kind>", ErrInvalidTopic)
	}

	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ParsedTopic{}, fmt.Errorf("%w: empty or relative segment", ErrInvalidTopic)
		}

		if !isTopicSegment(part) {
			return ParsedTopic{}, fmt.Errorf("%w: segment %q has disallowed characters", ErrInvalidTopic, part)
		}
	}

	return ParsedTopic{Entity: parts[0], EntityUID: parts[1], Kind: parts[2]}, nil
}

// isTopicSegment allows only [A-Za-z0-9.-] — note that `_` is NOT allowed.
//
// That covers UUIDs and kind names while excluding the characters that would
// make a topic dangerous: `%` and `_` are LIKE wildcards in the prefix reap
// (they are escaped there too, but not relying on that twice is cheap), and
// `/` would let a caller invent extra segments.
func isTopicSegment(segment string) bool {
	for _, char := range segment {
		switch {
		case char >= 'a' && char <= 'z',
			char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9',
			char == '-', char == '.':
		default:
			return false
		}
	}

	return true
}

// UploaderIdentity is what an authorizer knows about the caller. It carries no
// organization: the org is the authorizer's OUTPUT, derived from the entity the
// topic names and cross-checked against the agent, never taken from the
// request.
type UploaderIdentity struct {
	// AgentUID identifies the calling agent.
	AgentUID string
	// AgentOrgUID is the org the agent is bound to, or empty for a system
	// agent (which serves a cloud region across orgs).
	AgentOrgUID string
	// AgentRegion is the region a system agent serves. Empty for an org agent.
	AgentRegion string
}

// TopicAuthorizer decides whether an identity may write under a topic, and
// returns the organization the resulting file belongs to.
//
// The returned org is THE authorization result, not a convenience: it comes
// from the entity the topic names, so a caller cannot widen its reach by
// naming someone else's entity — the authorizer refuses instead.
type TopicAuthorizer interface {
	Authorize(ctx context.Context, topic ParsedTopic, who UploaderIdentity) (orgUID string, err error)
}

// AuthorizerFunc adapts a function to TopicAuthorizer.
type AuthorizerFunc func(ctx context.Context, topic ParsedTopic, who UploaderIdentity) (string, error)

// Authorize implements TopicAuthorizer.
func (f AuthorizerFunc) Authorize(
	ctx context.Context, topic ParsedTopic, who UploaderIdentity,
) (string, error) {
	return f(ctx, topic, who)
}

// AuthorizerRegistry maps a topic's ENTITY segment to its authorizer. Keeping
// it a registry rather than a switch is what makes the next attachment kind a
// registration instead of an edit to the upload endpoint.
type AuthorizerRegistry struct {
	byEntity map[string]TopicAuthorizer
}

// NewAuthorizerRegistry builds an empty registry.
func NewAuthorizerRegistry() *AuthorizerRegistry {
	return &AuthorizerRegistry{byEntity: make(map[string]TopicAuthorizer)}
}

// Register attaches an authorizer to an entity segment.
func (r *AuthorizerRegistry) Register(entity string, authorizer TopicAuthorizer) {
	r.byEntity[entity] = authorizer
}

// Authorize resolves the topic's entity to its authorizer and delegates. An
// unregistered entity is ErrUnknownTopicEntity — fail closed.
func (r *AuthorizerRegistry) Authorize(
	ctx context.Context, topic ParsedTopic, who UploaderIdentity,
) (string, error) {
	authorizer, ok := r.byEntity[topic.Entity]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownTopicEntity, topic.Entity)
	}

	return authorizer.Authorize(ctx, topic, who)
}
