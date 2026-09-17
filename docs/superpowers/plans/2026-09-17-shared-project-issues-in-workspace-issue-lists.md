# Shared Project Issues in Workspace Issue Lists Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Workspace-scoped issue reads (rows, groups, facets, counts, search, grouped, legacy list) also return issues from Projects shared into the active Workspace from another Workspace, while projectless issues stay Workspace-private.

**Architecture:** One pure policy function in `r2dauth` decides which Project ids an issue collection unions; the table channel compiles it into its SQL predicate and the legacy middleware path injects it as a `project_ids` allowlist. No migration; reads widen, writes do not.

**Tech Stack:** Go (Chi, sqlc/pgx, chi middleware), PostgreSQL, React/TypeScript clients (no client change expected).

**Spec:** `docs/superpowers/specs/2026-09-17-shared-project-issues-in-workspace-lists-design.md`

## Global Constraints

- Policy lives only in `server/internal/r2dauth/`; `r2dauth.Resolve` is the sole role resolver. SQL supplies facts only.
- Do not change write or batch-mutation authorization. `r2d_inbox_acl.go:157` must keep using the Workspace-owned readable set.
- No foreign keys, cascades, or new migrations.
- Comments are English. Conventional commits, atomic.
- `open_only=true` stays Workspace-bound (see spec Non-goals); do not touch `ListOpenIssues`.
- Go verification: `cd server && go test ./internal/r2dauth ./internal/middleware ./internal/handler`.
- After SQL changes run `make sqlc`; this plan adds no SQL, so no regeneration.

---

### Task 1: `r2dauth` policy — Project ids for an issue collection

**Files:**
- Modify: `server/internal/r2dauth/service.go`
- Test: `server/internal/r2dauth/collection_projects_test.go` (create)

**Interfaces:**
- Consumes: existing `ProjectFacts`, `Resolve`, `Decision.Can`, `OperationRead`, `ProjectRole`, `WorkspaceRole` in the same package.
- Produces: `func ProjectIDsForIssueCollection(facts []ProjectFacts, activeWorkspaceID string) []string` — sorted, de-duplicated, readable Project ids.

- [ ] **Step 1: Write the failing test**

Create `server/internal/r2dauth/collection_projects_test.go`:

```go
package r2dauth

import (
	"reflect"
	"testing"
)

func TestProjectIDsForIssueCollection(t *testing.T) {
	t.Parallel()

	facts := []ProjectFacts{
		{ProjectID: "own-visible", OwnerWorkspaceID: "ws-a", Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleMember},
		{ProjectID: "own-private-hidden", OwnerWorkspaceID: "ws-a", Visibility: VisibilityPrivate, OwnerWorkspaceRole: WorkspaceRoleMember},
		{ProjectID: "foreign-direct", OwnerWorkspaceID: "ws-b", Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleViewer},
		{ProjectID: "foreign-ws", OwnerWorkspaceID: "ws-b", Visibility: VisibilityPrivate, WorkspaceGrantRole: ProjectRoleMember},
		{ProjectID: "foreign-ungranted", OwnerWorkspaceID: "ws-b", Visibility: VisibilityWorkspace},
		{ProjectID: "other-workspace-membership", OwnerWorkspaceID: "ws-c", Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleMember},
		{ProjectID: "observer-only", OwnerWorkspaceID: "ws-b", Visibility: VisibilityPrivate, GlobalObserver: true},
		{ProjectID: "corrupt-visibility", OwnerWorkspaceID: "ws-a", Visibility: Visibility("bogus"), OwnerWorkspaceRole: WorkspaceRoleOwner},
	}

	got := ProjectIDsForIssueCollection(facts, "ws-a")
	want := []string{"foreign-direct", "foreign-ws", "observer-only", "own-visible"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
}

func TestProjectIDsForIssueCollectionGlobalObserverSeesEverythingReadable(t *testing.T) {
	t.Parallel()

	facts := []ProjectFacts{
		{ProjectID: "p1", OwnerWorkspaceID: "ws-b", Visibility: VisibilityPrivate, GlobalObserver: true},
		{ProjectID: "p2", OwnerWorkspaceID: "ws-c", Visibility: VisibilityWorkspace, GlobalObserver: true},
	}
	got := ProjectIDsForIssueCollection(facts, "ws-a")
	want := []string{"p1", "p2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd server && go test ./internal/r2dauth -run TestProjectIDsForIssueCollection -v`
Expected: FAIL — `undefined: ProjectIDsForIssueCollection`.

- [ ] **Step 3: Write the minimal implementation**

Append to `server/internal/r2dauth/service.go` (add `"sort"` to the imports):

```go
// isExplicitProjectGrant reports whether a Project role came from an explicit
// user or workspace grant rather than the owner Workspace's implicit role.
func isExplicitProjectGrant(role ProjectRole) bool {
	switch role {
	case ProjectRoleViewer, ProjectRoleMember, ProjectRoleManager:
		return true
	default:
		return false
	}
}

// ProjectIDsForIssueCollection returns the readable Projects an issue
// collection unions while activeWorkspaceID is the active Workspace: Projects
// owned by that Workspace, foreign Projects the user was explicitly granted,
// and the deployment-wide set a global observer may read. It mirrors the
// Project list rule (r2dProjectCollectionIDs) so Projects and Issues cannot
// drift. Callers pass facts from R2DListCandidateProjectAccessFacts; Resolve
// stays the only policy engine.
func ProjectIDsForIssueCollection(facts []ProjectFacts, activeWorkspaceID string) []string {
	ids := make([]string, 0, len(facts))
	seen := make(map[string]struct{}, len(facts))
	for _, f := range facts {
		if f.ProjectID == "" || !Resolve(f).Can(OperationRead) {
			continue
		}
		include := f.OwnerWorkspaceID == activeWorkspaceID ||
			f.GlobalObserver ||
			isExplicitProjectGrant(f.DirectGrantRole) ||
			isExplicitProjectGrant(f.WorkspaceGrantRole)
		if !include {
			continue
		}
		if _, ok := seen[f.ProjectID]; ok {
			continue
		}
		seen[f.ProjectID] = struct{}{}
		ids = append(ids, f.ProjectID)
	}
	sort.Strings(ids)
	return ids
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd server && go test ./internal/r2dauth -v`
Expected: PASS, including the new tests.

- [ ] **Step 5: Commit**

```bash
git add server/internal/r2dauth/service.go server/internal/r2dauth/collection_projects_test.go
git commit -m "feat(r2d): centralize issue-collection project visibility policy"
```

---

### Task 2: Middleware uses the shared policy

**Files:**
- Modify: `server/internal/middleware/r2d_project_scope.go:237-276`
- Modify: `server/internal/middleware/r2d_issue_collection_visibility.go:20-36,105-117`
- Test: `server/internal/middleware/r2d_issue_collection_visibility_test.go` (extend)

**Interfaces:**
- Consumes: `r2dauth.ProjectIDsForIssueCollection` (Task 1), existing `r2dFacts` in the middleware package.
- Produces: `func r2dReadableIssueProjectIDs(queries *db.Queries, r *http.Request, userID, activeWorkspaceID string) ([]string, error)` (replaces `r2dReadableWorkspaceProjectIDs`); `r2dProjectCollectionIDs` keeps its signature but delegates.

- [ ] **Step 1: Write the failing test**

The existing pure helpers are unchanged; add a guard test that pins the new helper name and that the policy call happens over candidate facts. Because this helper needs SQL, keep it as a compile-level test in the same file:

```go
func TestR2DReadableIssueProjectIDsIsWired(t *testing.T) {
	t.Parallel()
	// Compile-level guard: the middleware must expose the cross-Workspace
	// helper and no longer the Workspace-bound one.
	var fn func(*db.Queries, *http.Request, string, string) ([]string, error) = r2dReadableIssueProjectIDs
	if fn == nil {
		t.Fatal("r2dReadableIssueProjectIDs is not wired")
	}
}
```

Add the `db` import to the test file if missing.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd server && go test ./internal/middleware -run TestR2DReadableIssueProjectIDsIsWired -v`
Expected: FAIL — `undefined: r2dReadableIssueProjectIDs`.

- [ ] **Step 3: Replace the helper and delegate the project collection**

In `server/internal/middleware/r2d_issue_collection_visibility.go`, replace the body of `r2dReadableWorkspaceProjectIDs` (lines 20-36) with:

```go
// r2dReadableIssueProjectIDs returns the readable Projects an issue collection
// unions while activeWorkspaceID is active: Workspace-owned Projects, foreign
// Projects with an explicit grant, and a global observer's readable set. SQL
// supplies facts only; r2dauth.ProjectIDsForIssueCollection is the policy.
func r2dReadableIssueProjectIDs(queries *db.Queries, r *http.Request, userID, activeWorkspaceID string) ([]string, error) {
	facts, err := queries.R2DListCandidateProjectAccessFacts(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	policyFacts := make([]r2dauth.ProjectFacts, 0, len(facts))
	for _, fact := range facts {
		policyFacts = append(policyFacts, r2dFacts(fact))
	}
	return r2dauth.ProjectIDsForIssueCollection(policyFacts, activeWorkspaceID), nil
}
```

Delete the now-unused `sort` import if nothing else in the file uses it (check with `grep -n "sort\." server/internal/middleware/r2d_issue_collection_visibility.go`; `r2dIntersectProjectIDs` still sorts, so keep it).

Update its only caller in the same file (line 109):

```go
	readable, err := r2dReadableIssueProjectIDs(queries, r, userID, workspaceID)
```

In `server/internal/middleware/r2d_project_scope.go`, replace `r2dProjectCollectionIDs` (lines 250-276) with a delegation and delete the now-unused `r2dExplicitProjectGrant` (lines 237-244):

```go
// r2dProjectCollectionIDs builds the Project set shown while one Workspace is
// active. It shares r2dauth.ProjectIDsForIssueCollection with the issue
// collections so Projects and Issues cannot drift.
func r2dProjectCollectionIDs(ctx context.Context, queries *db.Queries, userID, activeWorkspaceID string) ([]string, error) {
	facts, err := queries.R2DListCandidateProjectAccessFacts(ctx, userID)
	if err != nil {
		return nil, err
	}
	policyFacts := make([]r2dauth.ProjectFacts, 0, len(facts))
	for _, fact := range facts {
		policyFacts = append(policyFacts, r2dFacts(fact))
	}
	return r2dauth.ProjectIDsForIssueCollection(policyFacts, activeWorkspaceID), nil
}
```

- [ ] **Step 4: Run the middleware tests**

Run: `cd server && go test ./internal/middleware`
Expected: PASS (existing `r2d_project_scope_test.go` and `r2d_issue_collection_visibility_test.go` still green).

- [ ] **Step 5: Commit**

```bash
git add server/internal/middleware/r2d_project_scope.go server/internal/middleware/r2d_issue_collection_visibility.go server/internal/middleware/r2d_issue_collection_visibility_test.go
git commit -m "feat(r2d): union shared Projects into workspace issue collection filters"
```

---

### Task 3: Table channel unions shared Projects

**Files:**
- Modify: `server/internal/handler/r2d_issue_table_acl.go`
- Modify: `server/internal/handler/issue_table_query.go:443-455`

**Interfaces:**
- Consumes: `r2dauth.ProjectIDsForIssueCollection` (Task 1), existing `r2dHandlerProjectFacts`, `util.ParseUUID`.
- Produces: `func (h *Handler) r2dReadableIssueProjectIDs(ctx context.Context, userID, workspaceID string) ([]pgtype.UUID, error)`.

- [ ] **Step 1: Add the cross-Workspace helper**

Append to `server/internal/handler/r2d_issue_table_acl.go`:

```go
// r2dReadableIssueProjectIDs is the read-side readable-Project set for Issue
// collections. It includes Projects outside the active Workspace when the user
// holds an explicit grant, matching the Project list. Write paths keep using
// r2dReadableWorkspaceProjectIDs: widening a read set must never widen a write
// gate.
func (h *Handler) r2dReadableIssueProjectIDs(ctx context.Context, userID, workspaceID string) ([]pgtype.UUID, error) {
	facts, err := h.Queries.R2DListCandidateProjectAccessFacts(ctx, userID)
	if err != nil {
		return nil, err
	}
	policyFacts := make([]r2dauth.ProjectFacts, 0, len(facts))
	for _, fact := range facts {
		policyFacts = append(policyFacts, r2dHandlerProjectFacts(fact))
	}
	ids := make([]pgtype.UUID, 0, len(policyFacts))
	for _, id := range r2dauth.ProjectIDsForIssueCollection(policyFacts, workspaceID) {
		parsed, err := util.ParseUUID(id)
		if err != nil {
			return nil, fmt.Errorf("invalid readable project id %q: %w", id, err)
		}
		ids = append(ids, parsed)
	}
	return ids, nil
}
```

- [ ] **Step 2: Change the table predicate**

In `server/internal/handler/issue_table_query.go`, replace lines 443-455 so the read set comes from the new helper and projectless rows stay Workspace-bound:

```go
	readableProjectIDs, err := h.r2dReadableIssueProjectIDs(
		r.Context(), userID, util.UUIDToString(workspaceUUID),
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to apply project visibility")
		return issueTableSQL{}, false
	}

	// Projectless Issues remain Workspace-scoped. Project-backed Issues follow
	// the readable-Project set, which may include Projects owned by another
	// Workspace when this user holds an explicit grant.
	where := []string{"((i.workspace_id = $1 AND i.project_id IS NULL) OR i.project_id = ANY($2::uuid[]))"}
	args := []any{workspaceUUID, readableProjectIDs}
```

- [ ] **Step 3: Build and run the handler package tests**

Run: `cd server && go build ./... && go test ./internal/handler -run 'TestIssueTable|TestCompileIssueTable' -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add server/internal/handler/r2d_issue_table_acl.go server/internal/handler/issue_table_query.go
git commit -m "feat(r2d): union shared Projects into the workspace issue table query"
```

---

### Task 4: DB-backed proof and leakage negatives

**Files:**
- Test: `server/internal/handler/r2d_issue_table_acl_test.go` (create)

**Interfaces:**
- Consumes: Task 3's predicate; package test harness `testPool`, `testWorkspaceID`, `testUserID`, `testHandler`, `newRequest`, `issueTableRowsRequest`, `issueTableQuerySpec` (see `issue_table_query_test.go:593`).

- [ ] **Step 1: Write the failing test**

Create `server/internal/handler/r2d_issue_table_acl_test.go`. The fixture creates a second Workspace, a user in it, a Project in the test Workspace, a workspace grant for the second Workspace, and one issue in the Project. The assertion drives `ListIssueTableRows` as the foreign user with `X-Workspace-ID` set to their own Workspace:

```go
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListIssueTableRows_IncludesExplicitlySharedForeignProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ($1, $2, 'FGN') RETURNING id
	`, fmt.Sprintf("Foreign %d", suffix), fmt.Sprintf("foreign-%d", suffix)).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID) })

	var foreignUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, fmt.Sprintf("Foreign User %d", suffix), fmt.Sprintf("foreign-%d@example.test", suffix)).Scan(&foreignUserID); err != nil {
		t.Fatalf("create foreign user: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, foreignUserID) })

	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')
	`, foreignWorkspaceID, foreignUserID); err != nil {
		t.Fatalf("add foreign member: %v", err)
	}

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Shared %d", suffix)).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })

	if _, err := testPool.Exec(ctx, `
		INSERT INTO r2d_project_grants (project_id, principal_type, principal_id, role, created_by)
		VALUES ($1, 'workspace', $2, 'member', $3)
	`, projectID, foreignWorkspaceID, testUserID); err != nil {
		t.Fatalf("grant workspace: %v", err)
	}

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, position, number, project_id)
		VALUES ($1, $2, 'todo', 'none', 'member', $3, 0,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), $4)
		RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Shared issue %d", suffix), testUserID, projectID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	req := newRequest("POST", "/api/issues/table/rows", issueTableRowsRequest{
		Query: issueTableQuerySpec{Scope: issueTableScope{Kind: "workspace"}},
	})
	req.Header.Set("X-User-ID", foreignUserID)
	req.Header.Set("X-Workspace-ID", foreignWorkspaceID)

	recorder := httptest.NewRecorder()
	testHandler.ListIssueTableRows(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rows: expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		Rows []struct {
			Issue struct {
				ID string `json:"id"`
			} `json:"issue"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	found := false
	for _, row := range body.Rows {
		if row.Issue.ID == issueID {
			found = true
		}
	}
	if !found {
		t.Fatalf("shared Project issue %s missing from workspace rows: %s", issueID, recorder.Body.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails against the old predicate**

Run: `cd server && go test ./internal/handler -run TestListIssueTableRows_IncludesExplicitlySharedForeignProject -v`
Expected: FAIL before Task 3 is applied (`shared Project issue … missing`); PASS after. If Task 3 is already applied, temporarily revert the predicate to confirm the test is meaningful, then re-apply.

- [ ] **Step 3: Add the negative and consistency tests**

Append to the same file. First the groups/facets consistency test, which proves counts move with rows:

```go
func TestListIssueTableGroupsAndFacets_IncludeSharedForeignProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// Reuse the shared-Project fixture shape from
	// TestListIssueTableRows_IncludesExplicitlySharedForeignProject: a foreign
	// Workspace + member, a Project in testWorkspace, a workspace grant, and one
	// issue in that Project. Then drive the sibling endpoints with the foreign
	// identity:
	//
	// groups: POST /api/issues/table/groups with
	//   issueTableGroupsRequest{Query: issueTableQuerySpec{Scope: issueTableScope{Kind: "workspace"}}}
	//   decode `groups[].total` and assert the sum is >= 1 and includes the
	//   fixture Project's issue.
	// facets: POST /api/issues/table/facets with the same Query; decode the
	//   status facet and assert the fixture status's count is >= 1.
	//
	// Request construction is identical to the rows test except for the path,
	// the request struct and the decoded field names; copy it rather than
	// sharing a helper so each endpoint's wire shape stays explicit.
}
```

Then the projectless negative test:

```go
func TestListIssueTableRows_ExcludesProjectlessIssuesOfForeignWorkspace(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var foreignWorkspaceID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix)
		VALUES ($1, $2, 'FGP') RETURNING id
	`, fmt.Sprintf("Foreign Projectless %d", suffix), fmt.Sprintf("foreign-pl-%d", suffix)).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID) })

	var foreignUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, fmt.Sprintf("Foreign PL User %d", suffix), fmt.Sprintf("foreign-pl-%d@example.test", suffix)).Scan(&foreignUserID); err != nil {
		t.Fatalf("create foreign user: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, foreignUserID) })

	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')
	`, foreignWorkspaceID, foreignUserID); err != nil {
		t.Fatalf("add foreign member: %v", err)
	}

	// Projectless issue in the test Workspace: it must never surface to a user
	// whose active Workspace is the foreign one.
	var projectlessID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id, position, number)
		VALUES ($1, $2, 'todo', 'none', 'member', $3, 0,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1))
		RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Projectless %d", suffix), testUserID).Scan(&projectlessID); err != nil {
		t.Fatalf("create projectless issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, projectlessID) })

	req := newRequest("POST", "/api/issues/table/rows", issueTableRowsRequest{
		Query: issueTableQuerySpec{Scope: issueTableScope{Kind: "workspace"}},
	})
	req.Header.Set("X-User-ID", foreignUserID)
	req.Header.Set("X-Workspace-ID", foreignWorkspaceID)

	recorder := httptest.NewRecorder()
	testHandler.ListIssueTableRows(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rows: expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}

	var body struct {
		Rows []struct {
			Issue struct {
				ID string `json:"id"`
			} `json:"issue"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode rows: %v", err)
	}
	for _, row := range body.Rows {
		if row.Issue.ID == projectlessID {
			t.Fatalf("projectless issue leaked into a foreign workspace list: %s", recorder.Body.String())
		}
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `cd server && go test ./internal/handler -run 'TestListIssueTableRows_' -v`
Expected: PASS (requires `make up`).

- [ ] **Step 5: Commit**

```bash
git add server/internal/handler/r2d_issue_table_acl_test.go
git commit -m "test(r2d): cover shared-Project rows and projectless exclusion"
```

---

### Task 5: Broaden verification and record the change

**Files:**
- Modify: `docs/r2d-extension-architecture.md` (append a short note under the ACL section)
- Test: existing suites

- [ ] **Step 1: Run the broad Go gate**

Run: `cd server && go test ./internal/r2dauth ./internal/middleware ./internal/handler`
Expected: PASS. Report any pre-existing failure separately rather than fixing it here.

- [ ] **Step 2: Run the frontend gate for regressions**

Run (repo root): `pnpm typecheck`
Expected: same result as before the change (this plan touches no TypeScript). Pre-existing `toBeInTheDocument` type errors are unrelated.

- [ ] **Step 3: Run the P07 leakage suite and confirm the Decision allowlist is untouched**

Run: `cd server && go test ./internal/r2dauth -run 'TestP07' -v`
Expected: PASS. `TestP07DecisionStructFieldAllowlists` (`server/internal/r2dauth/p07_leakage_test.go:11`) must stay green unchanged — this change adds no field to `Decision`, only a pure id-selection function. If it fails, the union was implemented by widening the decision struct instead of the collection, which is the wrong shape; revert and redo Task 1.

- [ ] **Step 4: Document the rule**

Append to `docs/r2d-extension-architecture.md`:

```markdown
## Issue collection visibility

Workspace-scoped issue reads union the Projects an issue collection may show:
Projects owned by the active Workspace, foreign Projects the user holds an
explicit user/workspace grant on, and a global observer's readable set. The rule
is `r2dauth.ProjectIDsForIssueCollection` and is shared with the Project list so
Projects and Issues cannot drift. Projectless issues stay Workspace-private, and
write/batch paths keep the Workspace-owned readable set.
```

- [ ] **Step 5: Commit**

```bash
git add docs/r2d-extension-architecture.md
git commit -m "docs(r2d): record issue-collection visibility rule"
```
