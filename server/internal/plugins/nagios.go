package plugins

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Nagios plugin exit codes.
const (
	nagiosOK       = 0
	nagiosWarning  = 1
	nagiosCritical = 2
)

// nagiosMaxOutput caps what is read from a nagios plugin's stdout.
const nagiosMaxOutput = 64 * 1024

// placeholderRE matches a {{key}} in a nagios argument template.
var placeholderRE = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}`)

// nagiosTransport forks the plugin once per run, Nagios style.
type nagiosTransport struct {
	path string
	dir  string
	args []string
	env  []string
}

func newNagiosTransport(path, dir string, args, env []string) *nagiosTransport {
	return &nagiosTransport{path: path, dir: dir, args: args, env: env}
}

// buildArgs fills the argument template from the config. A config value is
// only ever one argument (no shell), and an argument that ends up empty is
// dropped so an optional key can stay unset.
func buildArgs(template []string, config map[string]any) []string {
	out := make([]string, 0, len(template))

	for _, arg := range template {
		filled := placeholderRE.ReplaceAllStringFunc(arg, func(match string) string {
			key := placeholderRE.FindStringSubmatch(match)[1]

			value, ok := config[key]
			if !ok || value == nil {
				return ""
			}

			return fmt.Sprint(value)
		})

		if filled != "" {
			out = append(out, filled)
		}
	}

	return out
}

func (t *nagiosTransport) execute(ctx context.Context, _ string, cfg *Config) (*checkerdef.Result, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout())
	defer cancel()

	// The path comes from the manifest, resolved inside the plugin folder;
	// args are never shell-parsed.
	cmd := exec.CommandContext(ctx, t.path, buildArgs(t.args, cfg.Values())...)
	cmd.Dir = t.dir
	cmd.Env = t.env
	cmd.WaitDelay = time.Second
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killProcessGroup(cmd) }

	var stdout bytes.Buffer

	cmd.Stdout = &limitedWriter{buf: &stdout, max: nagiosMaxOutput}

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &checkerdef.Result{
			Status: checkerdef.StatusTimeout, Duration: duration,
			Output: map[string]any{checkerdef.OutputKeyError: "plugin killed after " + cfg.timeout().String()},
		}, nil
	}

	exitCode := 0

	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return &checkerdef.Result{
				Status: checkerdef.StatusError, Duration: duration,
				Output: map[string]any{checkerdef.OutputKeyError: "running plugin: " + runErr.Error()},
			}, nil
		}

		exitCode = exitErr.ExitCode()
	}

	message, metrics := parseNagiosOutput(stdout.String())

	output := map[string]any{outputKeyMessage: message, "exitCode": exitCode}

	return &checkerdef.Result{
		Status: nagiosStatus(exitCode), Duration: duration, Output: output, Metrics: metrics,
	}, nil
}

func (t *nagiosTransport) close() {}

// nagiosStatus maps a Nagios exit code: 0 up, 1 warning, 2 down, anything
// else (3 is UNKNOWN) error.
func nagiosStatus(exitCode int) checkerdef.Status {
	switch exitCode {
	case nagiosOK:
		return checkerdef.StatusUp
	case nagiosWarning:
		return checkerdef.StatusWarning
	case nagiosCritical:
		return checkerdef.StatusDown
	default:
		return checkerdef.StatusError
	}
}

// parseNagiosOutput splits a plugin's stdout into the first line's text and
// its perfdata: `TEXT | label=value[UOM];warn;crit;min;max ...`. Perfdata on
// later lines (after a `|` there too) is read as well. A malformed item is
// ignored.
func parseNagiosOutput(stdout string) (string, map[string]any) {
	lines := strings.Split(strings.ReplaceAll(stdout, "\r\n", "\n"), "\n")
	message := ""
	metrics := map[string]any{}

	for i, line := range lines {
		text, perf, hasPerf := strings.Cut(line, "|")
		if i == 0 {
			message = strings.TrimSpace(text)
		}

		if hasPerf {
			parsePerfdata(perf, metrics)
		}
	}

	return message, metrics
}

// perfValueRE is a perfdata value with its optional unit of measure.
var perfValueRE = regexp.MustCompile(`^(-?[0-9]+(?:\.[0-9]+)?)([a-zA-Z%]*)$`)

// parsePerfdata reads `label=value[UOM];warn;crit;min;max` items. The label
// may be quoted with single quotes to hold spaces.
func parsePerfdata(perf string, metrics map[string]any) {
	for _, item := range splitPerfItems(perf) {
		label, rest, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}

		label = strings.Trim(label, "'")
		if label == "" {
			continue
		}

		valuePart, _, _ := strings.Cut(rest, ";")

		match := perfValueRE.FindStringSubmatch(valuePart)
		if match == nil {
			continue
		}

		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}

		metrics[label] = value
	}
}

// splitPerfItems splits perfdata on spaces outside single-quoted labels.
func splitPerfItems(perf string) []string {
	var items []string

	var current strings.Builder

	quoted := false

	for _, char := range perf {
		switch {
		case char == '\'':
			quoted = !quoted

			current.WriteRune(char)
		case char == ' ' && !quoted:
			if current.Len() > 0 {
				items = append(items, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(char)
		}
	}

	if current.Len() > 0 {
		items = append(items, current.String())
	}

	return items
}

// limitedWriter keeps the first max bytes and discards the rest, so a chatty
// plugin cannot grow the server's memory.
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if len(data) > room {
			w.buf.Write(data[:room])
		} else {
			w.buf.Write(data)
		}
	}

	return len(data), nil
}
