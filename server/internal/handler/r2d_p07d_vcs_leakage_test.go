package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// P07-D keeps provider connections at the Workspace boundary. A Project grant
// is deliberately not a substitute for membership in the owning Workspace.
func TestP07DVCSConnectionsStayWorkspaceScoped(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	box := withVCSBox(t)
	connID := seedVCSConnection(t, context.Background(), box, "forgejo", "https://p07d-forgejo.test")
	t.Cleanup(func() { cleanupVCS(context.Background(), "") })

	projectID, _ := p07aProjectWithIssue(t, "private")
	foreignWS := dbfx.Workspace(t, "P07D Foreign", "p07d-foreign-"+uuid.NewString())

	type principal struct {
		name   string
		userID string
		want   int
	}
	principals := []principal{{name: "owner workspace member", userID: testUserID, want: http.StatusOK}}
	for _, role := range []string{"viewer", "member", "manager"} {
		userID := p07aUser(t, "vcs-"+role)
		dbfx.Member(t, foreignWS, userID, "member")
		p07aGrant(t, projectID, "user", userID, role)
		principals = append(principals, principal{name: "foreign " + role, userID: userID, want: http.StatusNotFound})
	}

	router := chi.NewRouter()
	router.Route("/api/workspaces/{id}", func(r chi.Router) {
		r.Use(middleware.RequireWorkspaceMemberFromURL(testHandler.Queries, "id"))
		r.Get("/vcs/connections", testHandler.ListVCSConnections)
	})

	for _, tc := range principals {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/workspaces/"+testWorkspaceID+"/vcs/connections", nil)
			req.Header.Set("X-User-ID", tc.userID)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
			body := w.Body.String()
			if tc.want == http.StatusOK && !strings.Contains(body, connID) {
				t.Fatalf("owner member response omitted connection %s: %s", connID, body)
			}
			for _, secret := range []string{"vcs-webhook-secret", "access_token_encrypted", "webhook_secret_encrypted"} {
				if strings.Contains(body, secret) {
					t.Fatalf("response leaked VCS secret material %q: %s", secret, body)
				}
			}
		})
	}
}

func TestP07DProjectRepoURLRequiresOwnerWorkspaceMembership(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	const repoURL = "https://github.com/example/p07d-shared-project"
	projectID, _ := p07aProjectWithIssue(t, "private")
	dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id": projectID, "workspace_id": testWorkspaceID,
		"resource_type": "github_repo", "resource_ref": `{"url":"` + repoURL + `"}`, "position": 0,
	})
	foreignWS := dbfx.Workspace(t, "P07D Resource Foreign", "p07d-resource-"+uuid.NewString())

	router := chi.NewRouter()
	router.With(middleware.RequireWorkspaceMember(testHandler.Queries)).Get("/api/projects/{id}/resources", testHandler.ListProjectResources)

	tests := []struct {
		name   string
		userID string
		want   int
	}{
		{name: "owner workspace member", userID: testUserID, want: http.StatusOK},
	}
	for _, role := range []string{"viewer", "member", "manager"} {
		userID := p07aUser(t, "repo-"+role)
		dbfx.Member(t, foreignWS, userID, "member")
		p07aGrant(t, projectID, "user", userID, role)
		tests = append(tests, struct {
			name   string
			userID string
			want   int
		}{name: "foreign " + role, userID: userID, want: http.StatusNotFound})
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/projects/"+projectID+"/resources", nil)
			req.Header.Set("X-User-ID", tc.userID)
			req.Header.Set("X-Workspace-ID", testWorkspaceID)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
			if tc.want == http.StatusOK && !strings.Contains(w.Body.String(), repoURL) {
				t.Fatalf("owner member response omitted repo URL: %s", w.Body.String())
			}
			if tc.want != http.StatusOK && strings.Contains(w.Body.String(), repoURL) {
				t.Fatalf("denied response leaked repo URL: %s", w.Body.String())
			}
		})
	}
}

func TestP07DVCSWebhookEventUsesConnectionMetadataAllowlist(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	box := withVCSBox(t)
	connID := seedVCSConnection(t, ctx, box, "forgejo", "https://p07d-event-forgejo.test")
	projectID, _ := p07aProjectWithIssue(t, "private")
	issue := newVCSIssue(t, "P07D shared VCS issue")
	dbfx.Exec(t, `UPDATE issue SET project_id = $1 WHERE id = $2`, projectID, issue.ID)
	t.Cleanup(func() { cleanupVCS(ctx, issue.ID) })

	var got events.Event
	testHandler.Bus.Subscribe(protocol.EventPullRequestUpdated, func(event events.Event) { got = event })
	raw, err := json.Marshal(map[string]any{
		"action": "opened",
		"pull_request": map[string]any{
			"number": 71, "html_url": "https://p07d-event-forgejo.test/acme/widget/pulls/71",
			"title": issue.Identifier + ": event allowlist", "body": "", "state": "open", "merged": false,
			"created_at": "2026-09-16T00:00:00Z", "updated_at": "2026-09-16T00:00:00Z",
			"head": map[string]any{"ref": "feature/p07d", "sha": "p07dsha"},
			"user": map[string]any{"login": "alice", "avatar_url": "https://example.test/alice.png"},
		},
		"repository": map[string]any{"name": "widget", "owner": map[string]any{"login": "acme"}},
	})
	if err != nil {
		t.Fatalf("marshal webhook: %v", err)
	}
	w := httptest.NewRecorder()
	testHandler.HandleVCSWebhook(w, vcsWebhookReq(connID, map[string]string{
		"X-Forgejo-Event":     "pull_request",
		"X-Forgejo-Signature": giteaSig(raw),
	}, raw))
	if w.Code != http.StatusAccepted {
		t.Fatalf("webhook status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if got.Type != protocol.EventPullRequestUpdated {
		t.Fatalf("missing pull-request event: %#v", got)
	}

	payload, err := json.Marshal(got.Payload)
	if err != nil {
		t.Fatalf("marshal event payload: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("decode event payload: %v", err)
	}
	for key := range fields {
		if key != "pull_request" && key != "linked_issue_ids" {
			t.Fatalf("unexpected top-level event field %q: %s", key, payload)
		}
	}
	text := string(payload)
	for _, forbidden := range []string{
		"connection_id", "instance_url", "account_login", "access_token", "webhook_secret",
		connID, "p07d-event-forgejo.test", vcsTestSecret,
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("event payload leaked VCS connection metadata %q: %s", forbidden, text)
		}
	}
}
