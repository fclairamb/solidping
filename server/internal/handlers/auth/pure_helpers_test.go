package auth

import (
	"encoding/pem"
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/require"
)

func TestAuthenticatorAAGUID(t *testing.T) {
	t.Parallel()

	full := []byte{0xad, 0xce, 0x00, 0x02, 0x35, 0xbc, 0xc6, 0x0a, 0x64, 0x8b, 0x0b, 0x25, 0xf1, 0xf0, 0x55, 0x03}

	tests := []struct {
		name string
		raw  []byte
		want string
	}{
		{"nil", nil, ""},
		{"wrong length", []byte{1, 2, 3}, ""},
		{"all zero", make([]byte, 16), ""},
		{"formatted", full, "adce0002-35bc-c60a-648b-0b25f1f05503"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cred := &webauthn.Credential{}
			cred.Authenticator.AAGUID = tt.raw
			require.Equal(t, tt.want, authenticatorAAGUID(cred))
		})
	}
}

func TestParseSAMLKeyPairPEM(t *testing.T) {
	t.Parallel()

	_, _, keyPEM, certPEM, err := generateSAMLKeyPair()
	require.NoError(t, err)

	key, cert, err := parseSAMLKeyPairPEM(keyPEM, certPEM)
	require.NoError(t, err)
	require.NotNil(t, key)
	require.NotNil(t, cert)

	garbageKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("junk")}))
	garbageCert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("junk")}))

	tests := []struct {
		name          string
		key, cert     string
		wantErr       error
		wantErrSubstr string
	}{
		{"key not pem", "nope", certPEM, errSAMLInvalidKeyPEM, ""},
		{"key not parseable", garbageKey, certPEM, nil, "parse SP private key"},
		{"cert not pem", keyPEM, "nope", errSAMLInvalidCertPEM, ""},
		{"cert not parseable", keyPEM, garbageCert, nil, "parse SP certificate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, parseErr := parseSAMLKeyPairPEM(tt.key, tt.cert)
			require.Error(t, parseErr)

			if tt.wantErr != nil {
				require.ErrorIs(t, parseErr, tt.wantErr)
			} else {
				require.ErrorContains(t, parseErr, tt.wantErrSubstr)
			}
		})
	}
}
