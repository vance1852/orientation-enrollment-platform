CREATE TABLE student_registrations (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    student_id         INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    term_id            INTEGER NOT NULL REFERENCES terms (id) ON DELETE CASCADE,
    status             TEXT    NOT NULL CHECK (status IN ('draft', 'submitted', 'verified', 'rejected')),
    program_code       TEXT    NOT NULL DEFAULT '',
    advisor_email      TEXT    NOT NULL DEFAULT '',
    dorm_preference    TEXT    NOT NULL DEFAULT 'undecided',
    submitted_at       TEXT,
    decided_at         TEXT,
    decided_by_user_id INTEGER REFERENCES users (id) ON DELETE SET NULL,
    decision_note      TEXT    NOT NULL DEFAULT '',
    version            INTEGER NOT NULL DEFAULT 1,
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL
);

CREATE UNIQUE INDEX ux_student_registrations_student_term ON student_registrations (student_id, term_id);
CREATE INDEX ix_student_registrations_term_status ON student_registrations (term_id, status);

CREATE TABLE academic_records (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    student_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    course_code  TEXT    NOT NULL,
    grade        TEXT    NOT NULL,
    credits      INTEGER NOT NULL CHECK (credits >= 0),
    completed_at TEXT    NOT NULL
);

CREATE UNIQUE INDEX ux_academic_records_student_course ON academic_records (student_id, course_code);
CREATE INDEX ix_academic_records_student ON academic_records (student_id);

CREATE TABLE enrollments (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    student_id     INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    term_id        INTEGER NOT NULL REFERENCES terms (id) ON DELETE CASCADE,
    section_id     INTEGER NOT NULL REFERENCES course_sections (id) ON DELETE CASCADE,
    course_code    TEXT    NOT NULL,
    credits        INTEGER NOT NULL CHECK (credits > 0),
    status         TEXT    NOT NULL CHECK (status IN ('pending', 'enrolled', 'waitlisted', 'dropped', 'withdrawn', 'completed')),
    waitlist_rank  INTEGER NOT NULL DEFAULT 0 CHECK (waitlist_rank >= 0),
    requested_at   TEXT    NOT NULL,
    decided_at     TEXT,
    released_at    TEXT,
    release_reason TEXT    NOT NULL DEFAULT '',
    version        INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT    NOT NULL,
    updated_at     TEXT    NOT NULL
);

CREATE UNIQUE INDEX ux_enrollments_active_seat
    ON enrollments (student_id, section_id)
    WHERE status IN ('pending', 'enrolled', 'waitlisted', 'completed');

CREATE INDEX ix_enrollments_student_term ON enrollments (student_id, term_id, status);
CREATE INDEX ix_enrollments_section_status ON enrollments (section_id, status, waitlist_rank);
