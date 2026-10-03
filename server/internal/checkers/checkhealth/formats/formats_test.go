package formats_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkhealth/formats"
)

func load(t *testing.T, name string) map[string]any {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))

	return body
}

func contentTypeFor(name string) string {
	if name == "ietf" {
		return "application/health+json"
	}

	return "application/json"
}

func statuses(report formats.Report) map[string]formats.ComponentStatus {
	out := map[string]formats.ComponentStatus{}
	for _, component := range report.Components {
		out[component.Name] = component.Status
	}

	return out
}

func TestDetectEachFixtureMatchesOnlyItsFormat(t *testing.T) {
	t.Parallel()

	names := []string{"spatie", "spring", "ietf", "aspnet", "microprofile", "simple"}

	for _, fixture := range names {
		body := load(t, fixture)

		for _, format := range formats.All {
			// simple is the fallback: any top-level status matches it, which is
			// why it comes last in the auto order.
			if format.Name == "simple" && fixture != "simple" {
				continue
			}

			got := format.Detect(contentTypeFor(fixture), body)
			require.Equal(t, format.Name == fixture, got, "format %s on fixture %s", format.Name, fixture)
		}

		detected, ok := formats.Detect(contentTypeFor(fixture), body)
		require.True(t, ok)
		require.Equal(t, fixture, detected.Name, "auto picks the right format for %s", fixture)
	}
}

func TestDetectIETFByContentTypeAlone(t *testing.T) {
	t.Parallel()

	format, ok := formats.Detect("application/health+json; charset=utf-8", map[string]any{"status": "pass"})
	require.True(t, ok)
	require.Equal(t, "ietf", format.Name)
}

func TestSpatie(t *testing.T) {
	t.Parallel()

	report, err := formats.Parse("spatie", "", load(t, "spatie"))
	require.NoError(t, err)
	require.Equal(t, map[string]formats.ComponentStatus{
		"Database":      formats.StatusOK,
		"UsedDiskSpace": formats.StatusFailed,
		"Cache":         formats.StatusWarning,
		"Queue":         formats.StatusFailed, // crashed
		"Horizon":       formats.StatusSkipped,
	}, statuses(report))

	require.NotNil(t, report.FinishedAt)
	require.Equal(t, time.Unix(1759482720, 0).UTC(), *report.FinishedAt)

	disk := report.Components[1]
	require.Equal(t, "Used Disk Space", disk.Label)
	require.Equal(t, "The disk is almost full (91% used)", disk.Message)
	require.Equal(t, "91%", disk.Summary)
	require.InDelta(t, 91, disk.Meta["disk_space_used_percentage"], 0)
}

func TestSpring(t *testing.T) {
	t.Parallel()

	report, err := formats.Parse("spring", "", load(t, "spring"))
	require.NoError(t, err)
	require.Equal(t, formats.StatusFailed, report.Overall)
	require.Equal(t, map[string]formats.ComponentStatus{
		"db":               formats.StatusOK,
		"diskSpace":        formats.StatusOK,
		"mail":             formats.StatusFailed,
		"broker.primary":   formats.StatusOK,
		"broker.secondary": formats.StatusFailed, // OUT_OF_SERVICE
		"broker.audit":     formats.StatusUnknown,
	}, statuses(report))

	for _, component := range report.Components {
		if component.Name == "mail" {
			require.Contains(t, component.Message, "connection refused")
		}
	}
}

func TestSpringLegacyDetails(t *testing.T) {
	t.Parallel()

	body := map[string]any{
		"status":  "UP",
		"details": map[string]any{"db": map[string]any{"status": "UP"}},
	}

	report, err := formats.Parse("auto", "", body)
	require.NoError(t, err)
	require.Equal(t, "spring", report.Format)
	require.Equal(t, formats.StatusOK, statuses(report)["db"])
}

func TestIETF(t *testing.T) {
	t.Parallel()

	report, err := formats.Parse("ietf", "application/health+json", load(t, "ietf"))
	require.NoError(t, err)
	require.Equal(t, formats.StatusWarning, report.Overall)

	got := statuses(report)
	require.Equal(t, formats.StatusOK, got["cassandra:responseTime"])
	require.Equal(t, formats.StatusOK, got["uptime"])
	// Several entries under one key get distinct, componentId-suffixed names.
	require.Equal(t, formats.StatusWarning, got["memory:utilization#node-1"])
	require.Equal(t, formats.StatusFailed, got["memory:utilization#node-2"])
	require.Len(t, got, 4)

	for _, component := range report.Components {
		if component.Name == "cassandra:responseTime" {
			require.Equal(t, "250 ms", component.Summary)
		}

		if component.Name == "memory:utilization#node-2" {
			require.Equal(t, "memory exhausted", component.Message)
		}
	}

	require.NotNil(t, report.FinishedAt)
	require.Equal(t, 2018, report.FinishedAt.Year())
}

func TestASPNet(t *testing.T) {
	t.Parallel()

	report, err := formats.Parse("aspnet", "", load(t, "aspnet"))
	require.NoError(t, err)
	require.Equal(t, map[string]formats.ComponentStatus{
		"sql":   formats.StatusFailed,
		"redis": formats.StatusOK,
		"cache": formats.StatusWarning, // Degraded
	}, statuses(report))
	require.Equal(t, formats.StatusFailed, report.Overall)

	for _, component := range report.Components {
		if component.Name == "sql" {
			require.Equal(t, "Connection refused", component.Message)
		}
	}
}

func TestMicroProfile(t *testing.T) {
	t.Parallel()

	report, err := formats.Parse("microprofile", "", load(t, "microprofile"))
	require.NoError(t, err)
	require.Equal(t, map[string]formats.ComponentStatus{
		"database": formats.StatusOK,
		"kafka":    formats.StatusFailed,
	}, statuses(report))
}

func TestSimpleStatusWords(t *testing.T) {
	t.Parallel()

	cases := map[string]formats.ComponentStatus{
		"ok": formats.StatusOK, "UP": formats.StatusOK, "pass": formats.StatusOK, "Healthy": formats.StatusOK,
		"warn": formats.StatusWarning, "degraded": formats.StatusWarning,
		"banana": formats.StatusFailed, "down": formats.StatusFailed, "": formats.StatusFailed,
	}

	for word, want := range cases {
		report, err := formats.Parse("simple", "", map[string]any{"status": word})
		require.NoError(t, err)
		require.Equal(t, want, report.Overall, word)
		require.Empty(t, report.Components)
	}
}

func TestForcedFormatOnWrongShapeFails(t *testing.T) {
	t.Parallel()

	for _, forced := range []string{"spring", "spatie", "aspnet", "microprofile", "ietf"} {
		_, err := formats.Parse(forced, "application/json", map[string]any{"unrelated": true})
		require.ErrorIs(t, err, formats.ErrNotRecognised, forced)
	}

	_, err := formats.Parse("spring", "", load(t, "spatie"))
	require.ErrorIs(t, err, formats.ErrNotRecognised)
}

func TestAutoNothingMatches(t *testing.T) {
	t.Parallel()

	_, err := formats.Parse("auto", "", map[string]any{"hello": "world"})
	require.ErrorIs(t, err, formats.ErrNotRecognised)
}
