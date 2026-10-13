package notifications

import (
	crypto_rand "crypto/rand"
	"encoding/base64"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	standardwebhooks "github.com/standard-webhooks/standard-webhooks/libraries/go"
	"github.com/stretchr/testify/require"
)

// standardSecretRE is the exact shape of a generated secret: prefix, 43
// standard-alphabet characters and one pad.
var standardSecretRE = regexp.MustCompile(`^whsec_[A-Za-z0-9+/]{43}=$`)

// signedHeaders signs a request with signRequest for the given secrets and
// returns the headers a receiver would see.
func signedHeaders(t *testing.T, secrets []string, id string, ts time.Time, body []byte) http.Header {
	t.Helper()

	sig, err := signRequest(secrets, id, strconv.FormatInt(ts.Unix(), 10), body)
	require.NoError(t, err)

	h := http.Header{}
	h.Set(headerWebhookID, id)
	h.Set(headerWebhookTimestamp, strconv.FormatInt(ts.Unix(), 10))
	h.Set(headerWebhookSignature, sig)

	return h
}

func TestGenerateWebhookSecret_Format(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	seen := make(map[string]struct{}, 1000)

	for range 1000 {
		secret, err := GenerateWebhookSecret()
		r.NoError(err)

		r.True(strings.HasPrefix(secret, WebhookSecretPrefix))
		r.Len(secret, 50)
		r.True(strings.HasSuffix(secret, "="))
		r.False(strings.HasSuffix(secret, "=="))
		r.Regexp(standardSecretRE, secret)

		key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, WebhookSecretPrefix))
		r.NoError(err)
		r.Len(key, 32)

		_, dup := seen[secret]
		r.False(dup, "generated secrets must be unique")
		seen[secret] = struct{}{}
	}
}

// TestGenerateWebhookSecret_OfficialLibraryAccepts is the regression guard:
// the official Go library takes a generated secret as is and verifies a
// request SolidPing signed with it.
func TestGenerateWebhookSecret_OfficialLibraryAccepts(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	body := []byte(`{"event":"incident.created"}`)

	for i := range 500 {
		secret, err := GenerateWebhookSecret()
		r.NoError(err)

		wh, err := standardwebhooks.NewWebhook(secret)
		r.NoError(err, "official library must accept %q", secret)

		headers := signedHeaders(t, []string{secret}, "msg_"+strconv.Itoa(i), time.Now(), body)
		r.NoError(wh.Verify(body, headers))
	}
}

func TestDecodeWebhookSecret(t *testing.T) {
	t.Parallel()

	// Chosen so the standard spelling contains both '+' and '/' (and the
	// URL-safe one '-' and '_'), and the length needs padding.
	key := []byte{
		0xfb, 0xef, 0xbe, 0xff, 0xff, 0xff, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06,
		0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19,
	}

	std := base64.StdEncoding.EncodeToString(key)
	require.Contains(t, std, "+")
	require.Contains(t, std, "/")
	require.True(t, strings.HasSuffix(std, "="))

	valid := map[string]string{
		"standard padded":   std,
		"standard unpadded": base64.RawStdEncoding.EncodeToString(key),
		"url-safe padded":   base64.URLEncoding.EncodeToString(key),
		"url-safe unpadded": base64.RawURLEncoding.EncodeToString(key),
	}

	for name, body := range valid {
		for _, prefix := range []string{"", WebhookSecretPrefix} {
			t.Run(name+"/prefix="+prefix, func(t *testing.T) {
				t.Parallel()

				r := require.New(t)
				got, err := DecodeWebhookSecret(prefix + body)
				r.NoError(err)
				r.Equal(key, got)
			})
		}
	}

	invalid := []struct {
		name   string
		secret string
	}{
		{"empty", ""},
		{"prefix only", "whsec_"},
		{"not base64", "whsec_not base64!!"},
		{"padding in the middle", "whsec_a=bc"},
		{"three pads", "whsec_abc==="},
		{"length mod 4 is 1", "whsec_abcde"},
		{"only pads", "whsec_=="},
	}

	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			_, err := DecodeWebhookSecret(tc.secret)
			r.ErrorIs(err, ErrInvalidSigningSecret)
		})
	}
}

// TestDecodeWebhookSecret_RandomEncodings is a property-style loop: random key
// lengths, every base64 spelling, with and without prefix. All decode to the
// same bytes, sign identically, and the official library verifies the result.
func TestDecodeWebhookSecret_RandomEncodings(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	}
	body := []byte(`{"n":1}`)
	ts := time.Now()

	for i := range 2000 {
		n, err := crypto_rand.Int(crypto_rand.Reader, big.NewInt(64-24+1))
		r.NoError(err)

		key := make([]byte, 24+int(n.Int64()))
		_, err = crypto_rand.Read(key)
		r.NoError(err)

		id := "msg_" + strconv.Itoa(i)
		wantHeader := ""

		for _, enc := range encodings {
			for _, prefix := range []string{"", WebhookSecretPrefix} {
				secret := prefix + enc.EncodeToString(key)

				got, decErr := DecodeWebhookSecret(secret)
				r.NoError(decErr, secret)
				r.Equal(key, got, secret)

				headers := signedHeaders(t, []string{secret}, id, ts, body)
				if wantHeader == "" {
					wantHeader = headers.Get(headerWebhookSignature)
				}

				r.Equal(wantHeader, headers.Get(headerWebhookSignature), secret)
			}
		}

		wh, err := standardwebhooks.NewWebhookRaw(key)
		r.NoError(err)

		headers := signedHeaders(t, []string{WebhookSecretPrefix + base64.RawURLEncoding.EncodeToString(key)}, id, ts, body)
		r.NoError(wh.Verify(body, headers))
	}
}

func TestCanonicalWebhookSecret(t *testing.T) {
	t.Parallel()

	legacyKey := []byte{
		0xfb, 0xff, 0xbf, 0xfe, 0x00, 0x01, 0x02, 0x03, 0xfb, 0xef, 0xbe, 0xfb,
		0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xa0, 0xb0, 0xc0, 0xd0, 0xe0,
		0xf0, 0x01, 0x11, 0x21, 0x31, 0x41,
	}

	tests := []struct {
		name   string
		secret string
		want   string
	}{
		{
			name:   "legacy url-safe unpadded becomes standard padded",
			secret: WebhookSecretPrefix + base64.RawURLEncoding.EncodeToString(legacyKey),
			want:   WebhookSecretPrefix + base64.StdEncoding.EncodeToString(legacyKey),
		},
		{
			name:   "known vector valid in both alphabets is unchanged",
			secret: "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw",
			want:   "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw",
		},
		{
			name:   "missing prefix is added",
			secret: base64.StdEncoding.EncodeToString(legacyKey),
			want:   WebhookSecretPrefix + base64.StdEncoding.EncodeToString(legacyKey),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got, err := CanonicalWebhookSecret(tc.secret)
			r.NoError(err)
			r.Equal(tc.want, got)

			again, err := CanonicalWebhookSecret(got)
			r.NoError(err)
			r.Equal(got, again, "canonicalization must be idempotent")

			before, err := DecodeWebhookSecret(tc.secret)
			r.NoError(err)
			after, err := DecodeWebhookSecret(got)
			r.NoError(err)
			r.Equal(before, after, "canonicalization must keep the key bytes")
		})
	}

	_, err := CanonicalWebhookSecret("whsec_not base64!!")
	require.ErrorIs(t, err, ErrInvalidSigningSecret)
}

// TestLegacySecret_OfficialLibraryRejects documents why secrets are re-encoded
// at read time: the official library rejects the legacy URL-safe spelling and
// accepts the canonical one for the same key.
func TestLegacySecret_OfficialLibraryRejects(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	key := []byte{
		0xfb, 0xff, 0xbf, 0xfe, 0x00, 0x01, 0x02, 0x03, 0xfb, 0xef, 0xbe, 0xfb,
		0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80, 0x90, 0xa0, 0xb0, 0xc0, 0xd0, 0xe0,
		0xf0, 0x01, 0x11, 0x21, 0x31, 0x41,
	}
	legacy := WebhookSecretPrefix + base64.RawURLEncoding.EncodeToString(key)
	r.True(strings.ContainsAny(legacy, "-_"))

	_, err := standardwebhooks.NewWebhook(legacy)
	r.Error(err, "the official library rejects the legacy spelling")

	canonical, err := CanonicalWebhookSecret(legacy)
	r.NoError(err)

	wh, err := standardwebhooks.NewWebhook(canonical)
	r.NoError(err)

	body := []byte(`{}`)
	headers := signedHeaders(t, []string{legacy}, "msg_legacy", time.Now(), body)
	r.NoError(wh.Verify(body, headers), "the stored legacy secret and its canonical form sign identically")
}
