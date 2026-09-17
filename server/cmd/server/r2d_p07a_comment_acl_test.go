package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// P07-A comment mutation ACL integration matrix. These run through the real
// HTTP stack, so the middleware Project-scope override and the handler
// author-only enforcement are exercised together — the layer the handler-level
// tests cannot reach because the Workspace membership gate lives in the
// middleware chain.
//
// Contract under test:
//   - A foreign Project contributor may create, edit, and delete their own
//     comment; a Project viewer cannot mutate at all.
//   - A foreign member/manager cannot moderate another user's comment.
//   - Revoked/no-grant/disabled principals and projectless comments fail
//     closed with a non-disclosing 404.
//   - Owner-Workspace member and admin semantics are unchanged.

func p07aITMember(t *testing.T, workspaceID, userID, role string) {
	t.Helper()
	p07aITExec(t, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, $3)`, workspaceID, userID, role)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, workspaceID, userID)
	})
}

func p07aITCommentID(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode comment response: %v", err)
	}
	if payload.ID == "" {
		t.Fatalf("comment response missing id")
	}
	return payload.ID
}

func TestP07AIntegrationCommentMutationForeignCollaborator(t *testing.T) {
	if testServer == nil || testPool == nil {
		t.Skip("integration server not available")
	}
	f := newP07AITFixture(t)

	member := p07aITUser(t, "mut-member")
	p07aITGrant(t, f.projectID, member, "member")
	otherMember := p07aITUser(t, "mut-other-member")
	p07aITGrant(t, f.projectID, otherMember, "member")
	manager := p07aITUser(t, "mut-manager")
	p07aITGrant(t, f.projectID, manager, "manager")
	viewer := p07aITUser(t, "mut-viewer")
	p07aITGrant(t, f.projectID, viewer, "viewer")

	memberToken := p07aITToken(t, member)
	commentsPath := "/api/issues/" + f.issueID + "/comments"

	// Create own comment through the Project ACL.
	resp := p07aITRequest(t, memberToken, http.MethodPost, commentsPath, f.ownerWorkspace, map[string]any{"content": "foreign member comment"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("foreign member create comment: status = %d, want 201", resp.StatusCode)
	}
	commentID := p07aITCommentID(t, resp)
	commentPath := "/api/comments/" + commentID

	// Edit own comment.
	p07aITWantStatus(t, p07aITRequest(t, memberToken, http.MethodPut, commentPath, f.ownerWorkspace, map[string]any{"content": "edited by author"}), http.StatusOK)

	// A Project member or manager is not a moderator: another user's comment is
	// 403 (contribute reached the handler, authorship did not).
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, otherMember), http.MethodPut, commentPath, f.ownerWorkspace, map[string]any{"content": "other member edit"}), http.StatusForbidden)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, manager), http.MethodPut, commentPath, f.ownerWorkspace, map[string]any{"content": "moderated"}), http.StatusForbidden)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, manager), http.MethodDelete, commentPath, f.ownerWorkspace, nil), http.StatusForbidden)

	// A viewer cannot contribute, so the by-id route is a non-disclosing 404.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, viewer), http.MethodPut, commentPath, f.ownerWorkspace, map[string]any{"content": "viewer edit"}), http.StatusNotFound)

	// Delete own comment through the modern keep-replies route.
	p07aITWantStatus(t, p07aITRequest(t, memberToken, http.MethodDelete, commentPath+"/keep-replies", f.ownerWorkspace, nil), http.StatusNoContent)
}

func TestP07AIntegrationCommentMutationFailsClosed(t *testing.T) {
	if testServer == nil || testPool == nil {
		t.Skip("integration server not available")
	}
	f := newP07AITFixture(t)

	// A user in another Workspace with no Project grant.
	noGrant := p07aITUser(t, "fail-nogrant")
	// A member of another Workspace — Workspace membership is not a Project grant.
	foreignWorkspaceMember := p07aITUser(t, "fail-foreignws")
	p07aITMember(t, f.foreignWorkspace, foreignWorkspaceMember, "member")
	// A grant that was revoked before the request.
	revoked := p07aITUser(t, "fail-revoked")
	p07aITGrant(t, f.projectID, revoked, "member")
	p07aITExec(t, `DELETE FROM r2d_project_grants WHERE project_id = $1 AND principal_id = $2`, f.projectID, revoked)

	commentID := p07aITComment(t, f.ownerWorkspace, f.issueID)
	commentPath := "/api/comments/" + commentID

	for _, tc := range []struct {
		name  string
		user  string
		token string
	}{
		{"no grant", noGrant, p07aITToken(t, noGrant)},
		{"foreign workspace member", foreignWorkspaceMember, p07aITToken(t, foreignWorkspaceMember)},
		{"revoked grant", revoked, p07aITToken(t, revoked)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p07aITWantStatus(t, p07aITRequest(t, tc.token, http.MethodPut, commentPath, f.ownerWorkspace, map[string]any{"content": "nope"}), http.StatusNotFound)
			p07aITWantStatus(t, p07aITRequest(t, tc.token, http.MethodDelete, commentPath, f.ownerWorkspace, nil), http.StatusNotFound)
		})
	}
}

func TestP07AIntegrationCommentProjectlessStaysWorkspaceScoped(t *testing.T) {
	if testServer == nil || testPool == nil {
		t.Skip("integration server not available")
	}
	f := newP07AITFixture(t)

	foreignMember := p07aITUser(t, "projectless-foreign")
	p07aITGrant(t, f.projectID, foreignMember, "member")

	projectlessComment := p07aITComment(t, f.ownerWorkspace, f.projectlessIssue)
	// A Project grant elsewhere never widens the projectless comment boundary.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, foreignMember), http.MethodPut, "/api/comments/"+projectlessComment, f.ownerWorkspace, map[string]any{"content": "nope"}), http.StatusNotFound)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, foreignMember), http.MethodDelete, "/api/comments/"+projectlessComment, f.ownerWorkspace, nil), http.StatusNotFound)

	// The owning-Workspace member keeps the native path.
	ownerMember := p07aITUser(t, "projectless-owner")
	p07aITMember(t, f.ownerWorkspace, ownerMember, "member")
	ownComment := p07aITComment(t, f.ownerWorkspace, f.projectlessIssue)
	p07aITExec(t, `UPDATE comment SET author_id = $1 WHERE id = $2`, ownerMember, ownComment)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, ownerMember), http.MethodPut, "/api/comments/"+ownComment, f.ownerWorkspace, map[string]any{"content": "owner edit"}), http.StatusOK)
}

func TestP07AIntegrationCommentOwnerWorkspaceSemanticsUnchanged(t *testing.T) {
	if testServer == nil || testPool == nil {
		t.Skip("integration server not available")
	}
	f := newP07AITFixture(t)

	ownerMember := p07aITUser(t, "native-member")
	p07aITMember(t, f.ownerWorkspace, ownerMember, "member")
	ownerAdmin := p07aITUser(t, "native-admin")
	p07aITMember(t, f.ownerWorkspace, ownerAdmin, "admin")

	// A private-Project comment authored by an ordinary Workspace member. The
	// Project ACL alone would deny this member, so the native fallback is what
	// keeps author edits working.
	commentID := p07aITComment(t, f.ownerWorkspace, f.issueID)
	p07aITExec(t, `UPDATE comment SET author_id = $1 WHERE id = $2`, ownerMember, commentID)
	commentPath := "/api/comments/" + commentID

	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, ownerMember), http.MethodPut, commentPath, f.ownerWorkspace, map[string]any{"content": "author edit"}), http.StatusOK)

	// Owner-Workspace admin keeps native moderation over another member's comment.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, ownerAdmin), http.MethodPut, commentPath, f.ownerWorkspace, map[string]any{"content": "admin edit"}), http.StatusOK)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, ownerAdmin), http.MethodDelete, commentPath, f.ownerWorkspace, nil), http.StatusNoContent)
}

func TestP07AIntegrationCommentWorkspaceGrantMembershipRemovalFailsClosed(t *testing.T) {
	if testServer == nil || testPool == nil {
		t.Skip("integration server not available")
	}
	f := newP07AITFixture(t)

	// Grant the Project to the whole foreign Workspace, so access comes only
	// from the caller's member row in that Workspace.
	grantID := uuid.NewString()
	p07aITExec(t, `
INSERT INTO r2d_project_grants (id, project_id, principal_type, principal_id, role, created_by)
VALUES ($1, $2, 'workspace', $3, 'member', $4)`, grantID, f.projectID, f.foreignWorkspace, testUserID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM r2d_project_grants WHERE id = $1`, grantID)
	})

	grantee := p07aITUser(t, "wsgrant-grantee")
	p07aITMember(t, f.foreignWorkspace, grantee, "member")
	granteeToken := p07aITToken(t, grantee)

	// While the membership stands, the workspace grant admits the caller.
	resp := p07aITRequest(t, granteeToken, http.MethodPost, "/api/issues/"+f.issueID+"/comments", f.ownerWorkspace, map[string]any{"content": "workspace grant"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("workspace-grant create: status = %d, want 201", resp.StatusCode)
	}
	commentPath := "/api/comments/" + p07aITCommentID(t, resp)

	// Removing the member row removes the only source of Project access.
	p07aITExec(t, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, f.foreignWorkspace, grantee)
	p07aITWantStatus(t, p07aITRequest(t, granteeToken, http.MethodPut, commentPath, f.ownerWorkspace, map[string]any{"content": "after removal"}), http.StatusNotFound)
	p07aITWantStatus(t, p07aITRequest(t, granteeToken, http.MethodDelete, commentPath, f.ownerWorkspace, nil), http.StatusNotFound)
}

func TestP07AIntegrationCommentIDORNonDisclosure(t *testing.T) {
	if testServer == nil || testPool == nil {
		t.Skip("integration server not available")
	}
	f := newP07AITFixture(t)

	attacker := p07aITUser(t, "idor-attacker")
	token := p07aITToken(t, attacker)
	existing := p07aITComment(t, f.ownerWorkspace, f.issueID)

	// An existing comment the caller cannot reach and a comment id that does
	// not exist must be indistinguishable.
	p07aITWantStatus(t, p07aITRequest(t, token, http.MethodPut, "/api/comments/"+existing, f.ownerWorkspace, map[string]any{"content": "probe"}), http.StatusNotFound)
	p07aITWantStatus(t, p07aITRequest(t, token, http.MethodPut, "/api/comments/00000000-0000-0000-0000-000000000000", f.ownerWorkspace, map[string]any{"content": "probe"}), http.StatusNotFound)
}
