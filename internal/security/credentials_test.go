package security_test

import (
	"strings"
	"testing"

	"github.com/vance1852/orientation-enrollment-platform/internal/security"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := security.HashNewPassword("orientation-student-2026")
	if err != nil {
		t.Fatalf("hashing failed: %v", err)
	}
	if !strings.HasPrefix(hash, "pbkdf2_sha256$") {
		t.Fatalf("hash format = %q", hash)
	}
	if strings.Contains(hash, "orientation-student-2026") {
		t.Fatal("the stored hash must not contain the plaintext password")
	}

	ok, err := security.VerifyPassword(hash, "orientation-student-2026")
	if err != nil {
		t.Fatalf("verification failed: %v", err)
	}
	if !ok {
		t.Fatal("the correct password must verify")
	}

	ok, err = security.VerifyPassword(hash, "orientation-student-2027")
	if err != nil {
		t.Fatalf("verification of a wrong password errored: %v", err)
	}
	if ok {
		t.Fatal("a wrong password must not verify")
	}
}

func TestHashPasswordRejectsWeakInputs(t *testing.T) {
	salt, err := security.NewSalt()
	if err != nil {
		t.Fatalf("salt generation failed: %v", err)
	}
	if _, err := security.HashPassword("short", salt, security.DefaultIterations); err == nil {
		t.Fatal("a password below the minimum length must be rejected")
	}
	if _, err := security.HashPassword("long-enough-password", []byte("tiny"), security.DefaultIterations); err == nil {
		t.Fatal("an undersized salt must be rejected")
	}
	if _, err := security.HashPassword("long-enough-password", salt, 0); err != nil {
		t.Fatalf("a zero work factor must fall back to the default, got %v", err)
	}
}

func TestSaltsAreUniquePerPassword(t *testing.T) {
	first, err := security.HashNewPassword("identical-password")
	if err != nil {
		t.Fatalf("hashing failed: %v", err)
	}
	second, err := security.HashNewPassword("identical-password")
	if err != nil {
		t.Fatalf("hashing failed: %v", err)
	}
	if first == second {
		t.Fatal("two hashes of the same password must differ because of the salt")
	}
	for _, hash := range []string{first, second} {
		ok, err := security.VerifyPassword(hash, "identical-password")
		if err != nil || !ok {
			t.Fatalf("both hashes must verify, got %v %v", ok, err)
		}
	}
}

func TestVerifyPasswordRejectsCorruptStoredValues(t *testing.T) {
	cases := []string{
		"",
		"plaintext",
		"bcrypt$12$salt$digest",
		"pbkdf2_sha256$notanumber$c2FsdA$ZGlnZXN0",
		"pbkdf2_sha256$1000$!!!$ZGlnZXN0",
		"pbkdf2_sha256$1000$c2FsdA$!!!",
	}
	for _, stored := range cases {
		if _, err := security.VerifyPassword(stored, "whatever"); err == nil {
			t.Fatalf("stored value %q must be rejected", stored)
		}
	}
}

func TestTokenDigestIsStableAndOpaque(t *testing.T) {
	digest := security.TokenDigest("abc123")
	if len(digest) != 64 {
		t.Fatalf("digest length = %d, want 64", len(digest))
	}
	if digest != security.TokenDigest("abc123") {
		t.Fatal("the digest must be stable for the same token")
	}
	if digest == security.TokenDigest("abc124") {
		t.Fatal("different tokens must produce different digests")
	}
	if strings.Contains(digest, "abc123") {
		t.Fatal("the digest must not embed the token")
	}
}

func TestFingerprintPayloadSeparatesParts(t *testing.T) {
	// Concatenating the parts differently must not collide, otherwise an
	// idempotency key could be replayed against another endpoint.
	first := security.FingerprintPayload("POST", "/api/v1/enrollments", "{}")
	second := security.FingerprintPayload("POST/api", "/v1/enrollments", "{}")
	if first == second {
		t.Fatal("part boundaries must influence the fingerprint")
	}
	if first != security.FingerprintPayload("POST", "/api/v1/enrollments", "{}") {
		t.Fatal("the fingerprint must be stable")
	}
}
