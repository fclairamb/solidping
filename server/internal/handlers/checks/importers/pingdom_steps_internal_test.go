package importers

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Every translated step is wrapped in step("<label>", …), a TODO line is not
// (spec 2026-11-17-04), so a failing run names the step.
func TestPingdomTMSWrapsStepsInStepMarker(t *testing.T) {
	t.Parallel()

	script := &pingdomScript{name: "acme", warn: &warnings{}, helpers: map[string]bool{}, secrets: map[string]string{}}
	script.generate(&pingdomTMSCheck{Steps: []pingdomTMSStep{
		{Fn: "go_to", Args: map[string]any{"url": "https://acme.example"}},
		{Fn: "click", Args: map[string]any{"element": "Se connecter"}},
		{Fn: "wpm_click_xy"},
	}})

	out := script.String()
	require.Contains(t, out, `step("Go to \"https://acme.example\"", function () {`)
	require.Contains(t, out, `step("Click \"Se connecter\"", function () {`)
	require.Contains(t, out, "// TODO step 3")
	require.NotContains(t, out, `step("wpm click xy"`)
}
