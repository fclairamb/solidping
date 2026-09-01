package heartbeatpush_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/heartbeatpush"
)

const (
	testToken = "0123456789abcdef0123456789abcdef"
	testOrg   = "acme"
	testCheck = "sensor-1"
)

// TestParseSP1 is the positive control every SP1 negative below mutates.
func TestParseSP1(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	beat, err := heartbeatpush.ParseLine([]byte("SP1 acme/sensor-1 " + testToken))
	r.NoError(err)
	r.Equal(1, beat.Version)
	r.Equal(testOrg, beat.Org)
	r.Equal(testCheck, beat.Identifier)
	r.Equal(testToken, beat.Token)
	r.Empty(beat.Annotation)
}

func TestParseSP1WithAnnotation(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	beat, err := heartbeatpush.ParseLine([]byte("SP1 acme/sensor-1 " + testToken + " started volts=3.71 rssi=-67"))
	r.NoError(err)
	r.Equal("started volts=3.71 rssi=-67", beat.Annotation)
}

// TestParseSP2PinsTheSignedBytes is the wire contract: the MAC is the LAST
// space-separated token and covers everything before it, annotation included.
// A firmware author transcribing this must get the same bytes.
func TestParseSP2PinsTheSignedBytes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	line := "SP2 acme/sensor-1 1754640000 4294967297 started volts=3.71 " +
		hex.EncodeToString(heartbeatpush.ComputeMAC(testToken, "SP2 acme/sensor-1 1754640000 4294967297 started volts=3.71"))

	beat, err := heartbeatpush.ParseLine([]byte(line))
	r.NoError(err)
	r.Equal(2, beat.Version)
	r.Equal(testOrg, beat.Org)
	r.Equal(testCheck, beat.Identifier)
	r.Equal(int64(1754640000), beat.Timestamp)
	r.Equal(int64(4294967297), beat.Counter)
	r.Equal("SP2 acme/sensor-1 1754640000 4294967297 started volts=3.71", beat.Signed)
	r.Equal("started volts=3.71", beat.Annotation)
	r.True(heartbeatpush.VerifyMAC(testToken, beat.Signed, beat.MAC))
}

// TestComputeMACMatchesAnIndependentHMAC recomputes the tag by hand so the
// suite does not merely assert ComputeMAC against itself.
func TestComputeMACMatchesAnIndependentHMAC(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	signed := "SP2 acme/sensor-1 0 7"
	mac := hmac.New(sha256.New, []byte(testToken))
	mac.Write([]byte(signed))

	r.Equal(mac.Sum(nil)[:16], heartbeatpush.ComputeMAC(testToken, signed))
	r.Len(heartbeatpush.ComputeMAC(testToken, signed), heartbeatpush.MACBytes)
}

// TestSignSP2RoundTrips proves the helper the docs and tests build lines with
// agrees with the parser.
func TestSignSP2RoundTrips(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, annotation := range []string{"", "started", "started volts=3.71 rssi=-67"} {
		line := heartbeatpush.SignSP2(testOrg, testCheck, testToken, 1754640000, 42, annotation)

		beat, err := heartbeatpush.ParseLine([]byte(line))
		r.NoError(err)
		r.True(heartbeatpush.VerifyMAC(testToken, beat.Signed, beat.MAC))
		r.Equal(annotation, beat.Annotation)
	}
}

// TestVerifyMACRejectsWrongKeyAndTamper is the negative half of the signature
// contract: one flipped byte anywhere in the covered bytes, or the wrong key,
// and the tag no longer matches.
func TestVerifyMACRejectsWrongKeyAndTamper(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	line := heartbeatpush.SignSP2(testOrg, testCheck, testToken, 1754640000, 42, "started volts=3.71")
	beat, err := heartbeatpush.ParseLine([]byte(line))
	r.NoError(err)

	// Positive control.
	r.True(heartbeatpush.VerifyMAC(testToken, beat.Signed, beat.MAC))

	r.False(heartbeatpush.VerifyMAC("wrong-token", beat.Signed, beat.MAC))
	r.False(heartbeatpush.VerifyMAC(testToken, beat.Signed+"x", beat.MAC))
	// Tampering with the annotation alone breaks the tag — that is the whole
	// point of putting the MAC last.
	r.False(heartbeatpush.VerifyMAC(testToken,
		strings.Replace(beat.Signed, "volts=3.71", "volts=9.99", 1), beat.MAC))
	r.False(heartbeatpush.VerifyMAC(testToken, beat.Signed, beat.MAC[:8]))
	r.False(heartbeatpush.VerifyMAC(testToken, beat.Signed, nil))
}

func TestParseLineRejectsMalformed(t *testing.T) {
	t.Parallel()

	valid := heartbeatpush.SignSP2(testOrg, testCheck, testToken, 1754640000, 42, "")

	cases := []struct {
		name string
		line string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"unknown protocol", "SP3 acme/sensor-1 " + testToken},
		{"lowercase protocol", "sp1 acme/sensor-1 " + testToken},
		{"no protocol", "acme/sensor-1 " + testToken},
		{"SP1 without token", "SP1 acme/sensor-1"},
		{"SP1 without target", "SP1 " + testToken},
		{"target without slash", "SP1 acme " + testToken},
		{"target with empty org", "SP1 /sensor-1 " + testToken},
		{"target with empty identifier", "SP1 acme/ " + testToken},
		{"target with two slashes", "SP1 acme/a/b " + testToken},
		{"double space", "SP1 acme/sensor-1  " + testToken},
		{"leading space", " SP1 acme/sensor-1 " + testToken},
		{"oversized org", "SP1 " + strings.Repeat("o", 65) + "/sensor-1 " + testToken},
		{"oversized identifier", "SP1 acme/" + strings.Repeat("i", 129) + " " + testToken},
		{"oversized line", "SP1 acme/sensor-1 " + strings.Repeat("t", 600)},
		{"SP2 too few fields", "SP2 acme/sensor-1 0 1"},
		{"SP2 non-numeric ts", strings.Replace(valid, " 1754640000 ", " later ", 1)},
		{"SP2 negative ts", strings.Replace(valid, " 1754640000 ", " -1 ", 1)},
		{"SP2 non-numeric counter", strings.Replace(valid, " 42 ", " many ", 1)},
		{"SP2 negative counter", strings.Replace(valid, " 42 ", " -1 ", 1)},
		{"SP2 counter above int64", strings.Replace(valid, " 42 ", " 18446744073709551615 ", 1)},
		{"SP2 short MAC", valid[:len(valid)-2]},
		{"SP2 non-hex MAC", valid[:len(valid)-32] + strings.Repeat("z", 32)},
	}

	for _, tc := range cases {
		r := require.New(t)
		beat, err := heartbeatpush.ParseLine([]byte(tc.line))
		r.ErrorIs(err, heartbeatpush.ErrMalformed, tc.name)
		r.Nil(beat, tc.name)
	}

	// Positive control: the unmutated line the negatives are derived from
	// parses, so none of the cases above can pass vacuously.
	beat, err := heartbeatpush.ParseLine([]byte(valid))
	require.NoError(t, err)
	require.NotNil(t, beat)
}

// TestParseLineToleratesLineEndings — a CRLF sender and an LF sender must sign
// the same bytes, otherwise half the world's firmware fails verification for a
// reason no one can see.
func TestParseLineToleratesLineEndings(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	line := heartbeatpush.SignSP2(testOrg, testCheck, testToken, 0, 1, "")

	for _, suffix := range []string{"", "\r", "\r\n", "\n", " ", "\t"} {
		beat, err := heartbeatpush.ParseLine([]byte(line + suffix))
		r.NoError(err, suffix)
		r.True(heartbeatpush.VerifyMAC(testToken, beat.Signed, beat.MAC), suffix)
	}
}
