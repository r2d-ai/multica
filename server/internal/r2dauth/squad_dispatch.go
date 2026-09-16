package r2dauth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrSquadDispatchTargetNotFound is returned when the requested Squad or Issue
// does not resolve. Callers map it to a non-disclosing forbidden response so a
// Project grant cannot be turned into Squad/Issue existence discovery.
var ErrSquadDispatchTargetNotFound = errors.New("squad dispatch target not found")

// SquadDispatchFacts are the durable relationships used before a Squad leader is
// written to the task queue. SquadWorkspaceID is the Squad's owning Workspace;
// it is always the actor's active Workspace, because Squads are only ever
// owned/discovered/configured inside their Workspace. ActorWorkspaceRole is the
// human caller's role in that Squad-owning Workspace; Project sharing must never
// synthesize this role.
//
// Leader* facts describe the durable enqueue target. They are resolved
// server-side so a caller can never substitute a different leader agent than the
// one the Squad currently names.
type SquadDispatchFacts struct {
	SquadID            string
	SquadWorkspaceID   string
	SquadArchived      bool
	LeaderAgentID      string
	LeaderWorkspaceID  string
	LeaderArchived     bool
	IssueID            string
	IssueWorkspaceID   string
	ProjectID          string
	ResolvedProjectID  string
	ProjectWorkspaceID string
	ActorWorkspaceRole WorkspaceRole
}

type SquadDispatchDenyReason string

const (
	SquadDispatchDenyInvalidRequest           SquadDispatchDenyReason = "invalid_request"
	SquadDispatchDenyTargetMismatch           SquadDispatchDenyReason = "target_mismatch"
	SquadDispatchDenySquadWorkspaceMismatch   SquadDispatchDenyReason = "squad_workspace_mismatch"
	SquadDispatchDenySquadUnavailable         SquadDispatchDenyReason = "squad_unavailable"
	SquadDispatchDenyLeaderUnavailable        SquadDispatchDenyReason = "leader_unavailable"
	SquadDispatchDenyLeaderWorkspaceMismatch  SquadDispatchDenyReason = "leader_workspace_mismatch"
	SquadDispatchDenyActorWorkspaceMembership SquadDispatchDenyReason = "actor_workspace_membership"
	SquadDispatchDenyIssueWorkspaceMismatch   SquadDispatchDenyReason = "issue_workspace_mismatch"
	SquadDispatchDenyMalformedProjectBinding  SquadDispatchDenyReason = "malformed_project_binding"
	SquadDispatchDenyStaleProject             SquadDispatchDenyReason = "stale_project"
	SquadDispatchDenyProjectPermission        SquadDispatchDenyReason = "project_permission"
)

// SquadDispatchDecision is deliberately small. It is an authorization result,
// not a Squad/project resource projection, so callers cannot use it to list or
// configure foreign Workspace inventory. LeaderAgentID names the exact durable
// enqueue target the decision authorized.
type SquadDispatchDecision struct {
	Allowed       bool
	Reason        SquadDispatchDenyReason
	SquadID       string
	WorkspaceID   string
	LeaderAgentID string
	IssueID       string
	ProjectID     string
}

func denySquadDispatch(reason SquadDispatchDenyReason) SquadDispatchDecision {
	return SquadDispatchDecision{Reason: reason}
}

// ResolveSquadDispatch applies the Workspace half of the P06-C decision.
// Project permission is supplied separately by SquadDispatchService so the
// existing Project ACL resolver remains the only source of Project role truth.
//
// The Squad stays Workspace-owned: the actor must be a human member of the
// Squad's owning Workspace, and a Projectless Issue must live in that same
// Workspace. A Project binding is the only way an Issue from another Workspace
// becomes executable, and even then the Squad itself never crosses Workspaces.
func ResolveSquadDispatch(actorWorkspaceID, requestedIssueID, requestedSquadID string, f SquadDispatchFacts) SquadDispatchDecision {
	if actorWorkspaceID == "" || requestedIssueID == "" || requestedSquadID == "" {
		return denySquadDispatch(SquadDispatchDenyInvalidRequest)
	}
	if f.SquadID == "" || f.SquadID != requestedSquadID || f.IssueID == "" || f.IssueID != requestedIssueID {
		return denySquadDispatch(SquadDispatchDenyTargetMismatch)
	}
	if f.SquadWorkspaceID == "" || f.SquadWorkspaceID != actorWorkspaceID {
		return denySquadDispatch(SquadDispatchDenySquadWorkspaceMismatch)
	}
	if f.SquadArchived {
		return denySquadDispatch(SquadDispatchDenySquadUnavailable)
	}
	if f.LeaderAgentID == "" || f.LeaderArchived {
		return denySquadDispatch(SquadDispatchDenyLeaderUnavailable)
	}
	if f.LeaderWorkspaceID == "" || f.LeaderWorkspaceID != f.SquadWorkspaceID {
		return denySquadDispatch(SquadDispatchDenyLeaderWorkspaceMismatch)
	}
	if !humanWorkspaceRole(f.ActorWorkspaceRole) {
		return denySquadDispatch(SquadDispatchDenyActorWorkspaceMembership)
	}
	if f.IssueWorkspaceID == "" {
		return denySquadDispatch(SquadDispatchDenyIssueWorkspaceMismatch)
	}

	allowed := SquadDispatchDecision{
		Allowed:       true,
		SquadID:       f.SquadID,
		WorkspaceID:   f.SquadWorkspaceID,
		LeaderAgentID: f.LeaderAgentID,
		IssueID:       f.IssueID,
	}

	// Projectless Issues keep the ordinary Workspace isolation rule. Cross-
	// Workspace execution exists only through an explicit Project binding.
	if f.ProjectID == "" {
		if f.ResolvedProjectID != "" || f.ProjectWorkspaceID != "" {
			return denySquadDispatch(SquadDispatchDenyMalformedProjectBinding)
		}
		if f.IssueWorkspaceID != f.SquadWorkspaceID {
			return denySquadDispatch(SquadDispatchDenyIssueWorkspaceMismatch)
		}
		return allowed
	}

	if f.ResolvedProjectID == "" || f.ResolvedProjectID != f.ProjectID {
		return denySquadDispatch(SquadDispatchDenyStaleProject)
	}
	if f.ProjectWorkspaceID == "" || f.ProjectWorkspaceID != f.IssueWorkspaceID {
		return denySquadDispatch(SquadDispatchDenyMalformedProjectBinding)
	}
	allowed.ProjectID = f.ProjectID
	return allowed
}

type SquadDispatchStore interface {
	LoadSquadDispatchFacts(ctx context.Context, userID, squadID, issueID string) (SquadDispatchFacts, error)
}

type SquadDispatchService struct {
	store    SquadDispatchStore
	projects *Service
}

func NewSquadDispatchService(store SquadDispatchStore, projects *Service) *SquadDispatchService {
	return &SquadDispatchService{store: store, projects: projects}
}

// Authorize proves both halves of a Squad enqueue decision: the caller may
// select a Squad owned by the caller's active Workspace (and the Squad still
// resolves to an enabled leader), and (for Project Issues) the caller has
// effective Project contribution permission. Cross-Workspace execution is only
// legal when the Project owning the Issue grants the actor's Workspace access;
// the Project owner Workspace never has to equal the Squad Workspace.
//
// Leader invocation mode (private/public_to) is deliberately composed at the
// handler boundary through the existing canInvokeAgent policy rather than
// duplicated here, exactly as P06-B does for direct Agent dispatch.
func (s *SquadDispatchService) Authorize(ctx context.Context, userID, actorWorkspaceID, issueID, squadID string) (SquadDispatchDecision, error) {
	if userID == "" || actorWorkspaceID == "" || issueID == "" || squadID == "" {
		return denySquadDispatch(SquadDispatchDenyInvalidRequest), nil
	}
	facts, err := s.store.LoadSquadDispatchFacts(ctx, userID, squadID, issueID)
	if err != nil {
		return SquadDispatchDecision{}, err
	}
	decision := ResolveSquadDispatch(actorWorkspaceID, issueID, squadID, facts)
	if !decision.Allowed || decision.ProjectID == "" {
		return decision, nil
	}
	if s.projects == nil {
		return denySquadDispatch(SquadDispatchDenyProjectPermission), nil
	}
	canContribute, err := s.projects.Can(ctx, userID, decision.ProjectID, OperationContribute)
	if err != nil {
		return SquadDispatchDecision{}, err
	}
	if !canContribute {
		return denySquadDispatch(SquadDispatchDenyProjectPermission), nil
	}
	return decision, nil
}

const squadDispatchFactsSQL = `
SELECT
    squad.id::text,
    squad.workspace_id::text,
    squad.archived_at IS NOT NULL,
    COALESCE(leader.id::text, ''),
    COALESCE(leader.workspace_id::text, ''),
    COALESCE(leader.archived_at IS NOT NULL, false),
    issue.id::text,
    issue.workspace_id::text,
    COALESCE(issue.project_id::text, ''),
    COALESCE(project.id::text, ''),
    COALESCE(project.workspace_id::text, ''),
    COALESCE(actor_member.role, '')
FROM squad
CROSS JOIN issue
LEFT JOIN agent leader ON leader.id = squad.leader_id
LEFT JOIN project ON project.id = issue.project_id
LEFT JOIN member actor_member
  ON actor_member.workspace_id = squad.workspace_id
 AND actor_member.user_id = $1::uuid
WHERE squad.id = $2::uuid
  AND issue.id = $3::uuid
`

// LoadSquadDispatchFacts intentionally returns only identity/binding facts. It
// does not expose Squad briefing text, member rosters, runtime, secrets or
// integrations.
func (s *PostgresStore) LoadSquadDispatchFacts(ctx context.Context, userID, squadID, issueID string) (SquadDispatchFacts, error) {
	var f SquadDispatchFacts
	var actorRole string
	err := s.db.QueryRow(ctx, squadDispatchFactsSQL, userID, squadID, issueID).Scan(
		&f.SquadID,
		&f.SquadWorkspaceID,
		&f.SquadArchived,
		&f.LeaderAgentID,
		&f.LeaderWorkspaceID,
		&f.LeaderArchived,
		&f.IssueID,
		&f.IssueWorkspaceID,
		&f.ProjectID,
		&f.ResolvedProjectID,
		&f.ProjectWorkspaceID,
		&actorRole,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return SquadDispatchFacts{}, ErrSquadDispatchTargetNotFound
	}
	if err != nil {
		return SquadDispatchFacts{}, err
	}
	f.ActorWorkspaceRole = WorkspaceRole(actorRole)
	return f, nil
}
