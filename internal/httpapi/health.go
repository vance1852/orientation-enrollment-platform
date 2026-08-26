package httpapi

import (
	"net/http"

	"github.com/vance1852/orientation-enrollment-platform/internal/httpx"
)

type livenessResponse struct {
	Status string `json:"status"`
}

type readinessResponse struct {
	Status         string `json:"status"`
	SchemaVersion  int    `json:"schema_version"`
	ExpectedSchema int    `json:"expected_schema_version"`
}

// handleLiveness answers as long as the process is able to serve traffic. It
// does not touch the database so a slow query cannot restart the container.
func (a *API) handleLiveness(w http.ResponseWriter, r *http.Request) {
	_ = httpx.WriteJSON(w, http.StatusOK, livenessResponse{Status: "alive"})
}

// handleReadiness verifies the dependencies the service actually needs: the
// database answers and the applied schema matches the binary.
func (a *API) handleReadiness(w http.ResponseWriter, r *http.Request) {
	if err := a.readiness.Ping(r.Context()); err != nil {
		a.logger.Warn("readiness probe failed on database ping", "error", err.Error())
		httpx.WriteCodedError(w, r, "migration_drift", "The database is not reachable.")
		return
	}
	version, err := a.readiness.SchemaVersion(r.Context())
	if err != nil {
		a.logger.Warn("readiness probe failed on schema lookup", "error", err.Error())
		httpx.WriteCodedError(w, r, "migration_drift", "The schema version could not be read.")
		return
	}
	if version != a.expectedSchema {
		a.logger.Warn("readiness probe found a schema mismatch",
			"applied", version, "expected", a.expectedSchema)
		_ = httpx.WriteJSON(w, http.StatusServiceUnavailable, readinessResponse{
			Status:         "schema_mismatch",
			SchemaVersion:  version,
			ExpectedSchema: a.expectedSchema,
		})
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, readinessResponse{
		Status:         "ready",
		SchemaVersion:  version,
		ExpectedSchema: a.expectedSchema,
	})
}
