package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/checkgroups"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/checktypes"
	"github.com/fclairamb/solidping/server/internal/handlers/connections"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/maintenancewindows"
	regionshandler "github.com/fclairamb/solidping/server/internal/handlers/regions"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/notifier"
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
	checksSvc      *checks.Service
	checkTypesSvc  *checktypes.Service
	resultsSvc     *results.Service
	incidentsSvc   *incidents.Service
	eventsSvc      *events.Service
	statusPagesSvc *statuspages.Service
	maintenanceSvc *maintenancewindows.Service
	connectionsSvc *connections.Service
	checkGroupsSvc *checkgroups.Service
	regionsSvc     *regionshandler.Service
	dbService      db.Service

	sessions sync.Map // map[string]*session
	tools    []ToolDefinition
	toolMap  map[string]toolFunc

	cancel context.CancelFunc
}

type toolFunc func(ctx context.Context, orgSlug string, args map[string]any) ToolCallResult

// NewHandler creates a new MCP handler.
func NewHandler(
	dbService db.Service,
	eventNotifier notifier.EventNotifier,
	jobSvc jobsvc.Service,
	checkTypesSvc *checktypes.Service,
) *Handler {
	handler := &Handler{
		checksSvc:      checks.NewService(dbService, eventNotifier),
		checkTypesSvc:  checkTypesSvc,
		resultsSvc:     results.NewService(dbService),
		incidentsSvc:   incidents.NewService(dbService, jobSvc),
		eventsSvc:      events.NewService(dbService),
		statusPagesSvc: statuspages.NewService(dbService),
		maintenanceSvc: maintenancewindows.NewService(dbService),
		connectionsSvc: connections.NewService(dbService),
		checkGroupsSvc: checkgroups.NewService(dbService),
		regionsSvc:     regionshandler.NewService(dbService),
		dbService:      dbService,
	}

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

// Handle handles an MCP HTTP request.
func (h *Handler) Handle(writer http.ResponseWriter, req bunrouter.Request) error {
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
