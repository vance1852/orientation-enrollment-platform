package httpapi

import (
	"net/http"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/httpx"
	"github.com/vance1852/orientation-enrollment-platform/internal/service"
)

type submitRegistrationRequest struct {
	StudentID      int64  `json:"student_id"`
	TermID         int64  `json:"term_id"`
	ProgramCode    string `json:"program_code"`
	AdvisorEmail   string `json:"advisor_email"`
	DormPreference string `json:"dorm_preference"`
}

func (a *API) handleSubmitRegistration(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	var payload submitRegistrationRequest
	if err := decodeJSON(r, &payload); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if payload.TermID <= 0 {
		httpx.WriteError(w, r, domain.NewFieldError("term_id", "must be a positive identifier"))
		return
	}
	registration, err := a.services.Registrations.Submit(r.Context(), actor, service.SubmitInput{
		StudentID:      payload.StudentID,
		TermID:         payload.TermID,
		ProgramCode:    payload.ProgramCode,
		AdvisorEmail:   payload.AdvisorEmail,
		DormPreference: payload.DormPreference,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, toRegistrationView(registration))
}

type decisionRequest struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

func (a *API) handleDecideRegistration(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	registrationID, err := pathInt64(r, "registrationID")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var payload decisionRequest
	if err := decodeJSON(r, &payload); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	registration, err := a.services.Registrations.Decide(r.Context(), actor, service.DecideInput{
		RegistrationID: registrationID,
		Status:         domain.RegistrationStatus(payload.Status),
		Note:           payload.Note,
	})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toRegistrationView(registration))
}

func (a *API) handleMyRegistration(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	termID, err := queryInt64(r, "term_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if termID == 0 {
		term, termErr := a.services.Catalog.CurrentTerm(r.Context())
		if termErr != nil {
			httpx.WriteError(w, r, termErr)
			return
		}
		termID = term.ID
	}
	registration, err := a.services.Registrations.Get(r.Context(), actor, actor.UserID, termID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toRegistrationView(registration))
}

func (a *API) handleListRegistrations(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	page, err := pageFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	termID, err := queryInt64(r, "term_id")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	status, err := registrationStatusFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	result, err := a.services.Registrations.List(r.Context(), actor, termID, status, page)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	views := make([]registrationView, 0, len(result.Items))
	for _, registration := range result.Items {
		views = append(views, toRegistrationView(registration))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, listEnvelope[registrationView]{Items: views, Meta: newMeta(result)})
}
