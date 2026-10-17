package checkvendorstatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	httpconfig "github.com/fclairamb/solidping/server/internal/checkers/checkhttp/config"
)

// summaryPath is the Statuspage v2 summary, served under the page root.
const summaryPath = "/api/v2/summary.json"

// errNotSummary is the one answer for a body that is not a v2 summary.
var errNotSummary = errors.New("not a Statuspage v2 summary")

// summary is the part of the Statuspage v2 summary the check reads. Pages
// hosted by incident.io serve the same document, minus the incidents key when
// none is open.
type summary struct {
	Page struct {
		Name string `json:"name"`
	} `json:"page"`
	Status struct {
		Indicator   string `json:"indicator"`
		Description string `json:"description"`
	} `json:"status"`
	Components []summaryComponent `json:"components"`
	Incidents  []summaryIncident  `json:"incidents"`
}

type summaryComponent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	GroupID string `json:"group_id"` //nolint:tagliatelle // Statuspage field name
	// Group is true for the header that lists child components; it has no
	// status of its own worth watching.
	Group bool `json:"group"`
}

type summaryIncident struct {
	Name       string `json:"name"`
	Impact     string `json:"impact"`
	Status     string `json:"status"`
	Shortlink  string `json:"shortlink"`
	UpdatedAt  string `json:"updated_at"` //nolint:tagliatelle // Statuspage field name
	Components []struct {
		ID string `json:"id"`
	} `json:"components"`
}

// parseSummary decodes a v2 summary and refuses any other document.
func parseSummary(body []byte) (*summary, error) {
	var parsed summary
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, errNotSummary
	}

	var probe struct {
		Page   json.RawMessage `json:"page"`
		Status json.RawMessage `json:"status"`
	}

	if err := json.Unmarshal(body, &probe); err != nil || len(probe.Page) == 0 || len(probe.Status) == 0 ||
		parsed.Status.Indicator == "" {
		return nil, errNotSummary
	}

	return &parsed, nil
}

// SummaryURL returns the summary URL of a status page root.
func SummaryURL(root string) string {
	return root + summaryPath
}

// fetchOutcome is what one fetch of a summary came to.
type fetchOutcome struct {
	// httpResult is the http probe's result: duration, diagnostics, output.
	httpResult *checkerdef.Result
	// summary is set when the page answered with a valid v2 summary.
	summary *summary
	// problem is a one-line reason when summary is nil.
	problem string
}

// fetch GETs the summary of a status page root through the http checker, which
// brings the egress guard, tunnels and timeouts with it.
func fetch(ctx context.Context, client *checkhttp.HTTPChecker, root string) (*fetchOutcome, error) {
	httpResult, resp, err := client.ExecuteCapturingBody(ctx, &httpconfig.HTTPConfig{URL: SummaryURL(root)})
	if err != nil || httpResult == nil {
		return nil, err
	}

	outcome := &fetchOutcome{httpResult: httpResult}

	switch {
	case !resp.Reached:
		// The http result carries the failure; the caller keeps its message.
		outcome.problem = outputError(httpResult)
		if outcome.problem == "" {
			outcome.problem = "status page unreachable"
		}
	case resp.StatusCode != http.StatusOK:
		outcome.problem = fmt.Sprintf("status page answered HTTP %d: %s", resp.StatusCode, errNotSummary)
	default:
		parsed, parseErr := parseSummary(resp.Body)
		if parseErr != nil {
			outcome.problem = parseErr.Error()
		} else {
			outcome.summary = parsed
		}
	}

	return outcome, nil
}

func outputError(result *checkerdef.Result) string {
	text, _ := result.Output[checkerdef.OutputKeyError].(string)

	return text
}

// Component is a component of a status page, as the form lists it.
type Component struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
}

// ErrFetch wraps the reason a status page's components could not be listed.
var ErrFetch = errors.New("cannot read the status page")

// ListComponents fetches a status page and returns its components, leaving out
// the group headers. It goes through the same egress-guarded fetch as the
// check, so ctx carries the guard.
func ListComponents(ctx context.Context, root string) ([]Component, error) {
	outcome, err := fetch(ctx, &checkhttp.HTTPChecker{}, root)
	if err != nil {
		return nil, err
	}

	if outcome.summary == nil {
		return nil, fmt.Errorf("%w: %s", ErrFetch, outcome.problem)
	}

	groups := make(map[string]string)

	for _, component := range outcome.summary.Components {
		if component.Group {
			groups[component.ID] = component.Name
		}
	}

	components := make([]Component, 0, len(outcome.summary.Components))

	for _, component := range outcome.summary.Components {
		if component.Group {
			continue
		}

		components = append(components, Component{
			ID: component.ID, Name: component.Name, Group: groups[component.GroupID],
		})
	}

	return components, nil
}
