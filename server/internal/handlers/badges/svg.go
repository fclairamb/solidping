package badges

import (
	"fmt"
	"strings"
)

// Color constants for badge generation.
const (
	ColorGreen  = "#4c1"
	ColorYellow = "#dfb317"
	ColorOrange = "#fe7d37"
	ColorRed    = "#e05d44"
	ColorGray   = "#9f9f9f"
	ColorLabel  = "#555"
	ColorTitle  = "#333"
	ColorGraph  = "#4078c0"
)

// Row heights (px) for the composable multi-row badge.
const (
	rowHeightText  = 20
	rowHeightBar   = 20
	rowHeightGraph = 40
	rowGap         = 4
)

// borderRadius returns the corner radius for the given style.
func borderRadius(style string) string {
	if style == "flat-square" {
		return "0"
	}

	return "3"
}

// textWidths computes the label, value and total widths for a shields row.
// When value is empty (black-title variant) the value cell is omitted.
func textWidths(label, value string, minWidth int) (labelWidth, valueWidth, totalWidth int) {
	labelWidth = len(label)*6 + 10

	if value == "" {
		totalWidth = labelWidth
		if minWidth > 0 && totalWidth < minWidth {
			labelWidth += minWidth - totalWidth
			totalWidth = minWidth
		}

		return labelWidth, 0, totalWidth
	}

	valueWidth = len(value)*6 + 10
	totalWidth = labelWidth + valueWidth

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
func renderBadgeRow(label, value, valueColor, style string, width, y int) string {
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
    <text x="5" y="14" fill="%s" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11" font-weight="bold">%s</text>
  </g>`, y, ColorTitle, escLabel)
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
    <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
      <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
      <text x="%d" y="14">%s</text>
      <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
      <text x="%d" y="14">%s</text>
    </g>
  </g>`,
		y,
		y,
		y,
		totalWidth, radius,
		y,
		labelWidth, ColorLabel,
		labelWidth, valueWidth, valueColor,
		totalWidth, y,
		labelWidth/2, escLabel,
		labelWidth/2, escLabel,
		labelWidth+valueWidth/2, escValue,
		labelWidth+valueWidth/2, escValue,
	)
}

// renderUptimeBarRow renders an N-segment availability strip as a positioned
// <g> fragment translated to y.
func renderUptimeBarRow(segments []string, width, height, y int, style string) string {
	n := len(segments)
	if n == 0 {
		return fmt.Sprintf(`  <g transform="translate(0,%d)"></g>`, y)
	}

	radius := borderRadius(style)

	// Segment width: integer division, last segment gets remaining pixels.
	gaps := n - 1
	availableWidth := width - gaps
	segWidth := availableWidth / n
	lastSegWidth := availableWidth - segWidth*(n-1)

	var rects strings.Builder

	posX := 0
	for idx, color := range segments {
		rectWidth := segWidth
		if idx == n-1 {
			rectWidth = lastSegWidth
		}

		fmt.Fprintf(&rects, `      <rect x="%d" width="%d" height="%d" fill="%s"/>`, posX, rectWidth, height, color)
		fmt.Fprintln(&rects)
		posX += rectWidth + 1
	}

	return fmt.Sprintf(`  <g transform="translate(0,%d)">
    <clipPath id="bar%d">
      <rect width="%d" height="%d" rx="%s" fill="#fff"/>
    </clipPath>
    <g clip-path="url(#bar%d)">
%s    </g>
  </g>`, y, y, width, height, radius, y, rects.String())
}

// renderResponseTimeGraphRow renders a filled-area response-time graph as a
// positioned <g> fragment translated to y. points holds per-bucket average
// response times oldest→newest; nil entries are gaps (no data) that break the
// line into separate segments. The Y axis auto-scales to [min, max] with 10%
// padding. A single data point (or a flat run) renders as a dot.
func renderResponseTimeGraphRow(points []*float64, width, height, y int, style string) string {
	radius := borderRadius(style)

	minV, maxV, hasData := pointsRange(points)
	if !hasData {
		// No data at all: render an empty framed area.
		return fmt.Sprintf(`  <g transform="translate(0,%d)">
    <rect width="%d" height="%d" rx="%s" fill="#f5f5f5"/>
  </g>`, y, width, height, radius)
	}

	// Apply 10%% padding to the value range so the line never touches edges.
	span := maxV - minV
	if span == 0 {
		// Flat series: pad symmetrically around the single value.
		pad := maxV * 0.1
		if pad == 0 {
			pad = 1
		}

		minV -= pad
		maxV += pad
	} else {
		pad := span * 0.1
		minV -= pad
		maxV += pad
	}

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

	segments := buildGraphSegments(points)

	var defs, areas, lines, dots strings.Builder

	fmt.Fprintf(&defs, `    <linearGradient id="grad%d" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0" stop-color="%s" stop-opacity="0.4"/>
      <stop offset="1" stop-color="%s" stop-opacity="0"/>
    </linearGradient>`, y, ColorGraph, ColorGraph)

	for _, seg := range segments {
		if len(seg) == 1 {
			// Single point: draw a dot.
			i := seg[0]
			fmt.Fprintf(&dots, `    <circle cx="%.1f" cy="%.1f" r="1.6" fill="%s"/>`,
				xAt(i), yAt(*points[i]), ColorGraph)
			fmt.Fprintln(&dots)

			continue
		}

		var poly strings.Builder
		for idx, i := range seg {
			if idx > 0 {
				poly.WriteByte(' ')
			}

			fmt.Fprintf(&poly, "%.1f,%.1f", xAt(i), yAt(*points[i]))
		}

		// Filled area path: down to baseline at both ends.
		fmt.Fprintf(&areas, `    <path d="M%.1f,%d L%s L%.1f,%d Z" fill="url(#grad%d)"/>`,
			xAt(seg[0]), height, poly.String(), xAt(seg[len(seg)-1]), height, y)
		fmt.Fprintln(&areas)

		fmt.Fprintf(&lines, `    <polyline points="%s" fill="none" stroke="%s" stroke-width="1.5"/>`,
			poly.String(), ColorGraph)
		fmt.Fprintln(&lines)
	}

	return fmt.Sprintf(`  <g transform="translate(0,%d)">
    <defs>
%s
    </defs>
    <rect width="%d" height="%d" rx="%s" fill="#f5f5f5"/>
%s%s%s  </g>`, y, defs.String(), width, height, radius, areas.String(), lines.String(), dots.String())
}

// pointsRange returns the min and max of the non-nil points and whether any
// data exists.
func pointsRange(points []*float64) (minV, maxV float64, hasData bool) {
	for _, p := range points {
		if p == nil {
			continue
		}

		if !hasData {
			minV, maxV, hasData = *p, *p, true

			continue
		}

		if *p < minV {
			minV = *p
		}

		if *p > maxV {
			maxV = *p
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
	row := renderUptimeBarRow(segments, width, height, 0, style)

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
