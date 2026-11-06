-- 0002_idempotency_keys.down.sql: recreate the previous schema exactly.
DROP INDEX IF EXISTS idx_idempotency_keys_created_at;
DROP TABLE IF EXISTS idempotency_keys;
