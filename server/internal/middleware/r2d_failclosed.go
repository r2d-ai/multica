package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// r2dUnfilteredIssueSurface reports collection/aggregate paths that still rely
// on upstream workspace-wide SQL. Once a Workspace contains a Project the
// current user cannot read, forwarding these paths would either leak hidden
// rows through counts/facets or let batch writes bypass Project ACL.
func r2dUnfilteredIssueSurface(r *http.Request) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/issues" {
		// GET is filtered by serveR2DFilteredCollection (or narrowed by an
		// explicit project_id in tryR2DProjectScope). Mutating collection
		// operations need ACL-native body/ID evaluation before they are safe.
		return r.Method != http.MethodGet
	}
	if path == "/api/issues/search" {
		return r.Method != http.MethodGet
	}
	if !strings.HasPrefix(path, "/api/issues/") {
		return false
	}

	first := strings.SplitN(strings.TrimPrefix(path, "/api/issues/"), "/", 2)[0]
	if first == "" {
		return false
	}
	// A UUID first segment is an entity route. Project-backed entity routes are
	// authorized before Workspace membership; projectless entities legitimately
	// retain the Workspace boundary. Everything else here is a collection,
	// aggregate, batch, or human-readable identifier surface and must wait for
	// ACL-native filtering.
	_, err := parseR2DUUID(first)
	return err != nil
}

func r2dWorkspaceHasUnreadableProject(ctx context.Context, queries *db.Queries, userID, workspaceID string) (bool, error) {
	facts, err := queries.R2DListWorkspaceProjectAccessFacts(ctx, userID, workspaceID)
	if err != nil {
		return false, err
	}
	for _, fact := range facts {
		if !r2dauth.Resolve(r2dFacts(fact)).Can(r2dauth.OperationRead) {
			return true, nil
		}
	}
	return false, nil
}

func shouldR2DFailClosed(queries *db.Queries, r *http.Request, userID, workspaceID string) (bool, error) {
	if !r2dUnfilteredIssueSurface(r) {
		return false, nil
	}
	return r2dWorkspaceHasUnreadableProject(r.Context(), queries, userID, workspaceID)
}
