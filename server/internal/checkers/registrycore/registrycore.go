// Package registrycore holds the process-wide table of check types (checker
// and config factories) and the runtime plugin registration. It links only the
// light configregistry, so a binary that merely reads plugin descriptors (the
// `sp` CLI) does not pull in every checker implementation. Package registry
// fills it with the built-ins.
package registrycore

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
)

// entry is one registered check type: how to build its checker and config.
type entry struct {
	newChecker func() checkerdef.Checker
	newConfig  func() checkerdef.Config
}

//nolint:gochecknoglobals // process-wide registry, written at init and at boot
var (
	entriesMu sync.RWMutex
	entries   = map[checkerdef.CheckType]entry{}
)

// ErrDuplicateType is returned when a check type is registered twice.
var ErrDuplicateType = errors.New("check type already registered")

// Register adds a check type: its meta, a checker factory and a config
// factory. A built-in registers through here at init; registering a type
// twice is a programming error and panics.
func Register(
	meta *checkerdef.CheckTypeMeta,
	newChecker func() checkerdef.Checker,
	newConfig func() checkerdef.Config,
) {
	if err := register(meta.Type, newChecker, newConfig); err != nil {
		panic("registry: " + err.Error())
	}
}

// RegisterPlugin adds a check type loaded at runtime from a plugin manifest
// (spec 2026-10-17-05). Unlike Register it returns an error, so a bad plugin
// is skipped rather than crashing the boot. It registers the meta (always
// ExcludedFromTally), the config factory and validator in configregistry, and
// the checker factory here.
func RegisterPlugin(
	meta *checkerdef.CheckTypeMeta,
	newChecker func() checkerdef.Checker,
	newConfig func() checkerdef.Config,
	validate func(spec *checkerdef.CheckSpec) error,
) error {
	if err := register(meta.Type, newChecker, newConfig); err != nil {
		return err
	}

	if err := configregistry.RegisterExternal(meta.Type, newConfig, validate); err != nil {
		unregister(meta.Type)

		return fmt.Errorf("registering %s config: %w", meta.Type, err)
	}

	if err := checkerdef.RegisterCheckTypeMeta(meta); err != nil {
		configregistry.UnregisterExternal(meta.Type)
		unregister(meta.Type)

		return fmt.Errorf("registering %s meta: %w", meta.Type, err)
	}

	return nil
}

// UnregisterPlugin removes a type added by RegisterPlugin. Built-ins are never
// unregistered.
func UnregisterPlugin(checkType checkerdef.CheckType) {
	if !checkType.IsPlugin() {
		return
	}

	checkerdef.UnregisterCheckTypeMeta(checkType)
	configregistry.UnregisterExternal(checkType)
	unregister(checkType)
}

// PluginTypes returns the plugin check types this process loaded, sorted. A
// worker advertises them at claim time (spec 2026-10-17-05 §17).
func PluginTypes() []string {
	entriesMu.RLock()
	defer entriesMu.RUnlock()

	out := make([]string, 0)

	for checkType := range entries {
		if checkType.IsPlugin() {
			out = append(out, string(checkType))
		}
	}

	sort.Strings(out)

	return out
}

func register(
	checkType checkerdef.CheckType,
	newChecker func() checkerdef.Checker,
	newConfig func() checkerdef.Config,
) error {
	entriesMu.Lock()
	defer entriesMu.Unlock()

	if _, dup := entries[checkType]; dup {
		return fmt.Errorf("%w: %s", ErrDuplicateType, checkType)
	}

	entries[checkType] = entry{newChecker: newChecker, newConfig: newConfig}

	return nil
}

func unregister(checkType checkerdef.CheckType) {
	entriesMu.Lock()
	defer entriesMu.Unlock()

	delete(entries, checkType)
}

func lookup(checkType checkerdef.CheckType) (entry, bool) {
	entriesMu.RLock()
	defer entriesMu.RUnlock()

	found, ok := entries[checkType]

	return found, ok
}

// GetChecker retrieves a checker by type.
// Returns the checker and true if found, nil and false otherwise.
//
//nolint:ireturn // Registry pattern requires interface return
func GetChecker(checkType checkerdef.CheckType) (checkerdef.Checker, bool) {
	found, ok := lookup(checkType)
	if !ok {
		return nil, false
	}

	return found.newChecker(), true
}

// ParseConfig creates the appropriate config struct for a given check type.
// Returns the config interface and true if the type is known, nil and false otherwise.
//
// Built-in configs come from configregistry, the light twin that knows only
// the configs, so the offline validator in `sp` and the server can never
// disagree about what a check type accepts. TestRegistriesAgree pins that.
//
//nolint:ireturn // Registry pattern requires interface return
func ParseConfig(checkType checkerdef.CheckType) (checkerdef.Config, bool) {
	found, ok := lookup(checkType)
	if !ok {
		return nil, false
	}

	return found.newConfig(), true
}
