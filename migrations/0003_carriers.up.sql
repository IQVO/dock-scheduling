-- 0003_carriers.up.sql: the carrier registry (ADR 0012). Additive: one new
-- table, nothing existing is touched, so an older binary keeps working against
-- the migrated database.
--
-- carriers: one row per Carrier aggregate, keyed by SCAC (the Standard Carrier
-- Alpha Code NMFTA assigns). usdot (FMCSA, up to 8 digits) and mc (MC or MX
-- operating authority, canonical with its prefix) are '' when unknown. version
-- starts at 1, grows by exactly one per accepted change and guards every UPDATE.
-- A booking that names a carrier_scac looks the row up; no row means the
-- carrier is not in the registry and the booking is accepted as before.
CREATE TABLE carriers (
    scac              TEXT COLLATE "C" PRIMARY KEY CHECK (scac ~ '^[A-Z]{2,4}$'),
    name              TEXT        NOT NULL CHECK (btrim(name) <> ''),
    usdot             TEXT COLLATE "C" NOT NULL DEFAULT '' CHECK (usdot ~ '^[0-9]{0,8}$'),
    mc                TEXT COLLATE "C" NOT NULL DEFAULT '' CHECK (mc ~ '^((MC|MX)[0-9]{1,8})?$'),
    status            TEXT        NOT NULL CHECK (status IN ('ACTIVE', 'SUSPENDED')),
    suspension_reason TEXT        NOT NULL DEFAULT '',
    suspended_at      TIMESTAMPTZ,
    version           BIGINT      NOT NULL CHECK (version >= 1),
    created_at        TIMESTAMPTZ NOT NULL,
    updated_at        TIMESTAMPTZ NOT NULL,
    -- A suspended carrier has a reason and a time; an active one has neither.
    CHECK ((status = 'SUSPENDED') = (suspended_at IS NOT NULL)),
    CHECK (status = 'SUSPENDED' OR suspension_reason = '')
);

-- The status filter of GET /carriers pages by SCAC within a status.
CREATE INDEX idx_carriers_status_scac ON carriers (status, scac);
