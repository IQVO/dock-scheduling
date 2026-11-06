package http

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

// capture records a handler's response so it can be stored (and, for a
// joined pass, committed) BEFORE anything reaches the client.
type capture struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (c *capture) Header() http.Header { return c.header }

func (c *capture) WriteHeader(code int) {
	if c.code == 0 {
		c.code = code
	}
}

func (c *capture) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c.body.Write(b)
}

// serveCaptured runs next and returns what it wrote.
func serveCaptured(next http.Handler, r *http.Request) *capture {
	c := &capture{header: http.Header{}}
	next.ServeHTTP(c, r)
	if c.code == 0 {
		c.code = http.StatusOK
	}
	return c
}

// envelope is the encoded stored answer (headers plus payload) so a replay is
// byte-identical.
type envelope struct {
	Headers http.Header `json:"headers"`
	Body    []byte      `json:"body"`
}

// record is the answer as the idempotency store keeps it.
func (c *capture) record(key, fingerprint string, at time.Time) repository.IdempotencyRecord {
	body, _ := json.Marshal(envelope{Headers: c.header, Body: c.body.Bytes()}) // plain data: cannot fail
	return repository.IdempotencyRecord{
		Key: key, Fingerprint: fingerprint, Status: c.code, ContentType: c.header.Get("Content-Type"), Body: body, CreatedAt: at,
	}
}

// flush copies the captured response onto the real client connection.
func (c *capture) flush(w http.ResponseWriter) {
	for k, vs := range c.header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(c.code)
	_, _ = w.Write(c.body.Bytes())
}
