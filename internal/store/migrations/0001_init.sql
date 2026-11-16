-- Netsession Relay schema v1 (docs/02-plan.md 5.1; deviations in docs/01-entscheidungen.md, phase 4).

CREATE TABLE game (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    category         TEXT NOT NULL,
    relay_needed     TEXT NOT NULL,
    default_enabled  INTEGER NOT NULL DEFAULT 0,
    confidence       TEXT NOT NULL,
    origin           TEXT NOT NULL CHECK (origin IN ('seed', 'custom', 'import', 'sniffer')),
    seed_hash        TEXT NOT NULL DEFAULT '',   -- hash of the seed version this row is based on
    seed_pending     TEXT NOT NULL DEFAULT '',   -- newer seed version not applied (row modified locally)
    locally_modified INTEGER NOT NULL DEFAULT 0,
    data             TEXT NOT NULL               -- full gamedb.Game as JSON (discovery specs, ports, notes)
);

CREATE TABLE vlan (
    vid        INTEGER PRIMARY KEY CHECK (vid BETWEEN 1 AND 4094),
    name       TEXT NOT NULL,
    grp        TEXT NOT NULL,
    cidr       TEXT NOT NULL,
    relay_ip   TEXT NOT NULL DEFAULT '',
    attach     TEXT NOT NULL CHECK (attach IN ('trunk', 'device')),
    parent     TEXT NOT NULL DEFAULT '',
    device     TEXT NOT NULL DEFAULT '',
    rate_pps   INTEGER NOT NULL DEFAULT 0,
    rate_burst INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE flow_profile (
    name       TEXT PRIMARY KEY,
    is_default INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE flow_profile_edge (
    profile TEXT NOT NULL REFERENCES flow_profile(name) ON DELETE CASCADE ON UPDATE CASCADE,
    src     TEXT NOT NULL,
    dst     TEXT NOT NULL,
    PRIMARY KEY (profile, src, dst)
);

CREATE TABLE event (
    id        INTEGER PRIMARY KEY,
    name      TEXT NOT NULL UNIQUE,
    is_active INTEGER NOT NULL DEFAULT 0,
    notes     TEXT NOT NULL DEFAULT '',
    created   TEXT NOT NULL
);
CREATE UNIQUE INDEX event_one_active ON event(is_active) WHERE is_active = 1;

-- VLANs used by an event (number of tables changes per event)
CREATE TABLE event_vlan (
    event_id INTEGER NOT NULL REFERENCES event(id) ON DELETE CASCADE,
    vid      INTEGER NOT NULL REFERENCES vlan(vid) ON DELETE CASCADE,
    enabled  INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (event_id, vid)
);

-- games switched on in an event, with matrix and overrides
CREATE TABLE event_game (
    event_id     INTEGER NOT NULL REFERENCES event(id) ON DELETE CASCADE,
    game_id      TEXT NOT NULL REFERENCES game(id) ON DELETE CASCADE,
    flow_profile TEXT NOT NULL DEFAULT '',
    override     TEXT NOT NULL DEFAULT '{}',
    PRIMARY KEY (event_id, game_id)
);

CREATE TABLE setting (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- every applied state; config = full configuration export (rollback restores it)
CREATE TABLE config_revision (
    id             INTEGER PRIMARY KEY,
    ts             TEXT NOT NULL,
    user           TEXT NOT NULL,
    note           TEXT NOT NULL DEFAULT '',
    hash           TEXT NOT NULL,
    snapshot       TEXT NOT NULL,
    config         TEXT NOT NULL,
    warnings       TEXT NOT NULL DEFAULT '[]',
    applied_ok     INTEGER NOT NULL,
    engine_message TEXT NOT NULL DEFAULT ''
);

-- append only
CREATE TABLE audit_log (
    id     INTEGER PRIMARY KEY,
    ts     TEXT NOT NULL,
    user   TEXT NOT NULL,
    action TEXT NOT NULL,
    object TEXT NOT NULL DEFAULT '',
    before TEXT NOT NULL DEFAULT '',
    after  TEXT NOT NULL DEFAULT ''
);
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log BEGIN SELECT RAISE(ABORT, 'audit_log is append-only'); END;
