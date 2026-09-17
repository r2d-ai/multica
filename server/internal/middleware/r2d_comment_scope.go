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
// override resolves comment -> Issue -> Project and admits a foreign Project
// collaborator through the Project ACL when they have contribute access,
// while keeping three boundaries intact:
//
//   - Projectless comments (issue has no Project) return false and stay on
//     the ordinary Workspace gate.
//   - An owner-Workspace member always falls back to the ordinary gate, so
//     native author-only edits and owner/admin moderation semantics are
//     unchanged. A Project reach is not required to keep them.
//   - A caller with no owner-Workspace member row — a foreign collaborator —
//     is denied with a uniform 404, never 403, so the route is not an IDOR
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
	suffix := ""
	if len(parts) == 2 {
		suffix = "/" + parts[1]
	}
	// DELETE /api/comments/{id}/keep-replies is the modern client's delete and
	// shares DeleteComment's authorization. Every other sub-route owns its own
	// handler and its own authorization.
	if suffix != "" && !(r.Method == http.MethodDelete && suffix == "/keep-replies") {
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
		// Let the ordinary gate produce its canonical non-disclosing 404.
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize comment")
		return true
	}

	// Resolve comment -> Issue -> Project from authoritative server state. The
	// caller cannot supply any of these ids.
	target, err := queries.R2DLoadIssueACLTarget(r.Context(), util.UUIDToString(comment.IssueID))
	if errors.Is(err, pgx.ErrNoRows) {
		// Issue deleted after comment creation — fall through to Workspace gate.
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize comment")
		return true
	}
	if target.ProjectID == "" {
		return false // projectless issue: ordinary Workspace boundary.
	}

	// Owner-Workspace members keep the ordinary boundary. This preserves the
	// existing author/owner/admin semantics on the native path and stops a
	// Project reach from narrowing what a Workspace member could already do.
	_, hasMember, err := r2dLoadWorkspaceMember(queries, r, userID, target.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	if hasMember {
		return false
	}

	// PUT/DELETE are mutations — require current contribute access.
	ownerWorkspaceID, allowed, exists, err := r2dAuthorizeProject(queries, r, userID, target.ProjectID, r2dauth.OperationContribute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize comment")
		return true
	}
	// No grant, revoked grant, disabled principal, or a corrupt Project row all
	// fail closed with the same non-disclosing 404 as a missing comment.
	if !exists || !allowed || ownerWorkspaceID != target.WorkspaceID {
		writeError(w, http.StatusNotFound, "comment not found")
		return true
	}

	// Authorized via the Project ACL. Set the owning Workspace and mark the
	// request so the handler enforces authorship without a member lookup.
	ctx := SetWorkspaceIDContext(r.Context(), target.WorkspaceID)
	ctx = SetR2DProjectACL(ctx)
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}
