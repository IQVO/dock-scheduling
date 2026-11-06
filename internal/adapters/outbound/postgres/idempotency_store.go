package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/claudioed/dock-scheduling/internal/application/repository"
)

// IdempotencyStore is the pgxpool-backed ports.IdempotencyStore over the
// idempotency_keys table (migration 0002). Every statement runs on the
// transaction carried by ctx when one is open (the Idempotency-Key
// middleware's unit of work) and on the pool otherwise.
type IdempotencyStore struct {
	pool *pgxpool.Pool
}

// NewIdempotencyStore constructs an IdempotencyStore over pool.
func NewIdempotencyStore(pool *pgxpool.Pool) *IdempotencyStore {
	return &IdempotencyStore{pool: pool}
}

// Lookup implements ports.IdempotencyStore.
func (s *IdempotencyStore) Lookup(ctx context.Context, key string) (repository.IdempotencyRecord, bool, error) {
	rec := repository.IdempotencyRecord{Key: key}
	err := queryFor(ctx, s.pool).QueryRow(ctx, `
		SELECT fingerprint, status, content_type, body, created_at
		FROM idempotency_keys WHERE key = $1
	`, key).Scan(&rec.Fingerprint, &rec.Status, &rec.ContentType, &rec.Body, &rec.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return repository.IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return repository.IdempotencyRecord{}, false, fmt.Errorf("lookup idempotency key: %w", err)
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	return rec, true, nil
}

// Save implements ports.IdempotencyStore: the first writer of a key wins and
// a later Save of the same key changes nothing. Under Postgres a concurrent
// Save of the same key blocks on the primary key until the other transaction
// ends, so the middleware's post-save re-read sees the committed winner.
func (s *IdempotencyStore) Save(ctx context.Context, rec repository.IdempotencyRecord) error {
	_, err := queryFor(ctx, s.pool).Exec(ctx, `
		INSERT INTO idempotency_keys (key, fingerprint, status, content_type, body, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (key) DO NOTHING
	`, rec.Key, rec.Fingerprint, rec.Status, rec.ContentType, nonNilBytes(rec.Body), rec.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("save idempotency key: %w", err)
	}
	return nil
}

// nonNilBytes keeps a nil body from becoming SQL NULL (the column is NOT NULL).
func nonNilBytes(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}
