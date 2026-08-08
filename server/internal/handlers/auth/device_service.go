package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// OAuth 2.0 Device Authorization Grant (RFC 8628) parameters.
//
// The wire format of the device endpoints is snake_case (device_code,
// user_code, verification_uri, expires_in, interval, and the RFC 6749 §5.2
// error codes) rather than solidping's house camelCase: these are the
// interoperable OAuth contract, and every off-the-shelf device-flow client
// expects the RFC spelling. The deviation is deliberate and confined to the
// four /auth/device* endpoints.
const (
	// DeviceAuthTTL bounds how long a device-authorization request stays
	// usable — the human has this long to walk to a browser and approve.
	DeviceAuthTTL = 15 * time.Minute
	// DevicePollIntervalSeconds is the minimum spacing the client must keep
	// between polls (RFC 8628 `interval`). Polling faster earns slow_down.
	DevicePollIntervalSeconds = 5
	// DevicePATExpiry is the lifetime of the PAT minted at consent time. It
	// matches the dashboard's default token expiry (90 days).
	DevicePATExpiry = 90 * 24 * time.Hour

	// deviceUserCodeLength is the length of the canonical (dashless) user code.
	deviceUserCodeLength = 8
	// deviceUserCodeAttempts bounds the collision-retry loop on user codes.
	deviceUserCodeAttempts = 5
	// deviceCodeBytes is the entropy behind the device code — the real
	// capability, never displayed in a browser.
	deviceCodeBytes = 32
	// deviceClientNameMaxLength caps the consent-page label.
	deviceClientNameMaxLength = 200
	// deviceDefaultClientName labels a request that sent no client name.
	deviceDefaultClientName = "Unknown application"
	// deviceVerificationPath is the dashboard route serving the consent page.
	deviceVerificationPath = "/dash0/device"
	// devicePollGrace absorbs network/scheduler jitter so a client polling at
	// exactly `interval` is never punished with slow_down.
	devicePollGrace = 500 * time.Millisecond

	// DeviceGrantType is the RFC 8628 grant_type the token endpoint accepts.
	// Not a credential despite the linter's guess: it is an RFC 8628 URN.
	DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	// deviceUserCodeAlphabet excludes visually ambiguous characters (0/O,
	// 1/I/L) so the code can be read aloud or retyped without confusion.
	deviceUserCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

// Device authorization errors. The four *Device* sentinels below map 1:1 onto
// the RFC 8628 §3.5 token-endpoint error codes.
var (
	// ErrDeviceAuthorizationPending means the human has not decided yet.
	ErrDeviceAuthorizationPending = errors.New("authorization_pending")
	// ErrDeviceSlowDown means the client polled faster than the advertised
	// interval.
	ErrDeviceSlowDown = errors.New("slow_down")
	// ErrDeviceExpiredToken means the device code is unknown, expired, or
	// already consumed — the three are deliberately indistinguishable.
	ErrDeviceExpiredToken = errors.New("expired_token")
	// ErrDeviceAccessDenied means the human denied the request.
	ErrDeviceAccessDenied = errors.New("access_denied")
	// ErrDeviceAuthNotFound means no live request carries that user code.
	ErrDeviceAuthNotFound = errors.New("device authorization request not found or expired")
	// ErrDeviceAuthAlreadyResolved means the request was already approved or
	// denied — consent is single-shot.
	ErrDeviceAuthAlreadyResolved = errors.New("device authorization request already responded to")
	// ErrDeviceClientNameTooLong rejects an oversized consent-page label.
	ErrDeviceClientNameTooLong = errors.New("client_name is too long")
	// errDeviceUserCodesExhausted means user-code generation kept colliding
	// with live requests — practically impossible, so it is a 500.
	errDeviceUserCodesExhausted = errors.New("exhausted user code attempts")
)

// DeviceAuthorizationResponse is the RFC 8628 §3.2 device authorization
// response. Field names are the RFC's, not the house camelCase style.
//
//nolint:tagliatelle // RFC 8628 wire format requires snake_case field names.
type DeviceAuthorizationResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// DeviceConsentInfo is the public detail of a device-authorization request,
// safe to render on the (authenticated) consent page. It never carries the
// device code or the minted token.
type DeviceConsentInfo struct {
	ClientName string    `json:"clientName"`
	UserCode   string    `json:"userCode"`
	Status     string    `json:"status"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// StartDeviceAuthorization opens a new device-authorization request. It is
// unauthenticated: anyone may ask for a request to be opened, but it grants
// nothing until a logged-in user approves it and it is bound to the org that
// user selects.
func (s *Service) StartDeviceAuthorization(
	ctx context.Context, clientName string,
) (*DeviceAuthorizationResponse, error) {
	clientName = strings.TrimSpace(clientName)
	if clientName == "" {
		clientName = deviceDefaultClientName
	}

	if len(clientName) > deviceClientNameMaxLength {
		return nil, ErrDeviceClientNameTooLong
	}

	// Opportunistic sweep: requests whose human never showed up would
	// otherwise sit on their (unique) user codes until something else cleaned
	// up. Failures here must not fail the request.
	if _, err := s.db.PurgeExpiredDeviceAuthRequests(ctx, time.Now()); err != nil {
		slog.WarnContext(ctx, "failed to purge expired device auth requests", "error", err)
	}

	expiresAt := time.Now().Add(DeviceAuthTTL)

	var created *models.DeviceAuthRequest

	for range deviceUserCodeAttempts {
		deviceCode, err := generateDeviceCode()
		if err != nil {
			return nil, err
		}

		userCode, err := generateDeviceUserCode()
		if err != nil {
			return nil, err
		}

		req := models.NewDeviceAuthRequest(clientName, deviceCode, userCode, expiresAt)
		if createErr := s.db.CreateDeviceAuthRequest(ctx, req); createErr != nil {
			// Assume a user-code collision and retry with a fresh pair; a
			// genuine storage failure loses on the last attempt below.
			slog.DebugContext(ctx, "device auth request insert failed, retrying", "error", createErr)

			continue
		}

		created = req

		break
	}

	if created == nil {
		return nil, errDeviceUserCodesExhausted
	}

	displayCode := FormatDeviceUserCode(created.UserCode)
	verificationURI := strings.TrimRight(s.fullCfg.Server.BaseURL, "/") + deviceVerificationPath

	return &DeviceAuthorizationResponse{
		DeviceCode:              created.DeviceCode,
		UserCode:                displayCode,
		VerificationURI:         verificationURI,
		VerificationURIComplete: verificationURI + "?user_code=" + displayCode,
		ExpiresIn:               int(time.Until(created.ExpiresAt).Seconds()),
		Interval:                DevicePollIntervalSeconds,
	}, nil
}

// GetDeviceConsent resolves a live request by user code for the consent page.
// The user code is the brute-forceable surface, so this lookup is both
// authenticated and rate limited at the route level.
func (s *Service) GetDeviceConsent(ctx context.Context, rawUserCode string) (*DeviceConsentInfo, error) {
	userCode := NormalizeDeviceUserCode(rawUserCode)
	if userCode == "" {
		return nil, ErrDeviceAuthNotFound
	}

	req, err := s.db.GetDeviceAuthRequestByUserCode(ctx, userCode)
	if err != nil {
		return nil, ErrDeviceAuthNotFound
	}

	return &DeviceConsentInfo{
		ClientName: req.ClientName,
		UserCode:   FormatDeviceUserCode(req.UserCode),
		Status:     req.Status,
		ExpiresAt:  req.ExpiresAt,
	}, nil
}

// RespondToDeviceConsent approves or denies a pending request on behalf of the
// authenticated user.
//
// On approval it mints a named PAT scoped to orgSlug — the org the user
// SELECTED on the consent page, which is why the caller passes it explicitly
// instead of it being read off the session. The compare-and-set that publishes
// the decision is the authority: if it loses (concurrent decision, expiry), the
// just-minted PAT is revoked again so a lost race can never leave a usable
// token behind.
func (s *Service) RespondToDeviceConsent(
	ctx context.Context, claims *Claims, rawUserCode, orgSlug string, approve bool,
) error {
	userCode := NormalizeDeviceUserCode(rawUserCode)
	if userCode == "" {
		return ErrDeviceAuthNotFound
	}

	req, err := s.db.GetDeviceAuthRequestByUserCode(ctx, userCode)
	if err != nil {
		return ErrDeviceAuthNotFound
	}

	if req.Status != models.DeviceAuthStatusPending {
		return ErrDeviceAuthAlreadyResolved
	}

	if !approve {
		return s.publishDeviceDecision(ctx, req.UID, &models.DeviceAuthResolution{
			Status:  models.DeviceAuthStatusDenied,
			UserUID: claims.UserUID,
		}, nil)
	}

	if strings.TrimSpace(orgSlug) == "" {
		orgSlug = claims.OrgSlug
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return ErrOrganizationNotFound
	}

	expiresAt := time.Now().Add(DevicePATExpiry)

	// CreatePAT enforces membership in orgSlug (super admins bypass), so a
	// user cannot mint a token for an org they do not belong to by posting a
	// different slug.
	pat, err := s.CreatePAT(ctx, orgSlug, claims.UserUID, CreateTokenRequest{
		Name:      req.ClientName,
		ExpiresAt: &expiresAt,
	})
	if err != nil {
		return err
	}

	return s.publishDeviceDecision(ctx, req.UID, &models.DeviceAuthResolution{
		Status:          models.DeviceAuthStatusApproved,
		UserUID:         claims.UserUID,
		OrganizationUID: org.UID,
		TokenUID:        pat.UID,
		TokenValue:      pat.Token,
	}, pat)
}

// publishDeviceDecision runs the compare-and-set and, when it loses after a PAT
// was already minted, revokes that PAT.
func (s *Service) publishDeviceDecision(
	ctx context.Context, uid string, res *models.DeviceAuthResolution, pat *CreateTokenResponse,
) error {
	won, err := s.db.ResolveDeviceAuthRequest(ctx, uid, res)
	if err != nil {
		return err
	}

	if !won {
		if pat != nil {
			if _, delErr := s.db.DeleteUserToken(ctx, pat.UID); delErr != nil {
				slog.ErrorContext(ctx, "failed to revoke PAT after losing device consent race",
					"error", delErr, "tokenUid", pat.UID)
			}
		}

		return ErrDeviceAuthAlreadyResolved
	}

	return nil
}

// PollDeviceToken is the RFC 8628 §3.4 token endpoint body. It returns the
// minted PAT once, and an error sentinel mapping to an OAuth error code
// otherwise.
//
// Delivery is exactly-once: the row is hard-deleted as the gate, and only the
// caller whose delete affected a row receives the token. A poll that arrives
// after delivery therefore looks exactly like an unknown device code
// (expired_token), which is also what a replayed device code should look like.
func (s *Service) PollDeviceToken(ctx context.Context, deviceCode string) (string, error) {
	if strings.TrimSpace(deviceCode) == "" {
		return "", ErrDeviceExpiredToken
	}

	req, err := s.db.GetDeviceAuthRequestByDeviceCode(ctx, deviceCode)
	if err != nil {
		return "", ErrDeviceExpiredToken
	}

	// Interval enforcement (slow_down). Applied before the status switch so an
	// impatient client is told to back off whatever the request's state is.
	now := time.Now()
	minSpacing := DevicePollIntervalSeconds*time.Second - devicePollGrace

	if req.LastPolledAt != nil && now.Sub(*req.LastPolledAt) < minSpacing {
		return "", ErrDeviceSlowDown
	}

	if touchErr := s.db.TouchDeviceAuthPoll(ctx, req.UID, now); touchErr != nil {
		slog.WarnContext(ctx, "failed to record device auth poll", "error", touchErr)
	}

	switch req.Status {
	case models.DeviceAuthStatusPending:
		return "", ErrDeviceAuthorizationPending
	case models.DeviceAuthStatusDenied:
		// Consume so a denied request stops answering; ignore the outcome —
		// either way the caller is told access_denied.
		if _, delErr := s.db.ConsumeDeviceAuthRequest(ctx, req.UID); delErr != nil {
			slog.WarnContext(ctx, "failed to consume denied device auth request", "error", delErr)
		}

		return "", ErrDeviceAccessDenied
	default:
		consumed, delErr := s.db.ConsumeDeviceAuthRequest(ctx, req.UID)
		if delErr != nil {
			return "", delErr
		}

		if !consumed || req.TokenValue == nil {
			// Another poll already took it: never hand out the same token twice.
			return "", ErrDeviceExpiredToken
		}

		return *req.TokenValue, nil
	}
}

// generateDeviceCode returns the client's secret device code as hex.
func generateDeviceCode() (string, error) {
	buf := make([]byte, deviceCodeBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate device code: %w", err)
	}

	return hex.EncodeToString(buf), nil
}

// generateDeviceUserCode produces an 8-character canonical (dashless,
// uppercase) user code. The display form (WDJP-4KXR) comes from
// FormatDeviceUserCode.
func generateDeviceUserCode() (string, error) {
	raw := make([]byte, deviceUserCodeLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate user code: %w", err)
	}

	code := make([]byte, deviceUserCodeLength)
	for i, v := range raw {
		code[i] = deviceUserCodeAlphabet[int(v)%len(deviceUserCodeAlphabet)]
	}

	return string(code), nil
}

// FormatDeviceUserCode inserts a dash at the midpoint of a canonical user code
// for display (WDJP4KXR -> WDJP-4KXR).
func FormatDeviceUserCode(canonical string) string {
	if len(canonical) != deviceUserCodeLength {
		return canonical
	}

	half := deviceUserCodeLength / 2

	return canonical[:half] + "-" + canonical[half:]
}

// NormalizeDeviceUserCode canonicalizes a user-entered code: uppercased, with
// every character outside the alphabet (dashes, spaces, …) stripped. So
// "wdjp-4kxr", "WDJP 4KXR" and "WDJP4KXR" all resolve to the same stored code.
func NormalizeDeviceUserCode(input string) string {
	upper := strings.ToUpper(strings.TrimSpace(input))

	var builder strings.Builder

	for _, char := range upper {
		if strings.ContainsRune(deviceUserCodeAlphabet, char) {
			builder.WriteRune(char)
		}
	}

	return builder.String()
}
