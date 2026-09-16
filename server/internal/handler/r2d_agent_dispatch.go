package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type r2dProjectAgentRunRequest struct {
	AgentID string `json:"agent_id"`
}

// r2dRequestedWorkspaceID resolves the human caller's selected Workspace from
// request identifiers WITHOUT consulting the request context. R2D Project/Issue
// middleware intentionally rewrites that context to the Project owner's
// Workspace after cross-Workspace authorization, while P06-B needs the caller's
// own Workspace to prove which private Agent inventory they may select.
func (h *Handler) r2dRequestedWorkspaceID(r *http.Request) string {
	if slug := strings.TrimSpace(r.Header.Get("X-Workspace-Slug")); slug != "" {
		if ws, err := h.Queries.GetWorkspaceBySlug(r.Context(), slug); err == nil {
			return uuidToString(ws.ID)
		}
		return ""
	}
	if slug := strings.TrimSpace(r.URL.Query().Get("workspace_slug")); slug != "" {
		if ws, err := h.Queries.GetWorkspaceBySlug(r.Context(), slug); err == nil {
			return uuidToString(ws.ID)
		}
		return ""
	}
	if id := strings.TrimSpace(r.Header.Get("X-Workspace-ID")); id != "" {
		return id
	}
	return strings.TrimSpace(r.URL.Query().Get("workspace_id"))
}

func (h *Handler) r2dAgentDispatchDecision(r *http.Request, userID, actorWorkspaceID, issueID, agentID string) (r2dauth.AgentDispatchDecision, error) {
	store := r2dauth.NewPostgresStore(h.DB)
	projects := r2dauth.NewService(store)
	return r2dauth.NewAgentDispatchService(store, projects).Authorize(
		r.Context(), userID, actorWorkspaceID, issueID, agentID,
	)
}

// r2dCanDispatchAgent composes the R2D Project/Workspace decision with
// Multica's existing Agent invocation mode policy. Neither policy is copied:
// r2dauth owns Project contribution + Workspace ownership, while
// canInvokeAgent remains authoritative for private/public_to semantics.
func (h *Handler) r2dCanDispatchAgent(r *http.Request, userID string, issue db.Issue, agent db.Agent) bool {
	if r.Header.Get("X-Actor-Source") == "task_token" || userID == "" {
		return false
	}
	actorWorkspaceID := h.r2dRequestedWorkspaceID(r)
	if actorWorkspaceID == "" {
		// Preserve installed-client behavior for ordinary same-Workspace work.
		// Cross-Workspace execution still fails below because the Agent must be
		// owned by this Workspace and the Projectless rule is same-Workspace.
		actorWorkspaceID = uuidToString(issue.WorkspaceID)
	}
	if _, err := util.ParseUUID(actorWorkspaceID); err != nil {
		return false
	}

	decision, err := h.r2dAgentDispatchDecision(
		r, userID, actorWorkspaceID, uuidToString(issue.ID), uuidToString(agent.ID),
	)
	if err != nil || !decision.Allowed {
		return false
	}
	if issue.ProjectID.Valid {
		if decision.ProjectID == "" || decision.ProjectID != uuidToString(issue.ProjectID) {
			return false
		}
	} else if decision.ProjectID != "" {
		return false
	}
	// Recheck mutable Agent state after the authorization query. P06-A will
	// independently re-validate the durable task binding when the task token is
	// used, but we still fail before enqueue when the Agent changed meanwhile.
	if agent.ArchivedAt.Valid || uuidToString(agent.WorkspaceID) != actorWorkspaceID {
		return false
	}
	return h.canInvokeAgent(r.Context(), agent, "member", userID, userID, actorWorkspaceID)
}

// RunProjectIssueAgent creates one explicit Agent task for a Project Issue.
// The selected Agent stays owned/discovered/configured in the caller's active
// Workspace; its ID is stored only on the task row and is never copied into the
// Project-visible issue.assignee_id field.
func (h *Handler) RunProjectIssueAgent(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, http.StatusForbidden, "project agent dispatch requires a human actor")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	projectID := chi.URLParam(r, "id")
	if _, ok := parseUUIDOrBadRequest(w, projectID, "project id"); !ok {
		return
	}
	issueID := chi.URLParam(r, "issueId")
	issueUUID, ok := parseUUIDOrBadRequest(w, issueID, "issue id")
	if !ok {
		return
	}

	var req r2dProjectAgentRunRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.AgentID) == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	actorWorkspaceID := h.r2dRequestedWorkspaceID(r)
	if actorWorkspaceID == "" {
		writeError(w, http.StatusBadRequest, "active workspace is required")
		return
	}
	if _, err := util.ParseUUID(actorWorkspaceID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid active workspace")
		return
	}

	decision, err := h.r2dAgentDispatchDecision(r, userID, actorWorkspaceID, issueID, req.AgentID)
	if err != nil {
		if errors.Is(err, r2dauth.ErrDispatchTargetNotFound) || errors.Is(err, r2dauth.ErrProjectNotFound) {
			writeError(w, http.StatusForbidden, "agent dispatch is not allowed")
			return
		}
		slog.Warn("r2d project agent dispatch authorization failed", "issue_id", issueID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to authorize agent dispatch")
		return
	}
	if !decision.Allowed {
		writeError(w, http.StatusForbidden, "agent dispatch is not allowed")
		return
	}
	if decision.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "issue not found in project")
		return
	}

	// Load full rows only AFTER the non-disclosing identity/ACL decision. A
	// Project grant alone therefore cannot turn this endpoint into Agent
	// inventory discovery.
	issue, err := h.Queries.GetIssue(r.Context(), issueUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "issue not found in project")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load issue")
		return
	}
	if !issue.ProjectID.Valid || uuidToString(issue.ProjectID) != projectID {
		writeError(w, http.StatusNotFound, "issue not found in project")
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), agentUUID)
	if err != nil || !h.r2dCanDispatchAgent(r, userID, issue, agent) {
		writeError(w, http.StatusForbidden, "agent dispatch is not allowed")
		return
	}

	actorUserID, err := util.ParseUUID(userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "user not authenticated")
		return
	}
	task, err := h.TaskService.EnqueueProjectAgentTask(r.Context(), issue, agent.ID, actorUserID)
	if errors.Is(err, service.ErrIssueInTriage) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonIssueInTriage)
		return
	}
	if errors.Is(err, service.ErrDuplicatePendingTask) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":  "agent_run_pending",
			"error": "an active or queued run already exists for this issue and agent",
		})
		return
	}
	if err != nil {
		slog.Warn("r2d project agent dispatch enqueue failed", "issue_id", issueID, "agent_id", req.AgentID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to enqueue agent run")
		return
	}

	resp := taskToResponse(task, uuidToString(issue.WorkspaceID))
	h.hydrateTaskAttributions(r.Context(), []*TaskAttribution{resp.Attribution})
	writeJSON(w, http.StatusAccepted, resp)
}

// Keep pgtype imported in this R2D integration file even when upstream alters
// task response internals; the explicit seam's signature is intentionally UUID
// typed and should fail compilation rather than silently widen to string ids.
var _ pgtype.UUID
