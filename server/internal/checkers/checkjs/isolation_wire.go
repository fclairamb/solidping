package checkjs

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
)

// The channel between a check process and the child running its script
// (spec 2026-11-02-06): length-prefixed JSON frames, the child's stdin one
// way and its stdout the other.

const (
	// frameHeaderBytes is the big-endian length in front of every frame.
	frameHeaderBytes = 4
	// maxCallArgsBytes caps the arguments of one binding call. The largest
	// legitimate one is an http request body or a browser script, both far
	// below (a response body is 1 MiB at most, and JSON escaping can grow
	// one several times).
	maxCallArgsBytes = 8 << 20
	// maxResultBytes caps the result frame: the output is stored with the
	// check's result.
	maxResultBytes = 4 << 20
	// maxChildFrameBytes caps any frame the child sends: the larger of the
	// two above, plus room for the envelope.
	maxChildFrameBytes = maxCallArgsBytes + 64<<10
	// maxChildFrameValues caps the JSON values the parent builds from what
	// the child sends (a call's arguments, the result): compact JSON
	// amplifies, `[]` is 2 bytes of text and hundreds of bytes as an object.
	maxChildFrameValues = 1 << 17
	// maxParentFrameBytes caps a frame the parent sends (a binding's result).
	maxParentFrameBytes = 64 << 20
)

// Node kinds of a value crossing the channel.
const (
	nodeValue     = "v" // plain JSON data, no function inside
	nodeUndefined = "u"
	nodeFunction  = "f" // a function the parent serves, by handle id
	nodeObject    = "o" // an object holding functions somewhere
	nodeArray     = "a" // an array holding functions somewhere
)

// errFrameTooLarge is a frame over its cap.
var errFrameTooLarge = errors.New("message too large")

// errTooManyValues is a frame holding more JSON values than the parent
// builds.
var errTooManyValues = errors.New("message with too many values")

// wireMsg is one frame. Exactly one field is set.
type wireMsg struct {
	// Parent to child.
	Init *wireInit `json:"init,omitempty"`
	Ret  *wireNode `json:"ret,omitempty"`
	Err  *wireErr  `json:"err,omitempty"`

	// Child to parent.
	Call *wireCall   `json:"call,omitempty"`
	Done *wireResult `json:"done,omitempty"`
}

// wireInit starts the child: the script, its config, and the host globals
// it reaches over the channel.
type wireInit struct {
	Script       string            `json:"script"`
	Env          map[string]string `json:"env,omitempty"`
	Secrets      map[string]string `json:"secrets,omitempty"`
	PluginConfig map[string]any    `json:"pluginConfig,omitempty"`
	// Plugin is set when PluginConfig is non-nil, empty or not: a JS
	// plugin's script, which a plain js check is not.
	Plugin      bool                 `json:"plugin,omitempty"`
	TimeoutNs   int64                `json:"timeoutNs"`
	DeadlineNs  int64                `json:"deadlineNs"`
	MemoryLimit int64                `json:"memoryLimit"`
	Globals     map[string]*wireNode `json:"globals"`
}

// wireNode is a value crossing the channel.
type wireNode struct {
	T      string          `json:"t"`
	V      json.RawMessage `json:"v,omitempty"`
	ID     int             `json:"id,omitempty"`
	Keys   []string        `json:"k,omitempty"`
	Values []*wireNode     `json:"e,omitempty"`
}

// wireCall is the child calling a function the parent serves.
type wireCall struct {
	ID int `json:"id"`
	// Args is a JSON array, as the child's JSON.stringify wrote each one.
	Args string `json:"args"`
	// Undef lists the arguments that were undefined.
	Undef []int `json:"undef,omitempty"`
}

// wireErr is what a served call threw: an Error by name and message, or any
// other value.
type wireErr struct {
	Name    string    `json:"name,omitempty"`
	Message string    `json:"message,omitempty"`
	Value   *wireNode `json:"value,omitempty"`
}

// wireResult is the script's result, as the child computed it.
type wireResult struct {
	Status     string         `json:"status"`
	DurationNs int64          `json:"durationNs"`
	Output     map[string]any `json:"output,omitempty"`
	Metrics    map[string]any `json:"metrics,omitempty"`
}

// writeFrame writes msg as one frame.
func writeFrame(dst io.Writer, msg *wireMsg) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encoding a message: %w", err)
	}

	if uint64(len(body)) > math.MaxUint32 {
		return errFrameTooLarge
	}
	if frameHeaderBytes > math.MaxInt || len(body) > math.MaxInt-frameHeaderBytes {
		return errFrameTooLarge
	}

	frame := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	frame = append(frame, body...)

	if _, err := dst.Write(frame); err != nil {
		return fmt.Errorf("writing a message: %w", err)
	}

	return nil
}

// readFrame reads one frame of at most limit bytes.
func readFrame(src *bufio.Reader, limit int) (*wireMsg, error) {
	body, err := readFrameBody(src, limit)
	if err != nil {
		return nil, err
	}

	msg := &wireMsg{}
	if err := decodeUsingNumber(body, msg); err != nil {
		return nil, fmt.Errorf("decoding a message: %w", err)
	}

	return msg, nil
}

// readFrameBody reads the body of one frame of at most limit bytes.
func readFrameBody(src *bufio.Reader, limit int) ([]byte, error) {
	var header [frameHeaderBytes]byte
	if _, err := io.ReadFull(src, header[:]); err != nil {
		return nil, err //nolint:wrapcheck // io.EOF is how the caller sees the peer leave.
	}

	size := binary.BigEndian.Uint32(header[:])
	if uint64(size) > uint64(limit) {
		return nil, fmt.Errorf("%w: %d bytes, the limit is %d", errFrameTooLarge, size, limit)
	}

	body := make([]byte, size)
	if _, err := io.ReadFull(src, body); err != nil {
		return nil, fmt.Errorf("reading a message: %w", err)
	}

	return body, nil
}

// childFrame is a frame the child sent, with its result left undecoded
// until it is checked.
type childFrame struct {
	Call *wireCall       `json:"call,omitempty"`
	Done json.RawMessage `json:"done,omitempty"`
}

// readChildFrame reads a frame the child sent, the parent's side of the
// channel (spec 2026-11-03-01): before building anything from it, the
// result and a call's arguments are checked against their byte cap and
// their count of JSON values, so the objects the parent builds stay bounded
// whatever text the child wrote.
func readChildFrame(src *bufio.Reader) (*wireMsg, error) {
	body, err := readFrameBody(src, maxChildFrameBytes)
	if err != nil {
		return nil, err
	}

	// The envelope only: a call's arguments are one JSON string in it, and
	// the result stays raw.
	frame := &childFrame{}
	if err := json.Unmarshal(body, frame); err != nil {
		return nil, fmt.Errorf("decoding a message: %w", err)
	}

	switch {
	case frame.Call != nil:
		if err := checkChildJSON("a binding call's arguments", []byte(frame.Call.Args), maxCallArgsBytes); err != nil {
			return nil, err
		}

		return &wireMsg{Call: frame.Call}, nil
	case len(frame.Done) > 0 && !bytes.Equal(frame.Done, []byte("null")):
		if err := checkChildJSON("the result", frame.Done, maxResultBytes); err != nil {
			return nil, err
		}

		done := &wireResult{}
		if err := decodeUsingNumber(frame.Done, done); err != nil {
			return nil, fmt.Errorf("decoding the result: %w", err)
		}

		return &wireMsg{Done: done}, nil
	default:
		return &wireMsg{}, nil
	}
}

// checkChildJSON refuses JSON text over maxBytes, or holding more than
// maxChildFrameValues values.
func checkChildJSON(what string, text []byte, maxBytes int) error {
	if len(text) > maxBytes {
		return fmt.Errorf("%w: %s: %d bytes, over the %d MiB limit",
			errFrameTooLarge, what, len(text), maxBytes/bytesPerMiB)
	}

	if countJSONValues(text, maxChildFrameValues) > maxChildFrameValues {
		return fmt.Errorf("%w: %s: more than the limit of %d JSON values",
			errTooManyValues, what, maxChildFrameValues)
	}

	return nil
}

// countJSONValues counts the structural tokens of JSON text (`[`, `{`, `,`,
// `:`) outside its strings, in one scan that allocates nothing: a bound on
// the values decoding it builds. It stops once the count passes limit.
func countJSONValues(text []byte, limit int) int {
	count := 0
	inString := false
	escaped := false

	for _, char := range text {
		if inString {
			switch {
			case escaped:
				escaped = false
			case char == '\\':
				escaped = true
			case char == '"':
				inString = false
			}

			continue
		}

		switch char {
		case '"':
			inString = true
		case '[', '{', ',', ':':
			count++
			if count > limit {
				return count
			}
		}
	}

	return count
}

// decodeUsingNumber decodes data into out, numbers as json.Number.
func decodeUsingNumber(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	return dec.Decode(out) //nolint:wrapcheck // Wrapped by the callers.
}

// decodeJSONValue decodes raw the way goja exports a value: an integral
// number is an int64, any other a float64.
func decodeJSONValue(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding a value: %w", err)
	}

	return normalizeNumbers(out), nil
}

// normalizeNumbers turns every json.Number in val, at any depth, into an
// int64 when it is integral and a float64 otherwise.
func normalizeNumbers(val any) any {
	switch typed := val.(type) {
	case json.Number:
		if n, err := strconv.ParseInt(typed.String(), 10, 64); err == nil {
			return n
		}

		f, _ := typed.Float64()

		return f
	case map[string]any:
		for key, inner := range typed {
			typed[key] = normalizeNumbers(inner)
		}
	case []any:
		for i, inner := range typed {
			typed[i] = normalizeNumbers(inner)
		}
	}

	return val
}
