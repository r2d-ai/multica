package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIsGitHubPRActivityActionable(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		reviewState  string
		path         string
		line         int
		want         bool
	}{
		{name: "changes_requested_always", body: "", reviewState: "changes_requested", want: true},
		{name: "ack_thanks", body: "thanks!", want: false},
		{name: "ack_lgtm", body: "LGTM", want: false},
		{name: "file_line_ref", body: "nit", path: "server/foo.go", line: 12, want: true},
		{name: "empty_body", body: "   ", want: false},
		{name: "concrete_feedback", body: "Please rename GetUser to FetchUser in auth.go", want: true},
		{name: "ci_churn", body: "All checks have passed for this pull request.", want: false},
		{name: "bot_non_ack", body: "Assertion failed: expected 200 got 500 in handler_test.go", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isGitHubPRActivityActionable(tc.body, tc.reviewState, tc.path, tc.line)
			if got != tc.want {
				t.Fatalf("isGitHubPRActivityActionable() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsGitHubAcknowledgement(t *testing.T) {
	if !isGitHubAcknowledgement("ok.") {
		t.Fatal("expected ok acknowledgement")
	}
	if isGitHubAcknowledgement("please fix the race in worker.go") {
		t.Fatal("expected actionable body")
	}
}

func TestResolveLinkedPRContext_Gate(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	secret := "pr-activity-gate-secret"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)

	const installationID int64 = 77665544
	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: installationID,
		AccountLogin:   "activity-gate",
		AccountType:    "User",
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pr_activity WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE workspace_id = $1`, testWorkspaceID)
	})

	// PR exists but no issue link — must be ignored.
	if _, err := testHandler.Queries.UpsertGitHubPullRequest(ctx, db.UpsertGitHubPullRequestParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: installationID,
		RepoOwner:      "acme",
		RepoName:       "gate-repo",
		PrNumber:       99,
		Title:          "unlinked",
		State:          "open",
		HtmlUrl:        "https://github.com/acme/gate-repo/pull/99",
		HeadSha:        "abc",
		PrCreatedAt:    parseGHTimeRequired("2026-06-01T00:00:00Z"),
		PrUpdatedAt:    parseGHTimeRequired("2026-06-01T00:00:00Z"),
	}); err != nil {
		t.Fatalf("UpsertGitHubPullRequest: %v", err)
	}

	body := map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number": 99,
			"pull_request": map[string]any{"url": "https://github.com/acme/gate-repo/pull/99"},
		},
		"comment": map[string]any{
			"id":       5001,
			"body":     "Please fix the nil deref in main.go",
			"html_url": "https://github.com/acme/gate-repo/pull/99#issuecomment-5001",
			"user":     map[string]any{"login": "reviewer", "type": "User"},
		},
		"repository":   map[string]any{"name": "gate-repo", "owner": map[string]any{"login": "acme"}},
		"installation": map[string]any{"id": installationID},
	}
	postGitHubWebhook(t, secret, "issue_comment", body)

	var count int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE content LIKE '%nil deref%'`).Scan(&count); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no mirrored comment for unlinked PR, got %d", count)
	}
}

func TestWebhook_LinkedPRComment_MirrorsAndDedupes(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	secret := "pr-activity-dedupe-secret"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "PR activity mirror test",
		"status": "in_progress",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	json.NewDecoder(w.Body).Decode(&created)

	const installationID int64 = 66554433
	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: installationID,
		AccountLogin:   "activity-dedupe",
		AccountType:    "User",
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pr_activity WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM issue_pull_request WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	// Seed linked PR via pull_request webhook.
	prBody := map[string]any{
		"action": "opened",
		"pull_request": map[string]any{
			"number":     77,
			"html_url":   "https://github.com/acme/mirror/pull/77",
			"title":      "Fix " + created.Identifier,
			"body":       "Follow-up",
			"state":      "open",
			"draft":      false,
			"merged":     false,
			"created_at": "2026-06-01T00:00:00Z",
			"updated_at": "2026-06-01T00:00:00Z",
			"head":       map[string]any{"ref": "fix", "sha": "deadbeef"},
			"user":       map[string]any{"login": "dev", "avatar_url": ""},
		},
		"repository":   map[string]any{"name": "mirror", "owner": map[string]any{"login": "acme"}},
		"installation": map[string]any{"id": installationID},
	}
	postGitHubWebhook(t, secret, "pull_request", prBody)

	commentBody := map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       77,
			"pull_request": map[string]any{"url": "https://github.com/acme/mirror/pull/77"},
		},
		"comment": map[string]any{
			"id":       9001,
			"body":     "Please handle the error path in auth.go",
			"html_url": "https://github.com/acme/mirror/pull/77#issuecomment-9001",
			"user":     map[string]any{"login": "reviewer", "type": "User"},
		},
		"repository":   map[string]any{"name": "mirror", "owner": map[string]any{"login": "acme"}},
		"installation": map[string]any{"id": installationID},
	}
	postGitHubWebhook(t, secret, "issue_comment", commentBody)
	postGitHubWebhook(t, secret, "issue_comment", commentBody)

	var commentCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id = $1`, created.ID).Scan(&commentCount); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if commentCount != 1 {
		t.Fatalf("expected 1 mirrored comment (deduped), got %d", commentCount)
	}

	var activityCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM github_pr_activity WHERE issue_id = $1`, created.ID).Scan(&activityCount); err != nil {
		t.Fatalf("count activity: %v", err)
	}
	if activityCount != 1 {
		t.Fatalf("expected 1 activity row, got %d", activityCount)
	}
}

func TestWebhook_ApprovedReview_MirrorsWithoutDuplicateOnRedelivery(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	ctx := context.Background()
	secret := "pr-activity-approved-secret"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)

	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":  "Approved review mirror",
		"status": "in_progress",
	})
	testHandler.CreateIssue(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("CreateIssue: %d %s", w.Code, w.Body.String())
	}
	var created IssueResponse
	json.NewDecoder(w.Body).Decode(&created)

	const installationID int64 = 55443322
	if _, err := testHandler.Queries.CreateGitHubInstallation(ctx, db.CreateGitHubInstallationParams{
		WorkspaceID:    parseUUID(testWorkspaceID),
		InstallationID: installationID,
		AccountLogin:   "approved-review",
		AccountType:    "User",
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}

	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM github_pr_activity WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM comment WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM issue_pull_request WHERE issue_id = $1`, created.ID)
		testPool.Exec(ctx, `DELETE FROM github_pull_request WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM github_installation WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, created.ID)
	})

	prBody := map[string]any{
		"action": "opened",
		"pull_request": map[string]any{
			"number":     88,
			"html_url":   "https://github.com/acme/approve/pull/88",
			"title":      created.Identifier + ": ship it",
			"body":       "",
			"state":      "open",
			"draft":      false,
			"merged":     false,
			"created_at": "2026-06-01T00:00:00Z",
			"updated_at": "2026-06-01T00:00:00Z",
			"head":       map[string]any{"ref": "ship", "sha": "cafebabe"},
			"user":       map[string]any{"login": "dev", "avatar_url": ""},
		},
		"repository":   map[string]any{"name": "approve", "owner": map[string]any{"login": "acme"}},
		"installation": map[string]any{"id": installationID},
	}
	postGitHubWebhook(t, secret, "pull_request", prBody)

	reviewBody := map[string]any{
		"action": "submitted",
		"review": map[string]any{
			"id":       7001,
			"body":     "",
			"state":    "approved",
			"html_url": "https://github.com/acme/approve/pull/88#pullrequestreview-7001",
			"user":     map[string]any{"login": "lead", "type": "User"},
		},
		"pull_request": map[string]any{"number": 88},
		"repository":   map[string]any{"name": "approve", "owner": map[string]any{"login": "acme"}},
		"installation": map[string]any{"id": installationID},
	}
	postGitHubWebhook(t, secret, "pull_request_review", reviewBody)
	postGitHubWebhook(t, secret, "pull_request_review", reviewBody)

	var commentCount int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM comment WHERE issue_id = $1 AND content LIKE '%approved%'`, created.ID).Scan(&commentCount); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if commentCount != 1 {
		t.Fatalf("expected 1 approved review mirror, got %d", commentCount)
	}
}

func postGitHubWebhook(t *testing.T, secret, event string, body map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal webhook body: %v", err)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", bytes.NewReader(raw))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", sig)
	testHandler.HandleGitHubWebhook(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("webhook %s: expected 202, got %d (%s)", event, w.Code, w.Body.String())
	}
}
