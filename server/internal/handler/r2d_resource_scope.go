package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// r2dTaskExecutionDecision resolves the P06 execution decision for one durable
// task. The caller supplies task/agent/workspace identity only; the store loads
// task -> agent -> issue -> project and the Project's resource rows itself, so
// neither a claim nor a task token can choose or widen the resource handoff.
func (h *Handler) r2dTaskExecutionDecision(ctx context.Context, taskID, agentID, agentWorkspaceID string) (r2dauth.TaskExecutionDecision, error) {
	service := r2dauth.NewTaskExecutionService(r2dauth.NewPostgresStore(h.DB))
	return service.Authorize(ctx, r2dauth.TaskExecutionActor{
		Source:      r2dauth.TaskActorSourceTaskToken,
		TaskID:      taskID,
		AgentID:     agentID,
		WorkspaceID: agentWorkspaceID,
	})
}

// resolveClaimIssueProjectContext is the P06-D issue-bound claim handoff. The
// Project resources a run receives are derived from the authoritative task
// execution decision instead of the soft issue.project_id reference:
//
//   - projectless issue -> ordinary Workspace context (unchanged);
//   - Project reference that is stale/foreign/rebound -> the reference is not
//     an execution grant, so the claim degrades to Projectless Workspace
//     context and never reuses the referenced Project's resources;
//   - authorized Project task -> the resource scope is exactly the Project
//     owner Workspace's resources for THIS Project;
//   - an unreadable authoritative binding fails closed (no claim payload) so a
//     transient error cannot silently fall back to a broader resource set.
func (h *Handler) resolveClaimIssueProjectContext(ctx context.Context, task *db.AgentTaskQueue, issue db.Issue, agent db.Agent) (claimProjectContext, *claimBuildFailure) {
	fail := func(outcome, message string, err error) (claimProjectContext, *claimBuildFailure) {
		slog.Error("issue claim: "+message,
			"task_id", uuidToString(task.ID),
			"issue_id", uuidToString(issue.ID),
			"error", err)
		return claimProjectContext{}, &claimBuildFailure{
			outcome: outcome,
			status:  http.StatusInternalServerError,
			message: message,
		}
	}

	if !issue.ProjectID.Valid {
		out, err := h.resolveClaimProjectContext(ctx, issue.ProjectID, issue.WorkspaceID)
		if err != nil {
			return fail("error_project_context", "failed to load project context", err)
		}
		return out, nil
	}

	decision, err := h.r2dTaskExecutionDecision(ctx,
		uuidToString(task.ID), uuidToString(task.AgentID), uuidToString(agent.WorkspaceID))
	if err != nil {
		return fail("error_project_scope", "failed to resolve project execution scope", err)
	}
	if !decision.Allowed {
		slog.Warn("issue claim: project binding not authorized; claiming with workspace context",
			"task_id", uuidToString(task.ID),
			"issue_id", uuidToString(issue.ID),
			"reason", string(decision.Reason))
		out, err := h.resolveClaimProjectContext(ctx, pgtype.UUID{}, issue.WorkspaceID)
		if err != nil {
			return fail("error_project_context", "failed to load project context", err)
		}
		return out, nil
	}

	return h.claimProjectContextFromDecision(ctx, decision, issue.WorkspaceID)
}

// claimProjectContextFromDecision builds the claim's Project handoff from an
// allowed decision's resource scope. The project row is re-read workspace-scoped
// against the decision's Project owner Workspace for title/description only;
// resources and repos come from the scope, so the wire payload can never carry
// a resource the policy did not admit.
func (h *Handler) claimProjectContextFromDecision(ctx context.Context, decision r2dauth.TaskExecutionDecision, workspaceID pgtype.UUID) (claimProjectContext, *claimBuildFailure) {
	fail := func(err error) (claimProjectContext, *claimBuildFailure) {
		slog.Error("issue claim: resolve authorized project context failed",
			"task_id", decision.TaskID,
			"project_id", decision.ProjectID,
			"error", err)
		return claimProjectContext{}, &claimBuildFailure{
			outcome: "error_project_scope",
			status:  http.StatusInternalServerError,
			message: "failed to resolve project execution scope",
		}
	}

	projectUUID, err := util.ParseUUID(decision.ProjectID)
	if err != nil {
		return fail(err)
	}
	projectWorkspaceUUID, err := util.ParseUUID(decision.ProjectWorkspaceID)
	if err != nil {
		return fail(err)
	}
	project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
		ID:          projectUUID,
		WorkspaceID: projectWorkspaceUUID,
	})
	if err != nil {
		return fail(err)
	}

	out := claimProjectContext{
		ProjectID:   decision.ProjectID,
		Title:       project.Title,
		Description: project.Description.String,
	}
	for _, resource := range decision.ResourceScope.Resources {
		ref := json.RawMessage(resource.ResourceRef)
		if len(ref) == 0 {
			ref = json.RawMessage("{}")
		}
		out.Resources = append(out.Resources, ProjectResourceData{
			ID:           resource.ID,
			ResourceType: resource.ResourceType,
			ResourceRef:  ref,
			Label:        resource.Label,
		})
	}
	for _, repo := range decision.ResourceScope.Repos {
		out.Repos = append(out.Repos, RepoData{URL: repo.URL, Ref: repo.Ref})
	}
	// Repo precedence is unchanged: an authorized Project with no github_repo
	// resources still falls back to the Project owner Workspace's repos.
	if len(out.Repos) == 0 {
		repos, err := h.claimWorkspaceRepos(ctx, workspaceID)
		if err != nil {
			return fail(err)
		}
		out.Repos = repos
	}
	return out, nil
}

// r2dListProjectResourcesForTaskToken serves a task-token read of the Project
// resource list. A task token is bound to one Workspace, so the generic list
// would expose the owner Workspace's whole resource inventory (every Project in
// it). P06-D narrows it to the single Project the task is authorized to run,
// resolved server-side; anything else is a non-disclosing 404.
func (h *Handler) r2dListProjectResourcesForTaskToken(w http.ResponseWriter, r *http.Request, projectIDParam string) {
	projectUUID, ok := parseUUIDOrBadRequest(w, projectIDParam, "project id")
	if !ok {
		return
	}
	decision, err := h.r2dTaskExecutionDecision(
		r.Context(),
		r.Header.Get("X-Task-ID"),
		r.Header.Get("X-Agent-ID"),
		r.Header.Get("X-Workspace-ID"),
	)
	if err != nil {
		if errors.Is(err, r2dauth.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		slog.Warn("task-token project resource read: execution scope lookup failed",
			"project_id", uuidToString(projectUUID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to resolve task execution scope")
		return
	}
	if !decision.Allowed || decision.ProjectID == "" || decision.ProjectID != uuidToString(projectUUID) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	resources, err := h.Queries.ListProjectResourcesInWorkspace(r.Context(), db.ListProjectResourcesInWorkspaceParams{
		ProjectID:   projectUUID,
		WorkspaceID: parseUUID(decision.ProjectWorkspaceID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list project resources")
		return
	}
	resp := make([]ProjectResourceResponse, len(resources))
	for i, res := range resources {
		resp[i] = projectResourceToResponse(res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"resources": resp, "total": len(resp)})
}
