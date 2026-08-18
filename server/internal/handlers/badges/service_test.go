package badges

import (
	"context"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
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
	r.Equal(24*time.Hour, parsePeriod("24h"))
	r.Equal(7*24*time.Hour, parsePeriod("7d"))
	r.Equal(30*24*time.Hour, parsePeriod("30d"))
	r.Equal(90*24*time.Hour, parsePeriod("90d"))
	r.Equal(30*24*time.Hour, parsePeriod("invalid")) // defaults to 30d
}

func TestGenerateSVG(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svg := GenerateSVG("Test", "up", ColorGreen, "flat", 0)
	r.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg"`)
	r.Contains(svg, "Test")
	r.Contains(svg, "up")
	r.Contains(svg, ColorGreen)
}

func TestGenerateSVGFlatSquare(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svg := GenerateSVG("Test", "up", ColorGreen, "flat-square", 0)
	r.Contains(svg, `rx="0"`)
}

func TestGenerateSVGMinWidth(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	t.Run("minWidth=200 pads narrow badge", func(t *testing.T) {
		t.Parallel()

		svg := GenerateSVG("check", "up", ColorGreen, "flat", 200)
		r.Contains(svg, `width="200"`)
	})

	t.Run("minWidth=50 has no effect on wider badge", func(t *testing.T) {
		t.Parallel()

		// "check name" label (10 chars) = 10*6+10 = 70; "up" value = 2*6+10 = 22; total = 92 > 50
		svg := GenerateSVG("check name", "up", ColorGreen, "flat", 50)
		r.Contains(svg, `width="92"`)
	})

	t.Run("minWidth=0 behaves like default", func(t *testing.T) {
		t.Parallel()

		svgDefault := GenerateSVG("check", "up", ColorGreen, "flat", 0)
		svgMinZero := GenerateSVG("check", "up", ColorGreen, "flat", 0)
		r.Equal(svgDefault, svgMinZero)
	})
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

	t.Run("duration alone is valid (primary no longer required)", func(t *testing.T) {
		t.Parallel()

		tokens, err := parseComponents("duration")
		r.NoError(err)
		r.Equal([]string{"duration"}, tokens)
	})

	t.Run("uptime-bar alone is valid (no text metric required)", func(t *testing.T) {
		t.Parallel()

		tokens, err := parseComponents("uptime-bar")
		r.NoError(err)
		r.Equal([]string{"uptime-bar"}, tokens)
	})

	t.Run("response-time-graph alone is valid", func(t *testing.T) {
		t.Parallel()

		tokens, err := parseComponents("response-time-graph")
		r.NoError(err)
		r.Equal([]string{"response-time-graph"}, tokens)
	})

	t.Run("all six tokens preserve order", func(t *testing.T) {
		t.Parallel()

		tokens, err := parseComponents("status,availability,duration,response-time,uptime-bar,response-time-graph")
		r.NoError(err)
		r.Equal([]string{
			"status", "availability", "duration", "response-time", "uptime-bar", "response-time-graph",
		}, tokens)
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

	t.Run("abandoned newest row is skipped, not read as the current status", func(t *testing.T) {
		t.Parallel()

		// Spec 2026-08-18-10: the reaper's row means OUR worker died, not that
		// the target went down. The badge must fall through it to the last
		// real observation — under the previous encoding this row carried
		// status=error and the badge announced a fake outage.
		abandoned := int(models.ResultStatusAbandoned)
		results := []*models.Result{
			{Status: &abandoned, PeriodStart: now.Add(-1 * time.Minute)},
			{Status: &statusUp, PeriodStart: now.Add(-6 * time.Minute)},
		}
		_, isUp, ok := calculateStatusDuration(results)
		r.True(ok)
		r.True(isUp, "the abandoned row must not turn an up check into a down badge")

		// Positive control: the very same shape with a GENUINE error in the
		// newest slot DOES read as down — proving the skip is specific to
		// abandoned, not a bug that ignores failures in general.
		errored := int(models.ResultStatusError)
		control := []*models.Result{
			{Status: &errored, PeriodStart: now.Add(-1 * time.Minute)},
			{Status: &statusUp, PeriodStart: now.Add(-6 * time.Minute)},
		}
		_, controlUp, controlOK := calculateStatusDuration(control)
		r.True(controlOK)
		r.False(controlUp, "a genuine error must still read as down")
	})

	t.Run("unknown when only abandoned results", func(t *testing.T) {
		t.Parallel()

		abandoned := int(models.ResultStatusAbandoned)
		results := []*models.Result{
			{Status: &abandoned, PeriodStart: now.Add(-1 * time.Minute)},
		}
		_, _, ok := calculateStatusDuration(results)
		r.False(ok, "a badge with nothing but reaped attempts has no known status")
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

func TestRenderBadgeRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	t.Run("with value renders label and value cells", func(t *testing.T) {
		t.Parallel()

		row := renderBadgeRow("My Check", "up", ColorGreen, "flat", 0, 0)
		r.Contains(row, "<g transform=\"translate(0,0)\">")
		r.Contains(row, "My Check")
		r.Contains(row, "up")
		r.Contains(row, ColorGreen)
	})

	t.Run("empty value renders black title without value cell", func(t *testing.T) {
		t.Parallel()

		row := renderBadgeRow("My Check", "", ColorGray, "flat", 0, 0)
		r.Contains(row, "My Check")
		r.Contains(row, ColorTitle)
		r.NotContains(row, ColorGray) // no value cell, so the value color is absent
		r.NotContains(row, "clipPath")
	})

	t.Run("y offset is applied to the group", func(t *testing.T) {
		t.Parallel()

		row := renderBadgeRow("X", "up", ColorGreen, "flat", 0, 24)
		r.Contains(row, "translate(0,24)")
	})
}

func TestComposeBadgeSVG(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	svg := ComposeBadgeSVG([]string{"<g>a</g>", "<g>b</g>"}, 300, 88)
	r.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg" width="300" height="88">`)
	r.Contains(svg, "<g>a</g>")
	r.Contains(svg, "<g>b</g>")
	r.Contains(svg, "</svg>")
}

func TestBuildGraphSegments(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	f := func(v float64) *float64 { return &v }

	t.Run("all present is one segment", func(t *testing.T) {
		t.Parallel()

		segs := buildGraphSegments([]*float64{f(1), f(2), f(3)})
		r.Equal([][]int{{0, 1, 2}}, segs)
	})

	t.Run("nil gap breaks into two segments", func(t *testing.T) {
		t.Parallel()

		segs := buildGraphSegments([]*float64{f(1), nil, f(3)})
		r.Equal([][]int{{0}, {2}}, segs)
	})

	t.Run("leading and trailing nils are skipped", func(t *testing.T) {
		t.Parallel()

		segs := buildGraphSegments([]*float64{nil, f(2), f(3), nil})
		r.Equal([][]int{{1, 2}}, segs)
	})

	t.Run("all nil produces no segments", func(t *testing.T) {
		t.Parallel()

		segs := buildGraphSegments([]*float64{nil, nil})
		r.Empty(segs)
	})
}

func TestRenderResponseTimeGraphRow(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	f := func(v float64) *float64 { return &v }

	t.Run("multiple points render a polyline and area", func(t *testing.T) {
		t.Parallel()

		row := renderResponseTimeGraphRow([]*float64{f(100), f(200), f(150)}, 300, 40, 0, "flat")
		r.Contains(row, "<polyline")
		r.Contains(row, "<path")
		r.Contains(row, "linearGradient")
		r.Contains(row, "translate(0,0)")
	})

	t.Run("renders two gridlines with value labels for a varying series", func(t *testing.T) {
		t.Parallel()

		row := renderResponseTimeGraphRow([]*float64{f(100), f(304), f(150)}, 300, 40, 0, "flat")
		// One line at actualMax, one at actualMin.
		r.Equal(2, strings.Count(row, "<line "))
		r.Contains(row, `stroke="#ccc"`)
		r.Contains(row, `stroke-dasharray="2,2"`)
		// Value labels for the unpadded min/max.
		r.Contains(row, ">304ms<")
		r.Contains(row, ">100ms<")
		r.Contains(row, `fill="#888"`)
		r.Contains(row, `text-anchor="end"`)
		// Grid renders before the area/line data.
		r.Less(strings.Index(row, "<line "), strings.Index(row, "<path"))
		r.Less(strings.Index(row, "<line "), strings.Index(row, "<polyline"))
	})

	t.Run("flat series renders a single gridline", func(t *testing.T) {
		t.Parallel()

		row := renderResponseTimeGraphRow([]*float64{f(150), f(150), f(150)}, 300, 40, 0, "flat")
		r.Equal(1, strings.Count(row, "<line "))
		r.Equal(1, strings.Count(row, ">150ms<"))
	})

	t.Run("single point renders a single gridline", func(t *testing.T) {
		t.Parallel()

		row := renderResponseTimeGraphRow([]*float64{nil, f(200), nil}, 300, 40, 0, "flat")
		r.Equal(1, strings.Count(row, "<line "))
		r.Contains(row, ">200ms<")
	})

	t.Run("no data renders no gridlines", func(t *testing.T) {
		t.Parallel()

		row := renderResponseTimeGraphRow([]*float64{nil, nil}, 300, 40, 0, "flat")
		r.NotContains(row, "<line ")
		r.NotContains(row, "<text")
	})

	t.Run("nil gap produces a line break (two polylines)", func(t *testing.T) {
		t.Parallel()

		row := renderResponseTimeGraphRow([]*float64{f(100), f(200), nil, f(150), f(120)}, 300, 40, 0, "flat")
		r.Equal(2, strings.Count(row, "<polyline"))
	})

	t.Run("single value renders a dot, no polyline", func(t *testing.T) {
		t.Parallel()

		row := renderResponseTimeGraphRow([]*float64{nil, f(150), nil}, 300, 40, 0, "flat")
		r.Contains(row, "<circle")
		r.NotContains(row, "<polyline")
	})

	t.Run("no data renders a framed empty area", func(t *testing.T) {
		t.Parallel()

		row := renderResponseTimeGraphRow([]*float64{nil, nil}, 300, 40, 0, "flat")
		r.NotContains(row, "<polyline")
		r.NotContains(row, "<circle")
		r.Contains(row, "<rect")
	})

	t.Run("y-scaling keeps points within the row height", func(t *testing.T) {
		t.Parallel()

		// Min/max auto-scale with 10%% padding: the highest value must map near
		// the top (small y) and lowest near the bottom (large y), both inside
		// [0, height]. Use a non-default height to exercise the height param.
		row := renderResponseTimeGraphRow([]*float64{f(100), f(300)}, 300, 60, 0, "flat")
		r.Contains(row, "<polyline")
		// Padding means neither endpoint sits exactly at y=0 or y=60.
		r.NotContains(row, ",0.0 ")
	})
}

func TestBuildBarSegments(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	bucketStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bucketDuration := 24 * time.Hour

	availMap := map[time.Time]float64{
		bucketStart:                         100.0,
		bucketStart.Add(2 * bucketDuration): 97.0,
	}

	segs := buildBarSegments(availMap, bucketStart, 3, bucketDuration)
	r.Len(segs, 3)
	r.Equal(ColorGreen, segs[0]) // 100% → green
	r.Equal(ColorGray, segs[1])  // missing bucket → gray
	r.Equal(ColorRed, segs[2])   // 97% → red
}

func TestBuildGraphPoints(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	bucketStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	bucketDuration := 24 * time.Hour

	v := 150.0
	durationMap := map[time.Time]*float64{
		bucketStart.Add(1 * bucketDuration): &v,
	}

	points := buildGraphPoints(durationMap, bucketStart, 3, bucketDuration)
	r.Len(points, 3)
	r.Nil(points[0])
	r.NotNil(points[1])
	r.InDelta(150.0, *points[1], 0.01)
	r.Nil(points[2])
}

// setupBadgeResultsTest spins up an in-memory DB with one org + one check and a
// badge Service, so fetchBucketData can be exercised against the real ListResults
// query path (the same shared uptimebar union the status page uses).
func setupBadgeResultsTest(t *testing.T) (context.Context, *Service, *models.Organization, *models.Check) {
	t.Helper()

	ctx := t.Context()

	dbService, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbService.Initialize(ctx))
	t.Cleanup(func() { _ = dbService.Close() })

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, dbService.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "API", "http")
	require.NoError(t, dbService.CreateCheck(ctx, check))

	return ctx, NewService(dbService, &config.Config{}), org, check
}

// TestFetchBucketData_MixesRawAndRollup verifies that the badge, via the shared
// uptimebar union, mixes a raw row and an hour rollup row in the same bucket into
// correctly weighted availability and average duration.
func TestFetchBucketData_MixesRawAndRollup(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org, check := setupBadgeResultsTest(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)

	// 1 raw row (up, 100ms) + 1 hour row (9 checks, 8 up, avg 200ms) in one hour.
	// Expected availability = 9/10 = 90%; weighted avg = (100*1 + 200*9)/10 = 190ms.
	rawUp := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 100)
	rawUp.PeriodStart = currentHour.Add(time.Minute)
	r.NoError(svc.dbSvc.CreateResult(ctx, rawUp))

	total, successful := 9, 8
	avgDur := float32(200.0)
	hourRow := models.NewResult(org.UID, check.UID, models.ResultStatusUp, 0)
	hourRow.PeriodType = models.PeriodTypeHour
	hourRow.PeriodStart = currentHour
	hourRow.Status = nil
	hourRow.Duration = nil
	hourRow.TotalChecks = &total
	hourRow.SuccessfulChecks = &successful
	hourRow.DurationAvg = &avgDur
	r.NoError(svc.dbSvc.CreateResult(ctx, hourRow))

	availMap, durationMap, _, err := svc.fetchBucketData(ctx, org.UID, check.UID, "24h")
	r.NoError(err)

	r.InDelta(90.0, availMap[currentHour], 0.001)
	r.NotNil(durationMap[currentHour])
	r.InDelta(190.0, *durationMap[currentHour], 0.001)
}

// TestFetchBucketData_WarningCountsAsUp pins the intended badge behavior change:
// the raw path now counts Warning as up (CountsAsUp), aligning the badge's raw
// tier with its own rolled-up tier and the aggregation job. Created/running
// lifecycle markers stay excluded from the denominator.
func TestFetchBucketData_WarningCountsAsUp(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org, check := setupBadgeResultsTest(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)

	insert := func(st models.ResultStatus, off time.Duration) {
		res := models.NewResult(org.UID, check.UID, st, 0)
		res.PeriodStart = currentHour.Add(off)
		r.NoError(svc.dbSvc.CreateResult(ctx, res))
	}

	insert(models.ResultStatusUp, time.Minute)
	insert(models.ResultStatusWarning, 2*time.Minute) // counts as up
	insert(models.ResultStatusDown, 3*time.Minute)
	insert(models.ResultStatusCreated, 4*time.Minute) // lifecycle → excluded
	insert(models.ResultStatusRunning, 5*time.Minute) // lifecycle → excluded

	availMap, _, _, err := svc.fetchBucketData(ctx, org.UID, check.UID, "24h")
	r.NoError(err)

	// 2 up/warning of 3 countable = 66.67%.
	r.InDelta(66.6667, availMap[currentHour], 0.01)
}

func TestComputeUptimeBarLabels7d(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Wednesday 2026-01-07 as bucketStart.
	bucketStart := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	labels := computeUptimeBarLabels(bucketStart, 7, 24*time.Hour)

	r.Len(labels, 7)
	r.Equal("Wed", labels[0])

	for i, l := range labels {
		r.NotEmpty(l, "label %d should be non-empty", i)
		r.Len(l, 3, "label %d should be 3 chars", i)
	}

	// Sequential weekdays.
	r.Equal([]string{"Wed", "Thu", "Fri", "Sat", "Sun", "Mon", "Tue"}, labels)
}

func TestComputeUptimeBarLabels24h(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	bucketStart := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	labels := computeUptimeBarLabels(bucketStart, 24, time.Hour)

	r.Len(labels, 24)
	r.Equal("0h", labels[0])
	r.Equal("6h", labels[6])
	r.Equal("12h", labels[12])
	r.Equal("18h", labels[18])

	for i, l := range labels {
		if i%6 != 0 {
			r.Empty(l, "label %d should be empty", i)
		}
	}
}

func TestComputeUptimeBarLabels30d(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Start on a Wednesday so we can verify both the first-segment label and a
	// Monday boundary label.
	bucketStart := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	labels := computeUptimeBarLabels(bucketStart, 30, 24*time.Hour)

	r.Len(labels, 30)
	// First segment is always labeled.
	r.Equal("Jan 7", labels[0])
	// The next Monday is 2026-01-12, i.e. index 5.
	r.Equal("Jan 12", labels[5])
	// A non-boundary, non-first segment is empty.
	r.Empty(labels[1])
}

func TestComputeUptimeBarLabels90d(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Start mid-January so the first-of-month boundary (Feb 1) lands inside.
	bucketStart := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	labels := computeUptimeBarLabels(bucketStart, 90, 24*time.Hour)

	r.Len(labels, 90)
	// First segment is always labeled with its month.
	r.Equal("Jan", labels[0])
	// Feb 1 is 17 days after Jan 15, i.e. index 17.
	r.Equal("Feb", labels[17])
	// A non-boundary day is empty.
	r.Empty(labels[1])
}

func TestComputeUptimeBarLabelsUnknownPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A combination that matches no case yields all-empty labels of length n.
	labels := computeUptimeBarLabels(time.Now(), 5, 12*time.Hour)
	r.Len(labels, 5)

	for _, l := range labels {
		r.Empty(l)
	}
}

func TestComputeUptimeBarValues7d(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	bucketStart := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	// Three days have data, the rest are missing → empty (gray) overlay.
	availMap := map[time.Time]float64{
		bucketStart:              100,  // whole number → no decimals
		bucketStart.Add(2 * day): 98.6, // fractional → one decimal
		bucketStart.Add(4 * day): 0,    // outage day
	}

	// 7d / 300 px → segWidth (300-6)/7 = 42 > 30 → overlay populated.
	values := computeUptimeBarValues(availMap, bucketStart, 7, day, 300)

	r.Len(values, 7)
	r.Equal("100%", values[0])
	r.Empty(values[1])
	r.Equal("98.6%", values[2])
	r.Empty(values[3])
	r.Equal("0%", values[4])
	r.Empty(values[5])
	r.Empty(values[6])
}

func TestComputeUptimeBarValues7dNarrowWidthIsEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	bucketStart := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	availMap := map[time.Time]float64{
		bucketStart:              100,
		bucketStart.Add(2 * day): 98.6,
		bucketStart.Add(4 * day): 0,
	}

	// 7d / 200 px → segWidth (200-6)/7 = 27 <= 30 → no overlay.
	values := computeUptimeBarValues(availMap, bucketStart, 7, day, 200)
	r.Len(values, 7)

	for i, v := range values {
		r.Empty(v, "value %d should be empty for narrow 7d badge", i)
	}
}

func TestComputeUptimeBarValuesNon7dIsEmpty(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	bucketStart := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)

	// At 300 px, 24h/30d/90d bars are too thin for an in-bar percentage:
	//   24h → (300-23)/24 = 11 px, 30d → (300-29)/30 = 9 px, 90d → (300-89)/90 = 2 px.
	// All <= 30 → all-empty.
	for _, tc := range []struct {
		n        int
		duration time.Duration
	}{
		{24, time.Hour},
		{30, 24 * time.Hour},
		{90, 24 * time.Hour},
	} {
		availMap := map[time.Time]float64{bucketStart: 99.5}
		values := computeUptimeBarValues(availMap, bucketStart, tc.n, tc.duration, 300)
		r.Len(values, tc.n)

		for i, v := range values {
			r.Empty(v, "n=%d duration=%s value %d should be empty", tc.n, tc.duration, i)
		}
	}
}

// TestComputeUptimeBarValuesSegWidthThreshold pins the exact 30 px boundary
// independent of period: the overlay appears only when segWidth > 30.
func TestComputeUptimeBarValuesSegWidthThreshold(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	bucketStart := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	for _, tc := range []struct {
		name     string
		n        int
		width    int
		segWidth int
		show     bool
	}{
		// (width - (n-1)) / n
		{"7d/300px=42", 7, 300, 42, true},
		{"7d/200px=27", 7, 200, 27, false},
		{"30d/300px=9", 30, 300, 9, false},
		{"24h/300px=11", 24, 300, 11, false},
		// Boundary: exactly 30 is suppressed (<=), 31 shows (>).
		{"5segs/154px=30", 5, 154, 30, false},
		{"5segs/159px=31", 5, 159, 31, true},
	} {
		availMap := map[time.Time]float64{bucketStart: 99.5}
		values := computeUptimeBarValues(availMap, bucketStart, tc.n, day, tc.width)
		r.Len(values, tc.n)

		if tc.show {
			r.Equal("99.5%", values[0], "%s: expected overlay (segWidth=%d)", tc.name, tc.segWidth)
		} else {
			r.Empty(values[0], "%s: expected no overlay (segWidth=%d)", tc.name, tc.segWidth)
		}
	}
}

func TestFormatBarPercent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("100%", formatBarPercent(100))
	r.Equal("0%", formatBarPercent(0))
	r.Equal("99%", formatBarPercent(99))
	r.Equal("98.6%", formatBarPercent(98.6))
	r.Equal("99.9%", formatBarPercent(99.857))
}

func TestRenderUptimeBarRowOverlay(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	segments := []string{ColorGreen, ColorRed, ColorGray}
	labels := []string{"Mon", "Tue", "Wed"}
	barValues := []string{"100%", "0%", ""}
	row := renderUptimeBarRow(segments, labels, barValues, 300, rowHeightBar, 0, "flat")

	// The two non-empty overlays render (white text + dark shadow = 2 each),
	// plus the three weekday labels → 7 <text> elements total.
	r.Contains(row, ">100%<")
	r.Contains(row, ">0%<")
	r.Contains(row, `font-size="9"`)
	r.Contains(row, `fill="#fff"`)
	r.Equal(7, strings.Count(row, "<text"))
}

func TestFormatDurationMs(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal("304ms", formatDurationMs(304.0))
	r.Equal("0ms", formatDurationMs(0.0))
	r.Equal("999ms", formatDurationMs(999.0))
	r.Equal("1000ms", formatDurationMs(999.6)) // rounds up but stays below the 1000 threshold input
	r.Equal("1.0s", formatDurationMs(1000.0))
	r.Equal("1.2s", formatDurationMs(1200.0))
}

func TestRenderUptimeBarRowLabels(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	segments := []string{ColorGreen, ColorGreen, ColorGreen}
	labels := []string{"Mon", "", "Wed"}
	row := renderUptimeBarRow(segments, labels, nil, 300, rowHeightBar, 0, "flat")

	// Colored strip uses the fixed color height, not the full row height.
	r.Contains(row, `height="20"`)
	// Only the two non-empty labels render as <text>.
	r.Equal(2, strings.Count(row, "<text"))
	r.Contains(row, ">Mon<")
	r.Contains(row, ">Wed<")
	r.Contains(row, `fill="#777"`)
	r.Contains(row, `font-size="7"`)
	r.Contains(row, `text-anchor="middle"`)
	r.Contains(row, `y="28"`)
}

func TestRenderUptimeBarRowNoLabels(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	segments := []string{ColorGreen, ColorGreen}
	row := renderUptimeBarRow(segments, nil, nil, 300, rowHeightBar, 0, "flat")
	r.NotContains(row, "<text")
}

// TestRenderUptimeBarRowEvenWidths is a regression test for the 24h uptime-bar
// last-segment width bug: the rounding remainder must be spread evenly across
// the segments so no single segment (notably the current-hour one) renders
// wider than the rest. Before the fix, n=24/width=300 produced 23 segments at
// 11 px and a last segment at 24 px.
func TestRenderUptimeBarRowEvenWidths(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const (
		n     = 24
		width = 300
	)

	segments := make([]string, n)
	for i := range segments {
		segments[i] = ColorGray
	}

	row := renderUptimeBarRow(segments, nil, nil, width, rowHeightBar, 0, "flat")

	widthRe := regexp.MustCompile(`<rect x="\d+" width="(\d+)"`)
	matches := widthRe.FindAllStringSubmatch(row, -1)
	r.Len(matches, n, "expected one <rect> per segment")

	minW, maxW, sum := math.MaxInt, 0, 0
	for _, m := range matches {
		w, err := strconv.Atoi(m[1])
		r.NoError(err)

		if w < minW {
			minW = w
		}

		if w > maxW {
			maxW = w
		}

		sum += w
	}

	r.LessOrEqual(maxW-minW, 1, "segment widths must differ by at most 1px")
	// Segment widths plus the (n-1) 1-px gaps sum to exactly the bar width.
	r.Equal(width, sum+(n-1))
}
