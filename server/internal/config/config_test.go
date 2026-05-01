package config

import (
	"log/slog"
	"testing"
)

const (
	testPathDashboard = "/dashboard"
	testPathStatus    = "/status"
	testHost5173      = "localhost:5173"
	testHost5174      = "localhost:5174"
)

func TestParseRedirectRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected RedirectRule
		ok       bool
	}{
		{
			name:  "simple redirect with same path",
			input: "/dashboard:localhost:5173",
			expected: RedirectRule{
				PathPrefix: testPathDashboard,
				TargetHost: testHost5173,
				TargetPath: testPathDashboard,
			},
			ok: true,
		},
		{
			name:  "redirect with different target path",
			input: "/dashboard:localhost:5173/app",
			expected: RedirectRule{
				PathPrefix: testPathDashboard,
				TargetHost: testHost5173,
				TargetPath: "/app",
			},
			ok: true,
		},
		{
			name:  "root redirect",
			input: "/:localhost:5173",
			expected: RedirectRule{
				PathPrefix: "/",
				TargetHost: testHost5173,
				TargetPath: "/",
			},
			ok: true,
		},
		{
			name:  "redirect with nested target path",
			input: "/api:localhost:8080/v1/api",
			expected: RedirectRule{
				PathPrefix: "/api",
				TargetHost: "localhost:8080",
				TargetPath: "/v1/api",
			},
			ok: true,
		},
	}

	runRedirectRuleTests(t, tests)
}

func TestParseRedirectRuleInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected RedirectRule
		ok       bool
	}{
		{
			name:     "missing leading slash",
			input:    "dashboard:localhost:5173",
			expected: RedirectRule{},
			ok:       false,
		},
		{
			name:     "missing target",
			input:    "/dashboard:",
			expected: RedirectRule{},
			ok:       false,
		},
		{
			name:     "no colon separator",
			input:    "/dashboard",
			expected: RedirectRule{},
			ok:       false,
		},
		{
			name:     "empty input",
			input:    "",
			expected: RedirectRule{},
			ok:       false,
		},
	}

	runRedirectRuleTests(t, tests)
}

func runRedirectRuleTests(
	t *testing.T,
	tests []struct {
		name     string
		input    string
		expected RedirectRule
		ok       bool
	},
) {
	t.Helper()

	for index := range tests {
		testCase := &tests[index]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result, isValid := parseRedirectRule(testCase.input)
			if isValid != testCase.ok {
				t.Errorf("parseRedirectRule(%q) ok = %v, want %v", testCase.input, isValid, testCase.ok)

				return
			}

			if isValid && result != testCase.expected {
				t.Errorf("parseRedirectRule(%q) = %+v, want %+v", testCase.input, result, testCase.expected)
			}
		})
	}
}

func TestParseRedirects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []RedirectRule
	}{
		{
			name:     "empty string",
			input:    "",
			expected: nil,
		},
		{
			name:  "single rule",
			input: "/dashboard:localhost:5173",
			expected: []RedirectRule{
				{PathPrefix: testPathDashboard, TargetHost: testHost5173, TargetPath: testPathDashboard},
			},
		},
		{
			name:  "multiple rules",
			input: "/dashboard:localhost:5173/dashboard,/status:localhost:5174/status",
			expected: []RedirectRule{
				{PathPrefix: testPathDashboard, TargetHost: testHost5173, TargetPath: testPathDashboard},
				{PathPrefix: testPathStatus, TargetHost: testHost5174, TargetPath: testPathStatus},
			},
		},
		{
			name:  "rules sorted by path length",
			input: "/:localhost:5173,/dashboard/settings:localhost:5173,/dashboard:localhost:5173",
			expected: []RedirectRule{
				{PathPrefix: "/dashboard/settings", TargetHost: testHost5173, TargetPath: "/dashboard/settings"},
				{PathPrefix: testPathDashboard, TargetHost: testHost5173, TargetPath: testPathDashboard},
				{PathPrefix: "/", TargetHost: testHost5173, TargetPath: "/"},
			},
		},
		{
			name:  "whitespace handling",
			input: " /dashboard:localhost:5173 , /status:localhost:5174 ",
			expected: []RedirectRule{
				{PathPrefix: testPathDashboard, TargetHost: testHost5173, TargetPath: testPathDashboard},
				{PathPrefix: testPathStatus, TargetHost: testHost5174, TargetPath: testPathStatus},
			},
		},
		{
			name:  "invalid rules skipped",
			input: "/valid:localhost:5173,invalid,/also-valid:localhost:5174",
			expected: []RedirectRule{
				{PathPrefix: "/also-valid", TargetHost: testHost5174, TargetPath: "/also-valid"},
				{PathPrefix: "/valid", TargetHost: testHost5173, TargetPath: "/valid"},
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := parseRedirects(testCase.input)
			if len(result) != len(testCase.expected) {
				t.Errorf("parseRedirects(%q) returned %d rules, want %d", testCase.input, len(result), len(testCase.expected))

				return
			}

			for i := range result {
				if result[i] != testCase.expected[i] {
					t.Errorf("parseRedirects(%q)[%d] = %+v, want %+v", testCase.input, i, result[i], testCase.expected[i])
				}
			}
		})
	}
}

func TestParseLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected slog.Level
	}{
		{
			name:     "debug lowercase",
			input:    "debug",
			expected: slog.LevelDebug,
		},
		{
			name:     "debug uppercase",
			input:    "DEBUG",
			expected: slog.LevelDebug,
		},
		{
			name:     "info lowercase",
			input:    "info",
			expected: slog.LevelInfo,
		},
		{
			name:     "info uppercase",
			input:    "INFO",
			expected: slog.LevelInfo,
		},
		{
			name:     "warn lowercase",
			input:    "warn",
			expected: slog.LevelWarn,
		},
		{
			name:     "warning lowercase",
			input:    "warning",
			expected: slog.LevelWarn,
		},
		{
			name:     "warn uppercase",
			input:    "WARN",
			expected: slog.LevelWarn,
		},
		{
			name:     "error lowercase",
			input:    "error",
			expected: slog.LevelError,
		},
		{
			name:     "error uppercase",
			input:    "ERROR",
			expected: slog.LevelError,
		},
		{
			name:     "empty string defaults to info",
			input:    "",
			expected: slog.LevelInfo,
		},
		{
			name:     "invalid value defaults to info",
			input:    "invalid",
			expected: slog.LevelInfo,
		},
		{
			name:     "whitespace trimmed",
			input:    "  debug  ",
			expected: slog.LevelDebug,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := ParseLogLevel(testCase.input)
			if result != testCase.expected {
				t.Errorf("ParseLogLevel(%q) = %v, want %v", testCase.input, result, testCase.expected)
			}
		})
	}
}
