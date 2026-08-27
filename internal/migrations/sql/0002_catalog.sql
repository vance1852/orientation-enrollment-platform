CREATE TABLE terms (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    code                 TEXT    NOT NULL,
    name                 TEXT    NOT NULL,
    enrollment_opens_at  TEXT    NOT NULL,
    enrollment_closes_at TEXT    NOT NULL,
    add_drop_closes_at   TEXT    NOT NULL,
    credit_limit         INTEGER NOT NULL CHECK (credit_limit > 0),
    archived             INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1))
);

CREATE UNIQUE INDEX ux_terms_code ON terms (code);

CREATE TABLE courses (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    code       TEXT    NOT NULL,
    title      TEXT    NOT NULL,
    credits    INTEGER NOT NULL CHECK (credits > 0 AND credits <= 12),
    department TEXT    NOT NULL,
    retired    INTEGER NOT NULL DEFAULT 0 CHECK (retired IN (0, 1))
);

CREATE UNIQUE INDEX ux_courses_code ON courses (code);
CREATE INDEX ix_courses_department ON courses (department);

CREATE TABLE course_prerequisites (
    course_id           INTEGER NOT NULL REFERENCES courses (id) ON DELETE CASCADE,
    required_course_code TEXT   NOT NULL,
    ordinal             INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (course_id, required_course_code)
);

CREATE INDEX ix_course_prerequisites_required ON course_prerequisites (required_course_code);

CREATE TABLE course_sections (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    term_id         INTEGER NOT NULL REFERENCES terms (id) ON DELETE CASCADE,
    course_id       INTEGER NOT NULL REFERENCES courses (id) ON DELETE CASCADE,
    code            TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK (status IN ('draft', 'open', 'closed', 'cancelled')),
    capacity        INTEGER NOT NULL CHECK (capacity >= 0),
    seats_taken     INTEGER NOT NULL DEFAULT 0 CHECK (seats_taken >= 0),
    waitlist_limit  INTEGER NOT NULL DEFAULT 0 CHECK (waitlist_limit >= 0),
    waitlist_length INTEGER NOT NULL DEFAULT 0 CHECK (waitlist_length >= 0),
    instructor      TEXT    NOT NULL DEFAULT '',
    version         INTEGER NOT NULL DEFAULT 1,
    updated_at      TEXT    NOT NULL,
    CHECK (seats_taken <= capacity)
);

CREATE UNIQUE INDEX ux_course_sections_term_code ON course_sections (term_id, code);
CREATE INDEX ix_course_sections_term_status ON course_sections (term_id, status);
CREATE INDEX ix_course_sections_course ON course_sections (course_id);

CREATE TABLE section_meetings (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    section_id   INTEGER NOT NULL REFERENCES course_sections (id) ON DELETE CASCADE,
    weekday      INTEGER NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    start_minute INTEGER NOT NULL CHECK (start_minute >= 0 AND start_minute < 1440),
    end_minute   INTEGER NOT NULL CHECK (end_minute > start_minute AND end_minute <= 1440),
    room         TEXT    NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX ux_section_meetings_slot ON section_meetings (section_id, weekday, start_minute);
CREATE INDEX ix_section_meetings_section ON section_meetings (section_id);
