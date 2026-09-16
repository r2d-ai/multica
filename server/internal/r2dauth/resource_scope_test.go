package r2dauth

import "testing"

func projectTaskFactsWithResources(resources ...TaskResourceFact) TaskExecutionFacts {
	facts := baseProjectTaskFacts()
	facts.Resources = resources
	return facts
}

// The P06-D handoff: an authorized Project-scoped task receives exactly the
// Project owner Workspace's resources, projected into repo/local-path fields the
// runtime can consume.
func TestResolveTaskExecutionProjectScopeCarriesAuthorizedResources(t *testing.T) {
	facts := projectTaskFactsWithResources(
		TaskResourceFact{
			ID:           "resource-repo",
			WorkspaceID:  "workspace-project",
			ResourceType: "github_repo",
			ResourceRef:  `{"url":"https://github.com/example/project","ref":"main"}`,
		},
		TaskResourceFact{
			ID:           "resource-dir",
			WorkspaceID:  "workspace-project",
			ResourceType: "local_directory",
			ResourceRef:  `{"daemon_id":"daemon-1","local_path":"/srv/project","execution_mode":"in_place"}`,
		},
	)

	decision := ResolveTaskExecution(baseTaskActor(), facts)
	if !decision.Allowed {
		t.Fatalf("expected allowed, got reason %q", decision.Reason)
	}
	scope := decision.ResourceScope
	if scope.ProjectID != "project-1" || scope.ProjectWorkspaceID != "workspace-project" {
		t.Fatalf("scope binding = %+v, want project-1 @ workspace-project", scope)
	}
	if len(scope.Resources) != 2 {
		t.Fatalf("resources = %+v, want both rows", scope.Resources)
	}
	if len(scope.Repos) != 1 || scope.Repos[0].URL != "https://github.com/example/project" || scope.Repos[0].Ref != "main" {
		t.Fatalf("repos = %+v, want the project repo with its ref", scope.Repos)
	}
	if len(scope.LocalPaths) != 1 || scope.LocalPaths[0] != "/srv/project" {
		t.Fatalf("local paths = %+v, want /srv/project", scope.LocalPaths)
	}
	if len(scope.DaemonIDs) != 1 || scope.DaemonIDs[0] != "daemon-1" {
		t.Fatalf("daemon ids = %+v, want daemon-1", scope.DaemonIDs)
	}
}

// A resource row whose own workspace disagrees with the Project owner Workspace
// is corrupt data. It must be dropped from the scope, never handed to the
// runtime, while the legitimate in-workspace row still rides along.
func TestResolveTaskExecutionProjectScopeDropsForeignResource(t *testing.T) {
	facts := projectTaskFactsWithResources(
		TaskResourceFact{
			ID:           "resource-local",
			WorkspaceID:  "workspace-project",
			ResourceType: "github_repo",
			ResourceRef:  `{"url":"https://github.com/example/local"}`,
		},
		TaskResourceFact{
			ID:           "resource-foreign",
			WorkspaceID:  "workspace-other",
			ResourceType: "github_repo",
			ResourceRef:  `{"url":"https://github.com/example/foreign"}`,
		},
	)

	decision := ResolveTaskExecution(baseTaskActor(), facts)
	if !decision.Allowed {
		t.Fatalf("expected allowed, got reason %q", decision.Reason)
	}
	if len(decision.ResourceScope.Resources) != 1 || decision.ResourceScope.Resources[0].ID != "resource-local" {
		t.Fatalf("scope resources = %+v, want only the local row", decision.ResourceScope.Resources)
	}
	for _, repo := range decision.ResourceScope.Repos {
		if repo.URL == "https://github.com/example/foreign" {
			t.Fatalf("foreign repo leaked into scope: %+v", decision.ResourceScope.Repos)
		}
	}
}

// A known resource type with an unusable reference is not a resource the
// runtime can safely materialize; it is dropped rather than half-admitted.
func TestResolveTaskResourceScopeDropsUnusableRefs(t *testing.T) {
	decision := TaskExecutionDecision{
		Allowed:            true,
		ProjectID:          "project-1",
		ProjectWorkspaceID: "workspace-project",
	}

	scope, ok := ResolveTaskResourceScope(decision, []TaskResourceFact{
		{ID: "bad-repo", WorkspaceID: "workspace-project", ResourceType: "github_repo", ResourceRef: `{"ref":"main"}`},
		{ID: "bad-dir", WorkspaceID: "workspace-project", ResourceType: "local_directory", ResourceRef: `{not json`},
		{ID: "unknown", WorkspaceID: "workspace-project", ResourceType: "notion_page", ResourceRef: `{"page":"x"}`},
	})
	if !ok {
		t.Fatal("expected a resolved scope")
	}
	if len(scope.Repos) != 0 || len(scope.LocalPaths) != 0 || len(scope.DaemonIDs) != 0 {
		t.Fatalf("unusable refs leaked into typed fields: %+v", scope)
	}
	// Unknown types still ride the scope by id so the runtime can see them
	// without the policy inventing semantics it does not own.
	if len(scope.Resources) != 1 || scope.Resources[0].ID != "unknown" {
		t.Fatalf("scope resources = %+v, want only the unknown-type row", scope.Resources)
	}
}

func TestResolveTaskResourceScopeFailsClosed(t *testing.T) {
	allowed := TaskExecutionDecision{
		Allowed:            true,
		ProjectID:          "project-1",
		ProjectWorkspaceID: "workspace-project",
	}

	denied := allowed
	denied.Allowed = false

	noOwner := allowed
	noOwner.ProjectWorkspaceID = ""

	if _, ok := ResolveTaskResourceScope(denied, nil); ok {
		t.Fatal("denied decision must not resolve a resource scope")
	}
	if _, ok := ResolveTaskResourceScope(noOwner, nil); ok {
		t.Fatal("an unknown Project owner Workspace must fail closed")
	}
	if _, ok := ResolveTaskResourceScope(TaskExecutionDecision{Allowed: true}, nil); ok {
		t.Fatal("a Projectless decision must not resolve a Project resource scope")
	}
}

// A non-issue task never gains Project resources, even if a corrupt store hands
// them over with the row.
func TestResolveTaskExecutionNonIssueTaskRejectsResources(t *testing.T) {
	facts := TaskExecutionFacts{
		TaskID:           "task-1",
		TaskAgentID:      "agent-1",
		AgentWorkspaceID: "workspace-agent",
		Resources: []TaskResourceFact{{
			ID:           "resource-1",
			WorkspaceID:  "workspace-agent",
			ResourceType: "github_repo",
			ResourceRef:  `{"url":"https://github.com/example/x"}`,
		}},
	}

	decision := ResolveTaskExecution(baseTaskActor(), facts)
	if decision.Allowed || decision.Reason != TaskExecutionDenyMalformedProjectBinding {
		t.Fatalf("expected malformed binding denial, got %+v", decision)
	}
}
