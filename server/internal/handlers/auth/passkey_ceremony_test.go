package auth_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/protocol/webauthncose"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
)

const (
	passkeyTestRPID   = "example.com"
	passkeyTestOrigin = "https://example.com"
)

// softAuthenticator is a minimal software WebAuthn authenticator: one P-256
// key, "none" attestation, enough to drive the real go-webauthn verification.
type softAuthenticator struct {
	key       *ecdsa.PrivateKey
	credID    []byte
	signCount uint32
}

func newSoftAuthenticator(t *testing.T) *softAuthenticator {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	credID := make([]byte, 16)
	_, err = rand.Read(credID)
	require.NoError(t, err)
	return &softAuthenticator{key: key, credID: credID}
}

func b64(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

func (a *softAuthenticator) clientData(t *testing.T, kind, challenge string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]string{"type": kind, "challenge": challenge, "origin": passkeyTestOrigin})
	require.NoError(t, err)
	return data
}

func (a *softAuthenticator) authData(flags byte, attested []byte) []byte {
	rpHash := sha256.Sum256([]byte(passkeyTestRPID))
	out := append([]byte{}, rpHash[:]...)
	out = append(out, flags)
	out = binary.BigEndian.AppendUint32(out, a.signCount)
	return append(out, attested...)
}

// register answers navigator.credentials.create() for the given challenge.
func (a *softAuthenticator) register(t *testing.T, challenge string) []byte {
	t.Helper()
	r := require.New(t)

	byteLen := (elliptic.P256().Params().BitSize + 7) / 8
	pub, err := a.key.PublicKey.Bytes()
	r.NoError(err)
	cose, err := webauthncbor.Marshal(webauthncose.EC2PublicKeyData{
		PublicKeyData: webauthncose.PublicKeyData{
			KeyType:   int64(webauthncose.EllipticKey),
			Algorithm: int64(webauthncose.AlgES256),
		},
		Curve:  int64(webauthncose.P256),
		XCoord: pub[1 : 1+byteLen],
		YCoord: pub[1+byteLen:],
	})
	r.NoError(err)

	attested := bytes.Repeat([]byte{0}, 16) // zero AAGUID
	attested = binary.BigEndian.AppendUint16(attested, uint16(len(a.credID)))
	attested = append(attested, a.credID...)
	attested = append(attested, cose...)

	attObj, err := webauthncbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": a.authData(0x45, attested), // UP | UV | AT
	})
	r.NoError(err)

	body, err := json.Marshal(map[string]any{
		"id":    b64(a.credID),
		"rawId": b64(a.credID),
		"type":  "public-key",
		"response": map[string]string{
			"clientDataJSON":    b64(a.clientData(t, "webauthn.create", challenge)),
			"attestationObject": b64(attObj),
		},
	})
	r.NoError(err)
	return body
}

// assert answers navigator.credentials.get() for the given challenge.
func (a *softAuthenticator) assert(t *testing.T, challenge string, userHandle []byte) []byte {
	t.Helper()
	r := require.New(t)

	a.signCount++
	authData := a.authData(0x05, nil) // UP | UV
	clientData := a.clientData(t, "webauthn.get", challenge)
	clientHash := sha256.Sum256(clientData)
	digest := sha256.Sum256(append(append([]byte{}, authData...), clientHash[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, a.key, digest[:])
	r.NoError(err)

	response := map[string]string{
		"clientDataJSON":    b64(clientData),
		"authenticatorData": b64(authData),
		"signature":         b64(sig),
	}
	if userHandle != nil {
		response["userHandle"] = b64(userHandle)
	}
	body, err := json.Marshal(map[string]any{
		"id":       b64(a.credID),
		"rawId":    b64(a.credID),
		"type":     "public-key",
		"response": response,
	})
	r.NoError(err)
	return body
}

// TestPasskeyService_FullCeremony registers a credential with a software
// authenticator, then logs in with it through both the targeted (email known)
// and the discoverable (user handle) branches, and checks the failure paths.
func TestPasskeyService_FullCeremony(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	pkSvc, dbSvc, user := passkeyTestUser(t)
	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))
	r.NoError(dbSvc.CreateOrganizationMember(ctx, models.NewOrganizationMember(org.UID, user.UID, models.MemberRoleAdmin)))

	device := newSoftAuthenticator(t)

	// Garbage session and garbage credential are both rejected.
	_, err := pkSvc.FinishRegistration(ctx, "not-a-session", []byte("{}"), "x")
	r.ErrorIs(err, auth.ErrPasskeyVerificationFailed)

	begin, err := pkSvc.BeginRegistration(ctx, user.UID, passkeyTestPasswordPtr())
	r.NoError(err)
	_, err = pkSvc.FinishRegistration(ctx, begin.Session, []byte("{"), "x")
	r.ErrorIs(err, auth.ErrPasskeyVerificationFailed)

	// A response for another challenge fails verification.
	_, err = pkSvc.FinishRegistration(ctx, begin.Session, device.register(t, b64([]byte("wrong-challenge-wrong"))), "")
	r.ErrorIs(err, auth.ErrPasskeyVerificationFailed)

	created := device.register(t, begin.Options.Response.Challenge.String())
	info, err := pkSvc.FinishRegistration(ctx, begin.Session, created, "")
	r.NoError(err)
	r.NotEmpty(info.UID)
	r.NotEmpty(info.Name, "a default display name is picked")

	// Registering the same credential twice is a replay.
	again, err := pkSvc.BeginRegistration(ctx, user.UID, passkeyTestPasswordPtr())
	r.NoError(err)
	r.Len(again.Options.Response.CredentialExcludeList, 1)
	duplicate := device.register(t, again.Options.Response.Challenge.String())
	_, err = pkSvc.FinishRegistration(ctx, again.Session, duplicate, "Dup")
	r.ErrorIs(err, auth.ErrPasskeyVerificationFailed)

	// Targeted login (email known).
	login, err := pkSvc.BeginLogin(ctx, user.Email)
	r.NoError(err)
	resp, err := pkSvc.FinishLogin(ctx, login.Session,
		device.assert(t, login.Options.Response.Challenge.String(), []byte(user.UID)), "acme",
		auth.Context{UserAgent: "test"})
	r.NoError(err)
	r.NotEmpty(resp.AccessToken)

	// Discoverable login (no email, user handle in the response).
	login, err = pkSvc.BeginLogin(ctx, "")
	r.NoError(err)
	resp, err = pkSvc.FinishLogin(ctx, login.Session,
		device.assert(t, login.Options.Response.Challenge.String(), []byte(user.UID)), "", auth.Context{})
	r.NoError(err)
	r.NotEmpty(resp.AccessToken)

	// Discoverable login with an unknown user handle.
	login, err = pkSvc.BeginLogin(ctx, "nobody@acme.com")
	r.NoError(err)
	_, err = pkSvc.FinishLogin(ctx, login.Session,
		device.assert(t, login.Options.Response.Challenge.String(), []byte("unknown-user")), "", auth.Context{})
	r.ErrorIs(err, auth.ErrPasskeyVerificationFailed)

	// A forged signature fails the targeted branch.
	other := newSoftAuthenticator(t)
	other.credID = device.credID
	login, err = pkSvc.BeginLogin(ctx, user.Email)
	r.NoError(err)
	_, err = pkSvc.FinishLogin(ctx, login.Session,
		other.assert(t, login.Options.Response.Challenge.String(), []byte(user.UID)), "", auth.Context{})
	r.ErrorIs(err, auth.ErrPasskeyVerificationFailed)

	// Malformed session / credential on login.
	_, err = pkSvc.FinishLogin(ctx, "garbage", []byte("{}"), "", auth.Context{})
	r.ErrorIs(err, auth.ErrPasskeyVerificationFailed)
	_, err = pkSvc.FinishLogin(ctx, login.Session, []byte("{"), "", auth.Context{})
	r.ErrorIs(err, auth.ErrPasskeyVerificationFailed)

	// A login session cannot finish a registration.
	_, err = pkSvc.FinishRegistration(ctx, login.Session, []byte("{}"), "")
	r.ErrorIs(err, auth.ErrPasskeyVerificationFailed)
}
