package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Errors specific to the passkey path.
var (
	// ErrPasskeyNotFound — the credential ID has no row, or the row was
	// soft-deleted.
	ErrPasskeyNotFound = errors.New("passkey not found")
	// ErrPasskeyVerificationFailed — the WebAuthn assertion did not
	// validate (bad signature, RP-ID mismatch, sign-count regression).
	ErrPasskeyVerificationFailed = errors.New("passkey verification failed")
	// ErrPasskeySessionExpired — the sealed session JWT is past its TTL
	// or invalid.
	ErrPasskeySessionExpired = errors.New("passkey session expired")
	// ErrPasskeyLastAuthMethod — removing this passkey would leave the
	// user with no way to sign in.
	ErrPasskeyLastAuthMethod = errors.New("passkey is the user's last auth method")
	// ErrWebAuthnNotConfigured — the server is missing or has rejected
	// the WebAuthn config (e.g. RP ID could not be derived from the
	// base URL).
	ErrWebAuthnNotConfigured = errors.New("webauthn is not configured on this server")
)

// PasskeyService owns the registration/login WebAuthn ceremonies and the
// passkey CRUD endpoints. It depends on the auth Service for the shared
// completeLogin path so passkey sign-ins issue tokens identical to the
// password and OAuth flows.
type PasskeyService struct {
	dbSvc    db.Service
	authSvc  *Service
	webAuthn *webauthn.WebAuthn
	enabled  bool
}

// NewPasskeyService constructs a PasskeyService from the auth service's
// state. Returns an instance with enabled=false (and a usable error) when
// the WebAuthn config is incomplete or insecure (e.g. base URL is plain
// http and the host is not localhost). The handler surfaces the disabled
// state as `passkeysEnabled: false` on /auth/providers; HTTP routes
// return WEBAUTHN_NOT_CONFIGURED.
func NewPasskeyService(authSvc *Service, dbSvc db.Service) *PasskeyService {
	cfg := authSvc.cfg.WebAuthn
	baseURL := ""
	if authSvc.fullCfg != nil {
		baseURL = authSvc.fullCfg.Server.BaseURL
	}

	if !cfg.Enabled {
		return &PasskeyService{dbSvc: dbSvc, authSvc: authSvc}
	}

	rpID, origins := resolveWebAuthnIdentity(cfg.RPID, cfg.Origins, baseURL)
	if rpID == "" {
		slog.Warn("webauthn disabled: RP ID could not be derived",
			"baseURL", baseURL, "rpId", cfg.RPID)

		return &PasskeyService{dbSvc: dbSvc, authSvc: authSvc}
	}

	displayName := cfg.RPDisplayName
	if displayName == "" {
		displayName = "SolidPing"
	}

	relyingParty, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: displayName,
		RPOrigins:     origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
	if err != nil {
		slog.Warn("webauthn disabled: invalid config", "err", err)

		return &PasskeyService{dbSvc: dbSvc, authSvc: authSvc}
	}

	return &PasskeyService{
		dbSvc:    dbSvc,
		authSvc:  authSvc,
		webAuthn: relyingParty,
		enabled:  true,
	}
}

// Enabled reports whether the WebAuthn config resolved cleanly. /auth/
// providers reads this; handlers refuse to start a ceremony when false.
func (s *PasskeyService) Enabled() bool {
	return s.enabled
}

// resolveWebAuthnIdentity derives the RP ID and origins. Explicit config
// wins; otherwise we infer both from the public base URL. Browsers refuse
// WebAuthn over plain http (except localhost), so a non-https BaseURL on
// a public host returns ("", nil) and the service stays disabled rather
// than registering credentials the browser will refuse to use.
func resolveWebAuthnIdentity(rpID string, origins []string, baseURL string) (string, []string) {
	if rpID != "" && len(origins) > 0 {
		return rpID, origins
	}

	if baseURL == "" {
		return "", nil
	}

	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		return "", nil
	}

	host := parsed.Hostname()
	scheme := parsed.Scheme

	if scheme != "https" && host != "localhost" && host != "127.0.0.1" {
		return "", nil
	}

	if rpID == "" {
		rpID = host
	}

	if len(origins) == 0 {
		origins = []string{scheme + "://" + parsed.Host}
	}

	return rpID, origins
}

// PasskeyInfo is the read-side projection returned by GET /auth/passkeys
// and embedded in the FinishRegistration response.
type PasskeyInfo struct {
	UID         string     `json:"uid"`
	Name        string     `json:"name"`
	AAGUIDLabel string     `json:"aaguidLabel,omitempty"`
	Transports  []string   `json:"transports,omitempty"`
	BackupState bool       `json:"backupState,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	LastUsedAt  *time.Time `json:"lastUsedAt,omitempty"`
}

// passkeyInfoFromModel projects a stored row into the API shape.
func passkeyInfoFromModel(passkey *models.UserPasskey) PasskeyInfo {
	info := PasskeyInfo{
		UID:         passkey.UID,
		Name:        passkey.Name,
		Transports:  passkey.Transports,
		BackupState: passkey.BackupState,
		CreatedAt:   passkey.CreatedAt,
		LastUsedAt:  passkey.LastUsedAt,
	}

	if passkey.AAGUID != nil {
		info.AAGUIDLabel = aaguidLabel(*passkey.AAGUID)
	}

	return info
}

// RegisterBeginResponse is the wire shape of POST /auth/passkeys/register/begin.
type RegisterBeginResponse struct {
	Options *protocol.CredentialCreation `json:"options"`
	Session string                       `json:"session"`
}

// LoginBeginResponse is the wire shape of POST /auth/passkeys/login/begin.
type LoginBeginResponse struct {
	Options *protocol.CredentialAssertion `json:"options"`
	Session string                        `json:"session"`
}

// BeginRegistration starts the ceremony for an authenticated user.
// The returned session JWT carries the WebAuthn challenge and the user's
// UID; the client must echo it back to FinishRegistration.
func (s *PasskeyService) BeginRegistration(
	ctx context.Context, userUID string,
) (*RegisterBeginResponse, error) {
	if !s.enabled {
		return nil, ErrWebAuthnNotConfigured
	}

	user, err := s.dbSvc.GetUser(ctx, userUID)
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}

	existing, err := s.dbSvc.ListUserPasskeysByUser(ctx, userUID)
	if err != nil {
		return nil, fmt.Errorf("list existing passkeys: %w", err)
	}

	rpUser := &passkeyUser{user: user, passkeys: existing}

	excludeList := make([]protocol.CredentialDescriptor, 0, len(existing))
	for _, passkey := range existing {
		excludeList = append(excludeList, protocol.CredentialDescriptor{
			Type:         protocol.PublicKeyCredentialType,
			CredentialID: passkey.CredentialID,
		})
	}

	options, sessionData, err := s.webAuthn.BeginRegistration(
		rpUser,
		webauthn.WithExclusions(excludeList),
	)
	if err != nil {
		return nil, fmt.Errorf("begin registration: %w", err)
	}

	sealed, err := s.authSvc.sealPasskeySession(passkeySessionPurposeRegister, userUID, sessionData)
	if err != nil {
		return nil, err
	}

	return &RegisterBeginResponse{Options: options, Session: sealed}, nil
}

// FinishRegistration consumes the sealed session and the credential
// returned by navigator.credentials.create(). On success it stores a
// row in user_passkeys and returns the projected info.
func (s *PasskeyService) FinishRegistration(
	ctx context.Context, sessionToken string, credentialJSON []byte, name string,
) (*PasskeyInfo, error) {
	if !s.enabled {
		return nil, ErrWebAuthnNotConfigured
	}

	claims, sessionData, err := s.authSvc.unsealPasskeySession(sessionToken, passkeySessionPurposeRegister)
	if err != nil {
		if errors.Is(err, errPasskeySessionExpired) {
			return nil, ErrPasskeySessionExpired
		}

		return nil, ErrPasskeyVerificationFailed
	}

	user, err := s.dbSvc.GetUser(ctx, claims.UserUID)
	if err != nil {
		return nil, fmt.Errorf("load user from session: %w", err)
	}

	parsed, err := protocol.ParseCredentialCreationResponseBytes(credentialJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %w", ErrPasskeyVerificationFailed, err)
	}

	rpUser := &passkeyUser{user: user}

	credential, err := s.webAuthn.CreateCredential(rpUser, *sessionData, parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPasskeyVerificationFailed, err)
	}

	if err := s.assertCredentialUnique(ctx, credential.ID); err != nil {
		return nil, err
	}

	row := buildPasskeyRow(user.UID, credential, name)

	if err := s.dbSvc.CreateUserPasskey(ctx, row); err != nil {
		return nil, fmt.Errorf("store passkey: %w", err)
	}

	info := passkeyInfoFromModel(row)

	return &info, nil
}

// assertCredentialUnique rejects re-registration of a credential ID
// already on file. Surfaced as PASSKEY_VERIFICATION_FAILED — a
// duplicate is either a replay or a misbehaving authenticator.
func (s *PasskeyService) assertCredentialUnique(ctx context.Context, credentialID []byte) error {
	existing, lookupErr := s.dbSvc.GetUserPasskeyByCredentialID(ctx, credentialID)
	if lookupErr != nil {
		if errors.Is(lookupErr, sql.ErrNoRows) {
			return nil
		}

		return fmt.Errorf("lookup duplicate: %w", lookupErr)
	}

	if existing != nil {
		return fmt.Errorf("%w: credential already registered", ErrPasskeyVerificationFailed)
	}

	return nil
}

// buildPasskeyRow turns a webauthn.Credential into the bun row, picking
// a default display name when the caller didn't supply one.
func buildPasskeyRow(userUID string, credential *webauthn.Credential, name string) *models.UserPasskey {
	displayName := strings.TrimSpace(name)
	aaguid := authenticatorAAGUID(credential)

	if displayName == "" {
		displayName = AAGUIDLabelSecurityKey
		if aaguid != "" {
			displayName = aaguidLabel(aaguid)
		}
	}

	row := models.NewUserPasskey(userUID, displayName, credential.ID, credential.PublicKey)
	row.SignCount = credential.Authenticator.SignCount
	row.UserVerified = credential.Flags.UserVerified
	row.BackupEligible = credential.Flags.BackupEligible
	row.BackupState = credential.Flags.BackupState

	if aaguid != "" {
		row.AAGUID = &aaguid
	}

	if credential.AttestationFormat != "" {
		format := credential.AttestationFormat
		row.AttestationFormat = &format
	}

	if len(credential.Transport) > 0 {
		transports := make([]string, 0, len(credential.Transport))
		for _, transport := range credential.Transport {
			transports = append(transports, string(transport))
		}
		row.Transports = transports
	}

	return row
}

// BeginLogin starts the assertion ceremony. When email is empty the
// returned options have an empty allowCredentials list — the browser
// uses discoverable credentials and the response carries the user
// handle. When email maps to a known user we list that user's
// credentials. Either way the response is indistinguishable to a
// caller probing for valid emails (the library generates a fresh
// challenge regardless).
func (s *PasskeyService) BeginLogin(
	ctx context.Context, email string,
) (*LoginBeginResponse, error) {
	if !s.enabled {
		return nil, ErrWebAuthnNotConfigured
	}

	options, sessionData, userUID, err := s.beginLoginCeremony(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("begin login: %w", err)
	}

	sealed, err := s.authSvc.sealPasskeySession(passkeySessionPurposeLogin, userUID, sessionData)
	if err != nil {
		return nil, err
	}

	return &LoginBeginResponse{Options: options, Session: sealed}, nil
}

// beginLoginCeremony picks the targeted vs discoverable branch. We fall
// back to discoverable on any user-lookup failure so the response shape
// is indistinguishable to a caller probing for valid emails.
func (s *PasskeyService) beginLoginCeremony(
	ctx context.Context, email string,
) (*protocol.CredentialAssertion, *webauthn.SessionData, string, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		options, session, err := s.webAuthn.BeginDiscoverableLogin()

		return options, session, "", err
	}

	user, lookupErr := s.dbSvc.GetUserByEmail(ctx, email)
	if lookupErr != nil || user == nil {
		options, session, err := s.webAuthn.BeginDiscoverableLogin()

		return options, session, "", err
	}

	passkeys, _ := s.dbSvc.ListUserPasskeysByUser(ctx, user.UID)
	if len(passkeys) == 0 {
		options, session, err := s.webAuthn.BeginDiscoverableLogin()

		return options, session, "", err
	}

	rpUser := &passkeyUser{user: user, passkeys: passkeys}
	options, session, err := s.webAuthn.BeginLogin(rpUser)

	return options, session, user.UID, err
}

// FinishLogin verifies the assertion, updates sign_count + last_used_at,
// resolves the user's organization preference, and delegates to
// completeLogin so the issued tokens match the password / OAuth paths.
// Skips TOTP per D2 — a passkey is already MFA-equivalent.
func (s *PasskeyService) FinishLogin(
	ctx context.Context, sessionToken string, credentialJSON []byte,
	orgPreference string, authContext Context,
) (*LoginResponse, error) {
	if !s.enabled {
		return nil, ErrWebAuthnNotConfigured
	}

	claims, sessionData, err := s.authSvc.unsealPasskeySession(sessionToken, passkeySessionPurposeLogin)
	if err != nil {
		if errors.Is(err, errPasskeySessionExpired) {
			return nil, ErrPasskeySessionExpired
		}

		return nil, ErrPasskeyVerificationFailed
	}

	parsed, err := protocol.ParseCredentialRequestResponseBytes(credentialJSON)
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %w", ErrPasskeyVerificationFailed, err)
	}

	user, credential, err := s.resolveAndVerifyAssertion(ctx, claims, sessionData, parsed)
	if err != nil {
		return nil, err
	}

	// Look up the row by credential ID so we can write back sign_count
	// + last_used_at.
	row, err := s.dbSvc.GetUserPasskeyByCredentialID(ctx, credential.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: row lookup: %w", ErrPasskeyVerificationFailed, err)
	}

	now := time.Now()
	signCount := credential.Authenticator.SignCount
	backupState := credential.Flags.BackupState

	if updateErr := s.dbSvc.UpdateUserPasskey(ctx, row.UID, models.UserPasskeyUpdate{
		SignCount:   &signCount,
		LastUsedAt:  &now,
		BackupState: &backupState,
	}); updateErr != nil {
		slog.WarnContext(ctx, "failed to write passkey sign_count", "err", updateErr, "passkeyUID", row.UID)
	}

	resolvedOrg, role, loginAction, orgSummaries, err := s.authSvc.resolveOrgPreference(ctx, orgPreference, user)
	if err != nil {
		return nil, err
	}

	return s.authSvc.completeLogin(
		ctx, user, resolvedOrg, role, loginAction, orgSummaries, AuthMethodPasskey, authContext)
}

// resolveAndVerifyAssertion handles both the "email known up-front"
// branch (claims.UserUID != "") and the discoverable-credential branch.
func (s *PasskeyService) resolveAndVerifyAssertion(
	ctx context.Context,
	claims *passkeySessionClaims,
	session *webauthn.SessionData,
	parsed *protocol.ParsedCredentialAssertionData,
) (*models.User, *webauthn.Credential, error) {
	if claims.UserUID != "" {
		user, err := s.dbSvc.GetUser(ctx, claims.UserUID)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: load user: %w", ErrPasskeyVerificationFailed, err)
		}

		passkeys, err := s.dbSvc.ListUserPasskeysByUser(ctx, user.UID)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: list passkeys: %w", ErrPasskeyVerificationFailed, err)
		}

		rpUser := &passkeyUser{user: user, passkeys: passkeys}

		credential, err := s.webAuthn.ValidateLogin(rpUser, *session, parsed)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrPasskeyVerificationFailed, err)
		}

		return user, credential, nil
	}

	// Discoverable: the assertion carries the user handle in the
	// response; we map it back to the user before letting the library
	// verify against their credentials.
	var matchedUser *models.User
	handler := func(_ /*rawID*/, userHandle []byte) (webauthn.User, error) {
		row, lookupErr := s.dbSvc.GetUser(ctx, string(userHandle))
		if lookupErr != nil {
			return nil, fmt.Errorf("user handle: %w", lookupErr)
		}

		passkeys, lookupErr := s.dbSvc.ListUserPasskeysByUser(ctx, row.UID)
		if lookupErr != nil {
			return nil, fmt.Errorf("list passkeys: %w", lookupErr)
		}

		matchedUser = row

		return &passkeyUser{user: row, passkeys: passkeys}, nil
	}

	credential, err := s.webAuthn.ValidateDiscoverableLogin(handler, *session, parsed)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrPasskeyVerificationFailed, err)
	}

	if matchedUser == nil {
		return nil, nil, ErrPasskeyVerificationFailed
	}

	return matchedUser, credential, nil
}

// ListPasskeys returns the projected info for every non-deleted passkey
// owned by the user.
func (s *PasskeyService) ListPasskeys(ctx context.Context, userUID string) ([]PasskeyInfo, error) {
	rows, err := s.dbSvc.ListUserPasskeysByUser(ctx, userUID)
	if err != nil {
		return nil, err
	}

	out := make([]PasskeyInfo, 0, len(rows))
	for _, p := range rows {
		out = append(out, passkeyInfoFromModel(p))
	}

	return out, nil
}

// RenamePasskey updates the user-visible label.
func (s *PasskeyService) RenamePasskey(
	ctx context.Context, userUID, passkeyUID, name string,
) error {
	row, err := s.dbSvc.GetUserPasskey(ctx, passkeyUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPasskeyNotFound
		}

		return err
	}

	if row.UserUID != userUID {
		return ErrPasskeyNotFound
	}

	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrPasskeyVerificationFailed)
	}

	return s.dbSvc.UpdateUserPasskey(ctx, passkeyUID, models.UserPasskeyUpdate{Name: &name})
}

// DeletePasskey enforces the last-auth-method invariant: a user can't
// remove their only sign-in method. A user has at least one of:
//   - a password set,
//   - another non-deleted passkey,
//   - a linked OAuth provider.
func (s *PasskeyService) DeletePasskey(
	ctx context.Context, userUID, passkeyUID string,
) error {
	row, err := s.dbSvc.GetUserPasskey(ctx, passkeyUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPasskeyNotFound
		}

		return err
	}

	if row.UserUID != userUID {
		return ErrPasskeyNotFound
	}

	user, err := s.dbSvc.GetUser(ctx, userUID)
	if err != nil {
		return err
	}

	hasPassword := user.PasswordHash != nil && *user.PasswordHash != ""

	otherPasskeys, err := s.dbSvc.ListUserPasskeysByUser(ctx, userUID)
	if err != nil {
		return err
	}

	hasOtherPasskey := false
	for _, p := range otherPasskeys {
		if p.UID != passkeyUID {
			hasOtherPasskey = true

			break
		}
	}

	hasOAuth := false

	providers, err := s.dbSvc.ListUserProvidersByUser(ctx, userUID)
	if err == nil && len(providers) > 0 {
		hasOAuth = true
	}

	if !hasPassword && !hasOtherPasskey && !hasOAuth {
		return ErrPasskeyLastAuthMethod
	}

	return s.dbSvc.DeleteUserPasskey(ctx, passkeyUID)
}

// authenticatorAAGUID extracts the AAGUID from the credential as a
// string. WebAuthn AAGUIDs are 16-byte UUIDs; an all-zero AAGUID means
// "not provided" and we return "".
func authenticatorAAGUID(credential *webauthn.Credential) string {
	raw := credential.Authenticator.AAGUID
	if len(raw) != 16 {
		return ""
	}

	allZero := true
	for _, b := range raw {
		if b != 0 {
			allZero = false

			break
		}
	}

	if allZero {
		return ""
	}

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}
