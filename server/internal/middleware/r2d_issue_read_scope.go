package middleware

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// tryR2DIssueReadScope handles read surfaces whose Project identity is carried
// inside a JSON request body. The issue-table API is a GET-with-body contract,
// so the ordinary URL-only Project scope cannot authorize a foreign
// collaborator before the Workspace membership middleware runs.
//
// Only explicit Project scopes are widened. Workspace-scoped table queries keep
// the caller's active Workspace and are filtered natively by the handler. This
// avoids mixing status/property/Agent/Squad semantics from different
// Workspaces in one table query.
func tryR2DIssueReadScope(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return false // Agent/Squad execution policy remains P06.
	}
	if r.Method != http.MethodGet || !r2dIssueTableReadPath(r.URL.Path) {
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
