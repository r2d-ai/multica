package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/middleware"
)

// P07-F: MCP inventory is Workspace-owned configuration, not Project content.
//
// A Project grant widens access to the shared Project's issues, comments and
// resources. It must never widen access to the owner Workspace's MCP library
// (shared server URLs and credentials), the agent MCP bindings, or the daemon
// MCP overlay. The R2D scope middleware only carves out `/api/issues/`,
// `/api/projects/` and `/api/attachments/`; every MCP route stays behind the
// ordinary Workspace-membership gate. These tests exercise that real boundary
// so a future route or an over-broad R2D carve-out cannot silently expose the
// inventory to a cross-Workspace collaborator.

// p07fPrincipals builds one collaborator of every Project-grant shape, all of
// whom may read the shared Project in testWorkspaceID but are NOT members of
// testWorkspaceID itself.
func p07fPrincipals(t *testing.T, projectID string) []struct {
	name   string
	userID string
} {
	t.Helper()

	viewer := p07aUser(t, "mcp-viewer")
	p07aGrant(t, projectID, "user", viewer, "viewer")
	member := p07aUser(t, "mcp-member")
	p07aGrant(t, projectID, "user", member, "member")
	manager := p07aUser(t, "mcp-manager")
	p07aGrant(t, projectID, "user", manager, "manager")

	foreignWS := dbfx.Workspace(t, "P07F Foreign", "p07f-foreign-"+uuid.NewString())
	workspaceGrantee := p07aUser(t, "mcp-workspace-grantee")
	dbfx.Member(t, foreignWS, workspaceGrantee, "member")
	p07aGrant(t, projectID, "workspace", foreignWS, "manager")

	observer := p07aUser(t, "mcp-observer")
	p07aObserver(t, observer)

	revoked := p07aUser(t, "mcp-revoked")
	revokedGrant := p07aGrant(t, projectID, "user", revoked, "manager")
	dbfx.Exec(t, `DELETE FROM r2d_project_grants WHERE id = $1`, revokedGrant)

	ungranted := p07aUser(t, "mcp-ungranted")

	return []struct {
		name   string
		userID string
	}{
		{"foreign direct viewer", viewer},
		{"foreign direct member", member},
		{"foreign direct manager", manager},
		{"foreign workspace grantee", workspaceGrantee},
		{"global observer", observer},
		{"revoked grant", revoked},
		{"ungranted foreign user", ungranted},
	}
}

// TestP07FWorkspaceMcpInventoryNotReachableViaProjectGrant pins the read and
// admin-write routes of the Workspace MCP library. A Project grant of any
// strength is still not Workspace membership, and the stored entry is a
// credential: denied responses must not echo the server entry either.
func TestP07FWorkspaceMcpInventoryNotReachableViaProjectGrant(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	serverID := createWorkspaceMcpServerForTest(t, "p07f-"+strings.ToLower(uuid.NewString()[:8]), workspaceMcpTestEntry)
	projectID, _ := p07aProjectWithIssue(t, "private")

	listBoundary := middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id")(http.HandlerFunc(testHandler.ListWorkspaceMcpServers))
	createBoundary := middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin")(http.HandlerFunc(testHandler.CreateWorkspaceMcpServer))
	updateBoundary := middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin")(http.HandlerFunc(testHandler.UpdateWorkspaceMcpServer))
	deleteBoundary := middleware.RequireWorkspaceRoleFromURL(testHandler.Queries, "id", "owner", "admin")(http.HandlerFunc(testHandler.DeleteWorkspaceMcpServer))

	serve := func(boundary http.Handler, userID, method, path string, withBody bool, paramKey, paramValue string) *httptest.ResponseRecorder {
		var body *strings.Reader
		if withBody {
			body = strings.NewReader(`{"name":"p07f-foreign","config":` + workspaceMcpTestEntry + `}`)
		}
		var req *http.Request
		if body != nil {
			req = httptest.NewRequest(method, path, body)
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		req.Header.Set("X-User-ID", userID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		if paramKey != "" {
			req = withURLParam(req, paramKey, paramValue)
		}
		w := httptest.NewRecorder()
		boundary.ServeHTTP(w, req)
		return w
	}

	assertDenied := func(t *testing.T, w *httptest.ResponseRecorder, surface string) {
		t.Helper()
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404 (Workspace membership required); body=%s", surface, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if strings.Contains(body, workspaceMcpTestSecret) || strings.Contains(body, "linear.example") {
			t.Fatalf("%s denied response leaked the Workspace MCP entry: %s", surface, body)
		}
	}

	for _, tc := range p07fPrincipals(t, projectID) {
		t.Run(tc.name, func(t *testing.T) {
			assertDenied(t, serve(listBoundary, tc.userID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/mcp-servers", false, "id", testWorkspaceID), "list")
			assertDenied(t, serve(createBoundary, tc.userID, http.MethodPost, "/api/workspaces/"+testWorkspaceID+"/mcp-servers", true, "id", testWorkspaceID), "create")
			assertDenied(t, serve(updateBoundary, tc.userID, http.MethodPut, "/api/workspaces/"+testWorkspaceID+"/mcp-servers/"+serverID, false, "id", testWorkspaceID), "update")
			assertDenied(t, serve(deleteBoundary, tc.userID, http.MethodDelete, "/api/workspaces/"+testWorkspaceID+"/mcp-servers/"+serverID, false, "id", testWorkspaceID), "delete")
		})
	}

	// Baseline: the owner Workspace member still reaches the library, so the
	// 404s above are the boundary and not a broken route. The payload stays
	// names/transports only — never the stored entry.
	w := serve(listBoundary, testUserID, http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/mcp-servers", false, "id", testWorkspaceID)
	if w.Code != http.StatusOK {
		t.Fatalf("owner member list status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), workspaceMcpTestSecret) || strings.Contains(w.Body.String(), "linear.example") {
		t.Fatalf("owner member list echoed the MCP entry: %s", w.Body.String())
	}
}

// TestP07FAgentMcpBindingsStayWorkspaceBoundary covers the agent-scoped MCP
// routes. A Project manager of a shared Project still cannot enumerate or
// reassign the owner Workspace agent's MCP library bindings.
func TestP07FAgentMcpBindingsStayWorkspaceBoundary(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	agentID := createHandlerTestAgent(t, "p07f-agent", nil)
	projectID, _ := p07aProjectWithIssue(t, "private")
	manager := p07aUser(t, "mcp-agent-manager")
	p07aGrant(t, projectID, "user", manager, "manager")

	boundary := middleware.RequireWorkspaceMember(testHandler.Queries)

	serve := func(userID, method, path string, withBody bool) *httptest.ResponseRecorder {
		var req *http.Request
		if withBody {
			req = httptest.NewRequest(method, path, strings.NewReader(`{"server_id":"`+uuid.NewString()+`"}`))
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		req.Header.Set("X-User-ID", userID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req = withURLParam(req, "id", agentID)
		w := httptest.NewRecorder()
		boundary(http.HandlerFunc(testHandler.ListAgentMcpServers)).ServeHTTP(w, req)
		return w
	}

	if w := serve(manager, http.MethodGet, "/api/agents/"+agentID+"/mcp-servers", false); w.Code != http.StatusNotFound {
		t.Fatalf("foreign project manager list agent MCP bindings status = %d, want 404; body=%s", w.Code, w.Body.String())
	}

	addBoundary := middleware.RequireWorkspaceMember(testHandler.Queries)(http.HandlerFunc(testHandler.AddAgentMcpServer))
	req := httptest.NewRequest(http.MethodPost, "/api/agents/"+agentID+"/mcp-servers", strings.NewReader(`{"server_id":"`+uuid.NewString()+`"}`))
	req.Header.Set("X-User-ID", manager)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	req = withURLParam(req, "id", agentID)
	w := httptest.NewRecorder()
	addBoundary.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign project manager add agent MCP binding status = %d, want 404; body=%s", w.Code, w.Body.String())
	}

	// Baseline: the owner Workspace member reaches the same route.
	if w := serve(testUserID, http.MethodGet, "/api/agents/"+agentID+"/mcp-servers", false); w.Code != http.StatusOK {
		t.Fatalf("owner member list agent MCP bindings status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}
