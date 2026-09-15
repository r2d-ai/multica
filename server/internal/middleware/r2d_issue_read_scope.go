package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// tryR2DIssueReadScope handles read surfaces whose Project identity is carried
// either in a read-query filter or inside a JSON request body. Only explicit
// Project scopes are widened before Workspace membership. Workspace-scoped
// views stay on the caller's active Workspace and are ACL-filtered natively.
func tryR2DIssueReadScope(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return false // Agent/Squad execution policy remains P06.
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	if r.Method == http.MethodGet && path == "/api/issues/child-progress" {
		return r2dServeChildIssueProgress(queries, w, r, userID)
	}
	if r.Method == http.MethodGet && path == "/api/issues/children" {
		return r2dServeBatchIssueChildren(queries, w, r, next, userID)
	}
	if r.Method == http.MethodGet && r2dDirectIssueChildrenPath(path) {
		return r2dServeDirectIssueChildren(queries, w, r, next, userID, path)
	}
	if r.Method == http.MethodGet && path == "/api/issues/search" {
		projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
		if projectID == "" {
			return false
		}
		if _, err := parseR2DUUID(projectID); err != nil {
			return false // preserve the handler's canonical malformed-filter response
		}
		return r2dServeProjectSearch(queries, w, r, next, userID, projectID)
	}
	if r.Method == http.MethodGet && path == "/api/issues/grouped" {
		projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
		if projectID == "" {
			return false
		}
		if _, err := parseR2DUUID(projectID); err != nil {
			return false // preserve the handler's canonical malformed-filter response
		}
		ownerWorkspaceID, _, handled := r2dRequireProjectOperation(
			queries, w, r, userID, projectID, r2dauth.OperationRead,
		)
		if handled {
			return true
		}
		ctx := SetWorkspaceIDContext(r.Context(), ownerWorkspaceID)
		next.ServeHTTP(w, r.WithContext(ctx))
		return true
	}

	if r.Method != http.MethodPost || !r2dIssueTableReadPath(path) {
		return false
	}

	projectID, ok := r2dIssueTableProjectScope(r)
	if !ok {
		return false // workspace scope or malformed body: ordinary handler owns it.
	}
	ownerWorkspaceID, _, handled := r2dRequireProjectOperation(
		queries, w, r, userID, projectID, r2dauth.OperationRead,
	)
	if handled {
		return true
	}

	ctx := SetWorkspaceIDContext(r.Context(), ownerWorkspaceID)
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}

func r2dDirectIssueChildrenPath(path string) bool {
	if !strings.HasPrefix(path, "/api/issues/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/issues/"), "/")
	if len(parts) != 2 || parts[1] != "children" {
		return false
	}
	_, err := parseR2DUUID(parts[0])
	return err == nil
}

func r2dServeDirectIssueChildren(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID, path string) bool {
	issueID := strings.TrimSuffix(strings.TrimPrefix(path, "/api/issues/"), "/children")
	if _, err := parseR2DUUID(issueID); err != nil {
		return false
	}
	target, err := queries.R2DLoadIssueACLTarget(r.Context(), issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize issue")
		return true
	}

	ownerWorkspaceID := target.WorkspaceID
	if target.ProjectID != "" {
		var handled bool
		ownerWorkspaceID, _, handled = r2dRequireProjectOperation(
			queries, w, r, userID, target.ProjectID, r2dauth.OperationRead,
		)
		if handled {
			return true
		}
		if ownerWorkspaceID != target.WorkspaceID {
			writeError(w, http.StatusNotFound, "issue not found")
			return true
		}
	}

	_, isMember, err := r2dLoadWorkspaceMember(queries, r, userID, ownerWorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	allowProjectless := isMember
	if !allowProjectless {
		allowProjectless, err = queries.R2DIsGlobalObserver(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize observer")
			return true
		}
	}
	if target.ProjectID == "" && !allowProjectless {
		return false // preserve Workspace-private non-disclosure for the parent
	}

	buf := newR2DResponseBuffer()
	ctx := SetWorkspaceIDContext(r.Context(), ownerWorkspaceID)
	next.ServeHTTP(buf, r.WithContext(ctx))
	if buf.status < 200 || buf.status >= 300 {
		copyR2DResponse(w, buf, buf.body.Bytes())
		return true
	}
	filtered, err := filterR2DIssueCollectionScoped(r.Context(), queries, userID, buf.body.Bytes(), allowProjectless, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to apply project visibility")
		return true
	}
	copyR2DResponse(w, buf, filtered)
	return true
}

func r2dServeChildIssueProgress(queries *db.Queries, w http.ResponseWriter, r *http.Request, userID string) bool {
	workspaceID := ResolveWorkspaceIDFromRequest(r, queries)
	if workspaceID == "" {
		return false
	}
	_, isMember, err := r2dLoadWorkspaceMember(queries, r, userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	if !isMember {
		observer, err := queries.R2DIsGlobalObserver(r.Context(), userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize observer")
			return true
		}
		if !observer {
			return false // preserve the ordinary Workspace non-disclosure response
		}
	}

	readableProjectIDs, err := r2dReadableWorkspaceProjectIDs(queries, r, userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to apply project visibility")
		return true
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace_id")
		return true
	}
	terminalStatusKeys, err := issuestatus.ExpandCategories(
		r.Context(), queries, wsUUID,
		[]string{issuestatus.CategoryDone, issuestatus.CategoryClosed},
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve status categories")
		return true
	}
	rows, err := queries.R2DChildIssueProgressVisible(r.Context(), workspaceID, readableProjectIDs, terminalStatusKeys)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get child issue progress")
		return true
	}

	type progressEntry struct {
		ParentIssueID string `json:"parent_issue_id"`
		Total         int64  `json:"total"`
		Done          int64  `json:"done"`
	}
	progress := make([]progressEntry, len(rows))
	for i, row := range rows {
		progress[i] = progressEntry{ParentIssueID: row.ParentIssueID, Total: row.Total, Done: row.Done}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"progress": progress})
	return true
}

func r2dIssueTableReadPath(path string) bool {
	path = strings.TrimSuffix(path, "/")
	switch path {
	case "/api/issues/table/rows", "/api/issues/table/groups", "/api/issues/table/facets":
		return true
	default:
		return false
	}
}

// r2dIssueTableProjectScope extracts only the authorization-bearing part of the
// table request. r2dReadJSONFields restores r.Body byte-for-byte, so the real
// handler still performs the full strict decode/validation after authorization.
func r2dIssueTableProjectScope(r *http.Request) (string, bool) {
	fields, err := r2dReadJSONFields(r)
	if err != nil {
		return "", false
	}
	rawQuery, ok := fields["query"]
	if !ok || r2dRawNull(rawQuery) {
		return "", false
	}
	var query struct {
		Scope struct {
			Kind      string `json:"kind"`
			ProjectID string `json:"project_id"`
		} `json:"scope"`
	}
	if err := json.Unmarshal(rawQuery, &query); err != nil {
		return "", false
	}
	if query.Scope.Kind != "project" {
		return "", false
	}
	projectID := strings.TrimSpace(query.Scope.ProjectID)
	if projectID == "" {
		return "", false
	}
	if _, err := parseR2DUUID(projectID); err != nil {
		return "", false
	}
	return projectID, true
}
