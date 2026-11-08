-- 0002_idempotency_keys.up.sql: the answers of POSTs and PUTs made with an
-- Idempotency-Key (the REST adapter's middleware, the fleet table pattern).
-- Only 2xx and 4xx answers are stored, never 5xx; the first writer of a key
-- wins (the primary key plus INSERT ... ON CONFLICT DO NOTHING). fingerprint
-- identifies the request (method, path, body) so the same key with another
-- request can be told apart (409 idempotency-key-reused).
CREATE TABLE idempotency_keys (
    key          TEXT COLLATE "C" PRIMARY KEY,
    fingerprint  TEXT        NOT NULL,
    status       INTEGER     NOT NULL CHECK (status >= 200 AND status < 500),
    content_type TEXT        NOT NULL DEFAULT '',
    body         BYTEA       NOT NULL DEFAULT ''::bytea,
    created_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys (created_at);
