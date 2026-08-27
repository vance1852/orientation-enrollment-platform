package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
)

// maxBodyBytes bounds the request body so a client cannot exhaust memory.
const maxBodyBytes = 64 * 1024

// decodeJSON reads a JSON body, rejecting unknown fields so a typo in a client
// payload is reported instead of being silently ignored.
func decodeJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return domain.NewFieldError("body", "must not be empty")
	}
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if err == io.EOF {
			return domain.NewFieldError("body", "must contain a JSON object")
		}
		return domain.NewFieldError("body", err.Error())
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return domain.NewFieldError("body", "must contain exactly one JSON object")
	}
	return nil
}

// rawBody reads and restores the request body so an idempotent handler can
// fingerprint the payload before decoding it.
func rawBody(r *http.Request) (string, error) {
	if r.Body == nil {
		return "", nil
	}
	data, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	if err != nil {
		return "", domain.NewFieldError("body", "could not be read")
	}
	return string(data), nil
}

// pathInt64 reads a positive identifier from a route pattern wildcard.
func pathInt64(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, domain.NewFieldError(name, "must be a positive integer")
	}
	return value, nil
}

// queryInt64 reads an optional positive identifier from the query string.
func queryInt64(r *http.Request, name string) (int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, domain.NewFieldError(name, "must be a positive integer")
	}
	return value, nil
}

// pageFromQuery builds the paging window from the query string.
func pageFromQuery(r *http.Request) (domain.Page, error) {
	query := r.URL.Query()
	page := domain.Page{
		SortBy: strings.TrimSpace(query.Get("sort_by")),
		Order:  domain.SortDirection(strings.TrimSpace(query.Get("order"))),
	}
	if raw := strings.TrimSpace(query.Get("page")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return domain.Page{}, domain.NewFieldError("page", "must be a positive integer")
		}
		page.Number = value
	}
	if raw := strings.TrimSpace(query.Get("page_size")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return domain.Page{}, domain.NewFieldError("page_size", "must be a positive integer")
		}
		page.Size = value
	}
	return page, nil
}

// enrollmentStatusesFromQuery parses the repeatable status filter.
func enrollmentStatusesFromQuery(r *http.Request) ([]domain.EnrollmentStatus, error) {
	raw := r.URL.Query()["status"]
	if len(raw) == 0 {
		return nil, nil
	}
	statuses := make([]domain.EnrollmentStatus, 0, len(raw))
	for _, entry := range raw {
		for _, part := range strings.Split(entry, ",") {
			trimmed := strings.ToLower(strings.TrimSpace(part))
			if trimmed == "" {
				continue
			}
			status := domain.EnrollmentStatus(trimmed)
			if !knownEnrollmentStatus(status) {
				return nil, domain.NewFieldError("status", fmt.Sprintf("%q is not an enrollment status", trimmed))
			}
			statuses = append(statuses, status)
		}
	}
	return statuses, nil
}

func knownEnrollmentStatus(status domain.EnrollmentStatus) bool {
	switch status {
	case domain.EnrollmentPending, domain.EnrollmentEnrolled, domain.EnrollmentWaitlisted,
		domain.EnrollmentDropped, domain.EnrollmentWithdrawn, domain.EnrollmentCompleted:
		return true
	default:
		return false
	}
}

func sectionStatusFromQuery(r *http.Request) (domain.SectionStatus, error) {
	raw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("section_status")))
	if raw == "" {
		return "", nil
	}
	status := domain.SectionStatus(raw)
	switch status {
	case domain.SectionDraft, domain.SectionOpen, domain.SectionClosed, domain.SectionCancelled:
		return status, nil
	default:
		return "", domain.NewFieldError("section_status", "is not a section status")
	}
}

func registrationStatusFromQuery(r *http.Request) (domain.RegistrationStatus, error) {
	raw := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if raw == "" {
		return "", nil
	}
	status := domain.RegistrationStatus(raw)
	switch status {
	case domain.RegistrationDraft, domain.RegistrationSubmitted,
		domain.RegistrationVerified, domain.RegistrationRejected:
		return status, nil
	default:
		return "", domain.NewFieldError("status", "is not a registration status")
	}
}

func boolFromQuery(r *http.Request, name string) (bool, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, domain.NewFieldError(name, "must be true or false")
	}
	return value, nil
}
