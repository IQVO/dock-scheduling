-- 0001_schedule_adherence.down.sql: recreate the previous (empty) schema exactly.
DROP INDEX IF EXISTS idx_appointment_facts_report;
DROP TABLE IF EXISTS appointment_facts;
DROP INDEX IF EXISTS idx_analytics_processed_events_occurred_at;
DROP TABLE IF EXISTS analytics_processed_events;
