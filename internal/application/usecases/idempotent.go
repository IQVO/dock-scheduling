package usecases

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

// IdempotencyKeyPrefix namespaces the keys of agent (MCP) writes inside the
// shared idempotency_keys table, so an agent key can never collide with the
// Idempotency-Key a REST client happens to choose.
const IdempotencyKeyPrefix = "mcp:"

// maxIdempotencyKeyLen is the longest accepted key (the REST header limit).
const maxIdempotencyKeyLen = 128

// The idempotency refusals; the tool boundary maps them to the same slugs the
// REST adapter answers (`idempotency-key-required` / `-invalid` / `-reused`).
var (
	ErrIdempotencyKeyRequired = errors.New("an idempotency key is required on every write")
	ErrIdempotencyKeyInvalid  = errors.New("the idempotency key must be 1 to 128 printable ASCII characters")
	ErrIdempotencyKeyReused   = errors.New("the idempotency key was already used with a different request")
)

// ReplayedFailure is a stored client-error answer replayed for a repeated key:
// the same refusal the first call got, with the slug and detail it had.
type ReplayedFailure struct {
	Slug   string
	Detail string
}

func (f *ReplayedFailure) Error() string { return f.Slug + ": " + f.Detail }

// Outcome is the answer of an idempotent run: the encoded body the work
// produced (or the stored one) and whether it is a replay.
type Outcome struct {
	Body     []byte
	Replayed bool
}

// Failure classifies a work error as a client error that is stored as the
// answer of the key (slug and detail), or reports ok=false for an error that
// is never stored (infrastructure, 5xx class): the same key may then retry.
type Failure func(err error) (slug, detail string, ok bool)

// Idempotency gives an agent write the REST Idempotency-Key semantics
// (adapters/inbound/http/idempotency.go, ADR 0008): the lookup, the work and
// the store of the answer run in ONE unit of work, so the aggregate, its
// outbox rows and the key commit or roll back together. Every answer below 5xx
// is stored, a client error included; the same key with another request is
// ErrIdempotencyKeyReused; racing calls with one key keep the first writer
// (the primary key blocks the loser, which re-reads, rolls its work back and
// replays the winner's answer).
type Idempotency struct {
	Store ports.IdempotencyStore
	UoW   ports.UnitOfWork
	Now   func() time.Time
}

const (
	statusStoredOK     = 200
	statusStoredFailed = 422
)

// errDiscard rolls the unit of work back without being a failure.
var errDiscard = errors.New("discard the work of this attempt")

// ValidateIdempotencyKey checks the key shape: 1-128 printable ASCII characters.
func ValidateIdempotencyKey(key string) error {
	if key == "" {
		return ErrIdempotencyKeyRequired
	}
	if len(key) > maxIdempotencyKeyLen {
		return ErrIdempotencyKeyInvalid
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x20 || key[i] > 0x7e {
			return ErrIdempotencyKeyInvalid
		}
	}
	return nil
}

// Fingerprint identifies a request: the tool name and the canonical JSON of its
// business arguments (the key and the attribution labels are not part of it,
// like REST's method, path and body).
func Fingerprint(tool string, args any) (string, error) {
	body, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("fingerprint %s: %w", tool, err)
	}
	sum := sha256.Sum256(append([]byte("MCP "+tool+"\n"), body...))
	return hex.EncodeToString(sum[:]), nil
}

// attempt is what one pass decided.
type attempt struct {
	stored  *repository.IdempotencyRecord // replay this
	body    []byte                        // or answer with this
	kept    bool                          // body was stored as the answer of the key
	failure error                         // or fail with this ...
	slug    string                        // ... (a client error not stored yet)
	detail  string
}

// Do runs work at most once for key and returns its encoded body.
func (ix Idempotency) Do(ctx context.Context, key, fingerprint string, work func(ctx context.Context) ([]byte, error), classify Failure) (Outcome, error) {
	if err := ValidateIdempotencyKey(key); err != nil {
		return Outcome{}, err
	}
	storeKey := IdempotencyKeyPrefix + key
	var res attempt
	err := ix.UoW.Do(ctx, func(ctx context.Context) error {
		return ix.joined(ctx, storeKey, fingerprint, work, classify, &res)
	})
	if err != nil && !errors.Is(err, errDiscard) {
		return Outcome{}, err
	}
	if res.failure != nil && res.stored == nil {
		ix.keepFailure(ctx, storeKey, fingerprint, &res)
	}
	return res.answer(fingerprint)
}

// joined is one pass inside the unit of work.
func (ix Idempotency) joined(ctx context.Context, key, fp string, work func(context.Context) ([]byte, error), classify Failure, res *attempt) error {
	rec, found, err := ix.Store.Lookup(ctx, key)
	if err != nil {
		return err
	}
	if found {
		res.stored = &rec
		return nil
	}
	body, err := work(ctx)
	if err != nil {
		slug, detail, client := classify(err)
		if !client {
			return err
		}
		res.failure, res.slug, res.detail = err, slug, detail
		return errDiscard
	}
	return ix.keep(ctx, ix.record(key, fp, statusStoredOK, body), res, body)
}

// keep stores rec as the answer of its key and re-reads the winner; when
// another call won, the pass replays it and discards our work.
func (ix Idempotency) keep(ctx context.Context, rec repository.IdempotencyRecord, res *attempt, body []byte) error {
	if err := ix.Store.Save(ctx, rec); err != nil {
		return err
	}
	got, found, err := ix.Store.Lookup(ctx, rec.Key)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("idempotency record has no readable outcome")
	}
	if !sameRecord(got, rec) {
		res.stored, res.failure = &got, nil
		return errDiscard
	}
	res.body, res.kept = body, true
	return nil
}

// keepFailure stores a client-error answer outside the discarded unit of work.
// A failure to store is logged, never surfaced: the caller still gets the
// truthful refusal.
func (ix Idempotency) keepFailure(ctx context.Context, key, fp string, res *attempt) {
	body, _ := json.Marshal(storedFailure{Slug: res.slug, Detail: res.detail}) // plain data: cannot fail
	err := ix.UoW.Do(ctx, func(ctx context.Context) error {
		return ix.keep(ctx, ix.record(key, fp, statusStoredFailed, body), res, nil)
	})
	if err != nil && !errors.Is(err, errDiscard) {
		slog.WarnContext(ctx, "idempotency: could not store a client-error answer", "error", err)
	}
}

type storedFailure struct {
	Slug   string `json:"slug"`
	Detail string `json:"detail"`
}

func (ix Idempotency) record(key, fp string, status int, body []byte) repository.IdempotencyRecord {
	return repository.IdempotencyRecord{
		Key: key, Fingerprint: fp, Status: status, ContentType: "application/json", Body: body,
		CreatedAt: ix.Now().UTC().Truncate(time.Microsecond),
	}
}

// answer turns the pass into the caller's result.
func (a *attempt) answer(fp string) (Outcome, error) {
	if a.stored != nil {
		return replay(a.stored, fp)
	}
	if a.failure != nil {
		return Outcome{}, a.failure
	}
	return Outcome{Body: a.body}, nil
}

// replay answers a stored record, or refuses when the key belongs to another request.
func replay(rec *repository.IdempotencyRecord, fp string) (Outcome, error) {
	if rec.Fingerprint != fp {
		return Outcome{}, ErrIdempotencyKeyReused
	}
	if rec.Status == statusStoredOK {
		return Outcome{Body: rec.Body, Replayed: true}, nil
	}
	var f storedFailure
	if err := json.Unmarshal(rec.Body, &f); err != nil || f.Slug == "" {
		return Outcome{}, errors.New("idempotency record has no readable outcome")
	}
	return Outcome{}, &ReplayedFailure{Slug: f.Slug, Detail: f.Detail}
}

// sameRecord reports whether two records are the very answer one call saved.
func sameRecord(a, b repository.IdempotencyRecord) bool {
	return a.Fingerprint == b.Fingerprint && a.Status == b.Status && bytes.Equal(a.Body, b.Body) && a.CreatedAt.Equal(b.CreatedAt)
}
