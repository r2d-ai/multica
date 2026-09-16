package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/r2dauth"
)

// ProjectResourceAccess protects the owner Workspace resource inventory from
// cross-Workspace Project collaborators. Project resources can contain repo
// URLs, daemon IDs and local filesystem paths; sharing a Project must not
// implicitly share that Workspace metadata.
//
// Task-token actors deliberately keep the existing execution path. P06 owns
// Agent/Squad execution policy; this middleware is the human Project UI/API
// boundary introduced by P05.
func (h *Handler) ProjectResourceAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		projectID, ok := projectResourceProjectID(r.URL.Path)
		if !ok || r.Header.Get("X-Actor-Source") == "task_token" {
			next.ServeHTTP(w, r)
			return
		}

		userID, ok := requireUserID(w, r)
		if !ok {
			return
		}
		caps, err := r2dauth.NewService(r2dauth.NewPostgresStore(h.DB)).Capabilities(r.Context(), userID, projectID)
		if err != nil {
			if errors.Is(err, r2dauth.ErrProjectNotFound) {
				writeError(w, http.StatusNotFound, "project not found")
			} else {
				writeError(w, http.StatusInternalServerError, "project resource authorization failed")
			}
			return
		}

		if status := projectResourceAccessStatus(caps, r.Method); status != 0 {
			if status == http.StatusNotFound {
				writeError(w, status, "project not found")
			} else {
				writeError(w, status, "project resource access denied")
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

// projectResourceProjectID recognizes only the Project resource collection and
// its children. Everything else passes through untouched.
func projectResourceProjectID(path string) (string, bool) {
	path = strings.TrimSuffix(path, "/")
	const prefix = "/api/projects/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] != "resources" {
		return "", false
	}
	return parts[0], true
}

// projectResourceAccessStatus is intentionally capability-driven: no role
// ranking is duplicated here. Read access requires explicit owner-Workspace
// resource visibility, while mutations additionally require Project manage.
// Zero means allow.
func projectResourceAccessStatus(caps r2dauth.ProjectCapabilities, method string) int {
	if !caps.Read {
		return http.StatusNotFound
	}
	if !caps.ViewResources {
		return http.StatusForbidden
	}
	if method != http.MethodGet && method != http.MethodHead && !caps.Manage {
		return http.StatusForbidden
	}
	return 0
}
