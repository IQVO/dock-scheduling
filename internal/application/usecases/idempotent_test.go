package usecases_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/claudioed/dock-scheduling/internal/adapters/outbound/memory"
	"github.com/claudioed/dock-scheduling/internal/application/ports"
	"github.com/claudioed/dock-scheduling/internal/application/repository"
	"github.com/claudioed/dock-scheduling/internal/application/usecases"
)

var (
	errClient = errors.New("a typed refusal")
	errServer = errors.New("the database is down")
)

func classifyClient(err error) (string, string, bool) {
	if errors.Is(err, errClient) {
		return "typed-refusal", err.Error(), true
	}
	return "", "", false
}

type idemFixture struct {
	store *memory.Store
	idem  usecases.Idempotency
	runs  int
}

func newIdemFixture() *idemFixture {
	s := memory.NewStore()
	return &idemFixture{store: s, idem: usecases.Idempotency{Store: s.Idempotency(), UoW: s, Now: func() time.Time { return t0 }}}
}

func (f *idemFixture) do(key, fp string, work func(ctx context.Context) ([]byte, error)) (usecases.Outcome, error) {
	return f.idem.Do(context.Background(), key, fp, func(ctx context.Context) ([]byte, error) {
		f.runs++
		return work(ctx)
	}, classifyClient)
}

func ok(body string) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) { return []byte(body), nil }
}

func TestIdempotencyRunsOnceAndReplays(t *testing.T) {
	f := newIdemFixture()
	first, err := f.do("k", "fp", ok(`{"n":1}`))
	if err != nil || first.Replayed || string(first.Body) != `{"n":1}` {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := f.do("k", "fp", ok(`{"n":2}`))
	if err != nil || !second.Replayed || string(second.Body) != `{"n":1}` {
		t.Fatalf("second = %+v, %v, want the stored first answer", second, err)
	}
	if f.runs != 1 {
		t.Fatalf("work ran %d times, want 1", f.runs)
	}
}

func TestIdempotencyKeyShape(t *testing.T) {
	f := newIdemFixture()
	for key, want := range map[string]error{
		"":                            usecases.ErrIdempotencyKeyRequired,
		"ok-key_1.:":                  nil,
		strings.Repeat("k", 128):      nil,
		strings.Repeat("k", 129):      usecases.ErrIdempotencyKeyInvalid,
		"two words\u00e9":             usecases.ErrIdempotencyKeyInvalid,
		"tab\there":                   usecases.ErrIdempotencyKeyInvalid,
		"del\x7f":                     usecases.ErrIdempotencyKeyInvalid,
		"space is printable ascii":    nil,
		"\x1fcontrol-at-the-edge":     usecases.ErrIdempotencyKeyInvalid,
		"tilde~is the last printable": nil,
	} {
		_, err := f.do(key, "fp", ok(`{}`))
		if want == nil {
			if err != nil {
				t.Errorf("key %q: err = %v, want it accepted", key, err)
			}
			continue
		}
		if !errors.Is(err, want) {
			t.Errorf("key %q: err = %v, want %v", key, err, want)
		}
	}
}

func TestIdempotencyKeyReusedWithAnotherRequest(t *testing.T) {
	f := newIdemFixture()
	if _, err := f.do("k", "fp-1", ok(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.do("k", "fp-2", ok(`{}`)); !errors.Is(err, usecases.ErrIdempotencyKeyReused) {
		t.Fatalf("err = %v, want ErrIdempotencyKeyReused", err)
	}
	if f.runs != 1 {
		t.Fatalf("work ran %d times", f.runs)
	}
}

func TestIdempotencyStoresClientErrorsAndReplaysThem(t *testing.T) {
	f := newIdemFixture()
	_, err := f.do("k", "fp", func(context.Context) ([]byte, error) { return nil, errClient })
	if !errors.Is(err, errClient) {
		t.Fatalf("first err = %v, want the original error", err)
	}
	// Even a work that would now succeed is not run: the key has its answer.
	_, err = f.do("k", "fp", ok(`{}`))
	var replayed *usecases.ReplayedFailure
	if !errors.As(err, &replayed) || replayed.Slug != "typed-refusal" || replayed.Detail != errClient.Error() {
		t.Fatalf("second err = %v, want the stored refusal", err)
	}
	if replayed.Error() != "typed-refusal: "+errClient.Error() {
		t.Errorf("Error() = %q", replayed.Error())
	}
	if f.runs != 1 {
		t.Fatalf("work ran %d times, want 1", f.runs)
	}
	if _, err := f.do("k", "other", ok(`{}`)); !errors.Is(err, usecases.ErrIdempotencyKeyReused) {
		t.Fatalf("other request err = %v", err)
	}
}

func TestIdempotencyNeverStoresOtherErrors(t *testing.T) {
	f := newIdemFixture()
	if _, err := f.do("k", "fp", func(context.Context) ([]byte, error) { return nil, errServer }); !errors.Is(err, errServer) {
		t.Fatalf("err = %v", err)
	}
	out, err := f.do("k", "fp", ok(`{"retried":true}`))
	if err != nil || out.Replayed || string(out.Body) != `{"retried":true}` {
		t.Fatalf("retry = %+v, %v: an unstored failure lets the same key retry the real work", out, err)
	}
}

func TestIdempotencyRollsBackTheWorkOfARefusedCall(t *testing.T) {
	f := newIdemFixture()
	_, err := f.do("k", "fp", func(ctx context.Context) ([]byte, error) {
		if _, err := f.store.Claim(ctx, "evt-1"); err != nil { // a side effect inside the unit of work
			return nil, err
		}
		return nil, errClient
	})
	if !errors.Is(err, errClient) {
		t.Fatalf("err = %v", err)
	}
	if first, _ := f.store.Claim(context.Background(), "evt-1"); !first {
		t.Fatal("the refused call's side effect survived: the unit of work was not rolled back")
	}
}

func TestIdempotencyCommitsTheWorkWithTheKey(t *testing.T) {
	f := newIdemFixture()
	if _, err := f.do("k", "fp", func(ctx context.Context) ([]byte, error) {
		_, err := f.store.Claim(ctx, "evt-2")
		return []byte(`{}`), err
	}); err != nil {
		t.Fatal(err)
	}
	if first, _ := f.store.Claim(context.Background(), "evt-2"); first {
		t.Fatal("the successful call's side effect was lost")
	}
}

// racingStore lets exactly one writer win a key, like the primary key does: the
// loser's Save is a no-op, so its re-read finds the winner's different record.
type racingStore struct {
	ports.IdempotencyStore
	rival repository.IdempotencyRecord
}

func (r racingStore) Lookup(ctx context.Context, key string) (repository.IdempotencyRecord, bool, error) {
	rec, found, err := r.IdempotencyStore.Lookup(ctx, key)
	return rec, found, err
}

func (r racingStore) Save(ctx context.Context, rec repository.IdempotencyRecord) error {
	// The rival committed first.
	r.rival.Key = rec.Key
	return r.IdempotencyStore.Save(ctx, r.rival)
}

func TestIdempotencyLoserOfARaceReplaysTheWinner(t *testing.T) {
	s := memory.NewStore()
	rival := repository.IdempotencyRecord{Fingerprint: "fp", Status: 200, ContentType: "application/json", Body: []byte(`{"winner":true}`), CreatedAt: t0.Add(time.Second)}
	idem := usecases.Idempotency{Store: racingStore{IdempotencyStore: s.Idempotency(), rival: rival}, UoW: s, Now: func() time.Time { return t0 }}
	out, err := idem.Do(context.Background(), "k", "fp", func(ctx context.Context) ([]byte, error) {
		_, err := s.Claim(ctx, "evt-3")
		return []byte(`{"winner":false}`), err
	}, classifyClient)
	if err != nil || !out.Replayed || string(out.Body) != `{"winner":true}` {
		t.Fatalf("loser = %+v, %v, want the winner's answer", out, err)
	}
	if first, _ := s.Claim(context.Background(), "evt-3"); !first {
		t.Fatal("the loser's work was not rolled back")
	}
}

func TestIdempotencyConcurrentCallsWithOneKeyRunOnce(t *testing.T) {
	f := newIdemFixture()
	var wg sync.WaitGroup
	results := make([]usecases.Outcome, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = f.do("k", "fp", ok(`{"n":1}`))
		}()
	}
	wg.Wait()
	replays := 0
	for _, r := range results {
		if string(r.Body) != `{"n":1}` {
			t.Fatalf("outcome = %+v", r)
		}
		if r.Replayed {
			replays++
		}
	}
	if f.runs != 1 || replays != len(results)-1 {
		t.Fatalf("runs = %d, replays = %d, want 1 and %d", f.runs, replays, len(results)-1)
	}
}

func TestFingerprint(t *testing.T) {
	type args struct {
		A string
		B int
	}
	a, err := usecases.Fingerprint("book", args{"x", 1})
	if err != nil {
		t.Fatal(err)
	}
	same, _ := usecases.Fingerprint("book", args{"x", 1})
	otherArgs, _ := usecases.Fingerprint("book", args{"x", 2})
	otherTool, _ := usecases.Fingerprint("cancel", args{"x", 1})
	if a != same || a == otherArgs || a == otherTool || len(a) != 64 {
		t.Fatalf("fingerprints: %s %s %s %s", a, same, otherArgs, otherTool)
	}
	if _, err := usecases.Fingerprint("book", func() {}); err == nil {
		t.Fatal("an unencodable argument must be an error")
	}
}

func TestIdempotencyKeysAreNamespaced(t *testing.T) {
	f := newIdemFixture()
	if _, err := f.do("k", "fp", ok(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := f.store.Idempotency().Lookup(context.Background(), "k"); found {
		t.Error("an agent key must not occupy the REST key space")
	}
	if _, found, _ := f.store.Idempotency().Lookup(context.Background(), usecases.IdempotencyKeyPrefix+"k"); !found {
		t.Error("the agent key is stored under the mcp: prefix")
	}
}
