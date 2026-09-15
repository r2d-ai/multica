package middleware

import (
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const r2dBatchIssueChildrenLimit = 200

// r2dParseIssueParentIDs mirrors the handler's permissive comma-separated input
// while deduplicating before ACL lookups. Invalid/oversized input is left to the
// upstream handler so its canonical 400 response stays authoritative.
func r2dParseIssueParentIDs(raw string) ([]string, bool) {
	parts := strings.Split(raw, ",")
	if len(parts) > r2dBatchIssueChildrenLimit {
		return nil, false
	}
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, err := parseR2DUUID(id); err != nil {
			return nil, false
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, true
}

// r2dServeBatchIssueChildren authorizes parent_ids before the Workspace gate,
// derives exactly one owner Workspace, lets the upstream batched handler load
// children, then filters those child rows again by their own Project ACL. This
// double boundary matters because data corruption/legacy rows can put a child
// in a different Project from its parent; the parent grant must never widen to
// an unrelated private Project.
func r2dServeBatchIssueChildren(queries *db.Queries, w http.ResponseWriter, r *http.Request, next http.Handler, userID string) bool {
	raw := strings.TrimSpace(r.URL.Query().Get("parent_ids"))
	if raw == "" {
		return false // preserve upstream empty-input semantics under Workspace gate
	}
	parentIDs, ok := r2dParseIssueParentIDs(raw)
	if !ok {
		return false // upstream owns canonical validation errors
	}
	if len(parentIDs) == 0 {
		return false
	}

	targets, err := queries.R2DListIssueACLTargets(r.Context(), parentIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize parent issues")
		return true
	}

	projectIDs := make([]string, 0, len(targets))
	projectSeen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target.ProjectID == "" {
			continue
		}
		if _, ok := projectSeen[target.ProjectID]; ok {
			continue
		}
		projectSeen[target.ProjectID] = struct{}{}
		projectIDs = append(projectIDs, target.ProjectID)
	}
	facts, err := queries.R2DListProjectAccessFacts(r.Context(), userID, projectIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize parent projects")
		return true
	}
	type projectAccess struct {
		workspaceID string
		readable    bool
	}
	access := make(map[string]projectAccess, len(facts))
	for _, fact := range facts {
		access[fact.ProjectID] = projectAccess{
			workspaceID: fact.OwnerWorkspaceID,
			readable:    r2dauth.Resolve(r2dFacts(fact)).Can(r2dauth.OperationRead),
		}
	}

	requestedWorkspaceID := ResolveWorkspaceIDFromRequest(r, queries)
	requestedMember := false
	if requestedWorkspaceID != "" {
		if _, err := parseR2DUUID(requestedWorkspaceID); err == nil {
			_, requestedMember, err = r2dLoadWorkspaceMember(queries, r, userID, requestedWorkspaceID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
				return true
			}
		}
	}
	observer, err := queries.R2DIsGlobalObserver(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize observer")
		return true
	}

	targetByID := make(map[string]db.R2DIssueACLTargetBatch, len(targets))
	for _, target := range targets {
		targetByID[target.IssueID] = target
	}
	byWorkspace := make(map[string][]string)
	for _, id := range parentIDs {
		target, exists := targetByID[id]
		if !exists {
			continue
		}
		allowed := false
		if target.ProjectID == "" {
			allowed = observer || (requestedMember && target.WorkspaceID == requestedWorkspaceID)
		} else if project, ok := access[target.ProjectID]; ok {
			// Fail closed if an Issue claims a Project owned by another Workspace.
			allowed = project.readable && project.workspaceID == target.WorkspaceID
		}
		if allowed {
			byWorkspace[target.WorkspaceID] = append(byWorkspace[target.WorkspaceID], id)
		}
	}

	selectedWorkspaceID := ""
	selectedParentIDs := []string(nil)
	if requestedMember && len(byWorkspace[requestedWorkspaceID]) > 0 {
		// Preserve the legacy active-Workspace contract when it can satisfy the
		// request. Authorized parents from other Workspaces are ignored rather
		// than turning this endpoint into a multi-tenant aggregator.
		selectedWorkspaceID = requestedWorkspaceID
		selectedParentIDs = byWorkspace[requestedWorkspaceID]
	} else {
		for workspaceID, ids := range byWorkspace {
			if selectedWorkspaceID != "" {
				writeError(w, http.StatusBadRequest, "parent_ids must belong to one workspace")
				return true
			}
			selectedWorkspaceID = workspaceID
			selectedParentIDs = ids
		}
	}
	if selectedWorkspaceID == "" || len(selectedParentIDs) == 0 {
		// Unknown and unauthorized parent IDs are intentionally indistinguishable.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"issues":[]}`))
		return true
	}

	_, selectedMember, err := r2dLoadWorkspaceMember(queries, r, userID, selectedWorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to authorize workspace")
		return true
	}
	allowProjectless := selectedMember || observer

	scoped := r.Clone(SetWorkspaceIDContext(r.Context(), selectedWorkspaceID))
	query := scoped.URL.Query()
	query.Set("parent_ids", strings.Join(selectedParentIDs, ","))
	scoped.URL.RawQuery = query.Encode()

	buf := newR2DResponseBuffer()
	next.ServeHTTP(buf, scoped)
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
