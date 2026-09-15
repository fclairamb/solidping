package checkerdef_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestDecodePayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		encoding string
		want     []byte
		wantErr  string
	}{
		{
			name:     "empty encoding is text",
			input:    `EHLO acme\r\n`,
			encoding: "",
			// THE compatibility guarantee: a stored send_data with a literal
			// backslash keeps sending that backslash.
			want: []byte(`EHLO acme\r\n`),
		},
		{
			name:     "explicit text is byte-for-byte",
			input:    "PING\r\n",
			encoding: checkerdef.PayloadEncodingText,
			want:     []byte("PING\r\n"),
		},
		{
			name:     "escaped decodes CRLF",
			input:    `EHLO acme\r\n`,
			encoding: checkerdef.PayloadEncodingEscaped,
			want:     []byte("EHLO acme\r\n"),
		},
		{
			name:     "escaped decodes NUL, tab and a literal backslash",
			input:    `a\0b\tc\\d`,
			encoding: checkerdef.PayloadEncodingEscaped,
			want:     []byte{'a', 0, 'b', '\t', 'c', '\\', 'd'},
		},
		{
			name:     "escaped decodes hex escapes",
			input:    `\x00\xff\x7f`,
			encoding: checkerdef.PayloadEncodingEscaped,
			want:     []byte{0x00, 0xff, 0x7f},
		},
		{
			name:     "escaped rejects an unknown escape",
			input:    `a\qb`,
			encoding: checkerdef.PayloadEncodingEscaped,
			wantErr:  "unknown escape",
		},
		{
			name:     "escaped rejects a trailing backslash",
			input:    `abc\`,
			encoding: checkerdef.PayloadEncodingEscaped,
			wantErr:  "trailing",
		},
		{
			name:     "escaped rejects a truncated hex escape",
			input:    `ab\x4`,
			encoding: checkerdef.PayloadEncodingEscaped,
			wantErr:  "two hex digits",
		},
		{
			name:     "escaped rejects a non-hex hex escape",
			input:    `\xzz`,
			encoding: checkerdef.PayloadEncodingEscaped,
			wantErr:  "escape",
		},
		{
			name:     "hex ignores whitespace",
			input:    "5350 0100\n0001\t0000",
			encoding: checkerdef.PayloadEncodingHex,
			want:     []byte{0x53, 0x50, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00},
		},
		{
			name:     "hex rejects an odd digit count",
			input:    "535",
			encoding: checkerdef.PayloadEncodingHex,
			wantErr:  "odd number of digits",
		},
		{
			name:     "hex rejects non-hex digits",
			input:    "53zz",
			encoding: checkerdef.PayloadEncodingHex,
			wantErr:  "invalid hex payload",
		},
		{
			name:     "an unknown encoding is rejected",
			input:    "abc",
			encoding: "base64",
			wantErr:  "unknown encoding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got, err := checkerdef.DecodePayload(tt.input, tt.encoding)
			if tt.wantErr != "" {
				r.Error(err)
				r.Contains(err.Error(), tt.wantErr)

				return
			}

			r.NoError(err)
			r.Equal(tt.want, got)
		})
	}
}

func TestRenderReplyData(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Valid UTF-8 passes through untouched.
	r.Equal("220 mail.acme.com ESMTP", checkerdef.RenderReplyData([]byte("220 mail.acme.com ESMTP")))

	// A binary reply is escaped rather than mangled into U+FFFD by encoding/json.
	r.Equal(`\xff\xfeok`, checkerdef.RenderReplyData([]byte{0xff, 0xfe, 'o', 'k'}))

	// The OUTPUT field is capped at 1 KB (matching is not — see the checker tests).
	long := strings.Repeat("a", 3000)
	r.Len(checkerdef.RenderReplyData([]byte(long)), checkerdef.MaxExchangeOutputSize)
}

func TestValidateExchangeConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		fields        checkerdef.ExchangeFields
		wantErrSubstr string
	}{
		{
			name:   "an empty exchange is valid",
			fields: checkerdef.ExchangeFields{},
		},
		{
			name: "a well-formed hex exchange is valid",
			fields: checkerdef.ExchangeFields{
				SendData: "5350 0100", SendEncoding: checkerdef.PayloadEncodingHex,
				ExpectData: "53508180", ExpectEncoding: checkerdef.PayloadEncodingHex,
				ExpectPattern: `^\x53`,
			},
		},
		{
			name:          "an unknown send encoding names send_encoding",
			fields:        checkerdef.ExchangeFields{SendData: "x", SendEncoding: "base64"},
			wantErrSubstr: "send_encoding",
		},
		{
			name: "an unknown expect encoding names expect_encoding",
			fields: checkerdef.ExchangeFields{
				ExpectData: "x", ExpectEncoding: "rot13",
			},
			wantErrSubstr: "expect_encoding",
		},
		{
			name: "a malformed hex payload names send_data",
			fields: checkerdef.ExchangeFields{
				SendData: "535", SendEncoding: checkerdef.PayloadEncodingHex,
			},
			wantErrSubstr: "send_data",
		},
		{
			name:          "an uncompilable pattern names expect_pattern",
			fields:        checkerdef.ExchangeFields{ExpectPattern: "(unclosed"},
			wantErrSubstr: "expect_pattern",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			err := tt.fields.Validate()
			if tt.wantErrSubstr == "" {
				r.NoError(err)

				return
			}

			r.Error(err)
			r.Contains(err.Error(), tt.wantErrSubstr)
		})
	}
}

func TestExchangeFieldsRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	stored := map[string]any{
		"send_data":       `PING\r\n`,
		"send_encoding":   checkerdef.PayloadEncodingEscaped,
		"expect_data":     "+PONG",
		"expect_encoding": checkerdef.PayloadEncodingText,
		"expect_pattern":  `^\+PONG`,
	}

	fields := checkerdef.ExchangeFields{}
	r.NoError(fields.FromMap(stored))
	r.Equal(`PING\r\n`, fields.SendData)
	r.Equal(checkerdef.PayloadEncodingEscaped, fields.SendEncoding)
	r.Equal("+PONG", fields.ExpectData)
	r.Equal(`^\+PONG`, fields.ExpectPattern)

	out := map[string]any{}
	fields.Apply(out)
	r.Equal(stored, out)

	// A non-string value is a config error, not a silent drop.
	r.Error((&checkerdef.ExchangeFields{}).FromMap(map[string]any{"send_data": 42}))

	// An absent key leaves the field empty and is omitted on the way out.
	empty := checkerdef.ExchangeFields{}
	r.NoError(empty.FromMap(map[string]any{}))

	emptyOut := map[string]any{}
	empty.Apply(emptyOut)
	r.Empty(emptyOut)
}

func TestExchangeMatchesRunsOnTheFullBuffer(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	exchange, err := checkerdef.NewExchange(
		"", "", "needle", "", `needle`, 0, time.Time{})
	r.NoError(err)
	r.True(exchange.HasExpectation())

	// Past the 1 KB output cap — the bug this whole spec exists to fix.
	reply := append([]byte(strings.Repeat("x", 2000)), []byte("needle")...)
	r.True(exchange.Matches(reply))
	r.False(exchange.Matches([]byte(strings.Repeat("x", 2000))))

	// Both expectations must hold when both are set.
	both, err := checkerdef.NewExchange("", "", "alpha", "", "beta", 0, time.Time{})
	r.NoError(err)
	r.False(both.Matches([]byte("alpha only")))
	r.False(both.Matches([]byte("beta only")))
	r.True(both.Matches([]byte("alpha and beta")))

	// No expectation at all matches everything (and nothing fails on silence).
	none, err := checkerdef.NewExchange("hi", "", "", "", "", 0, time.Time{})
	r.NoError(err)
	r.False(none.HasExpectation())
}
