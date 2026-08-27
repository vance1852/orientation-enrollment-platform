package httpapi

import (
	"net/http"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/domain"
	"github.com/vance1852/orientation-enrollment-platform/internal/httpx"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expires_at"`
	UserID      int64     `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
}

func (a *API) handleLogin(w http.ResponseWriter, r *http.Request) {
	var payload loginRequest
	if err := decodeJSON(r, &payload); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	result, err := a.services.Auth.Login(r.Context(), payload.Email, payload.Password, r.UserAgent())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, loginResponse{
		Token:       result.Token,
		ExpiresAt:   result.ExpiresAt,
		UserID:      result.Principal.UserID,
		DisplayName: result.Principal.DisplayName,
		Role:        string(result.Principal.Role),
	})
}

func (a *API) handleLogout(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	if err := a.services.Auth.Logout(r.Context(), actor); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusNoContent, nil)
}

type profileResponse struct {
	UserID         int64  `json:"user_id"`
	Email          string `json:"email"`
	DisplayName    string `json:"display_name"`
	Role           string `json:"role"`
	ActiveSessions int    `json:"active_sessions"`
}

func (a *API) handleProfile(w http.ResponseWriter, r *http.Request, actor domain.Principal) {
	sessions, err := a.services.Auth.ActiveSessionCount(r.Context(), actor)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_ = httpx.WriteJSON(w, http.StatusOK, profileResponse{
		UserID:         actor.UserID,
		Email:          actor.Email,
		DisplayName:    actor.DisplayName,
		Role:           string(actor.Role),
		ActiveSessions: sessions,
	})
}
