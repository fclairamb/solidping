package credentials_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// browserFixture is the blob dash0's sealing code (typage) produced, committed
// once and read by both the Go and the dash0 tests (spec 2026-11-08-01 §5).
type browserFixture struct {
	Identity    string         `json:"identity"`
	Recipient   string         `json:"recipient"`
	Fingerprint string         `json:"fingerprint"`
	Secrets     map[string]any `json:"secrets"`
	CT          string         `json:"ct"`
}

func loadBrowserFixture(t *testing.T) browserFixture {
	t.Helper()

	raw, err := os.ReadFile("../../../../web/dash0/src/lib/__fixtures__/browser-sealed.json")
	require.NoError(t, err)

	var fix browserFixture
	require.NoError(t, json.Unmarshal(raw, &fix))

	return fix
}

func TestBrowserSealedFixtureOpensWithAgentIdentity(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	fix := loadBrowserFixture(t)

	r.Equal(fix.Fingerprint, credentials.RecipientFingerprint(fix.Recipient),
		"the browser fingerprint must equal RecipientFingerprint byte for byte")
	r.NoError(credentials.ValidateSealedCiphertext(fix.CT, 1))

	envelope, err := credentials.BuildClientSealedEnvelope([]string{fix.Fingerprint}, fix.CT)
	r.NoError(err)
	r.Equal(credentials.SealOriginBrowser, credentials.SealedOrigin(envelope))

	secrets, err := credentials.UnsealWithIdentity(fix.Identity, envelope)
	r.NoError(err)
	r.Equal(fix.Secrets, secrets)
}

func TestValidateSealedCiphertext(t *testing.T) {
	t.Parallel()
	fix := loadBrowserFixture(t)

	_, recipientB, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	twoEnvelope, err := credentials.SealForRecipients([]string{fix.Recipient, recipientB}, map[string]any{"a": "b"})
	require.NoError(t, err)

	var two struct {
		CT string `json:"ct"`
	}
	require.NoError(t, json.Unmarshal([]byte(twoEnvelope), &two))

	big := make([]byte, credentials.MaxSealedCiphertextBytes+16)

	tests := []struct {
		name    string
		ct      string
		stanzas int
		wantErr error
	}{
		{name: "valid", ct: fix.CT, stanzas: 1},
		{name: "valid two stanzas", ct: two.CT, stanzas: 2},
		{name: "bad base64", ct: "!!!not-base64", stanzas: 1, wantErr: credentials.ErrSealedCiphertextEncoding},
		{
			name: "oversized", ct: base64Std(big), stanzas: 1,
			wantErr: credentials.ErrSealedCiphertextTooLarge,
		},
		{name: "not an age file", ct: base64Std([]byte("hello")), stanzas: 1, wantErr: credentials.ErrSealedCiphertextHeader},
		{name: "stanza count differs", ct: two.CT, stanzas: 1, wantErr: credentials.ErrSealedStanzaCount},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := credentials.ValidateSealedCiphertext(tc.ct, tc.stanzas)
			if tc.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// TestUnsealWithIdentitiesRegionAndAgentKeys: the same identity list opens a
// blob sealed to the region key and one sealed to the agent's own key, and a
// rotation holding the old and new region keys opens blobs sealed to either.
func TestUnsealWithIdentitiesRegionAndAgentKeys(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	agentID, agentRec, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	oldRegionID, oldRegionRec, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	newRegionID, newRegionRec, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	identities := []string{agentID, oldRegionID, newRegionID}

	for _, recipient := range []string{agentRec, oldRegionRec, newRegionRec} {
		blob, sealErr := credentials.SealForRecipients([]string{recipient}, map[string]any{"password": "x"})
		r.NoError(sealErr)
		r.Equal(credentials.SealOriginServer, credentials.SealedOrigin(blob))

		out, openErr := credentials.UnsealWithIdentities(blob, identities...)
		r.NoError(openErr)
		r.Equal("x", out["password"])
	}

	// Negative control: without the region key the region blob stays closed.
	regionOnly, err := credentials.SealForRecipients([]string{newRegionRec}, map[string]any{"password": "x"})
	r.NoError(err)
	_, err = credentials.UnsealWithIdentities(regionOnly, agentID)
	r.Error(err)
}

func base64Std(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }
