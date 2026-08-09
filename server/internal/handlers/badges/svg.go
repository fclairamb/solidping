package badges

import (
	"fmt"
	"math"
	"strings"
)

// Color constants for badge generation.
const (
	ColorGreen  = "#4c1"
	ColorYellow = "#dfb317"
	ColorOrange = "#fe7d37"
	ColorRed    = "#e05d44"
	ColorBlue   = "#007ec6"
	ColorGray   = "#9f9f9f"
	ColorLabel  = "#555"
	ColorTitle  = "#333"
	ColorGraph  = "#4078c0"
)

// fontFamily is the shields-style font stack used across rows.
const fontFamily = "DejaVu Sans,Verdana,Geneva,sans-serif"

// Row heights (px) for the composable multi-row badge.
const (
	rowHeightText  = 20
	rowHeightBar   = 30
	rowHeightGraph = 40
	rowGap         = 4

	// uptimeBarColorHeight is the height of the colored strip at the top of the
	// uptime-bar row; the remaining height holds the per-segment label band.
	uptimeBarColorHeight = 20
	// uptimeBarLabelBaseline is the text baseline (relative to the row's yOffset)
	// for the per-segment labels in the bottom label band.
	uptimeBarLabelBaseline = 28
)

// borderRadius returns the corner radius for the given style.
func borderRadius(style string) string {
	if style == "flat-square" {
		return "0"
	}

	return "3"
}

// textWidths computes the label, value and total widths for a shields row.
// When value is empty (black-title variant) the value cell is omitted. Returns
// (labelWidth, valueWidth, totalWidth).
func textWidths(label, value string, minWidth int) (int, int, int) {
	labelWidth := len(label)*6 + 10

	if value == "" {
		totalWidth := labelWidth
		if minWidth > 0 && totalWidth < minWidth {
			labelWidth += minWidth - totalWidth
			totalWidth = minWidth
		}

		return labelWidth, 0, totalWidth
	}

	valueWidth := len(value)*6 + 10
	totalWidth := labelWidth + valueWidth

	if minWidth > 0 && totalWidth < minWidth {
		valueWidth += minWidth - totalWidth
		totalWidth = minWidth
	}

	return labelWidth, valueWidth, totalWidth
}

// renderBadgeRow renders a single shields-style label|value row as a positioned
// <g> fragment translated to y. When value is empty it renders the label as a
// plain black title with no value cell. width, when > 0, stretches the value
// cell (or the title cell) to fill it.
func renderBadgeRow(label, value, valueColor, style string, width, yOffset int) string {
	labelWidth, valueWidth, naturalWidth := textWidths(label, value, width)

	totalWidth := naturalWidth
	if width > naturalWidth {
		// Stretch to fill the requested width.
		if value == "" {
			labelWidth += width - naturalWidth
		} else {
			valueWidth += width - naturalWidth
		}

		totalWidth = width
	}

	radius := borderRadius(style)
	escLabel := escapeXML(label)
	escValue := escapeXML(value)

	// Black-title variant: no value cell, plain text title.
	if value == "" {
		return fmt.Sprintf(`  <g transform="translate(0,%d)">
    <text x="5" y="14" fill="%s" font-family="%s" font-size="11" font-weight="bold">%s</text>
  </g>`, yOffset, ColorTitle, fontFamily, escLabel)
	}

	return fmt.Sprintf(`  <g transform="translate(0,%d)">
    <linearGradient id="g%d" x2="0" y2="100%%">
      <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>
      <stop offset="1" stop-opacity=".1"/>
    </linearGradient>
    <clipPath id="c%d">
      <rect width="%d" height="20" rx="%s" fill="#fff"/>
    </clipPath>
    <g clip-path="url(#c%d)">
      <rect width="%d" height="20" fill="%s"/>
      <rect x="%d" width="%d" height="20" fill="%s"/>
      <rect width="%d" height="20" fill="url(#g%d)"/>
    </g>
    <g fill="#fff" text-anchor="middle" font-family="%s" font-size="11">
      <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
      <text x="%d" y="14">%s</text>
      <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
      <text x="%d" y="14">%s</text>
    </g>
  </g>`,
		yOffset,
		yOffset,
		yOffset,
		totalWidth, radius,
		yOffset,
		labelWidth, ColorLabel,
		labelWidth, valueWidth, valueColor,
		totalWidth, yOffset,
		fontFamily,
		labelWidth/2, escLabel,
		labelWidth/2, escLabel,
		labelWidth+valueWidth/2, escValue,
		labelWidth+valueWidth/2, escValue,
	)
}

// barOverlayText returns one centered <text> element (a percentage label drawn
// inside an uptime bar) at the given fill and optional fill-opacity, terminated
// by a newline. The value is XML-escaped.
func barOverlayText(centerX, baseline int, fill, opacity, value string) string {
	opacityAttr := ""
	if opacity != "" {
		opacityAttr = ` fill-opacity="` + opacity + `"`
	}

	return fmt.Sprintf(
		`      <text x="%d" y="%d" fill="%s"%s font-family="%s" `+
			`font-size="9" text-anchor="middle">%s</text>`+"\n",
		centerX, baseline, fill, opacityAttr, fontFamily, escapeXML(value))
}

// renderUptimeBarRow renders an N-segment availability strip as a positioned
// <g> fragment translated to yOffset. The colored strip occupies the top
// uptimeBarColorHeight px; the remaining height is a label band holding the
// per-segment time labels. labels carries one entry per segment (empty string =
// no label for that slot); a nil/short slice renders no labels. barValues, when
// set for a segment, is drawn as a centered percentage overlay inside that
// segment's colored bar — only periods with bars wide enough (7d) populate it.
func renderUptimeBarRow(segments, labels, barValues []string, width, height, yOffset int, style string) string {
	n := len(segments)
	if n == 0 {
		return fmt.Sprintf(`  <g transform="translate(0,%d)"></g>`, yOffset)
	}

	radius := borderRadius(style)

	// The colored strip never exceeds the row height (defensive when callers
	// pass a small height, e.g. the standalone-SVG test wrapper).
	colorHeight := uptimeBarColorHeight
	if colorHeight > height {
		colorHeight = height
	}

	// Segment widths are distributed evenly: the rounding remainder is spread
	// across the bar instead of piling onto the last segment.
	gaps := n - 1
	availableWidth := width - gaps

	var rects, labelText strings.Builder

	posX := 0
	for idx, color := range segments {
		// Even distribution: each segment spans
		//   floor((idx+1)*availableWidth/n) - floor(idx*availableWidth/n)
		// so widths differ by at most 1px and the segment widths plus the
		// (n-1) 1-px gaps sum to exactly width, with the last segment's right
		// edge landing on width.
		rectWidth := (idx+1)*availableWidth/n - idx*availableWidth/n

		fmt.Fprintf(&rects, `      <rect x="%d" width="%d" height="%d" fill="%s"/>`, posX, rectWidth, colorHeight, color)
		fmt.Fprintln(&rects)

		// In-bar percentage overlay (populated only for periods whose bars are
		// wide enough to fit it, e.g. 7d). A dark shadow behind the white text
		// keeps it legible on the lighter segment colors.
		if idx < len(barValues) && barValues[idx] != "" {
			centerX := posX + rectWidth/2
			textY := colorHeight/2 + 4
			rects.WriteString(barOverlayText(centerX, textY+1, "#010101", ".3", barValues[idx]))
			rects.WriteString(barOverlayText(centerX, textY, "#fff", "", barValues[idx]))
		}

		if idx < len(labels) && labels[idx] != "" {
			centerX := posX + rectWidth/2
			fmt.Fprintf(&labelText,
				`    <text x="%d" y="%d" fill="#777" font-family="%s" font-size="7" text-anchor="middle">%s</text>`,
				centerX, uptimeBarLabelBaseline, fontFamily, escapeXML(labels[idx]))
			fmt.Fprintln(&labelText)
		}

		posX += rectWidth + 1
	}

	return fmt.Sprintf(`  <g transform="translate(0,%d)">
    <clipPath id="bar%d">
      <rect width="%d" height="%d" rx="%s" fill="#fff"/>
    </clipPath>
    <g clip-path="url(#bar%d)">
%s    </g>
%s  </g>`, yOffset, yOffset, width, colorHeight, radius, yOffset, rects.String(), labelText.String())
}

// paddedRange applies 10% padding around [minV, maxV] so the line never
// touches the row edges. A flat series (span 0) is padded symmetrically.
func paddedRange(minV, maxV float64) (float64, float64) {
	span := maxV - minV
	if span == 0 {
		pad := maxV * 0.1
		if pad == 0 {
			pad = 1
		}

		return minV - pad, maxV + pad
	}

	pad := span * 0.1

	return minV - pad, maxV + pad
}

// renderGraphSegments renders the area, line and dot fragments for the given
// point segments into the supplied builders.
func renderGraphSegments(
	segments [][]int, points []*float64,
	xAt func(int) float64, yAt func(float64) float64,
	height, yOffset int,
	areas, lines, dots *strings.Builder,
) {
	for _, seg := range segments {
		if len(seg) == 1 {
			idx := seg[0]
			fmt.Fprintf(dots, `    <circle cx="%.1f" cy="%.1f" r="1.6" fill="%s"/>`,
				xAt(idx), yAt(*points[idx]), ColorGraph)
			fmt.Fprintln(dots)

			continue
		}

		var poly strings.Builder
		for pos, idx := range seg {
			if pos > 0 {
				poly.WriteByte(' ')
			}

			fmt.Fprintf(&poly, "%.1f,%.1f", xAt(idx), yAt(*points[idx]))
		}

		fmt.Fprintf(areas, `    <path d="M%.1f,%d L%s L%.1f,%d Z" fill="url(#grad%d)"/>`,
			xAt(seg[0]), height, poly.String(), xAt(seg[len(seg)-1]), height, yOffset)
		fmt.Fprintln(areas)

		fmt.Fprintf(lines, `    <polyline points="%s" fill="none" stroke="%s" stroke-width="1.5"/>`,
			poly.String(), ColorGraph)
		fmt.Fprintln(lines)
	}
}

// renderGraphGrid emits the horizontal Y-axis gridlines and their right-edge
// value labels into grid. It draws one line at the actual (unpadded) maximum and
// one at the actual minimum; when the series is flat (actualMin == actualMax) a
// single line is drawn. Lines sit behind the chart data.
func renderGraphGrid(actualMin, actualMax float64, width int, yAt func(float64) float64, grid *strings.Builder) {
	gridVals := []float64{actualMax}
	if actualMin != actualMax {
		gridVals = append(gridVals, actualMin)
	}

	for _, gridValue := range gridVals {
		gridY := yAt(gridValue)
		fmt.Fprintf(grid,
			`    <line x1="0" y1="%.1f" x2="%d" y2="%.1f" stroke="#ccc" stroke-width="0.5" stroke-dasharray="2,2"/>`,
			gridY, width, gridY)
		fmt.Fprintln(grid)
		fmt.Fprintf(grid,
			`    <text x="%d" y="%.1f" fill="#888" font-family="%s" font-size="7" text-anchor="end">%s</text>`,
			width-2, gridY-1, fontFamily, escapeXML(formatDurationMs(gridValue)))
		fmt.Fprintln(grid)
	}
}

// formatDurationMs formats a millisecond response-time value the same way as
// formatResponseTime: integer milliseconds below 1000 ms, one decimal second
// above (e.g. "304ms", "1.2s").
func formatDurationMs(ms float64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", int(math.Round(ms)))
	}

	return fmt.Sprintf("%.1fs", ms/1000)
}

// renderResponseTimeGraphRow renders a filled-area response-time graph as a
// positioned <g> fragment translated to yOffset. points holds per-bucket
// average response times oldest→newest; nil entries are gaps (no data) that
// break the line into separate segments. The Y axis auto-scales to [min, max]
// with 10% padding. A single data point (or a flat run) renders as a dot.
func renderResponseTimeGraphRow(points []*float64, width, height, yOffset int, style string) string {
	radius := borderRadius(style)

	actualMin, actualMax, hasData := pointsRange(points)
	if !hasData {
		// No data at all: render an empty framed area.
		return fmt.Sprintf(`  <g transform="translate(0,%d)">
    <rect width="%d" height="%d" rx="%s" fill="#f5f5f5"/>
  </g>`, yOffset, width, height, radius)
	}

	minV, maxV := paddedRange(actualMin, actualMax)

	n := len(points)
	xStep := 0.0
	if n > 1 {
		xStep = float64(width) / float64(n-1)
	}

	xAt := func(i int) float64 {
		if n == 1 {
			return float64(width) / 2
		}

		return float64(i) * xStep
	}
	yAt := func(v float64) float64 {
		// Higher value → smaller y (top of the row).
		frac := (v - minV) / (maxV - minV)

		return float64(height) - frac*float64(height)
	}

	var defs, grid, areas, lines, dots strings.Builder

	fmt.Fprintf(&defs, `    <linearGradient id="grad%d" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="%s" stop-opacity="0.4"/>
      <stop offset="1" stop-color="%s" stop-opacity="0"/>
    </linearGradient>`, yOffset, ColorGraph, ColorGraph)

	renderGraphGrid(actualMin, actualMax, width, yAt, &grid)
	renderGraphSegments(buildGraphSegments(points), points, xAt, yAt, height, yOffset, &areas, &lines, &dots)

	// Grid renders before the area/line/dot fragments so it sits behind the data.
	return fmt.Sprintf(`  <g transform="translate(0,%d)">
    <defs>
%s
    </defs>
    <rect width="%d" height="%d" rx="%s" fill="#f5f5f5"/>
%s%s%s%s  </g>`, yOffset, defs.String(), width, height, radius,
		grid.String(), areas.String(), lines.String(), dots.String())
}

// pointsRange returns the min and max of the non-nil points and whether any
// data exists. Returns (min, max, hasData).
func pointsRange(points []*float64) (float64, float64, bool) {
	var minV, maxV float64

	hasData := false

	for _, point := range points {
		if point == nil {
			continue
		}

		if !hasData {
			minV, maxV, hasData = *point, *point, true

			continue
		}

		if *point < minV {
			minV = *point
		}

		if *point > maxV {
			maxV = *point
		}
	}

	return minV, maxV, hasData
}

// buildGraphSegments groups consecutive non-nil point indices into segments,
// breaking at nil gaps.
func buildGraphSegments(points []*float64) [][]int {
	var segments [][]int

	var current []int

	for i, p := range points {
		if p == nil {
			if len(current) > 0 {
				segments = append(segments, current)
				current = nil
			}

			continue
		}

		current = append(current, i)
	}

	if len(current) > 0 {
		segments = append(segments, current)
	}

	return segments
}

// ComposeBadgeSVG assembles the outer <svg width=W height=H> from pre-rendered
// row fragments.
func ComposeBadgeSVG(rows []string, w, h int) string {
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d">
%s
</svg>`, w, h, strings.Join(rows, "\n"))
}

// GenerateSVG creates a single-row shields.io-style badge SVG. Thin wrapper over
// renderBadgeRow + ComposeBadgeSVG, kept for the existing test surface.
func GenerateSVG(label, value, valueColor, style string, minWidth int) string {
	_, _, totalWidth := textWidths(label, value, minWidth)
	row := renderBadgeRow(label, value, valueColor, style, minWidth, 0)

	return ComposeBadgeSVG([]string{row}, totalWidth, rowHeightText)
}

// GenerateUptimeBarSVG creates a standalone uptime-bar SVG. Thin wrapper over
// renderUptimeBarRow + ComposeBadgeSVG, kept for the existing test surface.
func GenerateUptimeBarSVG(segments []string, width, height int, style string) string {
	row := renderUptimeBarRow(segments, nil, nil, width, height, 0, style)

	return ComposeBadgeSVG([]string{row}, width, height)
}

func escapeXML(input string) string {
	input = strings.ReplaceAll(input, "&", "&amp;")
	input = strings.ReplaceAll(input, "<", "&lt;")
	input = strings.ReplaceAll(input, ">", "&gt;")
	input = strings.ReplaceAll(input, "'", "&apos;")
	input = strings.ReplaceAll(input, `"`, "&quot;")

	return input
}
