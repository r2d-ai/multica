# Foreign unread badge and @mention roster Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A foreign Project collaborator sees an accurate sidebar Inbox unread count, and an owner can @mention that collaborator in a comment on the shared issue.

**Architecture:** The badge stops reading the account-level workspace summary and reads the workspace-scoped, recipient-scoped count endpoint, whose SQL is deduped to one row per issue so it matches the list. The mention suggestion gains an optional Project context and reads the same Project roster the assignee picker already uses.

**Tech Stack:** Go (sqlc-extension SQL, Chi), PostgreSQL, React/TypeScript (`packages/core`, `packages/views`), Vitest.

**Spec:** `docs/superpowers/specs/2026-09-17-foreign-inbox-unread-and-mentions-design.md`

## Global Constraints

- Policy stays in `r2dauth`; SQL supplies facts only. No new migration.
- R2D raw SQL lives in `server/pkg/db/generated/r2d_notification_ext.go` (established extension seam).
- Reads widen; write gates do not.
- Comments are English. Conventional commits, atomic.
- Go gate: `cd server && go test ./internal/handler`.
- Frontend gate: `pnpm typecheck && pnpm lint && pnpm test` (repo root, excludes mobile).
- DB-backed Go tests need `make up`; if the environment is unavailable, report the test as unrun rather than deleting it.

---

### Task 1: Dedupe the unread-count query

**Files:**
- Modify: `server/pkg/db/generated/r2d_notification_ext.go:264-294`
- Test: `server/internal/handler/r2d_p07c_inbox_read_test.go` (extend)

**Interfaces:**
- Consumes: nothing new.
- Produces: `R2DListUnreadInboxRowsForRecipient` keeps its signature `([]R2DInboxUnreadRow, error)` but now returns one row per `(workspace, COALESCE(issue_id, id))` group, newest first, unread only.

- [ ] **Step 1: Write the failing test**

Extend `server/internal/handler/r2d_p07c_inbox_read_test.go`:

```go
func TestCountUnreadInbox_DedupesSupersededRows(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := time.Now().UnixNano()

	var foreignWorkspaceID, foreignUserID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO workspace (name, slug, issue_prefix) VALUES ($1, $2, 'UNR') RETURNING id
	`, fmt.Sprintf("Unread WS %d", suffix), fmt.Sprintf("unread-%d", suffix)).Scan(&foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID) })
	if err := testPool.QueryRow(ctx, `
		INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id
	`, fmt.Sprintf("Unread User %d", suffix), fmt.Sprintf("unread-%d@example.test", suffix)).Scan(&foreignUserID); err != nil {
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
	`, testWorkspaceID, fmt.Sprintf("Unread Project %d", suffix)).Scan(&projectID); err != nil {
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
	`, testWorkspaceID, fmt.Sprintf("Unread issue %d", suffix), testUserID, projectID).Scan(&issueID); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, issueID) })

	insertRow := func(read bool, minutesAgo int) string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO inbox_item (workspace_id, recipient_type, recipient_id, type, severity, issue_id, title, read, created_at)
			VALUES ($1, 'member', $2, 'new_comment', 'info', $3, 'probe', $4, now() - ($5 || ' minutes')::interval)
			RETURNING id
		`, testWorkspaceID, foreignUserID, issueID, read, minutesAgo).Scan(&id); err != nil {
			t.Fatalf("insert inbox row: %v", err)
		}
		t.Cleanup(func() { testPool.Exec(context.Background(), `DELETE FROM inbox_item WHERE id = $1`, id) })
		return id
	}
	olderUnread := insertRow(false, 2)
	newerRead := insertRow(true, 1)

	count := func() int64 {
		req := newRequest("GET", "/api/inbox/unread-count", nil)
		req.Header.Set("X-User-ID", foreignUserID)
		req.Header.Set("X-Workspace-ID", foreignWorkspaceID)
		recorder := httptest.NewRecorder()
		testHandler.CountUnreadInbox(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("count: expected 200, got %d: %s", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Count int64 `json:"count"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode count: %v", err)
		}
		return body.Count
	}

	// The newest row for the issue is read, so the issue is not unread — the
	// older unread row is superseded and must not be counted.
	if got := count(); got != 0 {
		t.Fatalf("superseded row counted: got %d want 0", got)
	}

	if _, err := testPool.Exec(ctx, `UPDATE inbox_item SET read = false WHERE id = $1`, newerRead); err != nil {
		t.Fatalf("flip newer row: %v", err)
	}
	if got := count(); got != 1 {
		t.Fatalf("newest unread row not counted: got %d want 1", got)
	}
	_ = olderUnread
}
```

Imports already present in that file (`context`, `encoding/json`, `fmt`, `net/http`, `net/http/httptest`, `testing`, `time`) cover this test.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd server && go test ./internal/handler -run TestCountUnreadInbox_DedupesSupersededRows -v`
Expected: FAIL — count is 1 in the first case because both rows are counted raw.

- [ ] **Step 3: Rewrite the query**

Replace the body of `R2DListUnreadInboxRowsForRecipient`:

```go
// R2DListUnreadInboxRowsForRecipient returns the (workspace, project) pair of
// every VISIBLE unread inbox row for one recipient, across workspaces, after
// the same newest-per-issue dedup the Inbox list applies. The dedup key and
// ordering mirror R2DListUnreadInboxSummaryRows so the sidebar badge and the
// list cannot disagree; grouping before the caller's Project ACL filter is safe
// because every row in a group shares one Project. The caller filters by
// current Project ACL and counts; the payload stays tiny so a count endpoint
// never loads notification bodies.
func (q *Queries) R2DListUnreadInboxRowsForRecipient(ctx context.Context, recipientID string) ([]R2DInboxUnreadRow, error) {
	rows, err := q.db.Query(ctx, `
SELECT newest.workspace_id::text, newest.project_id
FROM (
    SELECT DISTINCT ON (i.workspace_id, COALESCE(i.issue_id, i.id))
        i.workspace_id,
        COALESCE(iss.project_id::text, '') AS project_id,
        i.read
    FROM inbox_item i
    LEFT JOIN issue iss ON iss.id = i.issue_id
    WHERE i.recipient_type = 'member'
      AND i.recipient_id = $1::uuid
      AND i.archived = false
    ORDER BY i.workspace_id, COALESCE(i.issue_id, i.id), i.created_at DESC, i.id DESC
) newest
WHERE newest.read = false`, recipientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]R2DInboxUnreadRow, 0)
	for rows.Next() {
		var r R2DInboxUnreadRow
		if err := rows.Scan(&r.WorkspaceID, &r.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
```

- [ ] **Step 4: Run the test**

Run: `cd server && go test ./internal/handler -run 'TestCountUnreadInbox|TestP07C' -v`
Expected: PASS (requires `make up`).

- [ ] **Step 5: Commit**

```bash
git add server/pkg/db/generated/r2d_notification_ext.go server/internal/handler/r2d_p07c_inbox_read_test.go
git commit -m "fix(r2d): dedupe the unread inbox count to match the list"
```

---

### Task 2: Point the sidebar badge at the count endpoint

**Files:**
- Modify: `packages/core/inbox/queries.ts:83-106`
- Modify: `packages/core/api/client.ts:2493-2501` (comment only)
- Test: `packages/core/inbox/queries.test.ts`, `packages/core/inbox/badge-convergence.test.tsx`, `packages/core/inbox/mutations.test.tsx`

**Interfaces:**
- Consumes: `api.getUnreadInboxCount()` (exists).
- Produces: `inboxUnreadCountOptions(wsId: string)`; `useInboxUnreadCount(wsId)` keeps its signature.

- [ ] **Step 1: Write the failing test**

Add to `packages/core/inbox/queries.test.ts`:

```ts
import { setApiInstance } from "../api";
import type { ApiClient } from "../api/client";
import { inboxKeys, inboxUnreadCountOptions } from "./queries";

describe("inboxUnreadCountOptions", () => {
  it("reads the workspace-scoped count endpoint, not the account summary", async () => {
    const getUnreadInboxCount = vi.fn(async () => ({ count: 3 }));
    setApiInstance({ getUnreadInboxCount } as unknown as ApiClient);
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    try {
      const count = await qc.fetchQuery(inboxUnreadCountOptions("ws-1"));
      expect(count).toBe(3);
      expect(getUnreadInboxCount).toHaveBeenCalledTimes(1);
      // The key must sit under the workspace prefix so onInboxInvalidate covers it.
      expect(inboxUnreadCountOptions("ws-1").queryKey).toEqual([
        ...inboxKeys.all("ws-1"),
        "unread-count",
      ]);
    } finally {
      setApiInstance(undefined as unknown as ApiClient);
      qc.clear();
    }
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd packages/core && pnpm vitest run inbox/queries.test.ts`
Expected: FAIL — `inboxUnreadCountOptions` is not exported.

- [ ] **Step 3: Implement the options and switch the hook**

In `packages/core/inbox/queries.ts`:

```ts
/**
 * Unread inbox count for one workspace. Workspace-scoped because the count must
 * match the rows the Inbox list shows for the ACTIVE workspace: the list is
 * recipient-scoped server-side, so a Project-grant notification from another
 * Workspace is shown (and counted) while that workspace is active, but the
 * account-level summary attributes it to the Workspace that owns the issue.
 * Keyed under `inboxKeys.all(wsId)` so `onInboxInvalidate` refreshes it.
 */
export function inboxUnreadCountOptions(wsId: string) {
  return queryOptions({
    queryKey: [...inboxKeys.all(wsId), "unread-count"] as const,
    queryFn: async () => (await api.getUnreadInboxCount()).count,
  });
}

export function useInboxUnreadCount(wsId: string | null | undefined): number {
  const { data } = useQuery({
    ...inboxUnreadCountOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  return data ?? 0;
}
```

Update the doc comment above `useInboxUnreadCount` to say the count comes from
the workspace-scoped endpoint and why (the summary cannot serve a
cross-workspace grant), and update the comment on
`api.getUnreadInboxCount` in `packages/core/api/client.ts` accordingly.

- [ ] **Step 4: Re-point the two existing suites**

`packages/core/inbox/badge-convergence.test.tsx` and
`packages/core/inbox/mutations.test.tsx` currently drive the badge through
`getInboxUnreadSummary`. Replace that mock with `getUnreadInboxCount` returning
the deduped unread count of the simulated server, and keep every assertion
about convergence intact — these tests exist to prove the badge converges with
the list, so they must keep doing that against the new source. Do not delete or
weaken an assertion to make them pass; if an assertion no longer applies,
replace it with the equivalent statement about the count endpoint.

- [ ] **Step 5: Run the suite**

Run: `cd packages/core && pnpm vitest run inbox/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/core/inbox/queries.ts packages/core/inbox/queries.test.ts packages/core/inbox/badge-convergence.test.tsx packages/core/inbox/mutations.test.tsx packages/core/api/client.ts
git commit -m "fix(r2d): read the sidebar unread badge from the workspace count"
```

---

### Task 3: Offer the Project roster in the @mention list

**Files:**
- Modify: `packages/views/editor/extensions/mention-suggestion.tsx:715-760`
- Modify: `packages/views/editor/extensions/index.ts:260-275`
- Modify: `packages/views/editor/content-editor.tsx` (pass the new option through)
- Modify: the issue comment editor call site (thread `issue.project_id`)
- Test: `packages/views/editor/extensions/mention-suggestion.test.tsx` (extend)

**Interfaces:**
- Consumes: `projectAssignableActorsOptions` and `r2dAssignableActorKeys` from `@multica/core/projects/r2d-assignable-actors` (added in PR #80).
- Produces: `createMentionSuggestion(queryClient, options)` gains `mentionProjectId?: string | null`; `ContentEditor` gains `mentionProjectId?: string | null`.

- [ ] **Step 1: Write the failing test**

Extend `packages/views/editor/extensions/mention-suggestion.test.tsx`. Seed the
query client with a Project roster that contains a user absent from the
Workspace member cache, build the suggestion with `mentionProjectId`, and assert
that user appears:

```tsx
it("offers the Project's granted collaborators when a Project is in context", () => {
  const qc = new QueryClient();
  qc.setQueryData(workspaceKeys.members("ws-1"), [memberFixture("user-1", "Ada")]);
  qc.setQueryData(r2dAssignableActorKeys.members("project-1"), [
    { type: "member", id: "user-1", name: "Ada" },
    { type: "member", id: "user-2", name: "Foreign Collaborator" },
  ]);

  const items = buildMentionItemsForTest(qc, { projectId: "project-1", query: "" });

  expect(items.map((i) => i.label)).toContain("Foreign Collaborator");
});

it("keeps the Workspace member list when no Project is in context", () => {
  const qc = new QueryClient();
  qc.setQueryData(workspaceKeys.members("ws-1"), [memberFixture("user-1", "Ada")]);
  qc.setQueryData(r2dAssignableActorKeys.members("project-1"), [
    { type: "member", id: "user-2", name: "Foreign Collaborator" },
  ]);

  const items = buildMentionItemsForTest(qc, { projectId: null, query: "" });

  expect(items.map((i) => i.label)).toContain("Ada");
  expect(items.map((i) => i.label)).not.toContain("Foreign Collaborator");
});
```

Export the existing synchronous item builder from `mention-suggestion.tsx` (or a
small wrapper) so the test can call it directly instead of driving the whole
tiptap suggestion UI.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd packages/views && pnpm vitest run editor/extensions/mention-suggestion.test.tsx`
Expected: FAIL — no Project roster support.

- [ ] **Step 3: Implement the roster source**

In `mention-suggestion.tsx`, take `mentionProjectId` from the options and, where
member items are built (`:727`), prefer the Project roster when it is set:

```ts
const rosterProjectId = options.mentionProjectId;
const members: MemberWithUser[] = rosterProjectId
  ? []
  : (qc.getQueryData(workspaceKeys.members(wsId)) ?? []);
const rosterPrincipals: R2DProjectPrincipal[] = rosterProjectId
  ? (qc.getQueryData(r2dAssignableActorKeys.members(rosterProjectId)) ?? [])
  : [];
```

Map `rosterPrincipals` to the same `MentionItem` shape the member rows produce
(`type: "member"`, `id: principal.id`, `label: principal.name`). Keep the
`"all members"` entry and the agent/squad gating exactly as they are; a
Project-scoped roster carries members only, so the agent/squad sections stay
driven by the Workspace caches.

- [ ] **Step 4: Thread the option**

Add `mentionProjectId` to the editor options in
`packages/views/editor/extensions/index.ts` and pass it through
`packages/views/editor/content-editor.tsx` to `createMentionSuggestion`. At the
issue comment editor, pass `issue.project_id`; at the chat input, pass nothing.
The issue surface must also subscribe to `projectAssignableActorsOptions(projectId)`
so the roster cache is warm before the user types `@`.

- [ ] **Step 5: Run the tests**

Run: `cd packages/views && pnpm vitest run editor/ issues/components/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/views/editor packages/views/issues/components
git commit -m "feat(r2d): offer project collaborators in the mention list"
```

---

### Task 4: Verification and documentation

**Files:**
- Modify: `docs/r2d-extension-architecture.md`

- [ ] **Step 1: Run the full gates**

Run: `cd server && go test ./internal/handler` and, from the repo root, `pnpm typecheck && pnpm lint && pnpm test`.
Expected: PASS except the pre-existing failures recorded in the session notes.

- [ ] **Step 2: Browser check**

Using the running dev environment, as the foreign collaborator: trigger an owner
comment, confirm the sidebar Inbox badge increments without a refresh, and
confirm the owner can `@`-mention the collaborator in a comment on the shared
issue.

- [ ] **Step 3: Document**

Append to `docs/r2d-extension-architecture.md`:

```markdown
## Cross-workspace inbox badge and mentions

The sidebar unread badge reads the workspace-scoped `GET /api/inbox/unread-count`,
deduped to one row per issue, so it matches the recipient-scoped Inbox list
(which includes Project-grant rows from other Workspaces). The account-level
`unread-summary` remains only for the workspace-switcher dot. The @mention list
uses the Project's assignable roster when the editor has a Project context, and
the active Workspace's member list otherwise.
```

- [ ] **Step 4: Commit**

```bash
git add docs/r2d-extension-architecture.md
git commit -m "docs(r2d): record inbox badge and mention roster rules"
```
