package mcp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// maxHandshakeProbeBytes caps how much of the request body the anonymous gate
// will read before deciding. A real `initialize` envelope is a few hundred
// bytes; anything past this cap is not a handshake, so the gate stops reading
// and hands the request to the authentication middleware (which reads nothing
// and answers 401). That keeps an oversized body from being a way to make the
// gate do unbounded work, and keeps it from being a way past the gate.
const maxHandshakeProbeBytes = 64 * 1024

// isAnonymousMethod reports whether a JSON-RPC method may be served without a
// token. The two handshake methods are the exhaustive list.
//
// `initialize` answers with the protocol version, three static capability
// flags and ServerInfo{"solidping", …} — public facts, identical for every
// caller, computed from neither the org nor the token. `notifications/
// initialized` carries no payload and returns 202 with no body. Every other
// method (tools/list, resources/list, every tools/call) stays behind
// RequireMCPAuth: the tool surface is a product fingerprint and the calls
// touch org data.
func isAnonymousMethod(method string) bool {
	// Exact match, deliberately not a prefix or a case-folded compare: a
	// method named `initialize_and_dump` must not be mistaken for the
	// handshake.
	return method == methodInitialize || method == methodInitialized
}

// AllowAnonymousHandshake wraps the MCP authentication middleware so that an
// unauthenticated MCP handshake is served instead of being challenged.
//
// MCP directories (Glama and the like) probe a server by sending `initialize`
// with no credentials; answering 401 makes the listing fail. Everything the
// handshake returns is public, so the probe is let through — but only the
// handshake, only when the caller presents no credentials at all, and only
// when the body is a single JSON-RPC object whose method is exactly one of the
// handshake methods.
//
// A caller that DOES present a credential always goes through requireAuth,
// even for `initialize`: a stale or malformed token must still produce the 401
// + `WWW-Authenticate` challenge that starts the OAuth discovery flow, rather
// than silently degrading to an anonymous answer.
func AllowAnonymousHandshake(requireAuth httpx.Middleware) httpx.Middleware {
	return func(next httpx.HandlerFunc) httpx.HandlerFunc {
		authed := requireAuth(next)

		return func(writer http.ResponseWriter, req *http.Request) error {
			if isAnonymousHandshakeRequest(req) {
				return next(writer, req)
			}

			return authed(writer, req)
		}
	}
}

// isAnonymousHandshakeRequest reports whether req is a credential-free POST
// whose body is a single JSON-RPC handshake call. It always leaves req.Body
// readable from byte zero, whatever it answers.
func isAnonymousHandshakeRequest(req *http.Request) bool {
	if req.Method != http.MethodPost || req.Body == nil {
		return false
	}

	if middleware.PresentsCredentials(req) {
		return false
	}

	body, complete := peekBody(req)
	if !complete {
		return false
	}

	return bodyIsHandshake(body)
}

// peekBody reads up to maxHandshakeProbeBytes of req.Body and puts everything
// it consumed back, so the downstream handler decodes the full original body.
// The second return is false when the body could not be read or is larger than
// the cap.
func peekBody(req *http.Request) ([]byte, bool) {
	original := req.Body

	peeked, err := io.ReadAll(io.LimitReader(original, maxHandshakeProbeBytes+1))

	// Restore before anything else can return: the downstream handler must see
	// the same bytes it would have seen without this gate. A consumed
	// io.ReadCloser here would turn every authenticated MCP call into a parse
	// error.
	req.Body = struct {
		io.Reader
		io.Closer
	}{Reader: io.MultiReader(bytes.NewReader(peeked), original), Closer: original}

	if err != nil || len(peeked) > maxHandshakeProbeBytes {
		return nil, false
	}

	return peeked, true
}

// bodyIsHandshake reports whether body is exactly one JSON-RPC object naming a
// handshake method.
//
// Decoding into a struct rejects a JSON-RPC batch (`[{…},{…}]`) outright, so a
// batch that smuggles `tools/list` behind `initialize` never reaches the
// anonymous path — it goes to requireAuth and gets 401. The trailing-content
// check rejects two concatenated objects for the same reason. The method
// itself is matched exactly.
func bodyIsHandshake(body []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))

	var probe struct {
		Method string `json:"method"`
	}

	if err := decoder.Decode(&probe); err != nil {
		return false
	}

	if decoder.More() {
		return false
	}

	return isAnonymousMethod(probe.Method)
}
