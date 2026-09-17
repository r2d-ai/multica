package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/r2dsharing"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type assignableActorsResponse struct {
	Actors []r2dsharing.Principal `json:"actors"`
}

// GetProjectAssignableActors is the assignee roster for one Project. It is
// contributor-readable, unlike the manager-only sharing directory.
func (h *Handler) GetProjectAssignableActors(w http.ResponseWriter, r *http.Request) {
	userID, projectID, ok := projectSharingRequestContext(w, r)
	if !ok {
		return
	}
	principalType := r2dsharing.PrincipalType(strings.TrimSpace(r.URL.Query().Get("type")))
	if principalType == "" {
		principalType = r2dsharing.PrincipalUser
	}
	if !r2dsharing.ValidPrincipalType(principalType) {
		writeError(w, http.StatusBadRequest, "invalid type")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 50 {
		limit = 50
	}
	actors, err := h.projectSharingService().AssignableActors(
		r.Context(), userID, projectID, principalType, r.URL.Query().Get("q"), limit,
	)
	if err != nil {
		writeProjectSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, assignableActorsResponse{Actors: actors})
}

// r2dMemberAssignable is the defense-in-depth copy of the middleware rule: a
// Project grant may assign a user who is assignable on that Project, even when
// the user is not a member of the Project's Workspace.
func (h *Handler) r2dMemberAssignable(ctx context.Context, workspaceID, projectID, userID string) bool {
	if workspaceID == "" || projectID == "" || userID == "" {
		return false
	}
	allowed, err := h.Queries.R2DIsAssignableMember(ctx, projectID, userID)
	if err != nil {
		return false
	}
	return allowed
}

// R2DAssigneeDisplay is the server-resolved display for one assignee. Empty
// fields mean "the caller may not enumerate this assignee"; the client then
// falls back to its local resolver.
type R2DAssigneeDisplay struct {
	Name      string
	AvatarURL string
}

// r2dAssigneeRef is one issue assignee plus the context the enumerability check
// needs.
type r2dAssigneeRef struct {
	Type        pgtype.Text
	ID          pgtype.UUID
	ProjectID   pgtype.UUID
	WorkspaceID pgtype.UUID
}

func assigneeDisplayKey(t pgtype.Text, id pgtype.UUID) string {
	if !t.Valid || !id.Valid {
		return ""
	}
	return t.String + ":" + uuidToString(id)
}

// applyAssigneeDisplay copies the server-resolved assignee display into resp
// when the caller may enumerate the assignee. A missing key leaves both fields
// nil so the client falls back to its local resolver.
func applyAssigneeDisplay(resp *IssueResponse, display map[string]R2DAssigneeDisplay, t pgtype.Text, id pgtype.UUID) {
	if len(display) == 0 {
		return
	}
	key := assigneeDisplayKey(t, id)
	if key == "" {
		return
	}
	d, ok := display[key]
	if !ok || d.Name == "" {
		return
	}
	name := d.Name
	resp.AssigneeName = &name
	if d.AvatarURL != "" {
		avatar := d.AvatarURL
		resp.AssigneeAvatarURL = &avatar
	}
}

// r2dAssigneeDisplay resolves assignee display fields for a set of issues,
// keyed by "<assignee_type>:<assignee_id>". member: owner-Workspace members,
// direct grantees, and members of granted Workspaces are named; agent/squad:
// owner-Workspace human members only (ProjectCapabilities.ViewResources).
func (h *Handler) r2dAssigneeDisplay(ctx context.Context, userID string, issues []db.Issue) (map[string]R2DAssigneeDisplay, error) {
	return h.r2dAssigneeDisplayFor(ctx, userID, r2dAssigneeRefsForIssues(issues))
}

// r2dAssigneeDisplayForIssues resolves display fields, degrading to an empty
// map so a naming lookup failure never fails an issue read.
func (h *Handler) r2dAssigneeDisplayForIssues(ctx context.Context, userID string, issues []db.Issue) map[string]R2DAssigneeDisplay {
	return h.r2dAssigneeDisplayOrEmpty(ctx, userID, r2dAssigneeRefsForIssues(issues))
}

// r2dAssigneeDisplayForRefs is the best-effort form for list rows.
func (h *Handler) r2dAssigneeDisplayForRefs(ctx context.Context, userID string, refs []r2dAssigneeRef) map[string]R2DAssigneeDisplay {
	return h.r2dAssigneeDisplayOrEmpty(ctx, userID, refs)
}

func (h *Handler) r2dAssigneeDisplayOrEmpty(ctx context.Context, userID string, refs []r2dAssigneeRef) map[string]R2DAssigneeDisplay {
	display, err := h.r2dAssigneeDisplayFor(ctx, userID, refs)
	if err != nil {
		slog.Warn("resolve assignee display failed", "error", err)
		return nil
	}
	return display
}

func r2dAssigneeRefsForIssues(issues []db.Issue) []r2dAssigneeRef {
	refs := make([]r2dAssigneeRef, 0, len(issues))
	for _, issue := range issues {
		refs = append(refs, r2dAssigneeRef{
			Type:        issue.AssigneeType,
			ID:          issue.AssigneeID,
			ProjectID:   issue.ProjectID,
			WorkspaceID: issue.WorkspaceID,
		})
	}
	return refs
}

func r2dAssigneeRefsForListRows(rows []db.ListIssuesRow) []r2dAssigneeRef {
	refs := make([]r2dAssigneeRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, r2dAssigneeRef{
			Type:        row.AssigneeType,
			ID:          row.AssigneeID,
			ProjectID:   row.ProjectID,
			WorkspaceID: row.WorkspaceID,
		})
	}
	return refs
}

func r2dAssigneeRefsForOpenRows(rows []db.ListOpenIssuesRow) []r2dAssigneeRef {
	refs := make([]r2dAssigneeRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, r2dAssigneeRef{
			Type:        row.AssigneeType,
			ID:          row.AssigneeID,
			ProjectID:   row.ProjectID,
			WorkspaceID: row.WorkspaceID,
		})
	}
	return refs
}

func (h *Handler) r2dAssigneeDisplayFor(ctx context.Context, userID string, refs []r2dAssigneeRef) (map[string]R2DAssigneeDisplay, error) {
	out := map[string]R2DAssigneeDisplay{}
	if userID == "" {
		return out, nil
	}

	memberRefs := map[string]r2dAssigneeRef{}
	memberIDs := map[string]pgtype.UUID{}
	agentRefs := map[string]r2dAssigneeRef{}
	squadRefs := map[string]r2dAssigneeRef{}
	projectIDs := map[string]struct{}{}
	for _, ref := range refs {
		key := assigneeDisplayKey(ref.Type, ref.ID)
		if key == "" {
			continue
		}
		switch ref.Type.String {
		case "member":
			memberRefs[key] = ref
			memberIDs[key] = ref.ID
		case "agent", "squad":
			if ref.Type.String == "agent" {
				agentRefs[key] = ref
			} else {
				squadRefs[key] = ref
			}
			if projectID := uuidToString(ref.ProjectID); projectID != "" {
				projectIDs[projectID] = struct{}{}
			}
		}
	}

	if len(memberRefs) > 0 {
		ids := make([]pgtype.UUID, 0, len(memberIDs))
		for _, id := range memberIDs {
			ids = append(ids, id)
		}
		users, err := h.Queries.GetUsersByIDs(ctx, ids)
		if err != nil {
			return nil, err
		}
		byID := make(map[string]db.GetUsersByIDsRow, len(users))
		for _, u := range users {
			byID[uuidToString(u.ID)] = u
		}
		for key, ref := range memberRefs {
			enumerable, err := h.r2dMemberEnumerable(ctx, ref)
			if err != nil {
				return nil, err
			}
			if !enumerable {
				continue
			}
			user, ok := byID[uuidToString(ref.ID)]
			if !ok {
				continue
			}
			out[key] = R2DAssigneeDisplay{Name: user.Name, AvatarURL: user.AvatarUrl.String}
		}
	}

	if len(agentRefs) > 0 || len(squadRefs) > 0 {
		type ownerProject struct {
			workspaceID string
			viewable    bool
		}
		projects := map[string]ownerProject{}
		for projectID := range projectIDs {
			facts, err := h.Queries.R2DLoadProjectAccessFacts(ctx, userID, projectID)
			if err != nil {
				// Not readable or gone: omit the name, never fail the read.
				continue
			}
			caps := r2dauth.ResolveCapabilities(r2dHandlerProjectFacts(facts))
			projects[projectID] = ownerProject{workspaceID: facts.OwnerWorkspaceID, viewable: caps.ViewResources}
		}
		for key, ref := range agentRefs {
			project := projects[uuidToString(ref.ProjectID)]
			if !project.viewable || project.workspaceID == "" {
				continue
			}
			wsUUID, err := util.ParseUUID(project.workspaceID)
			if err != nil {
				continue
			}
			agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: ref.ID, WorkspaceID: wsUUID})
			if err != nil {
				continue
			}
			out[key] = R2DAssigneeDisplay{Name: agent.Name, AvatarURL: agent.AvatarUrl.String}
		}
		for key, ref := range squadRefs {
			project := projects[uuidToString(ref.ProjectID)]
			if !project.viewable || project.workspaceID == "" {
				continue
			}
			wsUUID, err := util.ParseUUID(project.workspaceID)
			if err != nil {
				continue
			}
			squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: ref.ID, WorkspaceID: wsUUID})
			if err != nil {
				continue
			}
			out[key] = R2DAssigneeDisplay{Name: squad.Name, AvatarURL: squad.AvatarUrl.String}
		}
	}

	return out, nil
}

// r2dMemberEnumerable reports whether the caller may be shown this member
// assignee's display name: assignable on the issue's Project, or a member of
// the issue's owner Workspace when the issue is projectless.
func (h *Handler) r2dMemberEnumerable(ctx context.Context, ref r2dAssigneeRef) (bool, error) {
	assigneeID := uuidToString(ref.ID)
	if projectID := uuidToString(ref.ProjectID); projectID != "" {
		return h.Queries.R2DIsAssignableMember(ctx, projectID, assigneeID)
	}
	if !ref.WorkspaceID.Valid {
		return false, nil
	}
	if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      ref.ID,
		WorkspaceID: ref.WorkspaceID,
	}); err != nil {
		return false, nil
	}
	return true, nil
}
