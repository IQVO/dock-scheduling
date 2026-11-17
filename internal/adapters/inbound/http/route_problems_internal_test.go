package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func multiMethodRouter() chi.Router {
	r := chi.NewRouter()
	noop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	r.Get("/x", noop)
	r.Post("/x", noop)
	r.Delete("/x", noop)
	r.Get("/y", noop)
	return r
}

// A path with several methods proves the list is derived from the router, not
// hard-coded to GET.
func TestAllowedMethodsListsEveryRegisteredMethod(t *testing.T) {
	r := multiMethodRouter()
	for path, want := range map[string]string{"/x": "GET, POST, DELETE", "/y": "GET", "/nope": ""} {
		if got := allowedMethods(r, path); got != want {
			t.Errorf("allowedMethods(%s) = %q, want %q", path, got, want)
		}
	}
}

// Under a mount, chi hands the sub-router the prefix-stripped path in
// RoutePath; the handler must probe with that, not r.URL.Path.
func TestMethodNotAllowedForProbesTheMountedPath(t *testing.T) {
	sub := multiMethodRouter()
	sub.MethodNotAllowed(methodNotAllowedFor(sub))
	root := chi.NewRouter()
	root.Mount("/api", sub)
	rec := httptest.NewRecorder()
	root.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/x", http.NoBody))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got, want := rec.Header().Get("Allow"), "GET, POST, DELETE"; got != want {
		t.Errorf("Allow = %q, want %q", got, want)
	}
}
