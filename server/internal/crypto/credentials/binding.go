package credentials

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Target binding of sealed secrets (spec 2026-11-11-01).
//
// A client that seals a check's secrets puts a reserved `_binding` object in
// the sealed plaintext, next to the secrets: the check type and a digest of the
// check's target fields (the config keys that decide where the secrets go).
// The agent recomputes the digest from the job's public config before it uses
// the secrets, so whoever can write the stored config cannot point a sealed
// check at a host they control: the binding sits inside the ciphertext, which
// only the agents can open and nobody can edit.
//
// The digest is SHA-256 (hex) over a canonical JSON of the target fields, built
// the same way by dash0 (web/dash0/src/lib/sealing.ts, targetDigest) and pinned
// by the shared fixture web/dash0/src/lib/__fixtures__/target-binding.json:
//
//   - only the target fields carrying a value (not absent, null or "");
//   - keys sorted; strings escaped as JSON with `"`, `\` and the control
//     characters below 0x20 (as \u00xx) only, everything else raw UTF-8;
//   - numbers and booleans as strings ("5432", "true"), so 5432 and "5432"
//     bind the same;
//   - a field named `url` (or ending in `Url`/`_url`) is a URL: scheme and
//     host lowercased (ASCII), the scheme's default port made explicit;
//   - a field named `host`, `hostname` or `brokers` is lowercased (ASCII);
//   - an empty object (`env: {}`) carries no value, like an absent field;
//   - the wildcard `*` (AllPublicKeys) stands for every key of the config
//     but the NonTargetKeys (spec 2026-11-12-02).

// BindingKey is the reserved plaintext key holding the binding.
const BindingKey = "_binding"

// Keys of the binding object.
const (
	bindingKeyType   = "type"
	bindingKeyDigest = "digest"
)

// TunnelConfigKey is the tunnel reference every type binds (it mirrors
// checkerdef.TunnelCheckUIDConfigKey: a tunneled check sends its secrets
// through the bastion it names).
const TunnelConfigKey = "tunnelCheckUid"

// Binding errors. ErrSealedForAnotherTarget is the message the agent reports.
var (
	ErrSealedForAnotherTarget = errors.New("sealed secrets were sealed for another target: type them again")
	ErrSealedKeyNotSecret     = errors.New("sealed secrets carry a key that is not a secret field of the check type")
	ErrInvalidBinding         = errors.New("sealed secrets carry an invalid _binding")
)

// AllPublicKeys is the wildcard target field: every public key of the config
// but NonTargetKeys. It is the target list of a type that declares none
// (plugins, a type unknown to the agent): their destination is decided by
// code from arbitrary config keys, so every key binds (spec 2026-11-12-02).
const AllPublicKeys = "*"

// NonTargetKeys are the config keys the wildcard leaves out: they can never
// steer a destination. `timeout` is the run budget the worker threads into
// every config. Anything else binds: an over-broad binding only forces the
// secrets to be typed again.
var NonTargetKeys = []string{"timeout"} //nolint:gochecknoglobals // read-only list

// DefaultTargetFields is the target list of a type that declares none: the
// wildcard.
var DefaultTargetFields = []string{AllPublicKeys} //nolint:gochecknoglobals // read-only default list

// legacyDefaultTargetFields is what DefaultTargetFields was before spec
// 2026-11-12-02: blobs bound by then were digested over it.
var legacyDefaultTargetFields = []string{ //nolint:gochecknoglobals // read-only list
	"url", "host", "hostname", "port", "database", "dsn", "address", "endpoint", "server", "brokers",
}

// TargetFielder is an *optional* interface a checker config implements to
// declare its target fields: the config keys that decide where its secrets go
// (spec 2026-11-11-01). Declared next to SecretFields.
type TargetFielder interface {
	TargetFields() []string
}

// LegacyTargetFielder is an *optional* interface of a config whose target
// list grew after blobs were bound to it: it returns the old list, so the
// Re-seal page can tell which blobs the agents now refuse.
type LegacyTargetFielder interface {
	LegacyTargetFields() []string
}

// TargetFieldsFor returns the sorted target fields of a config: its declared
// ones (DefaultTargetFields when it declares none), plus the tunnel reference.
func TargetFieldsFor(cfg any) []string {
	fields := DefaultTargetFields

	if tf, ok := cfg.(TargetFielder); ok && cfg != nil {
		fields = tf.TargetFields()
	}

	return withTunnel(fields)
}

// LegacyTargetFieldsFor returns the target fields a blob bound before spec
// 2026-11-12-02 was digested over (the client did not record its list then).
func LegacyTargetFieldsFor(cfg any) []string {
	fields := legacyDefaultTargetFields

	if ltf, ok := cfg.(LegacyTargetFielder); ok && cfg != nil {
		fields = ltf.LegacyTargetFields()
	} else if tf, ok := cfg.(TargetFielder); ok && cfg != nil {
		fields = tf.TargetFields()
	}

	return withTunnel(fields)
}

func withTunnel(fields []string) []string {
	out := append([]string{TunnelConfigKey}, fields...)
	sort.Strings(out)

	return slices.Compact(out)
}

// expandTargetFields replaces the wildcard with the keys of config, minus
// NonTargetKeys; the result is sorted and without duplicates.
func expandTargetFields(targetFields []string, config map[string]any) []string {
	fields := make([]string, 0, len(targetFields)+len(config))

	for _, field := range targetFields {
		if field != AllPublicKeys {
			fields = append(fields, field)

			continue
		}

		for key := range config {
			if !slices.Contains(NonTargetKeys, key) {
				fields = append(fields, key)
			}
		}
	}

	sort.Strings(fields)

	return slices.Compact(fields)
}

// TargetDigest returns the hex SHA-256 of the canonical JSON of the target
// fields of config.
func TargetDigest(targetFields []string, config map[string]any) string {
	sum := sha256.Sum256([]byte(CanonicalTarget(targetFields, config)))

	return hex.EncodeToString(sum[:])
}

// CanonicalTarget returns the canonical JSON the target digest is computed
// over. Exposed for the shared fixture test.
func CanonicalTarget(targetFields []string, config map[string]any) string {
	fields := expandTargetFields(targetFields, config)

	var builder strings.Builder

	builder.WriteByte('{')

	first := true

	for _, field := range fields {
		value, ok := canonicalValue(field, config[field])
		if !ok || value == "{}" {
			continue
		}

		if !first {
			builder.WriteByte(',')
		}

		first = false

		writeCanonicalString(&builder, field)
		builder.WriteByte(':')
		builder.WriteString(value)
	}

	builder.WriteByte('}')

	return builder.String()
}

// TargetChanged reports whether two public configs differ on a target field.
func TargetChanged(targetFields []string, before, after map[string]any) bool {
	return CanonicalTarget(targetFields, before) != CanonicalTarget(targetFields, after)
}

// NewBinding returns the `_binding` object for a check of checkType with this
// public config.
func NewBinding(checkType string, targetFields []string, config map[string]any) map[string]any {
	return map[string]any{
		bindingKeyType:   checkType,
		bindingKeyDigest: TargetDigest(targetFields, config),
	}
}

// WithBinding returns a copy of secrets carrying the binding.
func WithBinding(
	secrets map[string]any, checkType string, targetFields []string, config map[string]any,
) map[string]any {
	out := make(map[string]any, len(secrets)+1)
	for key, value := range secrets {
		out[key] = value
	}

	out[BindingKey] = NewBinding(checkType, targetFields, config)

	return out
}

// BindingCheck is what VerifyBinding needs about the job the secrets are for.
type BindingCheck struct {
	// Type is the job's check type.
	Type string
	// Config is the job's public config.
	Config map[string]any
	// TargetFields are the type's target fields (TargetFieldsFor).
	TargetFields []string
	// SecretFields are the type's secret fields; nil when the type is unknown
	// to this process, in which case only keys absent from Config are allowed.
	SecretFields []string
}

// VerifyBinding checks the plaintext of a sealed blob against the job it is
// used for and returns the secrets without the binding. bound reports whether
// the plaintext carried a binding: an unbound blob (sealed before spec
// 2026-11-11-01, or by the server) is returned as is, and the caller decides.
//
// A bound blob must name the job's type and the digest of its target fields
// (ErrSealedForAnotherTarget otherwise), and may only carry secret fields of
// the type (ErrSealedKeyNotSecret), so it can never override a public field.
func VerifyBinding(plain map[string]any, check BindingCheck) (map[string]any, bool, error) {
	raw, bound := plain[BindingKey]
	if !bound {
		return plain, false, nil
	}

	binding, ok := raw.(map[string]any)
	if !ok {
		return nil, true, ErrInvalidBinding
	}

	gotType, _ := binding[bindingKeyType].(string)
	gotDigest, _ := binding[bindingKeyDigest].(string)

	if gotType == "" || gotDigest == "" {
		return nil, true, ErrInvalidBinding
	}

	if gotType != check.Type || gotDigest != TargetDigest(check.TargetFields, check.Config) {
		return nil, true, ErrSealedForAnotherTarget
	}

	secrets := make(map[string]any, len(plain)-1)

	for key, value := range plain {
		if key == BindingKey {
			continue
		}

		if err := checkSecretKey(key, check); err != nil {
			return nil, true, err
		}

		secrets[key] = value
	}

	return secrets, true, nil
}

// OpenSealedPlaintext applies the rules of a sealed plaintext about to be
// used for a job (spec 2026-11-11-01, shared with 2026-11-11-02) and returns
// the secrets to merge over its public config:
//
//   - a blob with a `_binding` must have been sealed for this type and these
//     target fields, and carry only secret fields of the type;
//   - a browser blob without one (sealed before the binding existed) still may
//     not carry a key that is not a secret field; legacyUnbound reports it, so
//     the caller can warn;
//   - an agent blob (a server blob re-sealed by a peer agent, spec
//     2026-11-12-03) without one may not carry a key that is not a secret
//     field either: the server never saw that plaintext;
//   - a server blob without one is used as is: the server sealed it from
//     plaintext it held, public `${param:}` overlays included. That is the
//     per-claim blob of a system agent, never stored; a stored server blob
//     is sealed from the private half of the config and only holds secret
//     fields. The strict rule keys on the origin rather than on the agent
//     kind so this system-agent path keeps working.
//
// Every caller that merges a sealed plaintext over a public config (the
// claim, the tunnel, the re-wrap and its confirmation) goes through it. The
// returns are the secrets, legacyUnbound, and the refusal.
func OpenSealedPlaintext(envelope string, plain map[string]any, check BindingCheck) (map[string]any, bool, error) {
	secrets, bound, err := VerifyBinding(plain, check)
	if err != nil || bound {
		return secrets, false, err
	}

	origin := SealedOrigin(envelope)
	if origin != SealOriginBrowser && origin != SealOriginAgent {
		return plain, false, nil
	}

	if strictErr := StrictSecretKeys(plain, check); strictErr != nil {
		return nil, false, strictErr
	}

	// Only a browser blob can be bound: an agent blob without a binding is
	// not legacy, it never had one.
	return plain, origin == SealOriginBrowser, nil
}

// StrictSecretKeys refuses a plaintext key that is not a secret field of the
// type. Used on browser and agent blobs without a binding, and by the re-wrap
// on every blob whatever its origin.
func StrictSecretKeys(plain map[string]any, check BindingCheck) error {
	for key := range plain {
		if err := checkSecretKey(key, check); err != nil {
			return err
		}
	}

	return nil
}

func checkSecretKey(key string, check BindingCheck) error {
	if check.SecretFields == nil {
		if _, public := check.Config[key]; public || slices.Contains(check.TargetFields, key) {
			return fmt.Errorf("%w: %s", ErrSealedKeyNotSecret, key)
		}

		return nil
	}

	if !slices.Contains(check.SecretFields, key) {
		return fmt.Errorf("%w: %s", ErrSealedKeyNotSecret, key)
	}

	return nil
}

// canonicalValue returns the canonical JSON of one target field value, false
// when the field carries no value.
func canonicalValue(field string, value any) (string, bool) {
	switch {
	case isURLField(field):
		if str, ok := value.(string); ok && str != "" {
			value = NormalizeTargetURL(str)
		}
	case isHostField(field):
		value = lowerStrings(value)
	}

	return canonicalJSON(value)
}

func isURLField(field string) bool {
	return field == "url" || strings.HasSuffix(field, "Url") || strings.HasSuffix(field, "_url")
}

func isHostField(field string) bool {
	return field == "host" || field == "hostname" || field == "brokers"
}

func lowerStrings(value any) any {
	switch typed := value.(type) {
	case string:
		return asciiLower(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = lowerStrings(item)
		}

		return out
	case []string:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = asciiLower(item)
		}

		return out
	default:
		return value
	}
}

func canonicalJSON(value any) (string, bool) {
	var builder strings.Builder

	if !writeCanonical(&builder, value) {
		return "", false
	}

	return builder.String(), true
}

// scalarString returns the canonical string of a scalar value, false when
// value is not one.
func scalarString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case bool:
		return strconv.FormatBool(typed), true
	case float64:
		return formatNumber(typed), true
	case float32:
		return formatNumber(float64(typed)), true
	case int:
		return strconv.Itoa(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case int32:
		return strconv.FormatInt(int64(typed), 10), true
	case json.Number:
		return typed.String(), true
	default:
		return "", false
	}
}

func writeCanonical(builder *strings.Builder, value any) bool {
	if value == nil || value == "" {
		return false
	}

	if str, ok := scalarString(value); ok {
		writeCanonicalString(builder, str)

		return true
	}

	switch typed := value.(type) {
	case []string:
		items := make([]any, len(typed))
		for i, item := range typed {
			items[i] = item
		}

		writeCanonicalArray(builder, items)
	case []any:
		writeCanonicalArray(builder, typed)
	case map[string]any:
		writeCanonicalObject(builder, typed)
	default:
		// Anything else: through encoding/json, then the canonical form of
		// what it decodes to.
		raw, err := json.Marshal(typed)
		if err != nil {
			return false
		}

		var decoded any
		if json.Unmarshal(raw, &decoded) != nil {
			return false
		}

		return writeCanonical(builder, decoded)
	}

	return true
}

func writeCanonicalArray(builder *strings.Builder, items []any) {
	builder.WriteByte('[')

	for i, item := range items {
		if i > 0 {
			builder.WriteByte(',')
		}

		if !writeCanonical(builder, item) {
			builder.WriteString("null")
		}
	}

	builder.WriteByte(']')
}

func writeCanonicalObject(builder *strings.Builder, object map[string]any) {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}

	sort.Strings(keys)
	builder.WriteByte('{')

	first := true

	for _, key := range keys {
		var item strings.Builder
		if !writeCanonical(&item, object[key]) {
			continue
		}

		if !first {
			builder.WriteByte(',')
		}

		first = false

		writeCanonicalString(builder, key)
		builder.WriteByte(':')
		builder.WriteString(item.String())
	}

	builder.WriteByte('}')
}

// formatNumber writes an integer-valued number without a fraction, as
// JavaScript's String(n) does.
func formatNumber(value float64) string {
	if value == math.Trunc(value) && math.Abs(value) < 1e21 {
		return strconv.FormatFloat(value, 'f', 0, 64)
	}

	return strconv.FormatFloat(value, 'g', -1, 64)
}

const hexDigits = "0123456789abcdef"

func writeCanonicalString(builder *strings.Builder, value string) {
	builder.WriteByte('"')

	for i := range len(value) {
		char := value[i]

		switch {
		case char == '"' || char == '\\':
			builder.WriteByte('\\')
			builder.WriteByte(char)
		case char < 0x20:
			builder.WriteString(`\u00`)
			builder.WriteByte(hexDigits[char>>4])
			builder.WriteByte(hexDigits[char&0x0f])
		default:
			builder.WriteByte(char)
		}
	}

	builder.WriteByte('"')
}

// asciiLower lowercases A-Z only, so Go and JavaScript agree byte for byte.
func asciiLower(value string) string {
	out := []byte(value)
	for i, char := range out {
		if char >= 'A' && char <= 'Z' {
			out[i] = char + ('a' - 'A')
		}
	}

	return string(out)
}

// defaultURLPorts are made explicit by NormalizeTargetURL.
var defaultURLPorts = map[string]string{ //nolint:gochecknoglobals // read-only table
	"http": "80", "https": "443", "ws": "80", "wss": "443", "ftp": "21",
}

// NormalizeTargetURL lowercases a URL's scheme and host (ASCII) and makes the
// scheme's default port explicit. Anything after the authority is kept as is.
// A string that is not `scheme://…` is returned unchanged.
func NormalizeTargetURL(raw string) string {
	sep := strings.Index(raw, "://")
	if sep <= 0 || !isURLScheme(raw[:sep]) {
		return raw
	}

	scheme := asciiLower(raw[:sep])
	rest := raw[sep+3:]

	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}

	authority, tail := rest[:end], rest[end:]

	userinfo := ""
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		userinfo, authority = authority[:at+1], authority[at+1:]
	}

	host, port := authority, ""

	if strings.HasPrefix(authority, "[") {
		if closing := strings.Index(authority, "]"); closing >= 0 {
			host = authority[:closing+1]
			if after := authority[closing+1:]; strings.HasPrefix(after, ":") {
				port = after[1:]
			}
		}
	} else if colon := strings.LastIndex(authority, ":"); colon >= 0 {
		host, port = authority[:colon], authority[colon+1:]
	}

	if port == "" {
		port = defaultURLPorts[scheme]
	}

	out := scheme + "://" + userinfo + asciiLower(host)
	if port != "" {
		out += ":" + port
	}

	return out + tail
}

func isURLScheme(scheme string) bool {
	for i := range len(scheme) {
		char := scheme[i]
		isLetter := (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')

		if i == 0 && !isLetter {
			return false
		}

		if !isLetter && (char < '0' || char > '9') && char != '+' && char != '-' && char != '.' {
			return false
		}
	}

	return true
}
