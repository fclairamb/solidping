package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	entcore "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checkgroups"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/checktypes"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/handlers/maintenancewindows"
	regionshandler "github.com/fclairamb/solidping/server/internal/handlers/regions"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

const (
	sessionTTL      = time.Hour
	cleanupInterval = 5 * time.Minute
)

// protocolVersion2025_03_26 is the MCP protocol version published in March
// 2025. Add newer entries to supportedProtocolVersions as they ship.
const protocolVersion2025_03_26 = "2025-03-26"

// negotiateProtocolVersion returns the version we should advertise to a
// client that requested clientVersion. Per the MCP spec: if we support the
// requested version, return it; otherwise return our latest. The client
// is responsible for disconnecting if it cannot speak what we returned.
func negotiateProtocolVersion(clientVersion string) string {
	supported := []string{
		protocolVersion2025_03_26,
		// Add new versions to the front as we adopt them, e.g.
		// "2025-06-18" once structuredContent / outputSchema are wired.
	}

	if clientVersion != "" {
		for _, v := range supported {
			if v == clientVersion {
				return v
			}
		}
	}

	return supported[0]
}

type session struct {
	id              string
	protocolVersion string
	clientInfo      ClientInfo
	orgSlug         string
	createdAt       time.Time
	lastUsed        time.Time
}

// Handler handles MCP requests over Streamable HTTP.
type Handler struct {
	checksSvc       *checks.Service
	checkTypesSvc   *checktypes.Service
	resultsSvc      *results.Service
	incidentsSvc    *incidents.Service
	eventsSvc       *events.Service
	statusPagesSvc  *statuspages.Service
	maintenanceSvc  *maintenancewindows.Service
	integrationsSvc *integrations.Service
	checkGroupsSvc  *checkgroups.Service
	regionsSvc      *regionshandler.Service
	// publicationsSvc manages the status-page incident publication overlay
	// (spec 2026-08-19-08). No scheduler and no subscriber notifier are wired
	// here: the MCP surface performs OPERATOR actions (publish this incident,
	// post this update), and the auto-publish pipeline belongs to the server.
	publicationsSvc *incidentpublications.Service
	dbService       db.Service

	sessions sync.Map // map[string]*session
	tools    []ToolDefinition
	toolMap  map[string]toolFunc

	cancel context.CancelFunc
}

type toolFunc func(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult

// NewHandler creates a new MCP handler. rt may be nil (realtime disabled) —
// hint publishing is a nil-safe no-op then. cfg is the app config; it is
// threaded into integrationsSvc so that surface behaves identically to the
// HTTP integrations handler — notably the Twilio credential-verification
// test-run bypass (RunMode == "test"), which otherwise would make a live
// Twilio call for every MCP-created Twilio connection regardless of run mode.
func NewHandler(
	dbService db.Service,
	eventNotifier notifier.EventNotifier,
	jobSvc jobsvc.Service,
	checkTypesSvc *checktypes.Service,
	creds credentials.Service,
	entSvc *entcore.Service,
	rtPub *realtime.Publisher,
	cfg *config.Config,
) *Handler {
	// The MCP surface opens and resolves incidents through the same rollup
	// gate as the worker, so it needs the operator-configured per-check
	// timeout too: without it the confirmation-hold cap here would silently
	// use incidents.DefaultCheckTimeoutFallback and disagree with every other
	// ingest path on the same check. cfg may be nil in tests — the setter
	// ignores the resulting zero and the fallback stands.
	incidentsSvc := incidents.NewService(dbService, jobSvc, clock.Real{}, rtPub)
	if cfg != nil {
		incidentsSvc.SetDefaultCheckTimeout(cfg.Server.Scheduling.CheckTimeout())
	}

	handler := &Handler{
		checksSvc:     checks.NewService(dbService, eventNotifier, creds, entSvc),
		checkTypesSvc: checkTypesSvc,
		resultsSvc:    results.NewService(dbService, cfg),
		incidentsSvc:  incidentsSvc,
		eventsSvc:     events.NewService(dbService),
		// nil cfg: the MCP surface has no app config to hand; the uptime-bar
		// raw clamp and safety caps this feeds fall back to the live
		// performance.* parameters and then the documented retention defaults
		// (see statuspages.Service.uptimebarHints).
		statusPagesSvc: statuspages.NewService(dbService, nil, nil),
		maintenanceSvc: maintenancewindows.NewService(dbService),
		// nil registry: the MCP surface manages integrations but does not
		// dispatch test notifications, which is the only path needing it. cfg
		// IS passed (see doc comment above) — integrations.Service uses it
		// only for the Twilio test-run verification bypass today.
		integrationsSvc: integrations.NewService(dbService, creds, nil, cfg),
		checkGroupsSvc:  checkgroups.NewService(dbService),
		regionsSvc:      regionshandler.NewService(dbService),
		publicationsSvc: incidentpublications.NewService(dbService, clock.Real{}, rtPub),
		dbService:       dbService,
	}

	// A check created over MCP has to land on a dynamic status page section
	// exactly like one created from the dashboard (spec 2026-08-29-11).
	handler.checksSvc.SetStatusPageReconciler(handler.statusPagesSvc)

	handler.registerTools()

	return handler
}

// Start begins background goroutines (session cleanup).
func (h *Handler) Start(ctx context.Context) {
	cleanupCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	go h.cleanupLoop(cleanupCtx)
}

// Stop stops the handler's background goroutines.
func (h *Handler) Stop() {
	if h.cancel != nil {
		h.cancel()
	}
}

// dashboardMCPPath is where a browser hitting GET on the MCP endpoint is
// sent: the dashboard's org-less MCP setup route, which resolves the org
// client-side and forwards to /orgs/$org/mcp. The from=get search param keys
// the contextual "you opened the API endpoint in a browser" hint.
const dashboardMCPPath = "/dash0/mcp?from=get"

// allowedMCPMethods is the Allow header value advertised on 405 responses:
// the methods the MCP endpoint actually implements.
const allowedMCPMethods = "POST, DELETE"

// acceptsEventStream reports whether the Accept header explicitly lists
// text/event-stream — the signature of an MCP client opening a listening
// stream (Streamable HTTP GET). Browser Accept lists (text/html,...,*/*)
// never name it explicitly, so wildcards intentionally don't match.
func acceptsEventStream(accept string) bool {
	for _, part := range strings.Split(accept, ",") {
		mediaType, _, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream") {
			return true
		}
	}

	return false
}

// HandleGet handles GET on the MCP endpoint. The Streamable HTTP transport
// expects GET to either open an SSE stream for server-initiated messages or
// return 405 Method Not Allowed. We don't support server-initiated streams,
// so MCP clients (Accept: text/event-stream) get the spec answer, while a
// human in a browser gets a redirect to the dashboard page that explains how
// to actually connect a client. Unauthenticated by design: a browser hitting
// the URL has no token, and the redirect leaks nothing.
func (h *Handler) HandleGet(writer http.ResponseWriter, req *http.Request) error {
	if acceptsEventStream(req.Header.Get("Accept")) {
		writer.Header().Set("Allow", allowedMCPMethods)

		return writeJSON(writer, http.StatusMethodNotAllowed, base.ErrorResponse{
			Title:  "SSE streams are not supported on this MCP endpoint",
			Code:   string(base.ErrorCodeMethodNotAllowed),
			Detail: "Send JSON-RPC requests with POST; GET streaming is not implemented.",
		})
	}

	http.Redirect(writer, req, dashboardMCPPath, http.StatusFound)

	return nil
}

// HandleDelete terminates the MCP session named by the Mcp-Session-Id header
// (Streamable HTTP explicit session termination). Auth is enforced by
// RequireMCPAuth at the route; the session must belong to the caller's org.
func (h *Handler) HandleDelete(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := middleware.GetClaimsFromContext(req.Context())
	if !ok {
		return writeJSON(writer, http.StatusUnauthorized, base.ErrorResponse{
			Title: "Authentication required",
			Code:  string(base.ErrorCodeUnauthorized),
		})
	}

	sessionID := req.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		return writeJSON(writer, http.StatusBadRequest, base.ErrorResponse{
			Title: "Mcp-Session-Id header is required",
			Code:  string(base.ErrorCodeValidationError),
		})
	}

	value, found := h.sessions.Load(sessionID)
	if found {
		// A session from another org is indistinguishable from a missing one
		// so cross-org probing can't confirm session IDs exist.
		sess, isSession := value.(*session)
		found = isSession && sess.orgSlug == claims.OrgSlug
	}

	if !found {
		return writeJSON(writer, http.StatusNotFound, base.ErrorResponse{
			Title: "Unknown MCP session",
			Code:  string(base.ErrorCodeNotFound),
		})
	}

	h.sessions.Delete(sessionID)
	writer.WriteHeader(http.StatusNoContent)

	return nil
}

// Handle handles an MCP HTTP request.
func (h *Handler) Handle(writer http.ResponseWriter, req *http.Request) error {
	if req.Method != http.MethodPost {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}

	// Extract org slug from authenticated claims
	claims, ok := middleware.GetClaimsFromContext(req.Context())
	if !ok {
		return writeJSON(writer, http.StatusUnauthorized,
			errorResponse(nil, CodeInvalidRequest, "Authentication required"))
	}

	if !hasMCPAccess(claims) {
		return writeJSON(writer, http.StatusForbidden,
			errorResponse(nil, CodeForbidden, "Token lacks mcp or mcp:read scope"))
	}

	orgSlug := claims.OrgSlug

	var rpcReq Request
	if err := json.NewDecoder(req.Body).Decode(&rpcReq); err != nil {
		return writeJSON(writer, http.StatusOK,
			errorResponse(nil, CodeParseError, "Parse error"))
	}

	if rpcReq.JSONRPC != jsonRPCVersion {
		return writeJSON(writer, http.StatusOK,
			errorResponse(rpcReq.ID, CodeInvalidRequest, "Invalid JSON-RPC version"))
	}

	resp, statusCode := h.dispatch(req.Context(), &rpcReq, orgSlug, claims, writer)
	if resp == nil {
		return nil
	}

	return writeJSON(writer, statusCode, resp)
}

func (h *Handler) dispatch(
	ctx context.Context, req *Request, orgSlug string,
	claims *auth.Claims, writer http.ResponseWriter,
) (*Response, int) {
	switch req.Method {
	case methodInitialize:
		return h.handleInitialize(ctx, req, orgSlug, writer)
	case methodInitialized:
		writer.WriteHeader(http.StatusAccepted)
		return nil, 0
	case methodPing:
		resp := successResponse(req.ID, map[string]any{})
		return &resp, http.StatusOK
	case methodToolsList:
		return h.handleToolsList(req)
	case methodToolsCall:
		return h.handleToolsCall(ctx, req, orgSlug, claims)
	case methodResourcesList:
		return h.handleResourcesList(req)
	case methodResourcesRead:
		return h.handleResourcesRead(ctx, req, orgSlug)
	case methodPromptsList:
		return h.handlePromptsList(req)
	case methodPromptsGet:
		return h.handlePromptsGet(req)
	default:
		resp := errorResponse(req.ID, CodeMethodNotFound, "Method not found")
		return &resp, http.StatusOK
	}
}

func (h *Handler) handleInitialize(
	ctx context.Context, req *Request, orgSlug string, writer http.ResponseWriter,
) (*Response, int) {
	var params InitializeParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp := errorResponse(req.ID, CodeInvalidParams, "Invalid params")
			return &resp, http.StatusOK
		}
	}

	negotiated := negotiateProtocolVersion(params.ProtocolVersion)
	if params.ProtocolVersion != "" && negotiated != params.ProtocolVersion {
		slog.InfoContext(ctx, "MCP version negotiation fallback",
			"clientRequested", params.ProtocolVersion,
			"serverReturned", negotiated)
	}

	sessionID := uuid.New().String()
	now := time.Now()
	h.sessions.Store(sessionID, &session{
		id:              sessionID,
		protocolVersion: negotiated,
		clientInfo:      params.ClientInfo,
		orgSlug:         orgSlug,
		createdAt:       now,
		lastUsed:        now,
	})

	writer.Header().Set("Mcp-Session-Id", sessionID)

	resp := successResponse(req.ID, InitializeResult{
		ProtocolVersion: negotiated,
		Capabilities: ServerCaps{
			Tools:     &ToolsCap{},
			Resources: &ResourcesCap{},
			Prompts:   &PromptsCap{},
		},
		ServerInfo: ServerInfo{Name: "solidping", Version: "0.1.0"},
	})

	return &resp, http.StatusOK
}

func (h *Handler) handleToolsList(req *Request) (*Response, int) {
	resp := successResponse(req.ID, ToolsListResult{Tools: h.tools})
	return &resp, http.StatusOK
}

func (h *Handler) handleToolsCall(
	ctx context.Context, req *Request, orgSlug string, claims *auth.Claims,
) (*Response, int) {
	var params ToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		resp := errorResponse(req.ID, CodeInvalidParams, "Invalid params")
		return &resp, http.StatusOK
	}

	toolFn, ok := h.toolMap[params.Name]
	if !ok {
		resp := errorResponse(req.ID, CodeMethodNotFound, "Unknown tool: "+params.Name)
		return &resp, http.StatusOK
	}

	if isMCPReadOnly(claims) && isMutationTool(params.Name) {
		resp := errorResponse(req.ID, CodeForbidden,
			"Tool "+params.Name+" requires the mcp scope; current token has mcp:read only")
		return &resp, http.StatusOK
	}

	result := toolFn(ctx, orgSlug, params.Arguments)
	resp := successResponse(req.ID, result)
	return &resp, http.StatusOK
}

func (h *Handler) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			h.sessions.Range(func(key, value any) bool {
				sess, ok := value.(*session)
				if !ok {
					return true
				}
				if now.Sub(sess.lastUsed) > sessionTTL {
					h.sessions.Delete(key)
					slog.DebugContext(ctx, "MCP session expired", "sessionId", sess.id)
				}
				return true
			})
		}
	}
}

func writeJSON(writer http.ResponseWriter, status int, data any) error {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	return json.NewEncoder(writer).Encode(data)
}

// Argument extraction helpers.

func getStringArg(args map[string]any, key string) string {
	val, ok := args[key]
	if !ok {
		return ""
	}
	str, ok := val.(string)
	if !ok {
		return ""
	}
	return str
}

func getIntArg(args map[string]any, key string, defaultVal int) int {
	val, ok := args[key]
	if !ok {
		return defaultVal
	}
	switch num := val.(type) {
	case float64:
		return int(num)
	case int:
		return num
	default:
		return defaultVal
	}
}

func getBoolArg(args map[string]any, key string) *bool {
	val, ok := args[key]
	if !ok {
		return nil
	}
	boolVal, ok := val.(bool)
	if !ok {
		return nil
	}
	return &boolVal
}

func getStringSliceArg(args map[string]any, key string) []string {
	val, ok := args[key]
	if !ok {
		return nil
	}
	switch arr := val.(type) {
	case []any:
		result := make([]string, 0, len(arr))
		for _, item := range arr {
			if str, ok := item.(string); ok {
				result = append(result, str)
			}
		}
		return result
	case []string:
		return arr
	default:
		return nil
	}
}

func getMapArg(args map[string]any, key string) map[string]any {
	val, ok := args[key]
	if !ok {
		return nil
	}
	mapVal, ok := val.(map[string]any)
	if !ok {
		return nil
	}
	return mapVal
}

func getStringMapArg(args map[string]any, key string) map[string]string {
	mapVal := getMapArg(args, key)
	if mapVal == nil {
		return nil
	}
	result := make(map[string]string, len(mapVal))
	for k, v := range mapVal {
		if str, ok := v.(string); ok {
			result[k] = str
		}
	}
	return result
}
