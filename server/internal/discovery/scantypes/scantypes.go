// Package scantypes is the discovery-type registry. A discovery type owns its
// parameter schema, validation, and the job it enqueues, mirroring the checker
// registry — so the handler/service stay type-agnostic and adding a new source
// is "register a discovery type" rather than "add an endpoint".
package scantypes

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Error codes returned by discovery-type validation. They are surfaced verbatim
// as the API error `code`.
const (
	// CodeUnknownType is returned when no discovery type is registered for the
	// requested `type`.
	CodeUnknownType = "DISCOVERY_UNKNOWN_TYPE"
	// CodeInvalidParameters is returned when a type's `parameters` fail validation.
	CodeInvalidParameters = "DISCOVERY_INVALID_PARAMETERS"
	// CodeAlreadyRunning is returned when a scan of the same resolved job type is
	// already in flight for the org.
	CodeAlreadyRunning = "DISCOVERY_ALREADY_RUNNING"
	// CodeFreeboxNotGranted is returned when a Freebox scan targets a channel that
	// has not completed the LCD-pairing flow.
	CodeFreeboxNotGranted = "FREEBOX_NOT_GRANTED"
)

// DiscoveryError is a coded validation/precondition failure from a discovery
// type. The handler maps Code to the HTTP status + error body.
type DiscoveryError struct {
	Code    string
	Message string
}

// Error implements the error interface.
func (e *DiscoveryError) Error() string {
	return e.Message
}

// NewError builds a coded DiscoveryError.
func NewError(code, message string) *DiscoveryError {
	return &DiscoveryError{Code: code, Message: message}
}

// Deps carries the dependencies a discovery type may need to validate
// parameters (e.g. the Freebox type probing a channel). Types that need nothing
// ignore the fields.
type Deps struct {
	DBService db.Service
	Creds     credentials.Service
}

// Definition is one discovery source/method.
type Definition interface {
	// Type is the user-facing discovery type id ("lan", "freebox", …).
	Type() string
	// Source is the DiscoverySource the type writes onto discovered_checks rows.
	Source() models.DiscoverySource
	// BuildJob validates parameters and returns the job to enqueue. For chunked
	// types it returns the PLAN job type (fan-out happens inside that job's Run,
	// unchanged). A validation failure returns a *DiscoveryError.
	BuildJob(ctx context.Context, deps Deps, orgUID string,
		parameters json.RawMessage) (jobType string, jobConfig json.RawMessage, err error)
}

//nolint:gochecknoglobals // process-wide registry, mirrors the checker registry
var (
	mu       sync.RWMutex
	registry = map[string]Definition{}
)

// Register adds a discovery type to the registry. It is called from each type's
// wiring (see RegisterDefaults). A duplicate Type() overwrites the prior entry.
func Register(d Definition) {
	mu.Lock()
	defer mu.Unlock()

	registry[d.Type()] = d
}

// Get returns the registered definition for a type, or false.
//
//nolint:ireturn // registry pattern returns the Definition interface
func Get(typ string) (Definition, bool) {
	mu.RLock()
	defer mu.RUnlock()

	d, ok := registry[typ]

	return d, ok
}

// List returns every registered definition, sorted by Type() for stable output
// (drives the frontend capability list and the activation test).
func List() []Definition {
	mu.RLock()
	defer mu.RUnlock()

	out := make([]Definition, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Type() < out[j].Type() })

	return out
}

// RegisterDefaults registers the built-in reference discovery types (lan,
// freebox). New sources (container, kubernetes) register their own definitions
// from their packages. Safe to call multiple times.
func RegisterDefaults() {
	Register(&LANDefinition{})
	Register(&FreeboxDefinition{})
}

//nolint:gochecknoinits // register built-in types on import, mirroring checkers
func init() {
	RegisterDefaults()
}
