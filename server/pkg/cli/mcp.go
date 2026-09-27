package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"
)

// `sp mcp` is a stdio front for a remote SolidPing MCP endpoint
// (spec 2026-09-26-04). It reads newline-delimited JSON-RPC on stdin, POSTs
// each message to <url>/api/v1/mcp with the CLI's bearer credential and the
// session id minted by `initialize`, and writes every JSON-RPC reply on its
// own line to stdout. Nothing else may ever reach stdout: an MCP client parses
// every byte there as protocol. Logs go to stderr.
//
// It adds no server surface. Auth, roles, scopes and the demo gate all stay
// server-side, where the HTTP transport already enforces them.
//
// This is the CLIENT half, reachable as `sp mcp` and `solidping client mcp`.
// The in-process server variant (`solidping mcp --stdio`, spec 2026-09-26-05)
// lives under the server binary's own command tree, so the two never clash.

const (
	// mcpAPIPath is the streamable-HTTP MCP endpoint on the server.
	mcpAPIPath = "/api/v1/mcp"
	// mcpHeaderSessionID carries the session minted by `initialize`.
	mcpHeaderSessionID = "Mcp-Session-Id"
	// mcpRequestTimeout bounds one proxied request. Generous on purpose: a
	// tool call such as diagnose_check runs a real check before answering.
	mcpRequestTimeout = 5 * time.Minute
	// mcpCloseTimeout bounds the best-effort DELETE sent on EOF.
	mcpCloseTimeout = 5 * time.Second
	// mcpRenewBackoff spaces out renewal attempts after one failed. Renewal
	// may fall back to a password login, and retrying it on every 401 with
	// bad credentials would hammer the login endpoint and its rate limit.
	mcpRenewBackoff = 30 * time.Second
	// mcpReadBufferSize is the initial stdin buffer; lines may grow past it.
	mcpReadBufferSize = 64 * 1024

	// JSON-RPC error codes the bridge answers with when the server gave no
	// JSON-RPC reply of its own. -32001 sits in the implementation-defined
	// range, next to the server's -32002 (forbidden) and -32003 (not found).
	mcpCodeParseError     = -32700
	mcpCodeInvalidRequest = -32600
	mcpCodeInternal       = -32603
	mcpCodeUnauthorized   = -32001

	mcpContentTypeSSE  = "text/event-stream"
	mcpContentTypeJSON = "application/json"
	jsonRPCVersion     = "2.0"
)

func mcpCommand() *cli.Command {
	return &cli.Command{
		Name: "mcp",
		Usage: "Serve the SolidPing MCP endpoint over stdin/stdout, for MCP clients that run a command " +
			"(Claude Desktop, Cursor, MCP directories)",
		Description: "Reads newline-delimited JSON-RPC on stdin, forwards each message to <url>/api/v1/mcp " +
			"with your credential, and writes the replies to stdout. Logs go to stderr.\n\n" +
			"The credential is the one other commands use (sp auth login, settings.json), " +
			"or --token / SP_TOKEN for a Personal Access Token.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    flagToken,
				Aliases: []string{"t"},
				Usage:   "Personal Access Token to use instead of the saved login (never written to disk)",
				Sources: cli.EnvVars("SP_TOKEN"),
			},
		},
		Action: mcpAction,
	}
}

func mcpAction(ctx context.Context, cmd *cli.Command) error {
	var level slog.LevelVar

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: &level}))

	if err := mcpServe(ctx, cmd, logger, &level); err != nil {
		return mcpFailure(ctx, logger, err)
	}

	return nil
}

// mcpFailure logs why `sp mcp` stopped, to stderr, and turns the error into an
// exit code with no message. urfave then exits by itself instead of handing
// the error back to main, and the solidping binary's main logs a returned
// error through a logger that writes to stdout, which is the protocol stream.
func mcpFailure(ctx context.Context, logger *slog.Logger, err error) error {
	logger.ErrorContext(ctx, "sp mcp stopped", "error", err)

	return cli.Exit("", 1)
}

func mcpServe(ctx context.Context, cmd *cli.Command, logger *slog.Logger, level *slog.LevelVar) error {
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	if cliCtx.Verbose {
		level.Set(slog.LevelDebug)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	bridge := &mcpBridge{
		endpoint:   strings.TrimRight(cliCtx.Config.URL, "/") + mcpAPIPath,
		httpClient: &http.Client{Timeout: mcpRequestTimeout},
		out:        os.Stdout,
		log:        logger,
	}

	if pat := cmd.String(flagToken); pat != "" {
		// A pasted PAT is used verbatim. There is nothing to refresh: the
		// server alone decides whether it is still good.
		bridge.token = pat
	} else {
		token, tokenErr := cliCtx.APIHelper.Token(ctx)
		if tokenErr != nil {
			// Still start: the anonymous handshake works without a token, and
			// every other call gets a JSON-RPC error naming the way out.
			logger.WarnContext(ctx, "No SolidPing credential, only the MCP handshake will work", "error", tokenErr)
		}

		bridge.token = token
		bridge.renew = cliCtx.APIHelper.RenewToken
	}

	logger.InfoContext(ctx, "MCP stdio bridge ready", "endpoint", bridge.endpoint)

	return bridge.run(ctx, os.Stdin)
}

// mcpBridge relays JSON-RPC between a stdio MCP client and the HTTP endpoint.
// Messages are handled one at a time, in order: replies come back in the order
// the client sent the requests, and `initialize` always lands its session id
// before the next request needs it.
type mcpBridge struct {
	endpoint   string
	httpClient *http.Client
	out        io.Writer
	log        *slog.Logger

	// token is the current bearer credential; empty sends no Authorization.
	token string
	// renew returns a new credential after a 401. Nil means none can be had
	// (a pasted PAT), so the 401 goes straight back to the client.
	renew func(ctx context.Context, rejected string) (string, error)
	// renewAfter holds renewal off until then, after a failed attempt.
	renewAfter time.Time
	// sessionID is the Mcp-Session-Id minted by the last `initialize`.
	sessionID string
}

// nullID is the id JSON-RPC mandates on an error that answers a message whose
// own id cannot be read (a parse error, an empty batch).
var nullID = json.RawMessage("null") //nolint:gochecknoglobals // Immutable JSON literal.

// rpcEnvelope is the part of an incoming message the bridge needs to answer
// on the server's behalf when the server gives no JSON-RPC reply.
type rpcEnvelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
}

// rpcError is a JSON-RPC error reply written by the bridge itself.
type rpcError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   rpcErrorBody    `json:"error"`
}

type rpcErrorBody struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// restError is the server's standard REST error shape (401/403 from the auth
// middleware answer with it rather than a JSON-RPC envelope).
type restError struct {
	Title  string `json:"title"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// run serves until stdin reaches EOF or ctx is canceled, then closes the
// server-side session. It returns an error only when stdout is gone, since
// then nothing more can be said to the client.
func (b *mcpBridge) run(ctx context.Context, input io.Reader) error {
	lines := make(chan []byte)

	go func() {
		defer close(lines)

		reader := bufio.NewReaderSize(input, mcpReadBufferSize)

		for {
			line, err := reader.ReadBytes('\n')
			if len(bytes.TrimSpace(line)) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}

			if err != nil {
				if !errors.Is(err, io.EOF) {
					b.log.WarnContext(ctx, "Reading stdin failed", "error", err)
				}

				return
			}
		}
	}()

	defer b.closeSession(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return nil
			}

			if err := b.handle(ctx, bytes.TrimSpace(line)); err != nil {
				return err
			}
		}
	}
}

// handle relays one stdin line and writes the reply it produces, if any.
func (b *mcpBridge) handle(ctx context.Context, msg []byte) error {
	if !json.Valid(msg) {
		// JSON-RPC 2.0 §5.1: no id can be read from an unparseable message,
		// so the spec mandates a null id on the parse error.
		return b.writeLine(ctx, b.errorMessage(ctx, nullID, mcpCodeParseError, "Parse error", nil))
	}

	if msg[0] == '[' {
		return b.handleBatch(ctx, msg)
	}

	for _, reply := range b.relay(ctx, msg) {
		if err := b.writeLine(ctx, reply); err != nil {
			return err
		}
	}

	return nil
}

// handleBatch relays a JSON-RPC batch one member at a time and answers with
// one array holding the replies to its requests, or nothing when it held only
// notifications. The server takes a single message per POST, so forwarding
// the array as is would only earn an id-less parse error.
func (b *mcpBridge) handleBatch(ctx context.Context, msg []byte) error {
	var members []json.RawMessage
	if err := json.Unmarshal(msg, &members); err != nil || len(members) == 0 {
		// §6: an empty batch gets a single Invalid Request with a null id.
		return b.writeLine(ctx, b.errorMessage(ctx, nullID, mcpCodeInvalidRequest, "Invalid Request", nil))
	}

	replies := make([]json.RawMessage, 0, len(members))
	for _, member := range members {
		replies = append(replies, b.relay(ctx, bytes.TrimSpace(member))...)
	}

	if len(replies) == 0 {
		return nil
	}

	payload, err := json.Marshal(replies)
	if err != nil {
		return fmt.Errorf("encoding batch reply: %w", err)
	}

	return b.writeLine(ctx, payload)
}

// relay forwards one JSON-RPC message and returns what goes back to the
// client: nothing for a notification, the response for a request (always
// carrying the request's id), after any server-initiated messages an event
// stream carried first.
func (b *mcpBridge) relay(ctx context.Context, msg []byte) []json.RawMessage {
	var env rpcEnvelope
	if len(msg) == 0 || msg[0] != '{' || json.Unmarshal(msg, &env) != nil {
		// §6: a batch member that is not a request object has no id to
		// answer with, so the spec mandates a null one.
		return []json.RawMessage{b.errorMessage(ctx, nullID, mcpCodeInvalidRequest, "Invalid Request", nil)}
	}

	resp, err := b.post(ctx, msg)
	if err != nil {
		b.log.ErrorContext(ctx, "MCP request failed", "method", env.Method, "error", err)

		return b.errorReply(ctx, env, mcpCodeInternal, "SolidPing MCP endpoint unreachable: "+err.Error(), nil)
	}
	defer func() { _ = resp.Body.Close() }()

	if sessionID := resp.Header.Get(mcpHeaderSessionID); sessionID != "" {
		b.sessionID = sessionID
	}

	b.log.DebugContext(ctx, "MCP request relayed", "method", env.Method, "status", resp.StatusCode)

	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == mcpContentTypeSSE {
		return b.relaySSE(ctx, resp.Body, env)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return b.errorReply(ctx, env, mcpCodeInternal, "Reading the SolidPing reply failed: "+err.Error(), nil)
	}

	if len(bytes.TrimSpace(body)) == 0 {
		// 202 for a notification, 204 for a response: nothing to relay.
		if resp.StatusCode >= http.StatusBadRequest {
			return b.httpErrorReply(ctx, env, resp.StatusCode, nil)
		}

		return nil
	}

	if isJSONRPC(body) {
		return b.answer(ctx, env, body)
	}

	return b.httpErrorReply(ctx, env, resp.StatusCode, body)
}

// answer vets one message from the server before it reaches the client.
//
// A server-initiated message (it has a method) passes as is. A response is
// dropped when the client sent a notification, which must never be replied
// to, and gets the request's id when the server left it out: the server
// answers some refusals (a token without the mcp scope, an unparseable body)
// with an id-less error that the client could not match to its request.
func (b *mcpBridge) answer(ctx context.Context, env rpcEnvelope, reply []byte) []json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(reply, &fields); err != nil {
		b.log.ErrorContext(ctx, "SolidPing sent a reply that is not a JSON object", "method", env.Method, "error", err)

		return b.errorReply(ctx, env, mcpCodeInternal, "SolidPing sent a malformed reply", nil)
	}

	if _, serverMessage := fields["method"]; serverMessage {
		return []json.RawMessage{reply}
	}

	if !env.isRequest() {
		b.log.WarnContext(ctx, "Dropping the server's reply to a notification",
			"method", env.Method, "error", string(fields["error"]))

		return nil
	}

	if id, ok := fields["id"]; ok && !isNullID(id) {
		return []json.RawMessage{reply}
	}

	fields["id"] = env.ID

	stamped, err := json.Marshal(fields)
	if err != nil {
		return b.errorReply(ctx, env, mcpCodeInternal, "SolidPing sent a malformed reply", nil)
	}

	return []json.RawMessage{stamped}
}

// post sends one message, renewing the credential and retrying once on a 401.
func (b *mcpBridge) post(ctx context.Context, msg []byte) (*http.Response, error) {
	resp, err := b.send(ctx, http.MethodPost, msg)
	if err != nil || resp.StatusCode != http.StatusUnauthorized || b.renew == nil || time.Now().Before(b.renewAfter) {
		return resp, err
	}

	token, renewErr := b.renew(ctx, b.token)
	if renewErr != nil || token == "" {
		b.renewAfter = time.Now().Add(mcpRenewBackoff)
		b.log.WarnContext(ctx, "SolidPing rejected the credential and it could not be renewed", "error", renewErr)

		return resp, nil
	}

	_ = resp.Body.Close()
	b.token = token
	b.log.InfoContext(ctx, "Renewed the SolidPing credential after a 401")

	return b.send(ctx, http.MethodPost, msg)
}

func (b *mcpBridge) send(ctx context.Context, method string, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, b.endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", mcpContentTypeJSON)
	}

	req.Header.Set("Accept", mcpContentTypeJSON+", "+mcpContentTypeSSE)

	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}

	if b.sessionID != "" {
		req.Header.Set(mcpHeaderSessionID, b.sessionID)
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, b.endpoint, err)
	}

	return resp, nil
}

// relaySSE vets the data of every event in a text/event-stream reply as one
// message. Event names, ids and comments carry nothing the client needs.
func (b *mcpBridge) relaySSE(ctx context.Context, body io.Reader, env rpcEnvelope) []json.RawMessage {
	reader := bufio.NewReaderSize(body, mcpReadBufferSize)

	var (
		data    []string
		replies []json.RawMessage
	)

	flush := func() {
		if len(data) == 0 {
			return
		}

		payload := []byte(strings.Join(data, "\n"))
		data = data[:0]
		replies = append(replies, b.answer(ctx, env, payload)...)
	}

	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}

		if err == nil {
			continue
		}

		flush()

		if !errors.Is(err, io.EOF) {
			b.log.ErrorContext(ctx, "Reading the SolidPing event stream failed", "error", err)

			if len(replies) == 0 {
				return b.errorReply(ctx, env, mcpCodeInternal, "Reading the SolidPing event stream failed: "+err.Error(), nil)
			}
		}

		return replies
	}
}

// httpErrorReply answers a request whose HTTP reply carried no JSON-RPC
// envelope (auth middleware errors, proxies, a wrong URL).
func (b *mcpBridge) httpErrorReply(ctx context.Context, env rpcEnvelope, status int, body []byte) []json.RawMessage {
	var rest restError
	if body != nil {
		_ = json.Unmarshal(body, &rest)
	}

	message := fmt.Sprintf("SolidPing answered HTTP %d", status)
	if rest.Title != "" {
		message += ": " + rest.Title
	}

	code := mcpCodeInternal
	if status == http.StatusUnauthorized {
		code = mcpCodeUnauthorized
		message += " (run `sp auth login`, or set SP_TOKEN to a Personal Access Token)"
	}

	data := map[string]any{"httpStatus": status}
	if rest.Code != "" {
		data["code"] = rest.Code
	}

	if rest.Detail != "" {
		data["detail"] = rest.Detail
	}

	b.log.WarnContext(ctx, "SolidPing refused the MCP request", "method", env.Method, "status", status, "code", rest.Code)

	return b.errorReply(ctx, env, code, message, data)
}

// errorReply builds the error for a request. A notification gets no reply by
// definition, so its failure is only logged.
func (b *mcpBridge) errorReply(
	ctx context.Context, env rpcEnvelope, code int, message string, data map[string]any,
) []json.RawMessage {
	if !env.isRequest() {
		b.log.WarnContext(ctx, "Dropping an error for a notification", "method", env.Method, "error", message)

		return nil
	}

	return []json.RawMessage{b.errorMessage(ctx, env.ID, code, message, data)}
}

// errorMessage encodes a JSON-RPC error reply written by the bridge itself.
func (b *mcpBridge) errorMessage(
	ctx context.Context, id json.RawMessage, code int, message string, data map[string]any,
) json.RawMessage {
	reply := rpcError{JSONRPC: jsonRPCVersion, ID: id, Error: rpcErrorBody{Code: code, Message: message, Data: data}}

	payload, err := json.Marshal(reply)
	if err != nil {
		// Unreachable with the ids and data the bridge passes; answer with a
		// bare internal error rather than nothing.
		b.log.ErrorContext(ctx, "Encoding a JSON-RPC error failed", "error", err)

		return fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":"Internal error"}}`,
			id, mcpCodeInternal)
	}

	return payload
}

// writeLine writes one JSON message as a single stdout line. The JSON is
// compacted first: a pretty-printed reply would span several lines and break
// newline-delimited framing.
func (b *mcpBridge) writeLine(ctx context.Context, payload []byte) error {
	var buf bytes.Buffer
	if err := json.Compact(&buf, payload); err != nil {
		b.log.ErrorContext(ctx, "Dropping a reply that is not JSON", "error", err)

		return nil
	}

	buf.WriteByte('\n')

	if _, err := b.out.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("writing to stdout: %w", err)
	}

	return nil
}

// closeSession ends the server-side session with DELETE, best effort. It runs
// on EOF and on a signal, so it must not inherit a canceled context.
func (b *mcpBridge) closeSession(ctx context.Context) {
	if b.sessionID == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mcpCloseTimeout)
	defer cancel()

	resp, err := b.send(ctx, http.MethodDelete, nil)
	if err != nil {
		b.log.WarnContext(ctx, "Closing the MCP session failed", "error", err)

		return
	}

	_ = resp.Body.Close()
	b.log.DebugContext(ctx, "MCP session closed", "status", resp.StatusCode)
	b.sessionID = ""
}

// isJSONRPC reports whether body is one JSON-RPC message rather than some
// other JSON document such as the REST error shape. Batches never come back:
// the bridge splits them before they reach the server.
func isJSONRPC(body []byte) bool {
	var probe struct {
		JSONRPC string `json:"jsonrpc"`
	}

	trimmed := bytes.TrimSpace(body)

	return len(trimmed) > 0 && trimmed[0] == '{' &&
		json.Unmarshal(trimmed, &probe) == nil && probe.JSONRPC == jsonRPCVersion
}

// isRequest reports whether the message expects a reply. A missing or null id
// makes it a notification.
func (e rpcEnvelope) isRequest() bool {
	return len(e.ID) > 0 && !isNullID(e.ID)
}

func isNullID(id json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(id), []byte("null"))
}
