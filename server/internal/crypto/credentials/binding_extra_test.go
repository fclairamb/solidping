package credentials_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

type pointStruct struct {
	X int `json:"x"`
}

func TestCanonicalTargetValueKinds(t *testing.T) {
	t.Parallel()

	fields := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p"}

	cfg := map[string]any{
		"a": []string{"B", "c"},
		"b": []any{"x", nil, 2.5},
		"c": map[string]any{"z": 1, "y": nil, "x": true},
		"d": float32(3),
		"e": int64(-7),
		"f": int32(8),
		"g": json.Number("12"),
		"h": pointStruct{X: 4},
		"i": 1e21,
		"j": "quote\"back\\slash\x01\x1f",
		"k": nil,
		"l": "",
		"m": make(chan int), // not marshalable: dropped
		"n": []any{[]string{"q"}},
		"o": 7,
		"p": false,
	}

	got := credentials.CanonicalTarget(fields, cfg)
	want := `{"a":["B","c"],"b":["x",null,"2.5"],"c":{"x":"true","z":"1"},"d":"3","e":"-7","f":"8",` +
		`"g":"12","h":{"x":"4"},"i":"1e+21","j":"quote\"back\\slash\u0001\u001f","n":[["q"]],"o":"7","p":"false"}`
	require.Equal(t, want, got)

	// Host-like fields are lowercased, including lists.
	hosts := `{"brokers":["a:1","b:2"],"host":"acme.com","hostname":["x"],"serverUrl":"https://acme.com:443"}`
	hostCfg := map[string]any{
		"brokers":   []string{"A:1", "B:2"},
		"host":      "ACME.com",
		"hostname":  []any{"X"},
		"serverUrl": "HTTPS://Acme.com",
	}
	require.Equal(t, hosts, credentials.CanonicalTarget([]string{"brokers", "host", "hostname", "serverUrl"}, hostCfg))
	number := `{"host":"7"}`
	require.Equal(t, number, credentials.CanonicalTarget([]string{"host"}, map[string]any{"host": 7}))
}

func TestNormalizeTargetURLEdges(t *testing.T) {
	t.Parallel()

	require.Equal(t, "http://[::1:8080:80", credentials.NormalizeTargetURL("HTTP://[::1:8080"))
	require.Equal(t, "http://[::1]:8080/p", credentials.NormalizeTargetURL("HTTP://[::1]:8080/p"))
	require.Equal(t, "a%b://x", credentials.NormalizeTargetURL("a%b://x"))
	require.Equal(t, "://x", credentials.NormalizeTargetURL("://x"))
	require.Equal(t, "a1+b-c.d://host", credentials.NormalizeTargetURL("A1+B-C.D://Host"))
}

func TestBindingRefusals(t *testing.T) {
	t.Parallel()

	check := credentials.BindingCheck{
		Type:         "http",
		Config:       map[string]any{"url": "https://acme.com"},
		TargetFields: []string{"url"},
		SecretFields: []string{"token"},
	}

	t.Run("binding is not an object", func(t *testing.T) {
		t.Parallel()

		_, bound, err := credentials.VerifyBinding(map[string]any{credentials.BindingKey: "x"}, check)
		require.True(t, bound)
		require.ErrorIs(t, err, credentials.ErrInvalidBinding)
	})

	t.Run("binding misses its digest", func(t *testing.T) {
		t.Parallel()

		_, _, err := credentials.VerifyBinding(
			map[string]any{credentials.BindingKey: map[string]any{"type": "http"}}, check)
		require.ErrorIs(t, err, credentials.ErrInvalidBinding)
	})

	t.Run("unbound blob is returned as is", func(t *testing.T) {
		t.Parallel()

		plain := map[string]any{"token": "t"}
		out, bound, err := credentials.VerifyBinding(plain, check)
		require.NoError(t, err)
		require.False(t, bound)
		require.Equal(t, plain, out)
	})
}

func TestOpenSealedPlaintext(t *testing.T) {
	t.Parallel()

	_, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	_, ct, err := credentials.SealForClient([]string{recipient}, map[string]any{"token": "t"})
	require.NoError(t, err)

	fps := []string{credentials.RecipientFingerprint(recipient)}
	browser, err := credentials.BuildClientSealedEnvelope(fps, ct)
	require.NoError(t, err)

	server, err := credentials.SealForRecipients([]string{recipient}, map[string]any{"token": "t"})
	require.NoError(t, err)

	typed := credentials.BindingCheck{
		Type: "http", Config: map[string]any{"url": "https://acme.com"},
		TargetFields: []string{"url"}, SecretFields: []string{"token"},
	}
	untyped := credentials.BindingCheck{
		Type: "plugin", Config: map[string]any{"host": "db"}, TargetFields: []string{"url", "port"},
	}

	bound := credentials.WithBinding(map[string]any{"token": "t"}, "http", typed.TargetFields, typed.Config)

	t.Run("bound blob loses its binding", func(t *testing.T) {
		t.Parallel()

		out, legacy, err := credentials.OpenSealedPlaintext(browser, bound, typed)
		require.NoError(t, err)
		require.False(t, legacy)
		require.Equal(t, map[string]any{"token": "t"}, out)
	})

	t.Run("bound blob for another target is refused", func(t *testing.T) {
		t.Parallel()

		other := typed
		other.Config = map[string]any{"url": "https://evil.example"}
		_, _, err := credentials.OpenSealedPlaintext(browser, bound, other)
		require.ErrorIs(t, err, credentials.ErrSealedForAnotherTarget)
	})

	t.Run("unbound server blob is used as is", func(t *testing.T) {
		t.Parallel()

		out, legacy, err := credentials.OpenSealedPlaintext(server, map[string]any{"url": "x"}, typed)
		require.NoError(t, err)
		require.False(t, legacy)
		require.Equal(t, map[string]any{"url": "x"}, out)
	})

	t.Run("unbound browser blob is legacy", func(t *testing.T) {
		t.Parallel()

		out, legacy, err := credentials.OpenSealedPlaintext(browser, map[string]any{"token": "t"}, typed)
		require.NoError(t, err)
		require.True(t, legacy)
		require.Equal(t, map[string]any{"token": "t"}, out)
	})

	t.Run("unbound browser blob with a public key is refused", func(t *testing.T) {
		t.Parallel()

		_, _, err := credentials.OpenSealedPlaintext(browser, map[string]any{"url": "x"}, typed)
		require.ErrorIs(t, err, credentials.ErrSealedKeyNotSecret)
	})

	t.Run("type unknown to this process", func(t *testing.T) {
		t.Parallel()

		// A key that is public or a target field is refused, any other passes.
		require.NoError(t, credentials.StrictSecretKeys(map[string]any{"token": "t"}, untyped))
		require.ErrorIs(t, credentials.StrictSecretKeys(map[string]any{"host": "x"}, untyped),
			credentials.ErrSealedKeyNotSecret)
		require.ErrorIs(t, credentials.StrictSecretKeys(map[string]any{"port": "x"}, untyped),
			credentials.ErrSealedKeyNotSecret)
	})
}

func TestSealedChallengeFailures(t *testing.T) {
	t.Parallel()

	_, _, err := credentials.NewSealedChallenge("not-a-recipient")
	require.Error(t, err)

	identity, recipient, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)

	noValue, err := credentials.SealForRecipients([]string{recipient}, map[string]any{"other": "x"})
	require.NoError(t, err)

	_, err = credentials.OpenSealedChallenge(noValue, identity)
	require.ErrorIs(t, err, credentials.ErrInvalidChallenge)
}

func TestMarkSealedRejectsNonEnvelopes(t *testing.T) {
	t.Parallel()

	_, err := credentials.MarkSealedBound("not json", nil)
	require.Error(t, err)

	_, err = credentials.MarkSealedUnconfirmed("not json")
	require.Error(t, err)
}

type fielderConfig struct{}

func (fielderConfig) TargetFields() []string { return []string{"queue", "url"} }

func TestTargetFieldsForDeclaredFields(t *testing.T) {
	t.Parallel()

	require.Equal(t, []string{"queue", "tunnelCheckUid", "url"}, credentials.TargetFieldsFor(fielderConfig{}))
}
