package main

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// P07-C delivery matrix: a Project-backed issue must reach its explicit
// r2d_project_grants principals (direct user grants and workspace grants
// expanded to members) in addition to the ordinary issue_subscriber set, while
// a projectless issue, a revoked grant, a removed workspace member, and a
// foreign user with no grant must all receive nothing.
//
// These are fan-out assertions on inbox_item rows, the same layer
// notification_listeners_test.go asserts on. Read-path surfacing of
// cross-workspace inbox rows is tracked separately from recipient-set
// correctness.

// p07cGrantWorkspace adds a workspace-principal grant and cleans it up.
func p07cGrantWorkspace(t *testing.T, projectID, workspaceID, role string) {
	t.Helper()
	id := uuid.NewString()
	p07aITExec(t, `
INSERT INTO r2d_project_grants (id, project_id, principal_type, principal_id, role, created_by)
VALUES ($1, $2, 'workspace', $3, $4, $5)`, id, projectID, workspaceID, role, testUserID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM r2d_project_grants WHERE id = $1`, id)
	})
}

func p07cAddMember(t *testing.T, workspaceID, userID, role string) {
	t.Helper()
	p07aITExec(t, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`, workspaceID, userID, role)
}

// p07cInboxTypes returns the non-archived inbox notification types for a
// recipient in the given workspace.
func p07cInboxTypes(t *testing.T, workspaceID, recipientID string) []string {
	t.Helper()
	rows, err := testPool.Query(context.Background(), `
SELECT type FROM inbox_item
WHERE workspace_id = $1 AND recipient_type = 'member' AND recipient_id = $2 AND archived = false
ORDER BY created_at`, workspaceID, recipientID)
	if err != nil {
		t.Fatalf("query inbox types: %v", err)
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var notifType string
		if err := rows.Scan(&notifType); err != nil {
			t.Fatalf("scan inbox type: %v", err)
		}
		out = append(out, notifType)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate inbox types: %v", err)
	}
	return out
}

func p07cHasType(types []string, want string) bool {
	for _, notifType := range types {
		if notifType == want {
			return true
		}
	}
	return false
}

func p07cIssueResponse(workspaceID, issueID, status string) handler.IssueResponse {
	return handler.IssueResponse{
		ID:          issueID,
		WorkspaceID: workspaceID,
		Title:       "p07c shared project issue",
		Status:      status,
		Priority:    "medium",
		CreatorType: "member",
		CreatorID:   testUserID,
	}
}

// p07cPublishStatusChange fires the issue:updated status transition the
// notification listener turns into a "status_changed" inbox row.
func p07cPublishStatusChange(bus *events.Bus, workspaceID, issueID, newStatus string) {
	bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: workspaceID,
		ActorType:   "member",
		ActorID:     testUserID,
		Payload: map[string]any{
			"issue":            p07cIssueResponse(workspaceID, issueID, newStatus),
			"assignee_changed": false,
			"status_changed":   true,
			"prev_status":      "todo",
		},
	})
}

// p07cPublishTaskFailed fires the task:failed event, an action_required
// notification that only contributor-level Project roles may receive.
func p07cPublishTaskFailed(bus *events.Bus, workspaceID, issueID string) {
	bus.Publish(events.Event{
		Type:        protocol.EventTaskFailed,
		WorkspaceID: workspaceID,
		ActorType:   "system",
		Payload: map[string]any{
			"task_id":  uuid.NewString(),
			"agent_id": "00000000-0000-0000-0000-aaaaaaaaaaaa",
			"issue_id": issueID,
		},
	})
}

// p07cProjectFixture reuses the P07-A fixture (private shared project in a
// separate owner workspace, plus a projectless issue) and drops inbox rows on
// cleanup.
func p07cProjectFixture(t *testing.T) *p07aITFixture {
	t.Helper()
	f := newP07AITFixture(t)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, f.issueID)
		testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE issue_id = $1`, f.projectlessIssue)
	})
	return f
}

// TestP07CNotification_DirectUserGrantReceivesSharedProjectEvent is the core
// positive case: a foreign user holding a direct viewer grant, with no
// issue_subscriber row, receives a Project-backed issue event.
func TestP07CNotification_DirectUserGrantReceivesSharedProjectEvent(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)
	f := p07cProjectFixture(t)

	collaborator := p07aITUser(t, "direct-viewer")
	p07aITGrant(t, f.projectID, collaborator, "viewer")

	p07cPublishStatusChange(bus, f.ownerWorkspace, f.issueID, "in_progress")

	types := p07cInboxTypes(t, f.ownerWorkspace, collaborator)
	if !p07cHasType(types, "status_changed") {
		t.Fatalf("expected granted foreign collaborator to receive status_changed, got %v", types)
	}

	// The actor (owner-workspace creator) is still excluded.
	if types := p07cInboxTypes(t, f.ownerWorkspace, testUserID); len(types) != 0 {
		t.Fatalf("expected actor to receive nothing, got %v", types)
	}
}

// TestP07CNotification_WorkspaceGrantExpandsToMembers verifies the second
// principal type: a workspace grant reaches the individual members of the
// grantee workspace, and only those members.
func TestP07CNotification_WorkspaceGrantExpandsToMembers(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)
	f := p07cProjectFixture(t)

	member := p07aITUser(t, "ws-grant-member")
	p07cAddMember(t, f.foreignWorkspace, member, "member")
	nonMember := p07aITUser(t, "ws-grant-nonmember")

	p07cGrantWorkspace(t, f.projectID, f.foreignWorkspace, "member")

	p07cPublishStatusChange(bus, f.ownerWorkspace, f.issueID, "in_progress")

	if types := p07cInboxTypes(t, f.ownerWorkspace, member); !p07cHasType(types, "status_changed") {
		t.Fatalf("expected workspace-grant member to receive status_changed, got %v", types)
	}
	if types := p07cInboxTypes(t, f.ownerWorkspace, nonMember); len(types) != 0 {
		t.Fatalf("expected a non-member of the grantee workspace to receive nothing, got %v", types)
	}
}

// TestP07CNotification_RevokedAndRemovedPrincipalsReceiveNothing pins the
// revocation contract: a deleted grant and a member removed from a granted
// workspace both drop out at delivery time.
func TestP07CNotification_RevokedAndRemovedPrincipalsReceiveNothing(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)
	f := p07cProjectFixture(t)

	revoked := p07aITUser(t, "revoked-grant")
	p07aITGrant(t, f.projectID, revoked, "viewer")
	p07aITExec(t, `DELETE FROM r2d_project_grants WHERE project_id = $1 AND principal_type = 'user' AND principal_id = $2`, f.projectID, revoked)

	removedMember := p07aITUser(t, "removed-ws-member")
	p07cAddMember(t, f.foreignWorkspace, removedMember, "member")
	p07cGrantWorkspace(t, f.projectID, f.foreignWorkspace, "member")
	p07aITExec(t, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, f.foreignWorkspace, removedMember)

	// The fork persists no "disabled" flag; the inactive-principal signal for a
	// direct grant is that the user record is gone.
	deletedPrincipal := p07aITUser(t, "deleted-principal")
	p07aITGrant(t, f.projectID, deletedPrincipal, "viewer")
	p07aITExec(t, `DELETE FROM "user" WHERE id = $1`, deletedPrincipal)

	p07cPublishStatusChange(bus, f.ownerWorkspace, f.issueID, "in_progress")

	cases := map[string]string{
		"revoked grant":     revoked,
		"removed member":    removedMember,
		"deleted principal": deletedPrincipal,
	}
	for name, id := range cases {
		if types := p07cInboxTypes(t, f.ownerWorkspace, id); len(types) != 0 {
			t.Fatalf("%s: expected no notifications, got %v", name, types)
		}
	}
}

// TestP07CNotification_ProjectlessIssueNeverFansOut verifies that a Project
// grant does not widen a projectless issue: the fan-out resolves the issue's
// Project and stops when there is none.
func TestP07CNotification_ProjectlessIssueNeverFansOut(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)
	f := p07cProjectFixture(t)

	collaborator := p07aITUser(t, "projectless-viewer")
	p07aITGrant(t, f.projectID, collaborator, "manager")

	p07cPublishStatusChange(bus, f.ownerWorkspace, f.projectlessIssue, "in_progress")

	if types := p07cInboxTypes(t, f.ownerWorkspace, collaborator); len(types) != 0 {
		t.Fatalf("expected no fan-out on a projectless issue, got %v", types)
	}
}

// TestP07CNotification_ContributeGate verifies the operation split: a viewer
// receives informational events but not action_required ones, while a member
// receives both.
func TestP07CNotification_ContributeGate(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)
	f := p07cProjectFixture(t)

	viewer := p07aITUser(t, "gate-viewer")
	p07aITGrant(t, f.projectID, viewer, "viewer")

	contributor := p07aITUser(t, "gate-member")
	p07aITGrant(t, f.projectID, contributor, "member")

	p07cPublishStatusChange(bus, f.ownerWorkspace, f.issueID, "in_progress")
	p07cPublishTaskFailed(bus, f.ownerWorkspace, f.issueID)

	viewerTypes := p07cInboxTypes(t, f.ownerWorkspace, viewer)
	if !p07cHasType(viewerTypes, "status_changed") {
		t.Fatalf("expected viewer to receive status_changed, got %v", viewerTypes)
	}
	if p07cHasType(viewerTypes, "task_failed") {
		t.Fatalf("viewer must not receive contributor-only task_failed, got %v", viewerTypes)
	}

	contributorTypes := p07cInboxTypes(t, f.ownerWorkspace, contributor)
	if !p07cHasType(contributorTypes, "status_changed") || !p07cHasType(contributorTypes, "task_failed") {
		t.Fatalf("expected member to receive status_changed and task_failed, got %v", contributorTypes)
	}
}

// TestP07CNotification_MutedGrantRecipient verifies that delivery preferences
// still gate project-grant fan-out: muting a group suppresses that group only.
func TestP07CNotification_MutedGrantRecipient(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)
	f := p07cProjectFixture(t)

	muted := p07aITUser(t, "muted-grant")
	// Preferences are workspace-scoped, so the recipient is a member of the
	// issue's workspace (with an explicit grant, since the project is private).
	p07cAddMember(t, f.ownerWorkspace, muted, "member")
	p07aITGrant(t, f.projectID, muted, "viewer")
	p07aITExec(t, `
INSERT INTO notification_preference (workspace_id, user_id, preferences)
VALUES ($1, $2, '{"status_changes":"muted"}'::jsonb)`, f.ownerWorkspace, muted)

	unmuted := p07aITUser(t, "unmuted-grant")
	p07cAddMember(t, f.ownerWorkspace, unmuted, "member")
	p07aITGrant(t, f.projectID, unmuted, "viewer")

	p07cPublishStatusChange(bus, f.ownerWorkspace, f.issueID, "in_progress")

	if types := p07cInboxTypes(t, f.ownerWorkspace, muted); len(types) != 0 {
		t.Fatalf("expected muted grant recipient to receive nothing, got %v", types)
	}
	if types := p07cInboxTypes(t, f.ownerWorkspace, unmuted); !p07cHasType(types, "status_changed") {
		t.Fatalf("expected unmuted grant recipient to receive status_changed, got %v", types)
	}
}

// TestP07CNotification_GlobalObserverPerPreference verifies the observer row of
// the P07 matrix: an observer is not a Project member, so access and delivery
// come from an explicit grant, and preferences still gate what lands.
func TestP07CNotification_GlobalObserverPerPreference(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)
	f := p07cProjectFixture(t)

	observer := p07aITUser(t, "observer-granted")
	p07aITObserver(t, observer)
	p07cAddMember(t, f.ownerWorkspace, observer, "member")
	p07aITGrant(t, f.projectID, observer, "viewer")

	unGrantedObserver := p07aITUser(t, "observer-ungranted")
	p07aITObserver(t, unGrantedObserver)

	p07cPublishStatusChange(bus, f.ownerWorkspace, f.issueID, "in_progress")

	if types := p07cInboxTypes(t, f.ownerWorkspace, observer); !p07cHasType(types, "status_changed") {
		t.Fatalf("expected granted global observer to receive status_changed, got %v", types)
	}
	if types := p07cInboxTypes(t, f.ownerWorkspace, unGrantedObserver); len(types) != 0 {
		t.Fatalf("expected ungranted global observer to receive nothing, got %v", types)
	}
}

// TestP07CNotification_NoCrossUserLeak walks the negative half of the
// principal x project matrix in one place: nobody without a grant, and nobody
// on an unshared project, ever gets a row, and no recipient receives a row
// addressed to another user.
func TestP07CNotification_NoCrossUserLeak(t *testing.T) {
	queries := db.New(testPool)
	bus := newNotificationBus(t, queries)
	f := p07cProjectFixture(t)

	granted := p07aITUser(t, "leak-granted")
	p07aITGrant(t, f.projectID, granted, "viewer")

	ungranted := p07aITUser(t, "leak-ungranted")
	ownerMemberNoGrant := p07aITUser(t, "leak-owner-member")
	p07cAddMember(t, f.ownerWorkspace, ownerMemberNoGrant, "member")

	p07cPublishStatusChange(bus, f.ownerWorkspace, f.issueID, "in_progress")

	if types := p07cInboxTypes(t, f.ownerWorkspace, granted); !p07cHasType(types, "status_changed") {
		t.Fatalf("expected granted user to receive status_changed, got %v", types)
	}

	noLeak := map[string]string{
		"foreign user with no grant":      ungranted,
		"owner-workspace member no grant": ownerMemberNoGrant,
	}
	for name, id := range noLeak {
		if types := p07cInboxTypes(t, f.ownerWorkspace, id); len(types) != 0 {
			t.Fatalf("%s: expected no notifications, got %v", name, types)
		}
	}

	// The fan-out recipient set for this event is exactly the granted user:
	// subscriber delivery contributes nothing (no subscribers) and the actor is
	// excluded, so any extra row would be a cross-user leak.
	var rows int
	if err := testPool.QueryRow(context.Background(), `
SELECT count(*) FROM inbox_item
WHERE workspace_id = $1 AND issue_id = $2 AND archived = false`, f.ownerWorkspace, f.issueID).Scan(&rows); err != nil {
		t.Fatalf("count inbox rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("expected exactly 1 project fan-out row, got %d", rows)
	}
}
