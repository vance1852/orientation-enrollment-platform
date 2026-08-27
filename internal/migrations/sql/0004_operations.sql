CREATE TABLE idempotency_keys (
    actor_user_id       INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    method              TEXT    NOT NULL,
    path                TEXT    NOT NULL,
    key                 TEXT    NOT NULL,
    request_fingerprint TEXT    NOT NULL,
    response_status     INTEGER NOT NULL,
    response_body       TEXT    NOT NULL,
    created_at          TEXT    NOT NULL,
    expires_at          TEXT    NOT NULL,
    PRIMARY KEY (actor_user_id, method, path, key)
);

CREATE INDEX ix_idempotency_keys_expires_at ON idempotency_keys (expires_at);

CREATE TABLE audit_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    actor_user_id INTEGER REFERENCES users (id) ON DELETE SET NULL,
    actor_role    TEXT    NOT NULL DEFAULT '',
    action        TEXT    NOT NULL,
    object_type   TEXT    NOT NULL,
    object_id     TEXT    NOT NULL DEFAULT '',
    result        TEXT    NOT NULL CHECK (result IN ('success', 'rejected', 'failure')),
    request_id    TEXT    NOT NULL DEFAULT '',
    detail        TEXT    NOT NULL DEFAULT '',
    occurred_at   TEXT    NOT NULL
);

CREATE INDEX ix_audit_events_actor ON audit_events (actor_user_id, occurred_at);
CREATE INDEX ix_audit_events_object ON audit_events (object_type, object_id, occurred_at);
CREATE INDEX ix_audit_events_action ON audit_events (action, occurred_at);

CREATE TABLE jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT    NOT NULL,
    payload      TEXT    NOT NULL DEFAULT '',
    state        TEXT    NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'permanently_failed')),
    attempts     INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts INTEGER NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
    last_error   TEXT    NOT NULL DEFAULT '',
    run_after    TEXT    NOT NULL,
    locked_at    TEXT,
    locked_by    TEXT    NOT NULL DEFAULT '',
    created_at   TEXT    NOT NULL,
    updated_at   TEXT    NOT NULL
);

CREATE INDEX ix_jobs_ready ON jobs (state, run_after, id);
CREATE INDEX ix_jobs_locked ON jobs (state, locked_at);
CREATE INDEX ix_jobs_kind_state ON jobs (kind, state);
