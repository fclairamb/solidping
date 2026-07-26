package usernotifications

import (
	"context"
	crypto_rand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/twilio"
	"github.com/fclairamb/solidping/server/internal/integrations/twilioconn"
)

// Phone-verification tuning.
const (
	// verifyCodeTTL is how long an issued code stays valid.
	verifyCodeTTL = 10 * time.Minute
	// verifyMaxAttempts is the failed-confirm cap before a code is invalidated.
	verifyMaxAttempts = 5
	// verifyResendWindow / verifyResendMax rate-limit code issuance per contact.
	verifyResendWindow = time.Hour
	verifyResendMax    = 3
)

// Verification errors.
var (
	// ErrNotAPhoneContact is returned when verification is requested for a
	// non-phone contact type.
	ErrNotAPhoneContact = errors.New("only phone contacts can be verified")
	// ErrAlreadyVerified is returned when a contact is already verified.
	ErrAlreadyVerified = errors.New("contact is already verified")
	// ErrNoProvider is returned when the org has no Twilio connection to send
	// the code through.
	ErrNoProvider = errors.New("no SMS provider configured for this organization")
	// ErrResendTooSoon is returned when the per-contact resend rate limit is hit.
	ErrResendTooSoon = errors.New("verification code was requested too recently")
	// ErrNoPendingCode is returned when confirm is called without an in-flight code.
	ErrNoPendingCode = errors.New("no verification code pending; request one first")
	// ErrCodeExpired is returned when the pending code has expired.
	ErrCodeExpired = errors.New("verification code has expired")
	// ErrTooManyAttempts is returned when the attempt cap is exceeded.
	ErrTooManyAttempts = errors.New("too many incorrect attempts; request a new code")
	// ErrCodeMismatch is returned when the submitted code is wrong.
	ErrCodeMismatch = errors.New("incorrect verification code")
)

// resendTracker rate-limits code issuance per contact in-memory. A restart
// resets it (acceptable — the DB attempt cap and TTL remain the durable
// controls; this only throttles resends).
//
//nolint:gochecknoglobals // process-wide per-contact resend throttle.
var resendTracker = newResendLimiter()

// newTwilioClient is the client constructor seam used when sending a
// verification code. Overridden in tests to target an httptest fake.
//
//nolint:gochecknoglobals // test seam for the outbound Twilio client.
var newTwilioClient = twilio.NewClient

// VerifyContact issues a fresh 6-digit code for a phone contact and sends it
// via the org's default Twilio connection.
func (s *Service) VerifyContact(
	ctx context.Context, orgSlug string, user *models.User, contactUID string,
) error {
	orgUID, err := s.resolveOrgUID(ctx, orgSlug)
	if err != nil {
		return err
	}

	contact, err := s.loadOwnedContact(ctx, orgUID, user, contactUID)
	if err != nil {
		return err
	}

	if contact.Type != models.UserContactTypePhone {
		return ErrNotAPhoneContact
	}

	if contact.VerifiedAt != nil {
		return ErrAlreadyVerified
	}

	if !resendTracker.allow(contactUID, s.now()) {
		return ErrResendTooSoon
	}

	// Resolve the provider first so we don't stamp a code we can't deliver.
	_, settings, err := twilioconn.ResolveDefault(ctx, s.db, s.creds, orgUID)
	if err != nil {
		if errors.Is(err, twilioconn.ErrNoTwilioConnection) {
			return ErrNoProvider
		}

		return fmt.Errorf("resolve twilio connection: %w", err)
	}

	code, err := generateCode()
	if err != nil {
		return fmt.Errorf("generate verification code: %w", err)
	}

	hash := hashCode(code)
	expires := s.now().Add(verifyCodeTTL)
	if setErr := s.db.SetUserContactVerifyState(ctx, contactUID, &hash, &expires, 0); setErr != nil {
		return fmt.Errorf("store verification code: %w", setErr)
	}

	if sendErr := sendVerificationSMS(ctx, settings, contact.Value, code); sendErr != nil {
		return fmt.Errorf("send verification code: %w", sendErr)
	}

	return nil
}

// ConfirmVerify checks a submitted code and, on success, marks the contact
// verified. Enforces expiry, the attempt cap, and a constant-time compare.
func (s *Service) ConfirmVerify(
	ctx context.Context, orgSlug string, user *models.User, contactUID, code string,
) error {
	orgUID, err := s.resolveOrgUID(ctx, orgSlug)
	if err != nil {
		return err
	}

	contact, err := s.loadOwnedContact(ctx, orgUID, user, contactUID)
	if err != nil {
		return err
	}

	if contact.Type != models.UserContactTypePhone {
		return ErrNotAPhoneContact
	}

	if contact.VerifiedAt != nil {
		return nil // already verified — idempotent success
	}

	if contact.VerifyCodeHash == nil || *contact.VerifyCodeHash == "" {
		return ErrNoPendingCode
	}

	if contact.VerifyExpiresAt == nil || s.now().After(*contact.VerifyExpiresAt) {
		return ErrCodeExpired
	}

	if contact.VerifyAttempts >= verifyMaxAttempts {
		// Invalidate the burnt code so a fresh one must be requested.
		_ = s.db.SetUserContactVerifyState(ctx, contactUID, nil, nil, contact.VerifyAttempts)

		return ErrTooManyAttempts
	}

	if subtle.ConstantTimeCompare([]byte(hashCode(code)), []byte(*contact.VerifyCodeHash)) != 1 {
		attempts := contact.VerifyAttempts + 1
		if attempts >= verifyMaxAttempts {
			// Cap reached on this miss — clear the code.
			_ = s.db.SetUserContactVerifyState(ctx, contactUID, nil, nil, attempts)
		} else {
			_ = s.db.SetUserContactVerifyState(ctx, contactUID, contact.VerifyCodeHash, contact.VerifyExpiresAt, attempts)
		}

		return ErrCodeMismatch
	}

	if markErr := s.db.MarkUserContactVerified(ctx, contactUID, s.now()); markErr != nil {
		return fmt.Errorf("mark verified: %w", markErr)
	}

	return nil
}

// loadOwnedContact fetches a contact and enforces that it belongs to the
// requesting user and org.
func (s *Service) loadOwnedContact(
	ctx context.Context, orgUID string, user *models.User, contactUID string,
) (*models.UserContact, error) {
	contact, err := s.db.GetUserContact(ctx, contactUID)
	if err != nil || contact == nil {
		return nil, ErrContactNotFound
	}

	if contact.UserUID != user.UID || contact.OrganizationUID != orgUID {
		return nil, ErrContactNotFound
	}

	return contact, nil
}

// generateCode returns a zero-padded 6-digit numeric code from crypto/rand.
func generateCode() (string, error) {
	n, err := crypto_rand.Int(crypto_rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%06d", n.Int64()), nil
}

// hashCode returns the SHA-256 hex of a code, matching the storage column.
func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))

	return hex.EncodeToString(sum[:])
}

// sendVerificationSMS sends the code through the resolved Twilio connection.
func sendVerificationSMS(
	ctx context.Context, settings *models.TwilioSettings, toNumber, code string,
) error {
	if settings.AccountSID == "" || settings.AuthToken == "" {
		return ErrNoProvider
	}

	client := newTwilioClient(settings.AccountSID, settings.AuthToken)
	_, err := client.SendSMS(ctx, &twilio.SendSMSParams{
		To:                  toNumber,
		From:                settings.FromNumber,
		MessagingServiceSID: settings.MessagingServiceSID,
		Body:                fmt.Sprintf("[SolidPing] Your verification code is %s (valid 10 minutes).", code),
	})

	return err
}
