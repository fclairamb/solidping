package badges

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestAvailabilityColor(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tests := []struct {
		pct   float64
		color string
	}{
		{100.0, ColorGreen},
		{99.95, ColorGreen},
		{99.5, ColorYellow},
		{98.5, ColorOrange},
		{97.0, ColorRed},
	}

	for _, tt := range tests {
		r.Equal(tt.color, availabilityColor(tt.pct))
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	tests := []struct {
		d    time.Duration
		want string
	}{
		{25 * time.Hour, "1d"},
		{7 * 24 * time.Hour, "7d"},
		{2 * time.Hour, "2h"},
		{30 * time.Minute, "30m"},
	}

	for _, tt := range tests {
		r.Equal(tt.want, formatDuration(tt.d))
	}
}

func TestParsePeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(time.Hour, parsePeriod("1h"))
	r.Equal(24*time.Hour, parsePeriod("24h"))
	r.Equal(7*24*time.Hour, parsePeriod("7d"))
	r.Equal(30*24*time.Hour, parsePeriod("30d"))
	r.Equal(24*time.Hour, parsePeriod("invalid")) // defaults to 24h
}

func TestGenerateSVG(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svg := GenerateSVG("Test", "up", ColorGreen, "flat")
	r.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg"`)
	r.Contains(svg, "Test")
	r.Contains(svg, "up")
	r.Contains(svg, ColorGreen)
}

func TestGenerateSVGFlatSquare(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svg := GenerateSVG("Test", "up", ColorGreen, "flat-square")
	r.Contains(svg, `rx="0"`)
}

func TestEscapeXML(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal("&amp;", escapeXML("&"))
	r.Equal("&lt;", escapeXML("<"))
	r.Equal("&gt;", escapeXML(">"))
	r.Equal("&apos;", escapeXML("'"))
	r.Equal("&quot;", escapeXML(`"`))
}

func TestFormatAvailability(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal("100.00%", formatAvailability(100.0))
	r.Equal("99.99%", formatAvailability(99.99))
	r.Equal("99.9%", formatAvailability(99.9))
	r.Equal("98.5%", formatAvailability(98.5))
}

func TestParseComponents(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	t.Run("single status", func(t *testing.T) {
		t.Parallel()

		tokens, err := parseComponents("status")
		r.NoError(err)
		r.Equal([]string{"status"}, tokens)
	})

	t.Run("single availability", func(t *testing.T) {
		t.Parallel()

		tokens, err := parseComponents("availability")
		r.NoError(err)
		r.Equal([]string{"availability"}, tokens)
	})

	t.Run("multi component all four", func(t *testing.T) {
		t.Parallel()

		tokens, err := parseComponents("status,availability,duration,response-time")
		r.NoError(err)
		r.Equal([]string{"status", "availability", "duration", "response-time"}, tokens)
	})

	t.Run("status and duration", func(t *testing.T) {
		t.Parallel()

		tokens, err := parseComponents("status,duration")
		r.NoError(err)
		r.Equal([]string{"status", "duration"}, tokens)
	})

	t.Run("availability and response-time", func(t *testing.T) {
		t.Parallel()

		tokens, err := parseComponents("availability,response-time")
		r.NoError(err)
		r.Equal([]string{"availability", "response-time"}, tokens)
	})

	t.Run("legacy availability-duration returns ErrInvalidFormat", func(t *testing.T) {
		t.Parallel()

		_, err := parseComponents("availability-duration")
		r.ErrorIs(err, ErrInvalidFormat)
	})

	t.Run("unknown token returns ErrInvalidFormat", func(t *testing.T) {
		t.Parallel()

		_, err := parseComponents("status,unknown-token")
		r.ErrorIs(err, ErrInvalidFormat)
	})

	t.Run("duplicate token returns ErrInvalidFormat", func(t *testing.T) {
		t.Parallel()

		_, err := parseComponents("status,status")
		r.ErrorIs(err, ErrInvalidFormat)
	})

	t.Run("missing primary returns ErrInvalidFormat", func(t *testing.T) {
		t.Parallel()

		_, err := parseComponents("duration")
		r.ErrorIs(err, ErrInvalidFormat)
	})

	t.Run("duration only returns ErrInvalidFormat", func(t *testing.T) {
		t.Parallel()

		_, err := parseComponents("duration,response-time")
		r.ErrorIs(err, ErrInvalidFormat)
	})
}

func TestFormatResponseTime(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	t.Run("no results returns n/a", func(t *testing.T) {
		t.Parallel()

		r.Equal("n/a", formatResponseTime(nil))
	})

	t.Run("no duration fields returns n/a", func(t *testing.T) {
		t.Parallel()

		results := []*models.Result{{}}
		r.Equal("n/a", formatResponseTime(results))
	})

	t.Run("sub-second response time formatted as ms", func(t *testing.T) {
		t.Parallel()

		d1 := float32(63.0) // 63ms
		d2 := float32(65.0) // 65ms
		results := []*models.Result{
			{Duration: &d1},
			{Duration: &d2},
		}
		// mean = 64ms
		r.Equal("64ms", formatResponseTime(results))
	})

	t.Run("over-second response time formatted with one decimal", func(t *testing.T) {
		t.Parallel()

		d1 := float32(1500.0) // 1500ms = 1.5s
		d2 := float32(2500.0) // 2500ms = 2.5s
		results := []*models.Result{
			{Duration: &d1},
			{Duration: &d2},
		}
		// mean = 2000ms = 2.0s
		r.Equal("2.0s", formatResponseTime(results))
	})

	t.Run("nil durations are skipped", func(t *testing.T) {
		t.Parallel()

		d := float32(250.0) // 250ms
		results := []*models.Result{
			{Duration: nil},
			{Duration: &d},
		}
		r.Equal("250ms", formatResponseTime(results))
	})
}

func TestCalculateStatusDuration(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now()

	statusUp := int(models.ResultStatusUp)
	statusDown := int(models.ResultStatusDown)

	t.Run("up arrow when last result is up", func(t *testing.T) {
		t.Parallel()

		results := []*models.Result{
			{Status: &statusUp, PeriodStart: now.Add(-2 * time.Minute)},
		}
		dur, isUp, ok := calculateStatusDuration(results)
		r.True(ok)
		r.True(isUp)
		r.Greater(dur, time.Duration(0))
	})

	t.Run("down arrow when last result is down", func(t *testing.T) {
		t.Parallel()

		results := []*models.Result{
			{Status: &statusDown, PeriodStart: now.Add(-5 * time.Minute)},
		}
		dur, isUp, ok := calculateStatusDuration(results)
		r.True(ok)
		r.False(isUp)
		r.Greater(dur, time.Duration(0))
	})

	t.Run("unknown when no results", func(t *testing.T) {
		t.Parallel()

		_, _, ok := calculateStatusDuration(nil)
		r.False(ok)
	})

	t.Run("unknown when only created/running results", func(t *testing.T) {
		t.Parallel()

		created := int(models.ResultStatusCreated)
		results := []*models.Result{
			{Status: &created, PeriodStart: now.Add(-1 * time.Minute)},
		}
		_, _, ok := calculateStatusDuration(results)
		r.False(ok)
	})

	t.Run("duration reflects time since status change", func(t *testing.T) {
		t.Parallel()

		// 3 up results; status changed from down to up 2 results ago
		results := []*models.Result{
			{Status: &statusUp, PeriodStart: now.Add(-1 * time.Minute)},
			{Status: &statusUp, PeriodStart: now.Add(-6 * time.Minute)},
			{Status: &statusDown, PeriodStart: now.Add(-11 * time.Minute)},
		}
		dur, isUp, ok := calculateStatusDuration(results)
		r.True(ok)
		r.True(isUp)
		// Duration should be approximately 6 minutes (since oldest up result)
		r.Greater(dur, 5*time.Minute)
	})
}
