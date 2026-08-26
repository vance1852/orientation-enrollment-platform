package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/httpx"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

// IdempotencyHeader carries the client supplied replay protection key.
const IdempotencyHeader = "Idempotency-Key"

type claimRequest struct {
	StudentID int64 `json:"student_id"`
	SectionID int64 `json:"section_id"`
}

type claimResponse struct {
	Enrollment enrollmentView `json:"enrollment"`
	Section    sectionView    `json:"section"`
	Waitlisted bool           `json:"waitlisted"`
}

// handleClaimEnrollment claims a seat. When the caller supplies an idempotency
// key the stored response of the first attempt is replayed byte for byte instead
// of allocating a second seat.
func (a *API) handleClaimEnrollment(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	raw, err := rawBody(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader([]byte(raw)))

	var payload claimRequest
	if err := decodeJSON(r, &payload); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if payload.SectionID <= 0 {
		httpx.WriteError(w, r, domain.NewFieldError("section_id", "must be a positive identifier"))
		return
	}

	scope := service.Scope{
		ActorUserID: actor.UserID,
		Method:      r.Method,
		Path:        r.URL.Path,
		Key:         strings.TrimSpace(r.Header.Get(IdempotencyHeader)),
		Payload:     raw,
	}
	outcome, err := a.services.Idempotency.Execute(r.Context(), scope, func(ctx context.Context) (int, string, error) {
		result, claimErr := a.services.Enrollments.Claim(ctx, actor, service.ClaimInput{
			StudentID: payload.StudentID,
			SectionID: payload.SectionID,
		})
		if claimErr != nil {
			return 0, "", claimErr
		}
		body, encodeErr := json.Marshal(claimResponse{
			Enrollment: toEnrollmentView(result.Enrollment),
			Section:    toSectionView(result.Section),
			Waitlisted: result.Waitlisted,
		})
		if encodeErr != nil {
			return 0, "", encodeErr
		}
		return http.StatusCreated, string(body), nil
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if outcome.Replayed {
		w.Header().Set("Idempotent-Replay", "true")
	}
	_ = httpx.WriteRawJSON(w, outcome.Status, outcome.Body)
}

type batchClaimRequest struct {
	StudentID  int64   `json:"student_id"`
	SectionIDs []int64 `json:"section_ids"`
}

type batchItemView struct {
	SectionID int64  `json:"section_id"`
	Succeeded bool   `json:"succeeded"`
	Status    string `json:"status"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

type batchClaimResponse struct {
	Items     []batchItemView `json:"items"`
	Succeeded int             `json:"succeeded"`
	Failed    int             `json:"failed"`
}

// handleBatchClaim claims several sections and always answers 207 style content:
// each item carries its own outcome so a partial failure is visible.
func (a *API) handleBatchClaim(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	var payload batchClaimRequest
	if err := decodeJSON(r, &payload); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	results, err := a.services.Enrollments.BatchClaim(r.Context(), actor, payload.StudentID, payload.SectionIDs)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	response := batchClaimResponse{Items: make([]batchItemView, 0, len(results))}
	for _, item := range results {
		if item.Succeeded {
			response.Succeeded++
		} else {
			response.Failed++
		}
		response.Items = append(response.Items, batchItemView{
			SectionID: item.SectionID,
			Succeeded: item.Succeeded,
			Status:    string(item.Status),
			Code:      item.Code,
			Message:   item.Message,
		})
	}
	status := http.StatusOK
	if response.Failed > 0 && response.Succeeded > 0 {
		status = http.StatusMultiStatus
	}
	_ = httpx.WriteJSON(w, status, response)
}

func (a *API) handleListEnrollments(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	page, err := pageFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	studentID, err := queryInt64(r, "student_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	termID, err := queryInt64(r, "term_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	sectionID, err := queryInt64(r, "section_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	statuses, err := enrollmentStatusesFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	result, err := a.services.Enrollments.List(r.Context(), actor, domain.EnrollmentFilter{
		StudentID: studentID,
		TermID:    termID,
		SectionID: sectionID,
		Statuses:  statuses,
		Page:      page,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	views := make([]enrollmentView, 0, len(result.Items))
	for _, enrollment := range result.Items {
		views = append(views, toEnrollmentView(enrollment))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, listEnvelope[enrollmentView]{Items: views, Meta: newMeta(result)})
}

func (a *API) handleGetEnrollment(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	enrollmentID, err := pathInt64(r, "enrollmentID")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	enrollment, err := a.services.Enrollments.Get(r.Context(), actor, enrollmentID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toEnrollmentView(enrollment))
}

func (a *API) handleDropEnrollment(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	enrollmentID, err := pathInt64(r, "enrollmentID")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	enrollment, err := a.services.Enrollments.Drop(r.Context(), actor, enrollmentID, reason)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toEnrollmentView(enrollment))
}
