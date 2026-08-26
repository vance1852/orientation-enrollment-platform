// Package ids generates request identifiers and opaque session tokens.
package ids

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"
)

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewRequestID returns a short, log friendly identifier attached to every
// inbound HTTP request and propagated into services, repositories and workers.
func NewRequestID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate request id: %w", err)
	}
	return "req_" + hex.EncodeToString(buf), nil
}

// NewSessionToken returns a 256 bit opaque bearer token. Only its digest is
// stored server side, so the raw value exists once, in the login response.
func NewSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return strings.ToLower(tokenEncoding.EncodeToString(buf)), nil
}

// NewWorkerID labels a worker instance inside job lock rows so an abandoned
// lease can be attributed to the process that took it.
func NewWorkerID(prefix string) (string, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate worker id: %w", err)
	}
	if prefix == "" {
		prefix = "worker"
	}
	return prefix + "-" + hex.EncodeToString(buf), nil
}
