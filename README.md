# Orientation Enrollment Platform

A Go backend for the start-of-term workflow of a university: incoming students
hand in their orientation paperwork, a registrar verifies it, and verified
students then claim seats in course sections that have limited capacity,
prerequisites, weekly time slots and a waitlist.

The two public business paths depend on each other: no seat can be claimed
before the paperwork is verified, and every seat that is released is offered back
to the waitlist by a durable background job.

## Architecture

```
cmd/server                 process entry point, signal handling
internal/app               wiring: config, store, services, HTTP, worker
internal/config            environment configuration and validation
internal/domain            entities, state machines, eligibility rules, errors
internal/repository        persistence contracts consumed by the services
internal/storage/sqlite    SQL implementation, transactions, migrations runner
internal/migrations        embedded versioned schema (0001..0004)
internal/service           use cases: auth, catalogue, registration, enrollment
internal/audit             audit trail recorder that joins the caller's transaction
internal/worker            durable job queue, retry/backoff, recurring maintenance
internal/httpapi           routes, request parsing, response presenters
internal/httpx             shared JSON envelope and error-code mapping
internal/middleware        request id, access log, panic recovery, timeout
internal/platform/clock    time source (system and deterministic)
internal/platform/ids      request ids, session tokens, worker ids
internal/platform/logging  structured logger and request-id context
internal/security          PBKDF2 password hashing, token digests, fingerprints
```

Dependency direction: the domain layer knows nothing about HTTP or SQL, the HTTP
layer never builds SQL, and the worker reaches business state only through the
services.

## Data model

SQLite through `modernc.org/sqlite` (pure Go, no cgo). The schema is applied from
embedded, versioned SQL files and is recorded with a checksum, so a repository
that diverges from an already migrated database is reported instead of silently
reapplied.

| Table | Purpose |
| --- | --- |
| `users` | principals with a role of `student` or `registrar` |
| `sessions` | revocable server-side sessions keyed by a token digest |
| `terms` | enrollment window, add/drop window, per-term credit limit |
| `courses` | catalogue entries with credits and department |
| `course_prerequisites` | required course codes per catalogue entry |
| `course_sections` | offerings with capacity, seat counter, waitlist and version |
| `section_meetings` | weekly time blocks used for conflict detection |
| `student_registrations` | orientation paperwork state machine |
| `academic_records` | completed courses used to evaluate prerequisites |
| `enrollments` | seat claims and waitlist positions |
| `idempotency_keys` | response snapshots for replayed mutations |
| `audit_events` | actor, object, action, result and request id |
| `jobs` | durable background queue with retry budget |
| `schema_migrations` | applied version and checksum |

## Business rules

- Orientation paperwork: `draft -> submitted -> verified | rejected`, and a
  rejected record may be resubmitted. A verified record is terminal.
- Seat lifecycle: `pending -> enrolled | waitlisted`, `enrolled -> dropped |
  completed`, `waitlisted -> enrolled | withdrawn`.
- Eligibility is evaluated in a fixed order: enrollment window, verified
  paperwork, section status, duplicate course, prerequisites, term credit limit,
  weekly schedule conflict, then capacity and waitlist room.
- Capacity is defended by a conditional update that carries both the section
  version and the capacity bound, so two concurrent requests cannot oversell a
  section. A version clash makes the service replay the transaction.
- Claiming a seat writes the enrollment, the section counter and the audit entry
  in one transaction. Releasing a seat additionally enqueues a promotion job.
- Rejected attempts are audited in their own transaction, so the trail survives
  the rollback of the business transaction that produced them.
- All deadlines are campus local (`APP_BUSINESS_TZ`, `Asia/Shanghai` by default).

## HTTP API

| Method and path | Role | Purpose |
| --- | --- | --- |
| `POST /api/v1/auth/login` | public | open a session, returns the bearer token once |
| `POST /api/v1/auth/logout` | any | revoke the caller's session |
| `GET /api/v1/auth/me` | any | profile and live session count |
| `GET /api/v1/terms` | any | terms; archived terms only for a registrar |
| `GET /api/v1/sections` | any | paged, filtered, sorted catalogue |
| `GET /api/v1/sections/{sectionID}` | any | one section with its weekly blocks |
| `GET /api/v1/sections/{sectionID}/roster` | registrar | paged roster |
| `POST /api/v1/registrations` | any | submit or resubmit paperwork |
| `GET /api/v1/registrations` | registrar | paged review queue |
| `GET /api/v1/registrations/mine` | any | own paperwork for a term |
| `POST /api/v1/registrations/{registrationID}/decision` | registrar | verify or reject |
| `POST /api/v1/enrollments` | any | claim a seat, honours `Idempotency-Key` |
| `POST /api/v1/enrollments/batch` | any | claim several sections, per-item result |
| `GET /api/v1/enrollments` | any | own claims; registrars see everything |
| `GET /api/v1/enrollments/{enrollmentID}` | any | one claim |
| `DELETE /api/v1/enrollments/{enrollmentID}` | any | drop or withdraw |
| `GET /api/v1/audit-events` | registrar | paged audit trail |
| `GET /healthz` | public | liveness, no dependency check |
| `GET /readyz` | public | database reachable and schema version matches |

Every failure uses one envelope:

```json
{"error": {"code": "capacity_exhausted", "message": "...", "request_id": "req_..."}}
```

Authentication uses `Authorization: Bearer <token>`. Expired and revoked sessions
are reported as distinct codes (`session_expired`, `session_revoked`).

## Running locally

```bash
cp .env.example .env      # adjust the seed passwords
go run ./cmd/server
curl -s localhost:8080/readyz
```

The first start-up applies the schema and, unless `APP_SEED_DEMO_DATA=false`,
inserts one term, four courses, four sections, a registrar and a student. The
seed is skipped on every later start-up.

## Verification

```bash
go build ./...
go vet ./...
go test ./... -count=1
go test -race ./... -count=1
```

## Container

```bash
docker build --platform linux/amd64 -t orientation-enrollment-platform:amd64 .
docker run --rm -p 8080:8080 orientation-enrollment-platform:amd64
curl -s localhost:8080/readyz
```

The image builds `./cmd/server` with `CGO_ENABLED=0`, runs as a non-root user and
keeps the database under the `/data` volume.
