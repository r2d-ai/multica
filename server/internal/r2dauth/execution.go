package r2dauth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var ErrTaskNotFound = errors.New("task not found")

// TaskActorSource is deliberately narrow: Project execution authorization is
// only for authenticated task-token actors. Human users continue through the
// Project ACL resolver in service.go and must never be projected into this
// execution path via their X-User-ID.
type TaskActorSource string

const TaskActorSourceTaskToken TaskActorSource = "task_token"

// TaskExecutionActor is the authoritative identity stamped by task-token
// authentication. There is intentionally no user ID or Project role here:
// owner-human Project grants must never become Agent execution grants.
type TaskExecutionActor struct {
	Source      TaskActorSource
	TaskID      string
	AgentID     string
	WorkspaceID string
}

// TaskExecutionFacts are loaded server-side from durable task relationships.
// TaskIssueID/ProjectID are the references stored on the task/issue rows;
// ResolvedIssueID/ResolvedProjectID prove those references still resolve.
// Keeping both lets the policy fail closed on stale or structurally-invalid
// bindings instead of silently degrading to broader Workspace execution.
type TaskExecutionFacts struct {
	TaskID             string
	TaskAgentID        string
	TaskIssueID        string
	TaskSquadID        string
	AgentWorkspaceID   string
	ResolvedIssueID    string
	IssueWorkspaceID   string
	ProjectID          string
	ResolvedProjectID  string
	ProjectWorkspaceID string
	// Resources are the Project resource rows bound to ProjectID, loaded by the
	// store without a caller-supplied filter. The policy admits only rows whose
	// own WorkspaceID equals ProjectWorkspaceID; see TaskResourceScope.
	Resources []TaskResourceFact
}

type TaskExecutionDenyReason string

const (
	TaskExecutionDenyInvalidActor             TaskExecutionDenyReason = "invalid_actor"
	TaskExecutionDenyTaskMismatch             TaskExecutionDenyReason = "task_mismatch"
	TaskExecutionDenyAgentMismatch            TaskExecutionDenyReason = "agent_mismatch"
	TaskExecutionDenyWorkspaceMismatch        TaskExecutionDenyReason = "workspace_mismatch"
	TaskExecutionDenyStaleIssue               TaskExecutionDenyReason = "stale_issue"
	TaskExecutionDenyIssueWorkspaceMismatch   TaskExecutionDenyReason = "issue_workspace_mismatch"
	TaskExecutionDenyMalformedProjectBinding  TaskExecutionDenyReason = "malformed_project_binding"
	TaskExecutionDenyStaleProject             TaskExecutionDenyReason = "stale_project"
	TaskExecutionDenyProjectWorkspaceMismatch TaskExecutionDenyReason = "project_workspace_mismatch"
	TaskExecutionDenyResourceScopeUnresolved  TaskExecutionDenyReason = "resource_scope_unresolved"
)

// TaskExecutionDecision is the reusable P06 execution result. WorkspaceID is
// always the Agent-owning Workspace (the task actor's isolation boundary).
// ProjectWorkspaceID is populated only for Project-scoped tasks and identifies
// the Project/Issue owner Workspace; it may differ from WorkspaceID for an
// explicitly-dispatched cross-Workspace Project task.
type TaskExecutionDecision struct {
	Allowed            bool
	Reason             TaskExecutionDenyReason
	TaskID             string
	AgentID            string
	WorkspaceID        string
	SquadID            string
	ProjectID          string
	ProjectWorkspaceID string
	// ResourceScope is the Project resource handoff derived from the
	// authoritative Project-Workspace binding. It is populated only for an
	// allowed Project-scoped task; projectless tasks keep the zero value and
	// consume their Agent Workspace resources through the normal Workspace path.
	ResourceScope TaskResourceScope
}

func denyTaskExecution(reason TaskExecutionDenyReason) TaskExecutionDecision {
	return TaskExecutionDecision{Reason: reason}
}

// ResolveTaskExecution is intentionally independent from human Project ACL.
// The durable task row is the exact execution grant: later P06 enqueue/dispatch
// slices are responsible for deciding which Agent/Squad may be written there.
// Once written, this resolver proves that the authenticated token still names
// that exact task, Agent and Agent Workspace and that any issue/project chain
// still resolves consistently.
func ResolveTaskExecution(actor TaskExecutionActor, f TaskExecutionFacts) TaskExecutionDecision {
	if actor.Source != TaskActorSourceTaskToken || actor.TaskID == "" || actor.AgentID == "" || actor.WorkspaceID == "" {
		return denyTaskExecution(TaskExecutionDenyInvalidActor)
	}
	if f.TaskID == "" || f.TaskID != actor.TaskID {
		return denyTaskExecution(TaskExecutionDenyTaskMismatch)
	}
	if f.TaskAgentID == "" || f.TaskAgentID != actor.AgentID {
		return denyTaskExecution(TaskExecutionDenyAgentMismatch)
	}
	if f.AgentWorkspaceID == "" || f.AgentWorkspaceID != actor.WorkspaceID {
		return denyTaskExecution(TaskExecutionDenyWorkspaceMismatch)
	}

	allowed := TaskExecutionDecision{
		Allowed:     true,
		TaskID:      f.TaskID,
		AgentID:     f.TaskAgentID,
		WorkspaceID: f.AgentWorkspaceID,
		SquadID:     f.TaskSquadID,
	}

	// Direct chat/autopilot/other non-issue tasks remain bound only to the
	// Agent's Workspace. They do not acquire Project scope implicitly.
	if f.TaskIssueID == "" {
		if f.ResolvedIssueID != "" || f.IssueWorkspaceID != "" || f.ProjectID != "" || f.ResolvedProjectID != "" || f.ProjectWorkspaceID != "" || len(f.Resources) > 0 {
			return denyTaskExecution(TaskExecutionDenyMalformedProjectBinding)
		}
		return allowed
	}

	if f.ResolvedIssueID == "" || f.ResolvedIssueID != f.TaskIssueID {
		return denyTaskExecution(TaskExecutionDenyStaleIssue)
	}
	if f.IssueWorkspaceID == "" {
		return denyTaskExecution(TaskExecutionDenyIssueWorkspaceMismatch)
	}

	// An issue without a Project is ordinary Workspace work. Cross-Workspace
	// execution is only legal when an explicit Project binding exists.
	if f.ProjectID == "" {
		if f.ResolvedProjectID != "" || f.ProjectWorkspaceID != "" || len(f.Resources) > 0 {
			return denyTaskExecution(TaskExecutionDenyMalformedProjectBinding)
		}
		if f.IssueWorkspaceID != f.AgentWorkspaceID {
			return denyTaskExecution(TaskExecutionDenyIssueWorkspaceMismatch)
		}
		return allowed
	}

	// Project-scoped tasks may intentionally dispatch an Agent owned by another
	// Workspace. The task row is the exact grant; the Project must still exist
	// and must still own the issue's Workspace boundary. No human Project role is
	// consulted here.
	if f.ResolvedProjectID == "" || f.ResolvedProjectID != f.ProjectID {
		return denyTaskExecution(TaskExecutionDenyStaleProject)
	}
	if f.ProjectWorkspaceID == "" || f.ProjectWorkspaceID != f.IssueWorkspaceID {
		return denyTaskExecution(TaskExecutionDenyProjectWorkspaceMismatch)
	}

	allowed.ProjectID = f.ProjectID
	allowed.ProjectWorkspaceID = f.ProjectWorkspaceID
	// P06-D: the resource handoff is part of the decision, not a separate
	// caller-supplied lookup. A Project-scoped task that cannot resolve its
	// scope fails closed rather than falling back to the Agent Workspace's own
	// resources.
	scope, ok := ResolveTaskResourceScope(allowed, f.Resources)
	if !ok {
		return denyTaskExecution(TaskExecutionDenyResourceScopeUnresolved)
	}
	allowed.ResourceScope = scope
	return allowed
}

// TaskExecutionStore resolves execution facts by authenticated task ID. The
// caller supplies no Project ID, so a token cannot choose or widen its own
// Project scope.
type TaskExecutionStore interface {
	LoadTaskExecutionFacts(ctx context.Context, taskID string) (TaskExecutionFacts, error)
}

type TaskExecutionService struct {
	store TaskExecutionStore
}

func NewTaskExecutionService(store TaskExecutionStore) *TaskExecutionService {
	return &TaskExecutionService{store: store}
}

func (s *TaskExecutionService) Authorize(ctx context.Context, actor TaskExecutionActor) (TaskExecutionDecision, error) {
	if actor.Source != TaskActorSourceTaskToken || actor.TaskID == "" || actor.AgentID == "" || actor.WorkspaceID == "" {
		return denyTaskExecution(TaskExecutionDenyInvalidActor), nil
	}
	facts, err := s.store.LoadTaskExecutionFacts(ctx, actor.TaskID)
	if err != nil {
		return TaskExecutionDecision{}, err
	}
	return ResolveTaskExecution(actor, facts), nil
}

const taskExecutionFactsSQL = `
SELECT
    task.id::text,
    task.agent_id::text,
    COALESCE(task.issue_id::text, ''),
    COALESCE(task.squad_id::text, ''),
    agent.workspace_id::text,
    COALESCE(issue.id::text, ''),
    COALESCE(issue.workspace_id::text, ''),
    COALESCE(issue.project_id::text, ''),
    COALESCE(project.id::text, ''),
    COALESCE(project.workspace_id::text, '')
FROM agent_task_queue task
JOIN agent ON agent.id = task.agent_id
LEFT JOIN issue ON issue.id = task.issue_id
LEFT JOIN project ON project.id = issue.project_id
WHERE task.id = $1::uuid
`

// taskResourceFactsSQL selects by project_id only. Filtering to the Project
// owner Workspace is the POLICY's job (see ResolveTaskResourceScope): a SQL
// predicate would hide the very mismatch the decision must reject, and would
// make the policy untestable with a corrupt row.
const taskResourceFactsSQL = `
SELECT
    resource.id::text,
    resource.workspace_id::text,
    resource.resource_type,
    resource.resource_ref::text,
    COALESCE(resource.label, '')
FROM project_resource resource
WHERE resource.project_id = $1::uuid
ORDER BY resource.position ASC, resource.created_at ASC
`

// LoadTaskExecutionFacts deliberately follows only authoritative server-side
// relationships: task -> agent, task -> issue -> project, and the Project's
// resource rows. It never reads r2d_project_grants, member roles, X-User-ID, or
// a caller-provided Project ID.
func (s *PostgresStore) LoadTaskExecutionFacts(ctx context.Context, taskID string) (TaskExecutionFacts, error) {
	var f TaskExecutionFacts
	err := s.db.QueryRow(ctx, taskExecutionFactsSQL, taskID).Scan(
		&f.TaskID,
		&f.TaskAgentID,
		&f.TaskIssueID,
		&f.TaskSquadID,
		&f.AgentWorkspaceID,
		&f.ResolvedIssueID,
		&f.IssueWorkspaceID,
		&f.ProjectID,
		&f.ResolvedProjectID,
		&f.ProjectWorkspaceID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskExecutionFacts{}, ErrTaskNotFound
	}
	if err != nil {
		return TaskExecutionFacts{}, err
	}
	if f.ProjectID == "" {
		return f, nil
	}
	resources, err := s.loadTaskResourceFacts(ctx, f.ProjectID)
	if err != nil {
		return TaskExecutionFacts{}, err
	}
	f.Resources = resources
	return f, nil
}

func (s *PostgresStore) loadTaskResourceFacts(ctx context.Context, projectID string) ([]TaskResourceFact, error) {
	rows, err := s.db.Query(ctx, taskResourceFactsSQL, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	facts := make([]TaskResourceFact, 0)
	for rows.Next() {
		var fact TaskResourceFact
		if err := rows.Scan(
			&fact.ID,
			&fact.WorkspaceID,
			&fact.ResourceType,
			&fact.ResourceRef,
			&fact.Label,
		); err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return facts, nil
}
