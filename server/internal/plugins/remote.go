package plugins

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"unicode/utf8"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
)

// Bounds on what an agent may report (spec 2026-10-17-05 §18). The server
// stores these rows, so an agent cannot make it keep megabytes per org.
const (
	// MaxReportedTypes is the most plugin types one report may carry.
	MaxReportedTypes = 32
	// maxRemoteSchemaBytes bounds the schema of one reported type.
	maxRemoteSchemaBytes = 64 * 1024
	// maxRemoteTextLen bounds the free-text fields of a reported type.
	maxRemoteTextLen = 500
	// maxRemoteLabels bounds the labels of a reported type.
	maxRemoteLabels = 16
)

//nolint:gochecknoglobals // process-wide, written by Load and Set.Close only
var (
	loadedMu sync.RWMutex
	loaded   = map[checkerdef.CheckType]protocol.TypeManifest{}
	// loadedGen changes whenever loaded does, so an agent reports again
	// when an http plugin's types change (spec 2026-10-17-06).
	loadedGen uint64
)

// LoadedGeneration changes whenever the set of loaded type manifests does.
func LoadedGeneration() uint64 {
	loadedMu.RLock()
	defer loadedMu.RUnlock()

	return loadedGen
}

func recordLoaded(manifest *Manifest, decl *TypeDecl, schema []byte) {
	loadedMu.Lock()
	defer loadedMu.Unlock()

	loadedGen++

	loaded[checkerdef.CheckType(decl.Type)] = protocol.TypeManifest{
		Type:          decl.Type,
		Plugin:        manifest.Name,
		Version:       manifest.Version,
		Protocol:      manifest.Protocol,
		Description:   decl.Description,
		Labels:        decl.Labels,
		MinPeriod:     decl.MinPeriod,
		DefaultPeriod: decl.DefaultPeriod,
		MaxPeriod:     decl.MaxPeriod,
		Schema:        json.RawMessage(schema),
	}
}

func forgetLoaded(checkType checkerdef.CheckType) {
	loadedMu.Lock()
	defer loadedMu.Unlock()

	if _, ok := loaded[checkType]; ok {
		loadedGen++
	}

	delete(loaded, checkType)
}

// LoadedTypeManifests returns the plugin types this process loaded, as an
// agent reports them to the server (spec 2026-10-17-05 §18), sorted by type.
// Nil when no plugin is loaded.
func LoadedTypeManifests() []protocol.TypeManifest {
	loadedMu.RLock()
	defer loadedMu.RUnlock()

	if len(loaded) == 0 {
		return nil
	}

	out := make([]protocol.TypeManifest, 0, len(loaded))
	for _, checkType := range slices.Sorted(maps.Keys(loaded)) {
		out = append(out, loaded[checkType])
	}

	return out
}

// RemoteType is a plugin type an agent reported, compiled on the server: its
// meta, its schema (the validator) and its config factory. The server never
// runs it; only the agents that loaded the plugin claim its jobs.
type RemoteType struct {
	meta   checkerdef.CheckTypeMeta
	schema *compiledSchema
}

// CompileRemote checks and compiles a reported plugin type. It applies the
// same rules as the loader (x- prefix, known protocol, valid periods, schema
// that compiles) plus size bounds, since the input comes over the network.
func CompileRemote(reported *protocol.TypeManifest) (*RemoteType, error) {
	if !typeNameRE.MatchString(reported.Type) {
		return nil, ErrTypePrefix
	}

	switch reported.Protocol {
	case ProtocolJS, ProtocolExec, ProtocolNagios, ProtocolHTTP:
	default:
		return nil, fmt.Errorf("%w: unknown protocol %q", ErrInvalidManifest, reported.Protocol)
	}

	if len(reported.Schema) == 0 {
		return nil, fmt.Errorf("%w: schema is required", ErrInvalidManifest)
	}

	if len(reported.Schema) > maxRemoteSchemaBytes {
		return nil, fmt.Errorf("%w: schema is larger than %d bytes", ErrTooLarge, maxRemoteSchemaBytes)
	}

	if len(reported.Labels) > maxRemoteLabels {
		return nil, fmt.Errorf("%w: more than %d labels", ErrInvalidManifest, maxRemoteLabels)
	}

	texts := append([]string{reported.Plugin, reported.Version, reported.Description}, reported.Labels...)
	for _, text := range texts {
		if len(text) > maxRemoteTextLen || !utf8.ValidString(text) {
			return nil, fmt.Errorf("%w: a text field is longer than %d bytes or not UTF-8", ErrInvalidManifest, maxRemoteTextLen)
		}
	}

	if reported.Plugin == "" || reported.Version == "" {
		return nil, fmt.Errorf("%w: plugin name and version are required", ErrInvalidManifest)
	}

	manifest := &Manifest{Name: reported.Plugin, Version: reported.Version, Protocol: reported.Protocol}
	decl := &TypeDecl{
		Type:          reported.Type,
		Description:   reported.Description,
		Labels:        reported.Labels,
		MinPeriod:     reported.MinPeriod,
		DefaultPeriod: reported.DefaultPeriod,
		MaxPeriod:     reported.MaxPeriod,
	}

	meta, err := decl.meta(manifest)
	if err != nil {
		return nil, err
	}

	// Never counted in the advertised type tally, like every plugin type.
	meta.ExcludedFromTally = true

	schema, err := compileSchema(reported.Type, reported.Schema, manifest)
	if err != nil {
		return nil, err
	}

	return &RemoteType{meta: meta, schema: schema}, nil
}

// Meta returns the check type meta (labels normalized: exec and nagios are
// always unsafe).
func (t *RemoteType) Meta() checkerdef.CheckTypeMeta {
	return t.meta
}

// NewConfig returns an empty config of the type, declaring its secret fields.
func (t *RemoteType) NewConfig() checkerdef.Config { //nolint:ireturn // config factory
	return &Config{secretFields: t.schema.secretFields}
}

// ValidateSpec validates a spec against the reported schema.
func (t *RemoteType) ValidateSpec(spec *checkerdef.CheckSpec) error {
	return t.schema.validateSpec(string(t.meta.Type), spec)
}

// SecretFields returns the schema's x-solidping-secret-fields.
func (t *RemoteType) SecretFields() []string {
	return t.schema.secretFields
}

// Schema returns the published schema document.
func (t *RemoteType) Schema() []byte {
	return t.schema.published
}
