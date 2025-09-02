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
)

// GenerateSVG creates a shields.io-style badge SVG.
func GenerateSVG(label, value, valueColor, style string) string {
	// Calculate widths (approximate: 6px per character + padding)
	labelWidth := len(label)*6 + 10
	valueWidth := len(value)*6 + 10
	totalWidth := labelWidth + valueWidth

	// Escape XML special characters
	label = escapeXML(label)
	value = escapeXML(value)

	radius := "3"
	if style == "flat-square" {
		radius = "0"
	}

	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20">
  <linearGradient id="b" x2="0" y2="100%%">
    <stop offset="0" stop-color="#bbb" stop-opacity=".1"/>
    <stop offset="1" stop-opacity=".1"/>
  </linearGradient>
  <clipPath id="a">
    <rect width="%d" height="20" rx="%s" fill="#fff"/>
  </clipPath>
  <g clip-path="url(#a)">
    <rect width="%d" height="20" fill="%s"/>
    <rect x="%d" width="%d" height="20" fill="%s"/>
    <rect width="%d" height="20" fill="url(#b)"/>
  </g>
  <g fill="#fff" text-anchor="middle" font-family="DejaVu Sans,Verdana,Geneva,sans-serif" font-size="11">
    <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
    <text x="%d" y="14">%s</text>
    <text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text>
    <text x="%d" y="14">%s</text>
  </g>
</svg>`,
		totalWidth,
		totalWidth, radius,
		labelWidth, ColorLabel,
		labelWidth, valueWidth, valueColor,
		totalWidth,
		labelWidth/2, label,
		labelWidth/2, label,
		labelWidth+valueWidth/2, value,
		labelWidth+valueWidth/2, value,
	)
}

func escapeXML(input string) string {
	input = strings.ReplaceAll(input, "&", "&amp;")
	input = strings.ReplaceAll(input, "<", "&lt;")
	input = strings.ReplaceAll(input, ">", "&gt;")
	input = strings.ReplaceAll(input, "'", "&apos;")
	input = strings.ReplaceAll(input, `"`, "&quot;")

	return input
}
