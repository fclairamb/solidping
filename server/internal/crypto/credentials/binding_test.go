package credentials_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// bindingFixture is the golden vector shared with dash0's sealing.test.ts
// (spec 2026-11-11-01): both canonicalisers must give these bytes.
type bindingFixture struct {
	Cases []struct {
		Name         string         `json:"name"`
		Type         string         `json:"type"`
		TargetFields []string       `json:"targetFields"`
		Config       map[string]any `json:"config"`
		Canonical    string         `json:"canonical"`
		Digest       string         `json:"digest"`
	} `json:"cases"`
}

func TestTargetDigestMatchesSharedFixture(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../../../web/dash0/src/lib/__fixtures__/target-binding.json")
	require.NoError(t, err)

	var fix bindingFixture
	require.NoError(t, json.Unmarshal(raw, &fix))
	require.NotEmpty(t, fix.Cases)

	for _, tc := range fix.Cases {
		require.Equal(t, tc.Canonical, credentials.CanonicalTarget(tc.TargetFields, tc.Config), tc.Name)
		require.Equal(t, tc.Digest, credentials.TargetDigest(tc.TargetFields, tc.Config), tc.Name)
	}

	// The pairs the fixture pins as equal really are.
	require.Equal(t, fix.Cases[0].Digest, fix.Cases[1].Digest, "normalised URL")
	require.Equal(t, fix.Cases[3].Digest, fix.Cases[4].Digest, "numeric and string port")
}

func TestNormalizeTargetURL(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"HTTPS://Acme.COM":                "https://acme.com:443",
		"http://acme.com/A?B#C":           "http://acme.com:80/A?B#C",
		"wss://u:P@Acme.com:9000/x":       "wss://u:P@acme.com:9000/x",
		"http://[::1]/":                   "http://[::1]:80/",
		"custom://Host":                   "custom://host",
		"not a url":                       "not a url",
		"1http://acme.com":                "1http://acme.com",
		"https://acme.com?x=HTTPS://A.B/": "https://acme.com:443?x=HTTPS://A.B/",
	}

	for in, want := range cases {
		require.Equal(t, want, credentials.NormalizeTargetURL(in), in)
	}
}

func TestTargetFieldsForAddsTheTunnel(t *testing.T) {
	t.Parallel()

	// A type that declares no list (a plugin, a type unknown to the agent)
	// binds every public key (spec 2026-11-12-02).
	require.Equal(t, []string{credentials.AllPublicKeys, "tunnelCheckUid"}, credentials.TargetFieldsFor(nil))
}

func bindingCheck(config map[string]any) credentials.BindingCheck {
	return credentials.BindingCheck{
		Type:         "http",
		Config:       config,
		TargetFields: []string{"tunnelCheckUid", "url"},
		SecretFields: []string{"basicAuth", "password", "secretHeaders"},
	}
}

func TestVerifyBinding(t *testing.T) {
	t.Parallel()

	original := map[string]any{"url": "https://db.acme.com", "timeout": "5s"}
	plain := credentials.WithBinding(map[string]any{"basicAuth": "alice:hunter2"}, "http",
		[]string{"tunnelCheckUid", "url"}, original)

	t.Run("same target: secrets without the binding", func(t *testing.T) {
		t.Parallel()

		// A non-target change (the timeout) keeps the binding valid.
		secrets, bound, err := credentials.VerifyBinding(plain,
			bindingCheck(map[string]any{"url": "https://DB.acme.com:443", "timeout": "9s"}))
		require.NoError(t, err)
		require.True(t, bound)
		require.Equal(t, map[string]any{"basicAuth": "alice:hunter2"}, secrets)
	})

	t.Run("another URL is refused", func(t *testing.T) {
		t.Parallel()

		_, bound, err := credentials.VerifyBinding(plain, bindingCheck(map[string]any{"url": "https://evil.example"}))
		require.True(t, bound)
		require.ErrorIs(t, err, credentials.ErrSealedForAnotherTarget)
	})

	t.Run("a tunnel added is refused", func(t *testing.T) {
		t.Parallel()

		_, _, err := credentials.VerifyBinding(plain,
			bindingCheck(map[string]any{"url": "https://db.acme.com", "tunnelCheckUid": "bastion"}))
		require.ErrorIs(t, err, credentials.ErrSealedForAnotherTarget)
	})

	t.Run("another type is refused", func(t *testing.T) {
		t.Parallel()

		check := bindingCheck(original)
		check.Type = "health"
		_, _, err := credentials.VerifyBinding(plain, check)
		require.ErrorIs(t, err, credentials.ErrSealedForAnotherTarget)
	})

	t.Run("a public key in the blob is refused", func(t *testing.T) {
		t.Parallel()

		overriding := credentials.WithBinding(map[string]any{"basicAuth": "a:b", "url": "https://evil.example"},
			"http", []string{"tunnelCheckUid", "url"}, original)
		_, _, err := credentials.VerifyBinding(overriding, bindingCheck(original))
		require.ErrorIs(t, err, credentials.ErrSealedKeyNotSecret)
	})

	t.Run("an unknown type still cannot override a public key", func(t *testing.T) {
		t.Parallel()

		check := bindingCheck(original)
		check.SecretFields = nil
		overriding := credentials.WithBinding(map[string]any{"timeout": "1s"}, "http",
			[]string{"tunnelCheckUid", "url"}, original)
		_, _, err := credentials.VerifyBinding(overriding, check)
		require.ErrorIs(t, err, credentials.ErrSealedKeyNotSecret)

		other := credentials.WithBinding(map[string]any{"token": "t"}, "http", []string{"tunnelCheckUid", "url"}, original)
		secrets, _, err := credentials.VerifyBinding(other, check)
		require.NoError(t, err)
		require.Equal(t, map[string]any{"token": "t"}, secrets)
	})

	t.Run("an unbound blob is returned as is", func(t *testing.T) {
		t.Parallel()

		legacy := map[string]any{"basicAuth": "a:b"}
		secrets, bound, err := credentials.VerifyBinding(legacy, bindingCheck(map[string]any{"url": "https://x"}))
		require.NoError(t, err)
		require.False(t, bound)
		require.Equal(t, legacy, secrets)
	})

	t.Run("a malformed binding is refused", func(t *testing.T) {
		t.Parallel()

		for _, bad := range []any{"x", map[string]any{"type": "http"}, map[string]any{"digest": "d"}} {
			_, bound, err := credentials.VerifyBinding(
				map[string]any{credentials.BindingKey: bad, "basicAuth": "a:b"}, bindingCheck(original))
			require.True(t, bound)
			require.ErrorIs(t, err, credentials.ErrInvalidBinding)
		}
	})
}

func TestTargetChanged(t *testing.T) {
	t.Parallel()

	fields := []string{"host", "port", "tunnelCheckUid"}
	before := map[string]any{"host": "db.acme.com", "port": float64(5432), "timeout": "5s"}

	require.False(t, credentials.TargetChanged(fields, before, map[string]any{"host": "DB.acme.com", "port": "5432"}))
	require.True(t, credentials.TargetChanged(fields, before, map[string]any{"host": "evil.example", "port": 5432}))
	require.True(t, credentials.TargetChanged(fields, before, map[string]any{"host": "db.acme.com", "port": 6543}))
}

func TestMarkSealedBound(t *testing.T) {
	t.Parallel()

	fix := loadBrowserFixture(t)
	envelope, err := credentials.BuildClientSealedEnvelope([]string{fix.Fingerprint}, fix.CT)
	require.NoError(t, err)
	require.False(t, credentials.IsSealedBound(envelope))

	bound, err := credentials.MarkSealedBound(envelope, nil)
	require.NoError(t, err)
	require.True(t, credentials.IsSealedBound(bound))
	require.Equal(t, credentials.SealOriginBrowser, credentials.SealedOrigin(bound))

	// A peer re-wrap keeps the flag.
	rewrapped, err := credentials.RewrapSealedEnvelope(bound, []string{fix.Fingerprint}, fix.CT)
	require.NoError(t, err)
	require.True(t, credentials.IsSealedBound(rewrapped))
	require.Nil(t, credentials.SealedBoundTargetFields(rewrapped), "no list recorded")

	// The list the client bound to is recorded, sorted, and kept by a re-wrap
	// (spec 2026-11-12-02).
	withFields, err := credentials.MarkSealedBound(envelope, []string{"script", "env", "tunnelCheckUid", "env"})
	require.NoError(t, err)
	require.Equal(t, []string{"env", "script", "tunnelCheckUid"}, credentials.SealedBoundTargetFields(withFields))
	rewrapped, err = credentials.RewrapSealedEnvelope(withFields, []string{fix.Fingerprint}, fix.CT)
	require.NoError(t, err)
	require.Equal(t, []string{"env", "script", "tunnelCheckUid"}, credentials.SealedBoundTargetFields(rewrapped))
}

func TestLegacyTargetFieldsFor(t *testing.T) {
	t.Parallel()

	require.Equal(t,
		[]string{
			"address", "brokers", "database", "dsn", "endpoint", "host", "hostname", "port", "server",
			"tunnelCheckUid", "url",
		},
		credentials.LegacyTargetFieldsFor(nil))
}

// TestRewrapMarksAServerBlobAsAnAgentBlob (spec 2026-11-12-03): a peer
// re-wrap of a server blob is an agent blob, which gets the strict-keys rule;
// a browser blob stays a browser blob; a server blob is used as is.
func TestRewrapMarksAServerBlobAsAnAgentBlob(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, recipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	server, err := credentials.SealForRecipients([]string{recipient}, map[string]any{"password": "s3cret"})
	r.NoError(err)
	r.Equal(credentials.SealOriginServer, credentials.SealedOrigin(server))

	fps, ct, err := credentials.SealForClient([]string{recipient}, map[string]any{"password": "s3cret"})
	r.NoError(err)

	agent, err := credentials.RewrapSealedEnvelope(server, fps, ct)
	r.NoError(err)
	r.Equal(credentials.SealOriginAgent, credentials.SealedOrigin(agent))

	again, err := credentials.RewrapSealedEnvelope(agent, fps, ct)
	r.NoError(err)
	r.Equal(credentials.SealOriginAgent, credentials.SealedOrigin(again))

	browser, err := credentials.BuildClientSealedEnvelope(fps, ct)
	r.NoError(err)
	rewrapped, err := credentials.RewrapSealedEnvelope(browser, fps, ct)
	r.NoError(err)
	r.Equal(credentials.SealOriginBrowser, credentials.SealedOrigin(rewrapped))

	check := credentials.BindingCheck{
		Type: "http", Config: map[string]any{"url": "https://acme.com"},
		TargetFields: []string{"url"}, SecretFields: []string{"password"},
	}
	leaky := map[string]any{"password": "s3cret", "url": "https://evil.example"}

	_, _, err = credentials.OpenSealedPlaintext(agent, leaky, check)
	r.ErrorIs(err, credentials.ErrSealedKeyNotSecret)

	opened, legacy, err := credentials.OpenSealedPlaintext(agent, map[string]any{"password": "s3cret"}, check)
	r.NoError(err)
	r.False(legacy, "an agent blob never had a binding: no legacy warning")
	r.Equal("s3cret", opened["password"])

	opened, _, err = credentials.OpenSealedPlaintext(server, leaky, check)
	r.NoError(err, "a server blob carries the per-claim ${param:} overlay of public fields")
	r.Equal("https://evil.example", opened["url"])
}
