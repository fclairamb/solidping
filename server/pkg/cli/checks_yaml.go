package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlIndent is the indent width used when re-encoding a transcoded document
// as YAML.
const yamlIndent = 2

// Errors returned while transcoding a JSON document into an order-preserving
// yaml.Node tree — wrapped with the offending token/delimiter for context.
var (
	errUnexpectedJSONDelimiter = errors.New("unexpected json delimiter")
	errUnexpectedJSONObjectKey = errors.New("unexpected json object key token")
	errUnexpectedJSONTokenType = errors.New("unexpected json token type")
)

// jsonToYAML transcodes JSON bytes into YAML bytes, preserving the source's
// object key order. It decodes through json.Decoder.Token() directly into a
// yaml.Node tree — never through map[string]interface{}, which randomizes Go
// map iteration order — so two transcodes of the same JSON produce
// byte-identical YAML, and the ordering matches the server's wire order
// (itself deterministic: encoding/json marshals struct fields in declaration
// order).
func jsonToYAML(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	node, err := decodeJSONNode(dec)
	if err != nil {
		return nil, fmt.Errorf("transcode json to yaml: %w", err)
	}

	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{node}}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(yamlIndent)

	if encErr := enc.Encode(doc); encErr != nil {
		return nil, fmt.Errorf("encode yaml: %w", encErr)
	}
	if closeErr := enc.Close(); closeErr != nil {
		return nil, fmt.Errorf("encode yaml: %w", closeErr)
	}

	return buf.Bytes(), nil
}

// decodeJSONNode reads one complete JSON value (scalar, object, or array)
// from dec and returns it as an order-preserving yaml.Node.
func decodeJSONNode(dec *json.Decoder) (*yaml.Node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}

	return jsonTokenToNode(dec, tok)
}

func jsonTokenToNode(dec *json.Decoder, tok json.Token) (*yaml.Node, error) {
	switch token := tok.(type) {
	case json.Delim:
		return jsonDelimToNode(dec, token)
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: token}, nil
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(string(token), ".eE") {
			tag = "!!float"
		}

		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: token.String()}, nil
	case bool:
		val := "false"
		if token {
			val = "true"
		}

		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: val}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	default:
		return nil, fmt.Errorf("%w: %T", errUnexpectedJSONTokenType, tok)
	}
}

// jsonDelimToNode dispatches an object/array-opening delimiter to its
// decoder. Split out from jsonTokenToNode purely to keep that function's
// cyclomatic complexity under the linter's threshold.
func jsonDelimToNode(dec *json.Decoder, delim json.Delim) (*yaml.Node, error) {
	switch delim {
	case '{':
		return decodeJSONObject(dec)
	case '[':
		return decodeJSONArray(dec)
	default:
		return nil, fmt.Errorf("%w: %q", errUnexpectedJSONDelimiter, delim)
	}
}

// decodeJSONObject decodes a JSON object into a yaml MappingNode, keeping
// keys in the order the decoder yields them (i.e. the wire order).
func decodeJSONObject(dec *json.Decoder) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}

		keyStr, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("%w: %v", errUnexpectedJSONObjectKey, keyTok)
		}

		valNode, err := decodeJSONNode(dec)
		if err != nil {
			return nil, err
		}

		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: keyStr}, valNode)
	}

	// Consume the closing '}'.
	if _, err := dec.Token(); err != nil {
		return nil, err
	}

	return node, nil
}

// decodeJSONArray decodes a JSON array into a yaml SequenceNode.
func decodeJSONArray(dec *json.Decoder) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}

	for dec.More() {
		valNode, err := decodeJSONNode(dec)
		if err != nil {
			return nil, err
		}

		node.Content = append(node.Content, valNode)
	}

	// Consume the closing ']'.
	if _, err := dec.Token(); err != nil {
		return nil, err
	}

	return node, nil
}

// exportFormatFromFlags resolves the export format: an explicit --format
// flag wins; otherwise infer from the -o/--file extension (.yaml/.yml →
// yaml); stdout or anything else (including .json) stays json, matching the
// pre-existing default.
func exportFormatFromFlags(explicit, outFile string) string {
	if explicit != "" {
		return strings.ToLower(explicit)
	}

	lower := strings.ToLower(outFile)
	if strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
		return "yaml"
	}

	return "json"
}
