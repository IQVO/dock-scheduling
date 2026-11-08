package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

// IdempotencyKeyHeader is the request header a caller must supply on every POST.
const IdempotencyKeyHeader = "Idempotency-Key"

// IdempotencyReplayedHeader is set to "true" on a response replayed from the
// stored answer of an earlier request with the same key, and only then.
const IdempotencyReplayedHeader = "Idempotency-Replayed"

// maxIdempotencyKeyLen is the longest accepted Idempotency-Key.
const maxIdempotencyKeyLen = 128

// validIdempotencyKey reports whether key is 1-128 printable ASCII characters.
func validIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > maxIdempotencyKeyLen {
		return false
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return false
		}
	}
	return true
}

// errDiscard rolls the middleware's unit of work back without being a failure.
var errDiscard = errors.New("discard the handler's work")

// idempotency is the fleet Idempotency-Key middleware over the
// ports.IdempotencyStore. Every answer below 500 is stored (4xx included): the
// same key and request replays the same answer with Idempotency-Replayed:
// true; the same key with another method, path or body is 409
// idempotency-key-reused. A 5xx or a panic is never stored, so the same key
// can retry the real work.
//
// Two modes. JOINED (every state-changing POST): the handler runs inside ONE
// unit of work together with the lookup and, for a 2xx answer, the store of
// the answer, so the aggregate, its outbox rows and the key commit or roll
// back together (the use cases' own Do joins). A 4xx or 5xx answer rolls the
// unit back (a failed statement may have aborted the transaction) and a 4xx is
// stored afterwards on its own. DETACHED (the sweep, naturally idempotent and
// one transaction per appointment by design): the handler runs with no outer
// unit of work and the answer is stored after it.
//
// Racing requests with the same key all try to store; the store keeps the
// first writer (a concurrent Save blocks on the primary key until the other
// transaction ends). The loser sees another answer on its post-save re-read,
// rolls its work back and replays the winner's.
type idempotency struct {
	store ports.IdempotencyStore
	uow   ports.UnitOfWork
	now   func() time.Time
}

// attempt is what one pass of the middleware decided.
type attempt struct {
	stored *repository.IdempotencyRecord // replay this
	fresh  *capture                      // or answer with this
	kept   bool                          // fresh was stored as the answer of the key
}

func (m *idempotency) wrap(next http.Handler, detached bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(IdempotencyKeyHeader)
		if key == "" {
			writeProblem(w, r, idempotencyKeyRequired, "send an Idempotency-Key header with a unique value per logical request")
			return
		}
		if !validIdempotencyKey(key) {
			writeProblem(w, r, idempotencyKeyInvalid, "the Idempotency-Key header must be 1 to 128 printable ASCII characters")
			return
		}
		fp, err := fingerprint(r)
		if err != nil {
			writeError(w, r, err)
			return
		}
		var res attempt
		if detached {
			err = m.detached(r, key, fp, next, &res)
		} else {
			err = m.uow.Do(r.Context(), func(ctx context.Context) error { return m.joined(ctx, r, key, fp, next, &res) })
		}
		if err != nil && !errors.Is(err, errDiscard) {
			writeError(w, r, err)
			return
		}
		m.finish(w, r, key, fp, &res)
	})
}

// joined is one pass inside the unit of work.
func (m *idempotency) joined(ctx context.Context, r *http.Request, key, fp string, next http.Handler, res *attempt) error {
	rec, found, err := m.store.Lookup(ctx, key)
	if err != nil {
		return err
	}
	if found {
		res.stored = &rec
		return nil
	}
	c := serveCaptured(next, r.WithContext(ctx))
	res.fresh = c
	if c.code >= http.StatusBadRequest {
		return errDiscard
	}
	return m.keep(ctx, key, fp, c, res)
}

// detached is one pass with no outer unit of work.
func (m *idempotency) detached(r *http.Request, key, fp string, next http.Handler, res *attempt) error {
	ctx := r.Context()
	rec, found, err := m.store.Lookup(ctx, key)
	if err != nil {
		return err
	}
	if found {
		res.stored = &rec
		return nil
	}
	c := serveCaptured(next, r)
	res.fresh = c
	if c.code >= http.StatusInternalServerError {
		return nil
	}
	return m.keep(ctx, key, fp, c, res)
}

// keep stores c as the answer of key and re-reads the winner; when another
// request won, the pass replays it and (inside a unit of work) discards ours.
func (m *idempotency) keep(ctx context.Context, key, fp string, c *capture, res *attempt) error {
	rec := c.record(key, fp, m.now().UTC().Truncate(time.Microsecond))
	if err := m.store.Save(ctx, rec); err != nil {
		return err
	}
	got, found, err := m.store.Lookup(ctx, key)
	if err != nil {
		return err
	}
	if !found {
		return errMissingOutcome
	}
	if !sameRecord(got, rec) {
		res.stored, res.fresh = &got, nil
		return errDiscard
	}
	res.kept = true
	return nil
}

// finish answers from the pass: a stored answer is replayed, a 5xx is flushed
// as is, a 4xx is stored on its own first, a 2xx was stored with its work.
func (m *idempotency) finish(w http.ResponseWriter, r *http.Request, key, fp string, res *attempt) {
	if res.stored == nil && !res.kept && res.fresh != nil && res.fresh.code >= http.StatusBadRequest && res.fresh.code < http.StatusInternalServerError {
		m.keep4xx(r.Context(), key, fp, res)
	}
	if res.stored != nil {
		replay(w, r, res.stored, fp)
		return
	}
	res.fresh.flush(w)
}

// keep4xx stores a 4xx answer outside the discarded unit of work. A failure to
// store is logged, never surfaced: the client still gets the truthful 4xx.
func (m *idempotency) keep4xx(ctx context.Context, key, fp string, res *attempt) {
	err := m.uow.Do(ctx, func(ctx context.Context) error { return m.keep(ctx, key, fp, res.fresh, res) })
	if err != nil && !errors.Is(err, errDiscard) {
		slog.WarnContext(ctx, "idempotency: could not store a client-error answer", "error", err)
	}
}

// replay answers a stored record, or 409 when the key belongs to another request.
func replay(w http.ResponseWriter, r *http.Request, rec *repository.IdempotencyRecord, fp string) {
	if rec.Fingerprint != fp {
		writeProblem(w, r, idempotencyKeyReused, "the Idempotency-Key was used with a different request (method, path or body)")
		return
	}
	var env envelope
	if err := json.Unmarshal(rec.Body, &env); err != nil {
		writeError(w, r, errMissingOutcome)
		return
	}
	for k, vs := range env.Headers {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set(IdempotencyReplayedHeader, "true")
	w.WriteHeader(rec.Status)
	_, _ = w.Write(env.Body)
}

var errMissingOutcome = errors.New("idempotency record has no readable outcome")

// fingerprint identifies the request: method, path and body.
func fingerprint(r *http.Request) (string, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	_ = r.Body.Close()
	if err != nil {
		return "", errMalformedRequest
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.URL.EscapedPath()+"\n"), body...))
	return hex.EncodeToString(sum[:]), nil
}

// sameRecord reports whether two records are the very answer one request saved.
func sameRecord(a, b repository.IdempotencyRecord) bool {
	return a.Fingerprint == b.Fingerprint && a.Status == b.Status && bytes.Equal(a.Body, b.Body) && a.CreatedAt.Equal(b.CreatedAt)
}
