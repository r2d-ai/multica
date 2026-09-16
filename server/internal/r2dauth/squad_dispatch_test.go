package r2dauth

import (
	"context"
	"testing"
)

type fakeSquadDispatchStore struct {
	facts SquadDispatchFacts
}

func (f *fakeSquadDispatchStore) LoadSquadDispatchFacts(_ context.Context, _, _, _ string) (SquadDispatchFacts, error) {
	return f.facts, nil
}

func baseSquadDispatchFacts() SquadDispatchFacts {
	return SquadDispatchFacts{
		SquadID:            "squad-a",
		SquadWorkspaceID:   "workspace-a",
		LeaderAgentID:      "agent-a",
		LeaderWorkspaceID:  "workspace-a",
		IssueID:            "issue-a",
		IssueWorkspaceID:   "workspace-a",
		ProjectID:          "project-a",
		ResolvedProjectID:  "project-a",
		ProjectWorkspaceID: "workspace-a",
		ActorWorkspaceRole: WorkspaceRoleMember,
	}
}

func TestSquadDispatchSameWorkspaceProjectWithOwnSquadAllowed(t *testing.T) {
	svc := NewSquadDispatchService(
		&fakeSquadDispatchStore{facts: baseSquadDispatchFacts()},
		NewService(&fakeProjectStore{facts: projectMemberFacts()}),
	)

	decision, err := svc.Authorize(context.Background(), "user-a", "workspace-a", "issue-a", "squad-a")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("expected same-workspace Project dispatch to be allowed, got %+v", decision)
	}
	if decision.SquadID != "squad-a" || decision.WorkspaceID != "workspace-a" || decision.LeaderAgentID != "agent-a" {
		t.Fatalf("unexpected scoped decision: %+v", decision)
	}
}

func TestSquadDispatchCrossWorkspaceProjectWithOwnSquadAllowed(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.SquadWorkspaceID = "workspace-b"
	facts.LeaderWorkspaceID = "workspace-b"
	svc := NewSquadDispatchService(
		&fakeSquadDispatchStore{facts: facts},
		NewService(&fakeProjectStore{facts: projectMemberFacts()}),
	)

	decision, err := svc.Authorize(context.Background(), "user-b", "workspace-b", "issue-a", "squad-a")
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

func TestSquadDispatchProjectViewerCannotExecute(t *testing.T) {
	projectFacts := projectMemberFacts()
	projectFacts.DirectGrantRole = ProjectRoleViewer
	svc := NewSquadDispatchService(
		&fakeSquadDispatchStore{facts: baseSquadDispatchFacts()},
		NewService(&fakeProjectStore{facts: projectFacts}),
	)

	decision, err := svc.Authorize(context.Background(), "user-a", "workspace-a", "issue-a", "squad-a")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if decision.Allowed || decision.Reason != SquadDispatchDenyProjectPermission {
		t.Fatalf("expected project permission denial, got %+v", decision)
	}
}

func TestSquadDispatchWithoutProjectResolverDeniesProjectIssue(t *testing.T) {
	svc := NewSquadDispatchService(&fakeSquadDispatchStore{facts: baseSquadDispatchFacts()}, nil)

	decision, err := svc.Authorize(context.Background(), "user-a", "workspace-a", "issue-a", "squad-a")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if decision.Allowed || decision.Reason != SquadDispatchDenyProjectPermission {
		t.Fatalf("expected project permission denial, got %+v", decision)
	}
}

func TestSquadDispatchCannotUseForeignOwnerWorkspaceSquad(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.ActorWorkspaceRole = ""
	svc := NewSquadDispatchService(
		&fakeSquadDispatchStore{facts: facts},
		NewService(&fakeProjectStore{facts: projectMemberFacts()}),
	)

	decision, err := svc.Authorize(context.Background(), "user-b", "workspace-b", "issue-a", "squad-a")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if decision.Allowed || decision.Reason != SquadDispatchDenySquadWorkspaceMismatch {
		t.Fatalf("expected foreign squad workspace denial, got %+v", decision)
	}
}

func TestSquadDispatchArchivedSquadDenied(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.SquadArchived = true
	decision := ResolveSquadDispatch("workspace-a", "issue-a", "squad-a", facts)
	if decision.Allowed || decision.Reason != SquadDispatchDenySquadUnavailable {
		t.Fatalf("expected archived squad denial, got %+v", decision)
	}
}

func TestSquadDispatchArchivedLeaderDenied(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.LeaderArchived = true
	decision := ResolveSquadDispatch("workspace-a", "issue-a", "squad-a", facts)
	if decision.Allowed || decision.Reason != SquadDispatchDenyLeaderUnavailable {
		t.Fatalf("expected archived leader denial, got %+v", decision)
	}
}

func TestSquadDispatchMissingLeaderDenied(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.LeaderAgentID = ""
	decision := ResolveSquadDispatch("workspace-a", "issue-a", "squad-a", facts)
	if decision.Allowed || decision.Reason != SquadDispatchDenyLeaderUnavailable {
		t.Fatalf("expected missing leader denial, got %+v", decision)
	}
}

func TestSquadDispatchLeaderWorkspaceMismatchDenied(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.LeaderWorkspaceID = "workspace-b"
	decision := ResolveSquadDispatch("workspace-a", "issue-a", "squad-a", facts)
	if decision.Allowed || decision.Reason != SquadDispatchDenyLeaderWorkspaceMismatch {
		t.Fatalf("expected leader workspace mismatch denial, got %+v", decision)
	}
}

func TestSquadDispatchProjectlessIssueRemainsWorkspaceBound(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.ProjectID = ""
	facts.ResolvedProjectID = ""
	facts.ProjectWorkspaceID = ""
	svc := NewSquadDispatchService(&fakeSquadDispatchStore{facts: facts}, nil)

	decision, err := svc.Authorize(context.Background(), "user-a", "workspace-a", "issue-a", "squad-a")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !decision.Allowed || decision.ProjectID != "" {
		t.Fatalf("expected projectless same-workspace dispatch, got %+v", decision)
	}
}

func TestSquadDispatchProjectlessCrossWorkspaceDenied(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.SquadWorkspaceID = "workspace-b"
	facts.LeaderWorkspaceID = "workspace-b"
	facts.ProjectID = ""
	facts.ResolvedProjectID = ""
	facts.ProjectWorkspaceID = ""
	svc := NewSquadDispatchService(&fakeSquadDispatchStore{facts: facts}, nil)

	decision, err := svc.Authorize(context.Background(), "user-b", "workspace-b", "issue-a", "squad-a")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if decision.Allowed || decision.Reason != SquadDispatchDenyIssueWorkspaceMismatch {
		t.Fatalf("expected projectless workspace denial, got %+v", decision)
	}
}

func TestSquadDispatchRejectsSquadSubstitution(t *testing.T) {
	decision := ResolveSquadDispatch("workspace-a", "issue-a", "squad-other", baseSquadDispatchFacts())
	if decision.Allowed || decision.Reason != SquadDispatchDenyTargetMismatch {
		t.Fatalf("expected exact squad target denial, got %+v", decision)
	}
}

func TestSquadDispatchRequiresHumanMembershipInSquadWorkspace(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.ActorWorkspaceRole = WorkspaceRoleAgent
	decision := ResolveSquadDispatch("workspace-a", "issue-a", "squad-a", facts)
	if decision.Allowed || decision.Reason != SquadDispatchDenyActorWorkspaceMembership {
		t.Fatalf("expected human workspace membership denial, got %+v", decision)
	}
}

func TestSquadDispatchRejectsStaleProjectBinding(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.ResolvedProjectID = ""
	decision := ResolveSquadDispatch("workspace-a", "issue-a", "squad-a", facts)
	if decision.Allowed || decision.Reason != SquadDispatchDenyStaleProject {
		t.Fatalf("expected stale project denial, got %+v", decision)
	}
}

func TestSquadDispatchRejectsMalformedProjectBinding(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.ProjectWorkspaceID = "workspace-b"
	decision := ResolveSquadDispatch("workspace-a", "issue-a", "squad-a", facts)
	if decision.Allowed || decision.Reason != SquadDispatchDenyMalformedProjectBinding {
		t.Fatalf("expected malformed project binding denial, got %+v", decision)
	}
}

func TestSquadDispatchRejectsMalformedProjectlessBinding(t *testing.T) {
	facts := baseSquadDispatchFacts()
	facts.ProjectID = ""
	facts.ProjectWorkspaceID = ""
	decision := ResolveSquadDispatch("workspace-a", "issue-a", "squad-a", facts)
	if decision.Allowed || decision.Reason != SquadDispatchDenyMalformedProjectBinding {
		t.Fatalf("expected malformed projectless binding denial, got %+v", decision)
	}
}

func TestSquadDispatchRejectsEmptyIdentifiers(t *testing.T) {
	decision := ResolveSquadDispatch("", "issue-a", "squad-a", baseSquadDispatchFacts())
	if decision.Allowed || decision.Reason != SquadDispatchDenyInvalidRequest {
		t.Fatalf("expected invalid request denial, got %+v", decision)
	}
}
