package configregistry

import (
	"errors"
	"fmt"
	"sync"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ErrTypeRegistered is returned when an external type is registered twice or
// shadows a built-in one.
var ErrTypeRegistered = errors.New("check type already registered")

// external is a config type registered at runtime: a plugin type (spec
// 2026-10-17-05), whose config and validator come from its manifest schema.
type external struct {
	newConfig func() checkerdef.Config
	validate  func(spec *checkerdef.CheckSpec) error
}

//nolint:gochecknoglobals // process-wide registry, written at boot only
var (
	externalMu    sync.RWMutex
	externalTypes = map[checkerdef.CheckType]external{}
)

// RegisterExternal adds a runtime config type. It refuses a built-in type and
// a type already registered.
func RegisterExternal(
	checkType checkerdef.CheckType,
	newConfig func() checkerdef.Config,
	validate func(spec *checkerdef.CheckSpec) error,
) error {
	if _, builtin := ParseConfig(checkType); builtin {
		return fmt.Errorf("%w: %s", ErrTypeRegistered, checkType)
	}

	externalMu.Lock()
	defer externalMu.Unlock()

	if _, dup := externalTypes[checkType]; dup {
		return fmt.Errorf("%w: %s", ErrTypeRegistered, checkType)
	}

	externalTypes[checkType] = external{newConfig: newConfig, validate: validate}

	return nil
}

// UnregisterExternal removes a runtime config type.
func UnregisterExternal(checkType checkerdef.CheckType) {
	externalMu.Lock()
	defer externalMu.Unlock()

	delete(externalTypes, checkType)
}

func lookupExternal(checkType checkerdef.CheckType) (external, bool) {
	externalMu.RLock()
	defer externalMu.RUnlock()

	ext, ok := externalTypes[checkType]

	return ext, ok
}
