# Foreign collaborator: unread badge and @mention roster

Date: 2026-09-17
Status: approved design, pending implementation plan

Follow-up to PR #80 (cross-workspace issue links, realtime, inbox list,
assignee roster) and PR #81 (LocalDirectoryHint resource 404).

## Problem 1 — the sidebar Inbox badge is always 0 for a foreign collaborator

The sidebar badge reads the cross-workspace unread summary:
`useInboxUnreadCount` → `unreadCountForWorkspace(summary, activeWsId)`
(`packages/core/inbox/queries.ts:98-106`) against
`GET /api/inbox/unread-summary`.

Measured against the running server as the foreign collaborator (assignee of a
shared Project issue, member of `proxima-centauri-dj5q`, not of the owner
Workspace `deneb-xo4l`):

```
GET /api/inbox/unread-summary  -> []            (badge renders 0)
GET /api/inbox/unread-count    -> {"count":14}
GET /api/inbox                 -> 23 rows
```

Two independent causes:

1. `R2DListUnreadInboxSummaryRows` inner-joins membership —
   `JOIN member m ON m.workspace_id = i.workspace_id AND m.user_id = i.recipient_id`
   (`server/pkg/db/generated/r2d_notification_ext.go:309`). A Project-grant
   notification row lives under the issue owner's Workspace, so the foreign
   recipient has no matching member row and every row is dropped.
2. Even without that join, the summary is **account-level and attributed to the
   notification's own Workspace**. The foreign row would land under
   `deneb-xo4l`, while the sidebar asks for the *active* Workspace's count, so
   the entry can never match. The summary exists so the badge costs no extra
   request and so the workspace switcher can mark which workspace holds a
   pending message (`unreadWorkspaceIds`).

### Design

The badge must equal the number of unread rows the Inbox list shows for the
active Workspace, so it has to come from a workspace-scoped, recipient-scoped,
ACL-filtered source: `GET /api/inbox/unread-count` (`CountUnreadInbox`), which
already selects rows by recipient and filters visibility against the active
Workspace (`server/internal/handler/inbox.go:353-383`).

Its one mismatch is the dedup rule: the list renders one row per issue (newest
wins), while `R2DListUnreadInboxRowsForRecipient` counts raw rows. Fix that in
SQL by mirroring the summary's grouping:

```sql
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
WHERE newest.read = false
```

The grouping key and the newest-wins ordering are copied verbatim from the
summary so the two cannot drift. `R2DListUnreadInboxRowsForRecipient` has one
caller (`CountUnreadInbox`), so the change is contained.

Client: `useInboxUnreadCount` switches to a new
`inboxUnreadCountOptions(wsId)` calling `api.getUnreadInboxCount()`, keyed under
`inboxKeys.all(wsId)` so the existing `onInboxInvalidate` refresh already covers
it. `unreadWorkspaceIds` / `unreadCountForWorkspace` stay for the switcher dot;
for a foreign row the dot correctly does not light a Workspace the recipient
cannot open.

### Non-goals

- Changing the switcher dot semantics.
- Attributing foreign rows to every Workspace the recipient belongs to (that
  would multiply the count).

### Verification

- Server DB test: a recipient with a Project-grant notification and no member
  row for its Workspace counts 1 through `CountUnreadInbox` for their own
  Workspace; a superseded unread row on the same issue counts 0 once a newer
  row is read.
- Client: `useInboxUnreadCount` reads the count endpoint; the badge converges
  with the visible list after `inbox:new`, read, and archive events.

## Problem 2 — the @mention list omits foreign Project collaborators

`mention-suggestion.tsx:727` builds the member items from the active
Workspace's member cache only:

```ts
const members: MemberWithUser[] = qc.getQueryData(workspaceKeys.members(wsId)) ?? [];
```

so an owner typing `@` in a comment on a shared Project issue never sees the
Project's granted collaborators — the same class of defect the assignee picker
had before PR #80.

### Design

`createMentionSuggestion` already takes options at the editor seam
(`packages/views/editor/extensions/index.ts:268`). Add one optional
`mentionProjectId`. When it is set, member items come from the Project roster
cache (`projectAssignableActorsOptions`, the same query the picker uses) mapped
to `MentionItem`s; otherwise the Workspace member cache stays the source. The
roster must be warm: the surface that sets `mentionProjectId` also subscribes to
`projectAssignableActorsOptions`, exactly as the picker does.

Wire it from the issue comment editor, which knows `issue.project_id`. Chat and
any other editor without a Project keep today's behaviour.

### Verification

- Component test: with `mentionProjectId` set and the roster cached, the
  suggestion list contains a granted collaborator absent from the Workspace
  member cache; without it, the list is unchanged.
- The issue comment editor passes the issue's `project_id`; the chat editor
  passes nothing.

## Risks

- The badge source change touches two existing suites that model the badge
  against the summary: `packages/core/inbox/badge-convergence.test.tsx` (a
  property test) and `packages/core/inbox/mutations.test.tsx`. Both must be
  re-pointed at the count endpoint rather than deleted.
- Deduping in SQL before the ACL filter is safe because every row in an issue
  group shares one Project, so visibility is identical across the group.
