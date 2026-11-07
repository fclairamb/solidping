package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/i18n/recipient"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// Re-authentication before adding a sign-in factor (spec 2026-10-31-02).
//
// A signed-in session alone must not be able to add a TOTP authenticator, a
// passkey, or a first password: a hijacked session would otherwise lock the
// owner out (TOTP) or keep a way in after every revocation (passkey,
// password). Every route that adds such a factor calls
// reauthenticateForFactor first.

// ErrReauthenticationRequired is returned when an account without a password
// asks to add a sign-in factor from a session whose primary sign-in is older
// than reauthSignInWindow (or unknown). The user must sign in again.
var ErrReauthenticationRequired = errors.New("a recent sign-in is required for this action")

// ErrInvalidSetupToken is returned by Confirm2FA when the setup token is
// missing, expired, for another user, or for another secret.
var ErrInvalidSetupToken = errors.New("2FA setup token is invalid or expired")

const (
	// reauthSignInWindow is how recent the primary sign-in of a passwordless
	// account must be for it to add a sign-in factor.
	reauthSignInWindow = 10 * time.Minute

	// keySignedInAt is the refresh-row property holding the time (Unix
	// seconds) of the primary sign-in that opened the session. Refresh copies
	// it into every access token it mints, so the session keeps the time of
	// the sign-in, never the time of the refresh.
	keySignedInAt = "signed_in_at"

	// totpSetupPurpose signs the token Setup2FA hands out and Confirm2FA
	// requires: the proof that the secret came from a re-authenticated setup.
	totpSetupPurpose     = "totp-setup"
	totpSetupTokenExpiry = 10 * time.Minute
)

// callerAuthTime is the primary sign-in time carried by the request's
// session, or 0 when there is none.
func callerAuthTime(ctx context.Context) int64 {
	if claims, ok := ctx.Value(base.ContextKeyClaims).(*Claims); ok && claims != nil {
		return claims.AuthTime
	}

	return 0
}

// stampSignedInAt records the primary sign-in time on a refresh row about to
// be persisted. A zero time records nothing.
func stampSignedInAt(row *models.UserToken, authTime int64) {
	if authTime <= 0 {
		return
	}

	if row.Properties == nil {
		row.Properties = models.JSONMap{}
	}

	row.Properties[keySignedInAt] = authTime
}

// signedInAtOf reads the primary sign-in time of a refresh row, 0 when absent
// (rows created before the property existed).
func signedInAtOf(row *models.UserToken) int64 {
	if row == nil {
		return 0
	}

	switch value := row.Properties[keySignedInAt].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		n, err := value.Int64()
		if err != nil {
			return 0
		}

		return n
	default:
		return 0
	}
}

// requireRecentSignIn accepts the request only when its session comes from a
// primary sign-in within reauthSignInWindow.
func requireRecentSignIn(ctx context.Context) error {
	authTime := callerAuthTime(ctx)
	if authTime <= 0 {
		return ErrReauthenticationRequired
	}

	signedInAt := time.Unix(authTime, 0)
	if time.Since(signedInAt) > reauthSignInWindow || signedInAt.After(time.Now().Add(time.Minute)) {
		return ErrReauthenticationRequired
	}

	return nil
}

// userHasPassword reports whether the account can sign in with a password.
func userHasPassword(user *models.User) bool {
	return user.PasswordHash != nil && *user.PasswordHash != ""
}

// verifyCurrentPassword checks the current password of an account that has
// one. The check is a brute-force surface, so every attempt bumps the shared
// change-password counter first, and the cap answers ErrRateLimited.
func (s *Service) verifyCurrentPassword(ctx context.Context, user *models.User, currentPassword *string) error {
	limited, err := s.bumpChangePasswordCounter(ctx, user.UID)
	if err != nil {
		return fmt.Errorf("failed to bump change-password counter: %w", err)
	}

	if limited {
		return ErrRateLimited
	}

	given := ""
	if currentPassword != nil {
		given = *currentPassword
	}

	// Always run the verify, even for an empty password, so "missing" and
	// "wrong" cost the same time.
	if !passwords.Verify(given, *user.PasswordHash) {
		return ErrInvalidCurrentPassword
	}

	return nil
}

// reauthenticateForFactor is the proof every route that adds or replaces a
// sign-in factor asks for: the current password when the account has one,
// otherwise a primary sign-in within reauthSignInWindow.
func (s *Service) reauthenticateForFactor(ctx context.Context, user *models.User, currentPassword *string) error {
	if userHasPassword(user) {
		return s.verifyCurrentPassword(ctx, user, currentPassword)
	}

	return requireRecentSignIn(ctx)
}

// ReauthenticateForFactor is reauthenticateForFactor for the routes outside
// this package that add a sign-in factor: linking a Discord account from a
// session (spec 2026-11-01-03) binds an identity that signs in. Its errors are
// the ones WriteReauthError answers.
func (s *Service) ReauthenticateForFactor(ctx context.Context, user *models.User, currentPassword *string) error {
	return s.reauthenticateForFactor(ctx, user, currentPassword)
}

// totpSetupClaims is the body of the token binding Confirm2FA to the secret a
// re-authenticated Setup2FA handed out.
type totpSetupClaims struct {
	UserUID    string `json:"userUid"`
	SecretHash string `json:"secretHash"`
	Purpose    string `json:"purpose"`
	jwt.RegisteredClaims
}

func hashTOTPSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))

	return hex.EncodeToString(sum[:])
}

func (s *Service) generateTOTPSetupToken(userUID, secret string) (string, error) {
	now := time.Now()

	return s.signJWT(totpSetupPurpose, &totpSetupClaims{
		UserUID:    userUID,
		SecretHash: hashTOTPSecret(secret),
		Purpose:    totpSetupPurpose,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(totpSetupTokenExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    jwtIssuer,
			ID:        uuid.New().String(),
		},
	})
}

// checkTOTPSetupToken accepts a setup token minted for this user and this
// pending secret only.
func (s *Service) checkTOTPSetupToken(setupToken, userUID, secret string) error {
	if setupToken == "" {
		return ErrInvalidSetupToken
	}

	token, err := s.parsePurposeJWT(setupToken, totpSetupPurpose, &totpSetupClaims{})
	if err != nil || !token.Valid {
		return ErrInvalidSetupToken
	}

	claims, ok := token.Claims.(*totpSetupClaims)
	if !ok || claims.Purpose != totpSetupPurpose || claims.UserUID != userUID {
		return ErrInvalidSetupToken
	}

	if subtle.ConstantTimeCompare([]byte(claims.SecretHash), []byte(hashTOTPSecret(secret))) != 1 {
		return ErrInvalidSetupToken
	}

	return nil
}

// notifyTOTPEnabled emails the account owner that two-factor authentication
// was turned on, like notifyPasskeyAdded: a TOTP enabled behind the owner's
// back locks them out, so it must not go unnoticed. Best effort.
func (s *Service) notifyTOTPEnabled(ctx context.Context, user *models.User) {
	dashboardURL := strings.TrimRight(s.fullCfg.Server.BaseURL, "/") + config.DashboardBasePath
	securityURL := dashboardURL + "/"

	if claims, ok := ctx.Value(base.ContextKeyClaims).(*Claims); ok && claims.OrgSlug != "" {
		securityURL = fmt.Sprintf("%s/orgs/%s/account/security", dashboardURL, claims.OrgSlug)
	}

	s.enqueueEmail(ctx, "", user.Email, email.TemplateTOTPEnabled, recipient.ForUser(user),
		map[string]any{
			"EnabledAt":    time.Now().UTC(),
			keySecurityURL: securityURL,
		},
	)
}

// WriteReauthError answers the errors of reauthenticateForFactor. It reports
// false when err is not one of them, so the caller falls through to its own
// mapping.
func WriteReauthError(
	handler *base.HandlerBase, writer http.ResponseWriter, request *http.Request, err error,
) (bool, error) {
	switch {
	case errors.Is(err, ErrInvalidCurrentPassword):
		return true, handler.WriteErrorErr(writer, request, http.StatusUnauthorized, base.ErrorCodeInvalidCurrentPassword,
			"Current password is incorrect", err)
	case errors.Is(err, ErrReauthenticationRequired):
		return true, handler.WriteErrorErr(writer, request, http.StatusForbidden, base.ErrorCodeReauthenticationRequired,
			"Sign in again to make this change", err)
	case errors.Is(err, ErrRateLimited):
		return true, handler.WriteErrorErr(writer, request, http.StatusTooManyRequests, base.ErrorCodeRateLimited,
			"Too many attempts, please try again later", err)
	default:
		return false, nil
	}
}
