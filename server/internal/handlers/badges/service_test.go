package badges

import (
	"strings"
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

func TestUptimeBarColor(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	tests := []struct {
		pct   float64
		color string
	}{
		{100.0, ColorGreen},
		{99.95, ColorGreen},
		{99.9, ColorGreen},
		{99.5, ColorYellow},
		{99.0, ColorYellow},
		{98.5, ColorOrange},
		{98.0, ColorOrange},
		{97.0, ColorRed},
		{0.0, ColorRed},
	}

	for _, tt := range tests {
		r.Equal(tt.color, uptimeBarColor(tt.pct))
	}
}

func TestUptimeBarPeriodInfo(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	t.Run("24h produces 24 hourly segments", func(t *testing.T) {
		t.Parallel()

		pt, n, d := uptimeBarPeriodInfo("24h")
		r.Equal(models.PeriodTypeHour, pt)
		r.Equal(24, n)
		r.Equal(time.Hour, d)
	})

	t.Run("7d produces 7 daily segments", func(t *testing.T) {
		t.Parallel()

		pt, n, d := uptimeBarPeriodInfo("7d")
		r.Equal(models.PeriodTypeDay, pt)
		r.Equal(7, n)
		r.Equal(24*time.Hour, d)
	})

	t.Run("30d produces 30 daily segments", func(t *testing.T) {
		t.Parallel()

		pt, n, d := uptimeBarPeriodInfo("30d")
		r.Equal(models.PeriodTypeDay, pt)
		r.Equal(30, n)
		r.Equal(24*time.Hour, d)
	})

	t.Run("90d produces 90 daily segments", func(t *testing.T) {
		t.Parallel()

		pt, n, d := uptimeBarPeriodInfo("90d")
		r.Equal(models.PeriodTypeDay, pt)
		r.Equal(90, n)
		r.Equal(24*time.Hour, d)
	})

	t.Run("unknown defaults to 30d", func(t *testing.T) {
		t.Parallel()

		pt, n, d := uptimeBarPeriodInfo("invalid")
		r.Equal(models.PeriodTypeDay, pt)
		r.Equal(30, n)
		r.Equal(24*time.Hour, d)
	})
}

func TestGenerateUptimeBarSVG(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	t.Run("all green segments", func(t *testing.T) {
		t.Parallel()

		segments := []string{ColorGreen, ColorGreen, ColorGreen}
		svg := GenerateUptimeBarSVG(segments, 300, 20, "flat")
		r.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg"`)
		r.Equal(3, strings.Count(svg, `<rect x=`))
		r.Contains(svg, ColorGreen)
		r.Contains(svg, `rx="3"`)
	})

	t.Run("flat-square style has rx=0", func(t *testing.T) {
		t.Parallel()

		segments := []string{ColorGreen}
		svg := GenerateUptimeBarSVG(segments, 100, 20, "flat-square")
		r.Contains(svg, `rx="0"`)
	})

	t.Run("missing bucket shows gray", func(t *testing.T) {
		t.Parallel()

		segments := []string{ColorGreen, ColorGray, ColorGreen}
		svg := GenerateUptimeBarSVG(segments, 300, 20, "flat")
		r.Contains(svg, ColorGray)
	})

	t.Run("empty segments returns minimal SVG", func(t *testing.T) {
		t.Parallel()

		svg := GenerateUptimeBarSVG(nil, 300, 20, "flat")
		r.Contains(svg, `<svg`)
		r.NotContains(svg, `<rect`)
	})

	t.Run("correct segment count for 30 buckets", func(t *testing.T) {
		t.Parallel()

		segments := make([]string, 30)
		for i := range 30 {
			segments[i] = ColorGreen
		}

		svg := GenerateUptimeBarSVG(segments, 300, 20, "flat")
		r.Equal(30, strings.Count(svg, `<rect x=`))
	})

	t.Run("correct segment count for 90 buckets", func(t *testing.T) {
		t.Parallel()

		segments := make([]string, 90)
		for i := range 90 {
			segments[i] = ColorGreen
		}

		svg := GenerateUptimeBarSVG(segments, 300, 20, "flat")
		r.Equal(90, strings.Count(svg, `<rect x=`))
	})

	t.Run("width and height are reflected in SVG", func(t *testing.T) {
		t.Parallel()

		segments := []string{ColorGreen}
		svg := GenerateUptimeBarSVG(segments, 600, 30, "flat")
		r.Contains(svg, `width="600"`)
		r.Contains(svg, `height="30"`)
	})
}
