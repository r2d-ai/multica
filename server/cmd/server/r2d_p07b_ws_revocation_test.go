package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/realtime"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// P07-B end-to-end revocation matrix: a real WebSocket connection subscribes to
// a Project room through the production event bus and the real DB-backed
// ScopeAuthorizer, the underlying grant/membership is revoked while the socket
// stays open, and the next Project event must not be delivered. This is the
// cross-boundary half of the fix; hub_test.go pins the delivery gate itself.

// p07bAllowAllMembership admits every connection. The connection-time check is
// not under test — the Project scope authorizer is.
type p07bAllowAllMembership struct{}

func (p07bAllowAllMembership) IsMember(context.Context, string, string) bool { return true }

// p07bWSServer wires a real Hub + real DB ScopeAuthorizer behind a real
// WebSocket endpoint and returns the event bus carrying the production fanout.
func p07bWSServer(t *testing.T) (*realtime.Hub, *httptest.Server, *events.Bus) {
	t.Helper()
	hub := realtime.NewHub()
	hub.SetAuthorizer(newScopeAuthorizer(db.New(testPool)))
	go hub.Run()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		realtime.HandleWebSocket(hub, p07bAllowAllMembership{}, nil, nil, w, r)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	bus := events.New()
	registerListeners(bus, hub, newProjectScopeResolver(db.New(testPool)))
	return hub, server, bus
}

// p07bReadFrame reads the next text frame or fails the test.
func p07bReadFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("expected a frame within %s: %v", timeout, err)
	}
	return string(raw)
}

// p07bExpectNoFrame asserts the socket receives nothing within timeout.
func p07bExpectNoFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, raw, err := conn.ReadMessage()
	if err == nil {
		t.Fatalf("expected no frame, got %s", raw)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("expected a read timeout, got %v", err)
		}
	}
	_ = conn.SetReadDeadline(time.Time{})
}

// p07bConnect authenticates a socket in workspaceID and joins projectID,
// asserting the join was accepted.
func p07bConnect(t *testing.T, server *httptest.Server, workspaceID, userID, projectID string) *websocket.Conn {
	t.Helper()
	token, err := generateTestJWT(userID, userID+"@p07b.test", "P07B User")
	if err != nil {
		t.Fatalf("generate jwt: %v", err)
	}

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?workspace_id=" + workspaceID
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	authMsg, _ := json.Marshal(map[string]any{
		"type":    "auth",
		"payload": map[string]string{"token": token},
	})
	if err := conn.WriteMessage(websocket.TextMessage, authMsg); err != nil {
		t.Fatalf("write auth frame: %v", err)
	}
	if got := p07bReadFrame(t, conn, 2*time.Second); !strings.Contains(got, "auth_ack") {
		t.Fatalf("expected auth_ack, got %s", got)
	}

	subMsg, _ := json.Marshal(map[string]any{
		"type":    "subscribe",
		"payload": map[string]string{"scope": realtime.ScopeProject, "id": projectID},
	})
	if err := conn.WriteMessage(websocket.TextMessage, subMsg); err != nil {
		t.Fatalf("write project subscribe frame: %v", err)
	}
	reply := p07bReadFrame(t, conn, 2*time.Second)
	if !strings.Contains(reply, "subscribe_ack") {
		t.Fatalf("expected project subscribe_ack, got %s", reply)
	}
	return conn
}

// p07bPublishIssueEvent emits the production issue:updated fanout for a
// project-backed issue. It returns only after the synchronous event bus has
// fanned the frame out.
func p07bPublishIssueEvent(bus *events.Bus, ownerWorkspace, issueID, projectID string) {
	projectIDCopy := projectID
	bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: ownerWorkspace,
		ActorType:   "member",
		ActorID:     testUserID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID:          issueID,
				WorkspaceID: ownerWorkspace,
				ProjectID:   &projectIDCopy,
				Title:       "p07b shared project issue",
				Status:      "in_progress",
				Priority:    "medium",
				CreatorType: "member",
				CreatorID:   testUserID,
			},
		},
	})
}

// TestP07BWebSocketDirectGrantRevocationStopsDelivery is the core regression:
// subscribe → receive → revoke the direct user grant → next event is dropped.
func TestP07BWebSocketDirectGrantRevocationStopsDelivery(t *testing.T) {
	if testPool == nil {
		t.Skip("integration database not available")
	}
	hub, server, bus := p07bWSServer(t)
	f := newP07AITFixture(t)

	collaborator := p07aITUser(t, "p07b-direct")
	p07aITGrant(t, f.projectID, collaborator, "viewer")

	conn := p07bConnect(t, server, f.foreignWorkspace, collaborator, f.projectID)

	p07bPublishIssueEvent(bus, f.ownerWorkspace, f.issueID, f.projectID)
	if got := p07bReadFrame(t, conn, 2*time.Second); !strings.Contains(got, protocol.EventIssueUpdated) {
		t.Fatalf("granted collaborator did not receive the project event: %s", got)
	}

	p07aITExec(t, `DELETE FROM r2d_project_grants WHERE project_id = $1 AND principal_type = 'user' AND principal_id = $2`, f.projectID, collaborator)

	p07bPublishIssueEvent(bus, f.ownerWorkspace, f.issueID, f.projectID)
	p07bExpectNoFrame(t, conn, 300*time.Millisecond)

	if hub.HasLocalSubscribers(realtime.ScopeProject, f.projectID) {
		t.Fatal("revoked subscription was not evicted from the project room")
	}
}

// TestP07BWebSocketWorkspaceGrantRevocationStopsDelivery covers the second
// principal type: deleting a workspace grant stops delivery to that workspace's
// members without a reconnect.
func TestP07BWebSocketWorkspaceGrantRevocationStopsDelivery(t *testing.T) {
	if testPool == nil {
		t.Skip("integration database not available")
	}
	_, server, bus := p07bWSServer(t)
	f := newP07AITFixture(t)

	member := p07aITUser(t, "p07b-ws-grant")
	p07cAddMember(t, f.foreignWorkspace, member, "member")
	p07cGrantWorkspace(t, f.projectID, f.foreignWorkspace, "member")

	conn := p07bConnect(t, server, f.foreignWorkspace, member, f.projectID)

	p07bPublishIssueEvent(bus, f.ownerWorkspace, f.issueID, f.projectID)
	if got := p07bReadFrame(t, conn, 2*time.Second); !strings.Contains(got, protocol.EventIssueUpdated) {
		t.Fatalf("workspace-grant member did not receive the project event: %s", got)
	}

	p07aITExec(t, `DELETE FROM r2d_project_grants WHERE project_id = $1 AND principal_type = 'workspace' AND principal_id = $2`, f.projectID, f.foreignWorkspace)

	p07bPublishIssueEvent(bus, f.ownerWorkspace, f.issueID, f.projectID)
	p07bExpectNoFrame(t, conn, 300*time.Millisecond)
}

// TestP07BWebSocketWorkspaceMembershipRemovalStopsDelivery covers the indirect
// path: the grant survives but the member is removed from the granted
// workspace, so the workspace-principal expansion no longer includes them.
func TestP07BWebSocketWorkspaceMembershipRemovalStopsDelivery(t *testing.T) {
	if testPool == nil {
		t.Skip("integration database not available")
	}
	_, server, bus := p07bWSServer(t)
	f := newP07AITFixture(t)

	member := p07aITUser(t, "p07b-ws-member")
	p07cAddMember(t, f.foreignWorkspace, member, "member")
	p07cGrantWorkspace(t, f.projectID, f.foreignWorkspace, "member")

	conn := p07bConnect(t, server, f.foreignWorkspace, member, f.projectID)

	p07bPublishIssueEvent(bus, f.ownerWorkspace, f.issueID, f.projectID)
	if got := p07bReadFrame(t, conn, 2*time.Second); !strings.Contains(got, protocol.EventIssueUpdated) {
		t.Fatalf("workspace member did not receive the project event: %s", got)
	}

	p07aITExec(t, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, f.foreignWorkspace, member)

	p07bPublishIssueEvent(bus, f.ownerWorkspace, f.issueID, f.projectID)
	p07bExpectNoFrame(t, conn, 300*time.Millisecond)
}

// TestP07BWebSocketVisibilityTransitionStopsDelivery covers a non-grant ACL
// transition: an owner-Workspace member reads a workspace-visible Project, then
// the Project becomes private and their implicit member role no longer grants
// read. The frame is published straight to the Project room so the owner
// Workspace fanout cannot mask the project-room decision.
func TestP07BWebSocketVisibilityTransitionStopsDelivery(t *testing.T) {
	if testPool == nil {
		t.Skip("integration database not available")
	}
	hub, server, _ := p07bWSServer(t)
	f := newP07AITFixture(t)

	member := p07aITUser(t, "p07b-visibility")
	p07aITExec(t, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, f.ownerWorkspace, member)
	p07aITExec(t, `UPDATE r2d_project_extra SET visibility = 'workspace' WHERE project_id = $1`, f.projectID)

	conn := p07bConnect(t, server, f.ownerWorkspace, member, f.projectID)

	frame := []byte(`{"type":"` + protocol.EventIssueUpdated + `"}`)
	hub.BroadcastToScope(realtime.ScopeProject, f.projectID, frame)
	if got := p07bReadFrame(t, conn, 2*time.Second); got != string(frame) {
		t.Fatalf("owner-workspace member did not receive the project frame: %s", got)
	}

	p07aITExec(t, `UPDATE r2d_project_extra SET visibility = 'private' WHERE project_id = $1`, f.projectID)

	hub.BroadcastToScope(realtime.ScopeProject, f.projectID, frame)
	p07bExpectNoFrame(t, conn, 300*time.Millisecond)
}

// TestP07BWebSocketProjectlessEventStaysOwnerOnly verifies the grant does not
// widen a projectless issue: a foreign collaborator subscribed to their Project
// room receives nothing for an issue with no Project.
func TestP07BWebSocketProjectlessEventStaysOwnerOnly(t *testing.T) {
	if testPool == nil {
		t.Skip("integration database not available")
	}
	_, server, bus := p07bWSServer(t)
	f := newP07AITFixture(t)

	collaborator := p07aITUser(t, "p07b-projectless")
	p07aITGrant(t, f.projectID, collaborator, "viewer")

	conn := p07bConnect(t, server, f.foreignWorkspace, collaborator, f.projectID)

	bus.Publish(events.Event{
		Type:        protocol.EventIssueUpdated,
		WorkspaceID: f.ownerWorkspace,
		ActorType:   "member",
		ActorID:     testUserID,
		Payload: map[string]any{
			"issue": handler.IssueResponse{
				ID:          f.projectlessIssue,
				WorkspaceID: f.ownerWorkspace,
				Title:       "p07b projectless issue",
				Status:      "in_progress",
				Priority:    "medium",
				CreatorType: "member",
				CreatorID:   testUserID,
			},
		},
	})

	p07bExpectNoFrame(t, conn, 300*time.Millisecond)
}
