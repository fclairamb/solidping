package checks

import (
	"context"
	"sort"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// browserConfigField is the field the warning is attached to. The browser
// capability is a property of the check as a whole rather than of one config
// key, and `url` is the field the dashboard renders at the top of the browser
// form, so that is where the message lands.
const browserConfigField = "url"

// regionCapabilityWarnings is the single advisory entry point the write paths
// call: every region-capability warning, in one slice.
//
// Advisory, never a rejection — see ipVersionRegionWarnings for the two reasons
// (the value lags, and "unknown" is a real state), which apply verbatim here.
func (s *Service) regionCapabilityWarnings(
	ctx context.Context, orgUID, checkType string, config map[string]any, checkRegions []string,
) []base.ValidationErrorField {
	warnings := s.ipVersionRegionWarnings(ctx, orgUID, checkType, config, checkRegions)

	return append(warnings, s.browserRegionWarnings(ctx, orgUID, checkType, checkRegions)...)
}

// browserRegionWarnings reports — as a WARNING, never a rejection — that a
// `browser` check targets a region whose live workers currently advertise no
// headless Chrome (spec 2026-08-19-03).
//
// This is the proactive half of the spec: without it a user creates a browser
// check in a region that has no Chrome and learns it one failed execution at a
// time. Only an explicit "no" warns: a region with no live worker, or one served
// by an agent predating the probe, reports "unknown" and must never be treated
// as incapable.
func (s *Service) browserRegionWarnings(
	ctx context.Context, orgUID, checkType string, checkRegions []string,
) []base.ValidationErrorField {
	if checkerdef.CheckType(checkType) != checkerdef.CheckTypeBrowser {
		return nil
	}

	effective, err := s.regions.ResolveRegionsForCheck(ctx, checkRegions, orgUID)
	if err != nil || len(effective) == 0 {
		return nil
	}

	index, err := s.regions.CapabilityIndex(ctx, orgUID)
	if err != nil {
		return nil
	}

	var lacking []string

	for _, slug := range effective {
		def, known := index[slug]
		if !known {
			continue
		}

		if def.BrowserCapability() == regions.CapabilityNo {
			lacking = append(lacking, regionLabel(def))
		}
	}

	if len(lacking) == 0 {
		return nil
	}

	return []base.ValidationErrorField{{
		Name:    browserConfigField,
		Message: browserRegionWarningMessage(lacking, browserCapableRegions(index)),
	}}
}

// browserCapableRegions lists the regions that DO advertise a browser, so the
// warning ends with something actionable. Sorted for a stable message.
func browserCapableRegions(index map[string]regions.RegionDefinition) []string {
	capable := make([]string, 0, len(index))

	for slug := range index {
		def := index[slug]
		if def.BrowserCapability() == regions.CapabilityYes {
			capable = append(capable, regionLabel(def))
		}
	}

	sort.Strings(capable)

	return capable
}

// browserRegionWarningMessage names the offending regions and, when one exists,
// points at a region that can actually run the check.
func browserRegionWarningMessage(lacking, capable []string) string {
	var builder strings.Builder

	builder.WriteString("this is a browser check, but ")

	if len(lacking) == 1 {
		builder.WriteString("region " + lacking[0] + " reports no headless Chrome")
	} else {
		builder.WriteString("regions " + strings.Join(lacking, ", ") + " report no headless Chrome")
	}

	builder.WriteString(" on its live workers")

	if len(capable) > 0 {
		builder.WriteString(", so runs there will fail with a browser-unavailable error. ")
		builder.WriteString(strings.Join(capable, ", "))

		if len(capable) == 1 {
			builder.WriteString(" advertises headless Chrome")
		} else {
			builder.WriteString(" advertise headless Chrome")
		}

		builder.WriteString(".")
	} else {
		builder.WriteString(", so runs there will fail with a browser-unavailable error.")
	}

	builder.WriteString(
		" The check is still saved and will still run — this is only what the region last reported.",
	)

	return builder.String()
}
