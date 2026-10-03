// Package checkhealth reads an application's health endpoint and reports its
// state per component. The request is an http check's; the response body is
// parsed with one of the formats in the formats package.
package checkhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhealth/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhealth/formats"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
)

const (
	// maxOutputComponents caps the components kept in the result output.
	maxOutputComponents = 100

	// maxMetaMetrics caps the numeric meta values exported as metrics.
	maxMetaMetrics = 20

	errNotRecognised = "health response not recognised"

	outputKeyFormat              = "format"
	outputKeyFinishedAt          = "finished_at"
	outputKeyComponents          = "components"
	outputKeyComponentsTruncated = "components_truncated"
	outputKeyFailed              = "failed"
	outputKeyWarning             = "warning"

	metricComponentsTotal   = "components_total"
	metricComponentsFailed  = "components_failed"
	metricComponentsWarning = "components_warning"
)

// now is the clock, replaceable in tests.
//
//nolint:gochecknoglobals // test seam
var now = time.Now

// HealthChecker implements the Checker interface for health checks.
type HealthChecker struct {
	http checkhttp.HTTPChecker
}

// Type returns the check type identifier.
func (c *HealthChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeHealth
}

// Validate checks that the configuration is valid.
func (c *HealthChecker) Validate(spec *checkerdef.CheckSpec) error {
	return config.ValidateSpec(spec)
}

// Execute sends the request, parses the health document and judges it.
func (c *HealthChecker) Execute(ctx context.Context, cfgIn checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*HealthConfig](cfgIn)
	if err != nil {
		return nil, err
	}

	httpResult, resp, err := c.http.ExecuteCapturingBody(ctx, &cfg.HTTPConfig)
	if err != nil || httpResult == nil {
		return httpResult, err
	}

	// No usable response (network error, timeout, wrong protocol, unreadable
	// body): the http result is the answer, diagnostics included.
	if !resp.Reached {
		return httpResult, nil
	}

	result := &checkerdef.Result{
		Duration:    httpResult.Duration,
		Output:      httpResult.Output,
		Diagnostics: httpResult.Diagnostics,
	}

	if result.Output == nil {
		result.Output = make(map[string]any)
	}

	judge(cfg, resp, result)

	if result.Status == checkerdef.StatusUp || result.Status == checkerdef.StatusWarning {
		result.Diagnostics = nil
	}

	return result, nil
}

// judge fills result's status, output and metrics from the response.
func judge(cfg *HealthConfig, resp *checkhttp.Response, result *checkerdef.Result) {
	var document any
	if err := json.Unmarshal(resp.Body, &document); err != nil {
		notRecognised(result, resp)

		return
	}

	body, isObject := document.(map[string]any)
	if !isObject {
		notRecognised(result, resp)

		return
	}

	report, err := formats.Parse(cfg.EffectiveFormat(), resp.ContentType, body)
	if err != nil {
		notRecognised(result, resp)

		return
	}

	result.Output[outputKeyFormat] = report.Format
	if report.FinishedAt != nil {
		result.Output[outputKeyFinishedAt] = report.FinishedAt.Format(time.RFC3339)
	}

	failed, warning, downNames, downMessages := classify(cfg, report)
	writeComponents(cfg, report, result)
	result.Output[outputKeyFailed] = failed
	result.Output[outputKeyWarning] = warning
	result.Metrics = buildMetrics(cfg, report, len(failed), len(warning))

	// Rule 3: stale results.
	if maxAge := cfg.EffectiveMaxAge(); maxAge > 0 && report.FinishedAt != nil {
		if age := now().Sub(*report.FinishedAt); age > maxAge {
			result.Status = checkerdef.StatusDown
			result.Output[checkerdef.OutputKeyError] = fmt.Sprintf(
				"health results are stale (last run %s ago)", humanAge(age))

			return
		}
	}

	switch {
	case len(downNames) > 0: // rule 4
		result.Status = checkerdef.StatusDown
		result.Output[checkerdef.OutputKeyError] = strings.Join(downMessages, "; ")
	case len(warning) > 0 || len(failed) > 0: // rule 5 (failed here are all downgraded)
		result.Status = checkerdef.StatusWarning
	case len(report.Components) == 0: // rule 6
		overallVerdict(report, result)
	default: // rule 7
		result.Status = checkerdef.StatusUp
	}
}

// overallVerdict applies the application's own status when it listed no components.
func overallVerdict(report formats.Report, result *checkerdef.Result) {
	switch report.Overall {
	case formats.StatusFailed:
		result.Status = checkerdef.StatusDown
		result.Output[checkerdef.OutputKeyError] = fmt.Sprintf("health endpoint reports status %q", report.Overall)
	case formats.StatusWarning:
		result.Status = checkerdef.StatusWarning
	default:
		result.Status = checkerdef.StatusUp
	}
}

func notRecognised(result *checkerdef.Result, resp *checkhttp.Response) {
	result.Status = checkerdef.StatusDown
	result.Output[checkerdef.OutputKeyError] = fmt.Sprintf("%s (HTTP %d)", errNotRecognised, resp.StatusCode)
}

// classify lists the non-ignored failed and warning components, and the ones
// among the failed that bring the check down, with their `Name: message` text.
func classify(cfg *HealthConfig, report formats.Report) (failed, warning, downNames, downMessages []string) {
	failed, warning = []string{}, []string{}

	for _, component := range report.Components {
		if cfg.IsIgnored(component.Name) {
			continue
		}

		switch component.Status {
		case formats.StatusFailed:
			failed = append(failed, component.Name)

			if cfg.OnFailed(component.Name) == config.OnFailedDown {
				downNames = append(downNames, component.Name)

				text := component.Name
				if component.Message != "" {
					text += ": " + component.Message
				}

				downMessages = append(downMessages, text)
			}
		case formats.StatusWarning:
			warning = append(warning, component.Name)
		case formats.StatusOK, formats.StatusSkipped, formats.StatusUnknown:
		}
	}

	return failed, warning, downNames, downMessages
}

// writeComponents stores at most maxOutputComponents components in the output.
func writeComponents(cfg *HealthConfig, report formats.Report, result *checkerdef.Result) {
	components := make([]map[string]any, 0, min(len(report.Components), maxOutputComponents))

	for index, component := range report.Components {
		if index >= maxOutputComponents {
			result.Output[outputKeyComponentsTruncated] = true

			break
		}

		entry := map[string]any{"name": component.Name, "status": string(component.Status)}
		if component.Label != "" && component.Label != component.Name {
			entry["label"] = component.Label
		}

		if component.Summary != "" {
			entry["summary"] = component.Summary
		}

		if component.Message != "" {
			entry["message"] = component.Message
		}

		if cfg.IsIgnored(component.Name) {
			entry["ignored"] = true
		}

		components = append(components, entry)
	}

	result.Output[outputKeyComponents] = components
}

// buildMetrics counts components and exports numeric meta values, in component
// order, up to maxMetaMetrics.
func buildMetrics(cfg *HealthConfig, report formats.Report, failed, warning int) map[string]any {
	metrics := map[string]any{
		metricComponentsTotal:   float64(len(report.Components)),
		metricComponentsFailed:  float64(failed),
		metricComponentsWarning: float64(warning),
	}

	added := 0

	for _, component := range report.Components {
		if cfg.IsIgnored(component.Name) {
			continue
		}

		keys := make([]string, 0, len(component.Meta))
		for key, value := range component.Meta {
			if _, numeric := value.(float64); numeric {
				keys = append(keys, key)
			}
		}

		sort.Strings(keys)

		for _, key := range keys {
			if added >= maxMetaMetrics {
				return metrics
			}

			metrics["meta."+component.Name+"."+key] = component.Meta[key]
			added++
		}
	}

	return metrics
}

// humanAge renders an age as "23 min" / "3 h" / "45 s".
func humanAge(age time.Duration) string {
	switch {
	case age >= 48*time.Hour:
		return fmt.Sprintf("%d d", int(age.Hours()/24))
	case age >= time.Hour:
		return fmt.Sprintf("%d h", int(age.Hours()))
	case age >= time.Minute:
		return fmt.Sprintf("%d min", int(age.Minutes()))
	default:
		return fmt.Sprintf("%d s", int(age.Seconds()))
	}
}
