package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/r2dsharing"
)

type updateProjectSharingRequest struct {
	Visibility string `json:"visibility"`
}

type createProjectGrantRequest struct {
	PrincipalType string `json:"principal_type"`
	PrincipalID   string `json:"principal_id"`
	Role          string `json:"role"`
}

type updateProjectGrantRequest struct {
	Role string `json:"role"`
}

func (h *Handler) projectSharingService() *r2dsharing.Service {
	authz := r2dauth.NewService(r2dauth.NewPostgresStore(h.DB))
	return r2dsharing.NewService(r2dsharing.NewPostgresStore(h.DB), authz)
}

func projectSharingRequestContext(w http.ResponseWriter, r *http.Request) (userID, projectID string, ok bool) {
	userID, ok = requireUserID(w, r)
	if !ok {
		return "", "", false
	}
	projectID = chi.URLParam(r, "id")
	if _, ok = parseUUIDOrBadRequest(w, projectID, "project id"); !ok {
		return "", "", false
	}
	return userID, projectID, true
}

func writeProjectSharingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, r2dsharing.ErrInvalidQuery):
		writeError(w, http.StatusBadRequest, "invalid project sharing request")
	case errors.Is(err, r2dsharing.ErrForbidden):
		writeError(w, http.StatusForbidden, "project manager permission required")
	case errors.Is(err, r2dauth.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "project not found")
	case errors.Is(err, r2dsharing.ErrGrantNotFound):
		writeError(w, http.StatusNotFound, "project grant not found")
	case errors.Is(err, r2dsharing.ErrPrincipalMissing):
		writeError(w, http.StatusNotFound, "grant principal not found")
	case errors.Is(err, r2dsharing.ErrDuplicateGrant):
		writeError(w, http.StatusConflict, "project grant already exists")
	default:
		writeError(w, http.StatusInternalServerError, "project sharing operation failed")
	}
}

func (h *Handler) GetProjectSharing(w http.ResponseWriter, r *http.Request) {
	userID, projectID, ok := projectSharingRequestContext(w, r)
	if !ok {
		return
	}
	sharing, err := h.projectSharingService().Get(r.Context(), userID, projectID)
	if err != nil {
		writeProjectSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sharing)
}

func (h *Handler) UpdateProjectSharing(w http.ResponseWriter, r *http.Request) {
	userID, projectID, ok := projectSharingRequestContext(w, r)
	if !ok {
		return
	}
	var req updateProjectSharingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.projectSharingService().SetVisibility(r.Context(), userID, projectID, r2dauth.Visibility(req.Visibility)); err != nil {
		writeProjectSharingError(w, err)
		return
	}
	sharing, err := h.projectSharingService().Get(r.Context(), userID, projectID)
	if err != nil {
		writeProjectSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sharing)
}

func (h *Handler) CreateProjectGrant(w http.ResponseWriter, r *http.Request) {
	userID, projectID, ok := projectSharingRequestContext(w, r)
	if !ok {
		return
	}
	var req createProjectGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	grant, err := h.projectSharingService().CreateGrant(
		r.Context(), userID, projectID, randomID(),
		r2dsharing.PrincipalType(req.PrincipalType), req.PrincipalID, req.Role,
	)
	if err != nil {
		writeProjectSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, grant)
}

func (h *Handler) UpdateProjectGrant(w http.ResponseWriter, r *http.Request) {
	userID, projectID, ok := projectSharingRequestContext(w, r)
	if !ok {
		return
	}
	grantID := chi.URLParam(r, "grantId")
	if grantID == "" {
		writeError(w, http.StatusBadRequest, "grant id is required")
		return
	}
	var req updateProjectGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	grant, err := h.projectSharingService().UpdateGrantRole(r.Context(), userID, projectID, grantID, req.Role)
	if err != nil {
		writeProjectSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

func (h *Handler) DeleteProjectGrant(w http.ResponseWriter, r *http.Request) {
	userID, projectID, ok := projectSharingRequestContext(w, r)
	if !ok {
		return
	}
	grantID := chi.URLParam(r, "grantId")
	if grantID == "" {
		writeError(w, http.StatusBadRequest, "grant id is required")
		return
	}
	if err := h.projectSharingService().DeleteGrant(r.Context(), userID, projectID, grantID); err != nil {
		writeProjectSharingError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) SearchProjectSharingDirectory(w http.ResponseWriter, r *http.Request) {
	userID, projectID, ok := projectSharingRequestContext(w, r)
	if !ok {
		return
	}
	principalType := r2dsharing.PrincipalType(r.URL.Query().Get("type"))
	query := r.URL.Query().Get("q")
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = parsed
	}
	principals, err := h.projectSharingService().SearchDirectory(r.Context(), userID, projectID, principalType, query, limit)
	if err != nil {
		writeProjectSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"principals": principals})
}
