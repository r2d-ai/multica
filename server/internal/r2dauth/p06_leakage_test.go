package r2dauth

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// These allowlists are security contracts. Adding a field to either decision
// can expose Workspace-owned inventory to a cross-Workspace Project caller.
func TestP06AgentAndSquadInventoryIsolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		value  any
		fields []string
	}{
		{
			name:  "agent dispatch returns identity and scope only",
			value: AgentDispatchDecision{},
			fields: []string{
				"Allowed", "Reason", "AgentID", "WorkspaceID", "IssueID", "ProjectID",
			},
		},
		{
			name:  "task execution returns agent and squad identity but no inventory",
			value: TaskExecutionDecision{},
			fields: []string{
				"Allowed", "Reason", "TaskID", "AgentID", "WorkspaceID", "SquadID", "ProjectID", "ProjectWorkspaceID",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ := reflect.TypeOf(tt.value)
			got := make([]string, typ.NumField())
			for i := range got {
				got[i] = typ.Field(i).Name
			}
			if !reflect.DeepEqual(got, tt.fields) {
				t.Fatalf("exported decision fields = %v, want %v; inventory/config fields must not cross the authorization boundary", got, tt.fields)
			}
		})
	}
}

func TestP06AuthorizationQueriesDoNotLoadWorkspaceInventory(t *testing.T) {
	t.Parallel()

	for name, query := range map[string]string{
		"agent dispatch": agentDispatchFactsSQL,
		"task execution": taskExecutionFactsSQL,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lower := strings.ToLower(query)
			for _, forbidden := range []string{
				"runtime_config", "custom_env", "custom_args", "secret", "credential",
				"integration", "workspace_mcp", "agent_runtime", "squad_member",
			} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("authorization query contains Workspace inventory source %q", forbidden)
				}
			}
		})
	}
}

func TestP06ResourceScopeIsolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		facts         ProjectFacts
		viewResources bool
	}{
		{
			name:          "project owner Workspace member sees associated resources",
			facts:         ProjectFacts{Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleMember},
			viewResources: true,
		},
		{
			name:  "cross-Workspace project member cannot see owner Workspace resources",
			facts: ProjectFacts{Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleMember},
		},
		{
			name:  "cross-Workspace project manager cannot see owner Workspace resources",
			facts: ProjectFacts{Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleManager},
		},
		{
			name: "agent role cannot turn a project grant into Workspace resource access",
			facts: ProjectFacts{
				Visibility:         VisibilityPrivate,
				OwnerWorkspaceRole: WorkspaceRoleAgent,
				DirectGrantRole:    ProjectRoleMember,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveCapabilities(tt.facts)
			if !got.Read {
				t.Fatal("fixture must retain Project read access")
			}
			if got.ViewResources != tt.viewResources {
				t.Fatalf("ViewResources = %t, want %t", got.ViewResources, tt.viewResources)
			}
		})
	}
}

func TestP06DispatchAuthorizationBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*AgentDispatchFacts)
		reason AgentDispatchDenyReason
	}{
		{
			name: "archived agent",
			mutate: func(f *AgentDispatchFacts) {
				f.AgentArchived = true
			},
			reason: AgentDispatchDenyAgentUnavailable,
		},
		{
			name: "stale project binding",
			mutate: func(f *AgentDispatchFacts) {
				f.ResolvedProjectID = ""
			},
			reason: AgentDispatchDenyStaleProject,
		},
		{
			name: "project binding resolves to another project",
			mutate: func(f *AgentDispatchFacts) {
				f.ResolvedProjectID = "project-other"
			},
			reason: AgentDispatchDenyStaleProject,
		},
		{
			name: "project belongs to another Workspace than issue",
			mutate: func(f *AgentDispatchFacts) {
				f.ProjectWorkspaceID = "workspace-other"
			},
			reason: AgentDispatchDenyMalformedProjectBinding,
		},
		{
			name: "projectless issue carries residual project resolution",
			mutate: func(f *AgentDispatchFacts) {
				f.ProjectID = ""
			},
			reason: AgentDispatchDenyMalformedProjectBinding,
		},
		{
			name: "foreign Agent substitution",
			mutate: func(f *AgentDispatchFacts) {
				f.AgentID = "agent-other"
			},
			reason: AgentDispatchDenyTargetMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			facts := baseAgentDispatchFacts()
			tt.mutate(&facts)
			got := ResolveAgentDispatch("workspace-b", "issue-a", "agent-b", facts)
			if got.Allowed || got.Reason != tt.reason {
				t.Fatalf("decision = %+v, want denial %q", got, tt.reason)
			}
		})
	}
}

func TestP06TaskExecutionBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*TaskExecutionActor, *TaskExecutionFacts)
		reason TaskExecutionDenyReason
	}{
		{
			name: "token cannot select another task",
			mutate: func(a *TaskExecutionActor, _ *TaskExecutionFacts) {
				a.TaskID = "task-other"
			},
			reason: TaskExecutionDenyTaskMismatch,
		},
		{
			name: "token cannot select another agent",
			mutate: func(a *TaskExecutionActor, _ *TaskExecutionFacts) {
				a.AgentID = "agent-other"
			},
			reason: TaskExecutionDenyAgentMismatch,
		},
		{
			name: "token cannot widen to another agent Workspace",
			mutate: func(a *TaskExecutionActor, _ *TaskExecutionFacts) {
				a.WorkspaceID = "workspace-project"
			},
			reason: TaskExecutionDenyWorkspaceMismatch,
		},
		{
			name: "stale issue binding",
			mutate: func(_ *TaskExecutionActor, f *TaskExecutionFacts) {
				f.ResolvedIssueID = ""
			},
			reason: TaskExecutionDenyStaleIssue,
		},
		{
			name: "stale project binding",
			mutate: func(_ *TaskExecutionActor, f *TaskExecutionFacts) {
				f.ResolvedProjectID = ""
			},
			reason: TaskExecutionDenyStaleProject,
		},
		{
			name: "project moved across Workspace boundary",
			mutate: func(_ *TaskExecutionActor, f *TaskExecutionFacts) {
				f.ProjectWorkspaceID = "workspace-other"
			},
			reason: TaskExecutionDenyProjectWorkspaceMismatch,
		},
		{
			name: "non-issue task cannot carry project scope",
			mutate: func(_ *TaskExecutionActor, f *TaskExecutionFacts) {
				f.TaskIssueID = ""
			},
			reason: TaskExecutionDenyMalformedProjectBinding,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actor := baseTaskActor()
			facts := baseProjectTaskFacts()
			tt.mutate(&actor, &facts)
			got := ResolveTaskExecution(actor, facts)
			if got.Allowed || got.Reason != tt.reason {
				t.Fatalf("decision = %+v, want denial %q", got, tt.reason)
			}
		})
	}
}

func TestP06TaskScopeComesOnlyFromServerSideResolution(t *testing.T) {
	t.Parallel()

	store := &fakeTaskExecutionStore{facts: baseProjectTaskFacts()}
	decision, err := NewTaskExecutionService(store).Authorize(context.Background(), baseTaskActor())
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allowed || decision.ProjectID != "project-1" {
		t.Fatalf("decision = %+v, want server-resolved project-1 scope", decision)
	}
	if store.requestedID != "task-1" {
		t.Fatalf("facts lookup used %q, want authenticated task-1 only", store.requestedID)
	}
}

func TestP06CrossWorkspaceAgentAndSquadExecutionIsolation(t *testing.T) {
	t.Parallel()

	facts := baseProjectTaskFacts()
	facts.TaskSquadID = "squad-agent-workspace"
	decision := ResolveTaskExecution(baseTaskActor(), facts)
	if !decision.Allowed {
		t.Fatalf("cross-Workspace Project execution denied: %+v", decision)
	}
	if decision.WorkspaceID != "workspace-agent" {
		t.Fatalf("execution Workspace = %q, want agent-owning workspace-agent", decision.WorkspaceID)
	}
	if decision.ProjectWorkspaceID != "workspace-project" || decision.ProjectID != "project-1" {
		t.Fatalf("Project scope changed: %+v", decision)
	}
	if decision.SquadID != "squad-agent-workspace" {
		t.Fatalf("Squad identity = %q, want authoritative task binding", decision.SquadID)
	}
}
