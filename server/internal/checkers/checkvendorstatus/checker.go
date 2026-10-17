// Package checkvendorstatus watches a vendor's public status page (GitHub,
// Cloudflare, OpenAI...) and alerts when it reports a problem. Only the
// Atlassian Statuspage v2 summary format is supported.
package checkvendorstatus

import (
	"context"
	"fmt"
	"strings"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkvendorstatus/config"
)

const (
	// maxOutputComponents and maxOutputIncidents cap the result output.
	maxOutputComponents = 200
	maxOutputIncidents  = 20

	outputKeyComponents = "components"
	outputKeyIncidents  = "incidents"

	incidentResolved   = "resolved"
	incidentPostmortem = "postmortem"
)

// VendorStatusChecker implements the Checker interface for vendor-status checks.
type VendorStatusChecker struct {
	http checkhttp.HTTPChecker
}

// Type returns the check type identifier.
func (c *VendorStatusChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeVendorStatus
}

// Validate checks that the configuration is valid.
func (c *VendorStatusChecker) Validate(spec *checkerdef.CheckSpec) error {
	return config.ValidateSpec(spec)
}

// Execute reads the vendor's summary and judges it.
//
// A vendor that cannot be read (network error, non-200, not a v2 summary) is
// StatusError, never StatusDown: not being able to read a status page is not
// the vendor reporting an outage.
func (c *VendorStatusChecker) Execute(ctx context.Context, cfgIn checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*VendorStatusConfig](cfgIn)
	if err != nil {
		return nil, err
	}

	outcome, err := fetch(ctx, &c.http, config.NormalizeURL(cfg.URL))
	if err != nil || outcome == nil {
		return nil, err
	}

	result := &checkerdef.Result{
		Duration:    outcome.httpResult.Duration,
		Output:      outcome.httpResult.Output,
		Diagnostics: outcome.httpResult.Diagnostics,
	}

	if result.Output == nil {
		result.Output = make(map[string]any)
	}

	if outcome.summary == nil {
		// Keep a non-Down http failure (timeout) as it is, everything else is an error.
		result.Status = checkerdef.StatusError
		if outcome.httpResult.Status == checkerdef.StatusTimeout {
			result.Status = checkerdef.StatusTimeout
		}

		result.Output[checkerdef.OutputKeyError] = outcome.problem

		return result, nil
	}

	judge(cfg, outcome.summary, result)

	if result.Status == checkerdef.StatusUp || result.Status == checkerdef.StatusWarning {
		result.Diagnostics = nil
	}

	return result, nil
}

// judge fills result's status and output from a valid summary.
func judge(cfg *VendorStatusConfig, sum *summary, result *checkerdef.Result) {
	result.Output[outputKeyComponents] = outputComponents(sum)

	var (
		status   checkerdef.Status
		describe string
		problem  string
	)

	if len(cfg.Components) == 0 {
		status, problem = wholePageStatus(sum.Status.Indicator)
		describe = sum.Status.Description
	} else {
		var missing []string

		status, describe, missing, problem = componentsStatus(cfg.Components, sum)
		if len(missing) > 0 {
			result.Status = checkerdef.StatusError
			result.Output[checkerdef.OutputKeyError] = fmt.Sprintf(
				"%s: component %s not found on the status page (renamed or removed?)",
				pageName(sum), strings.Join(quoteAll(missing), ", "))

			return
		}
	}

	if problem != "" {
		result.Status = checkerdef.StatusError
		result.Output[checkerdef.OutputKeyError] = problem

		return
	}

	incidents := relevantIncidents(cfg.Components, sum)
	result.Output[outputKeyIncidents] = outputIncidents(incidents)
	result.Status = status

	if status == checkerdef.StatusUp {
		return
	}

	if len(incidents) > 0 {
		first := incidents[0]
		result.Output[checkerdef.OutputKeyError] = strings.TrimSpace(fmt.Sprintf(
			"%s: %q (%s) %s", pageName(sum), first.Name, first.Impact, first.Shortlink))

		return
	}

	result.Output[checkerdef.OutputKeyError] = fmt.Sprintf("%s: %s", pageName(sum), describe)
}

// wholePageStatus maps the page's overall indicator.
func wholePageStatus(indicator string) (checkerdef.Status, string) {
	switch indicator {
	case "none", "maintenance":
		return checkerdef.StatusUp, ""
	case "minor":
		return checkerdef.StatusWarning, ""
	case "major", "critical":
		return checkerdef.StatusDown, ""
	default:
		return checkerdef.StatusError, fmt.Sprintf("unknown status indicator %q", indicator)
	}
}

// componentStatus maps one component's status.
func componentStatus(status string) (checkerdef.Status, bool) {
	switch status {
	case "operational", "under_maintenance":
		return checkerdef.StatusUp, true
	case "degraded_performance":
		return checkerdef.StatusWarning, true
	case "partial_outage", "major_outage":
		return checkerdef.StatusDown, true
	default:
		return checkerdef.StatusError, false
	}
}

// severity orders the statuses a component can map to: the worst one wins.
func severity(status checkerdef.Status) int {
	switch status {
	case checkerdef.StatusDown:
		return 2 //nolint:mnd // ordering
	case checkerdef.StatusWarning:
		return 1
	default:
		return 0
	}
}

// componentsStatus judges the selected components, worst one wins. It returns
// the ids missing from the summary, and a problem for an unknown status.
func componentsStatus(
	selected []string, sum *summary,
) (status checkerdef.Status, describe string, missing []string, problem string) {
	byID := make(map[string]summaryComponent, len(sum.Components))
	for _, component := range sum.Components {
		byID[component.ID] = component
	}

	status = checkerdef.StatusUp

	for _, id := range selected {
		component, found := byID[id]
		if !found {
			missing = append(missing, id)

			continue
		}

		componentStatusValue, known := componentStatus(component.Status)
		if !known {
			return status, "", nil, fmt.Sprintf("component %q has unknown status %q", component.Name, component.Status)
		}

		if severity(componentStatusValue) > severity(status) {
			status = componentStatusValue
			describe = fmt.Sprintf("%s is %s", component.Name, strings.ReplaceAll(component.Status, "_", " "))
		}
	}

	return status, describe, missing, ""
}

// relevantIncidents returns the unresolved incidents that touch a selected
// component, or every unresolved one when the whole page is watched.
func relevantIncidents(selected []string, sum *summary) []summaryIncident {
	var out []summaryIncident

	for _, incident := range sum.Incidents {
		if incident.Status == incidentResolved || incident.Status == incidentPostmortem {
			continue
		}

		if len(selected) > 0 && !touches(incident, selected) {
			continue
		}

		out = append(out, incident)
	}

	return out
}

func touches(incident summaryIncident, selected []string) bool {
	for _, affected := range incident.Components {
		for _, id := range selected {
			if affected.ID == id {
				return true
			}
		}
	}

	return false
}

func outputComponents(sum *summary) []map[string]any {
	out := make([]map[string]any, 0, len(sum.Components))

	for _, component := range sum.Components {
		if component.Group {
			continue
		}

		if len(out) >= maxOutputComponents {
			break
		}

		out = append(out, map[string]any{"id": component.ID, "name": component.Name, "status": component.Status})
	}

	return out
}

func outputIncidents(incidents []summaryIncident) []map[string]any {
	out := make([]map[string]any, 0, min(len(incidents), maxOutputIncidents))

	for _, incident := range incidents {
		if len(out) >= maxOutputIncidents {
			break
		}

		out = append(out, map[string]any{
			"name": incident.Name, "impact": incident.Impact, "status": incident.Status,
			"shortlink": incident.Shortlink, "updatedAt": incident.UpdatedAt,
		})
	}

	return out
}

func pageName(sum *summary) string {
	if sum.Page.Name == "" {
		return "Vendor"
	}

	return sum.Page.Name
}

func quoteAll(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = fmt.Sprintf("%q", value)
	}

	return out
}
