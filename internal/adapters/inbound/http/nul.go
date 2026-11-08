package http

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// rejectNUL turns a NUL character in a request into a client error before it
// can reach a Postgres text parameter. PostgreSQL text cannot hold U+0000, and
// a statement that carries one aborts the surrounding transaction (SQLSTATE
// 22021): that was a 500 on the real runtime of sibling services, hidden for
// as long as the in-memory adapters served the contract run.
//
//   - a NUL in the URL path can never name a resource: 404 not-found;
//   - a NUL in the query string (raw byte or %00) or in a JSON body (a raw
//     byte or the \u0000 escape) is a malformed request: 400
//     malformed-request-body, before the Idempotency-Key middleware records
//     anything.
func rejectNUL(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.ContainsRune(r.URL.Path, 0) || strings.ContainsRune(r.URL.RawPath, 0) || strings.Contains(strings.ToLower(r.URL.RawPath), "%00") {
			writeProblem(w, r, notFoundResource, "no such path")
			return
		}
		if strings.ContainsRune(r.URL.RawQuery, 0) || strings.Contains(strings.ToLower(r.URL.RawQuery), "%00") {
			writeError(w, r, errNULQuery)
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
			_ = r.Body.Close()
			if err != nil {
				writeError(w, r, fmt.Errorf("%w: %w", errMalformedRequest, err))
				return
			}
			if containsNUL(body) {
				writeError(w, r, errNULBody)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	})
}

// The malformed-request details of a NUL (both map to malformed-request-body).
var (
	errNULQuery = fmt.Errorf("%w: a NUL character (U+0000) is not allowed in the query string", errMalformedRequest)
	errNULBody  = fmt.Errorf("%w: a NUL character (U+0000) is not allowed in a request body", errMalformedRequest)
)

// containsNUL reports whether a JSON document carries U+0000, either as a raw
// 0x00 byte or as the six-character escape \u0000. A backslash that is itself
// escaped (\\u0000, a literal backslash followed by the text u0000) is not a
// NUL: the escape only counts when it is preceded by an even number of
// backslashes.
func containsNUL(body []byte) bool {
	if bytes.IndexByte(body, 0) >= 0 {
		return true
	}
	const escape = `\u0000`
	for from := 0; ; {
		i := bytes.Index(body[from:], []byte(escape))
		if i < 0 {
			return false
		}
		i += from
		slashes := 0
		for j := i - 1; j >= 0 && body[j] == '\\'; j-- {
			slashes++
		}
		if slashes%2 == 0 {
			return true
		}
		from = i + len(escape)
	}
}
