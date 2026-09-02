package checkgrpc

import (
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort    = 50051
	defaultTimeout = 10 * time.Second
	maxTimeout     = 30 * time.Second

	// configKeyMetadata carries plain, queryable request metadata.
	configKeyMetadata = "metadata"
	// configKeySecretMetadata carries request metadata encrypted at rest.
	configKeySecretMetadata = "secretMetadata"

	// reservedMetadataPrefix is reserved by the gRPC runtime itself
	// (grpc-timeout, grpc-encoding, grpc-status, …). Sending one is either
	// ignored or actively breaks the call.
	reservedMetadataPrefix = "grpc-"
	// binaryMetadataSuffix marks a binary metadata key, whose value must be
	// base64 — which a plain string config field cannot express safely.
	binaryMetadataSuffix = "-bin"
)

// metadataKeyPattern is the subset of RFC 7230 header names gRPC accepts,
// already lowercased. gRPC normalizes keys to lowercase on the wire, so an
// uppercase key in the config is a silent rename — rejected rather than
// quietly folded.
var metadataKeyPattern = regexp.MustCompile(`^[a-z0-9._-]+$`)

// GRPCConfig holds the configuration for gRPC health checks.
type GRPCConfig struct {
	Host          string        `json:"host"`
	Port          int           `json:"port,omitempty"`
	TLS           bool          `json:"tls,omitempty"`
	TLSSkipVerify bool          `json:"tlsSkipVerify,omitempty"`
	ServiceName   string        `json:"serviceName,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`

	// Metadata is sent on the health RPC as plain, queryable request
	// metadata — the gRPC equivalent of an HTTP check's `headers`.
	Metadata map[string]string `json:"metadata,omitempty"`

	// SecretMetadata is merged on top of Metadata at execute time and is
	// encrypted at rest (see SecretFields). Values are never echoed into a
	// result's output.
	SecretMetadata map[string]string `json:"secretMetadata,omitempty"`

	// Keyword matches a substring of the serving-status ENUM string
	// (SERVING/NOT_SERVING/…), which is redundant with the serving-status
	// check itself.
	//
	// Deprecated: kept for backward compatibility with checks created before
	// the phase-breakdown work; not exposed by the dashboard and not
	// documented as a supported option. New checks should rely on the
	// serving status alone.
	Keyword string `json:"keyword,omitempty"`
	// InvertKeyword inverts Keyword.
	//
	// Deprecated: see Keyword.
	InvertKeyword bool `json:"invertKeyword,omitempty"`
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *GRPCConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap["host"].(string); ok {
		c.Host = host
	} else if configMap["host"] != nil {
		return checkerdef.NewConfigError("host", "must be a string")
	}

	if port, ok := configMap["port"].(int); ok {
		c.Port = port
	} else if portFloat, ok := configMap["port"].(float64); ok {
		c.Port = int(portFloat)
	} else if configMap["port"] != nil {
		return checkerdef.NewConfigError("port", "must be a number")
	}

	if tls, ok := configMap["tls"].(bool); ok {
		c.TLS = tls
	}

	if tlsSkipVerify, ok := configMap["tlsSkipVerify"].(bool); ok {
		c.TLSSkipVerify = tlsSkipVerify
	}

	if serviceName, ok := configMap["serviceName"].(string); ok {
		c.ServiceName = serviceName
	} else if configMap["serviceName"] != nil {
		return checkerdef.NewConfigError("serviceName", "must be a string")
	}

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	if keyword, ok := configMap["keyword"].(string); ok {
		c.Keyword = keyword
	} else if configMap["keyword"] != nil {
		return checkerdef.NewConfigError("keyword", "must be a string")
	}

	if invertKeyword, ok := configMap["invertKeyword"].(bool); ok {
		c.InvertKeyword = invertKeyword
	}

	metadata, err := stringMapConfigValue(configMap, configKeyMetadata)
	if err != nil {
		return err
	}

	c.Metadata = metadata

	secretMetadata, err := stringMapConfigValue(configMap, configKeySecretMetadata)
	if err != nil {
		return err
	}

	c.SecretMetadata = secretMetadata

	return nil
}

// stringMapConfigValue decodes a map[string]string config key, tolerating the
// `map[string]any` shape a JSONB round-trip produces (the same two-shape decode
// HTTPConfig does for `headers`/`secretHeaders`).
func stringMapConfigValue(configMap map[string]any, key string) (map[string]string, error) {
	switch typed := configMap[key].(type) {
	case nil:
		// An absent key yields an empty map rather than nil: every consumer
		// guards on len(), and it keeps this from returning a nil value with a
		// nil error.
		return map[string]string{}, nil
	case map[string]string:
		return typed, nil
	case map[string]any:
		out := make(map[string]string, len(typed))

		for name, value := range typed {
			str, ok := value.(string)
			if !ok {
				return nil, checkerdef.NewConfigErrorf(key, "%s must be a string", name)
			}

			out[name] = str
		}

		return out, nil
	default:
		return nil, checkerdef.NewConfigError(key, "must be a map of string to string")
	}
}

// GetConfig returns the configuration as a map.
func (c *GRPCConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		"host": c.Host,
	}

	if c.Port != 0 && c.Port != defaultPort {
		cfg["port"] = c.Port
	}

	if c.TLS {
		cfg["tls"] = c.TLS
	}

	if c.TLSSkipVerify {
		cfg["tlsSkipVerify"] = c.TLSSkipVerify
	}

	if c.ServiceName != "" {
		cfg["serviceName"] = c.ServiceName
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.Keyword != "" {
		cfg["keyword"] = c.Keyword
	}

	if c.InvertKeyword {
		cfg["invertKeyword"] = c.InvertKeyword
	}

	if len(c.Metadata) > 0 {
		cfg[configKeyMetadata] = c.Metadata
	}

	if len(c.SecretMetadata) > 0 {
		cfg[configKeySecretMetadata] = c.SecretMetadata
	}

	return cfg
}

// SecretFields declares which top-level config keys carry secrets and must be
// encrypted at rest. Implements credentials.SecretFielder.
//
// Only `secretMetadata` qualifies: `metadata` is deliberately public and
// queryable (it is the gRPC analog of HTTP's plain `headers`), so an operator
// can search for the checks that send a given routing header.
func (c *GRPCConfig) SecretFields() []string {
	return []string{configKeySecretMetadata}
}

// EffectiveMetadata merges the plain and secret maps into the set actually sent
// on the RPC. Secret values win a key collision — the explicit credential is
// what the operator meant, and letting the public half override it would be a
// silent downgrade. Returns nil when there is nothing to send.
func (c *GRPCConfig) EffectiveMetadata() map[string]string {
	if len(c.Metadata) == 0 && len(c.SecretMetadata) == 0 {
		return nil
	}

	merged := make(map[string]string, len(c.Metadata)+len(c.SecretMetadata))
	for key, value := range c.Metadata {
		merged[strings.ToLower(key)] = value
	}

	for key, value := range c.SecretMetadata {
		merged[strings.ToLower(key)] = value
	}

	return merged
}

// Validate checks if the configuration is valid.
func (c *GRPCConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError("host", "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf(
			"timeout", "must be > 0 and <= 30s, got %s", c.Timeout.String(),
		)
	}

	if err := validateMetadata(configKeyMetadata, c.Metadata); err != nil {
		return err
	}

	return validateMetadata(configKeySecretMetadata, c.SecretMetadata)
}

// validateMetadata enforces the gRPC key rules on one metadata map. Keys are
// walked in sorted order so a config with several bad keys always reports the
// same one — a map's iteration order would otherwise make the error message
// (and any test asserting it) nondeterministic.
func validateMetadata(configKey string, metadata map[string]string) error {
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		switch {
		case key == "":
			return checkerdef.NewConfigError(configKey, "keys must not be empty")
		case !metadataKeyPattern.MatchString(key):
			return checkerdef.NewConfigErrorf(configKey,
				"key %q is invalid: use lowercase letters, digits, '-', '.' or '_' "+
					"(gRPC lowercases metadata keys on the wire)", key)
		case strings.HasPrefix(key, reservedMetadataPrefix):
			return checkerdef.NewConfigErrorf(configKey,
				"key %q uses the reserved %q prefix, which belongs to the gRPC runtime",
				key, reservedMetadataPrefix)
		case strings.HasSuffix(key, binaryMetadataSuffix):
			return checkerdef.NewConfigErrorf(configKey,
				"key %q is binary metadata (%q suffix), which this check cannot send",
				key, binaryMetadataSuffix)
		}
	}

	return nil
}

func (c *GRPCConfig) resolvePort() int {
	if c.Port != 0 {
		return c.Port
	}

	return defaultPort
}

func (c *GRPCConfig) resolveTimeout() time.Duration {
	if c.Timeout != 0 {
		return c.Timeout
	}

	return defaultTimeout
}

func (c *GRPCConfig) resolveTarget() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.resolvePort()))
}
