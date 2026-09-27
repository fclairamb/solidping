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
	mcpCodeParseError   = -32700
	mcpCodeInternal     = -32603
	mcpCodeUnauthorized = -32001

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
	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	if cliCtx.Verbose {
		level = slog.LevelDebug
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

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

// handle relays one message and writes whatever reply it produces.
func (b *mcpBridge) handle(ctx context.Context, msg []byte) error {
	if !json.Valid(msg) {
		return b.writeError(ctx, nil, mcpCodeParseError, "Parse error", nil)
	}

	var env rpcEnvelope
	// A batch (JSON array) has no single id; it is forwarded as is and any
	// synthesized error carries a null id.
	_ = json.Unmarshal(msg, &env)

	resp, err := b.post(ctx, msg)
	if err != nil {
		b.log.ErrorContext(ctx, "MCP request failed", "method", env.Method, "error", err)

		return b.replyError(ctx, env, mcpCodeInternal, "SolidPing MCP endpoint unreachable: "+err.Error(), nil)
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
		return b.replyError(ctx, env, mcpCodeInternal, "Reading the SolidPing reply failed: "+err.Error(), nil)
	}

	if len(bytes.TrimSpace(body)) == 0 {
		// 202 for a notification, 204 for a response: nothing to relay.
		if resp.StatusCode >= http.StatusBadRequest {
			return b.replyHTTPError(ctx, env, resp.StatusCode, nil)
		}

		return nil
	}

	if isJSONRPC(body) {
		return b.writeLine(ctx, body)
	}

	return b.replyHTTPError(ctx, env, resp.StatusCode, body)
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

// relaySSE writes the data of every event in a text/event-stream reply as one
// stdout line. Event names, ids and comments carry nothing the client needs.
func (b *mcpBridge) relaySSE(ctx context.Context, body io.Reader, env rpcEnvelope) error {
	reader := bufio.NewReaderSize(body, mcpReadBufferSize)

	var data []string

	relayed := false

	flush := func() error {
		if len(data) == 0 {
			return nil
		}

		payload := []byte(strings.Join(data, "\n"))
		data = data[:0]
		relayed = true

		return b.writeLine(ctx, payload)
	}

	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")

		switch {
		case line == "":
			if flushErr := flush(); flushErr != nil {
				return flushErr
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}

		if err != nil {
			if flushErr := flush(); flushErr != nil {
				return flushErr
			}

			if !errors.Is(err, io.EOF) {
				b.log.ErrorContext(ctx, "Reading the SolidPing event stream failed", "error", err)

				if !relayed {
					return b.replyError(ctx, env, mcpCodeInternal, "Reading the SolidPing event stream failed: "+err.Error(), nil)
				}
			}

			return nil
		}
	}
}

// replyHTTPError answers a request whose HTTP reply carried no JSON-RPC
// envelope (auth middleware errors, proxies, a wrong URL).
func (b *mcpBridge) replyHTTPError(ctx context.Context, env rpcEnvelope, status int, body []byte) error {
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

	return b.replyError(ctx, env, code, message, data)
}

// replyError writes an error for a request. A notification (no id) gets no
// reply by definition, so the failure is only logged.
func (b *mcpBridge) replyError(
	ctx context.Context, env rpcEnvelope, code int, message string, data map[string]any,
) error {
	if len(env.ID) == 0 || string(env.ID) == "null" {
		b.log.WarnContext(ctx, "Dropping an error for a notification", "method", env.Method, "error", message)

		return nil
	}

	return b.writeError(ctx, env.ID, code, message, data)
}

func (b *mcpBridge) writeError(
	ctx context.Context, id json.RawMessage, code int, message string, data map[string]any,
) error {
	if id == nil {
		id = json.RawMessage("null")
	}

	payload, err := json.Marshal(rpcError{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Error:   rpcErrorBody{Code: code, Message: message, Data: data},
	})
	if err != nil {
		return fmt.Errorf("encoding JSON-RPC error: %w", err)
	}

	return b.writeLine(ctx, payload)
}

// writeLine writes one JSON message as a single stdout line. The JSON is
// compacted first: a pretty-printed reply would span several lines and break
// newline-delimited framing.
func (b *mcpBridge) writeLine(ctx context.Context, payload []byte) error {
	var buf bytes.Buffer
	if err := json.Compact(&buf, payload); err != nil {
		b.log.ErrorContext(ctx, "SolidPing sent a reply that is not JSON, dropping it", "error", err)

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

// isJSONRPC reports whether body is a JSON-RPC reply (or a batch of them)
// rather than some other JSON document such as the REST error shape.
func isJSONRPC(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return false
	}

	if trimmed[0] == '[' {
		return json.Valid(trimmed)
	}

	var probe struct {
		JSONRPC string `json:"jsonrpc"`
	}

	return json.Unmarshal(trimmed, &probe) == nil && probe.JSONRPC == jsonRPCVersion
}
