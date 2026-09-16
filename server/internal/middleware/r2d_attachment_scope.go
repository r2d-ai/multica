package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// tryR2DAttachmentScope runs before the ordinary Workspace membership lookup.
// Attachment by-id is workspace-scoped upstream, so a foreign user holding an
// explicit Project grant cannot reach an attachment on a shared Project's Issue
// at all — the Workspace membership check denies them. This override resolves
// attachment -> Issue -> Project and admits the caller through the Project ACL
// instead, while keeping two boundaries intact:
//
//   - Projectless attachments (chat, avatar, unbound upload, or an Issue with
//     no Project) return false and stay on the ordinary Workspace gate.
//   - Denials are a uniform 404, never 403, so the route is not an IDOR oracle
//     for attachment ids that live in another Workspace.
//
// Reads require Project read; DELETE requires Project manage. A caller who is
// a member of the owning Workspace falls back to the ordinary boundary so the
// existing uploader/Workspace-admin delete rules are unchanged.
func tryR2DAttachmentScope(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodDelete {
		return false
	}

	path := strings.TrimSuffix(r.URL.Path, "/")
	if !strings.HasPrefix(path, "/api/attachments/") {
		return false
	}
	rest := strings.TrimPrefix(path, "/api/attachments/")
	parts := strings.SplitN(rest, "/", 2)
	attachmentID := parts[0]
	suffix := ""
	if len(parts) == 2 {
		suffix = "/" + parts[1]
	}
	// Only the metadata read and the text-preview proxy are Project-aware here.
	// The download and signed-download routes self-resolve their workspace and
	// enforce the same ACL inside the handler.
	if suffix != "" && suffix != "/content" {
		return false
	}
	if r.Method == http.MethodDelete && suffix != "" {
		return false
	}
	if _, err := parseR2DUUID(attachmentID); err != nil {
		return false
	}

	target, err := queries.R2DLoadAttachmentACLTarget(r.Context(), attachmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Let the handler produce its canonical non-disclosing 404.
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize attachment")
		return true
	}
	if target.ProjectID == "" {
		return false // Projectless attachment: ordinary Workspace boundary.
	}

	op := r2dauth.OperationRead
	if r.Method == http.MethodDelete {
		op = r2dauth.OperationManage
	}
	ownerWorkspaceID, allowed, exists, err := r2dAuthorizeProject(queries, r, userID, target.ProjectID, op)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize attachment")
		return true
	}
	if exists && allowed && ownerWorkspaceID == target.WorkspaceID {
		member, hasMember, memberErr := r2dLoadWorkspaceMember(queries, r, userID, target.WorkspaceID)
		if memberErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
			return true
		}
		r2dServeAuthorizedWorkspace(w, r, next, target.WorkspaceID, member, hasMember)
		return true
	}

	// Denied through the Project ACL. Owner-Workspace members keep the ordinary
	// boundary (so uploader/Workspace-admin delete semantics do not change);
	// every other caller gets the same 404 as a missing attachment.
	_, hasMember, memberErr := r2dLoadWorkspaceMember(queries, r, userID, target.WorkspaceID)
	if memberErr != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	if hasMember {
		return false
	}
	writeError(w, http.StatusNotFound, "attachment not found")
	return true
}
