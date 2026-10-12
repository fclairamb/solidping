package logsetup

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

var errDiskFull = errors.New("disk full")

// testEnv builds an Environment from a fixed set of variables and files.
func testEnv(vars map[string]string, tty bool, files ...string) Environment {
	return Environment{
		Getenv:     func(key string) string { return vars[key] },
		IsTerminal: tty,
		FileExists: func(path string) bool {
			for _, file := range files {
				if file == path {
					return true
				}
			}

			return false
		},
	}
}

func TestResolveFormat(t *testing.T) {
	t.Parallel()

	k8s := map[string]string{"KUBERNETES_SERVICE_HOST": "10.0.0.1"}

	tests := []struct {
		name     string
		format   config.LogFormat
		env      Environment
		expected config.LogFormat
	}{
		{
			name:     "auto on kubernetes is json",
			format:   config.LogFormatAuto,
			env:      testEnv(k8s, false),
			expected: config.LogFormatJSON,
		},
		{
			name:     "auto on kubernetes beats a terminal",
			format:   config.LogFormatAuto,
			env:      testEnv(k8s, true, "/.dockerenv"),
			expected: config.LogFormatJSON,
		},
		{
			name:     "auto on a terminal is pretty",
			format:   config.LogFormatAuto,
			env:      testEnv(nil, true),
			expected: config.LogFormatPretty,
		},
		{
			name:     "auto in docker is pretty",
			format:   config.LogFormatAuto,
			env:      testEnv(nil, false, "/.dockerenv"),
			expected: config.LogFormatPretty,
		},
		{
			name:     "auto in podman is pretty",
			format:   config.LogFormatAuto,
			env:      testEnv(nil, false, "/run/.containerenv"),
			expected: config.LogFormatPretty,
		},
		{
			name:     "auto piped outside a container is text",
			format:   config.LogFormatAuto,
			env:      testEnv(nil, false),
			expected: config.LogFormatText,
		},
		{
			name:     "explicit text wins on a terminal",
			format:   config.LogFormatText,
			env:      testEnv(nil, true),
			expected: config.LogFormatText,
		},
		{
			name:     "explicit pretty wins on kubernetes",
			format:   config.LogFormatPretty,
			env:      testEnv(k8s, false),
			expected: config.LogFormatPretty,
		},
		{
			name:     "explicit json wins in docker",
			format:   config.LogFormatJSON,
			env:      testEnv(nil, true, "/.dockerenv"),
			expected: config.LogFormatJSON,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.expected, ResolveFormat(testCase.format, testCase.env))
		})
	}
}

func TestUseColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      Environment
		expected bool
	}{
		{name: "terminal", env: testEnv(nil, true), expected: true},
		{name: "container", env: testEnv(nil, false, "/.dockerenv"), expected: true},
		{name: "piped", env: testEnv(nil, false), expected: false},
		{name: "NO_COLOR on a terminal", env: testEnv(map[string]string{"NO_COLOR": "1"}, true), expected: false},
		{name: "FORCE_COLOR when piped", env: testEnv(map[string]string{"FORCE_COLOR": "1"}, false), expected: true},
		{
			name:     "NO_COLOR beats FORCE_COLOR",
			env:      testEnv(map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}, true),
			expected: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, testCase.expected, UseColor(testCase.env))
		})
	}
}

func TestNewHandlerFormats(t *testing.T) {
	t.Parallel()

	t.Run("auto on kubernetes writes json", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		var buf bytes.Buffer

		env := testEnv(map[string]string{"KUBERNETES_SERVICE_HOST": "10.0.0.1"}, false)
		slog.New(newHandler(&buf, slog.LevelInfo, config.LogFormatAuto, env)).Info("hello", "key", "value")

		var record map[string]any
		r.NoError(json.Unmarshal(buf.Bytes(), &record))
		r.Equal("hello", record["msg"])
		r.Equal("value", record["key"])
	})

	t.Run("auto piped writes text", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		var buf bytes.Buffer

		slog.New(newHandler(&buf, slog.LevelInfo, config.LogFormatAuto, testEnv(nil, false))).Info("hello")

		r.Contains(buf.String(), "level=INFO msg=hello")
	})

	t.Run("level is honored", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		var buf bytes.Buffer

		logger := slog.New(newHandler(&buf, slog.LevelWarn, config.LogFormatPretty, testEnv(nil, false)))
		logger.Info("dropped")
		logger.Warn("kept")

		r.NotContains(buf.String(), "dropped")
		r.Contains(buf.String(), "kept")
	})
}

// prettyLine matches one uncolored pretty line: short timestamp, short level,
// message, attributes.
var prettyLine = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}\.\d{3} INF starting server a=1 http.b=2 http.c=3 d=4 z=last\n$`)

func TestPrettyAttributeOrder(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var buf bytes.Buffer

	logger := slog.New(newPrettyHandler(&buf, slog.LevelInfo, false))
	logger.With("a", 1).WithGroup("http").With("b", 2).Info("starting server", "c", 3, slog.Group("", "d", 4))

	r.Regexp(`^\d{2}:\d{2}:\d{2}\.\d{3} INF starting server a=1 http.b=2 http.c=3 http.d=4\n$`, buf.String())

	// Same keys every time, same order every time: the old handler went
	// through a JSON map and shuffled them per line.
	for range 20 {
		buf.Reset()
		logger := slog.New(newPrettyHandler(&buf, slog.LevelInfo, false))
		logger.With("a", 1).Info("starting server", slog.Group("http", "b", 2, "c", 3), "d", 4, "z", "last")
		r.Regexp(prettyLine, buf.String())
	}
}

func TestPrettyColors(t *testing.T) {
	t.Parallel()

	t.Run("errors are red", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		var buf bytes.Buffer

		logger := slog.New(newPrettyHandler(&buf, slog.LevelInfo, true))
		logger.Error("boom", "error", errDiskFull, "err", "short", "other", "plain")

		out := buf.String()
		r.Contains(out, "\x1b[91m", "the level and error values use bright red")
		r.Regexp(`\x1b\[2;91merror=\x1b\[22m"?disk full"?\x1b\[0m`, out)
		r.Regexp(`\x1b\[2;91merr=\x1b\[22mshort\x1b\[0m`, out)
		r.Regexp(`\x1b\[2mother=\x1b\[0mplain`, out, "other attributes keep the default color")
	})

	t.Run("no color writes no escape sequence", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)

		var buf bytes.Buffer

		logger := slog.New(newPrettyHandler(&buf, slog.LevelInfo, false))
		logger.Error("boom", "error", errDiskFull)

		r.NotContains(buf.String(), "\x1b")
		r.Contains(buf.String(), `ERR boom error="disk full"`)
	})
}
