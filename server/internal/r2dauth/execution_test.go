package r2dauth

import (
	"context"
	"errors"
	"testing"
)

type fakeTaskExecutionStore struct {
	facts       TaskExecutionFacts
	err         error
	requestedID string
}

func (f *fakeTaskExecutionStore) LoadTaskExecutionFacts(_ context.Context, taskID string) (TaskExecutionFacts, error) {
	f.requestedID = taskID
	return f.facts, f.err
}

func baseTaskActor() TaskExecutionActor {
	return TaskExecutionActor{
		Source:      TaskActorSourceTaskToken,
		TaskID:      "task-1",
		AgentID:     "agent-1",
		WorkspaceID: "workspace-agent",
	}
}

func baseProjectTaskFacts() TaskExecutionFacts {
	return TaskExecutionFacts{
		TaskID:             "task-1",
		TaskAgentID:        "agent-1",
		TaskIssueID:        "issue-1",
		TaskSquadID:        "squad-1",
		AgentWorkspaceID:   "workspace-agent",
		ResolvedIssueID:    "issue-1",
		IssueWorkspaceID:   "workspace-project",
		ProjectID:          "project-1",
		ResolvedProjectID:  "project-1",
		ProjectWorkspaceID: "workspace-project",
	}
}

func TestResolveTaskExecutionProjectScopedCrossWorkspace(t *testing.T) {
	decision := ResolveTaskExecution(baseTaskActor(), baseProjectTaskFacts())
	if !decision.Allowed {
		t.Fatalf("expected project-scoped task to be allowed, got reason %q", decision.Reason)
	}
	if decision.ProjectID != "project-1" {
		t.Fatalf("expected project-1, got %q", decision.ProjectID)
	}
	if decision.WorkspaceID != "workspace-agent" {
		t.Fatalf("expected agent workspace, got %q", decision.WorkspaceID)
	}
	if decision.ProjectWorkspaceID != "workspace-project" {
		t.Fatalf("expected project workspace, got %q", decision.ProjectWorkspaceID)
	}
	if decision.SquadID != "squad-1" {
		t.Fatalf("expected squad identity to survive decision, got %q", decision.SquadID)
	}
}

func TestResolveTaskExecutionProjectlessIssueStaysWorkspaceBound(t *testing.T) {
	facts := baseProjectTaskFacts()
	facts.ProjectID = ""
	facts.ResolvedProjectID = ""
	facts.ProjectWorkspaceID = ""
	facts.IssueWorkspaceID = "workspace-agent"

	decision := ResolveTaskExecution(baseTaskActor(), facts)
	if !decision.Allowed {
		t.Fatalf("expected same-workspace projectless issue task to be allowed, got reason %q", decision.Reason)
	}
	if decision.ProjectID != "" {
		t.Fatalf("projectless task unexpectedly gained project %q", decision.ProjectID)
	}
}

func TestResolveTaskExecutionProjectlessIssueRejectsCrossWorkspace(t *testing.T) {
	facts := baseProjectTaskFacts()
	facts.ProjectID = ""
	facts.ResolvedProjectID = ""
	facts.ProjectWorkspaceID = ""

	decision := ResolveTaskExecution(baseTaskActor(), facts)
	if decision.Allowed || decision.Reason != TaskExecutionDenyIssueWorkspaceMismatch {
		t.Fatalf("expected issue workspace mismatch denial, got %+v", decision)
	}
}

func TestResolveTaskExecutionNonIssueTaskStaysAgentWorkspaceBound(t *testing.T) {
	facts := TaskExecutionFacts{
		TaskID:           "task-1",
		TaskAgentID:      "agent-1",
		AgentWorkspaceID: "workspace-agent",
	}

	decision := ResolveTaskExecution(baseTaskActor(), facts)
	if !decision.Allowed {
		t.Fatalf("expected non-issue task to be allowed in agent workspace, got reason %q", decision.Reason)
	}
	if decision.ProjectID != "" || decision.ProjectWorkspaceID != "" {
		t.Fatalf("non-issue task unexpectedly gained project scope: %+v", decision)
	}
}

func TestResolveTaskExecutionDoesNotAcceptHumanActor(t *testing.T) {
	actor := baseTaskActor()
	actor.Source = "human"
	decision := ResolveTaskExecution(actor, baseProjectTaskFacts())
	if decision.Allowed || decision.Reason != TaskExecutionDenyInvalidActor {
		t.Fatalf("expected human actor to fail closed, got %+v", decision)
	}
}

func TestResolveTaskExecutionExactIdentityMatrix(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*TaskExecutionActor, *TaskExecutionFacts)
		reason TaskExecutionDenyReason
	}{
		{
			name: "task mismatch",
			mutate: func(_ *TaskExecutionActor, f *TaskExecutionFacts) {
				f.TaskID = "task-other"
			},
			reason: TaskExecutionDenyTaskMismatch,
		},
		{
			name: "agent mismatch",
			mutate: func(_ *TaskExecutionActor, f *TaskExecutionFacts) {
				f.TaskAgentID = "agent-other"
			},
			reason: TaskExecutionDenyAgentMismatch,
		},
		{
			name: "workspace mismatch",
			mutate: func(_ *TaskExecutionActor, f *TaskExecutionFacts) {
				f.AgentWorkspaceID = "workspace-other"
			},
			reason: TaskExecutionDenyWorkspaceMismatch,
		},
		{
			name: "stale issue",
			mutate: func(_ *TaskExecutionActor, f *TaskExecutionFacts) {
				f.ResolvedIssueID = ""
			},
			reason: TaskExecutionDenyStaleIssue,
		},
		{
			name: "stale project",
			mutate: func(_ *TaskExecutionActor, f *TaskExecutionFacts) {
				f.ResolvedProjectID = ""
			},
			reason: TaskExecutionDenyStaleProject,
		},
		{
			name: "project moved to another workspace",
			mutate: func(_ *TaskExecutionActor, f *TaskExecutionFacts) {
				f.ProjectWorkspaceID = "workspace-other"
			},
			reason: TaskExecutionDenyProjectWorkspaceMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actor := baseTaskActor()
			facts := baseProjectTaskFacts()
			tt.mutate(&actor, &facts)
			decision := ResolveTaskExecution(actor, facts)
			if decision.Allowed || decision.Reason != tt.reason {
				t.Fatalf("expected denial %q, got %+v", tt.reason, decision)
			}
		})
	}
}

func TestResolveTaskExecutionRejectsMalformedBindings(t *testing.T) {
	facts := TaskExecutionFacts{
		TaskID:             "task-1",
		TaskAgentID:        "agent-1",
		AgentWorkspaceID:   "workspace-agent",
		ResolvedProjectID:  "project-1",
		ProjectWorkspaceID: "workspace-agent",
	}
	decision := ResolveTaskExecution(baseTaskActor(), facts)
	if decision.Allowed || decision.Reason != TaskExecutionDenyMalformedProjectBinding {
		t.Fatalf("expected malformed binding denial, got %+v", decision)
	}
}

func TestTaskExecutionServiceLoadsFactsOnlyByAuthenticatedTask(t *testing.T) {
	store := &fakeTaskExecutionStore{facts: baseProjectTaskFacts()}
	service := NewTaskExecutionService(store)

	decision, err := service.Authorize(context.Background(), baseTaskActor())
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("expected allowed decision, got %+v", decision)
	}
	if store.requestedID != "task-1" {
		t.Fatalf("expected server lookup by authenticated task id, got %q", store.requestedID)
	}
}

func TestTaskExecutionServiceInvalidActorDoesNotHitStore(t *testing.T) {
	store := &fakeTaskExecutionStore{facts: baseProjectTaskFacts()}
	service := NewTaskExecutionService(store)
	actor := baseTaskActor()
	actor.Source = "human"

	decision, err := service.Authorize(context.Background(), actor)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if decision.Allowed || decision.Reason != TaskExecutionDenyInvalidActor {
		t.Fatalf("expected invalid actor denial, got %+v", decision)
	}
	if store.requestedID != "" {
		t.Fatalf("invalid actor should not hit store, requested %q", store.requestedID)
	}
}

func TestTaskExecutionServicePropagatesStoreFailure(t *testing.T) {
	want := errors.New("db unavailable")
	store := &fakeTaskExecutionStore{err: want}
	service := NewTaskExecutionService(store)

	_, err := service.Authorize(context.Background(), baseTaskActor())
	if !errors.Is(err, want) {
		t.Fatalf("expected store error %v, got %v", want, err)
	}
}
