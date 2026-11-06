-- dock-scheduling analytics read model (ADR 0006).
--
-- This is the ANALYTICAL database (dock_scheduling_analytics), separate from the
-- OLTP database. It is written only by cmd/projector and read (read-only) by
-- cmd/reports. Everything here is a projection derived from
-- warehouse.dock-scheduling.analytics, not a source of truth: it can be dropped
-- and rebuilt by replaying the topic under a new consumer group.
--
-- Facts, not aggregates: one row per appointment, one column per milestone. Each
-- milestone is kept with LEAST/COALESCE, so arrival order never matters.

-- Idempotency: every applied CloudEvents id is recorded here exactly once, in
-- the SAME transaction as its effect. occurred_at is the event's CloudEvents
-- `time`; max(occurred_at) is the freshness of the projection.
CREATE TABLE analytics_processed_events (
    event_id    TEXT        PRIMARY KEY,
    event_type  TEXT        NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_analytics_processed_events_occurred_at ON analytics_processed_events (occurred_at);

-- appointment_facts: one row per dock appointment.
--   window_*        the CURRENT window (Booked, then Rescheduled); window_version
--                   is the aggregate version that wrote it, so a replayed or
--                   reordered older event never overwrites a newer window.
--   arrival_status  from DockAppointmentCheckedIn (EARLY, ON_TIME, LATE)
--   completed_at    from DockAppointmentCompleted. A completion with NO
--                   CheckedIn is an implicit arrival (X6): ON_TIME.
--   no_show_at      from DockAppointmentNoShow
--
-- outcome is THE adherence classification, declared once and computed by the
-- database: an arrival counts as its status, an implicit arrival (completed
-- without a check-in) as ON_TIME, a no-show as NO_SHOW; an appointment that was
-- only booked, or was cancelled before anyone arrived, has no outcome and is not
-- counted. ref_at is the instant the report range applies to: the (current)
-- window start.
CREATE TABLE appointment_facts (
    appointment_id      TEXT COLLATE "C" PRIMARY KEY,
    site_id             TEXT COLLATE "C" NOT NULL,
    carrier_scac        TEXT COLLATE "C",
    window_start        TIMESTAMPTZ,
    window_end          TIMESTAMPTZ,
    window_version      BIGINT      NOT NULL DEFAULT 0,
    booked_at           TIMESTAMPTZ,
    checked_in_at       TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    cancelled_at        TIMESTAMPTZ,
    no_show_at          TIMESTAMPTZ,
    arrival_status      TEXT        CHECK (arrival_status IN ('EARLY', 'ON_TIME', 'LATE')),
    cancellation_reason TEXT,
    outcome             TEXT GENERATED ALWAYS AS (
        CASE
            WHEN arrival_status IS NOT NULL THEN arrival_status
            WHEN completed_at   IS NOT NULL THEN 'ON_TIME'
            WHEN no_show_at     IS NOT NULL THEN 'NO_SHOW'
        END
    ) STORED,
    ref_at              TIMESTAMPTZ GENERATED ALWAYS AS (
        COALESCE(window_start, window_end, checked_in_at, completed_at)
    ) STORED
);

-- The report: counted appointments in a range, grouped by site and carrier.
CREATE INDEX idx_appointment_facts_report ON appointment_facts (ref_at, site_id) WHERE outcome IS NOT NULL;
