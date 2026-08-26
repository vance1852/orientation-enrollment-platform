package ids_test

import (
	"strings"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/platform/ids"
)

func TestRequestIDsArePrefixedAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, 200)
	for i := 0; i < 200; i++ {
		id, err := ids.NewRequestID()
		if err != nil {
			t.Fatalf("generating a request id failed: %v", err)
		}
		if !strings.HasPrefix(id, "req_") || len(id) != len("req_")+16 {
			t.Fatalf("request id = %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("request id %q was generated twice", id)
		}
		seen[id] = struct{}{}
	}
}

func TestSessionTokensAreLongAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, 200)
	for i := 0; i < 200; i++ {
		token, err := ids.NewSessionToken()
		if err != nil {
			t.Fatalf("generating a session token failed: %v", err)
		}
		if len(token) != 52 {
			t.Fatalf("token length = %d, want 52 base32 characters", len(token))
		}
		if token != strings.ToLower(token) {
			t.Fatalf("token %q must be lowercase", token)
		}
		if strings.ContainsAny(token, "=+/") {
			t.Fatalf("token %q must be url safe", token)
		}
		if _, duplicate := seen[token]; duplicate {
			t.Fatalf("token %q was generated twice", token)
		}
		seen[token] = struct{}{}
	}
}

func TestWorkerIDsCarryThePrefix(t *testing.T) {
	id, err := ids.NewWorkerID("orientation")
	if err != nil {
		t.Fatalf("generating a worker id failed: %v", err)
	}
	if !strings.HasPrefix(id, "orientation-") {
		t.Fatalf("worker id = %q", id)
	}
	fallback, err := ids.NewWorkerID("")
	if err != nil {
		t.Fatalf("generating a worker id failed: %v", err)
	}
	if !strings.HasPrefix(fallback, "worker-") {
		t.Fatalf("fallback worker id = %q", fallback)
	}
	other, err := ids.NewWorkerID("orientation")
	if err != nil {
		t.Fatalf("generating a worker id failed: %v", err)
	}
	if other == id {
		t.Fatal("two worker ids must differ")
	}
}
