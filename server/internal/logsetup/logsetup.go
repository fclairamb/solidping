// Package logsetup builds the process slog handler. It resolves the "auto" log
// format against the environment (Kubernetes, terminal, container) and decides
// whether the pretty format writes ANSI colors.
package logsetup

import (
	"io"
	"log/slog"
	"os"

	"github.com/lmittmann/tint"
	"golang.org/x/term"

	"github.com/fclairamb/solidping/server/internal/config"
)

const (
	// prettyTimeFormat is the short local timestamp of the pretty format. The
	// date is left out: a terminal session or `docker logs` rarely spans days,
	// and the other formats keep the full RFC 3339 time.
	prettyTimeFormat = "15:04:05.000"

	// colorBrightRed is the ANSI 8-bit color of error attribute values.
	colorBrightRed = 9
)

// containerMarkers are the files a container runtime leaves at the root of the
// container filesystem: Docker writes /.dockerenv, Podman /run/.containerenv.
//
//nolint:gochecknoglobals // read-only lookup table
var containerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// Environment is what format and color resolution look at. Every input is
// injectable, so the decision table is testable without a real terminal,
// container or Kubernetes pod.
type Environment struct {
	// Getenv reads an environment variable, "" when unset.
	Getenv func(key string) string
	// IsTerminal reports whether the log writer itself is a terminal.
	IsTerminal bool
	// FileExists reports whether a file exists at path.
	FileExists func(path string) bool
}

// SystemEnvironment probes the real process for a logger writing to out.
func SystemEnvironment(out io.Writer) Environment {
	return Environment{
		Getenv:     os.Getenv,
		IsTerminal: isTerminal(out),
		FileExists: fileExists,
	}
}

func (e Environment) inKubernetes() bool {
	return e.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

func (e Environment) inContainer() bool {
	for _, marker := range containerMarkers {
		if e.FileExists(marker) {
			return true
		}
	}

	return false
}

// ResolveFormat turns config.LogFormatAuto into a concrete format. An explicit
// format is returned unchanged.
//
//   - Kubernetes (KUBERNETES_SERVICE_HOST set): json, a collector reads stdout.
//   - The writer is a terminal: pretty.
//   - In a container (/.dockerenv, /run/.containerenv): pretty, for
//     `docker logs` and Docker Desktop.
//   - Otherwise: text.
func ResolveFormat(format config.LogFormat, env Environment) config.LogFormat {
	if format != config.LogFormatAuto {
		return format
	}

	switch {
	case env.inKubernetes():
		return config.LogFormatJSON
	case env.IsTerminal:
		return config.LogFormatPretty
	case env.inContainer():
		return config.LogFormatPretty
	default:
		return config.LogFormatText
	}
}

// UseColor reports whether the pretty format writes ANSI colors. NO_COLOR
// disables them and FORCE_COLOR forces them. Otherwise they are on when the
// writer is a terminal or the process runs in a container (Docker Desktop and
// `docker logs` render ANSI).
func UseColor(env Environment) bool {
	switch {
	case env.Getenv("NO_COLOR") != "":
		return false
	case env.Getenv("FORCE_COLOR") != "":
		return true
	default:
		return env.IsTerminal || env.inContainer()
	}
}

// NewHandler builds the handler for format, writing to out at level. The auto
// format and the colors are resolved here, once, against the real environment
// and against out itself (not os.Stdout: the client and mcp commands log to
// stderr).
func NewHandler(out io.Writer, level slog.Level, format config.LogFormat) slog.Handler {
	return newHandler(out, level, format, SystemEnvironment(out))
}

func newHandler(out io.Writer, level slog.Level, format config.LogFormat, env Environment) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}

	switch ResolveFormat(format, env) {
	case config.LogFormatJSON:
		return slog.NewJSONHandler(out, opts)
	case config.LogFormatPretty:
		return newPrettyHandler(out, level, UseColor(env))
	case config.LogFormatText, config.LogFormatAuto:
		return slog.NewTextHandler(out, opts)
	default:
		return slog.NewTextHandler(out, opts)
	}
}

// newPrettyHandler is the human-first format: short timestamp, short colored
// level, the message, then the attributes in record order (the logger's own
// WithAttrs ones first, group names as a "group." prefix).
func newPrettyHandler(out io.Writer, level slog.Level, color bool) slog.Handler {
	opts := &tint.Options{
		Level:      level,
		TimeFormat: prettyTimeFormat,
		NoColor:    !color,
	}

	if color {
		enableANSI(out)

		opts.ReplaceAttr = redErrors
	}

	return tint.NewTextHandler(out, opts)
}

// redErrors writes the value of an "error" or "err" attribute in red.
func redErrors(_ []string, attr slog.Attr) slog.Attr {
	if attr.Key == "error" || attr.Key == "err" {
		return tint.Attr(colorBrightRed, attr)
	}

	return attr
}

// fdWriter is a writer backed by a file descriptor, such as *os.File.
type fdWriter interface {
	Fd() uintptr
}

func isTerminal(out io.Writer) bool {
	file, ok := out.(fdWriter)
	if !ok {
		return false
	}

	return term.IsTerminal(int(file.Fd()))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
