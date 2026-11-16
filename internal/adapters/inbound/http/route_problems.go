package http

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// probeMethods are the methods chi can route; allowedMethods tries each.
var probeMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
	http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace,
}

// allowedMethods lists, in probeMethods order, the methods routes has a
// handler for on path, as an Allow header value ("" when none).
func allowedMethods(routes chi.Routes, path string) string {
	var allowed []string
	for _, m := range probeMethods {
		if routes.Match(chi.NewRouteContext(), m, path) {
			allowed = append(allowed, m)
		}
	}
	return strings.Join(allowed, ", ")
}

// routedPath is the path the router matched on: chi strips a mount prefix into
// RoutePath for sub-routers.
func routedPath(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil && rctx.RoutePath != "" {
		return rctx.RoutePath
	}
	return r.URL.Path
}

// methodNotAllowedFor answers a request whose path exists but not for the
// request's method, as an RFC 7807 problem WITH the Allow header RFC 9110
// section 15.5.6 requires. chi sets Allow only in its DEFAULT 405 handler: once
// a custom handler is registered it is called as-is, so the list is derived
// from the router itself.
func methodNotAllowedFor(routes chi.Routes) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if allow := allowedMethods(routes, routedPath(r)); allow != "" {
			w.Header().Set("Allow", allow)
		}
		writeProblem(w, r, problem{http.StatusMethodNotAllowed, "method-not-allowed", "Method not allowed"}, "the method is not supported on this path")
	}
}
