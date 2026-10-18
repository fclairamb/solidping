// Package finger is the reference HTTP check plugin (spec 2026-10-17-06): the
// `x-finger` check type, a Finger (RFC 1288) probe served over the plugin
// protocol's three endpoints. Standard library only, so it doubles as an
// example a plugin author can copy. server/cmd/plugin-finger serves it.
package finger

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// CheckType is the check type this plugin serves.
const CheckType = "x-finger"

// Version is the plugin version reported by GET /checkers.
const Version = "1.0.0"

// Result statuses of the plugin protocol.
const (
	statusUp      = "up"
	statusDown    = "down"
	statusTimeout = "timeout"
)

const (
	defaultPort = 79
	// maxResponse caps what is read from the Finger server.
	maxResponse = 64 * 1024
	// maxRequestBody caps a request body from SolidPing.
	maxRequestBody = 1024 * 1024
	// defaultBudget is the run budget when a request carries no deadline.
	defaultBudget = 10 * time.Second
)

//go:embed schema.json
var schema []byte

// Descriptor is the x-finger type as GET /checkers lists it.
func Descriptor() protocol.TypeDescriptor {
	return protocol.TypeDescriptor{
		Type:          CheckType,
		Description:   "Check a Finger server (RFC 1288)",
		Labels:        []string{"category:network"},
		Schema:        schema,
		MinPeriod:     "1m",
		DefaultPeriod: "5m",
		Validate:      true,
	}
}

// NewHandler serves GET /checkers, POST /validate and POST /execute. Every
// request must carry a valid SolidPing signature for one of keys; any other
// request gets a 401.
func NewHandler(keys servicesig.KeySet, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET "+protocol.PathCheckers, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, protocol.DescribeResponse{
			ProtocolVersion: protocol.ProtocolVersion,
			Version:         Version,
			Data:            []protocol.TypeDescriptor{Descriptor()},
		})
	})

	mux.HandleFunc("POST "+protocol.PathValidate, func(writer http.ResponseWriter, httpReq *http.Request) {
		var req protocol.ValidateRequest
		if !decode(writer, httpReq, &req) {
			return
		}

		writeJSON(writer, protocol.ValidateResponse{Errors: Validate(req.Config)})
	})

	mux.HandleFunc("POST "+protocol.PathExecute, func(writer http.ResponseWriter, httpReq *http.Request) {
		var req protocol.ExecuteRequest
		if !decode(writer, httpReq, &req) {
			return
		}

		if req.Type != CheckType {
			http.Error(writer, "unknown check type", http.StatusBadRequest)

			return
		}

		deadline := req.Deadline
		if deadline.IsZero() {
			deadline = time.Now().Add(defaultBudget)
		}

		ctx, cancel := context.WithDeadline(httpReq.Context(), deadline)
		defer cancel()

		res := Run(ctx, req.Config)
		writeJSON(writer, res)
	})

	return verify(keys, logger, mux)
}

// verify rejects any request whose signature does not check out. The body is
// read here (bounded), verified, then handed on.
func verify(keys servicesig.KeySet, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(io.LimitReader(req.Body, maxRequestBody+1))
		if err != nil || len(body) > maxRequestBody {
			http.Error(writer, "request body too large", http.StatusRequestEntityTooLarge)

			return
		}

		if _, err = servicesig.VerifyRequest(req, keys, body, time.Now()); err != nil {
			// The reason is logged, never answered.
			logger.WarnContext(req.Context(), "Rejected an unsigned or badly signed request",
				"path", req.URL.Path, "error", err)
			http.Error(writer, "unauthorized", http.StatusUnauthorized)

			return
		}

		req.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(writer, req)
	})
}

func decode(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)

		return false
	}

	return true
}

func writeJSON(writer http.ResponseWriter, value any) {
	raw, err := json.Marshal(value)
	if err != nil {
		http.Error(writer, "encoding the answer failed", http.StatusInternalServerError)

		return
	}

	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(raw)
}

// Validate checks what the schema cannot say: a user name is one line, so it
// cannot smuggle a second query.
func Validate(config map[string]any) []protocol.ValidationError {
	user, _ := config["user"].(string)
	if strings.ContainsAny(user, "\r\n") {
		return []protocol.ValidationError{{Field: "user", Message: "must be a single line"}}
	}

	return nil
}

// Run queries the Finger server described by config, within ctx's deadline.
func Run(ctx context.Context, config map[string]any) protocol.ExecuteResponse {
	host, _ := config["host"].(string)
	user, _ := config["user"].(string)
	contains, _ := config["contains"].(string)

	port := defaultPort
	if raw, ok := config["port"].(float64); ok && raw >= 1 && raw <= 65535 {
		port = int(raw)
	}

	address := net.JoinHostPort(host, strconv.Itoa(port))
	output := map[string]any{"host": host, "port": port}

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return failure(ctx, err, output, "connection failed")
	}

	defer func() { _ = conn.Close() }()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if _, err = conn.Write([]byte(user + "\r\n")); err != nil {
		return failure(ctx, err, output, "sending the query failed")
	}

	// RFC 1288: the server closes the connection when it is done.
	response, err := io.ReadAll(io.LimitReader(conn, maxResponse))
	if err != nil {
		return failure(ctx, err, output, "reading the answer failed")
	}

	text := string(response)
	lines := strings.Count(text, "\n")

	if text != "" && !strings.HasSuffix(text, "\n") {
		lines++
	}

	firstLine, _, _ := strings.Cut(text, "\n")
	output["message"] = strings.TrimSpace(firstLine)

	res := protocol.ExecuteResponse{
		Status:  statusUp,
		Output:  output,
		Metrics: map[string]any{"bytes": len(response), "lines": lines},
	}

	switch {
	case len(response) == 0:
		res.Status = statusDown
		output["message"] = "the server sent an empty answer"
	case contains != "" && !strings.Contains(text, contains):
		res.Status = statusDown
		output["error"] = fmt.Sprintf("the answer does not contain %q", contains)
	}

	return res
}

// failure maps a network error: the deadline is a timeout, anything else
// (refused, reset, unknown host) is the target being down.
func failure(ctx context.Context, err error, output map[string]any, what string) protocol.ExecuteResponse {
	var netErr net.Error

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) ||
		ctx.Err() != nil || (errors.As(err, &netErr) && netErr.Timeout()) {
		output["message"] = "no answer before the deadline"

		return protocol.ExecuteResponse{Status: statusTimeout, Output: output}
	}

	output["message"] = what
	output["error"] = err.Error()

	return protocol.ExecuteResponse{Status: statusDown, Output: output}
}
