// Package httpx is a thin routing adapter over github.com/go-chi/chi/v5 that
// preserves the ergonomics the codebase relied on under uptrace/bunrouter:
//
//   - Handlers keep the error-returning signature
//     func(http.ResponseWriter, *http.Request) error, so a handler body can
//     `return err` after writing its response via handlers/base. Wrap adapts
//     such a handler to a stdlib http.HandlerFunc, discarding the returned
//     error exactly like bunrouter's ServeHTTP did (handlers write their own
//     responses; the returned error is only observed by the middleware chain).
//   - Middleware is an error-returning decorator (HandlerFunc -> HandlerFunc),
//     so the whole chain (cors, sentry, logging, metrics, timeout, rate limit,
//     auth, org access, ...) threads errors up to the logging/metrics
//     middleware the same way it did before.
//   - Group mirrors bunrouter.Group: NewGroup joins a path prefix, Use appends
//     middleware (returning a new group), and the verb methods register a route
//     with the group's accumulated middleware applied outermost-first.
//
// Routing itself (path matching, params, static-vs-param precedence, 404/405)
// is delegated to chi. Bunrouter-style ":param" / "*wildcard" patterns are
// translated to chi's "{param}" / "*" on registration.
package httpx

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
)

// HandlerFunc is an HTTP handler that returns an error. Handlers write their
// own response (via handlers/base) and return the write error, if any.
type HandlerFunc func(http.ResponseWriter, *http.Request) error

// Middleware decorates a HandlerFunc, mirroring bunrouter.MiddlewareFunc.
type Middleware func(HandlerFunc) HandlerFunc

// Param returns the named URL path parameter for the current route, delegating
// to chi. It is the migration target for bunrouter's req.Param(name).
func Param(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// RoutePattern returns the matched route pattern (e.g. "/api/v1/orgs/{org}/checks")
// for the current request, or "" if unavailable. Used to keep HTTP metric
// cardinality bounded — one series per registered route, not per raw URL. It
// replaces bunrouter.Request.Route().
func RoutePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		return rc.RoutePattern()
	}

	return ""
}

// Wrap adapts an error-returning HandlerFunc into a stdlib http.HandlerFunc.
// The returned error is discarded, matching bunrouter's ServeHTTP, which did
// `_ = ServeHTTPError(...)`: handlers are responsible for writing their own
// responses, and the error return exists only for the middleware chain to
// observe (logging/metrics) as it unwinds.
func Wrap(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = h(w, r)
	}
}

// HTTPHandler adapts a stdlib http.Handler into a HandlerFunc so it can be
// registered through a Group and pick up the group's middleware chain. It
// replaces bunrouter.HTTPHandler (used to mount the Prometheus handler).
func HTTPHandler(h http.Handler) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		h.ServeHTTP(w, r)

		return nil
	}
}

// Route is one registered (method, resolved chi pattern) pair.
type Route struct {
	Method  string
	Pattern string
}

// DuplicateRouteError is the panic value raised when the same method+pattern is
// registered twice on one Router. It is a distinct type so a caller can assert
// on the failure with errors.As rather than on a message string.
type DuplicateRouteError struct {
	Route Route
}

func (e DuplicateRouteError) Error() string {
	return fmt.Sprintf(
		"httpx: duplicate route registration %s %s — chi silently keeps only the LAST handler, "+
			"so one of the two would be unreachable at runtime",
		e.Route.Method, e.Route.Pattern)
}

// routeRegistry records every route registered on one Router, so a duplicate
// can be caught WHERE THE INFORMATION STILL EXISTS: at registration time.
//
// After registration it is gone. chi stores handlers in a
// map[method]http.Handler per node and `setEndpoint` overwrites that entry, so
// a duplicate is erased from the tree before chi.Walk can ever see it — a
// post-hoc uniqueness check over the walked table is a tautology that can never
// fail. That is not a hypothetical: spec 2026-08-21-07 shipped an operator-side
// endpoint that a later public registration on the same pattern silently made
// unreachable, and nothing in the build noticed.
type routeRegistry struct {
	mu    sync.Mutex
	seen  map[Route]struct{}
	order []Route
}

func newRouteRegistry() *routeRegistry {
	return &routeRegistry{seen: make(map[Route]struct{})}
}

// record registers a route, panicking on a duplicate.
//
// A PANIC rather than a returned error, deliberately: every call site is a
// literal line in the route table that runs once at boot, none of them checks
// an error today, and a route table with two handlers on one pattern is not a
// condition to degrade through — it is a build that cannot serve what it
// claims. Failing at boot turns a silent runtime hole into a startup crash with
// the offending pattern named.
func (reg *routeRegistry) record(method, pattern string) {
	route := Route{Method: method, Pattern: pattern}

	reg.mu.Lock()
	defer reg.mu.Unlock()

	if _, exists := reg.seen[route]; exists {
		panic(DuplicateRouteError{Route: route})
	}

	reg.seen[route] = struct{}{}
	reg.order = append(reg.order, route)
}

// snapshot returns the recorded routes, sorted for stable comparison.
func (reg *routeRegistry) snapshot() []Route {
	reg.mu.Lock()
	defer reg.mu.Unlock()

	out := make([]Route, len(reg.order))
	copy(out, reg.order)

	sort.Slice(out, func(i, j int) bool {
		if out[i].Pattern != out[j].Pattern {
			return out[i].Pattern < out[j].Pattern
		}

		return out[i].Method < out[j].Method
	})

	return out
}

// Router is the root of the routing tree, backed by a chi mux. It implements
// http.Handler, so it drops in wherever the old *bunrouter.Router was used.
type Router struct {
	mux      *chi.Mux
	registry *routeRegistry
	Group
}

// New creates a Router backed by a fresh chi mux.
func New() *Router {
	mux := chi.NewRouter()
	registry := newRouteRegistry()
	r := &Router{mux: mux, registry: registry}
	r.Group = Group{mux: mux, registry: registry}

	return r
}

// RegisteredRoutes returns every route registered on this Router, in sorted
// order, INCLUDING any that a later duplicate would have shadowed.
//
// This is the honest counterpart to Walk: Walk reports what chi's tree ended up
// holding, which is already the post-overwrite state. Use this when the
// question is "what did the code ask for", and Walk when it is "what will the
// mux actually match".
func (r *Router) RegisteredRoutes() []Route {
	return r.registry.snapshot()
}

// ServeHTTP implements http.Handler.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

// Walk visits every registered route as (method, chi pattern). It exists so a
// test can assert a property over the WHOLE route table rather than over a
// hand-maintained list — the route table is 1500 lines of registrations, and a
// new group that forgets a cross-cutting middleware is invisible in review.
func (r *Router) Walk(fn func(method, pattern string) error) error {
	return chi.Walk(r.mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		return fn(method, route)
	})
}

// Group is a set of routes sharing a path prefix and a middleware stack. It
// mirrors bunrouter.Group so the route table in app/server.go maps over almost
// unchanged.
type Group struct {
	mux   *chi.Mux
	path  string
	stack []Middleware
	// registry is shared with the owning Router (and every sibling group), so
	// duplicate detection spans the whole route table rather than one group.
	// nil only for a zero-value Group, which cannot register anything anyway.
	registry *routeRegistry
}

// NewGroup returns a sub-group with the given path appended to this group's
// prefix and a copy of this group's middleware stack.
func (g *Group) NewGroup(path string) *Group {
	return &Group{
		mux:      g.mux,
		path:     joinPath(g.path, path),
		stack:    g.cloneStack(),
		registry: g.registry,
	}
}

// Use returns a new group with the given middleware appended to the stack.
// Middleware added first runs outermost (first), matching bunrouter.
func (g *Group) Use(middlewares ...Middleware) *Group {
	ng := g.NewGroup("")
	ng.stack = append(ng.stack, middlewares...)

	return ng
}

func (g *Group) cloneStack() []Middleware {
	// Full three-index slice so a later append allocates rather than
	// clobbering a sibling group that shares the backing array.
	return g.stack[:len(g.stack):len(g.stack)]
}

// wrap applies the group's middleware stack to a handler, outermost-first.
func (g *Group) wrap(handler HandlerFunc) HandlerFunc {
	for i := len(g.stack) - 1; i >= 0; i-- {
		handler = g.stack[i](handler)
	}

	return handler
}

// Handle registers handler for method at this group's prefix + path, with the
// group's middleware chain applied.
func (g *Group) Handle(method, path string, handler HandlerFunc) {
	pattern := convertPattern(g.path + path)

	// Recorded BEFORE handing the route to chi: once chi has it, a duplicate is
	// indistinguishable from a single registration. See routeRegistry.
	if g.registry != nil {
		g.registry.record(method, pattern)
	}

	g.mux.Method(method, pattern, Wrap(g.wrap(handler)))
}

// GET registers a GET route. See Handle.
func (g *Group) GET(path string, handler HandlerFunc) { g.Handle(http.MethodGet, path, handler) }

// POST registers a POST route. See Handle.
func (g *Group) POST(path string, handler HandlerFunc) { g.Handle(http.MethodPost, path, handler) }

// PUT registers a PUT route. See Handle.
func (g *Group) PUT(path string, handler HandlerFunc) { g.Handle(http.MethodPut, path, handler) }

// PATCH registers a PATCH route. See Handle.
func (g *Group) PATCH(path string, handler HandlerFunc) { g.Handle(http.MethodPatch, path, handler) }

// DELETE registers a DELETE route. See Handle.
func (g *Group) DELETE(path string, handler HandlerFunc) { g.Handle(http.MethodDelete, path, handler) }

// HEAD registers a HEAD route. See Handle.
func (g *Group) HEAD(path string, handler HandlerFunc) { g.Handle(http.MethodHead, path, handler) }

// OPTIONS registers an OPTIONS route. See Handle.
func (g *Group) OPTIONS(path string, handler HandlerFunc) {
	g.Handle(http.MethodOptions, path, handler)
}

// joinPath concatenates a base prefix and a path, trimming a trailing slash so
// every registered sub-path keeps its own leading slash. Mirrors bunrouter's
// joinPath.
func joinPath(base, path string) string {
	joined := base + path
	if len(joined) > 1 && joined[len(joined)-1] == '/' {
		joined = joined[:len(joined)-1]
	}

	return joined
}

// convertPattern rewrites a bunrouter-style route pattern into chi's syntax:
//
//	:name  -> {name}     (named path parameter)
//	*name  -> *          (trailing catch-all; chi reads it via URLParam(r, "*"))
//
// Static segments are left untouched. A "*name" catch-all is only valid as the
// final segment, which is the only way bunrouter used it here.
func convertPattern(pattern string) string {
	if pattern == "" {
		return "/"
	}

	if !strings.ContainsAny(pattern, ":*") {
		return pattern
	}

	segments := strings.Split(pattern, "/")
	for i, seg := range segments {
		switch {
		case strings.HasPrefix(seg, ":"):
			segments[i] = "{" + seg[1:] + "}"
		case strings.HasPrefix(seg, "*"):
			segments[i] = "*"
		}
	}

	return strings.Join(segments, "/")
}
