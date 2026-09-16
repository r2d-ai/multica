package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// P07-A end-to-end leakage matrix: the middleware Project-scope override and
// the handler ACL run together behind real JWTs. These are the cases the
// handler-level tests cannot reach because the Workspace membership gate lives
// in the middleware chain.

type p07aITFixture struct {
	ownerWorkspace   string
	foreignWorkspace string
	projectID        string
	issueID          string
	attachmentID     string
	projectlessIssue string
	projectlessAtt   string
}

func p07aITExec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec: %v\nSQL: %s", err, sql)
	}
}

func p07aITWorkspace(t *testing.T, label string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `
INSERT INTO workspace (name, slug, description, issue_prefix)
VALUES ($1, $2, '', 'P7A')
RETURNING id`, label, "p07a-"+label+"-"+uuid.NewString()).Scan(&id); err != nil {
		t.Fatalf("insert workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, id)
	})
	return id
}

func p07aITUser(t *testing.T, label string) string {
	t.Helper()
	email := "p07a-it-" + label + "-" + uuid.NewString() + "@multica.ai"
	var id string
	if err := testPool.QueryRow(context.Background(), `
INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`, "P07A "+label, email).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, id)
	})
	return id
}

func p07aITIssue(t *testing.T, workspaceID, projectID string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `
INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, position, number, project_id)
VALUES ($1, 'p07a issue', 'todo', 'none', 'member', $2, 0,
        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), NULLIF($3, '')::uuid)
RETURNING id`, workspaceID, testUserID, projectID).Scan(&id); err != nil {
		t.Fatalf("insert issue: %v", err)
	}
	return id
}

func p07aITComment(t *testing.T, workspaceID, issueID string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `
INSERT INTO comment (issue_id, workspace_id, author_type, author_id, content, type)
VALUES ($1, $2, 'member', $3, 'p07a comment', 'comment')
RETURNING id`, issueID, workspaceID, testUserID).Scan(&id); err != nil {
		t.Fatalf("insert comment: %v", err)
	}
	return id
}

func p07aITAttachment(t *testing.T, workspaceID, issueID string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `
INSERT INTO attachment (workspace_id, issue_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
VALUES ($1, $2, 'member', $3, 'p07a.txt', 'https://example.test/p07a.txt', 'text/plain', 1)
RETURNING id`, workspaceID, issueID, testUserID).Scan(&id); err != nil {
		t.Fatalf("insert attachment: %v", err)
	}
	return id
}

func p07aITGrant(t *testing.T, projectID, userID, role string) {
	t.Helper()
	id := uuid.NewString()
	p07aITExec(t, `
INSERT INTO r2d_project_grants (id, project_id, principal_type, principal_id, role, created_by)
VALUES ($1, $2, 'user', $3, $4, $5)`, id, projectID, userID, role, testUserID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM r2d_project_grants WHERE id = $1`, id)
	})
}

func p07aITObserver(t *testing.T, userID string) {
	t.Helper()
	p07aITExec(t, `
INSERT INTO r2d_global_roles (user_id, role, created_by) VALUES ($1, 'global_observer', $2)`, userID, testUserID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM r2d_global_roles WHERE user_id = $1`, userID)
	})
}

func newP07AITFixture(t *testing.T) *p07aITFixture {
	t.Helper()
	f := &p07aITFixture{}
	f.ownerWorkspace = p07aITWorkspace(t, "owner")
	f.foreignWorkspace = p07aITWorkspace(t, "foreign")

	var projectID string
	if err := testPool.QueryRow(context.Background(), `
INSERT INTO project (workspace_id, title, status, priority)
VALUES ($1, 'P07A shared project', 'planned', 'none') RETURNING id`, f.ownerWorkspace).Scan(&projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	p07aITExec(t, `INSERT INTO r2d_project_extra (project_id, visibility) VALUES ($1, 'private')`, projectID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM r2d_project_grants WHERE project_id = $1`, projectID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM r2d_project_extra WHERE project_id = $1`, projectID)
	})
	f.projectID = projectID
	f.issueID = p07aITIssue(t, f.ownerWorkspace, projectID)
	f.attachmentID = p07aITAttachment(t, f.ownerWorkspace, f.issueID)

	f.projectlessIssue = p07aITIssue(t, f.ownerWorkspace, "")
	f.projectlessAtt = p07aITAttachment(t, f.ownerWorkspace, f.projectlessIssue)
	return f
}

func p07aITRequest(t *testing.T, token, method, path, workspaceID string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, testServer.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Workspace-ID", workspaceID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, path, err)
	}
	return resp
}

func p07aITToken(t *testing.T, userID string) string {
	t.Helper()
	token, err := generateTestJWT(userID, userID+"@p07a.test", "P07A User")
	if err != nil {
		t.Fatalf("generate jwt: %v", err)
	}
	return token
}

func p07aITWantStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d; body=%s", resp.StatusCode, want, body)
	}
}

func TestP07AIntegrationAttachmentByIDRoute(t *testing.T) {
	if testServer == nil || testPool == nil {
		t.Skip("integration server not available")
	}
	f := newP07AITFixture(t)

	viewer := p07aITUser(t, "viewer")
	p07aITGrant(t, f.projectID, viewer, "viewer")
	revoked := p07aITUser(t, "revoked")
	foreignMember := p07aITUser(t, "foreign-member")
	p07aITExec(t, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`, f.foreignWorkspace, foreignMember)

	// Foreign project collaborator reaches a shared project's attachment by id.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, viewer), http.MethodGet, "/api/attachments/"+f.attachmentID, f.ownerWorkspace, nil), http.StatusOK)

	// No grant, and a foreign Workspace member with no grant: 404, not 403.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, revoked), http.MethodGet, "/api/attachments/"+f.attachmentID, f.ownerWorkspace, nil), http.StatusNotFound)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, foreignMember), http.MethodGet, "/api/attachments/"+f.attachmentID, f.ownerWorkspace, nil), http.StatusNotFound)

	// Projectless attachment: a Project grant never reaches it.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, viewer), http.MethodGet, "/api/attachments/"+f.projectlessAtt, f.ownerWorkspace, nil), http.StatusNotFound)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, foreignMember), http.MethodGet, "/api/attachments/"+f.projectlessAtt, f.ownerWorkspace, nil), http.StatusNotFound)
}

func TestP07AIntegrationCommentRoute(t *testing.T) {
	if testServer == nil || testPool == nil {
		t.Skip("integration server not available")
	}
	f := newP07AITFixture(t)

	viewer := p07aITUser(t, "comment-viewer")
	p07aITGrant(t, f.projectID, viewer, "viewer")
	member := p07aITUser(t, "comment-member")
	p07aITGrant(t, f.projectID, member, "member")
	ungranted := p07aITUser(t, "comment-ungranted")

	commentsPath := "/api/issues/" + f.issueID + "/comments"

	// Read: a granted viewer lists comments on the shared project's issue.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, viewer), http.MethodGet, commentsPath, f.ownerWorkspace, nil), http.StatusOK)

	// The fold/summary projections are the same authorized read, so they must
	// not become a bypass for a caller with no grant.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, viewer), http.MethodGet, commentsPath+"?fold=true&summary=true", f.ownerWorkspace, nil), http.StatusOK)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, ungranted), http.MethodGet, commentsPath+"?fold=true&summary=true", f.ownerWorkspace, nil), http.StatusNotFound)

	// Write requires contribute: a viewer is denied, a member is admitted.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, viewer), http.MethodPost, commentsPath, f.ownerWorkspace, map[string]any{"content": "viewer write"}), http.StatusForbidden)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, member), http.MethodPost, commentsPath, f.ownerWorkspace, map[string]any{"content": "member write"}), http.StatusCreated)

	// No grant: the shared project's comments are a non-disclosing 404.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, ungranted), http.MethodGet, commentsPath, f.ownerWorkspace, nil), http.StatusNotFound)

	// Comment-by-id mutation is still Workspace-member scoped, so a foreign
	// collaborator cannot reach it — fail closed, not a bypass.
	commentID := p07aITComment(t, f.ownerWorkspace, f.issueID)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, member), http.MethodDelete, "/api/comments/"+commentID, f.ownerWorkspace, nil), http.StatusNotFound)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, viewer), http.MethodDelete, "/api/comments/"+commentID, f.ownerWorkspace, nil), http.StatusNotFound)

	// Projectless issue: a Project grant does not widen the Workspace boundary.
	projectlessPath := "/api/issues/" + f.projectlessIssue + "/comments"
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, member), http.MethodGet, projectlessPath, f.ownerWorkspace, nil), http.StatusNotFound)
}

func TestP07AIntegrationGlobalObserverReadsSharedProject(t *testing.T) {
	if testServer == nil || testPool == nil {
		t.Skip("integration server not available")
	}
	f := newP07AITFixture(t)

	observer := p07aITUser(t, "observer")
	p07aITObserver(t, observer)

	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, observer), http.MethodGet, "/api/attachments/"+f.attachmentID, f.ownerWorkspace, nil), http.StatusOK)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, observer), http.MethodGet, "/api/issues/"+f.issueID+"/comments", f.ownerWorkspace, nil), http.StatusOK)

	// The observer is read-only and does not inherit Workspace-private files.
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, observer), http.MethodGet, "/api/attachments/"+f.projectlessAtt, f.ownerWorkspace, nil), http.StatusNotFound)
	p07aITWantStatus(t, p07aITRequest(t, p07aITToken(t, observer), http.MethodDelete, "/api/attachments/"+f.attachmentID, f.ownerWorkspace, nil), http.StatusNotFound)
}
