package heartbeatpush_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/heartbeatpush"
)

// TestParseAnnotationHappyPath is the positive control: the shape the docs
// advertise splits into a status word, numeric metrics and text fields.
func TestParseAnnotationHappyPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := heartbeatpush.ParseAnnotation("started volts=3.71 rssi=-67 fw=1.4.2")
	r.Equal("started", got.Status)
	r.Equal(map[string]float64{"volts": 3.71, "rssi": -67}, got.Numeric)
	r.Equal(map[string]string{"fw": "1.4.2"}, got.Text)
	r.Empty(got.Raw)
	r.False(got.IsEmpty())
}

func TestParseAnnotationVariants(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		input   string
		status  string
		numeric map[string]float64
		text    map[string]string
	}{
		{"empty", "", "", nil, nil},
		{"whitespace", "   ", "", nil, nil},
		{"status only", "started", "started", nil, nil},
		{"pairs only", "volts=3.3", "", map[string]float64{"volts": 3.3}, nil},
		{"integer stays numeric", "count=12", "", map[string]float64{"count": 12}, nil},
		{"exponent stays numeric", "x=1e3", "", map[string]float64{"x": 1000}, nil},
		{"value may contain =", "", "", nil, map[string]string{"k": "a=b"}},
	} {
		r := require.New(t)

		input := tc.input
		if tc.name == "value may contain =" {
			input = "k=a=b"
		}

		got := heartbeatpush.ParseAnnotation(input)
		r.Equal(tc.status, got.Status, tc.name)
		r.Equal(tc.numeric, got.Numeric, tc.name)
		r.Equal(tc.text, got.Text, tc.name)
		r.Empty(got.Raw, tc.name)
	}
}

// TestParseAnnotationNeverFailsABeat is the load-bearing property: every
// grammar violation degrades to Raw, so no annotation can ever make a healthy
// device look dead.
func TestParseAnnotationNeverFailsABeat(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		input string
	}{
		{"bare word after the first", "started alive"},
		{"bare word after a pair", "volts=3.3 alive"},
		{"uppercase key", "Volts=3.3"},
		{"key with a dash", "batt-volts=3.3"},
		{"empty key", "=3.3"},
		{"key too long", strings.Repeat("k", 33) + "=1"},
		{"value too long", "k=" + strings.Repeat("v", 65)},
		{"too many pairs", "a=1 b=2 c=3 d=4 e=5 f=6 g=7 h=8 i=9 j=10 k=11"},
		{"duplicate key", "a=1 a=2"},
		{"status word too long", strings.Repeat("s", 33)},
		{"status word with a slash", "a/b"},
		{"over the byte cap", strings.Repeat("a=1 ", 40)},
	} {
		r := require.New(t)

		got := heartbeatpush.ParseAnnotation(tc.input)
		r.NotEmpty(got.Raw, tc.name)
		r.Empty(got.Status, tc.name)
		r.Empty(got.Numeric, tc.name)
		r.Empty(got.Text, tc.name)
		r.False(got.IsEmpty(), tc.name)
	}
}

// TestParseAnnotationRejectsNonFiniteNumbers keeps NaN/Inf out of the metrics
// jsonb, where they would break both JSON encoding and every rollup that
// touches them. They fall through to text rather than being dropped.
func TestParseAnnotationRejectsNonFiniteNumbers(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, value := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "infinity"} {
		got := heartbeatpush.ParseAnnotation("v=" + value)
		r.Empty(got.Numeric, value)
		r.Equal(map[string]string{"v": value}, got.Text, value)
	}

	// Positive control: a finite number does land in Numeric.
	r.Equal(map[string]float64{"v": 1.5}, heartbeatpush.ParseAnnotation("v=1.5").Numeric)
}

// TestParseAnnotationStripsControlCharacters — annotation bytes are untrusted
// input that ends up in logs, JSON and the dashboard. A newline or an escape
// sequence must never survive the parse.
func TestParseAnnotationStripsControlCharacters(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := heartbeatpush.ParseAnnotation("k=a\x1b[31mb\x00c")
	r.Equal("a[31mbc", got.Text["k"])
	r.NotContains(got.Text["k"], "\x1b")
	r.NotContains(got.Text["k"], "\x00")

	// The Raw fallback is sanitized too — it is the path an attacker controls
	// most freely, since anything that fails the grammar lands there.
	raw := heartbeatpush.ParseAnnotation("not a pair\x07 \x1b]0;title\x07").Raw
	r.NotContains(raw, "\x07")
	r.NotContains(raw, "\x1b")
}

// TestParseAnnotationCapsRawLength bounds what one beat can write even when
// the grammar fails.
func TestParseAnnotationCapsRawLength(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	raw := heartbeatpush.ParseAnnotation(strings.Repeat("x", 400)).Raw
	r.Len(raw, heartbeatpush.MaxAnnotationBytes)
}
