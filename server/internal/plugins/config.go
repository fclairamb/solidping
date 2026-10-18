package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"

	jsv "github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// Config is the generic config of a plugin check type: the raw map, checked
// against the manifest schema. It declares the schema's
// `x-solidping-secret-fields`, so encryption at rest and export redaction work
// exactly as for a built-in type.
type Config struct {
	values       map[string]any
	secretFields []string
}

// FromMap stores a copy of the config map.
func (c *Config) FromMap(configMap map[string]any) error {
	c.values = maps.Clone(configMap)
	if c.values == nil {
		c.values = map[string]any{}
	}

	return nil
}

// GetConfig returns a copy of the config map.
func (c *Config) GetConfig() map[string]any {
	return maps.Clone(c.values)
}

// SecretFields returns the keys the schema lists in x-solidping-secret-fields.
func (c *Config) SecretFields() []string {
	return c.secretFields
}

// Values returns the config map (not a copy).
func (c *Config) Values() map[string]any {
	return c.values
}

// timeout returns the run budget: the `timeout` key the worker threads into
// every config, bounded to maxTimeout.
func (c *Config) timeout() time.Duration {
	raw, _ := c.values["timeout"].(string)

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 || parsed > maxTimeout {
		return maxTimeout
	}

	return parsed
}

// maxTimeout is the longest run a plugin gets, the js check's own ceiling.
const maxTimeout = 30 * time.Second

// schemaDoc is the subset of a plugin schema the loader reads itself.
type schemaDoc struct {
	SecretFields []string `json:"x-solidping-secret-fields"` //nolint:tagliatelle // JSON Schema extension keyword
}

// compiledSchema is a plugin type's schema, compiled once at boot.
type compiledSchema struct {
	schema       *jsv.Schema
	secretFields []string
	// published is the document served at /api/v1/checks/schema/:type.
	published []byte
}

// compileSchema compiles a plugin schema and builds the published document,
// which adds the plugin's identity and says this schema IS the validator.
func compileSchema(checkType string, raw []byte, manifest *Manifest) (*compiledSchema, error) {
	doc, err := jsv.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadSchema, err)
	}

	url := "https://solidping.io/schemas/plugins/" + checkType + ".json"

	compiler := jsv.NewCompiler()
	compiler.DefaultDraft(jsv.Draft2020)

	if err = compiler.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadSchema, err)
	}

	schema, err := compiler.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadSchema, err)
	}

	var meta schemaDoc
	if err = json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadSchema, err)
	}

	var published map[string]any
	if err = json.Unmarshal(raw, &published); err != nil {
		return nil, fmt.Errorf("%w: schema must be a JSON object: %w", ErrBadSchema, err)
	}

	published["x-solidping-plugin"] = map[string]any{"name": manifest.Name, "version": manifest.Version}
	published["x-solidping-validation"] = "This schema is the validator of the plugin check type " +
		checkType + ": a config it rejects is refused with VALIDATION_ERROR."

	out, err := json.MarshalIndent(published, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadSchema, err)
	}

	return &compiledSchema{schema: schema, secretFields: meta.SecretFields, published: out}, nil
}

// slugUnsafeRE matches what a slug may not contain.
var slugUnsafeRE = regexp.MustCompile(`[^a-z0-9]+`)

// validateSpec validates a check spec against the schema and fills in the
// slug default, like a built-in ValidateSpec.
func (s *compiledSchema) validateSpec(checkType string, spec *checkerdef.CheckSpec) error {
	if err := s.validate(spec.Config); err != nil {
		return err
	}

	if spec.Slug == "" {
		slug := checkType
		if host, ok := spec.Config["host"].(string); ok && host != "" {
			slug += "-" + strings.Trim(slugUnsafeRE.ReplaceAllString(strings.ToLower(host), "-"), "-")
		}

		spec.Slug = slug
	}

	return nil
}

// validate checks a config map against the schema. A rejection is a
// *checkerdef.ConfigError, which the API maps to a VALIDATION_ERROR 400.
func (s *compiledSchema) validate(config map[string]any) error {
	if config == nil {
		config = map[string]any{}
	}

	// Round-trip so the validator sees JSON shapes (float64, no Go types).
	raw, err := json.Marshal(config)
	if err != nil {
		return checkerdef.NewConfigError("config", "must be JSON: "+err.Error())
	}

	value, err := jsv.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return checkerdef.NewConfigError("config", "must be JSON: "+err.Error())
	}

	err = s.schema.Validate(value)
	if err == nil {
		return nil
	}

	var verr *jsv.ValidationError
	if !errors.As(err, &verr) {
		return checkerdef.NewConfigError("config", err.Error())
	}

	return configErrorFrom(verr)
}

//nolint:gochecknoglobals // stateless printer
var printer = message.NewPrinter(language.English)

// configErrorFrom turns the first leaf of a schema validation error into a
// ConfigError naming the offending key.
func configErrorFrom(verr *jsv.ValidationError) error {
	leaf := verr
	for len(leaf.Causes) > 0 {
		leaf = leaf.Causes[0]
	}

	location := append([]string(nil), leaf.InstanceLocation...)
	if required, ok := leaf.ErrorKind.(*kind.Required); ok && len(required.Missing) > 0 {
		location = append(location, required.Missing[0])
	}

	field := strings.Join(location, ".")
	if field == "" {
		field = "config"
	}

	return checkerdef.NewConfigError(field, leaf.ErrorKind.LocalizedString(printer))
}
