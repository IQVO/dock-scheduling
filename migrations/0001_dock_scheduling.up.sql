-- 0001_dock_scheduling.up.sql: dock-scheduling's OLTP schema (ADR 0002 aggregates,
-- ADR 0003 local copies, ADR 0004 facts).
--
-- Text keys that are compared or ordered in Go (ids, codes) use the "C"
-- collation so ORDER BY follows byte order, the same order the in-memory
-- adapter uses.

-- btree_gist lets the door exclusion constraint combine text equality with
-- range overlap.
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- dock_appointments: one row per DockAppointment aggregate (the whole snapshot).
-- version starts at 1, grows by exactly one per accepted change and guards
-- every UPDATE.
CREATE TABLE dock_appointments (
    appointment_id      TEXT COLLATE "C" PRIMARY KEY,
    site_id             TEXT COLLATE "C" NOT NULL,
    door_code           TEXT COLLATE "C" NOT NULL,
    direction           TEXT        NOT NULL CHECK (direction IN ('INBOUND', 'OUTBOUND')),
    carrier             TEXT        NOT NULL,
    carrier_scac        TEXT COLLATE "C" NOT NULL DEFAULT '',
    window_start        TIMESTAMPTZ NOT NULL,
    window_end          TIMESTAMPTZ NOT NULL,
    asn_numbers         TEXT[]      NOT NULL DEFAULT '{}',
    cpt_id              TEXT COLLATE "C" NOT NULL DEFAULT '',
    shipment_refs       TEXT[]      NOT NULL DEFAULT '{}',
    expected_seal       TEXT        NOT NULL DEFAULT '',
    provenance          TEXT        NOT NULL CHECK (provenance IN ('native', 'legacy-import')),
    -- door_override: the physical fact won (ActualDoorWins); the row is
    -- exempt from the overlap backstop on its door.
    door_override       BOOLEAN     NOT NULL DEFAULT false,
    state               TEXT        NOT NULL CHECK (state IN ('BOOKED', 'CHECKED_IN', 'COMPLETED', 'CANCELLED', 'NO_SHOW')),
    version             BIGINT      NOT NULL CHECK (version >= 1),
    eta_at              TIMESTAMPTZ,
    eta_reported_at     TIMESTAMPTZ,
    eta_source          TEXT,
    arrival_at          TIMESTAMPTZ,
    arrival_status      TEXT        CHECK (arrival_status IN ('EARLY', 'ON_TIME', 'LATE')),
    arrival_visit_id    TEXT        NOT NULL DEFAULT '',
    arrival_implicit    BOOLEAN     NOT NULL DEFAULT false,
    at_risk_cause       TEXT        NOT NULL DEFAULT '' CHECK (at_risk_cause IN ('', 'LATE_ETA', 'DOOR_REASSIGNED_OVERLAP')),
    completed_at        TIMESTAMPTZ,
    cancellation_reason TEXT        NOT NULL DEFAULT '' CHECK (cancellation_reason IN ('', 'CARRIER_REQUEST', 'SITE_REQUEST', 'ASN_CANCELLED', 'TURNED_AWAY')),
    created_at          TIMESTAMPTZ NOT NULL,
    updated_at          TIMESTAMPTZ NOT NULL,
    CHECK (window_end > window_start),
    CHECK ((eta_at IS NULL) = (eta_source IS NULL)),
    CHECK ((arrival_at IS NULL) = (arrival_status IS NULL))
);

-- Invariant (ADR 0002): no two active (BOOKED or CHECKED_IN) appointments
-- overlap on one door; windows are half-open, so [10:00,12:00) and
-- [12:00,14:00) are compatible. The domain service DoorSchedule.CheckNoOverlap
-- decides under a per-door advisory lock; this constraint is the DB backstop
-- the losing writer of a race fails on (storage error 23P01 ->
-- ErrDoorDoubleBooked). Rows whose door was set by a physical fact
-- (door_override) are exempt (the physical fact wins), and so are legacy
-- imports (D-2: ImportLegacyAppointment enforces no overlap).
ALTER TABLE dock_appointments ADD CONSTRAINT ex_dock_appointments_door_window
    EXCLUDE USING gist (door_code WITH =, tstzrange(window_start, window_end, '[)') WITH &&)
    WHERE (state IN ('BOOKED', 'CHECKED_IN') AND NOT door_override AND provenance = 'native');

-- Listing (window start, id) keyset order and the per-site / per-door scans.
CREATE INDEX idx_dock_appointments_start ON dock_appointments (window_start, appointment_id);
CREATE INDEX idx_dock_appointments_site ON dock_appointments (site_id, window_start, appointment_id);
CREATE INDEX idx_dock_appointments_door_active ON dock_appointments (door_code, window_start)
    WHERE state IN ('BOOKED', 'CHECKED_IN');
-- NoShowSweep: BOOKED appointments by window end.
CREATE INDEX idx_dock_appointments_booked_end ON dock_appointments (window_end, window_start, appointment_id)
    WHERE state = 'BOOKED';
CREATE INDEX idx_dock_appointments_at_risk ON dock_appointments (site_id, window_start, appointment_id)
    WHERE at_risk_cause <> '';
-- CancelWhenAllAsnsCancelled: BOOKED INBOUND appointments covering an ASN.
CREATE INDEX idx_dock_appointments_asns ON dock_appointments USING gin (asn_numbers);

-- asn_claims: an ASN may sit on only ONE active native appointment (D-12,
-- ErrAsnAlreadyScheduled). The repository keeps one row per ASN of every
-- active native appointment in the same transaction as the appointment row;
-- the primary key is the backstop (unique violation 23505 ->
-- ErrAsnAlreadyScheduled). Legacy imports enforce none of it (D-2).
CREATE TABLE asn_claims (
    asn_number     TEXT COLLATE "C" PRIMARY KEY,
    appointment_id TEXT COLLATE "C" NOT NULL REFERENCES dock_appointments (appointment_id) ON DELETE CASCADE
);

CREATE INDEX idx_asn_claims_appointment ON asn_claims (appointment_id);

-- dock_calendars: one row per DockCalendar aggregate (one per site), created
-- lazily with the contracts-delta defaults; version guards every UPDATE.
CREATE TABLE dock_calendars (
    site_id                   TEXT COLLATE "C" PRIMARY KEY,
    max_window_minutes        INTEGER NOT NULL CHECK (max_window_minutes >= 1),
    slot_granularity_minutes  INTEGER NOT NULL CHECK (slot_granularity_minutes >= 1),
    arrival_tolerance_minutes INTEGER NOT NULL CHECK (arrival_tolerance_minutes >= 0),
    no_show_grace_minutes     INTEGER NOT NULL CHECK (no_show_grace_minutes >= 0),
    version                   BIGINT  NOT NULL CHECK (version >= 1)
);

-- calendar_operating_hours: the operating-hours rows of a site. door_code ''
-- is the site default. position keeps the declared order.
CREATE TABLE calendar_operating_hours (
    site_id   TEXT COLLATE "C" NOT NULL REFERENCES dock_calendars (site_id) ON DELETE CASCADE,
    door_code TEXT COLLATE "C" NOT NULL DEFAULT '',
    weekday   TEXT NOT NULL CHECK (weekday IN ('MON', 'TUE', 'WED', 'THU', 'FRI', 'SAT', 'SUN')),
    opens_at  TEXT NOT NULL,
    closes_at TEXT NOT NULL,
    timezone  TEXT NOT NULL,
    position  INTEGER NOT NULL,
    PRIMARY KEY (site_id, door_code, weekday)
);

-- calendar_closures: the closures of a site, lifted ones kept for history.
CREATE TABLE calendar_closures (
    site_id    TEXT COLLATE "C" NOT NULL REFERENCES dock_calendars (site_id) ON DELETE CASCADE,
    closure_id TEXT COLLATE "C" NOT NULL,
    door_code  TEXT COLLATE "C" NOT NULL,
    from_at    TIMESTAMPTZ NOT NULL,
    to_at      TIMESTAMPTZ NOT NULL,
    reason     TEXT        NOT NULL,
    lifted     BOOLEAN     NOT NULL DEFAULT false,
    position   INTEGER     NOT NULL,
    PRIMARY KEY (site_id, closure_id),
    CHECK (to_at > from_at)
);

-- Local copies (ADR 0003). zones and doors are fed by facility-layout:
-- ZoneRegistered(zoneId -> siteCode) and LocationSlotRegistered (role Dock).
-- A door whose zone has not arrived is held pending (no join row, hence not
-- bookable) and resolves when the zone arrives (X5).
CREATE TABLE zones (
    zone_id   TEXT COLLATE "C" PRIMARY KEY,
    site_code TEXT COLLATE "C" NOT NULL
);

CREATE TABLE doors (
    door_code TEXT COLLATE "C" PRIMARY KEY,
    zone_id   TEXT COLLATE "C" NOT NULL,
    dock_flow TEXT NOT NULL CHECK (dock_flow IN ('Inbound', 'Outbound', 'Both'))
);

CREATE INDEX idx_doors_zone ON doors (zone_id);

-- asns: inbound-receiving's ASNRegistered / ASNCancelled. A cancelled ASN
-- stays cancelled (a late ASNRegistered does not revive it).
CREATE TABLE asns (
    asn_number       TEXT COLLATE "C" PRIMARY KEY,
    status           TEXT        NOT NULL CHECK (status IN ('REGISTERED', 'CANCELLED')),
    expected_arrival TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- cutoff_schedules / cutoffs: process-path-management's CPTScheduleChanged
-- (the whole schedule of a site replaces the previous one).
CREATE TABLE cutoff_schedules (
    site_id  TEXT COLLATE "C" PRIMARY KEY,
    timezone TEXT NOT NULL
);

CREATE TABLE cutoffs (
    site_id      TEXT COLLATE "C" NOT NULL REFERENCES cutoff_schedules (site_id) ON DELETE CASCADE,
    cpt_id       TEXT COLLATE "C" NOT NULL,
    local_time   TEXT NOT NULL DEFAULT '',
    days_of_week TEXT[] NOT NULL DEFAULT '{}',
    ship_method  TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (site_id, cpt_id)
);

-- processed_events: the inbound consumers' dedupe guard (fleet rule: the
-- CloudEvents id is claimed in the SAME transaction as the effect, so a
-- rollback un-claims it).
CREATE TABLE processed_events (
    event_id     TEXT COLLATE "C" PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- outbox: the transactional outbox. One row per DOMAIN fact, inserted in the
-- SAME transaction as the aggregate's Save; the relay drains rows, encodes
-- them as CloudEvents 1.0 and produces to Kafka.
--   event_id  the CloudEvents id (UUID v4), minted ONCE at enqueue and
--             persisted: a relay retry republishes the same id.
--   payload   the fact envelope {event, aggregate_id, occurred_at, data} with
--             data already in the wire shape of ADR 0004.
--   attempts / next_attempt_at  the retry state; rows at the attempt cap stay
--             here until an operator re-arms them (attempts = 0).
CREATE TABLE outbox (
    id              BIGSERIAL   PRIMARY KEY,
    event_id        TEXT COLLATE "C" NOT NULL UNIQUE,
    aggreg_type     TEXT COLLATE "C" NOT NULL,
    aggreg_id       TEXT COLLATE "C" NOT NULL,
    payload         JSONB       NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_outbox_attempts_next_attempt ON outbox (attempts, next_attempt_at);
