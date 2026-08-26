package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/app"
	"github.com/vance1852/orientation-enrollment-platform/internal/config"
	"github.com/vance1852/orientation-enrollment-platform/internal/httpapi"
	"github.com/vance1852/orientation-enrollment-platform/internal/middleware"
	"github.com/vance1852/orientation-enrollment-platform/internal/platform/logging"
)

const (
	registrarEmail    = "registrar@campus.example"
	registrarPassword = "orientation-registrar-2026"
	studentEmail      = "student@campus.example"
	studentPassword   = "orientation-student-2026"
)

// server boots the assembled application against a temporary database so the
// tests exercise routing, middleware, services and SQL together.
type server struct {
	t       *testing.T
	handler http.Handler
	app     *app.App
}

func newServer(t *testing.T) *server {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "api-test.db"))
	t.Setenv("APP_DATABASE_DSN", dsn)
	t.Setenv("APP_WORKER_ENABLED", "false")
	t.Setenv("APP_SEED_DEMO_DATA", "true")
	t.Setenv("APP_SEED_REGISTRAR_EMAIL", registrarEmail)
	t.Setenv("APP_SEED_REGISTRAR_PASSWORD", registrarPassword)
	t.Setenv("APP_SEED_STUDENT_EMAIL", studentEmail)
	t.Setenv("APP_SEED_STUDENT_PASSWORD", studentPassword)

	cfg, err := config.Load(os.LookupEnv)
	if err != nil {
		t.Fatalf("loading the configuration failed: %v", err)
	}
	instance, err := app.Build(context.Background(), cfg, logging.Discard())
	if err != nil {
		t.Fatalf("building the application failed: %v", err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	return &server{t: t, handler: instance.Handler(), app: instance}
}

type response struct {
	status  int
	header  http.Header
	body    []byte
	decoded map[string]any
}

func (s *server) do(method, path, token string, payload any, headers map[string]string) response {
	s.t.Helper()
	var body *bytes.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			s.t.Fatalf("encoding the request failed: %v", err)
		}
		body = bytes.NewReader(encoded)
	} else {
		body = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(method, path, body)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	s.handler.ServeHTTP(recorder, request)

	result := response{status: recorder.Code, header: recorder.Header(), body: recorder.Body.Bytes()}
	if len(result.body) > 0 && strings.HasPrefix(strings.TrimSpace(string(result.body)), "{") {
		if err := json.Unmarshal(result.body, &result.decoded); err != nil {
			s.t.Fatalf("decoding the response failed: %v (body %s)", err, result.body)
		}
	}
	return result
}

func (s *server) login(email, password string) string {
	s.t.Helper()
	res := s.do(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"email": email, "password": password}, nil)
	if res.status != http.StatusCreated {
		s.t.Fatalf("login for %s returned %d: %s", email, res.status, res.body)
	}
	token, ok := res.decoded["token"].(string)
	if !ok || token == "" {
		s.t.Fatalf("login response has no token: %s", res.body)
	}
	return token
}

func errorCode(t *testing.T, res response) string {
	t.Helper()
	envelope, ok := res.decoded["error"].(map[string]any)
	if !ok {
		t.Fatalf("response is not an error envelope: %s", res.body)
	}
	code, _ := envelope["code"].(string)
	if requestID, _ := envelope["request_id"].(string); requestID == "" {
		t.Fatalf("the error envelope must carry a request id: %s", res.body)
	}
	return code
}

func (s *server) currentTermID(token string) int64 {
	s.t.Helper()
	res := s.do(http.MethodGet, "/api/v1/terms", token, nil, nil)
	if res.status != http.StatusOK {
		s.t.Fatalf("listing terms returned %d: %s", res.status, res.body)
	}
	items, ok := res.decoded["items"].([]any)
	if !ok || len(items) == 0 {
		s.t.Fatalf("no term was seeded: %s", res.body)
	}
	term := items[0].(map[string]any)
	return int64(term["id"].(float64))
}

func (s *server) sectionIDByCode(token, code string) int64 {
	s.t.Helper()
	res := s.do(http.MethodGet, "/api/v1/sections?page_size=50", token, nil, nil)
	if res.status != http.StatusOK {
		s.t.Fatalf("listing sections returned %d: %s", res.status, res.body)
	}
	items := res.decoded["items"].([]any)
	for _, raw := range items {
		section := raw.(map[string]any)
		if section["code"] == code {
			return int64(section["id"].(float64))
		}
	}
	s.t.Fatalf("section %s was not seeded: %s", code, res.body)
	return 0
}

// verifyStudent walks the real paperwork path so the enrollment tests start from
// a state a student can actually reach through the API.
func (s *server) verifyStudent(studentToken, registrarToken string, termID int64) {
	s.t.Helper()
	submit := s.do(http.MethodPost, "/api/v1/registrations", studentToken, map[string]any{
		"term_id": termID, "program_code": "CS-BSC",
		"advisor_email": "advisor@campus.example", "dorm_preference": "on_campus",
	}, nil)
	if submit.status != http.StatusCreated {
		s.t.Fatalf("submitting the registration returned %d: %s", submit.status, submit.body)
	}
	registrationID := int64(submit.decoded["id"].(float64))
	decide := s.do(http.MethodPost,
		"/api/v1/registrations/"+itoa(registrationID)+"/decision", registrarToken,
		map[string]any{"status": "verified", "note": "documents complete"}, nil)
	if decide.status != http.StatusOK {
		s.t.Fatalf("verifying the registration returned %d: %s", decide.status, decide.body)
	}
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestHealthAndReadinessProbes(t *testing.T) {
	s := newServer(t)

	live := s.do(http.MethodGet, "/healthz", "", nil, nil)
	if live.status != http.StatusOK || live.decoded["status"] != "alive" {
		t.Fatalf("liveness = %d %s", live.status, live.body)
	}

	ready := s.do(http.MethodGet, "/readyz", "", nil, nil)
	if ready.status != http.StatusOK || ready.decoded["status"] != "ready" {
		t.Fatalf("readiness = %d %s", ready.status, ready.body)
	}
	if ready.decoded["schema_version"] != ready.decoded["expected_schema_version"] {
		t.Fatalf("schema mismatch reported: %s", ready.body)
	}
}

func TestRequestIDIsEchoedAndGenerated(t *testing.T) {
	s := newServer(t)

	generated := s.do(http.MethodGet, "/healthz", "", nil, nil)
	if generated.header.Get(middleware.RequestIDHeader) == "" {
		t.Fatal("the server must allocate a request id")
	}

	supplied := s.do(http.MethodGet, "/healthz", "", nil,
		map[string]string{middleware.RequestIDHeader: "trace-abc-123"})
	if got := supplied.header.Get(middleware.RequestIDHeader); got != "trace-abc-123" {
		t.Fatalf("request id = %q, want the supplied value", got)
	}

	rejected := s.do(http.MethodGet, "/healthz", "", nil,
		map[string]string{middleware.RequestIDHeader: "not a valid id!"})
	if got := rejected.header.Get(middleware.RequestIDHeader); got == "not a valid id!" || got == "" {
		t.Fatalf("a malformed request id must be replaced, got %q", got)
	}
}

func TestLoginLogoutAndProfile(t *testing.T) {
	s := newServer(t)

	bad := s.do(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"email": studentEmail, "password": "wrong"}, nil)
	if bad.status != http.StatusUnauthorized {
		t.Fatalf("a wrong password returned %d: %s", bad.status, bad.body)
	}
	if code := errorCode(t, bad); code != "unauthenticated" {
		t.Fatalf("error code = %q", code)
	}

	malformed := s.do(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"email": "not-an-email", "password": studentPassword}, nil)
	if malformed.status != http.StatusBadRequest || errorCode(t, malformed) != "validation_failed" {
		t.Fatalf("a malformed address returned %d: %s", malformed.status, malformed.body)
	}

	unknownField := s.do(http.MethodPost, "/api/v1/auth/login", "",
		map[string]string{"email": studentEmail, "password": studentPassword, "role": "admin"}, nil)
	if unknownField.status != http.StatusBadRequest {
		t.Fatalf("an unknown field must be rejected, got %d: %s", unknownField.status, unknownField.body)
	}

	token := s.login(studentEmail, studentPassword)

	profile := s.do(http.MethodGet, "/api/v1/auth/me", token, nil, nil)
	if profile.status != http.StatusOK {
		t.Fatalf("profile returned %d: %s", profile.status, profile.body)
	}
	if profile.decoded["role"] != "student" || profile.decoded["email"] != studentEmail {
		t.Fatalf("profile = %s", profile.body)
	}
	if profile.decoded["active_sessions"].(float64) != 1 {
		t.Fatalf("active sessions = %s", profile.body)
	}

	logout := s.do(http.MethodPost, "/api/v1/auth/logout", token, nil, nil)
	if logout.status != http.StatusNoContent {
		t.Fatalf("logout returned %d: %s", logout.status, logout.body)
	}
	after := s.do(http.MethodGet, "/api/v1/auth/me", token, nil, nil)
	if after.status != http.StatusUnauthorized || errorCode(t, after) != "session_revoked" {
		t.Fatalf("a revoked token returned %d: %s", after.status, after.body)
	}
}

func TestProtectedRoutesRejectMissingOrMalformedTokens(t *testing.T) {
	s := newServer(t)
	cases := map[string]map[string]string{
		"no header":     nil,
		"wrong scheme":  {"Authorization": "Basic abc123"},
		"empty bearer":  {"Authorization": "Bearer "},
		"unknown token": {"Authorization": "Bearer aaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			res := s.do(http.MethodGet, "/api/v1/sections", "", nil, headers)
			if res.status != http.StatusUnauthorized {
				t.Fatalf("status = %d: %s", res.status, res.body)
			}
			if code := errorCode(t, res); code != "unauthenticated" {
				t.Fatalf("error code = %q", code)
			}
		})
	}
}

func TestSectionListingPaginatesFiltersAndValidates(t *testing.T) {
	s := newServer(t)
	token := s.login(studentEmail, studentPassword)

	page := s.do(http.MethodGet, "/api/v1/sections?page=1&page_size=2&sort_by=code&order=asc", token, nil, nil)
	if page.status != http.StatusOK {
		t.Fatalf("listing returned %d: %s", page.status, page.body)
	}
	meta := page.decoded["meta"].(map[string]any)
	if meta["total"].(float64) < 4 || meta["page_size"].(float64) != 2 {
		t.Fatalf("meta = %v", meta)
	}
	items := page.decoded["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	first := items[0].(map[string]any)
	if first["seats_available"] == nil || first["meetings"] == nil {
		t.Fatalf("section payload is incomplete: %v", first)
	}

	filtered := s.do(http.MethodGet, "/api/v1/sections?course_code=cs210&only_open=true", token, nil, nil)
	if filtered.status != http.StatusOK {
		t.Fatalf("filtering returned %d: %s", filtered.status, filtered.body)
	}
	if filtered.decoded["meta"].(map[string]any)["total"].(float64) != 1 {
		t.Fatalf("course filter = %s", filtered.body)
	}

	invalid := map[string]string{
		"page_size too large": "/api/v1/sections?page_size=500",
		"page not a number":   "/api/v1/sections?page=abc",
		"unknown sort":        "/api/v1/sections?sort_by=password_hash",
		"unknown order":       "/api/v1/sections?order=sideways",
		"unknown status":      "/api/v1/sections?section_status=frozen",
		"bad boolean":         "/api/v1/sections?only_open=perhaps",
	}
	for name, path := range invalid {
		t.Run(name, func(t *testing.T) {
			res := s.do(http.MethodGet, path, token, nil, nil)
			if res.status != http.StatusBadRequest || errorCode(t, res) != "validation_failed" {
				t.Fatalf("status = %d: %s", res.status, res.body)
			}
		})
	}

	missing := s.do(http.MethodGet, "/api/v1/sections/999999", token, nil, nil)
	if missing.status != http.StatusNotFound || errorCode(t, missing) != "not_found" {
		t.Fatalf("a missing section returned %d: %s", missing.status, missing.body)
	}
	badPath := s.do(http.MethodGet, "/api/v1/sections/abc", token, nil, nil)
	if badPath.status != http.StatusBadRequest {
		t.Fatalf("a malformed identifier returned %d: %s", badPath.status, badPath.body)
	}
}

func TestRegistrationFlowAndRoleBoundaries(t *testing.T) {
	s := newServer(t)
	studentToken := s.login(studentEmail, studentPassword)
	registrarToken := s.login(registrarEmail, registrarPassword)
	termID := s.currentTermID(studentToken)

	invalid := s.do(http.MethodPost, "/api/v1/registrations", studentToken, map[string]any{
		"term_id": termID, "program_code": "CS-BSC",
		"advisor_email": "advisor@campus.example", "dorm_preference": "tent",
	}, nil)
	if invalid.status != http.StatusBadRequest || errorCode(t, invalid) != "validation_failed" {
		t.Fatalf("an invalid dorm choice returned %d: %s", invalid.status, invalid.body)
	}

	submit := s.do(http.MethodPost, "/api/v1/registrations", studentToken, map[string]any{
		"term_id": termID, "program_code": "CS-BSC",
		"advisor_email": "advisor@campus.example", "dorm_preference": "on_campus",
	}, nil)
	if submit.status != http.StatusCreated || submit.decoded["status"] != "submitted" {
		t.Fatalf("submission returned %d: %s", submit.status, submit.body)
	}
	registrationID := int64(submit.decoded["id"].(float64))

	if res := s.do(http.MethodGet, "/api/v1/registrations", studentToken, nil, nil); res.status != http.StatusForbidden {
		t.Fatalf("a student must not read the queue, got %d: %s", res.status, res.body)
	}
	queue := s.do(http.MethodGet, "/api/v1/registrations?status=submitted", registrarToken, nil, nil)
	if queue.status != http.StatusOK {
		t.Fatalf("the queue returned %d: %s", queue.status, queue.body)
	}
	if queue.decoded["meta"].(map[string]any)["total"].(float64) != 1 {
		t.Fatalf("queue = %s", queue.body)
	}

	forbidden := s.do(http.MethodPost, "/api/v1/registrations/"+itoa(registrationID)+"/decision",
		studentToken, map[string]any{"status": "verified"}, nil)
	if forbidden.status != http.StatusForbidden || errorCode(t, forbidden) != "forbidden" {
		t.Fatalf("a student decision returned %d: %s", forbidden.status, forbidden.body)
	}

	decided := s.do(http.MethodPost, "/api/v1/registrations/"+itoa(registrationID)+"/decision",
		registrarToken, map[string]any{"status": "verified", "note": "documents complete"}, nil)
	if decided.status != http.StatusOK || decided.decoded["status"] != "verified" {
		t.Fatalf("verification returned %d: %s", decided.status, decided.body)
	}

	mine := s.do(http.MethodGet, "/api/v1/registrations/mine", studentToken, nil, nil)
	if mine.status != http.StatusOK || mine.decoded["status"] != "verified" {
		t.Fatalf("own registration returned %d: %s", mine.status, mine.body)
	}

	repeated := s.do(http.MethodPost, "/api/v1/registrations/"+itoa(registrationID)+"/decision",
		registrarToken, map[string]any{"status": "verified"}, nil)
	if repeated.status != http.StatusConflict || errorCode(t, repeated) != "invalid_transition" {
		t.Fatalf("a repeated decision returned %d: %s", repeated.status, repeated.body)
	}
}

func TestEnrollmentRequiresVerifiedPaperwork(t *testing.T) {
	s := newServer(t)
	studentToken := s.login(studentEmail, studentPassword)
	sectionID := s.sectionIDByCode(studentToken, "CS210-A")

	res := s.do(http.MethodPost, "/api/v1/enrollments", studentToken,
		map[string]any{"section_id": sectionID}, nil)
	if res.status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d: %s", res.status, res.body)
	}
	if code := errorCode(t, res); code != "registration_incomplete" {
		t.Fatalf("error code = %q", code)
	}
}

func TestIdempotentEnrollmentReplaysTheFirstResponse(t *testing.T) {
	s := newServer(t)
	studentToken := s.login(studentEmail, studentPassword)
	registrarToken := s.login(registrarEmail, registrarPassword)
	termID := s.currentTermID(studentToken)
	s.verifyStudent(studentToken, registrarToken, termID)
	sectionID := s.sectionIDByCode(studentToken, "CS210-A")

	headers := map[string]string{httpapi.IdempotencyHeader: "orientation-key-1"}
	first := s.do(http.MethodPost, "/api/v1/enrollments", studentToken,
		map[string]any{"section_id": sectionID}, headers)
	if first.status != http.StatusCreated {
		t.Fatalf("the first claim returned %d: %s", first.status, first.body)
	}
	enrollment := first.decoded["enrollment"].(map[string]any)
	if enrollment["status"] != "enrolled" {
		t.Fatalf("enrollment = %v", enrollment)
	}
	if first.header.Get("Idempotent-Replay") != "" {
		t.Fatal("the first response must not be marked as a replay")
	}

	replay := s.do(http.MethodPost, "/api/v1/enrollments", studentToken,
		map[string]any{"section_id": sectionID}, headers)
	if replay.status != http.StatusCreated {
		t.Fatalf("the replay returned %d: %s", replay.status, replay.body)
	}
	if replay.header.Get("Idempotent-Replay") != "true" {
		t.Fatal("the replay must be flagged")
	}
	if string(replay.body) != string(first.body) {
		t.Fatalf("the replay body differs:\n%s\n%s", first.body, replay.body)
	}

	// The same key with another payload must not hand back the stored answer.
	otherSection := s.sectionIDByCode(studentToken, "CS110-A")
	mismatch := s.do(http.MethodPost, "/api/v1/enrollments", studentToken,
		map[string]any{"section_id": otherSection}, headers)
	if mismatch.status != http.StatusConflict || errorCode(t, mismatch) != "idempotency_mismatch" {
		t.Fatalf("the mismatch returned %d: %s", mismatch.status, mismatch.body)
	}

	// Without a key the duplicate rule applies instead.
	duplicate := s.do(http.MethodPost, "/api/v1/enrollments", studentToken,
		map[string]any{"section_id": sectionID}, nil)
	if duplicate.status != http.StatusConflict || errorCode(t, duplicate) != "duplicate_enrollment" {
		t.Fatalf("the duplicate returned %d: %s", duplicate.status, duplicate.body)
	}
}

func TestEnrollmentListingDroppingAndRosterAccess(t *testing.T) {
	s := newServer(t)
	studentToken := s.login(studentEmail, studentPassword)
	registrarToken := s.login(registrarEmail, registrarPassword)
	termID := s.currentTermID(studentToken)
	s.verifyStudent(studentToken, registrarToken, termID)
	sectionID := s.sectionIDByCode(studentToken, "CS210-A")

	created := s.do(http.MethodPost, "/api/v1/enrollments", studentToken,
		map[string]any{"section_id": sectionID}, nil)
	if created.status != http.StatusCreated {
		t.Fatalf("claiming returned %d: %s", created.status, created.body)
	}
	enrollmentID := int64(created.decoded["enrollment"].(map[string]any)["id"].(float64))

	list := s.do(http.MethodGet, "/api/v1/enrollments?status=enrolled&page_size=10", studentToken, nil, nil)
	if list.status != http.StatusOK {
		t.Fatalf("listing returned %d: %s", list.status, list.body)
	}
	if list.decoded["meta"].(map[string]any)["total"].(float64) != 1 {
		t.Fatalf("listing = %s", list.body)
	}

	single := s.do(http.MethodGet, "/api/v1/enrollments/"+itoa(enrollmentID), studentToken, nil, nil)
	if single.status != http.StatusOK || single.decoded["course_code"] != "CS210" {
		t.Fatalf("reading the enrollment returned %d: %s", single.status, single.body)
	}

	if res := s.do(http.MethodGet, "/api/v1/sections/"+itoa(sectionID)+"/roster", studentToken, nil, nil); res.status != http.StatusForbidden {
		t.Fatalf("a student roster read returned %d: %s", res.status, res.body)
	}
	roster := s.do(http.MethodGet, "/api/v1/sections/"+itoa(sectionID)+"/roster", registrarToken, nil, nil)
	if roster.status != http.StatusOK {
		t.Fatalf("the roster returned %d: %s", roster.status, roster.body)
	}
	if roster.decoded["meta"].(map[string]any)["total"].(float64) != 1 {
		t.Fatalf("roster = %s", roster.body)
	}

	dropped := s.do(http.MethodDelete,
		"/api/v1/enrollments/"+itoa(enrollmentID)+"?reason=schedule%20changed", studentToken, nil, nil)
	if dropped.status != http.StatusOK || dropped.decoded["status"] != "dropped" {
		t.Fatalf("dropping returned %d: %s", dropped.status, dropped.body)
	}
	if dropped.decoded["release_reason"] != "schedule changed" {
		t.Fatalf("release reason = %s", dropped.body)
	}
	again := s.do(http.MethodDelete, "/api/v1/enrollments/"+itoa(enrollmentID), studentToken, nil, nil)
	if again.status != http.StatusConflict || errorCode(t, again) != "invalid_transition" {
		t.Fatalf("dropping twice returned %d: %s", again.status, again.body)
	}
}

func TestBatchEnrollmentReportsPartialFailure(t *testing.T) {
	s := newServer(t)
	studentToken := s.login(studentEmail, studentPassword)
	registrarToken := s.login(registrarEmail, registrarPassword)
	termID := s.currentTermID(studentToken)
	s.verifyStudent(studentToken, registrarToken, termID)

	good := s.sectionIDByCode(studentToken, "ORI100-A")
	res := s.do(http.MethodPost, "/api/v1/enrollments/batch", studentToken,
		map[string]any{"section_ids": []int64{good, 999999}}, nil)
	if res.status != http.StatusMultiStatus {
		t.Fatalf("a partial batch must answer 207, got %d: %s", res.status, res.body)
	}
	if res.decoded["succeeded"].(float64) != 1 || res.decoded["failed"].(float64) != 1 {
		t.Fatalf("batch counters = %s", res.body)
	}
	items := res.decoded["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	for _, raw := range items {
		item := raw.(map[string]any)
		if int64(item["section_id"].(float64)) == good {
			if item["succeeded"] != true || item["status"] != "enrolled" {
				t.Fatalf("successful item = %v", item)
			}
			continue
		}
		if item["succeeded"] != false || item["code"] != "not_found" {
			t.Fatalf("failed item = %v", item)
		}
	}

	empty := s.do(http.MethodPost, "/api/v1/enrollments/batch", studentToken,
		map[string]any{"section_ids": []int64{}}, nil)
	if empty.status != http.StatusBadRequest {
		t.Fatalf("an empty batch returned %d: %s", empty.status, empty.body)
	}
}

func TestAuditTrailIsRegistrarOnly(t *testing.T) {
	s := newServer(t)
	studentToken := s.login(studentEmail, studentPassword)
	registrarToken := s.login(registrarEmail, registrarPassword)

	if res := s.do(http.MethodGet, "/api/v1/audit-events", studentToken, nil, nil); res.status != http.StatusForbidden {
		t.Fatalf("a student read returned %d: %s", res.status, res.body)
	}
	trail := s.do(http.MethodGet, "/api/v1/audit-events?action=auth.login&page_size=10", registrarToken, nil, nil)
	if trail.status != http.StatusOK {
		t.Fatalf("the trail returned %d: %s", trail.status, trail.body)
	}
	if trail.decoded["meta"].(map[string]any)["total"].(float64) < 2 {
		t.Fatalf("both logins must be recorded: %s", trail.body)
	}
	items := trail.decoded["items"].([]any)
	event := items[0].(map[string]any)
	if event["request_id"] == "" || event["action"] != "auth.login" {
		t.Fatalf("audit event = %v", event)
	}

	badTime := s.do(http.MethodGet, "/api/v1/audit-events?since=yesterday", registrarToken, nil, nil)
	if badTime.status != http.StatusBadRequest {
		t.Fatalf("a malformed timestamp returned %d: %s", badTime.status, badTime.body)
	}
}

func TestUnknownRoutesAndMethods(t *testing.T) {
	s := newServer(t)
	token := s.login(studentEmail, studentPassword)

	if res := s.do(http.MethodGet, "/api/v1/nothing-here", token, nil, nil); res.status != http.StatusNotFound {
		t.Fatalf("an unknown path returned %d", res.status)
	}
	if res := s.do(http.MethodPut, "/api/v1/sections", token, nil, nil); res.status != http.StatusMethodNotAllowed {
		t.Fatalf("an unsupported method returned %d", res.status)
	}
}
