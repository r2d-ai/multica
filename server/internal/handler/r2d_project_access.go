package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/r2dcore"
)

type projectAccessMetadata struct {
	EffectiveRole  string `json:"effective_role,omitempty"`
	GlobalObserver bool   `json:"global_observer,omitempty"`
	Visibility     string `json:"visibility"`
}

type accessibleProjectResponse struct {
	ProjectResponse
	OwnerWorkspace r2dcore.WorkspaceSummary `json:"owner_workspace"`
	Access         projectAccessMetadata     `json:"access"`
}

func (h *Handler) r2dProjectAuthorizer() (*r2dauth.PostgresStore, *r2dauth.Service) {
	store := r2dauth.NewPostgresStore(h.DB)
	return store, r2dauth.NewService(store)
}

func (h *Handler) r2dAccessibleProjectResponse(record r2dcore.ProjectRecord, facts r2dauth.ProjectFacts) accessibleProjectResponse {
	decision := r2dauth.Resolve(facts)
	resp := projectToResponse(record.Project)
	resp.IssueCount, resp.DoneCount = h.loadProjectIssueStats(
		contextBackgroundIfNil(nil), record.Project.WorkspaceID, record.Project.ID,
	)
	// Resource contents remain workspace-only in P04. A count is metadata only;
	// it does not expose resource refs, local paths, repo URLs, or credentials.
	resp.ResourceCount = h.loadProjectResourceCount(contextBackgroundIfNil(nil), record.Project.ID)
	return accessibleProjectResponse{
		ProjectResponse: resp,
		OwnerWorkspace: record.OwnerWorkspace,
		Access: projectAccessMetadata{
			EffectiveRole:  string(decision.Role),
			GlobalObserver: decision.GlobalObserver,
			Visibility:     string(facts.Visibility),
		},
	}
}

// contextBackgroundIfNil exists only to keep r2dAccessibleProjectResponse a
// pure formatting helper when callers do not pass a request context. Prefer the
// request-aware variant below for all HTTP paths.
func contextBackgroundIfNil(ctx interface{ Done() <-chan struct{} }) context.Context {
	if real, ok := any(ctx).(context.Context); ok && real != nil {
		return real
	}
	return context.Background()
}

func (h *Handler) r2dAccessibleProjectResponseWithContext(ctx context.Context, record r2dcore.ProjectRecord, facts r2dauth.ProjectFacts) accessibleProjectResponse {
	decision := r2dauth.Resolve(facts)
	resp := projectToResponse(record.Project)
	resp.IssueCount, resp.DoneCount = h.loadProjectIssueStats(ctx, record.Project.WorkspaceID, record.Project.ID)
	resp.ResourceCount = h.loadProjectResourceCount(ctx, record.Project.ID)
	return accessibleProjectResponse{
		ProjectResponse: resp,
		OwnerWorkspace: record.OwnerWorkspace,
		Access: projectAccessMetadata{
			EffectiveRole:  string(decision.Role),
			GlobalObserver: decision.GlobalObserver,
			Visibility:     string(facts.Visibility),
		},
	}
}

func (h *Handler) ListAccessibleProjects(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	if h.DB == nil {
		writeError(w, http.StatusInternalServerError, "database is unavailable")
		return
	}

	status := r.URL.Query().Get("status")
	if status != "" && !projectEnumContains(validProjectStatuses, status) {
		writeError(w, http.StatusBadRequest, "invalid project status")
		return
	}
	priority := r.URL.Query().Get("priority")
	if priority != "" && !projectEnumContains(validProjectPriorities, priority) {
		writeError(w, http.StatusBadRequest, "invalid project priority")
		return
	}
	limit := 50
	offset := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = value
	}
	if limit > 100 {
		limit = 100
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeError(w, http.StatusBadRequest, "invalid offset")
			return
		}
		offset = value
	}

	factStore, _ := h.r2dProjectAuthorizer()
	facts, err := factStore.ListCandidateProjectFacts(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve visible projects")
		return
	}

	visibleIDs := make([]string, 0, len(facts))
	factsByID := make(map[string]r2dauth.ProjectFacts, len(facts))
	for _, fact := range facts {
		decision := r2dauth.Resolve(fact)
		if !decision.Can(r2dauth.OperationRead) {
			continue
		}
		visibleIDs = append(visibleIDs, fact.ProjectID)
		factsByID[fact.ProjectID] = fact
	}

	records, err := r2dcore.NewProjectStore(h.DB).List(r.Context(), visibleIDs, r2dcore.ProjectFilter{
		Query:    r.URL.Query().Get("q"),
		Status:   status,
		Priority: priority,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list projects")
		return
	}

	resp := make([]accessibleProjectResponse, 0, len(records))
	for _, record := range records {
		fact, ok := factsByID[uuidToString(record.Project.ID)]
		if !ok {
			continue
		}
		resp = append(resp, h.r2dAccessibleProjectResponseWithContext(r.Context(), record, fact))
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": resp, "total": len(resp)})
}

func (h *Handler) SearchAccessibleProjects(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.URL.Query().Get("q")) == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return
	}
	h.ListAccessibleProjects(w, r)
}

func (h *Handler) GetAccessibleProject(w http.ResponseWriter, r *http.Request) {
	userID, projectID, facts, ok := h.requireR2DProjectOperation(w, r, r2dauth.OperationRead)
	_ = userID
	if !ok {
		return
	}
	record, err := r2dcore.NewProjectStore(h.DB).Get(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	writeJSON(w, http.StatusOK, h.r2dAccessibleProjectResponseWithContext(r.Context(), record, facts))
}

func (h *Handler) UpdateAccessibleProject(w http.ResponseWriter, r *http.Request) {
	_, _, facts, ok := h.requireR2DProjectOperation(w, r, r2dauth.OperationManage)
	if !ok {
		return
	}

	// Cross-workspace managers may manage the Project shell, but Agent/Squad
	// references belong to the owner Workspace. Until P06 defines that policy,
	// reject lead changes from callers who are not members of the owner Team.
	body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if facts.OwnerWorkspaceRole == "" {
		if _, hasType := raw["lead_type"]; hasType {
			writeError(w, http.StatusForbidden, "cross-workspace managers cannot change project lead until Agent/Squad policy is enabled")
			return
		}
		if _, hasID := raw["lead_id"]; hasID {
			writeError(w, http.StatusForbidden, "cross-workspace managers cannot change project lead until Agent/Squad policy is enabled")
			return
		}
	}

	r2 := requestWithAuthoritativeWorkspace(r, facts.OwnerWorkspaceID)
	r2.Body = io.NopCloser(bytes.NewReader(body))
	h.UpdateProject(w, r2)
}

func (h *Handler) DeleteAccessibleProject(w http.ResponseWriter, r *http.Request) {
	_, _, facts, ok := h.requireR2DProjectOperation(w, r, r2dauth.OperationManage)
	if !ok {
		return
	}
	h.DeleteProject(w, requestWithAuthoritativeWorkspace(r, facts.OwnerWorkspaceID))
}

func (h *Handler) requireR2DProjectOperation(w http.ResponseWriter, r *http.Request, op r2dauth.Operation) (userID, projectID string, facts r2dauth.ProjectFacts, ok bool) {
	userID, ok = requireUserID(w, r)
	if !ok {
		return "", "", r2dauth.ProjectFacts{}, false
	}
	projectID = chi.URLParam(r, "id")
	if _, parsed := parseUUIDOrBadRequest(w, projectID, "project id"); !parsed {
		return "", "", r2dauth.ProjectFacts{}, false
	}
	if h.DB == nil {
		writeError(w, http.StatusInternalServerError, "database is unavailable")
		return "", "", r2dauth.ProjectFacts{}, false
	}
	store, _ := h.r2dProjectAuthorizer()
	facts, err := store.LoadProjectFacts(r.Context(), userID, projectID)
	if errors.Is(err, r2dauth.ErrProjectNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return "", "", r2dauth.ProjectFacts{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize project")
		return "", "", r2dauth.ProjectFacts{}, false
	}
	if !r2dauth.Resolve(facts).Can(op) {
		// 404 for read prevents project-id probing; mutation paths use 403 once
		// the user already has enough context to target a visible Project.
		if op == r2dauth.OperationRead {
			writeError(w, http.StatusNotFound, "project not found")
		} else {
			writeError(w, http.StatusForbidden, "insufficient project permission")
		}
		return "", "", r2dauth.ProjectFacts{}, false
	}
	return userID, projectID, facts, true
}

func requestWithAuthoritativeWorkspace(r *http.Request, workspaceID string) *http.Request {
	clone := r.Clone(r.Context())
	clone.Header = r.Header.Clone()
	clone.Header.Set("X-Workspace-ID", workspaceID)
	clone.Header.Del("X-Workspace-Slug")
	return clone
}

func projectEnumContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
