package r2dauth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrDispatchTargetNotFound = errors.New("dispatch target not found")

// AgentDispatchFacts are the durable relationships used before an Agent is
// written to the task queue. ActorWorkspaceRole is the human caller's role in
// the Agent-owning Workspace; Project sharing must never synthesize this role.
type AgentDispatchFacts struct {
	AgentID            string
	AgentWorkspaceID   string
	AgentArchived      bool
	IssueID            string
	IssueWorkspaceID   string
	ProjectID          string
	ResolvedProjectID  string
	ProjectWorkspaceID string
	ActorWorkspaceRole WorkspaceRole
}

type AgentDispatchDenyReason string

const (
	AgentDispatchDenyInvalidRequest           AgentDispatchDenyReason = "invalid_request"
	AgentDispatchDenyTargetMismatch           AgentDispatchDenyReason = "target_mismatch"
	AgentDispatchDenyAgentWorkspaceMismatch   AgentDispatchDenyReason = "agent_workspace_mismatch"
	AgentDispatchDenyAgentUnavailable         AgentDispatchDenyReason = "agent_unavailable"
	AgentDispatchDenyActorWorkspaceMembership AgentDispatchDenyReason = "actor_workspace_membership"
	AgentDispatchDenyIssueWorkspaceMismatch   AgentDispatchDenyReason = "issue_workspace_mismatch"
	AgentDispatchDenyMalformedProjectBinding  AgentDispatchDenyReason = "malformed_project_binding"
	AgentDispatchDenyStaleProject             AgentDispatchDenyReason = "stale_project"
	AgentDispatchDenyProjectPermission        AgentDispatchDenyReason = "project_permission"
)

// AgentDispatchDecision is deliberately small. It is an authorization result,
// not an Agent/project resource projection, so callers cannot use it to list or
// configure foreign Workspace inventory.
type AgentDispatchDecision struct {
	Allowed     bool
	Reason      AgentDispatchDenyReason
	AgentID     string
	WorkspaceID string
	IssueID     string
	ProjectID   string
}

func denyAgentDispatch(reason AgentDispatchDenyReason) AgentDispatchDecision {
	return AgentDispatchDecision{Reason: reason}
}

func humanWorkspaceRole(role WorkspaceRole) bool {
	switch role {
	case WorkspaceRoleOwner, WorkspaceRoleAdmin, WorkspaceRoleMember:
		return true
	default:
		return false
	}
}

// ResolveAgentDispatch applies the Workspace half of the P06-B decision.
// Project permission is supplied separately by AgentDispatchService so the
// existing Project ACL resolver remains the only source of Project role truth.
func ResolveAgentDispatch(actorWorkspaceID, requestedIssueID, requestedAgentID string, f AgentDispatchFacts) AgentDispatchDecision {
	if actorWorkspaceID == "" || requestedIssueID == "" || requestedAgentID == "" {
		return denyAgentDispatch(AgentDispatchDenyInvalidRequest)
	}
	if f.AgentID == "" || f.AgentID != requestedAgentID || f.IssueID == "" || f.IssueID != requestedIssueID {
		return denyAgentDispatch(AgentDispatchDenyTargetMismatch)
	}
	if f.AgentWorkspaceID == "" || f.AgentWorkspaceID != actorWorkspaceID {
		return denyAgentDispatch(AgentDispatchDenyAgentWorkspaceMismatch)
	}
	if f.AgentArchived {
		return denyAgentDispatch(AgentDispatchDenyAgentUnavailable)
	}
	if !humanWorkspaceRole(f.ActorWorkspaceRole) {
		return denyAgentDispatch(AgentDispatchDenyActorWorkspaceMembership)
	}
	if f.IssueWorkspaceID == "" {
		return denyAgentDispatch(AgentDispatchDenyIssueWorkspaceMismatch)
	}

	allowed := AgentDispatchDecision{
		Allowed:     true,
		AgentID:     f.AgentID,
		WorkspaceID: f.AgentWorkspaceID,
		IssueID:     f.IssueID,
	}

	// Projectless Issues keep the ordinary Workspace isolation rule. Cross-
	// Workspace execution exists only through an explicit Project binding.
	if f.ProjectID == "" {
		if f.ResolvedProjectID != "" || f.ProjectWorkspaceID != "" {
			return denyAgentDispatch(AgentDispatchDenyMalformedProjectBinding)
		}
		if f.IssueWorkspaceID != f.AgentWorkspaceID {
			return denyAgentDispatch(AgentDispatchDenyIssueWorkspaceMismatch)
		}
		return allowed
	}

	if f.ResolvedProjectID == "" || f.ResolvedProjectID != f.ProjectID {
		return denyAgentDispatch(AgentDispatchDenyStaleProject)
	}
	if f.ProjectWorkspaceID == "" || f.ProjectWorkspaceID != f.IssueWorkspaceID {
		return denyAgentDispatch(AgentDispatchDenyMalformedProjectBinding)
	}
	allowed.ProjectID = f.ProjectID
	return allowed
}

type AgentDispatchStore interface {
	LoadAgentDispatchFacts(ctx context.Context, userID, agentID, issueID string) (AgentDispatchFacts, error)
}

type AgentDispatchService struct {
	store    AgentDispatchStore
	projects *Service
}

func NewAgentDispatchService(store AgentDispatchStore, projects *Service) *AgentDispatchService {
	return &AgentDispatchService{store: store, projects: projects}
}

// Authorize proves both halves of an enqueue decision: the caller may select
// an Agent owned by the caller's active Workspace, and (for Project Issues) the
// caller has effective Project contribution permission. Agent invocation mode
// (private/public_to) is deliberately composed at the handler boundary through
// the existing canInvokeAgent policy rather than duplicated here.
//
// The Project owner Workspace never has to equal the Agent Workspace.
func (s *AgentDispatchService) Authorize(ctx context.Context, userID, actorWorkspaceID, issueID, agentID string) (AgentDispatchDecision, error) {
	if userID == "" || actorWorkspaceID == "" || issueID == "" || agentID == "" {
		return denyAgentDispatch(AgentDispatchDenyInvalidRequest), nil
	}
	facts, err := s.store.LoadAgentDispatchFacts(ctx, userID, agentID, issueID)
	if err != nil {
		return AgentDispatchDecision{}, err
	}
	decision := ResolveAgentDispatch(actorWorkspaceID, issueID, agentID, facts)
	if !decision.Allowed || decision.ProjectID == "" {
		return decision, nil
	}
	if s.projects == nil {
		return denyAgentDispatch(AgentDispatchDenyProjectPermission), nil
	}
	canContribute, err := s.projects.Can(ctx, userID, decision.ProjectID, OperationContribute)
	if err != nil {
		return AgentDispatchDecision{}, err
	}
	if !canContribute {
		return denyAgentDispatch(AgentDispatchDenyProjectPermission), nil
	}
	return decision, nil
}

const agentDispatchFactsSQL = `
SELECT
    agent.id::text,
    agent.workspace_id::text,
    agent.archived_at IS NOT NULL,
    issue.id::text,
    issue.workspace_id::text,
    COALESCE(issue.project_id::text, ''),
    COALESCE(project.id::text, ''),
    COALESCE(project.workspace_id::text, ''),
    COALESCE(actor_member.role, '')
FROM agent
CROSS JOIN issue
LEFT JOIN project ON project.id = issue.project_id
LEFT JOIN member actor_member
  ON actor_member.workspace_id = agent.workspace_id
 AND actor_member.user_id = $1::uuid
WHERE agent.id = $2::uuid
  AND issue.id = $3::uuid
`

// LoadAgentDispatchFacts intentionally returns only identity/binding facts. It
// does not expose Agent configuration, runtime, secrets or integrations.
func (s *PostgresStore) LoadAgentDispatchFacts(ctx context.Context, userID, agentID, issueID string) (AgentDispatchFacts, error) {
	var f AgentDispatchFacts
	var actorRole string
	err := s.db.QueryRow(ctx, agentDispatchFactsSQL, userID, agentID, issueID).Scan(
		&f.AgentID,
		&f.AgentWorkspaceID,
		&f.AgentArchived,
		&f.IssueID,
		&f.IssueWorkspaceID,
		&f.ProjectID,
		&f.ResolvedProjectID,
		&f.ProjectWorkspaceID,
		&actorRole,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentDispatchFacts{}, ErrDispatchTargetNotFound
	}
	if err != nil {
		return AgentDispatchFacts{}, err
	}
	f.ActorWorkspaceRole = WorkspaceRole(actorRole)
	return f, nil
}
