# Cross-Workspace Assignment on Shared Projects Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A user with a Project grant can assign work on that Project to themselves or to another user who also holds a grant, the picker offers exactly those people, and their real name renders instead of `Unknown`.

**Architecture:** An ACL-native roster is served per Project and shared with the write gate; the assignee write gate moves from "must be a Workspace member" to "must be assignable on this Project"; issue payloads carry the assignee's display name so the client stops depending on the active Workspace's member list.

**Tech Stack:** Go (Chi, sqlc/pgx), PostgreSQL, React/TypeScript (`packages/core`, `packages/views`), Vitest.

**Spec:** `docs/superpowers/specs/2026-09-17-cross-workspace-assignment-design.md`

## Global Constraints

- Policy lives only in `server/internal/r2dauth/`; `r2dauth.Resolve` is the sole role resolver.
- Assigning owner-Workspace Agents or Squads to a foreign collaborator stays forbidden (P06 boundary).
- `attachment_ids`, `label_ids`, `origin_type`, `origin_id` stay forbidden for non-members.
- Projectless issues keep the Workspace boundary: no foreign assignee.
- `assignee_id` for `assignee_type="member"` is a **user id**, not a `member` row id.
- Raw SQL for R2D lives in `server/pkg/db/generated/r2d_acl_ext.go` (the established extension seam); no new sqlc query files, no migration.
- Comments are English. Conventional commits, atomic.
- Go gate: `cd server && go test ./internal/handler ./internal/middleware ./internal/r2dauth`.
- Frontend gate: `pnpm typecheck`, `pnpm lint`, `pnpm test` (repo root, excludes mobile).

---

### Task 1: ACL-native assignable-member queries

**Files:**
- Modify: `server/pkg/db/generated/r2d_acl_ext.go`

**Interfaces:**
- Produces:
  - `type R2DPrincipal struct { ID, Name, Email, AvatarURL string }`
  - `func (q *Queries) R2DListAssignableMembers(ctx context.Context, projectID string) ([]R2DPrincipal, error)`
  - `func (q *Queries) R2DIsAssignableMember(ctx context.Context, projectID, userID string) (bool, error)`

- [ ] **Step 1: Add the struct and queries**

Append to `server/pkg/db/generated/r2d_acl_ext.go`:

```go
// R2DPrincipal is one assignable person on a Project. ID is a user id for
// member-type principals, matching issue.assignee_id.
type R2DPrincipal struct {
	ID        string
	Name      string
	Email     string
	AvatarURL string
}

const r2dAssignableMembersSQL = `
SELECT DISTINCT u.id::text, u.name, COALESCE(u.email, ''), COALESCE(u.avatar_url, '')
FROM "user" u
JOIN member m ON m.user_id = u.id
WHERE m.workspace_id = (SELECT p.workspace_id FROM project p WHERE p.id = $1::uuid)
   OR m.workspace_id::text IN (
        SELECT g.principal_id
        FROM r2d_project_grants g
        WHERE g.project_id = $1::text
          AND g.principal_type = 'workspace'
   )
   OR u.id::text IN (
        SELECT g.principal_id
        FROM r2d_project_grants g
        WHERE g.project_id = $1::text
          AND g.principal_type = 'user'
   )
ORDER BY u.name`

// R2DListAssignableMembers returns every user who may be assigned on the
// Project: owner-Workspace members, direct grantees, and members of granted
// Workspaces. SQL supplies candidates; the caller must already hold
// OperationContribute on the Project.
func (q *Queries) R2DListAssignableMembers(ctx context.Context, projectID string) ([]R2DPrincipal, error) {
	rows, err := q.db.Query(ctx, r2dAssignableMembersSQL, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]R2DPrincipal, 0)
	for rows.Next() {
		var p R2DPrincipal
		if err := rows.Scan(&p.ID, &p.Name, &p.Email, &p.AvatarURL); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// R2DIsAssignableMember is the single-target form used by the write gate so a
// mutation does not enumerate the whole roster.
func (q *Queries) R2DIsAssignableMember(ctx context.Context, projectID, userID string) (bool, error) {
	var allowed bool
	err := q.db.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM member m
    WHERE m.user_id = $2::uuid
      AND (
            m.workspace_id = (SELECT p.workspace_id FROM project p WHERE p.id = $1::uuid)
         OR m.workspace_id::text IN (
                SELECT g.principal_id FROM r2d_project_grants g
                WHERE g.project_id = $1::text AND g.principal_type = 'workspace'
            )
      )
) OR EXISTS (
    SELECT 1 FROM r2d_project_grants g
    WHERE g.project_id = $1::text
      AND g.principal_type = 'user'
      AND g.principal_id = $2::text
)`, projectID, userID).Scan(&allowed)
	return allowed, err
}
```

- [ ] **Step 2: Build**

Run: `cd server && go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add server/pkg/db/generated/r2d_acl_ext.go
git commit -m "feat(r2d): add ACL-native assignable-member queries"
```

---

### Task 2: Roster endpoint

**Files:**
- Modify: `server/internal/r2dsharing/service.go`
- Modify: `server/internal/r2dsharing/postgres.go`
- Create: `server/internal/handler/r2d_assignable_actors.go`
- Modify: `server/cmd/server/r2d_routes.go`
- Test: `server/internal/handler/r2d_assignable_actors_test.go` (create)

**Interfaces:**
- Consumes: `R2DListAssignableMembers` (Task 1); existing `r2dauth.Service.Capabilities`, `projectSharingRequestContext`, `writeProjectSharingError`.
- Produces:
  - `func (s *Service) AssignableActors(ctx context.Context, userID, projectID string, principalType PrincipalType, query string, limit int) ([]Principal, error)`
  - `func (h *Handler) GetProjectAssignableActors(w http.ResponseWriter, r *http.Request)`
  - route `GET /api/projects/{id}/assignable-actors`

- [ ] **Step 1: Write the failing test**

Create `server/internal/handler/r2d_assignable_actors_test.go` (DB-backed; mirrors the fixture style of Task 4 in the companion plan):

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

// A foreign Workspace grant recipient is offered the owner Workspace's members
// and their own Workspace's members, but never the owner Workspace's Agents.
func TestGetProjectAssignableActors_MemberRosterRespectsProjectACL(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Actors %d", suffix)).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })

	var foreignWorkspaceID, foreignUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix) VALUES ($1, $2, 'ACT') RETURNING id
	`, fmt.Sprintf("Actors WS %d", suffix), fmt.Sprintf("actors-%d", suffix)).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID) })
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, fmt.Sprintf("Granted %d", suffix), fmt.Sprintf("granted-%d@example.test", suffix)).Scan(&foreignUserID); err != nil {
		t.Fatalf("create foreign user: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, foreignUserID) })
	if _, err := testPool.Exec(ctx, `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')
	`, foreignWorkspaceID, foreignUserID); err != nil {
		t.Fatalf("add foreign member: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO r2d_project_grants (project_id, principal_type, principal_id, role, created_by)
		VALUES ($1, 'workspace', $2, 'member', $3)
	`, projectID, foreignWorkspaceID, testUserID); err != nil {
		t.Fatalf("grant workspace: %v", err)
	}

	req := newRequest("GET", "/api/projects/"+projectID+"/assignable-actors?type=member", nil)
	req = withURLParam(req, "id", projectID)
	req.Header.Set("X-User-ID", foreignUserID)
	req.Header.Set("X-Workspace-ID", foreignWorkspaceID)

	recorder := httptest.NewRecorder()
	testHandler.GetProjectAssignableActors(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("actors: expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Actors []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"actors"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode actors: %v", err)
	}
	foundSelf := false
	for _, actor := range body.Actors {
		if actor.Type != "member" {
			t.Fatalf("unexpected actor type %q", actor.Type)
		}
		if actor.ID == foreignUserID {
			foundSelf = true
		}
	}
	if !foundSelf {
		t.Fatalf("granted member %s missing from roster: %s", foreignUserID, recorder.Body.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd server && go test ./internal/handler -run TestGetProjectAssignableActors_MemberRosterRespectsProjectACL -v`
Expected: FAIL — `testHandler.GetProjectAssignableActors` undefined.

- [ ] **Step 3: Implement the store method**

Add to `server/internal/r2dsharing/postgres.go`:

```go
func (s *PostgresStore) ListAssignableMembers(ctx context.Context, projectID string) ([]Principal, error) {
	rows, err := s.db.Query(ctx, r2dAssignableMembersSQL, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Principal, 0)
	for rows.Next() {
		var p Principal
		p.Type = PrincipalUser
		var email string
		if err := rows.Scan(&p.ID, &p.Name, &email, &p.AvatarURL); err != nil {
			return nil, err
		}
		p.Secondary = email
		out = append(out, p)
	}
	return out, rows.Err()
}
```

`r2dsharing` cannot import the `pkg/db/generated` extension method (different DB interface, and `pkg/db/generated` must not import `internal/`), so the SQL text is duplicated: `pkg/db/generated/r2d_acl_ext.go` keeps the copy used by middleware, and `r2dsharing/postgres.go` declares its own. Both carry a comment naming the other file, and the texts must stay byte-identical. Do not try to share the constant across those packages.

Add `ListAssignableMembers(ctx context.Context, projectID string) ([]Principal, error)` to the `Store` interface in `service.go`.

- [ ] **Step 4: Implement the service method**

Add to `server/internal/r2dsharing/service.go`:

```go
// AssignableActors returns the people who may be assigned on the Project.
// Callers need OperationContribute. Agents and Squads belong to the owner
// Workspace's inventory, so they are returned only to a human member of that
// Workspace; a foreign collaborator receives members only.
func (s *Service) AssignableActors(ctx context.Context, userID, projectID string, principalType PrincipalType, query string, limit int) ([]Principal, error) {
	allowed, err := s.authz.Can(ctx, userID, projectID, r2dauth.OperationContribute)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrForbidden
	}
	if principalType != PrincipalUser {
		// Agent/Squad roster is served by the owner-Workspace path only and is
		// out of scope for this endpoint; a foreign caller never reaches it.
		return []Principal{}, nil
	}
	members, err := s.store.ListAssignableMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return filterPrincipalsByQuery(members, query, limit), nil
}
```

Implement `filterPrincipalsByQuery` in the same file (case-insensitive substring on `Name`/`Secondary`, bounded by `limit`), and reuse it from `SearchDirectory` if that keeps the two consistent — do not change `SearchDirectory`'s behavior.

- [ ] **Step 5: Implement the handler and route**

Create `server/internal/handler/r2d_assignable_actors.go`:

```go
package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/r2dsharing"
)

type assignableActorsResponse struct {
	Actors []r2dsharing.Principal `json:"actors"`
}

// GetProjectAssignableActors is the assignee roster for one Project. It is
// contributor-readable, unlike the manager-only sharing directory.
func (h *Handler) GetProjectAssignableActors(w http.ResponseWriter, r *http.Request) {
	userID, projectID, ok := projectSharingRequestContext(w, r)
	if !ok {
		return
	}
	principalType := r2dsharing.PrincipalType(strings.TrimSpace(r.URL.Query().Get("type")))
	if principalType == "" {
		principalType = r2dsharing.PrincipalUser
	}
	if !r2dsharing.ValidPrincipalType(principalType) {
		writeError(w, http.StatusBadRequest, "invalid type")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 50 {
		limit = 50
	}
	actors, err := h.projectSharingService().AssignableActors(
		r.Context(), userID, projectID, principalType, r.URL.Query().Get("q"), limit,
	)
	if err != nil {
		writeProjectSharingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, assignableActorsResponse{Actors: actors})
}
```

Export `ValidPrincipalType` in `r2dsharing` by renaming the existing unexported `validPrincipalType` (update its two call sites).

Add the route in `server/cmd/server/r2d_routes.go` inside the sharing route block:

```go
		r.Get("/assignable-actors", h.GetProjectAssignableActors)
```

- [ ] **Step 6: Run the tests**

Run: `cd server && go test ./internal/handler -run TestGetProjectAssignableActors -v`
Expected: PASS (requires `make up`).

- [ ] **Step 7: Add the Agent/Squad boundary test**

Append to `server/internal/handler/r2d_assignable_actors_test.go`. A foreign caller asking for `type=agent` must receive an empty list, not an error and not the owner Workspace's inventory:

```go
func TestGetProjectAssignableActors_ForeignCallerGetsNoAgents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	// Fixture: same shape as the member test — Project in testWorkspace, foreign
	// Workspace + member, workspace grant. Reuse it verbatim, then:
	req := newRequest("GET", "/api/projects/"+projectID+"/assignable-actors?type=agent", nil)
	req = withURLParam(req, "id", projectID)
	req.Header.Set("X-User-ID", foreignUserID)
	req.Header.Set("X-Workspace-ID", foreignWorkspaceID)

	recorder := httptest.NewRecorder()
	testHandler.GetProjectAssignableActors(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("actors: expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Actors []json.RawMessage `json:"actors"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode actors: %v", err)
	}
	if len(body.Actors) != 0 {
		t.Fatalf("foreign caller received %d agent actors: %s", len(body.Actors), recorder.Body.String())
	}
}
```

Copy the fixture block from `TestGetProjectAssignableActors_MemberRosterRespectsProjectACL` rather than extracting a helper, so each test states its own preconditions.

- [ ] **Step 8: Run the tests**

Run: `cd server && go test ./internal/handler -run TestGetProjectAssignableActors -v`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add server/internal/r2dsharing server/internal/handler/r2d_assignable_actors.go server/internal/handler/r2d_assignable_actors_test.go server/cmd/server/r2d_routes.go
git commit -m "feat(r2d): add project assignable-actors roster endpoint"
```

---

### Task 3: Write gate allows assigning grant holders

**Files:**
- Modify: `server/internal/middleware/r2d_issue_scope.go:165-190,300-330,490-520`
- Test: `server/internal/middleware/r2d_issue_scope_test.go` (extend)

**Interfaces:**
- Consumes: `Queries.R2DIsAssignableMember` (Task 1); existing `r2dRequireProjectOperation`, `r2dRawUUID`, `r2dReadJSONFields`.
- Produces: `func r2dAssigneeFieldsAllowed(queries *db.Queries, r *http.Request, fields map[string]json.RawMessage, projectID, userID string) (bool, bool, error)` returning `(allowed, handled, err)` — `handled=true` means a 403 was already written.

- [ ] **Step 1: Write the failing test**

Extend `server/internal/middleware/r2d_issue_scope_test.go` with a table test over `r2dAssigneeFieldsAllowed` using a fake `*db.Queries` is not possible (concrete type), so assert the pure decision helper instead. Add a pure helper and test it:

```go
func TestR2DAssigneeFieldDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fields     map[string]json.RawMessage
		assignable bool
		wantOK     bool
	}{
		{"unrelated field", map[string]json.RawMessage{"title": json.RawMessage(`"x"`)}, false, true},
		{"assignee null clears", map[string]json.RawMessage{"assignee_type": json.RawMessage(`null`), "assignee_id": json.RawMessage(`null`)}, false, true},
		{"member assignee allowed when assignable", map[string]json.RawMessage{"assignee_type": json.RawMessage(`"member"`), "assignee_id": json.RawMessage(`"u1"`)}, true, true},
		{"member assignee rejected when not assignable", map[string]json.RawMessage{"assignee_type": json.RawMessage(`"member"`), "assignee_id": json.RawMessage(`"u1"`)}, false, false},
		{"agent assignee always rejected", map[string]json.RawMessage{"assignee_type": json.RawMessage(`"agent"`), "assignee_id": json.RawMessage(`"a1"`)}, true, false},
		{"squad assignee always rejected", map[string]json.RawMessage{"assignee_type": json.RawMessage(`"squad"`), "assignee_id": json.RawMessage(`"s1"`)}, true, false},
		{"attachments always rejected", map[string]json.RawMessage{"attachment_ids": json.RawMessage(`["f1"]`)}, true, false},
	}

	for _, tt := range tests {
		got, _ := r2dAssigneeFieldDecision(tt.fields, tt.assignable)
		if got != tt.wantOK {
			t.Errorf("%s: allowed=%v want %v", tt.name, got, tt.wantOK)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd server/internal/middleware && go test -run TestR2DAssigneeFieldDecision -v`
Expected: FAIL — `undefined: r2dAssigneeFieldDecision`.

- [ ] **Step 3: Implement the decision helper**

Add to `server/internal/middleware/r2d_issue_scope.go`:

```go
// r2dAssigneeFieldDecision reports whether a non-member may submit these
// fields. A member-type assignee is allowed only when the target user is
// assignable on the Project; Agent/Squad inventory and attachment/label/origin
// fields stay Workspace-member-only. The second return value reports whether
// the caller must be answered 403.
func r2dAssigneeFieldDecision(fields map[string]json.RawMessage, assignable bool) (bool, bool) {
	rawType, hasType := fields["assignee_type"]
	_, hasID := fields["assignee_id"]
	if !hasType && !hasID {
		// fall through to the other restricted fields below
	} else if (!hasType || r2dRawNull(rawType)) && !hasID {
		// clearing the assignee is allowed
	} else {
		var assigneeType string
		if hasType && !r2dRawNull(rawType) {
			if err := json.Unmarshal(rawType, &assigneeType); err != nil {
				return false, true
			}
		}
		if assigneeType != "member" || !assignable {
			return false, true
		}
	}
	for _, key := range []string{"attachment_ids", "label_ids", "origin_type", "origin_id"} {
		if raw, ok := fields[key]; ok && !r2dRawNull(raw) {
			return false, true
		}
	}
	return true, false
}
```

- [ ] **Step 4: Wire the three non-member guards**

In each of the three non-member blocks (`:185-190`, `:318-326`, `:505-513`), replace the loop over `[]string{"assignee_type", "assignee_id", ...}` with a call that resolves `assignable` first. For the create path (`:185-190`), the Project is `projectID` and the target user is the submitted `assignee_id`:

```go
	if !isMember {
		assignable := false
		if rawAssignee, ok := fields["assignee_id"]; ok && !r2dRawNull(rawAssignee) {
			if assigneeID, err := r2dRawUUID(rawAssignee); err == nil {
				assignable, err = queries.R2DIsAssignableMember(r.Context(), projectID, assigneeID)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "failed to authorize assignee")
					return true
				}
			}
		}
		if _, forbidden := r2dAssigneeFieldDecision(fields, assignable); forbidden {
			writeError(w, http.StatusForbidden, "project access does not grant workspace resource access")
			return true
		}
	}
```

For the direct-mutation path (`:318-326`) the Project is `target.ProjectID`; for the batch path (`:505-513`) use the single resolved `effectiveProjectID` and treat a multi-project batch (`len(projectIDs) != 1`) as `assignable = false`. Apply the same `R2DIsAssignableMember` lookup before calling `r2dAssigneeFieldDecision`.

- [ ] **Step 5: Run the middleware tests**

Run: `cd server && go test ./internal/middleware`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add server/internal/middleware/r2d_issue_scope.go server/internal/middleware/r2d_issue_scope_test.go
git commit -m "feat(r2d): allow assigning grant holders on shared Projects"
```

---

### Task 4: Upstream assignee validation seam

**Files:**
- Modify: `server/internal/handler/issue.go:2974,3639,3800-3821,4388`
- Test: `server/internal/handler/r2d_assignee_seam_test.go` (create)

**Interfaces:**
- Consumes: `Queries.R2DIsAssignableMember` (Task 1).
- Produces: `func (h *Handler) r2dMemberAssignable(ctx context.Context, workspaceID, projectID, userID string) bool` and a new `projectID string` parameter on `validateAssigneePair`.

- [ ] **Step 1: Write the failing test**

Create `server/internal/handler/r2d_assignee_seam_test.go`:

```go
package handler

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestValidateAssigneePair_AcceptsGrantedForeignMember(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Seam %d", suffix)).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })

	var foreignUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, fmt.Sprintf("Seam User %d", suffix), fmt.Sprintf("seam-%d@example.test", suffix)).Scan(&foreignUserID); err != nil {
		t.Fatalf("create foreign user: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, foreignUserID) })

	if _, err := testPool.Exec(ctx, `
		INSERT INTO r2d_project_grants (project_id, principal_type, principal_id, role, created_by)
		VALUES ($1, 'user', $2, 'member', $3)
	`, projectID, foreignUserID, testUserID); err != nil {
		t.Fatalf("grant user: %v", err)
	}

	assigneeID, err := util.ParseUUID(foreignUserID)
	if err != nil {
		t.Fatalf("parse assignee: %v", err)
	}
	status, msg := testHandler.validateAssigneePair(
		ctx, newRequest("POST", "/api/issues", nil), testWorkspaceID, projectID,
		pgtype.Text{String: "member", Valid: true}, assigneeID,
	)
	if status != 0 {
		t.Fatalf("granted foreign member rejected: %d %s", status, msg)
	}
}
```

Import `github.com/jackc/pgx/v5/pgtype` and `github.com/multica-ai/multica/server/internal/util` as needed.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd server && go test ./internal/handler -run TestValidateAssigneePair_AcceptsGrantedForeignMember -v`
Expected: FAIL — too many arguments in call to `validateAssigneePair` (signature not yet changed).

- [ ] **Step 3: Add the seam and thread the Project id**

In `server/internal/handler/issue.go`, change the signature and member branch:

```go
func (h *Handler) validateAssigneePair(ctx context.Context, r *http.Request, workspaceID, projectID string, assigneeType pgtype.Text, assigneeID pgtype.UUID) (int, string) {
	// ... unchanged guards ...
	switch assigneeType.String {
	case "member":
		if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
			UserID:      assigneeID,
			WorkspaceID: wsUUID,
		}); err != nil {
			if projectID != "" && h.r2dMemberAssignable(ctx, workspaceID, projectID, util.UUIDToString(assigneeID)) {
				return 0, ""
			}
			return http.StatusBadRequest, "assignee_id does not refer to a member of this workspace"
		}
		return 0, ""
```

Add the helper in `server/internal/handler/r2d_assignable_actors.go`:

```go
// r2dMemberAssignable is the defense-in-depth copy of the middleware rule: a
// Project grant may assign a user who is assignable on that Project, even when
// the user is not a member of the Project's Workspace.
func (h *Handler) r2dMemberAssignable(ctx context.Context, workspaceID, projectID, userID string) bool {
	if workspaceID == "" || projectID == "" || userID == "" {
		return false
	}
	allowed, err := h.Queries.R2DIsAssignableMember(ctx, projectID, userID)
	if err != nil {
		return false
	}
	return allowed
}
```

Update the three call sites to pass the effective Project id:

- create (`:2974`): the request's resolved `projectID` (empty when projectless);
- direct update (`:3639`): the issue's current `project_id` when the update does not change it, otherwise the destination `project_id`;
- batch (`:4388`): only pass a Project id when the batch resolves to a single Project, else `""`.

- [ ] **Step 4: Run the test and the package**

Run: `cd server && go test ./internal/handler -run TestValidateAssigneePair -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 5: Add the rejection and revocation tests**

Append to `server/internal/handler/r2d_assignee_seam_test.go`:

```go
func TestValidateAssigneePair_RejectsUngrantedForeignMember(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Seam Reject %d", suffix)).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })

	var strangerID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, fmt.Sprintf("Stranger %d", suffix), fmt.Sprintf("stranger-%d@example.test", suffix)).Scan(&strangerID); err != nil {
		t.Fatalf("create stranger: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, strangerID) })

	assigneeID, err := util.ParseUUID(strangerID)
	if err != nil {
		t.Fatalf("parse assignee: %v", err)
	}
	status, _ := testHandler.validateAssigneePair(
		ctx, newRequest("POST", "/api/issues", nil), testWorkspaceID, projectID,
		pgtype.Text{String: "member", Valid: true}, assigneeID,
	)
	if status != http.StatusBadRequest {
		t.Fatalf("ungranted foreign member: status=%d want %d", status, http.StatusBadRequest)
	}
}

func TestValidateAssigneePair_RejectsAfterGrantRevoked(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Seam Revoke %d", suffix)).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })

	var granteeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, fmt.Sprintf("Revoked %d", suffix), fmt.Sprintf("revoked-%d@example.test", suffix)).Scan(&granteeID); err != nil {
		t.Fatalf("create grantee: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, granteeID) })

	if _, err := testPool.Exec(ctx, `
		INSERT INTO r2d_project_grants (project_id, principal_type, principal_id, role, created_by)
		VALUES ($1, 'user', $2, 'member', $3)
	`, projectID, granteeID, testUserID); err != nil {
		t.Fatalf("grant user: %v", err)
	}

	assigneeID, err := util.ParseUUID(granteeID)
	if err != nil {
		t.Fatalf("parse assignee: %v", err)
	}
	before, _ := testHandler.validateAssigneePair(
		ctx, newRequest("POST", "/api/issues", nil), testWorkspaceID, projectID,
		pgtype.Text{String: "member", Valid: true}, assigneeID,
	)
	if before != 0 {
		t.Fatalf("granted member rejected before revocation: %d", before)
	}

	if _, err := testPool.Exec(ctx, `
		DELETE FROM r2d_project_grants
		WHERE project_id = $1 AND principal_type = 'user' AND principal_id = $2
	`, projectID, granteeID); err != nil {
		t.Fatalf("revoke grant: %v", err)
	}

	after, _ := testHandler.validateAssigneePair(
		ctx, newRequest("POST", "/api/issues", nil), testWorkspaceID, projectID,
		pgtype.Text{String: "member", Valid: true}, assigneeID,
	)
	if after != http.StatusBadRequest {
		t.Fatalf("revoked member: status=%d want %d", after, http.StatusBadRequest)
	}
}
```

- [ ] **Step 6: Run the tests**

Run: `cd server && go test ./internal/handler -run TestValidateAssigneePair -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add server/internal/handler/issue.go server/internal/handler/r2d_assignable_actors.go server/internal/handler/r2d_assignee_seam_test.go
git commit -m "feat(r2d): accept project-granted members in assignee validation"
```

---

### Task 5: Assignee display name in issue payloads

**Files:**
- Modify: `server/internal/handler/issue.go:37-80` (`IssueResponse`), `:326-455` (`issueToResponse`, `issueListRowToResponse`, third builder)
- Create: `packages/views/issues/utils/assignee-display.ts`
- Test: `packages/views/issues/utils/assignee-display.test.ts`

**Interfaces:**
- Produces:
  - server: optional `AssigneeName`, `AssigneeAvatarURL` fields (`json:"assignee_name,omitempty"`, `json:"assignee_avatar_url,omitempty"`).
  - client: `function issueAssigneeDisplay(issue: { assignee_type?: string | null; assignee_id?: string | null; assignee_name?: string | null; assignee_avatar_url?: string | null }, resolve: (type: string, id: string) => string): { name: string; avatarUrl: string | null }`.

- [ ] **Step 1: Write the failing client test**

Create `packages/views/issues/utils/assignee-display.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import { issueAssigneeDisplay } from "./assignee-display";

describe("issueAssigneeDisplay", () => {
  it("prefers the payload name over the local resolver", () => {
    const got = issueAssigneeDisplay(
      { assignee_type: "member", assignee_id: "u1", assignee_name: "Foreign User", assignee_avatar_url: "https://cdn/a.png" },
      () => "Unknown",
    );
    expect(got).toEqual({ name: "Foreign User", avatarUrl: "https://cdn/a.png" });
  });

  it("falls back to the resolver when the payload omits the name", () => {
    const got = issueAssigneeDisplay(
      { assignee_type: "member", assignee_id: "u1" },
      () => "Local Name",
    );
    expect(got).toEqual({ name: "Local Name", avatarUrl: null });
  });

  it("reports an unassigned issue", () => {
    const got = issueAssigneeDisplay({ assignee_type: null, assignee_id: null }, () => "Unknown");
    expect(got.name).toBe("");
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd packages/views && pnpm vitest run issues/utils/assignee-display.test.ts`
Expected: FAIL — cannot resolve `./assignee-display`.

- [ ] **Step 3: Implement the client helper**

Create `packages/views/issues/utils/assignee-display.ts`:

```ts
export interface AssigneeDisplayIssue {
  assignee_type?: string | null;
  assignee_id?: string | null;
  assignee_name?: string | null;
  assignee_avatar_url?: string | null;
}

/**
 * Prefers the server-resolved assignee name, which is the only source that can
 * name a collaborator from another Workspace. Falls back to the local
 * members/agents resolver, then to an empty name for an unassigned issue.
 */
export function issueAssigneeDisplay(
  issue: AssigneeDisplayIssue,
  resolve: (type: string, id: string) => string,
): { name: string; avatarUrl: string | null } {
  if (!issue.assignee_type || !issue.assignee_id) {
    return { name: "", avatarUrl: null };
  }
  const name = issue.assignee_name ?? resolve(issue.assignee_type, issue.assignee_id);
  return { name, avatarUrl: issue.assignee_avatar_url ?? null };
}
```

- [ ] **Step 4: Run the client test**

Run: `cd packages/views && pnpm vitest run issues/utils/assignee-display.test.ts`
Expected: PASS.

- [ ] **Step 5: Populate the payload fields on the server**

Add to `IssueResponse` in `server/internal/handler/issue.go`:

```go
	// AssigneeName/AssigneeAvatarURL are server-resolved so a collaborator from
	// another Workspace renders by name instead of falling back to "Unknown".
	// Omitted when the caller may not enumerate the assignee.
	AssigneeName      *string `json:"assignee_name,omitempty"`
	AssigneeAvatarURL *string `json:"assignee_avatar_url,omitempty"`
```

Resolve them once per response batch (not per row) with a helper in `r2d_assignable_actors.go`:

```go
// R2DAssigneeDisplay is the server-resolved display for one assignee. Empty
// fields mean "the caller may not enumerate this assignee"; the client then
// falls back to its local resolver.
type R2DAssigneeDisplay struct {
	Name      string
	AvatarURL string
}

// r2dAssigneeDisplay resolves assignee display fields for a set of issues,
// keyed by "<assignee_type>:<assignee_id>". member: owner-Workspace members,
// direct grantees, and members of granted Workspaces are named; agent/squad:
// owner-Workspace human members only (ProjectCapabilities.ViewResources).
func (h *Handler) r2dAssigneeDisplay(ctx context.Context, userID string, issues []db.Issue) (map[string]R2DAssigneeDisplay, error) {
	// 1. Collect the distinct (type,id) pairs from issues with a valid assignee.
	// 2. For "member", load the user rows for the collected ids and keep only
	//    those R2DIsAssignableMember reports assignable on the issue's Project;
	//    build "member:<id>" entries from user name/avatar_url.
	// 3. For "agent"/"squad", resolve the issue's Project capabilities via
	//    r2dauth; only when ViewResources is true, load the agent/squad name
	//    from the owner Workspace and add "<type>:<id>" entries. Otherwise omit.
	// 4. Return the map; missing keys mean "do not name this assignee".
	return map[string]R2DAssigneeDisplay{}, nil
}
```

Fill `AssigneeName`/`AssigneeAvatarURL` in `issueToResponse`, `issueListRowToResponse` and the third builder from that map, keyed by `*issue.AssigneeType + ":" + uuidToString(issue.AssigneeID)`; leave them nil when the key is absent.

- [ ] **Step 6: Add the redaction test**

Append to `server/internal/handler/r2d_assignable_actors_test.go`:

```go
func TestIssuePayload_OmitsAssigneeNameWhenNotEnumerable(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var projectID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Redact %d", suffix)).Scan(&projectID); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM project WHERE id = $1`, projectID) })

	// Assignee: a plain user with no grant and no owner-Workspace membership.
	var hiddenUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, fmt.Sprintf("Hidden %d", suffix), fmt.Sprintf("hidden-%d@example.test", suffix)).Scan(&hiddenUserID); err != nil {
		t.Fatalf("create hidden user: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, hiddenUserID) })

	// Viewer: a foreign user with a direct viewer grant on the Project.
	var viewerID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, fmt.Sprintf("Viewer %d", suffix), fmt.Sprintf("viewer-%d@example.test", suffix)).Scan(&viewerID); err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, viewerID) })
	if _, err := testPool.Exec(ctx, `
		INSERT INTO r2d_project_grants (project_id, principal_type, principal_id, role, created_by)
		VALUES ($1, 'user', $2, 'viewer', $3)
	`, projectID, viewerID, testUserID); err != nil {
		t.Fatalf("grant viewer: %v", err)
	}

	var issueID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, assignee_type, assignee_id, creator_type, creator_id, position, number, project_id)
		VALUES ($1, $2, 'todo', 'none', 'member', $3, 'member', $4, 0,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), $5)
		RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Redact issue %d", suffix), hiddenUserID, testUserID, projectID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	read := func(userID, workspaceID string) map[string]any {
		req := newRequest("GET", "/api/issues/"+issueID, nil)
		req.Header.Set("X-User-ID", userID)
		req.Header.Set("X-Workspace-ID", workspaceID)
		recorder := httptest.NewRecorder()
		testHandler.GetIssue(recorder, withURLParam(req, "id", issueID))
		if recorder.Code != http.StatusOK {
			t.Fatalf("get issue as %s: expected 200, got %d: %s", userID, recorder.Code, recorder.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode issue: %v", err)
		}
		return payload
	}

	if _, present := read(viewerID, testWorkspaceID)["assignee_name"]; present {
		t.Fatal("foreign viewer received assignee_name for a user it may not enumerate")
	}
	if _, present := read(testUserID, testWorkspaceID)["assignee_name"]; !present {
		t.Fatal("owner-Workspace member did not receive assignee_name")
	}
}
```

The viewer reads with `X-Workspace-ID` set to the owner Workspace so the direct read resolves the Project normally; the middleware re-binds it anyway.

- [ ] **Step 7: Wire the client helper into the issue presentation**

Replace direct `getActorName(issue.assignee_type, issue.assignee_id)` calls in `packages/views/issues/components/list-row.tsx`, `table-view-model.ts`, `board-card.tsx`, `swimlane-view.tsx`, `issue-detail.tsx`, `issue-hover-card.tsx` with `issueAssigneeDisplay(issue, getActorName)`. Keep the avatar URL fallback to the local resolver when `assignee_avatar_url` is absent.

- [ ] **Step 8: Run the gates**

Run: `cd server && go test ./internal/handler -run TestIssue` and `pnpm typecheck && pnpm test` from the repo root.
Expected: PASS; the only typecheck errors are the pre-existing `toBeInTheDocument` ones.

- [ ] **Step 9: Commit**

```bash
git add server/internal/handler/issue.go server/internal/handler/r2d_assignable_actors.go packages/views/issues/utils/assignee-display.ts packages/views/issues/utils/assignee-display.test.ts packages/views/issues/components
git commit -m "feat(r2d): resolve cross-workspace assignee names from issue payloads"
```

---

### Task 6: Acceptance verification and documentation

**Files:**
- Modify: `docs/r2d-extension-architecture.md`
- Test: existing suites

- [ ] **Step 1: Add the My Issues acceptance test**

The open question from the request — does a foreign assignee see the issue in My Issues — is answered by the companion plan's union plus this plan's assignment. Add a DB-backed test in `server/internal/handler/r2d_issue_table_acl_test.go`:

```go
func TestListIssueTableRows_ForeignAssigneeSeesIssueInMyIssuesScope(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var foreignWorkspaceID, foreignUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix) VALUES ($1, $2, 'FMI') RETURNING id
	`, fmt.Sprintf("My Issues WS %d", suffix), fmt.Sprintf("my-issues-%d", suffix)).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID) })
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, fmt.Sprintf("My Issues User %d", suffix), fmt.Sprintf("my-issues-%d@example.test", suffix)).Scan(&foreignUserID); err != nil {
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
	`, testWorkspaceID, fmt.Sprintf("My Issues Project %d", suffix)).Scan(&projectID); err != nil {
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
		INSERT INTO issue (workspace_id, title, status, priority, assignee_type, assignee_id, creator_type, creator_id, position, number, project_id)
		VALUES ($1, $2, 'todo', 'none', 'member', $3, 'member', $4, 0,
		        (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1), $5)
		RETURNING id
	`, testWorkspaceID, fmt.Sprintf("Assigned to foreign %d", suffix), foreignUserID, testUserID, projectID).Scan(&issueID); err != nil {
		t.Fatalf("create assigned issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	req := newRequest("POST", "/api/issues/table/rows", issueTableRowsRequest{
		Query: issueTableQuerySpec{Scope: issueTableScope{Kind: "my", Relation: "assigned"}},
	})
	req.Header.Set("X-User-ID", foreignUserID)
	req.Header.Set("X-Workspace-ID", foreignWorkspaceID)

	recorder := httptest.NewRecorder()
	testHandler.ListIssueTableRows(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("my rows: expected 200, got %d: %s", recorder.Code, recorder.Body.String())
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
		if row.Issue.ID == issueID {
			return
		}
	}
	t.Fatalf("foreign assignee does not see issue %s in My Issues: %s", issueID, recorder.Body.String())
}
```

The `my` scope reads the caller from `X-User-ID` (`compileIssueTableQuery`, `case "my"`), so no extra headers are needed.

- [ ] **Step 2: Run it**

Run: `cd server && go test ./internal/handler -run TestListIssueTableRows_ForeignAssigneeSeesIssueInMyIssuesScope -v`
Expected: PASS only when the companion plan is merged; otherwise record it as blocked and report.

- [ ] **Step 3: Verify notification delivery still targets grant recipients**

The assignee now may be a foreign grant holder, so assignment notifications must reach them through the P07-C fan-out rather than a Workspace-member list.

Run: `cd server && go test ./internal/handler -run 'TestP07C|Inbox' -v`
Expected: PASS.

Then read `server/internal/handler/r2d_inbox_acl.go` and confirm the recipient set is built from Project grant recipients (direct users + members of granted Workspaces) and that delivery re-checks the grant. If assignment notifications are instead built from `member` rows of the owner Workspace only, add the assignee to the recipient set behind the same grant check and cover it with a test in `server/internal/handler/r2d_p07c_inbox_read_test.go` before proceeding.

- [ ] **Step 4: Run the full gates**

Run: `cd server && go test ./internal/r2dauth ./internal/middleware ./internal/handler` and, from the repo root, `pnpm typecheck && pnpm lint && pnpm test`.
Expected: PASS except the pre-existing failures listed in the session notes.

- [ ] **Step 5: Document the model**

Append to `docs/r2d-extension-architecture.md`:

```markdown
## Cross-workspace assignment

A Project grant makes its holder assignable on that Project. `assignee_type`
stays `member`; `assignee_id` is a user id that may belong to a Workspace other
than the Project owner's. Assignable users are owner-Workspace members, direct
grantees, and members of granted Workspaces (`GET
/api/projects/{id}/assignable-actors`). Owner-Workspace Agents and Squads stay
unassignable for foreign collaborators. Issue payloads carry `assignee_name`
and `assignee_avatar_url` when the caller may enumerate the assignee, so a
cross-Workspace assignee never renders as `Unknown`.
```

- [ ] **Step 6: Commit**

```bash
git add docs/r2d-extension-architecture.md server/internal/handler/r2d_issue_table_acl_test.go
git commit -m "docs(r2d): record cross-workspace assignment model"
```
