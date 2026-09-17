# Cross-workspace assignment on shared Projects

Date: 2026-09-17
Status: approved design, pending implementation plan

Companion to
`docs/superpowers/specs/2026-09-17-shared-project-issues-in-workspace-lists-design.md`
(spec A). Spec A makes shared-Project issues visible in workspace issue reads;
this spec makes the people involved in those Projects assignable. Both are
required before "assign a foreign collaborator" works end to end.

## Problem

Three related gaps, all rooted in the assumption "assignee belongs to the
Workspace that owns the issue":

1. **Picker roster.** `assignee-picker.tsx:110-112` reads
   `memberListOptions/agentListOptions/squadListOptions(wsId)` for the active
   Workspace only, so a Project's workspace-grant recipients never appear.
2. **Assignee naming.** `useActorName` (`packages/core/workspace/hooks.ts:94-128`)
   resolves names from the same active-Workspace lists and falls back to
   `"Unknown"`, so a foreign assignee renders as `Unknown` everywhere.
3. **Write gate, two layers.**
   - R2D middleware blocks `assignee_type`/`assignee_id` for any non-member:
     `server/internal/middleware/r2d_issue_scope.go:185-190` (create),
     `:318-326` (direct mutation), `:505-513` (batch update), answering
     `403 project access does not grant workspace resource access`.
   - Even with that relaxed, upstream rejects the pair:
     `validateAssigneePair` (`server/internal/handler/issue.go:3813-3821`)
     requires `assignee_type="member"` to satisfy `GetMemberByUserAndWorkspace`,
     otherwise `400 assignee_id does not refer to a member of this workspace`.

Verified separately: `assignee_id` for a member assignee is a **user id**
(`issue_assignee_types_test.go:66`), not a `member` row id, so a foreign user is
representable in the column today.

## Goal

A user holding a Project grant can assign work on that Project to themselves or
to another user who also holds a grant on it, and every surface shows that
person's real name instead of `Unknown`.

## Non-goals

- Assigning owner-Workspace Agents or Squads to a foreign collaborator.
- A new `assignee_type`.
- Changing notification delivery semantics; only the targeting set is extended.
- Reconciling dashboard rollups (spec A non-goal).

## Design

### 1. Policy (`r2dauth`)

`ProjectAssignableMembers(ctx, projectID) ([]string, error)` returns the user ids
that may be assigned on the Project:

- members of the Project's owner Workspace;
- users with a direct user grant;
- members of any Workspace that holds a workspace grant.

The caller must already hold `OperationContribute` on the Project; the set is
independent of the caller. `r2dauth.Resolve` remains the only role resolver.

### 2. Roster endpoint

`GET /api/projects/{id}/assignable-actors?type=member|agent|squad&q=&limit=`

- mounted from `server/cmd/server/r2d_routes.go` so it inherits URL-based
  Project authorization and workspace re-binding;
- requires `OperationContribute` on the Project;
- `type=member` returns `ProjectAssignableMembers` entries
  `{ type: "member", id: <user id>, name, avatar_url?, secondary? }`;
- `type=agent|squad` returns owner-Workspace inventory and is served **only**
  when the caller is a human member of the owner Workspace (the same condition
  as `ProjectCapabilities.ViewResources`); a foreign caller gets an empty list,
  never a 403 that would confirm inventory, preserving the P06 boundary
  ("sharing a project does not expose foreign workspace Squads/Agents");
- `q` follows the existing sharing-directory search convention (2-char
  minimum, bounded limit).

### 3. Write enforcement

Middleware (`r2d_issue_scope.go`): for a non-member of the issue's Workspace,
replace the blanket `assignee_type`/`assignee_id` block with:

- allow `assignee_type` unset/null (clearing) and `"member"`;
- a `"member"` value is allowed only when `assignee_id` is in
  `ProjectAssignableMembers` for the issue's effective Project;
- keep rejecting `"agent"` and `"squad"` (owner-Workspace inventory), and keep
  rejecting `attachment_ids`, `label_ids`, `origin_type`, `origin_id`;
- projectless issues keep the Workspace boundary: no foreign assignee.

Upstream seam (`issue.go` `validateAssigneePair`, member branch): when
`GetMemberByUserAndWorkspace` fails, call a handler-level
`h.r2dAssigneeAllowed(ctx, r, workspaceID, assigneeID, effectiveProjectID)`.
Allowed → the pair is valid; otherwise keep the existing 400. This is the
defense-in-depth copy of the middleware rule so a future route that bypasses the
middleware cannot admit an unauthorized assignee. The effective Project comes
from the request's `project_id` on create and from the loaded issue on update.

### 4. Assignee display in payloads

Add optional `assignee_name` and `assignee_avatar_url` to `IssueResponse`
(`server/internal/handler/issue.go`), resolved server-side:

- `member` → the user's name/avatar when the caller may enumerate that user:
  owner-Workspace member, direct grantee, or member of a granted Workspace;
- `agent`/`squad` → owner-Workspace human members only;
- otherwise the fields are omitted.

Client: a single `issueAssigneeDisplay(issue)` helper prefers the payload
fields and falls back to the current local resolver. Only the issue presentation
call sites change (list row, table, board card, swimlane, issue detail, hover
card) — `useActorName` itself keeps its Workspace-scoped meaning.

The picker uses `assignable-actors` when the surface has a Project context
(project issue surface, issue detail of a Project issue) and keeps the
active-Workspace lists otherwise. `assignee-frequency` stays Workspace-scoped.

### 5. Notifications and My Issues

Assignee notification reuses the P07-C grant-recipient fan-out and must apply
the same ACL at delivery time, so a revoked grant stops delivery.

My Issues already reads through the collection path spec A widens, filtered by
`assignee_id = <user id>`, so once A and B land the foreign assignee sees the
issue in My Issues in any Workspace shell they belong to. This is the explicit
acceptance test for the open question that motivated this spec.

## Risks

- Extending `member` semantics is the main risk. Audit list: Members tab,
  board/swimlane assignee grouping, workload and `assignee-frequency`, mention
  pickers, notification targeting, `involves_user_id` relations. Each must
  either tolerate a non-member user id or be explicitly left Workspace-scoped.
- `assignable-actors` is a new enumeration surface: it must never return a
  foreign Workspace's members for a Project that does not grant that Workspace,
  and never return owner-Workspace Agents/Squads to a foreign caller.
- Revocation: an issue can stay assigned to a user whose grant was revoked. The
  assignment remains data; visibility and delivery follow the ACL. No cleanup is
  attempted.

## Verification

DB-backed Go tests (`testutil`, `dbfx`):

- roster: a foreign contributor receives granted users/members; receives no
  Agents/Squads; a non-contributor gets a non-disclosing response;
- write: a foreign contributor can self-assign and assign a granted member;
  cannot assign an ungranted user; cannot assign an owner-Workspace Agent or
  Squad; an owner-Workspace member can assign a granted foreign member;
  projectless issues reject foreign assignees;
- payload: `assignee_name` present for an enumerable assignee, omitted when
  redacted;
- revocation: after the grant is removed, new assignment is rejected and the
  existing assignment no longer exposes the name to the removed user;
- spec A acceptance: the foreign assignee sees the issue in My Issues.

Views tests: picker sources the Project roster when project-scoped; name
resolution prefers the payload fields and still handles a missing name.
