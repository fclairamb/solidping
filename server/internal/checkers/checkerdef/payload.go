package checkerdef

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Payload encodings shared by every checker that sends a configurable payload
// and asserts on the reply (`tcp`, `udp`). One decoder, one set of names: a
// `send_encoding` means the same thing everywhere.
const (
	// PayloadEncodingText passes the string through byte-for-byte. It is the
	// default, so a stored `send_data` keeps its exact meaning.
	PayloadEncodingText = "text"
	// PayloadEncodingEscaped decodes C-style escapes (the Nagios
	// `check_tcp --escape` convention), which is the only way a <textarea> can
	// express a CRLF.
	PayloadEncodingEscaped = "escaped"
	// PayloadEncodingHex decodes hex digits, whitespace ignored. For binary
	// protocols: DNS, NTP, SNMP, RCON.
	PayloadEncodingHex = "hex"
)

const (
	// MaxExchangeReadSize caps how much of a reply is accumulated AND matched.
	// This is a liveness probe, not a body fetch.
	MaxExchangeReadSize = 4 * 1024
	// MaxExchangeOutputSize caps the `received_data` OUTPUT field only —
	// matching always runs on the full accumulated buffer.
	MaxExchangeOutputSize = 1024

	hexEscapeDigits    = 2
	asciiPrintableLow  = 0x20
	asciiPrintableHigh = 0x7e
)

// PayloadEncodings lists the accepted encoding names, in the order the
// dashboard offers them.
var PayloadEncodings = []string{PayloadEncodingText, PayloadEncodingEscaped, PayloadEncodingHex}

var errTrailingBackslash = errors.New(`trailing "\" with nothing to escape`)

// DecodePayload turns a configured payload string into the bytes to put on the
// wire (or to look for in a reply), according to `encoding`. An empty encoding
// means `text`, so every config written before encodings existed decodes to
// exactly what it decoded to before.
func DecodePayload(s, encoding string) ([]byte, error) {
	switch encoding {
	case "", PayloadEncodingText:
		return []byte(s), nil
	case PayloadEncodingEscaped:
		return decodeEscaped(s)
	case PayloadEncodingHex:
		return decodeHex(s)
	default:
		return nil, fmt.Errorf( //nolint:err113 // message is surfaced verbatim as a config error
			"unknown encoding %q, must be one of %s", encoding, strings.Join(PayloadEncodings, ", "))
	}
}

// decodeEscaped understands exactly the escapes the config documents:
// `\r`, `\n`, `\t`, `\0`, `\\` and `\xNN`. Anything else is a typo the operator
// wants to hear about at save time, not a silent literal backslash on the wire.
func decodeEscaped(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))

	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			out = append(out, s[i])

			continue
		}

		i++
		if i >= len(s) {
			return nil, errTrailingBackslash
		}

		switch s[i] {
		case 'r':
			out = append(out, '\r')
		case 'n':
			out = append(out, '\n')
		case 't':
			out = append(out, '\t')
		case '0':
			out = append(out, 0)
		case '\\':
			out = append(out, '\\')
		case 'x':
			if i+hexEscapeDigits >= len(s) {
				return nil, fmt.Errorf( //nolint:err113 // surfaced verbatim as a config error
					`"\x" at offset %d needs two hex digits`, i-1)
			}

			b, err := hex.DecodeString(s[i+1 : i+1+hexEscapeDigits])
			if err != nil {
				return nil, fmt.Errorf( //nolint:err113 // surfaced verbatim as a config error
					`invalid "\x%s" escape at offset %d`, s[i+1:i+1+hexEscapeDigits], i-1)
			}

			out = append(out, b[0])
			i += hexEscapeDigits
		default:
			return nil, fmt.Errorf( //nolint:err113 // surfaced verbatim as a config error
				`unknown escape "\%c" at offset %d`, s[i], i-1)
		}
	}

	return out, nil
}

// decodeHex ignores whitespace so a payload can be written in readable groups
// ("5350 0100 0001"), and rejects an odd digit count rather than guessing which
// nibble was meant.
func decodeHex(s string) ([]byte, error) {
	var compact strings.Builder

	compact.Grow(len(s))

	for _, r := range s {
		switch r {
		case ' ', '\t', '\r', '\n':
		default:
			compact.WriteRune(r)
		}
	}

	cleaned := compact.String()
	if len(cleaned)%hexEscapeDigits != 0 {
		return nil, fmt.Errorf( //nolint:err113 // surfaced verbatim as a config error
			"hex payload has an odd number of digits (%d)", len(cleaned))
	}

	out, err := hex.DecodeString(cleaned)
	if err != nil {
		return nil, fmt.Errorf("invalid hex payload: %w", err)
	}

	return out, nil
}

// RenderReplyData produces the `received_data` output field: the reply capped
// at 1 KB, raw when that capped copy is valid UTF-8 and `\xNN`-escaped when it
// is not. Without the escaping, encoding/json replaces every invalid sequence
// with U+FFFD and the operator sees `?????` where the diagnostic bytes were.
func RenderReplyData(b []byte) string {
	if len(b) > MaxExchangeOutputSize {
		b = b[:MaxExchangeOutputSize]
	}

	if utf8.Valid(b) {
		return string(b)
	}

	return EscapeBytes(b)
}

// EscapeBytes renders arbitrary bytes readably: printable ASCII as itself, the
// usual control characters as their short escape, everything else as `\xNN`.
func EscapeBytes(b []byte) string {
	var out strings.Builder

	out.Grow(len(b))

	for _, c := range b {
		switch {
		case c == '\\':
			out.WriteString(`\\`)
		case c == '\r':
			out.WriteString(`\r`)
		case c == '\n':
			out.WriteString(`\n`)
		case c == '\t':
			out.WriteString(`\t`)
		case c >= asciiPrintableLow && c <= asciiPrintableHigh:
			out.WriteByte(c)
		default:
			fmt.Fprintf(&out, `\x%02x`, c)
		}
	}

	return out.String()
}

// ExchangeFields is the send/expect half of a `tcp` or `udp` config. The two
// checkers parse and serialize it identically, so they parse and serialize it
// HERE — a key added on one side can no longer go missing on the other.
type ExchangeFields struct {
	SendData       string
	SendEncoding   string
	ExpectData     string
	ExpectEncoding string
	ExpectPattern  string
}

// exchangeKeys maps each config key to where it lands on ExchangeFields.
func (f *ExchangeFields) targets() map[string]*string {
	return map[string]*string{
		"send_data":       &f.SendData,
		"send_encoding":   &f.SendEncoding,
		"expect_data":     &f.ExpectData,
		"expect_encoding": &f.ExpectEncoding,
		"expect_pattern":  &f.ExpectPattern,
	}
}

// FromMap reads the send/expect keys out of a raw config map.
func (f *ExchangeFields) FromMap(configMap map[string]any) error {
	for key, target := range f.targets() {
		value, present := configMap[key]
		if !present || value == nil {
			continue
		}

		str, ok := value.(string)
		if !ok {
			return NewConfigError(key, "must be a string")
		}

		*target = str
	}

	return nil
}

// Apply writes the non-empty send/expect keys back into a config map.
func (f *ExchangeFields) Apply(cfg map[string]any) {
	for key, source := range f.targets() {
		if *source != "" {
			cfg[key] = *source
		}
	}
}

// Validate is ValidateExchangeConfig over the parsed fields.
func (f *ExchangeFields) Validate() error {
	return ValidateExchangeConfig(f.SendData, f.SendEncoding, f.ExpectData, f.ExpectEncoding, f.ExpectPattern)
}

// ValidateExchangeConfig checks the send/expect half of a `tcp` or `udp`
// config at SAVE time, so a bad encoding or an uncompilable pattern is a
// VALIDATION_ERROR on the API rather than a check that errors forever.
func ValidateExchangeConfig(sendData, sendEncoding, expectData, expectEncoding, expectPattern string) error {
	if _, err := DecodePayload(sendData, sendEncoding); err != nil {
		field := "send_data"
		if sendEncoding != "" && !validEncoding(sendEncoding) {
			field = "send_encoding"
		}

		return NewConfigError(field, err.Error())
	}

	if _, err := DecodePayload(expectData, expectEncoding); err != nil {
		field := "expect_data"
		if expectEncoding != "" && !validEncoding(expectEncoding) {
			field = "expect_encoding"
		}

		return NewConfigError(field, err.Error())
	}

	if expectPattern != "" {
		if _, err := regexp.Compile(expectPattern); err != nil {
			return NewConfigErrorf("expect_pattern", "invalid regex: %v", err)
		}
	}

	return nil
}

func validEncoding(encoding string) bool {
	for _, e := range PayloadEncodings {
		if e == encoding {
			return true
		}
	}

	return false
}

// matchesExpectation reports whether the accumulated reply satisfies both
// expectations. Either may be unset; both must hold when set.
func matchesExpectation(buf, expectData []byte, expectPattern *regexp.Regexp) bool {
	if len(expectData) > 0 && !bytes.Contains(buf, expectData) {
		return false
	}

	if expectPattern != nil && !expectPattern.Match(buf) {
		return false
	}

	return true
}
