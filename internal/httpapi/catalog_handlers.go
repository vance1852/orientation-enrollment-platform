package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/httpx"
)

func (a *API) handleListTerms(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	terms, err := a.services.Catalog.ListTerms(r.Context(), actor)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	views := make([]termView, 0, len(terms))
	for _, term := range terms {
		views = append(views, toTermView(term))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, listEnvelope[termView]{
		Items: views,
		Meta:  pageMeta{Total: len(views), Page: 1, PageSize: len(views), TotalPages: 1},
	})
}

func (a *API) handleListSections(w http.ResponseWriter, r *http.Request, _ domain.Principal) {
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
	status, err := sectionStatusFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	onlyOpen, err := boolFromQuery(r, "only_open")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}

	filter := domain.SectionFilter{
		TermID:     termID,
		CourseCode: strings.TrimSpace(r.URL.Query().Get("course_code")),
		Department: strings.TrimSpace(r.URL.Query().Get("department")),
		Status:     status,
		OnlyOpen:   onlyOpen,
		Instructor: strings.TrimSpace(r.URL.Query().Get("instructor")),
		Page:       page,
	}
	result, err := a.services.Catalog.ListSections(r.Context(), filter)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	views := make([]sectionView, 0, len(result.Items))
	for _, section := range result.Items {
		views = append(views, toSectionView(section))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, listEnvelope[sectionView]{Items: views, Meta: newMeta(result)})
}

func (a *API) handleGetSection(w http.ResponseWriter, r *http.Request, _ domain.Principal) {
	sectionID, err := pathInt64(r, "sectionID")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	section, err := a.services.Catalog.GetSection(r.Context(), sectionID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, toSectionView(section))
}

func (a *API) handleSectionRoster(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	sectionID, err := pathInt64(r, "sectionID")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	page, err := pageFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	result, err := a.services.Catalog.SectionRoster(r.Context(), actor, sectionID, page)
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

func (a *API) handleListAuditEvents(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	page, err := pageFromQuery(r)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	filter := domain.AuditFilter{
		Action:     strings.TrimSpace(r.URL.Query().Get("action")),
		ObjectType: strings.TrimSpace(r.URL.Query().Get("object_type")),
		ObjectID:   strings.TrimSpace(r.URL.Query().Get("object_id")),
		Page:       page,
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		since, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			httpx.WriteError(w, r, domain.NewFieldError("since", "must be an RFC3339 timestamp"))
			return
		}
		filter.Since = &since
	}
	if actorID, queryErr := queryInt64(r, "actor_user_id"); queryErr != nil {
		httpx.WriteError(w, r, queryErr)
		return
	} else if actorID > 0 {
		filter.ActorUserID = &actorID
	}

	result, err := a.services.Catalog.ListAuditEvents(r.Context(), actor, filter)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	views := make([]auditEventView, 0, len(result.Items))
	for _, event := range result.Items {
		views = append(views, toAuditEventView(event))
	}
	_ = httpx.WriteJSON(w, http.StatusOK, listEnvelope[auditEventView]{Items: views, Meta: newMeta(result)})
}
