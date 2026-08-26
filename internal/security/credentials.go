// Package security implements password hashing and session token digests.
//
// Passwords use PBKDF2-HMAC-SHA256 with a per-user salt. Session tokens are
// high entropy random strings, so a single SHA-256 pass is enough to keep the
// raw bearer value out of the database.
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// DefaultIterations is the PBKDF2 work factor used for new passwords.
const DefaultIterations = 60000

const (
	hashPrefix = "pbkdf2_sha256"
	saltLength = 16
	keyLength  = 32
)

// HashPassword derives a storable password hash. The returned value encodes the
// algorithm, work factor and salt so the format can evolve without a migration.
func HashPassword(password string, salt []byte, iterations int) (string, error) {
	if len(password) < 8 {
		return "", fmt.Errorf("password must be at least 8 characters")
	}
	if len(salt) != saltLength {
		return "", fmt.Errorf("salt must be %d bytes, got %d", saltLength, len(salt))
	}
	if iterations <= 0 {
		iterations = DefaultIterations
	}
	key := pbkdf2SHA256([]byte(password), salt, iterations, keyLength)
	return strings.Join([]string{
		hashPrefix,
		strconv.Itoa(iterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

// VerifyPassword compares a candidate password against a stored hash in
// constant time.
func VerifyPassword(stored, candidate string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != hashPrefix {
		return false, fmt.Errorf("unsupported password hash format")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false, fmt.Errorf("invalid password hash work factor")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false, fmt.Errorf("invalid password hash salt: %w", err)
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false, fmt.Errorf("invalid password hash digest: %w", err)
	}
	actual := pbkdf2SHA256([]byte(candidate), salt, iterations, len(expected))
	return subtle.ConstantTimeCompare(expected, actual) == 1, nil
}

// TokenDigest returns the lowercase hex SHA-256 digest of a session token.
func TokenDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// FingerprintPayload returns a stable digest of an idempotent request body so a
// replayed key can be checked against the original payload.
func FingerprintPayload(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// pbkdf2SHA256 is a local PBKDF2 implementation so the module keeps a small,
// auditable dependency surface.
func pbkdf2SHA256(password, salt []byte, iterations, length int) []byte {
	mac := hmac.New(sha256.New, password)
	hashLen := mac.Size()
	blocks := (length + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)
	buf := make([]byte, 4)
	for block := 1; block <= blocks; block++ {
		mac.Reset()
		mac.Write(salt)
		buf[0] = byte(block >> 24)
		buf[1] = byte(block >> 16)
		buf[2] = byte(block >> 8)
		buf[3] = byte(block)
		mac.Write(buf)
		u := mac.Sum(nil)
		t := make([]byte, hashLen)
		copy(t, u)
		for iteration := 2; iteration <= iterations; iteration++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(u[:0])
			for i := range t {
				t[i] ^= u[i]
			}
		}
		out = append(out, t...)
	}
	return out[:length]
}

// NewSalt returns a fresh random salt sized for HashPassword.
func NewSalt() ([]byte, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate password salt: %w", err)
	}
	return salt, nil
}

// HashNewPassword combines salt generation and derivation for the common case.
func HashNewPassword(password string) (string, error) {
	salt, err := NewSalt()
	if err != nil {
		return "", err
	}
	return HashPassword(password, salt, DefaultIterations)
}
