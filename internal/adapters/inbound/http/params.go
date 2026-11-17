package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/dock-scheduling/internal/domain/dockappointment"
	"github.com/claudioed/dock-scheduling/internal/domain/shared"
)

var (
	// errUnknownQueryParameter backs 400 unknown-query-parameter (REST
	// guideline 4.2): a retired paging parameter must fail loudly.
	errUnknownQueryParameter = errors.New("unknown query parameter")
	// errInvalidQuery backs 400 invalid-query-parameter (a bad time, boolean or integer).
	errInvalidQuery = errors.New("query parameter is invalid")
	// errInvalidPathParameter backs 400 invalid-path-parameter.
	errInvalidPathParameter = errors.New("path parameter is invalid")
)

func errUnknownQuery(name string) error {
	return fmt.Errorf("%w: %q is not a parameter of this operation", errUnknownQueryParameter, name)
}

func invalidQuery(name, why string) error {
	return fmt.Errorf("%w: %s %s", errInvalidQuery, name, why)
}

func invalidPath(name string, cause error) error {
	return fmt.Errorf("%w: %s: %w", errInvalidPathParameter, name, cause)
}

// pathAppointmentID reads and validates {appointmentId}.
func pathAppointmentID(r *http.Request) (string, error) {
	id := chi.URLParam(r, "appointmentId")
	if _, err := dockappointment.NewID(id); err != nil {
		return "", invalidPath("appointmentId", err)
	}
	return id, nil
}

// pathDoorCode reads and validates {doorCode}.
func pathDoorCode(r *http.Request) (string, error) {
	code := chi.URLParam(r, "doorCode")
	if _, err := shared.NewDoorCode(code); err != nil {
		return "", invalidPath("doorCode", err)
	}
	return code, nil
}

// pathSiteID reads and validates {siteId}.
func pathSiteID(r *http.Request) (string, error) {
	site := chi.URLParam(r, "siteId")
	if _, err := shared.NewSiteID(site); err != nil {
		return "", invalidPath("siteId", err)
	}
	return site, nil
}

// etag is the weak entity tag of an appointment version.
func etag(version int64) string { return `W/"` + strconv.FormatInt(version, 10) + `"` }

// ifMatchVersion reads the optional If-Match header: absent or "*" is no
// precondition (0); otherwise the version of the weak or strong tag.
func ifMatchVersion(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" || raw == "*" {
		return 0, nil
	}
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 1 {
		return 0, fmt.Errorf("%w: If-Match must be the ETag of the resource", errInvalidIfMatch)
	}
	return v, nil
}

// errInvalidIfMatch backs 400 invalid-if-match.
var errInvalidIfMatch = errors.New("If-Match is invalid")

// rejectBadQuery answers 400 for a query string that does not parse (a bad
// percent escape): net/url would otherwise drop the pair silently and the
// request would be read as if the parameter were absent.
func rejectBadQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {
			writeError(w, r, invalidQuery("query string", "is not valid percent-encoding"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// unknownParam returns the first query parameter of r (sorted) not in allowed, or "".
func unknownParam(r *http.Request, allowed ...string) string {
	known := map[string]bool{}
	for _, a := range allowed {
		known[a] = true
	}
	var names []string
	for name := range r.URL.Query() {
		if !known[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return names[0]
}

// singleQuery returns the one value of a query parameter ("" when absent) and
// fails when it is repeated.
func singleQuery(r *http.Request, name string) (string, error) {
	vals := r.URL.Query()[name]
	if len(vals) > 1 {
		return "", invalidQuery(name, "must not be repeated")
	}
	if len(vals) == 0 {
		return "", nil
	}
	return vals[0], nil
}

// queryTime parses an optional RFC 3339 query parameter (zero when absent).
func queryTime(r *http.Request, name string) (time.Time, error) {
	raw, err := singleQuery(r, name)
	if err != nil || raw == "" {
		return time.Time{}, err
	}
	t, perr := time.Parse(time.RFC3339, raw)
	if perr != nil {
		return time.Time{}, invalidQuery(name, "must be an RFC 3339 time")
	}
	return t.UTC(), nil
}

// queryBool parses an optional boolean query parameter (false when absent).
func queryBool(r *http.Request, name string) (bool, error) {
	raw, err := singleQuery(r, name)
	if err != nil || raw == "" {
		return false, err
	}
	b, perr := strconv.ParseBool(raw)
	if perr != nil {
		return false, invalidQuery(name, "must be true or false")
	}
	return b, nil
}
