package tools

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
)

const (
	// RunsPerDay is the per-client-IP daily allowance. The per-minute one is
	// the route's rate limiter.
	RunsPerDay = 100

	// EventToolRun is the analytics event of one run: {type, status, cached}
	// only, never the target nor the IP.
	EventToolRun = "tool_run"

	maxBodyBytes      = 4096
	busyRetryAfterSec = 5
	ipHashBytes       = 8
	saltBytes         = 16
)

// Handler serves POST /api/v1/tools/run.
type Handler struct {
	base.HandlerBase
	cfg      *config.Config
	svc      *Service
	clientIP func(*http.Request) string
	daily    *dailyCounter
	salt     []byte
}

// NewHandler builds the handler. clientIP extracts the caller's IP the way
// the route's rate limiter does (trusted proxy hops included).
func NewHandler(cfg *config.Config, svc *Service, clientIP func(*http.Request) string) *Handler {
	salt := make([]byte, saltBytes)
	_, _ = rand.Read(salt)

	return &Handler{
		HandlerBase: base.NewHandlerBase(cfg),
		cfg:         cfg,
		svc:         svc,
		clientIP:    clientIP,
		daily:       &dailyCounter{counts: make(map[string]int)},
		salt:        salt,
	}
}

// Run executes one tool run. It answers 404 while tools.enabled is off.
func (h *Handler) Run(writer http.ResponseWriter, req *http.Request) error {
	if !h.cfg.Tools.Enabled {
		return h.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "Not found")
	}

	ipHash := h.hashIP(h.clientIP(req))

	count, allowed := h.daily.add(ipHash, time.Now().UTC())
	if !allowed {
		writer.Header().Set("Retry-After", strconv.Itoa(secondsUntilUTCMidnight(time.Now().UTC())))

		return h.WriteError(writer, http.StatusTooManyRequests, base.ErrorCodeRateLimited,
			"Daily limit reached, try again tomorrow")
	}

	var body Request

	raw, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "Invalid JSON body")
	}

	result, cached, err := h.svc.Run(req.Context(), &body)

	typ := strings.ToLower(strings.TrimSpace(body.Type))
	status := statusLabel(result, err)

	slog.InfoContext(req.Context(), "Tool run",
		"type", typ, "status", status, "cached", cached, "ip_hash", ipHash, "ip_runs_today", count)
	analytics.Capture(req.Context(), analytics.Event{
		Name:       EventToolRun,
		Properties: map[string]any{"type": typ, "status": status, "cached": cached},
	})

	if err != nil {
		return h.writeRunError(writer, req, err)
	}

	out := *result
	out.Cached = cached

	return h.WriteJSON(writer, http.StatusOK, &out)
}

func (h *Handler) writeRunError(writer http.ResponseWriter, req *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrInvalidTarget):
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeInvalidTarget,
			"The target is not valid for this tool")
	case errors.Is(err, ErrTargetNotAllowed):
		return h.WriteError(writer, http.StatusForbidden, base.ErrorCodeTargetNotAllowed,
			"This target is not allowed: it is private, reserved or a SolidPing host")
	case errors.Is(err, ErrResolveFailed):
		return h.WriteError(writer, http.StatusUnprocessableEntity, base.ErrorCodeResolveFailed,
			"The target name could not be resolved")
	case errors.Is(err, ErrTimeout):
		return h.WriteError(writer, http.StatusGatewayTimeout, base.ErrorCodeTimeout,
			"The target did not answer in time")
	case errors.Is(err, ErrBusy):
		writer.Header().Set("Retry-After", strconv.Itoa(busyRetryAfterSec))

		return h.WriteError(writer, http.StatusServiceUnavailable, base.ErrorCodeServiceBusy,
			"Too many checks in progress, retry in a few seconds")
	default:
		return h.WriteInternalError(writer, req, err)
	}
}

// statusLabel is the run's status for logs and analytics: the result status,
// or the error code.
func statusLabel(result *Result, err error) string {
	switch {
	case err == nil && result != nil:
		return result.Status
	case errors.Is(err, ErrInvalidTarget):
		return "invalid_target"
	case errors.Is(err, ErrTargetNotAllowed):
		return "target_not_allowed"
	case errors.Is(err, ErrResolveFailed):
		return "resolve_failed"
	case errors.Is(err, ErrTimeout):
		return "timeout"
	case errors.Is(err, ErrBusy):
		return "busy"
	default:
		return "error"
	}
}

// hashIP is a salted, truncated hash: stable within one process, so abuse
// stands out in the logs, and never the raw IP.
func (h *Handler) hashIP(ip string) string {
	sum := sha256.Sum256(append(append([]byte{}, h.salt...), ip...))

	return hex.EncodeToString(sum[:ipHashBytes])
}

func secondsUntilUTCMidnight(now time.Time) int {
	midnight := now.Truncate(hoursPerDay * time.Hour).Add(hoursPerDay * time.Hour)

	return int(midnight.Sub(now).Seconds()) + 1
}

// dailyCounter counts runs per IP hash for the current UTC day. The whole map
// is dropped when the day changes, which keeps it bounded by one day of
// distinct callers.
type dailyCounter struct {
	mu     sync.Mutex
	day    string
	counts map[string]int
}

// add counts one run and reports the caller's count today and whether it is
// within RunsPerDay. A refused run is not counted.
func (d *dailyCounter) add(key string, now time.Time) (int, bool) {
	day := now.Format(time.DateOnly)

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.day != day {
		d.day = day
		d.counts = make(map[string]int)
	}

	if d.counts[key] >= RunsPerDay {
		return d.counts[key], false
	}

	d.counts[key]++

	return d.counts[key], true
}
