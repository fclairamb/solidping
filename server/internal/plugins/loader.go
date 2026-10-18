package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkers/schemas"
)

// transport runs one check of a plugin type.
type transport interface {
	execute(ctx context.Context, checkType string, cfg *Config) (*checkerdef.Result, error)
	close()
}

// validator is implemented by a transport that can validate a config itself.
type validator interface {
	validate(ctx context.Context, checkType string, config map[string]any) error
}

// Plugin is one loaded plugin folder.
type Plugin struct {
	Manifest  Manifest
	Dir       string
	transport transport
}

// Set is what Load registered: close it to unregister every type and stop the
// plugin processes.
type Set struct {
	Plugins []*Plugin
	Types   []checkerdef.CheckType
	// Errors lists every plugin or type that was skipped, already logged.
	Errors []error

	closeOnce sync.Once
}

// Close unregisters the loaded types and stops exec plugins.
func (s *Set) Close() {
	if s == nil {
		return
	}

	s.closeOnce.Do(func() {
		for _, checkType := range s.Types {
			registry.UnregisterPlugin(checkType)
			schemas.UnregisterExternal(string(checkType))
		}

		for _, plugin := range s.Plugins {
			plugin.transport.close()
		}
	})
}

// Load reads every plugin folder under dir and registers its check types. An
// empty dir is the feature switched off. A broken plugin or type is logged
// and skipped: it never stops the boot or the other plugins.
func Load(dir string, logger *slog.Logger) *Set {
	set := &Set{}
	if dir == "" {
		return set
	}

	if logger == nil {
		logger = slog.Default()
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		set.fail(logger, fmt.Errorf("reading plugin directory %s: %w", dir, err))

		return set
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pluginDir := filepath.Join(dir, entry.Name())

		if err := set.loadPlugin(pluginDir, logger); err != nil {
			set.fail(logger, fmt.Errorf("plugin %s: %w", entry.Name(), err))
		}
	}

	if len(set.Types) > 0 {
		logger.Info("Check plugins loaded", "dir", dir, "types", set.Types)
	}

	return set
}

func (s *Set) fail(logger *slog.Logger, err error) {
	logger.Error("Skipping check plugin", "error", err)
	s.Errors = append(s.Errors, err)
}

func (s *Set) loadPlugin(dir string, logger *slog.Logger) error {
	manifest, err := readManifest(dir)
	if err != nil {
		return err
	}

	entryPath, err := resolveInside(dir, manifest.Entry)
	if err != nil {
		return err
	}

	runner, err := newTransport(manifest, dir, entryPath, logger)
	if err != nil {
		return err
	}

	plugin := &Plugin{Manifest: *manifest, Dir: dir, transport: runner}
	seen := map[string]bool{}
	registered := 0

	for i := range manifest.Types {
		decl := &manifest.Types[i]

		if seen[decl.Type] {
			s.fail(logger, fmt.Errorf("plugin %s: %w: %s", manifest.Name, ErrDuplicateType, decl.Type))

			continue
		}

		seen[decl.Type] = true

		if err := s.registerType(plugin, decl, dir); err != nil {
			s.fail(logger, fmt.Errorf("plugin %s: type %s: %w", manifest.Name, decl.Type, err))

			continue
		}

		registered++
	}

	if registered == 0 {
		runner.close()

		return nil
	}

	s.Plugins = append(s.Plugins, plugin)

	return nil
}

//nolint:ireturn // one of three transports
func newTransport(manifest *Manifest, dir, entryPath string, logger *slog.Logger) (transport, error) {
	switch manifest.Protocol {
	case ProtocolJS:
		return newJSTransport(entryPath)
	case ProtocolExec:
		return newExecTransport(entryPath, dir, baseEnv(dir), logger), nil
	case ProtocolNagios:
		return newNagiosTransport(entryPath, dir, manifest.Args, baseEnv(dir)), nil
	default:
		return nil, fmt.Errorf("%w: unknown protocol %q", ErrInvalidManifest, manifest.Protocol)
	}
}

func (s *Set) registerType(plugin *Plugin, decl *TypeDecl, dir string) error {
	if !typeNameRE.MatchString(decl.Type) {
		return ErrTypePrefix
	}

	meta, err := decl.meta(&plugin.Manifest)
	if err != nil {
		return err
	}

	if decl.Schema == "" {
		return fmt.Errorf("%w: schema is required", ErrInvalidManifest)
	}

	schemaPath, err := resolveInside(dir, decl.Schema)
	if err != nil {
		return err
	}

	raw, err := readBounded(schemaPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadSchema, err)
	}

	schema, err := compileSchema(decl.Type, raw, &plugin.Manifest)
	if err != nil {
		return err
	}

	checker := &Checker{checkType: meta.Type, plugin: plugin, schema: schema, validateWithPlugin: decl.Validate}

	err = registry.RegisterPlugin(&meta,
		func() checkerdef.Checker { return checker },
		checker.newConfig,
		checker.Validate,
	)
	if err != nil {
		if errors.Is(err, registry.ErrDuplicateType) {
			return fmt.Errorf("%w: %w", ErrDuplicateType, err)
		}

		return err
	}

	schemas.RegisterExternal(decl.Type, schema.published)
	s.Types = append(s.Types, meta.Type)

	return nil
}

// Checker runs one plugin check type. It implements checkerdef.Checker.
type Checker struct {
	checkType          checkerdef.CheckType
	plugin             *Plugin
	schema             *compiledSchema
	validateWithPlugin bool
}

// Type returns the plugin check type.
func (c *Checker) Type() checkerdef.CheckType {
	return c.checkType
}

func (c *Checker) newConfig() checkerdef.Config { //nolint:ireturn // config factory
	return &Config{secretFields: c.schema.secretFields}
}

// Validate checks a spec against the manifest schema (the validator of a
// plugin type), then, when the manifest asks for it, against the plugin's own
// validate method.
func (c *Checker) Validate(spec *checkerdef.CheckSpec) error {
	if err := c.schema.validateSpec(string(c.checkType), spec); err != nil {
		return err
	}

	if !c.validateWithPlugin {
		return nil
	}

	if pluginValidator, ok := c.plugin.transport.(validator); ok {
		return pluginValidator.validate(context.Background(), string(c.checkType), spec.Config)
	}

	return nil
}

// Execute runs the check through the plugin's transport and applies the
// result caps.
func (c *Checker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*Config](config)
	if err != nil {
		return nil, err
	}

	result, err := c.plugin.transport.execute(ctx, string(c.checkType), cfg)
	if err != nil {
		return nil, err
	}

	return LimitResult(result), nil
}
