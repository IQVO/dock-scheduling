-- 0001_dock_scheduling.down.sql: recreates the previous schema (empty) exactly.

DROP INDEX IF EXISTS idx_outbox_attempts_next_attempt;
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS processed_events;
DROP TABLE IF EXISTS cutoffs;
DROP TABLE IF EXISTS cutoff_schedules;
DROP TABLE IF EXISTS asns;
DROP INDEX IF EXISTS idx_doors_zone;
DROP TABLE IF EXISTS doors;
DROP TABLE IF EXISTS zones;
DROP TABLE IF EXISTS calendar_closures;
DROP TABLE IF EXISTS calendar_operating_hours;
DROP TABLE IF EXISTS dock_calendars;
DROP INDEX IF EXISTS idx_asn_claims_appointment;
DROP TABLE IF EXISTS asn_claims;
DROP INDEX IF EXISTS idx_dock_appointments_asns;
DROP INDEX IF EXISTS idx_dock_appointments_at_risk;
DROP INDEX IF EXISTS idx_dock_appointments_booked_end;
DROP INDEX IF EXISTS idx_dock_appointments_door_active;
DROP INDEX IF EXISTS idx_dock_appointments_site;
DROP INDEX IF EXISTS idx_dock_appointments_start;
ALTER TABLE IF EXISTS dock_appointments DROP CONSTRAINT IF EXISTS ex_dock_appointments_door_window;
DROP TABLE IF EXISTS dock_appointments;
-- btree_gist is left installed: it is a harmless, shared extension that other
-- schemas of the database may rely on.
