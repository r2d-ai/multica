package r2dauth

import (
	"context"
	"testing"
)

type fakeAgentDispatchStore struct {
	facts AgentDispatchFacts
}

func (f *fakeAgentDispatchStore) LoadAgentDispatchFacts(_ context.Context, _, _, _ string) (AgentDispatchFacts, error) {
	return f.facts, nil
}

type fakeProjectStore struct {
	facts ProjectFacts
}

func (f *fakeProjectStore) LoadProjectFacts(_ context.Context, _, _ string) (ProjectFacts, error) {
	return f.facts, nil
}

func (f *fakeProjectStore) ListCandidateProjectFacts(_ context.Context, _ string) ([]ProjectFacts, error) {
	return []ProjectFacts{f.facts}, nil
}

func baseAgentDispatchFacts() AgentDispatchFacts {
	return AgentDispatchFacts{
		AgentID:            "agent-b",
		AgentWorkspaceID:   "workspace-b",
		IssueID:            "issue-a",
		IssueWorkspaceID:   "workspace-a",
		ProjectID:          "project-a",
		ResolvedProjectID:  "project-a",
		ProjectWorkspaceID: "workspace-a",
		ActorWorkspaceRole: WorkspaceRoleMember,
	}
}

func projectMemberFacts() ProjectFacts {
	return ProjectFacts{
		ProjectID:          "project-a",
		OwnerWorkspaceID:   "workspace-a",
		Visibility:         VisibilityPrivate,
		DirectGrantRole:    ProjectRoleMember,
	}
}

func TestAgentDispatchCrossWorkspaceProjectWithOwnAgentAllowed(t *testing.T) {
	store := &fakeAgentDispatchStore{facts: baseAgentDispatchFacts()}
	projects := NewService(&fakeProjectStore{facts: projectMemberFacts()})
	svc := NewAgentDispatchService(store, projects)

	decision, err := svc.Authorize(context.Background(), "user-b", "workspace-b", "issue-a", "agent-b")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("expected cross-workspace Project dispatch to be allowed, got %+v", decision)
	}
	if decision.ProjectID != "project-a" || decision.WorkspaceID != "workspace-b" {
		t.Fatalf("unexpected scoped decision: %+v", decision)
	}
}

func TestAgentDispatchProjectViewerCannotExecute(t *testing.T) {
	store := &fakeAgentDispatchStore{facts: baseAgentDispatchFacts()}
	projectFacts := projectMemberFacts()
	projectFacts.DirectGrantRole = ProjectRoleViewer
	svc := NewAgentDispatchService(store, NewService(&fakeProjectStore{facts: projectFacts}))

	decision, err := svc.Authorize(context.Background(), "user-b", "workspace-b", "issue-a", "agent-b")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if decision.Allowed || decision.Reason != AgentDispatchDenyProjectPermission {
		t.Fatalf("expected project permission denial, got %+v", decision)
	}
}

func TestAgentDispatchCannotUseForeignOwnerWorkspaceAgent(t *testing.T) {
	facts := baseAgentDispatchFacts()
	facts.AgentID = "agent-a"
	facts.AgentWorkspaceID = "workspace-a"
	facts.ActorWorkspaceRole = ""
	store := &fakeAgentDispatchStore{facts: facts}
	svc := NewAgentDispatchService(store, NewService(&fakeProjectStore{facts: projectMemberFacts()}))

	decision, err := svc.Authorize(context.Background(), "user-b", "workspace-b", "issue-a", "agent-a")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if decision.Allowed || decision.Reason != AgentDispatchDenyAgentWorkspaceMismatch {
		t.Fatalf("expected foreign agent workspace denial, got %+v", decision)
	}
}

func TestAgentDispatchArchivedAgentDenied(t *testing.T) {
	facts := baseAgentDispatchFacts()
	facts.AgentArchived = true
	decision := ResolveAgentDispatch("workspace-b", "issue-a", "agent-b", facts)
	if decision.Allowed || decision.Reason != AgentDispatchDenyAgentUnavailable {
		t.Fatalf("expected archived agent denial, got %+v", decision)
	}
}

func TestAgentDispatchProjectlessIssueRemainsWorkspaceBound(t *testing.T) {
	facts := baseAgentDispatchFacts()
	facts.IssueID = "issue-b"
	facts.IssueWorkspaceID = "workspace-b"
	facts.ProjectID = ""
	facts.ResolvedProjectID = ""
	facts.ProjectWorkspaceID = ""
	svc := NewAgentDispatchService(&fakeAgentDispatchStore{facts: facts}, nil)

	decision, err := svc.Authorize(context.Background(), "user-b", "workspace-b", "issue-b", "agent-b")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !decision.Allowed || decision.ProjectID != "" {
		t.Fatalf("expected projectless same-workspace dispatch, got %+v", decision)
	}
}

func TestAgentDispatchProjectlessCrossWorkspaceDenied(t *testing.T) {
	facts := baseAgentDispatchFacts()
	facts.ProjectID = ""
	facts.ResolvedProjectID = ""
	facts.ProjectWorkspaceID = ""
	svc := NewAgentDispatchService(&fakeAgentDispatchStore{facts: facts}, nil)

	decision, err := svc.Authorize(context.Background(), "user-b", "workspace-b", "issue-a", "agent-b")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if decision.Allowed || decision.Reason != AgentDispatchDenyIssueWorkspaceMismatch {
		t.Fatalf("expected projectless workspace denial, got %+v", decision)
	}
}

func TestAgentDispatchRejectsAgentSubstitution(t *testing.T) {
	facts := baseAgentDispatchFacts()
	decision := ResolveAgentDispatch("workspace-b", "issue-a", "agent-other", facts)
	if decision.Allowed || decision.Reason != AgentDispatchDenyTargetMismatch {
		t.Fatalf("expected exact agent target denial, got %+v", decision)
	}
}

func TestAgentDispatchRequiresHumanMembershipInAgentWorkspace(t *testing.T) {
	facts := baseAgentDispatchFacts()
	facts.ActorWorkspaceRole = WorkspaceRoleAgent
	decision := ResolveAgentDispatch("workspace-b", "issue-a", "agent-b", facts)
	if decision.Allowed || decision.Reason != AgentDispatchDenyActorWorkspaceMembership {
		t.Fatalf("expected human workspace membership denial, got %+v", decision)
	}
}

func TestAgentDispatchRejectsStaleProjectBinding(t *testing.T) {
	facts := baseAgentDispatchFacts()
	facts.ResolvedProjectID = ""
	decision := ResolveAgentDispatch("workspace-b", "issue-a", "agent-b", facts)
	if decision.Allowed || decision.Reason != AgentDispatchDenyStaleProject {
		t.Fatalf("expected stale project denial, got %+v", decision)
	}
}
