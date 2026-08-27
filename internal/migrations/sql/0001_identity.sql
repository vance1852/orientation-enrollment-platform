CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    email         TEXT    NOT NULL,
    display_name  TEXT    NOT NULL,
    role          TEXT    NOT NULL CHECK (role IN ('student', 'registrar')),
    password_hash TEXT    NOT NULL,
    disabled      INTEGER NOT NULL DEFAULT 0 CHECK (disabled IN (0, 1)),
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL
);

CREATE UNIQUE INDEX ux_users_email ON users (email);
CREATE INDEX ix_users_role ON users (role);

CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_digest TEXT    NOT NULL,
    issued_at    TEXT    NOT NULL,
    expires_at   TEXT    NOT NULL,
    revoked_at   TEXT,
    last_seen_at TEXT    NOT NULL,
    user_agent   TEXT    NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX ux_sessions_token_digest ON sessions (token_digest);
CREATE INDEX ix_sessions_user_active ON sessions (user_id, revoked_at, expires_at);
CREATE INDEX ix_sessions_expires_at ON sessions (expires_at);
