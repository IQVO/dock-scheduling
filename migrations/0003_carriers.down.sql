-- 0003_carriers.down.sql: recreate the previous schema (0002) exactly.
DROP INDEX IF EXISTS idx_carriers_status_scac;
DROP TABLE IF EXISTS carriers;
