package main

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/realtime"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// fakeScopeQuerier implements scopeAuthQuerier with in-memory maps.
type fakeScopeQuerier struct {
	tasks    map[[16]byte]db.AgentTaskQueue
	issues   map[[16]byte]db.Issue
	sessions map[[16]byte]db.ChatSession
	projects map[string]db.R2DProjectAccessFacts
}

func (f *fakeScopeQuerier) R2DLoadProjectAccessFacts(_ context.Context, _, projectID string) (db.R2DProjectAccessFacts, error) {
	if p, ok := f.projects[projectID]; ok {
		return p, nil
	}
	return db.R2DProjectAccessFacts{}, pgx.ErrNoRows
}

func (f *fakeScopeQuerier) GetAgentTask(_ context.Context, id pgtype.UUID) (db.AgentTaskQueue, error) {
	if t, ok := f.tasks[id.Bytes]; ok {
		return t, nil
	}
	return db.AgentTaskQueue{}, pgx.ErrNoRows
}
func (f *fakeScopeQuerier) GetIssue(_ context.Context, id pgtype.UUID) (db.Issue, error) {
	if i, ok := f.issues[id.Bytes]; ok {
		return i, nil
	}
	return db.Issue{}, pgx.ErrNoRows
}
func (f *fakeScopeQuerier) GetChatSession(_ context.Context, id pgtype.UUID) (db.ChatSession, error) {
	if s, ok := f.sessions[id.Bytes]; ok {
		return s, nil
	}
	return db.ChatSession{}, pgx.ErrNoRows
}

func mustUUID(t *testing.T) (string, pgtype.UUID) {
	t.Helper()
	u, err := uuid.NewRandom()
	if err != nil {
		t.Fatal(err)
	}
	return u.String(), pgtype.UUID{Bytes: u, Valid: true}
}

// TestScopeAuthorizer_ChatRequiresCreator pins must-fix #2 from PR #1429:
// ScopeChat MUST verify CreatorID == userID. A workspace peer that knows the
// session_id must NOT be able to subscribe to chat:message / chat:done /
// chat:session_read for that private session.
func TestScopeAuthorizer_ChatRequiresCreator(t *testing.T) {
	wsStr, wsUUID := mustUUID(t)
	creatorStr, creatorUUID := mustUUID(t)
	otherStr, _ := mustUUID(t)
	sessStr, sessUUID := mustUUID(t)
	otherWsStr, _ := mustUUID(t)
	otherWsStrOnly, otherWsUUID := mustUUID(t)
	_ = otherWsStrOnly

	q := &fakeScopeQuerier{
		sessions: map[[16]byte]db.ChatSession{
			sessUUID.Bytes: {
				ID:          sessUUID,
				WorkspaceID: wsUUID,
				CreatorID:   creatorUUID,
			},
		},
	}
	a := newScopeAuthorizer(q)
	ctx := context.Background()

	// Creator in matching workspace → allowed.
	ok, err := a.AuthorizeScope(ctx, creatorStr, wsStr, realtime.ScopeChat, sessStr)
	if err != nil || !ok {
		t.Fatalf("creator should be allowed: ok=%v err=%v", ok, err)
	}

	// Same workspace, different (peer) member → must be denied.
	ok, err = a.AuthorizeScope(ctx, otherStr, wsStr, realtime.ScopeChat, sessStr)
	if err != nil || ok {
		t.Fatalf("peer must be denied: ok=%v err=%v", ok, err)
	}

	// Cross-workspace creator (e.g. session in workspace A, request in
	// workspace B) → must be denied even though creator matches.
	ok, err = a.AuthorizeScope(ctx, creatorStr, otherWsStr, realtime.ScopeChat, sessStr)
	if err != nil || ok {
		t.Fatalf("cross-workspace must be denied: ok=%v err=%v", ok, err)
	}
	_ = otherWsUUID

	// Empty userID → must be denied (defensive).
	ok, err = a.AuthorizeScope(ctx, "", wsStr, realtime.ScopeChat, sessStr)
	if err != nil || ok {
		t.Fatalf("empty userID must be denied: ok=%v err=%v", ok, err)
	}

	// Unknown session → denied.
	_, missingStr := mustUUID(t)
	_ = missingStr
	missingUUID, _ := uuid.NewRandom()
	ok, err = a.AuthorizeScope(ctx, creatorStr, wsStr, realtime.ScopeChat, missingUUID.String())
	if err != nil || ok {
		t.Fatalf("unknown session must be denied: ok=%v err=%v", ok, err)
	}
}

// TestScopeAuthorizer_ChatTaskRequiresCreator pins must-fix #2 for the
// task-scope path of chat tasks (task.ChatSessionID set, no IssueID): only
// the chat session creator may subscribe to that task's stream, since
// task:message for chat tasks contains assistant chat content.
func TestScopeAuthorizer_ChatTaskRequiresCreator(t *testing.T) {
	wsStr, wsUUID := mustUUID(t)
	creatorStr, creatorUUID := mustUUID(t)
	otherStr, _ := mustUUID(t)
	sessStr, sessUUID := mustUUID(t)
	taskStr, taskUUID := mustUUID(t)
	_ = sessStr

	q := &fakeScopeQuerier{
		tasks: map[[16]byte]db.AgentTaskQueue{
			taskUUID.Bytes: {
				ID:            taskUUID,
				ChatSessionID: sessUUID,
			},
		},
		sessions: map[[16]byte]db.ChatSession{
			sessUUID.Bytes: {
				ID:          sessUUID,
				WorkspaceID: wsUUID,
				CreatorID:   creatorUUID,
			},
		},
	}
	a := newScopeAuthorizer(q)
	ctx := context.Background()

	ok, err := a.AuthorizeScope(ctx, creatorStr, wsStr, realtime.ScopeTask, taskStr)
	if err != nil || !ok {
		t.Fatalf("creator should be allowed for chat task: ok=%v err=%v", ok, err)
	}

	ok, err = a.AuthorizeScope(ctx, otherStr, wsStr, realtime.ScopeTask, taskStr)
	if err != nil || ok {
		t.Fatalf("peer must be denied for chat task: ok=%v err=%v", ok, err)
	}
}

// TestScopeAuthorizer_IssueTaskWorkspaceOnly verifies issue tasks remain
// workspace-scoped (any member who can see the issue may subscribe).
func TestScopeAuthorizer_IssueTaskWorkspaceOnly(t *testing.T) {
	wsStr, wsUUID := mustUUID(t)
	memberStr, _ := mustUUID(t)
	otherWsStr, _ := mustUUID(t)
	taskStr, taskUUID := mustUUID(t)
	_, issueUUID := mustUUID(t)

	q := &fakeScopeQuerier{
		tasks: map[[16]byte]db.AgentTaskQueue{
			taskUUID.Bytes: {
				ID:      taskUUID,
				IssueID: issueUUID,
			},
		},
		issues: map[[16]byte]db.Issue{
			issueUUID.Bytes: {
				ID:          issueUUID,
				WorkspaceID: wsUUID,
			},
		},
	}
	a := newScopeAuthorizer(q)
	ctx := context.Background()

	ok, err := a.AuthorizeScope(ctx, memberStr, wsStr, realtime.ScopeTask, taskStr)
	if err != nil || !ok {
		t.Fatalf("member in workspace should be allowed: ok=%v err=%v", ok, err)
	}

	ok, err = a.AuthorizeScope(ctx, memberStr, otherWsStr, realtime.ScopeTask, taskStr)
	if err != nil || ok {
		t.Fatalf("cross-workspace must be denied: ok=%v err=%v", ok, err)
	}
}

// TestScopeAuthorizer_ProjectRequiresReadAccess pins the P07-B consumer
// contract: a client may join ScopeProject only when the central Project policy
// grants read, regardless of the client's own (home) workspace.
func TestScopeAuthorizer_ProjectRequiresReadAccess(t *testing.T) {
	homeWsStr, _ := mustUUID(t)
	callerStr, _ := mustUUID(t)
	ownerWsStr, _ := mustUUID(t)
	grantedStr, _ := mustUUID(t)
	deniedStr, _ := mustUUID(t)
	observerStr, _ := mustUUID(t)

	q := &fakeScopeQuerier{projects: map[string]db.R2DProjectAccessFacts{
		// Explicit cross-workspace grant → read.
		grantedStr: {ProjectID: grantedStr, OwnerWorkspaceID: ownerWsStr, Visibility: "private", DirectGrantRole: "viewer"},
		// No grant, no owner-workspace role → no read.
		deniedStr: {ProjectID: deniedStr, OwnerWorkspaceID: ownerWsStr, Visibility: "private"},
		// Deployment-wide observer → read.
		observerStr: {ProjectID: observerStr, OwnerWorkspaceID: ownerWsStr, Visibility: "private", GlobalObserver: true},
	}}
	a := newScopeAuthorizer(q)
	ctx := context.Background()

	if ok, err := a.AuthorizeScope(ctx, callerStr, homeWsStr, realtime.ScopeProject, grantedStr); err != nil || !ok {
		t.Fatalf("granted collaborator should be allowed: ok=%v err=%v", ok, err)
	}
	if ok, err := a.AuthorizeScope(ctx, callerStr, homeWsStr, realtime.ScopeProject, deniedStr); err != nil || ok {
		t.Fatalf("ungranted caller must be denied: ok=%v err=%v", ok, err)
	}
	if ok, err := a.AuthorizeScope(ctx, callerStr, homeWsStr, realtime.ScopeProject, observerStr); err != nil || !ok {
		t.Fatalf("global observer should be allowed: ok=%v err=%v", ok, err)
	}
	if ok, err := a.AuthorizeScope(ctx, "", homeWsStr, realtime.ScopeProject, grantedStr); err != nil || ok {
		t.Fatalf("empty userID must be denied: ok=%v err=%v", ok, err)
	}
	missingStr, _ := mustUUID(t)
	if ok, err := a.AuthorizeScope(ctx, callerStr, homeWsStr, realtime.ScopeProject, missingStr); err != nil || ok {
		t.Fatalf("unknown project must be a plain denial: ok=%v err=%v", ok, err)
	}
}

// failingScopeQuerier returns a non-ErrNoRows error from every lookup,
// simulating a transient database failure (pool exhaustion, cancelled
// context, network blip). Such errors must propagate out of AuthorizeScope
// so handleSubscribe reports "lookup_failed" rather than "forbidden".
type failingScopeQuerier struct{}

func (failingScopeQuerier) GetAgentTask(context.Context, pgtype.UUID) (db.AgentTaskQueue, error) {
	return db.AgentTaskQueue{}, errors.New("connection reset by peer")
}
func (failingScopeQuerier) GetIssue(context.Context, pgtype.UUID) (db.Issue, error) {
	return db.Issue{}, errors.New("connection reset by peer")
}
func (failingScopeQuerier) GetChatSession(context.Context, pgtype.UUID) (db.ChatSession, error) {
	return db.ChatSession{}, errors.New("connection reset by peer")
}
func (failingScopeQuerier) R2DLoadProjectAccessFacts(context.Context, string, string) (db.R2DProjectAccessFacts, error) {
	return db.R2DProjectAccessFacts{}, errors.New("connection reset by peer")
}

// errOnInnerQuerier succeeds for GetAgentTask (so the task path reaches its
// inner lookups) but fails GetIssue / GetChatSession with a non-ErrNoRows
// error. This isolates the inner lookup points from the outer GetAgentTask.
type errOnInnerQuerier struct {
	task db.AgentTaskQueue
}

func (q *errOnInnerQuerier) GetAgentTask(_ context.Context, _ pgtype.UUID) (db.AgentTaskQueue, error) {
	return q.task, nil
}
func (*errOnInnerQuerier) GetIssue(context.Context, pgtype.UUID) (db.Issue, error) {
	return db.Issue{}, errors.New("connection reset by peer")
}
func (*errOnInnerQuerier) GetChatSession(context.Context, pgtype.UUID) (db.ChatSession, error) {
	return db.ChatSession{}, errors.New("connection reset by peer")
}
func (*errOnInnerQuerier) R2DLoadProjectAccessFacts(context.Context, string, string) (db.R2DProjectAccessFacts, error) {
	return db.R2DProjectAccessFacts{}, errors.New("connection reset by peer")
}

// TestScopeAuthorizer_DoesNotSwallowQueryErrors pins #6037: a real database
// error at any of the four lookup points must be returned to the caller as a
// non-nil error, not silently converted to a (false, nil) "forbidden" denial.
// Only a missing resource (pgx.ErrNoRows) stays a plain denial.
func TestScopeAuthorizer_DoesNotSwallowQueryErrors(t *testing.T) {
	wsStr, _ := mustUUID(t)
	userStr, _ := mustUUID(t)
	taskStr, taskUUID := mustUUID(t)
	_, issueUUID := mustUUID(t)
	_, sessUUID := mustUUID(t)
	chatScopeStr, _ := mustUUID(t)

	a := newScopeAuthorizer(failingScopeQuerier{})
	ctx := context.Background()

	// Point 1: GetAgentTask fails on the task path.
	if _, err := a.AuthorizeScope(ctx, userStr, wsStr, realtime.ScopeTask, taskStr); err == nil {
		t.Fatalf("task-path GetAgentTask error must propagate, got err=nil")
	}
	// Point 4: GetChatSession fails on the chat path.
	if _, err := a.AuthorizeScope(ctx, userStr, wsStr, realtime.ScopeChat, chatScopeStr); err == nil {
		t.Fatalf("chat-path GetChatSession error must propagate, got err=nil")
	}
	// Point 5: Project facts lookup fails on the project path.
	if _, err := a.AuthorizeScope(ctx, userStr, wsStr, realtime.ScopeProject, chatScopeStr); err == nil {
		t.Fatalf("project-path R2DLoadProjectAccessFacts error must propagate, got err=nil")
	}

	// Point 2: GetIssue fails (task resolves to an issue task).
	issueAuth := newScopeAuthorizer(&errOnInnerQuerier{
		task: db.AgentTaskQueue{ID: taskUUID, IssueID: issueUUID},
	})
	if _, err := issueAuth.AuthorizeScope(ctx, userStr, wsStr, realtime.ScopeTask, taskStr); err == nil {
		t.Fatalf("task-path GetIssue error must propagate, got err=nil")
	}

	// Point 3: GetChatSession fails on the task path (task resolves to a chat task).
	chatTaskAuth := newScopeAuthorizer(&errOnInnerQuerier{
		task: db.AgentTaskQueue{ID: taskUUID, ChatSessionID: sessUUID},
	})
	if _, err := chatTaskAuth.AuthorizeScope(ctx, userStr, wsStr, realtime.ScopeTask, taskStr); err == nil {
		t.Fatalf("task-path GetChatSession error must propagate, got err=nil")
	}
}

// TestScopeAuthorizer_MissingResourceIsPlainDenial pins the other half of
// #6037: a missing resource (pgx.ErrNoRows) must remain a (false, nil)
// denial — not-found is reported as "forbidden", matching the HTTP layer's
// 404-not-403 convention.
func TestScopeAuthorizer_MissingResourceIsPlainDenial(t *testing.T) {
	wsStr, _ := mustUUID(t)
	userStr, _ := mustUUID(t)
	missingTask, _ := mustUUID(t)
	missingChat, _ := mustUUID(t)

	a := newScopeAuthorizer(&fakeScopeQuerier{})
	ctx := context.Background()

	if ok, err := a.AuthorizeScope(ctx, userStr, wsStr, realtime.ScopeTask, missingTask); err != nil || ok {
		t.Fatalf("missing task must be a plain denial: ok=%v err=%v", ok, err)
	}
	if ok, err := a.AuthorizeScope(ctx, userStr, wsStr, realtime.ScopeChat, missingChat); err != nil || ok {
		t.Fatalf("missing chat session must be a plain denial: ok=%v err=%v", ok, err)
	}
}
