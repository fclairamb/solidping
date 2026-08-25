package statussubscribers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Channel errors.
var (
	// ErrInvalidChannel is returned for a channel outside the three supported
	// values.
	ErrInvalidChannel = errors.New("invalid subscriber channel")
	// ErrPublicChannelNotAllowed is returned when a PUBLIC subscribe call asks
	// for anything but email.
	ErrPublicChannelNotAllowed = errors.New("only email subscriptions can be created publicly")
	// ErrInvalidEndpointURL is returned for a malformed or non-https endpoint.
	ErrInvalidEndpointURL = errors.New("invalid endpoint URL")
	// ErrEndpointRequired is returned when a webhook/slack subscription is
	// created without a URL.
	ErrEndpointRequired = errors.New("an endpoint URL is required for this channel")
	// ErrSlackWebhookExpected is returned when a `slack` subscription points at
	// something that is not a Slack incoming webhook.
	ErrSlackWebhookExpected = errors.New("a Slack incoming-webhook URL is required")
)

// Envelope keys inside the encrypted endpoint blob.
const (
	endpointURLKey    = "url"
	endpointSecretKey = "signingSecret"
)

// slackWebhookHost is the only host a `slack` subscription may point at.
// Anything else is either a typo or an attempt to use the Slack-shaped
// formatting against an unrelated endpoint.
const slackWebhookHost = "hooks.slack.com"

// signingSecretBytes is the size of a generated webhook signing secret before
// base64 encoding. 32 bytes = 256 bits, matching the HMAC-SHA256 block the
// servicesig scheme keys with.
const signingSecretBytes = 32

// normalizeEndpointURL validates a delivery URL and returns its canonical form.
//
// https is REQUIRED: the payload names which of the customer's services is
// down and carries an HMAC signature, and neither should cross the network in
// clear. Local/loopback hosts are refused because a status-page subscriber is
// a customer-supplied URL — treating it as a way to make the SolidPing server
// issue requests into its own network would be a plain SSRF.
func normalizeEndpointURL(raw string, allowPrivate bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrEndpointRequired
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", ErrInvalidEndpointURL
	}

	if parsed.Scheme != "https" || parsed.Host == "" {
		return "", ErrInvalidEndpointURL
	}

	host := strings.ToLower(parsed.Hostname())
	if !allowPrivate && isLocalHost(host) {
		return "", ErrInvalidEndpointURL
	}

	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""

	return parsed.String(), nil
}

// isLocalHost reports whether a hostname obviously points back at the server or
// its private network. It is a coarse first line — the HTTP client still
// resolves the name at delivery time — but it stops the trivial cases at the
// point where a human could be told why.
func isLocalHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1", "[::1]":
		return true
	}

	return strings.HasSuffix(host, ".localhost") ||
		strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "192.168.") ||
		strings.HasPrefix(host, "169.254.")
}

// validateChannelEndpoint applies the per-channel URL rules on top of the
// generic ones.
func validateChannelEndpoint(
	channel models.SubscriberChannel, raw string, allowPrivate bool,
) (string, error) {
	normalized, err := normalizeEndpointURL(raw, allowPrivate)
	if err != nil {
		return "", err
	}

	if channel == models.SubscriberChannelSlack {
		parsed, parseErr := url.Parse(normalized)
		if parseErr != nil || !strings.EqualFold(parsed.Hostname(), slackWebhookHost) {
			return "", ErrSlackWebhookExpected
		}
	}

	return normalized, nil
}

// endpointKeyFor is the dedup key stored in the live-uniqueness index: a hash,
// never the URL, so the URL itself never lands in an index (indexes end up in
// backups, query plans and error messages).
func endpointKeyFor(normalizedURL string) string {
	sum := sha256.Sum256([]byte(normalizedURL))

	return hex.EncodeToString(sum[:])
}

// maskEndpoint renders the only part of a delivery URL any response may echo:
// scheme + host + a heavily-truncated path. That is enough for an operator to
// tell two webhooks apart and useless to anyone who steals the response — the
// path segment IS the credential in an incoming-webhook URL.
func maskEndpoint(normalizedURL string) string {
	parsed, err := url.Parse(normalizedURL)
	if err != nil {
		return "https://…"
	}

	path := strings.Trim(parsed.Path, "/")
	if path == "" {
		return parsed.Scheme + "://" + parsed.Host
	}

	segments := strings.Split(path, "/")
	last := segments[len(segments)-1]

	// Show at most the first four characters of the final segment. Four is
	// enough to disambiguate a handful of webhooks in a table and far too few
	// to be worth guessing the rest from.
	const shown = 4
	if len(last) > shown {
		last = last[:shown]
	}

	return parsed.Scheme + "://" + parsed.Host + "/…/" + last + "…"
}

// generateSigningSecret mints a per-subscriber webhook signing secret.
func generateSigningSecret() (string, error) {
	buf := make([]byte, signingSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// endpointSecrets is the decrypted content of a subscriber's endpoint envelope.
type endpointSecrets struct {
	URL           string
	SigningSecret string
}

// sealEndpoint encrypts the delivery URL and signing secret for storage.
//
// When no encryption service is configured (the documented self-hosted V1
// fallback, same as checks.config), it falls back to the PLAINTEXT envelope
// format rather than to a bare string: the column then still holds a
// self-describing envelope, so a later `solidping` credential migration can
// find and upgrade it instead of having to guess what the column contains.
func sealEndpoint(
	ctx context.Context, creds credentials.Service, orgUID string, secrets endpointSecrets,
) (string, error) {
	payload := map[string]any{
		endpointURLKey:    secrets.URL,
		endpointSecretKey: secrets.SigningSecret,
	}

	if creds == nil || !creds.Enabled() {
		return credentials.SealPlaintext(payload)
	}

	return creds.EncryptForOrg(ctx, orgUID, payload)
}

// openEndpoint decrypts a stored endpoint envelope.
func openEndpoint(
	ctx context.Context, creds credentials.Service, orgUID, envelope string,
) (endpointSecrets, error) {
	if envelope == "" {
		return endpointSecrets{}, ErrEndpointRequired
	}

	var (
		payload map[string]any
		err     error
	)

	switch {
	case credentials.IsPlaintextEnvelope(envelope):
		payload, err = credentials.OpenPlaintext(envelope)
	case creds == nil:
		// An encrypted envelope with no key on this process. Failing loudly is
		// the only honest option: silently skipping the delivery would look
		// exactly like "the endpoint is fine, nothing happened".
		return endpointSecrets{}, fmt.Errorf(
			"%w: endpoint is encrypted but no encryption key is configured on this process",
			ErrInvalidEndpointURL)
	default:
		payload, err = creds.DecryptForOrg(ctx, orgUID, envelope)
	}

	if err != nil {
		return endpointSecrets{}, fmt.Errorf("open endpoint envelope: %w", err)
	}

	out := endpointSecrets{}
	if raw, ok := payload[endpointURLKey].(string); ok {
		out.URL = raw
	}

	if raw, ok := payload[endpointSecretKey].(string); ok {
		out.SigningSecret = raw
	}

	if out.URL == "" {
		return endpointSecrets{}, ErrEndpointRequired
	}

	return out, nil
}

// --- Operator-side endpoint subscriptions ---

// CreateEndpointSubscriber registers a webhook or Slack delivery for a status
// page. AUTHENTICATED and operator-only by construction: this method is only
// reachable from the org-scoped route, never from the public subscribe path.
//
// Unlike an email subscription there is no double opt-in — the operator
// registering their own webhook on their own page IS the verification — so the
// row is created already confirmed and starts delivering on the next update.
func (s *Service) CreateEndpointSubscriber(
	ctx context.Context, orgSlug, statusPageUID string, req *CreateEndpointSubscriberRequest,
) (*CreateEndpointSubscriberResponse, error) {
	channel := models.SubscriberChannel(strings.TrimSpace(req.Channel))
	if !channel.IsEndpoint() {
		return nil, ErrInvalidChannel
	}

	scope, incidentUID, err := resolveScope(req.Scope, req.IncidentUID)
	if err != nil {
		return nil, err
	}

	normalized, err := validateChannelEndpoint(channel, req.URL, s.allowPrivateEndpoints)
	if err != nil {
		return nil, err
	}

	org, page, err := s.resolveOrgAndPage(ctx, orgSlug, statusPageUID, incidentUID)
	if err != nil {
		return nil, err
	}

	envelope, secret, err := s.sealFor(ctx, org.UID, normalized, req.SigningSecret)
	if err != nil {
		return nil, err
	}

	sub := models.NewEndpointSubscriber(org.UID, page.UID, channel, scope)
	sub.IncidentUID = incidentUID
	sub.EndpointPrivate = &envelope
	sub.EndpointKey = strPtr(endpointKeyFor(normalized))
	sub.EndpointHint = strPtr(maskEndpoint(normalized))

	// The two token columns are NOT NULL and globally unique. An endpoint
	// subscription has no confirm or unsubscribe link — there is no mailbox to
	// send one to — so it gets unguessable random values it will simply never
	// use, rather than a sentinel that would collide with the next one.
	if sub.ConfirmToken, err = generateToken(); err != nil {
		return nil, err
	}

	if sub.UnsubscribeToken, err = generateToken(); err != nil {
		return nil, err
	}

	if createErr := s.db.CreateSubscriber(ctx, sub); createErr != nil {
		return nil, createErr
	}

	return &CreateEndpointSubscriberResponse{
		SubscriberResponse: SubscriberResponse{
			UID:       sub.UID,
			Channel:   string(sub.Channel),
			Endpoint:  sub.EndpointHint,
			Scope:     string(sub.Scope),
			Confirmed: true,
			CreatedAt: sub.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		},
		SigningSecret: secret,
	}, nil
}

// resolveOrgAndPage resolves the org and page for an endpoint subscription and
// checks the referenced incident when the scope needs one.
func (s *Service) resolveOrgAndPage(
	ctx context.Context, orgSlug, statusPageUID string, incidentUID *string,
) (*models.Organization, *models.StatusPage, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, nil, ErrOrganizationNotFound
	}

	page, err := s.db.GetStatusPage(ctx, org.UID, statusPageUID)
	if err != nil || page == nil {
		return nil, nil, ErrStatusPageNotFound
	}

	if incidentUID != nil {
		if _, incErr := s.db.GetIncident(ctx, org.UID, *incidentUID); incErr != nil {
			return nil, nil, ErrIncidentNotFound
		}
	}

	return org, page, nil
}

// sealFor generates the signing secret when the caller supplied none and
// returns the encrypted envelope to store PLUS the plaintext secret, which the
// caller shows the operator exactly once. Returning it is the whole point: a
// signature nobody can verify is decoration.
func (s *Service) sealFor(
	ctx context.Context, orgUID, normalizedURL string, suppliedSecret *string,
) (string, string, error) {
	secret := ""
	if suppliedSecret != nil {
		secret = strings.TrimSpace(*suppliedSecret)
	}

	if secret == "" {
		generated, err := generateSigningSecret()
		if err != nil {
			return "", "", err
		}

		secret = generated
	}

	envelope, err := sealEndpoint(ctx, s.creds, orgUID,
		endpointSecrets{URL: normalizedURL, SigningSecret: secret})
	if err != nil {
		return "", "", fmt.Errorf("seal endpoint: %w", err)
	}

	return envelope, secret, nil
}

// resolveScope validates the optional scope/incident pair shared by the public
// and operator subscription paths.
func resolveScope(rawScope, incidentUID *string) (models.SubscriberScope, *string, error) {
	scope := models.SubscriberScopePage
	if rawScope != nil && *rawScope != "" {
		scope = models.SubscriberScope(*rawScope)
		if !scope.IsValid() {
			return "", nil, ErrInvalidScope
		}
	}

	if scope != models.SubscriberScopeIncident {
		return scope, nil, nil
	}

	if incidentUID == nil || *incidentUID == "" {
		return "", nil, ErrIncidentRequired
	}

	return scope, incidentUID, nil
}

func strPtr(s string) *string { return &s }
