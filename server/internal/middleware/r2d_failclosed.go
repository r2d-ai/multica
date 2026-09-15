package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func r2dQueryIssuesOpenOnly(r *http.Request) bool {
	fields, err := r2dReadJSONFields(r)
	if err != nil {
		return true // malformed body stays fail-closed when hidden Projects exist
	}
	raw, ok := fields["open_only"]
	if !ok {
		return false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	return value == "true"
}

// r2dUnfilteredIssueSurface reports collection/aggregate paths that still rely
// on upstream workspace-wide SQL. Once a Workspace contains a Project the
// current user cannot read, forwarding these paths would either leak hidden
// rows through counts/facets or let batch writes bypass Project ACL.
func r2dUnfilteredIssueSurface(r *http.Request) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/issues" {
		return r.Method != http.MethodGet
	}
	if path == "/api/issues/search" {
		return r.Method != http.MethodGet
	}
	if path == "/api/issues/grouped" && r.Method == http.MethodGet {
		// P04-C2 injects readable project_ids before ListGroupedIssues builds
		// its SQL window, so group totals and pagination are ACL-native.
		return false
	}
	if path == "/api/issues/children" && r.Method == http.MethodGet {
		// Valid parent_ids are authorized and rewritten before Workspace
		// membership, then child rows are filtered by their own Project ACL.
		// Empty/malformed input is non-enumerating and can safely retain the
		// upstream handler's canonical response without the temporary guard.
		return false
	}
	if path == "/api/issues/query" && r.Method == http.MethodPost {
		// QueryIssues delegates to ListIssues after rebuilding the URL query.
		// P04-C2 rewrites the string-map body with readable project_ids first.
		// open_only is the exception because its legacy static query does not
		// consume project_ids; keep that mode fail-closed until it gets a native
		// predicate rather than weakening the boundary.
		return r2dQueryIssuesOpenOnly(r)
	}
	if r.Method == http.MethodPost {
		switch path {
		case "/api/issues/table/rows", "/api/issues/table/groups", "/api/issues/table/facets":
			// These POST endpoints are read-only query surfaces. P04-C2 compiles
			// them from an ACL-native membership predicate.
			return false
		}
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
	// Apply ACL filters before deciding whether the legacy surface still needs
	// the temporary guard. This mutates only Issue list/grouped/query filters;
	// all other routes are untouched.
	if err := r2dApplyIssueCollectionVisibility(queries, r, userID, workspaceID); err != nil {
		return false, err
	}
	if !r2dUnfilteredIssueSurface(r) {
		return false, nil
	}
	return r2dWorkspaceHasUnreadableProject(r.Context(), queries, userID, workspaceID)
}
