package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type r2dProjectSquadRunRequest struct {
	SquadID string `json:"squad_id"`
}

func (h *Handler) r2dSquadDispatchDecision(r *http.Request, userID, actorWorkspaceID, issueID, squadID string) (r2dauth.SquadDispatchDecision, error) {
	store := r2dauth.NewPostgresStore(h.DB)
	projects := r2dauth.NewService(store)
	return r2dauth.NewSquadDispatchService(store, projects).Authorize(
		r.Context(), userID, actorWorkspaceID, issueID, squadID,
	)
}

// r2dCanDispatchSquad composes the R2D Project/Workspace decision with
// Multica's existing leader-invocation policy. Neither policy is copied:
// r2dauth owns Project contribution + Squad ownership, while canInvokeAgent
// remains authoritative for the leader agent's private/public_to semantics.
func (h *Handler) r2dCanDispatchSquad(r *http.Request, userID string, issue db.Issue, squad db.Squad) bool {
	if r.Header.Get("X-Actor-Source") == "task_token" || userID == "" {
		return false
	}
	actorWorkspaceID := h.r2dRequestedWorkspaceID(r)
	if actorWorkspaceID == "" {
		// Preserve installed-client behavior for ordinary same-Workspace work.
		// Cross-Workspace execution still fails below because the Squad must be
		// owned by this Workspace and the Projectless rule is same-Workspace.
		actorWorkspaceID = uuidToString(issue.WorkspaceID)
	}
	if _, err := util.ParseUUID(actorWorkspaceID); err != nil {
		return false
	}

	decision, err := h.r2dSquadDispatchDecision(
		r, userID, actorWorkspaceID, uuidToString(issue.ID), uuidToString(squad.ID),
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
	// Recheck mutable Squad state after the authorization query, and pin the
	// enqueue target to the exact leader the decision resolved.
	if squad.ArchivedAt.Valid || uuidToString(squad.WorkspaceID) != actorWorkspaceID {
		return false
	}
	if decision.LeaderAgentID == "" || uuidToString(squad.LeaderID) != decision.LeaderAgentID {
		return false
	}
	leader, err := h.Queries.GetAgent(r.Context(), squad.LeaderID)
	if err != nil || leader.ArchivedAt.Valid {
		return false
	}
	return h.canInvokeAgent(r.Context(), leader, "member", userID, userID, actorWorkspaceID)
}

// RunProjectIssueSquad creates one leader task for a Squad on a Project Issue.
// The selected Squad stays owned/discovered/configured in the caller's active
// Workspace; its ID is stored only on the task row (is_leader_task + squad_id)
// and is never copied into the Project-visible issue.assignee_id field.
func (h *Handler) RunProjectIssueSquad(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		writeError(w, http.StatusForbidden, "project squad dispatch requires a human actor")
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

	var req r2dProjectSquadRunRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.SquadID) == "" {
		writeError(w, http.StatusBadRequest, "squad_id is required")
		return
	}
	squadUUID, ok := parseUUIDOrBadRequest(w, req.SquadID, "squad_id")
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

	decision, err := h.r2dSquadDispatchDecision(r, userID, actorWorkspaceID, issueID, req.SquadID)
	if err != nil {
		if errors.Is(err, r2dauth.ErrSquadDispatchTargetNotFound) || errors.Is(err, r2dauth.ErrProjectNotFound) {
			writeError(w, http.StatusForbidden, "squad dispatch is not allowed")
			return
		}
		slog.Warn("r2d project squad dispatch authorization failed", "issue_id", issueID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to authorize squad dispatch")
		return
	}
	if !decision.Allowed {
		writeError(w, http.StatusForbidden, "squad dispatch is not allowed")
		return
	}
	if decision.ProjectID != projectID {
		writeError(w, http.StatusNotFound, "issue not found in project")
		return
	}

	// Load full rows only AFTER the non-disclosing identity/ACL decision. A
	// Project grant alone therefore cannot turn this endpoint into Squad
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
	squad, err := h.Queries.GetSquad(r.Context(), squadUUID)
	if err != nil || !h.r2dCanDispatchSquad(r, userID, issue, squad) {
		writeError(w, http.StatusForbidden, "squad dispatch is not allowed")
		return
	}

	actorUserID, err := util.ParseUUID(userID)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "user not authenticated")
		return
	}
	task, err := h.TaskService.EnqueueProjectSquadTask(r.Context(), issue, squad.LeaderID, squad.ID, actorUserID)
	if errors.Is(err, service.ErrIssueInTriage) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonIssueInTriage)
		return
	}
	if errors.Is(err, service.ErrDuplicatePendingTask) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"code":  "agent_run_pending",
			"error": "an active or queued run already exists for this issue and squad leader",
		})
		return
	}
	if err != nil {
		slog.Warn("r2d project squad dispatch enqueue failed", "issue_id", issueID, "squad_id", req.SquadID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to enqueue squad run")
		return
	}

	resp := taskToResponse(task, uuidToString(issue.WorkspaceID))
	h.hydrateTaskAttributions(r.Context(), []*TaskAttribution{resp.Attribution})
	writeJSON(w, http.StatusAccepted, resp)
}
