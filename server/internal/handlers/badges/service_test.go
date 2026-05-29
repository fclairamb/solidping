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

// TestBucketAccumulator verifies that mixing raw and hour rows in the same
// bucket produces correctly weighted availability and average duration.
func TestBucketAccumulator(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Simulate: 1 raw row (up, 100ms) + 1 hour row (9 checks, 8 up, avg 200ms).
	// Expected availability = 9/10 = 90%; weighted avg duration = (100*1 + 200*9)/10 = 1900/10 = 190ms.
	acc := &bucketAccumulator{}

	// Raw row
	statusUp := int(models.ResultStatusUp)
	rawDuration := float32(100.0)
	raw := &models.Result{
		PeriodType: models.PeriodTypeRaw,
		Status:     &statusUp,
		Duration:   &rawDuration,
	}

	// Aggregate raw row
	if raw.Status != nil {
		s := models.ResultStatus(*raw.Status)
		if s != models.ResultStatusCreated && s != models.ResultStatusRunning {
			acc.total++
			if *raw.Status == int(models.ResultStatusUp) {
				acc.up++
			}
			if raw.Duration != nil {
				acc.durSum += float64(*raw.Duration)
				acc.durCnt++
			}
		}
	}

	// Hour aggregated row: 9 total, 8 up, avg duration 200ms
	total := 9
	successful := 8
	avgDur := float32(200.0)
	hourRow := &models.Result{
		PeriodType:       models.PeriodTypeHour,
		TotalChecks:      &total,
		SuccessfulChecks: &successful,
		DurationAvg:      &avgDur,
	}

	if hourRow.TotalChecks != nil {
		acc.total += *hourRow.TotalChecks
	}
	if hourRow.SuccessfulChecks != nil {
		acc.up += *hourRow.SuccessfulChecks
	}
	if hourRow.DurationAvg != nil && hourRow.TotalChecks != nil && *hourRow.TotalChecks > 0 {
		acc.durSum += float64(*hourRow.DurationAvg) * float64(*hourRow.TotalChecks)
		acc.durCnt += *hourRow.TotalChecks
	}

	// Verify: 9 total (1 raw + 8 successful+1 from hour row... wait, total is 9, up is 8+1=9)
	// Actually: raw contributes total=1, up=1; hour contributes total=9, up=8 → total=10, up=9
	r.Equal(10, acc.total)
	r.Equal(9, acc.up)
	r.InDelta(90.0, float64(acc.up)/float64(acc.total)*100, 0.001)

	// Weighted average duration: (100*1 + 200*9) / (1+9) = 1900/10 = 190ms
	r.Equal(10, acc.durCnt)
	r.InDelta(190.0, acc.durSum/float64(acc.durCnt), 0.001)
}

// TestBucketAccumulatorSkipsCreatedRunning verifies that raw rows with
// created or running status are excluded from the totals.
func TestBucketAccumulatorSkipsCreatedRunning(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	acc := &bucketAccumulator{}

	statusCreated := int(models.ResultStatusCreated)
	statusRunning := int(models.ResultStatusRunning)
	statusDown := int(models.ResultStatusDown)

	rows := []*models.Result{
		{PeriodType: models.PeriodTypeRaw, Status: &statusCreated},
		{PeriodType: models.PeriodTypeRaw, Status: &statusRunning},
		{PeriodType: models.PeriodTypeRaw, Status: &statusDown},
	}

	for _, row := range rows {
		if row.Status == nil {
			continue
		}
		s := models.ResultStatus(*row.Status)
		if s == models.ResultStatusCreated || s == models.ResultStatusRunning {
			continue
		}
		acc.total++
		if *row.Status == int(models.ResultStatusUp) {
			acc.up++
		}
	}

	r.Equal(1, acc.total)
	r.Equal(0, acc.up)
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
	// First segment is always labelled.
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
	// First segment is always labelled with its month.
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
	row := renderUptimeBarRow(segments, labels, 300, rowHeightBar, 0, "flat")

	// Coloured strip uses the fixed colour height, not the full row height.
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
	row := renderUptimeBarRow(segments, nil, 300, rowHeightBar, 0, "flat")
	r.NotContains(row, "<text")
}
