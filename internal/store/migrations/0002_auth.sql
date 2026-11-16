-- Accounts and server-side sessions (docs/02-plan.md 6.5). OIDC accounts follow in phase 9.

CREATE TABLE user (
    id            INTEGER PRIMARY KEY,
    kind          TEXT NOT NULL CHECK (kind IN ('local', 'oidc')),
    name          TEXT NOT NULL UNIQUE,
    role          TEXT NOT NULL CHECK (role IN ('viewer', 'operator', 'admin')),
    password_hash TEXT NOT NULL DEFAULT '',
    failed        INTEGER NOT NULL DEFAULT 0,
    locked_until  TEXT NOT NULL DEFAULT '',
    last_login    TEXT NOT NULL DEFAULT ''
);

CREATE TABLE session (
    id      TEXT PRIMARY KEY,           -- SHA-256 of the cookie value
    user_id INTEGER NOT NULL REFERENCES user(id) ON DELETE CASCADE,
    csrf    TEXT NOT NULL,
    created TEXT NOT NULL,
    expires TEXT NOT NULL,
    ip      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX session_expires ON session(expires);
