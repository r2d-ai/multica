package handler

import (
	"context"
	"net/http"

	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// This file is the read-path half of P07-C. Project-grant notification rows are
// written under the issue OWNER Workspace, so a foreign collaborator's active
// Workspace never matches the row's workspace_id. The list/archived/count read
// paths therefore select rows by recipient and derive visibility from the
// CURRENT Project ACL, and the mutation path re-checks the same boundary before
// touching an item. Native (owner-Workspace) rows keep their existing behavior:
// projectless and issue-less rows remain Workspace-private, and readable
// Project-backed rows stay visible.

// r2dExplicitProjectGrantRole reports whether a stored grant role is one of the
// explicit Project roles. It duplicates middleware.r2dExplicitProjectGrant on
// purpose: the reader below must not import the middleware package's unexported
// helper, and keeping the switch next to the read policy makes the "explicit
// grant" boundary obvious where it is used.
func r2dExplicitProjectGrantRole(role string) bool {
	switch r2dauth.ProjectRole(role) {
	case r2dauth.ProjectRoleViewer, r2dauth.ProjectRoleMember, r2dauth.ProjectRoleManager:
		return true
	default:
		return false
	}
}

// r2dInboxProjectFacts resolves the current authorization facts for the
// Projects referenced by a batch of inbox rows in one query. A Project that no
// longer exists is absent from the result, which the visibility check treats as
// unreadable (fail closed).
func (h *Handler) r2dInboxProjectFacts(ctx context.Context, userID string, projectIDs []string) (map[string]db.R2DProjectAccessFacts, error) {
	facts, err := h.Queries.R2DListProjectAccessFacts(ctx, userID, projectIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]db.R2DProjectAccessFacts, len(facts))
	for _, fact := range facts {
		out[fact.ProjectID] = fact
	}
	return out, nil
}

// r2dReadableProjectIDsAllWorkspaces returns every Project the user can read,
// regardless of which Workspace owns it. The cross-workspace summary dedups in
// SQL (newest notification per issue), so it needs the complete readable set as
// a filter rather than a per-row answer.
func (h *Handler) r2dReadableProjectIDsAllWorkspaces(ctx context.Context, userID string) ([]string, error) {
	facts, err := h.Queries.R2DListCandidateProjectAccessFacts(ctx, userID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(facts))
	for _, fact := range facts {
		if fact.ProjectID == "" {
			continue
		}
		if !r2dauth.Resolve(r2dHandlerProjectFacts(fact)).Can(r2dauth.OperationRead) {
			continue
		}
		ids = append(ids, fact.ProjectID)
	}
	return ids, nil
}

// r2dInboxVisibleFor is the single visibility rule shared by the list, archived,
// count and mutation paths.
//
//   - A projectless (or issue-less) row is Workspace-private: visible only while
//     its own Workspace is the active one. This is the native boundary, and it
//     is why a foreign stale projectless row is never surfaced.
//   - A Project-backed row requires current Project read. Inside the active
//     Workspace readability alone is sufficient. A foreign row additionally
//     requires an explicit user/workspace grant or the deployment-wide observer
//     role, so being a member of another Workspace does not strand that
//     Workspace's notifications in the active inbox.
func r2dInboxVisibleFor(projectID, rowWorkspaceID, activeWorkspaceID string, facts map[string]db.R2DProjectAccessFacts) bool {
	if projectID == "" {
		return rowWorkspaceID == activeWorkspaceID
	}
	fact, ok := facts[projectID]
	if !ok {
		return false
	}
	if !r2dauth.Resolve(r2dHandlerProjectFacts(fact)).Can(r2dauth.OperationRead) {
		return false
	}
	if rowWorkspaceID == activeWorkspaceID {
		return true
	}
	return fact.GlobalObserver ||
		r2dExplicitProjectGrantRole(fact.DirectGrantRole) ||
		r2dExplicitProjectGrantRole(fact.WorkspaceGrantRole)
}

func r2dInboxRowVisible(row db.R2DInboxItemRow, activeWorkspaceID string, facts map[string]db.R2DProjectAccessFacts) bool {
	return r2dInboxVisibleFor(row.ProjectID, uuidToString(row.WorkspaceID), activeWorkspaceID, facts)
}

func r2dInboxUnreadRowVisible(row db.R2DInboxUnreadRow, activeWorkspaceID string, facts map[string]db.R2DProjectAccessFacts) bool {
	return r2dInboxVisibleFor(row.ProjectID, row.WorkspaceID, activeWorkspaceID, facts)
}

// r2dInboxProjectIDs and r2dInboxUnreadProjectIDs collect the distinct,
// non-empty Project ids referenced by a batch of rows, so fact resolution is
// one query rather than an N+1.
func r2dInboxProjectIDs(rows []db.R2DInboxItemRow) []string {
	seen := make(map[string]struct{}, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ProjectID == "" {
			continue
		}
		if _, ok := seen[row.ProjectID]; ok {
			continue
		}
		seen[row.ProjectID] = struct{}{}
		ids = append(ids, row.ProjectID)
	}
	return ids
}

func r2dInboxUnreadProjectIDs(rows []db.R2DInboxUnreadRow) []string {
	seen := make(map[string]struct{}, len(rows))
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ProjectID == "" {
			continue
		}
		if _, ok := seen[row.ProjectID]; ok {
			continue
		}
		seen[row.ProjectID] = struct{}{}
		ids = append(ids, row.ProjectID)
	}
	return ids
}

// r2dProjectIDList wraps a single optional Project id for fact resolution.
func r2dProjectIDList(projectID string) []string {
	if projectID == "" {
		return nil
	}
	return []string{projectID}
}

// r2dInboxReadableWorkspaceProjectIDStrings is the string form of
// r2dReadableWorkspaceProjectIDs, for the batch-mutation queries whose Project
// filter is a text[] parameter.
func (h *Handler) r2dInboxReadableWorkspaceProjectIDStrings(ctx context.Context, userID, workspaceID string) ([]string, error) {
	ids, err := h.r2dReadableWorkspaceProjectIDs(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, uuidToString(id))
	}
	return out, nil
}

// r2dInboxBatchReadableProjects resolves the readable Project set for a
// workspace-scoped batch inbox mutation and writes a 500 on failure. The
// boolean is false when the caller must return.
func (h *Handler) r2dInboxBatchReadableProjects(w http.ResponseWriter, r *http.Request, userID, workspaceID string) ([]string, bool) {
	ids, err := h.r2dInboxReadableWorkspaceProjectIDStrings(r.Context(), userID, workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to apply project visibility")
		return nil, false
	}
	return ids, true
}
