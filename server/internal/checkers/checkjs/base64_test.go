package checkjs

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBase64RoundTrip proves encode/decode are inverses for ordinary text.
func TestBase64RoundTrip(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScript(t, `
var original = "hello world, this has spaces and punctuation!";
var encoded = base64.encode(original);
var decoded = base64.decode(encoded);
return { status: decoded === original ? "up" : "down", output: { encoded: encoded, decoded: decoded } };
`)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
}

// TestBase64EncodeKnownVector pins the exact value the "Basic auth via
// base64.encode" doc example depends on — a regression that silently changed
// the alphabet or padding would break Basic-auth examples without any error.
func TestBase64EncodeKnownVector(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScript(t, `
return { status: base64.encode("user:pass") === "dXNlcjpwYXNz" ? "up" : "down" };
`)

	r.Equal("up", result.Status.String())
}

// TestBase64EncodePadding covers an input length that requires standard
// (padded) encoding to insert "=" — the padding HTTP Basic auth needs and
// URL-safe/no-pad encodings would omit.
func TestBase64EncodePadding(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScript(t, `
// "a" (1 byte) base64-encodes to "YQ==" under standard padded encoding.
return { status: base64.encode("a") === "YQ==" ? "up" : "down", output: { got: base64.encode("a") } };
`)

	r.Equal("up", result.Status.String(), "output: %#v", result.Output)
}

// TestBase64DecodeMalformedThrows is the negative that matters: a typo'd or
// corrupted base64 value must surface as a script error, never silently
// decode to an empty or partial string (which would turn a bad credential
// into a check that probes with an empty password and reports "down" with
// nothing visibly wrong).
func TestBase64DecodeMalformedThrows(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScript(t, `
base64.decode("not valid base64!!!");
return { status: "up" };
`)

	r.Equal("error", result.Status.String())
	r.Contains(result.Output["error"], "base64.decode")
}
