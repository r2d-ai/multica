package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// P07-A leakage matrix for comments & attachments. Every case asserts the
// non-disclosing 404-on-deny shape so the attachment routes cannot be used as
// an IDOR oracle, and the read cases assert that no owner-Workspace inventory
// (repo URLs, daemon ids, VCS ids, secrets) crosses the Project boundary.

func p07aUser(t *testing.T, label string) string {
	t.Helper()
	return dbfx.User(t, "P07A "+label, "p07a-"+strings.ToLower(label)+"-"+uuid.NewString()+"@multica.ai")
}

func p07aProjectWithIssue(t *testing.T, visibility string) (projectID, issueID string) {
	t.Helper()
	projectID = dbfx.Insert(t, "project", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"title":        "P07A project " + uuid.NewString(),
		"status":       "planned",
		"priority":     "none",
	})
	dbfx.InsertNoID(t, "r2d_project_extra", testutil.Cols{
		"project_id": projectID,
		"visibility": visibility,
	}, "project_id = $1", projectID)
	issueID = dbfx.Issue(t, "P07A issue", testutil.Cols{"project_id": projectID})
	return projectID, issueID
}

func p07aGrant(t *testing.T, projectID, principalType, principalID, role string) string {
	t.Helper()
	return dbfx.Insert(t, "r2d_project_grants", testutil.Cols{
		"id":             uuid.NewString(),
		"project_id":     projectID,
		"principal_type": principalType,
		"principal_id":   principalID,
		"role":           role,
		"created_by":     testUserID,
	})
}

func p07aObserver(t *testing.T, userID string) {
	t.Helper()
	dbfx.InsertNoID(t, "r2d_global_roles", testutil.Cols{
		"user_id":    userID,
		"role":       "global_observer",
		"created_by": testUserID,
	}, "user_id = $1 AND role = 'global_observer'", userID)
}

// p07aAttachment binds an attachment to an Issue in the test Workspace. The
// Issue carries the Project binding that decides the attachment's ACL.
func p07aAttachment(t *testing.T, issueID string) string {
	t.Helper()
	return dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  testWorkspaceID,
		"issue_id":      issueID,
		"uploader_type": "member",
		"uploader_id":   testUserID,
		"filename":      "p07a.txt",
		"url":           "https://example.test/p07a.txt",
		"content_type":  "text/plain",
		"size_bytes":    1,
	})
}

func p07aSeedStoredAttachment(t *testing.T, store *mockStorage, issueID, key, filename, contentType string, body []byte) string {
	t.Helper()
	url, err := store.Upload(context.Background(), key, body, contentType, filename)
	if err != nil {
		t.Fatalf("seed Upload: %v", err)
	}
	id := dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id":  testWorkspaceID,
		"issue_id":      issueID,
		"uploader_type": "member",
		"uploader_id":   testUserID,
		"filename":      filename,
		"url":           url,
		"content_type":  contentType,
		"size_bytes":    len(body),
	})
	return id
}

func p07aRequest(t *testing.T, method, path, userID, workspaceID, paramKey, paramValue string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	return withURLParam(req, paramKey, paramValue)
}

func TestP07AAttachmentByIDProjectReadMatrix(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID, issueID := p07aProjectWithIssue(t, "private")
	attachmentID := p07aAttachment(t, issueID)

	foreignWS := dbfx.Workspace(t, "P07A Foreign", "p07a-foreign-"+uuid.NewString())
	viewer := p07aUser(t, "viewer")
	p07aGrant(t, projectID, "user", viewer, "viewer")
	member := p07aUser(t, "member")
	p07aGrant(t, projectID, "user", member, "member")
	manager := p07aUser(t, "manager")
	p07aGrant(t, projectID, "user", manager, "manager")
	revoked := p07aUser(t, "revoked") // grant created then removed below
	revokedGrant := p07aGrant(t, projectID, "user", revoked, "viewer")
	dbfx.Exec(t, `DELETE FROM r2d_project_grants WHERE id = $1`, revokedGrant)
	observer := p07aUser(t, "observer")
	p07aObserver(t, observer)
	foreignMember := p07aUser(t, "foreign-member")
	dbfx.Member(t, foreignWS, foreignMember, "member")

	cases := []struct {
		name   string
		userID string
		want   int
	}{
		{"owner workspace member", testUserID, http.StatusOK},
		{"foreign viewer", viewer, http.StatusOK},
		{"foreign member", member, http.StatusOK},
		{"foreign manager", manager, http.StatusOK},
		{"global observer", observer, http.StatusOK},
		{"foreign workspace member without grant", foreignMember, http.StatusNotFound},
		{"revoked grant", revoked, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := p07aRequest(t, http.MethodGet, "/api/attachments/"+attachmentID, tc.userID, testWorkspaceID, "id", attachmentID)
			testHandler.GetAttachmentByID(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestP07AAttachmentByIDProjectlessDeniesForeign(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectlessIssue := dbfx.Issue(t, "P07A projectless issue")
	attachmentID := p07aAttachment(t, projectlessIssue)

	// A Project grant elsewhere must not widen a collection of projectless
	// files, whatever the granted role is.
	otherProjectID, _ := p07aProjectWithIssue(t, "private")
	manager := p07aUser(t, "other-project-manager")
	p07aGrant(t, otherProjectID, "user", manager, "manager")
	observer := p07aUser(t, "projectless-observer")
	p07aObserver(t, observer)

	for _, tc := range []struct {
		name   string
		userID string
		want   int
	}{
		{"owner workspace member", testUserID, http.StatusOK},
		{"foreign project manager", manager, http.StatusNotFound},
		{"global observer", observer, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := p07aRequest(t, http.MethodGet, "/api/attachments/"+attachmentID, tc.userID, testWorkspaceID, "id", attachmentID)
			testHandler.GetAttachmentByID(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestP07AAttachmentResponseHasNoOwnerWorkspaceInventory(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID, issueID := p07aProjectWithIssue(t, "private")
	attachmentID := p07aAttachment(t, issueID)
	viewer := p07aUser(t, "inventory-viewer")
	p07aGrant(t, projectID, "user", viewer, "viewer")

	w := httptest.NewRecorder()
	req := p07aRequest(t, http.MethodGet, "/api/attachments/"+attachmentID, viewer, testWorkspaceID, "id", attachmentID)
	testHandler.GetAttachmentByID(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for key := range payload {
		lower := strings.ToLower(key)
		for _, forbidden := range []string{
			"repo", "daemon", "vcs", "secret", "credential", "mcp",
			"runtime_config", "custom_env", "local_path", "work_dir",
		} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("attachment response exposes owner-Workspace field %q", key)
			}
		}
	}
}

func TestP07AAttachmentDownloadProjectACL(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	store := &mockStorage{}
	origStorage := testHandler.Storage
	testHandler.Storage = store
	defer func() { testHandler.Storage = origStorage }()

	projectID, issueID := p07aProjectWithIssue(t, "private")
	attachmentID := p07aSeedStoredAttachment(t, store, issueID, "p07a/download.txt", "download.txt", "text/plain", []byte("payload"))
	viewer := p07aUser(t, "download-viewer")
	p07aGrant(t, projectID, "user", viewer, "viewer")
	revoked := p07aUser(t, "download-revoked")

	w := httptest.NewRecorder()
	req := p07aRequest(t, http.MethodGet, "/api/attachments/"+attachmentID+"/download", viewer, testWorkspaceID, "id", attachmentID)
	testHandler.DownloadAttachment(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("authorized download status=%d, want 302 signed redirect; body=%s", w.Code, w.Body.String())
	}
	if location := w.Header().Get("Location"); !strings.Contains(location, "p07a/download.txt") {
		t.Fatalf("authorized download Location = %q, want a signed URL for the attachment key", location)
	}

	w = httptest.NewRecorder()
	req = p07aRequest(t, http.MethodGet, "/api/attachments/"+attachmentID+"/download", revoked, testWorkspaceID, "id", attachmentID)
	testHandler.DownloadAttachment(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("revoked download status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestP07AAttachmentDeleteRequiresProjectManage(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	projectID, issueID := p07aProjectWithIssue(t, "private")

	manager := p07aUser(t, "delete-manager")
	p07aGrant(t, projectID, "user", manager, "manager")
	managerAttachment := p07aAttachment(t, issueID)
	w := httptest.NewRecorder()
	req := p07aRequest(t, http.MethodDelete, "/api/attachments/"+managerAttachment, manager, testWorkspaceID, "id", managerAttachment)
	testHandler.DeleteAttachment(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("project manager delete status = %d, want 204; body=%s", w.Code, w.Body.String())
	}

	viewer := p07aUser(t, "delete-viewer")
	p07aGrant(t, projectID, "user", viewer, "viewer")
	viewerAttachment := p07aAttachment(t, issueID)
	w = httptest.NewRecorder()
	req = p07aRequest(t, http.MethodDelete, "/api/attachments/"+viewerAttachment, viewer, testWorkspaceID, "id", viewerAttachment)
	testHandler.DeleteAttachment(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("project viewer delete status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}
