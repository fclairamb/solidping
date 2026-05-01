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
	"github.com/fclairamb/solidping/server/internal/handlers/checkgroups"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/connections"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	regionshandler "github.com/fclairamb/solidping/server/internal/handlers/regions"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

const (
	sessionTTL      = time.Hour
	cleanupInterval = 5 * time.Minute
	mcpProtocolVer  = "2025-03-26"
)

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
	resultsSvc     *results.Service
	incidentsSvc   *incidents.Service
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
) *Handler {
	handler := &Handler{
		checksSvc:      checks.NewService(dbService, eventNotifier),
		resultsSvc:     results.NewService(dbService),
		incidentsSvc:   incidents.NewService(dbService, jobSvc),
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
	orgSlug := claims.OrgSlug

	var rpcReq Request
	if err := json.NewDecoder(req.Body).Decode(&rpcReq); err != nil {
		return writeJSON(writer, http.StatusOK,
			errorResponse(nil, CodeParseError, "Parse error"))
	}

	if rpcReq.JSONRPC != "2.0" {
		return writeJSON(writer, http.StatusOK,
			errorResponse(rpcReq.ID, CodeInvalidRequest, "Invalid JSON-RPC version"))
	}

	resp, statusCode := h.dispatch(req.Context(), &rpcReq, orgSlug, writer)
	if resp == nil {
		return nil
	}

	return writeJSON(writer, statusCode, resp)
}

func (h *Handler) dispatch(
	ctx context.Context, req *Request, orgSlug string, writer http.ResponseWriter,
) (*Response, int) {
	switch req.Method {
	case "initialize":
		return h.handleInitialize(req, orgSlug, writer)
	case "notifications/initialized":
		writer.WriteHeader(http.StatusAccepted)
		return nil, 0
	case "ping":
		resp := successResponse(req.ID, map[string]any{})
		return &resp, http.StatusOK
	case "tools/list":
		return h.handleToolsList(req)
	case "tools/call":
		return h.handleToolsCall(ctx, req, orgSlug)
	case "resources/list":
		return h.handleResourcesList(req)
	case "resources/read":
		return h.handleResourcesRead(ctx, req, orgSlug)
	default:
		resp := errorResponse(req.ID, CodeMethodNotFound, "Method not found")
		return &resp, http.StatusOK
	}
}

func (h *Handler) handleInitialize(
	req *Request, orgSlug string, writer http.ResponseWriter,
) (*Response, int) {
	var params InitializeParams
	if req.Params != nil {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp := errorResponse(req.ID, CodeInvalidParams, "Invalid params")
			return &resp, http.StatusOK
		}
	}

	sessionID := uuid.New().String()
	now := time.Now()
	h.sessions.Store(sessionID, &session{
		id:              sessionID,
		protocolVersion: params.ProtocolVersion,
		clientInfo:      params.ClientInfo,
		orgSlug:         orgSlug,
		createdAt:       now,
		lastUsed:        now,
	})

	writer.Header().Set("Mcp-Session-Id", sessionID)

	resp := successResponse(req.ID, InitializeResult{
		ProtocolVersion: mcpProtocolVer,
		Capabilities: ServerCaps{
			Tools:     &ToolsCap{},
			Resources: &ResourcesCap{},
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
	ctx context.Context, req *Request, orgSlug string,
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
