package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// tryR2DCommentScope runs before the ordinary Workspace membership lookup.
// Comment PUT/DELETE by-id routes live outside /api/issues/*, so the
// Project-ACL intercept in tryR2DProjectScope never fires for them. This
// override resolves comment -> Issue -> Project and admits the caller
// through the Project ACL when the user has at least contribute access,
// while keeping two boundaries intact:
//
//   - Projectless comments (issue has no Project) return false and stay on
//     the ordinary Workspace gate.
//   - Denials are a uniform 404, never 403, so the route is not an IDOR
//     oracle for comment ids that live in another Workspace.
//
// A Project grant only grants contribute (edit/delete own) — never
// moderation authority over other users' comments. The handler must still
// verify authorship after this middleware succeeds.
func tryR2DCommentScope(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return false
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		return false
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	if !strings.HasPrefix(path, "/api/comments/") {
		return false
	}
	rest := strings.TrimPrefix(path, "/api/comments/")
	parts := strings.SplitN(rest, "/", 2)
	commentID := parts[0]
	if commentID == "" {
		return false
	}
	// Sub-routes like /keep-replies, /resolve, /reactions are handled by
	// their own handlers which carry their own authorization.
	suffix := ""
	if len(parts) == 2 {
		suffix = "/" + parts[1]
	}
	if suffix != "" {
		return false
	}
	if _, err := parseR2DUUID(commentID); err != nil {
		return false
	}

	// Load the comment without workspace scope to discover its Issue binding.
	commentUUID, err := util.ParseUUID(commentID)
	if err != nil {
		return false
	}
	comment, err := queries.GetComment(r.Context(), commentUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Let the handler produce its canonical non-disclosing 404.
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize comment")
		return true
	}

	// Resolve comment -> Issue -> Project.
	target, err := queries.R2DLoadIssueACLTarget(r.Context(), util.UUIDToString(comment.IssueID))
	if errors.Is(err, pgx.ErrNoRows) {
		// Issue deleted after comment creation — fall through to workspace boundary.
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize comment")
		return true
	}
	if target.ProjectID == "" {
		return false // projectless issue: ordinary Workspace boundary.
	}

	// PUT and DELETE are mutations — require contribute access.
	ownerWorkspaceID, allowed, exists, err := r2dAuthorizeProject(queries, r, userID, target.ProjectID, r2dauth.OperationContribute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize comment")
		return true
	}
	if !exists || !allowed {
		_, readable, _, readErr := r2dAuthorizeProject(queries, r, userID, target.ProjectID, r2dauth.OperationRead)
		if readErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize comment")
			return true
		}
		if !readable {
			writeError(w, http.StatusNotFound, "comment not found")
		} else {
			writeError(w, http.StatusForbidden, "insufficient project permissions")
		}
		return true
	}
	if ownerWorkspaceID != target.WorkspaceID {
		writeError(w, http.StatusNotFound, "comment not found")
		return true
	}

	// Authorized via Project ACL. Set workspace context and mark the request
	// so handlers can enforce author-only writes (no admin/moderation bypass).
	ctx := SetWorkspaceIDContext(r.Context(), target.WorkspaceID)
	ctx = SetR2DProjectACL(ctx)
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}
