// Package plugins loads check types from the operator's plugin directory
// (spec 2026-10-17-05): one folder per plugin, each with a manifest.json that
// declares `x-` prefixed check types, their JSON Schema and how they run.
//
// The manifest schema is the validator of a plugin type. Execution goes
// through one of three transports:
//
//   - "js": the entry script runs in the checkjs runtime (sandboxed, same
//     helpers, egress guard and tunnels as a js check);
//   - "exec": one long-lived process per plugin, JSON-RPC 2.0 over stdio;
//   - "nagios": one process per run, Nagios plugin exit codes and perfdata.
//
// Every result goes through LimitResult whatever the transport.
package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ManifestFile is the name of the manifest in each plugin folder.
const ManifestFile = "manifest.json"

// Plugin transports.
const (
	ProtocolJS     = "js"
	ProtocolExec   = "exec"
	ProtocolNagios = "nagios"
	ProtocolHTTP   = "http"
)

// Labels a manifest may not decide on its own.
const (
	labelSafe   = "safe"
	labelUnsafe = "unsafe"
	labelCatPfx = "category:"
	labelCatOth = "category:other"
)

// maxManifestBytes bounds a manifest or schema file read at boot.
const maxManifestBytes = 1024 * 1024

// typeNameRE is the shape of a plugin check type: the `x-` prefix, then a
// lowercase slug, so the type is also a valid agent capability name.
var typeNameRE = regexp.MustCompile(`^x-[a-z0-9]+(-[a-z0-9]+)*$`)

// Loading errors. Each one skips the plugin (or the type), never the boot.
var (
	ErrInvalidManifest = errors.New("invalid plugin manifest")
	ErrTypePrefix      = errors.New("plugin check type must match x-[a-z0-9-]+")
	ErrDuplicateType   = errors.New("plugin check type declared twice")
	ErrEntryOutside    = errors.New("plugin file is outside the plugin folder")
	ErrEntryMissing    = errors.New("plugin entry file not found")
	ErrBadSchema       = errors.New("plugin schema does not compile")
	ErrTooLarge        = errors.New("plugin file too large")
)

// Manifest is a plugin's manifest.json.
type Manifest struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Protocol string `json:"protocol"`
	// Entry is the JS script (js) or the executable (exec, nagios), relative
	// to the plugin folder. A check config can never name a command.
	Entry string `json:"entry"`
	// Args is the nagios argument template: each `{{key}}` is replaced with
	// the config value of that key; an argument that ends up empty is
	// dropped. Ignored by the other protocols.
	Args  []string   `json:"args,omitempty"`
	Types []TypeDecl `json:"types"`
	// URL is the base URL of an http plugin service (spec 2026-10-17-06). Its
	// types and version come from GET /checkers, not from the manifest.
	URL string `json:"url,omitempty"`
	// SecretEnv names the environment variable holding the http plugin's
	// signing key set (servicesig.ParseKeySet form).
	SecretEnv string `json:"secretEnv,omitempty"`
}

// TypeDecl declares one check type of a plugin.
type TypeDecl struct {
	Type          string   `json:"type"`
	Description   string   `json:"description"`
	Labels        []string `json:"labels"`
	Schema        string   `json:"schema"`
	MinPeriod     string   `json:"minPeriod,omitempty"`
	DefaultPeriod string   `json:"defaultPeriod,omitempty"`
	MaxPeriod     string   `json:"maxPeriod,omitempty"`
	// Validate asks an exec plugin to also validate a config through its
	// `validate` method, after the schema accepted it.
	Validate bool `json:"validate,omitempty"`
}

// readManifest parses and checks a plugin folder's manifest.
func readManifest(dir string) (*Manifest, error) {
	raw, err := readBounded(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}

	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}

	if manifest.Protocol == ProtocolHTTP {
		if manifest.Name == "" {
			return nil, fmt.Errorf("%w: name is required", ErrInvalidManifest)
		}

		return &manifest, nil
	}

	switch {
	case manifest.Name == "":
		return nil, fmt.Errorf("%w: name is required", ErrInvalidManifest)
	case manifest.Entry == "":
		return nil, fmt.Errorf("%w: entry is required", ErrInvalidManifest)
	case len(manifest.Types) == 0:
		return nil, fmt.Errorf("%w: types is empty", ErrInvalidManifest)
	}

	switch manifest.Protocol {
	case ProtocolJS, ProtocolExec, ProtocolNagios:
	default:
		return nil, fmt.Errorf("%w: unknown protocol %q", ErrInvalidManifest, manifest.Protocol)
	}

	return &manifest, nil
}

// resolveInside returns the absolute path of rel inside dir, refusing any path
// that escapes it (an absolute path, `..`, or a symlink pointing out).
func resolveInside(dir, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: %q", ErrEntryOutside, rel)
	}

	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolving plugin folder: %w", err)
	}

	joined := filepath.Join(root, rel)
	if !isInside(root, joined) {
		return "", fmt.Errorf("%w: %q", ErrEntryOutside, rel)
	}

	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %q", ErrEntryMissing, rel)
		}

		return "", fmt.Errorf("resolving %q: %w", rel, err)
	}

	if !isInside(root, resolved) {
		return "", fmt.Errorf("%w: %q", ErrEntryOutside, rel)
	}

	return resolved, nil
}

func isInside(root, path string) bool {
	relPath, err := filepath.Rel(root, path)

	return err == nil && relPath != ".." && !strings.HasPrefix(relPath, ".."+string(filepath.Separator))
}

func readBounded(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err //nolint:wrapcheck // the path is in the error already
	}

	if info.Size() > maxManifestBytes {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrTooLarge, filepath.Base(path), maxManifestBytes)
	}

	return os.ReadFile(path) //nolint:wrapcheck // the path is in the error already
}

// meta builds the check type meta of a declaration.
func (d *TypeDecl) meta(manifest *Manifest) (checkerdef.CheckTypeMeta, error) {
	meta := checkerdef.CheckTypeMeta{
		Type:        checkerdef.CheckType(d.Type),
		Description: d.Description,
		Labels:      pluginLabels(manifest.Protocol, d.Labels),
		Plugin:      &checkerdef.PluginRef{Name: manifest.Name, Version: manifest.Version},
	}

	periods := []struct {
		raw string
		dst *time.Duration
	}{
		{d.MinPeriod, &meta.MinPeriod},
		{d.DefaultPeriod, &meta.DefaultPeriod},
		{d.MaxPeriod, &meta.MaxPeriod},
	}

	for _, period := range periods {
		if period.raw == "" {
			continue
		}

		parsed, err := time.ParseDuration(period.raw)
		if err != nil || parsed <= 0 {
			return meta, fmt.Errorf("%w: %s: invalid period %q", ErrInvalidManifest, d.Type, period.raw)
		}

		*period.dst = parsed
	}

	return meta, nil
}

// pluginLabels normalizes a declaration's labels: exactly one of safe/unsafe,
// at least one category. exec, nagios and http plugins run arbitrary code
// outside the sandbox, so they are always unsafe whatever the manifest says.
func pluginLabels(protocol string, declared []string) []string {
	out := make([]string, 0, len(declared)+2)
	hasCategory := false
	unsafe := protocol != ProtocolJS

	for _, label := range declared {
		switch {
		case label == labelSafe:
			continue
		case label == labelUnsafe:
			unsafe = true

			continue
		case strings.HasPrefix(label, labelCatPfx):
			hasCategory = true
		}

		out = append(out, label)
	}

	safety := labelSafe
	if unsafe {
		safety = labelUnsafe
	}

	out = append([]string{safety}, out...)

	if !hasCategory {
		out = append(out, labelCatOth)
	}

	return out
}
