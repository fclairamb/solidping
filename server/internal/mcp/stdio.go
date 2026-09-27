package mcp

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/middleware"
)

// The stdio transport (`solidping mcp --stdio`, spec 2026-09-26-05).
//
// It serves the same dispatcher as the HTTP endpoint, in-process, on the
// server's own database: newline-delimited JSON-RPC on stdin, one reply per
// line on stdout, nothing else on stdout ever. There is no token: the process
// already holds the database credentials, so acting as a member of an org
// grants it nothing it could not do with SQL. What it acts as is decided once,
// at startup, by ResolveStdioPrincipal. The process is the session: there is
// no Mcp-Session-Id, and nothing is added to the HTTP session map.
//
// Every gate after authentication still runs: the scope gate (the principal
// carries the mcp scope), the demo gate (a demo user stays a demo session)
// and the role gate (read off the membership row on every call, so a viewer
// cannot write). must_change_password is deliberately ignored: that rotation
// protects a password, and no password is involved here.

// stdioReadBufferSize is the initial stdin buffer; lines may grow past it.
const stdioReadBufferSize = 64 * 1024

// Principal resolution failures. The command prints them on stderr and exits
// non-zero, so each one names what to change.
var (
	// ErrStdioOrgNotFound means --org names no live organization.
	ErrStdioOrgNotFound = errors.New("organization not found")
	// ErrStdioUserNotFound means --user names no live user.
	ErrStdioUserNotFound = errors.New("user not found")
	// ErrStdioNotMember means the --user exists but has no membership in --org.
	ErrStdioNotMember = errors.New("user is not a member of the organization")
	// ErrStdioNoOwner means --user was left out and the org has no owner to
	// default to.
	ErrStdioNoOwner = errors.New("organization has no owner")
)

// StdioPrincipal is who a stdio session acts as: one user, in one org, with
// the role their membership row held at startup. The role gate re-reads the
// membership on every mutation, so Role is informational (logs, claims).
type StdioPrincipal struct {
	User *models.User
	Org  *models.Organization
	Role models.MemberRole
}

// ResolveStdioPrincipal picks the principal of a stdio session.
//
// orgSlug is required (the command defaults it to the default org). userRef
// is an email or a user uid; empty means the org's owner, and the oldest owner
// membership when there are several, so the choice is stable across runs.
// Every miss is an error: an unknown org, an unknown user, a user who is not a
// member, and an org with no owner when userRef is empty.
func ResolveStdioPrincipal(ctx context.Context, dbSvc db.Service, orgSlug, userRef string) (*StdioPrincipal, error) {
	org, err := dbSvc.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %q (pick another with --org)", ErrStdioOrgNotFound, orgSlug)
		}

		return nil, fmt.Errorf("looking up organization %q: %w", orgSlug, err)
	}

	if userRef == "" {
		return resolveStdioOwner(ctx, dbSvc, org)
	}

	user, err := lookupStdioUser(ctx, dbSvc, userRef)
	if err != nil {
		return nil, err
	}

	member, err := dbSvc.GetMemberByUserAndOrg(ctx, user.UID, org.UID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s in %q", ErrStdioNotMember, user.Email, org.Slug)
		}

		return nil, fmt.Errorf("looking up the membership of %s in %q: %w", user.Email, org.Slug, err)
	}

	return &StdioPrincipal{User: user, Org: org, Role: member.Role}, nil
}

// lookupStdioUser finds the --user: by email when it has an @, by uid
// otherwise.
//
// Anything else is not found without asking the database: users.uid is a
// uuid column on Postgres, which answers a malformed value with a syntax
// error rather than no row, and the operator must read "user not found" on
// both engines.
func lookupStdioUser(ctx context.Context, dbSvc db.Service, userRef string) (*models.User, error) {
	notFound := fmt.Errorf("%w: %q (pass an email or a user uid to --user)", ErrStdioUserNotFound, userRef)

	var (
		user *models.User
		err  error
	)

	switch {
	case strings.Contains(userRef, "@"):
		user, err = dbSvc.GetUserByEmail(ctx, userRef)
	case isCanonicalUUID(userRef):
		user, err = dbSvc.GetUser(ctx, userRef)
	default:
		return nil, notFound
	}

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, notFound
		}

		return nil, fmt.Errorf("looking up user %q: %w", userRef, err)
	}

	return user, nil
}

// isCanonicalUUID reports whether ref is a uuid in its 36-character hyphenated
// form. uuid.Validate alone would also admit the urn: and braced spellings,
// which Postgres does not parse.
func isCanonicalUUID(ref string) bool {
	const canonicalUUIDLen = 36

	return len(ref) == canonicalUUIDLen && uuid.Validate(ref) == nil
}

// resolveStdioOwner returns the org's oldest live owner membership.
func resolveStdioOwner(ctx context.Context, dbSvc db.Service, org *models.Organization) (*StdioPrincipal, error) {
	members, err := dbSvc.ListMembersByOrg(ctx, org.UID)
	if err != nil {
		return nil, fmt.Errorf("listing the members of %q: %w", org.Slug, err)
	}

	owner := pickStdioOwner(members)
	if owner == nil {
		return nil, fmt.Errorf("%w: %q (name a member with --user)", ErrStdioNoOwner, org.Slug)
	}

	return &StdioPrincipal{User: owner.User, Org: org, Role: owner.Role}, nil
}

// pickStdioOwner returns the oldest owner membership whose user is live, or
// nil. Memberships sharing a created_at are ordered by membership uid, so the
// choice never depends on the order the database listed them in (it orders by
// created_at alone). ListMembersByOrg already leaves soft-deleted memberships
// out; a soft-deleted user is skipped here.
func pickStdioOwner(members []*models.OrganizationMember) *models.OrganizationMember {
	var owner *models.OrganizationMember

	for _, member := range members {
		if member.Role != models.MemberRoleOwner || member.DeletedAt != nil ||
			member.User == nil || member.User.DeletedAt != nil {
			continue
		}

		if owner == nil || member.CreatedAt.Before(owner.CreatedAt) ||
			(member.CreatedAt.Equal(owner.CreatedAt) && member.UID < owner.UID) {
			owner = member
		}
	}

	return owner
}

// Claims builds the claims a stdio session carries: the claims a full-scope
// MCP PAT of this user in this org would validate to.
func (p *StdioPrincipal) Claims() *auth.Claims {
	role := string(p.Role)
	if p.User.SuperAdmin {
		role = auth.RoleSuperAdmin
	}

	return &auth.Claims{
		UserUID: p.User.UID,
		OrgSlug: p.Org.Slug,
		Role:    role,
		Scopes:  []string{scopeMCP},
	}
}

// stdioReply is a JSON-RPC response as written on stdout. Unlike Response, the
// id is always present: every stdio reply answers a request, whose raw id is
// echoed byte for byte, or is an error whose id could not be read, which
// JSON-RPC 2.0 requires to be null.
type stdioReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// stdioNullID is the id of an error answering a message whose id is unreadable.
var stdioNullID = json.RawMessage("null") //nolint:gochecknoglobals // Immutable JSON literal.

// ServeStdio serves MCP to one client over input/out until input reaches EOF or ctx
// is canceled. Messages are handled one at a time, in order. It returns an
// error only when out is gone, since nothing more can be said to the client.
func (h *Handler) ServeStdio(ctx context.Context, input io.Reader, out io.Writer, principal *StdioPrincipal) error {
	claims := principal.Claims()
	// The same context RequireMCPAuth builds, once, for the whole session.
	ctx = middleware.WithMCPPrincipal(ctx, claims, principal.User)

	lines := make(chan []byte)

	go readStdioLines(ctx, input, lines)

	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				return nil
			}

			reply := h.handleStdioLine(ctx, line, claims)
			if reply == nil {
				continue
			}

			if _, err := out.Write(append(reply, '\n')); err != nil {
				return fmt.Errorf("writing to stdout: %w", err)
			}
		}
	}
}

// readStdioLines feeds every non-blank stdin line to lines, then closes it.
func readStdioLines(ctx context.Context, input io.Reader, lines chan<- []byte) {
	defer close(lines)

	reader := bufio.NewReaderSize(input, stdioReadBufferSize)

	for {
		line, err := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			select {
			case lines <- trimmed:
			case <-ctx.Done():
				return
			}
		}

		if err != nil {
			if !errors.Is(err, io.EOF) {
				slog.WarnContext(ctx, "MCP stdio: reading stdin failed", "error", err)
			}

			return
		}
	}
}

// handleStdioLine answers one stdin line: a message or a batch. It returns
// nil when nothing is owed, i.e. the line held only notifications.
func (h *Handler) handleStdioLine(ctx context.Context, line []byte, claims *auth.Claims) []byte {
	if !json.Valid(line) {
		return encodeStdioReply(ctx, stdioReply{ID: stdioNullID, Error: &RPCError{
			Code: CodeParseError, Message: "Parse error",
		}})
	}

	if line[0] != '[' {
		return h.handleStdioMessage(ctx, line, claims)
	}

	var members []json.RawMessage
	if err := json.Unmarshal(line, &members); err != nil || len(members) == 0 {
		return encodeStdioReply(ctx, stdioReply{ID: stdioNullID, Error: &RPCError{
			Code: CodeInvalidRequest, Message: "Invalid Request",
		}})
	}

	replies := make([]json.RawMessage, 0, len(members))

	for _, member := range members {
		if reply := h.handleStdioMessage(ctx, bytes.TrimSpace(member), claims); reply != nil {
			replies = append(replies, reply)
		}
	}

	if len(replies) == 0 {
		return nil
	}

	payload, err := json.Marshal(replies)
	if err != nil {
		slog.ErrorContext(ctx, "MCP stdio: encoding a batch reply failed", "error", err)

		return nil
	}

	return payload
}

// stdioEnvelope is the part of a message that decides whether it is owed a
// reply. The raw id is kept so the reply echoes it exactly (a float64
// round trip would corrupt a large integer id).
type stdioEnvelope struct {
	ID json.RawMessage `json:"id,omitempty"`
}

// isRequest reports whether the message expects a reply: a missing or null id
// makes it a notification, which is never answered, not even with an error.
func (e stdioEnvelope) isRequest() bool {
	return len(e.ID) > 0 && !bytes.Equal(bytes.TrimSpace(e.ID), stdioNullID)
}

// handleStdioMessage handles one JSON-RPC message and returns its encoded
// reply, or nil for a notification.
func (h *Handler) handleStdioMessage(ctx context.Context, msg []byte, claims *auth.Claims) []byte {
	var env stdioEnvelope
	if len(msg) == 0 || msg[0] != '{' || json.Unmarshal(msg, &env) != nil {
		// A batch member that is not an object has no id to answer with.
		return encodeStdioReply(ctx, stdioReply{ID: stdioNullID, Error: &RPCError{
			Code: CodeInvalidRequest, Message: "Invalid Request",
		}})
	}

	var req Request
	if err := json.Unmarshal(msg, &req); err != nil || req.JSONRPC != jsonRPCVersion {
		return stdioAnswer(ctx, env, errorResponse(nil, CodeInvalidRequest, "Invalid Request"))
	}

	resp := h.dispatchStdioRecovered(ctx, &req, claims)
	if resp == nil {
		// Only notifications/initialized dispatches to nothing. Sent with an
		// id, it is a request all the same and is owed an answer.
		empty := successResponse(nil, map[string]any{})
		resp = &empty
	}

	return stdioAnswer(ctx, env, *resp)
}

// dispatchStdioRecovered is dispatchStdio turning a panic into an internal
// error. A panicking tool must cost one reply, not the process: the HTTP path
// has a recovery middleware, and stdio has nothing else above it.
func (h *Handler) dispatchStdioRecovered(ctx context.Context, req *Request, claims *auth.Claims) *Response {
	var resp *Response

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(ctx, "MCP stdio: request panicked", "method", req.Method, "panic", recovered)

				internal := errorResponse(nil, CodeInternalError, "Internal error")
				resp = &internal
			}
		}()

		resp = h.dispatchStdio(ctx, req, claims)
	}()

	return resp
}

// dispatchStdio runs the shared dispatcher for a stdio principal. The HTTP
// response writer the dispatcher expects only ever receives the session header
// and the 202 of a notification, neither of which means anything here.
func (h *Handler) dispatchStdio(ctx context.Context, req *Request, claims *auth.Claims) *Response {
	var writer stdioResponseWriter

	if !hasMCPAccess(claims) {
		resp := errorResponse(req.ID, CodeForbidden, "Token lacks mcp or mcp:read scope")
		return &resp
	}

	if req.Method == methodInitialize {
		// No session: the process is the session, and a client that
		// re-initializes must not grow the HTTP session map.
		resp, _ := h.handleInitialize(ctx, req, claims.OrgSlug, &writer, false)
		return resp
	}

	resp, _ := h.dispatch(ctx, req, claims.OrgSlug, claims, &writer)

	return resp
}

// stdioAnswer encodes resp as the reply to the message env came from, or
// returns nil when that message was a notification.
func stdioAnswer(ctx context.Context, env stdioEnvelope, resp Response) []byte {
	if !env.isRequest() {
		if resp.Error != nil {
			slog.DebugContext(ctx, "MCP stdio: not answering a notification",
				"code", resp.Error.Code, "error", resp.Error.Message)
		}

		return nil
	}

	return encodeStdioReply(ctx, stdioReply{ID: env.ID, Result: resp.Result, Error: resp.Error})
}

// encodeStdioReply encodes one reply as a single line of compact JSON.
func encodeStdioReply(ctx context.Context, reply stdioReply) []byte {
	reply.JSONRPC = jsonRPCVersion

	payload, err := json.Marshal(reply)
	if err != nil {
		// A tool result that cannot be encoded: answer with an internal error
		// rather than leave the request hanging.
		slog.ErrorContext(ctx, "MCP stdio: encoding a reply failed", "error", err)

		id := reply.ID
		if len(id) == 0 {
			id = stdioNullID
		}

		payload = fmt.Appendf(nil, `{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":"Internal error"}}`,
			id, CodeInternalError)
	}

	return payload
}

// stdioResponseWriter is the http.ResponseWriter handed to the dispatcher on
// the stdio path. It records nothing.
type stdioResponseWriter struct {
	header http.Header
}

func (w *stdioResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}

	return w.header
}

func (w *stdioResponseWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *stdioResponseWriter) WriteHeader(int) {}
