package r2dauth

import (
	"reflect"
	"strings"
	"testing"
)

// P07-H: consolidate cross-surface leakage tests so every new decision/SQL
// shape is checked in one place before a handler can expose it.
func TestP07DecisionStructFieldAllowlists(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		value  any
		fields []string
	}{
		{
			name:  "project facts carry roles and identity only",
			value: ProjectFacts{},
			fields: []string{
				"ProjectID", "OwnerWorkspaceID", "Visibility", "OwnerWorkspaceRole",
				"DirectGrantRole", "WorkspaceGrantRole", "GlobalObserver",
			},
		},
		{
			name:  "project decision carries role and observer bit only",
			value: Decision{},
			fields: []string{
				"Role", "GlobalObserver",
			},
		},
		{
			name:  "project capabilities carry authorization booleans only",
			value: ProjectCapabilities{},
			fields: []string{
				"Role", "GlobalObserver", "Read", "Contribute", "Manage", "Share", "ViewResources",
			},
		},
		{
			name:  "agent dispatch decision carries target identity only",
			value: AgentDispatchDecision{},
			fields: []string{
				"Allowed", "Reason", "AgentID", "WorkspaceID", "IssueID", "ProjectID",
			},
		},
		{
			name:  "squad dispatch decision carries target identity only",
			value: SquadDispatchDecision{},
			fields: []string{
				"Allowed", "Reason", "SquadID", "WorkspaceID", "LeaderAgentID", "IssueID", "ProjectID",
			},
		},
		{
			name:  "task execution decision carries task scope only",
			value: TaskExecutionDecision{},
			fields: []string{
				"Allowed", "Reason", "TaskID", "AgentID", "WorkspaceID", "SquadID", "ProjectID", "ProjectWorkspaceID", "ResourceScope",
			},
		},
		{
			name:  "task resource scope carries project-owned resource projections only",
			value: TaskResourceScope{},
			fields: []string{
				"ProjectID", "ProjectWorkspaceID", "Resources", "Repos", "LocalPaths", "DaemonIDs",
			},
		},
	}

	forbiddenFragments := []string{
		"secret", "credential", "runtime_config", "integration", "workspace_mcp", "squad_member",
		"agent_runtime", "custom_env", "custom_args", "token",
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			typ := reflect.TypeOf(tt.value)
			exported := make([]string, 0, typ.NumField())
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				if field.PkgPath != "" {
					continue
				}
				exported = append(exported, field.Name)
				lower := strings.ToLower(field.Name)
				for _, forbidden := range forbiddenFragments {
					if strings.Contains(lower, forbidden) {
						t.Fatalf("field %q exposes forbidden inventory fragment %q", field.Name, forbidden)
					}
				}
			}
			if !reflect.DeepEqual(exported, tt.fields) {
				t.Fatalf("exported fields = %v, want %v; update this allowlist deliberately", exported, tt.fields)
			}
		})
	}
}

func TestP07AuthorizationQueriesDoNotLoadWorkspaceInventory(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "project facts", query: projectFactsSQL},
		{name: "candidate project facts", query: candidateProjectFactsSQL},
		{name: "agent dispatch facts", query: agentDispatchFactsSQL},
		{name: "task execution facts", query: taskExecutionFactsSQL},
		{name: "task resource facts", query: taskResourceFactsSQL},
		{name: "squad dispatch facts", query: squadDispatchFactsSQL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lower := strings.ToLower(tc.query)
			for _, forbidden := range []string{
				"runtime_config", "custom_env", "custom_args", "secret", "credential",
				"integration", "workspace_mcp", "agent_runtime", "squad_member",
			} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("authorization query contains forbidden workspace-inventory source %q", forbidden)
				}
			}
		})
	}
}

func TestP07PrincipalResourceMatrixSharedProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		facts         ProjectFacts
		read          bool
		contribute    bool
		manage        bool
		share         bool
		viewResources bool
	}{
		{
			name:          "owner workspace owner",
			facts:         ProjectFacts{Visibility: VisibilityPrivate, OwnerWorkspaceRole: WorkspaceRoleOwner},
			read:          true,
			contribute:    true,
			manage:        true,
			share:         true,
			viewResources: true,
		},
		{
			name:          "owner workspace admin",
			facts:         ProjectFacts{Visibility: VisibilityPrivate, OwnerWorkspaceRole: WorkspaceRoleAdmin},
			read:          true,
			contribute:    true,
			manage:        true,
			share:         true,
			viewResources: true,
		},
		{
			name:          "owner workspace member",
			facts:         ProjectFacts{Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleMember},
			read:          true,
			contribute:    true,
			viewResources: true,
		},
		{
			name:       "foreign direct viewer grant",
			facts:      ProjectFacts{Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleViewer},
			read:       true,
			contribute: false,
		},
		{
			name:       "foreign direct member grant",
			facts:      ProjectFacts{Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleMember},
			read:       true,
			contribute: true,
		},
		{
			name:       "foreign direct manager grant",
			facts:      ProjectFacts{Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleManager},
			read:       true,
			manage:     true,
			share:      true,
			contribute: true,
		},
		{
			name:       "foreign workspace grant member",
			facts:      ProjectFacts{Visibility: VisibilityPrivate, WorkspaceGrantRole: ProjectRoleMember},
			read:       true,
			contribute: true,
		},
		{
			name:  "foreign Team Leader without grant",
			facts: ProjectFacts{Visibility: VisibilityPrivate},
		},
		{
			name:  "foreign no grant",
			facts: ProjectFacts{Visibility: VisibilityPrivate},
		},
		{
			name:       "global observer",
			facts:      ProjectFacts{Visibility: VisibilityPrivate, GlobalObserver: true},
			read:       true,
			contribute: false,
		},
		{
			name:  "revoked user",
			facts: ProjectFacts{Visibility: VisibilityPrivate},
		},
		{
			name:  "disabled user",
			facts: ProjectFacts{Visibility: VisibilityPrivate},
		},
		{
			name:  "system admin without explicit R2D role",
			facts: ProjectFacts{Visibility: VisibilityPrivate},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveCapabilities(tt.facts)
			if got.Read != tt.read ||
				got.Contribute != tt.contribute ||
				got.Manage != tt.manage ||
				got.Share != tt.share ||
				got.ViewResources != tt.viewResources {
				t.Fatalf(
					"capabilities = %#v, want read=%t contribute=%t manage=%t share=%t view_resources=%t",
					got, tt.read, tt.contribute, tt.manage, tt.share, tt.viewResources,
				)
			}
		})
	}
}

func TestP07PrincipalResourceMatrixProjectlessIssueStaysWorkspaceBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		actorWorkspaceID string
		issueWorkspaceID string
		allowDispatch    bool
		allowExecution   bool
	}{
		{
			name:             "owner workspace owner admin member",
			actorWorkspaceID: "workspace-a",
			issueWorkspaceID: "workspace-a",
			allowDispatch:    true,
			allowExecution:   true,
		},
		{
			name:             "foreign direct viewer member manager",
			actorWorkspaceID: "workspace-b",
			issueWorkspaceID: "workspace-a",
		},
		{
			name:             "foreign workspace grant member",
			actorWorkspaceID: "workspace-b",
			issueWorkspaceID: "workspace-a",
		},
		{
			name:             "foreign Team Leader without grant",
			actorWorkspaceID: "workspace-b",
			issueWorkspaceID: "workspace-a",
		},
		{
			name:             "foreign no grant",
			actorWorkspaceID: "workspace-b",
			issueWorkspaceID: "workspace-a",
		},
		{
			name:             "global observer",
			actorWorkspaceID: "workspace-b",
			issueWorkspaceID: "workspace-a",
		},
		{
			name:             "revoked user",
			actorWorkspaceID: "workspace-b",
			issueWorkspaceID: "workspace-a",
		},
		{
			name:             "disabled user",
			actorWorkspaceID: "workspace-b",
			issueWorkspaceID: "workspace-a",
		},
		{
			name:             "system admin where not explicitly modeled",
			actorWorkspaceID: "workspace-b",
			issueWorkspaceID: "workspace-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dispatchFacts := baseAgentDispatchFacts()
			dispatchFacts.AgentWorkspaceID = tt.actorWorkspaceID
			dispatchFacts.IssueWorkspaceID = tt.issueWorkspaceID
			dispatchFacts.ProjectID = ""
			dispatchFacts.ResolvedProjectID = ""
			dispatchFacts.ProjectWorkspaceID = ""
			dispatchFacts.ActorWorkspaceRole = WorkspaceRoleMember
			dispatchDecision := ResolveAgentDispatch(tt.actorWorkspaceID, "issue-a", "agent-b", dispatchFacts)
			if dispatchDecision.Allowed != tt.allowDispatch {
				t.Fatalf("dispatch allowed = %t, want %t; decision=%+v", dispatchDecision.Allowed, tt.allowDispatch, dispatchDecision)
			}

			execFacts := baseProjectTaskFacts()
			execFacts.AgentWorkspaceID = tt.actorWorkspaceID
			execFacts.IssueWorkspaceID = tt.issueWorkspaceID
			execFacts.ProjectID = ""
			execFacts.ResolvedProjectID = ""
			execFacts.ProjectWorkspaceID = ""
			execDecision := ResolveTaskExecution(TaskExecutionActor{
				Source:      TaskActorSourceTaskToken,
				TaskID:      execFacts.TaskID,
				AgentID:     execFacts.TaskAgentID,
				WorkspaceID: tt.actorWorkspaceID,
			}, execFacts)
			if execDecision.Allowed != tt.allowExecution {
				t.Fatalf("execution allowed = %t, want %t; decision=%+v", execDecision.Allowed, tt.allowExecution, execDecision)
			}
		})
	}
}
