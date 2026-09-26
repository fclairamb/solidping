// Package authhandoff hands a freshly minted session from a server-side login
// callback to the dashboard without ever putting a token in a URL
// (spec 2026-09-25-12).
//
// A federated login callback (Google, GitHub, GitLab, Microsoft, Discord,
// Slack, OIDC, SAML, the Slack app install) ends with a browser redirect. The
// session it minted used to ride along in that redirect's query string, which
// leaks it to browser history, Referer headers, proxy logs and session replay.
// Instead the callback calls Issue, redirects with the returned code, and the
// dashboard trades the code for the session once, over a POST.
//
// The code is:
//
//   - random: 32 bytes from crypto/rand, base64url (43 characters);
//   - single use: Redeem deletes the row in the same statement that reads it;
//   - short-lived: TTL (60 s), checked on redeem and swept by the
//     state-cleanup job;
//   - never stored: the row is keyed by the code's SHA-256;
//   - the only key to the payload: the session is AES-256-GCM sealed under a
//     key derived from the code with HKDF, so a database dump yields neither a
//     redeemable code nor a token.
//
// Nothing in this package logs a code, and callers must not either.
package authhandoff

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TTL is how long an issued code stays redeemable. The dashboard redeems it
// on the very first page load after the redirect, so a minute is generous.
const TTL = 60 * time.Second

const (
	// codeBytes is the unencoded code length; base64url makes it 43 chars.
	codeBytes = 32
	// maxCodeLen bounds what Redeem will even hash. A real code is 43 chars.
	maxCodeLen = 128
	// payloadKeyInfo is the HKDF context string. Versioned so a future format
	// change can never open an old payload with the wrong interpretation.
	payloadKeyInfo = "solidping auth handoff payload v1"
	payloadKeyLen  = 32
)

// ErrInvalidCode is the ONE error Redeem returns for a code that cannot be
// redeemed: unknown, already used, expired, forged or unreadable. Callers
// must answer all of them identically — telling them apart would tell an
// attacker which codes exist.
var ErrInvalidCode = errors.New("handoff code invalid, expired or already used")

// errEmptyUser guards Issue against minting a code bound to nobody.
var errEmptyUser = errors.New("authhandoff: session has no user")

// Session is what a code hands over: the tokens the callback minted plus the
// routing facts the dashboard needs to finish the login.
type Session struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresIn    int    `json:"expiresIn"`
	UserUID      string `json:"userUid"`
	// OrgSlug is the org the session is scoped to. Empty for an org-less
	// session (a login that authenticated but was not admitted).
	OrgSlug string `json:"orgSlug,omitempty"`
	// MembershipPending names the org whose admission is pending, so the
	// dashboard can say so. Not a secret; it also travels in the redirect URL.
	MembershipPending string `json:"membershipPending,omitempty"`
	// ReturnTo is where the login was started from (the OAuth state's
	// redirect_uri), or a server-chosen landing page. It is a HINT: the
	// dashboard only follows it through its own same-origin / same-org guards.
	ReturnTo string `json:"returnTo,omitempty"`
}

// Issue stores session under a new code and returns the code. The row is bound
// to the session's user and, when it has one, its organization, so deleting
// either takes outstanding codes with it.
func Issue(ctx context.Context, dbService db.Service, session *Session) (string, error) {
	if session == nil || session.UserUID == "" {
		return "", errEmptyUser
	}

	code, err := newCode()
	if err != nil {
		return "", err
	}

	codeHash := HashCode(code)

	plaintext, err := json.Marshal(session)
	if err != nil {
		return "", fmt.Errorf("authhandoff: marshal session: %w", err)
	}

	sealed, err := seal(code, codeHash, plaintext)
	if err != nil {
		return "", err
	}

	row := &models.AuthHandoffCode{
		CodeHash:  codeHash,
		UserUID:   session.UserUID,
		Payload:   sealed,
		ExpiresAt: time.Now().Add(TTL),
		CreatedAt: time.Now(),
	}

	if session.OrgSlug != "" {
		org, orgErr := dbService.GetOrganizationBySlug(ctx, session.OrgSlug)
		if orgErr != nil {
			return "", fmt.Errorf("authhandoff: resolve organization: %w", orgErr)
		}

		row.OrganizationUID = &org.UID
	}

	if err := dbService.CreateAuthHandoffCode(ctx, row); err != nil {
		return "", fmt.Errorf("authhandoff: store code: %w", err)
	}

	return code, nil
}

// Redeem consumes code and returns the session it carried. Every way a code
// can be unusable yields ErrInvalidCode; any other error is a storage failure.
func Redeem(ctx context.Context, dbService db.Service, code string) (*Session, error) {
	if code == "" || len(code) > maxCodeLen {
		return nil, ErrInvalidCode
	}

	codeHash := HashCode(code)

	row, err := dbService.ConsumeAuthHandoffCode(ctx, codeHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidCode
		}

		return nil, fmt.Errorf("authhandoff: consume code: %w", err)
	}

	// The row is gone whatever happens next: an expired or unreadable code is
	// consumed like a good one.
	if !time.Now().Before(row.ExpiresAt) {
		return nil, ErrInvalidCode
	}

	plaintext, err := open(code, row.CodeHash, row.Payload)
	if err != nil {
		return nil, ErrInvalidCode
	}

	var session Session
	if err := json.Unmarshal(plaintext, &session); err != nil {
		return nil, ErrInvalidCode
	}

	if session.UserUID == "" || session.UserUID != row.UserUID || session.AccessToken == "" {
		return nil, ErrInvalidCode
	}

	return &session, nil
}

// newCode draws a fresh code: codeBytes of crypto/rand, base64url.
func newCode() (string, error) {
	raw := make([]byte, codeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("authhandoff: generate code: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashCode is the storage key of a code: its hex SHA-256. Exported so tests
// can find a row from the code they were handed.
func HashCode(code string) string {
	sum := sha256.Sum256([]byte(code))

	return hex.EncodeToString(sum[:])
}

// payloadAEAD derives the payload cipher from the code. HKDF with its own
// context string makes the key independent of HashCode's output, which IS
// stored: knowing the hash does not help open the payload.
func payloadAEAD(code string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, []byte(code), nil, payloadKeyInfo, payloadKeyLen)
	if err != nil {
		return nil, fmt.Errorf("authhandoff: derive key: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("authhandoff: cipher: %w", err)
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("authhandoff: gcm: %w", err)
	}

	return aead, nil
}

// seal encrypts plaintext, binding it to codeHash as associated data so a
// payload cannot be moved onto another row.
func seal(code, codeHash string, plaintext []byte) (string, error) {
	aead, err := payloadAEAD(code)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("authhandoff: nonce: %w", err)
	}

	sealed := aead.Seal(nonce, nonce, plaintext, []byte(codeHash))

	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

// errShortPayload reports a stored payload too short to hold a nonce.
var errShortPayload = errors.New("authhandoff: payload too short")

// open reverses seal.
func open(code, codeHash, payload string) ([]byte, error) {
	aead, err := payloadAEAD(code)
	if err != nil {
		return nil, err
	}

	sealed, err := base64.RawStdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("authhandoff: decode payload: %w", err)
	}

	if len(sealed) < aead.NonceSize() {
		return nil, errShortPayload
	}

	nonce, ciphertext := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]

	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(codeHash))
	if err != nil {
		return nil, fmt.Errorf("authhandoff: open payload: %w", err)
	}

	return plaintext, nil
}
