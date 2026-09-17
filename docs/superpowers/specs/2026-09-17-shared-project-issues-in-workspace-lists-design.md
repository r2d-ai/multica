# Shared Project issues in workspace issue lists

Date: 2026-09-17
Status: approved design, pending implementation plan

## Problem

A collaborator can read a Project that lives in another Workspace: the Project
surface works because project-scoped issue reads re-bind the request workspace
to the Project owner (`server/internal/middleware/r2d_issue_read_scope.go:64-81`,
`:212-232`). The same collaborator's workspace issue list does not show that
Project's issues.

The cause is not a crash. There are two independent readable-Project
implementations and both are hard-scoped to the active Workspace:

- table channel: `compileIssueTableQuery` compiles
  `i.workspace_id = $1 AND (i.project_id IS NULL OR i.project_id = ANY($2::uuid[]))`
  in `server/internal/handler/issue_table_query.go:443-455`, using
  `Handler.r2dReadableWorkspaceProjectIDs`;
- legacy rewrite: `r2dReadableWorkspaceProjectIDs` in
  `server/internal/middleware/r2d_issue_collection_visibility.go:20-36`, used for
  `GET /api/issues`, `GET /api/issues/grouped` and `POST /api/issues/query`.

Both read `R2DListWorkspaceProjectAccessFacts`
(`server/pkg/db/generated/r2d_acl_ext.go:123-140`), which filters
`p.workspace_id = $2`. Foreign shared Projects are excluded even though the
Project list already includes them via `r2dProjectCollectionIDs`
(`server/internal/middleware/r2d_project_scope.go:246-276`), and dashboard
rollups already use the cross-workspace `ListVisibleProjectIDs`
(`server/internal/handler/r2d_dashboard_acl.go:63`). The issue list is the
outlier.

## Goal

Workspace-scoped issue reads union issues from the Projects already shown in the
active Workspace's Project list, while projectless issues stay
Workspace-private.

## Non-goals

- Widening project-scoped reads, entity by-id reads, or any write path.
- Changing the Workspace-membership gate itself.
- Adding new UI surfaces.
- `GET /api/issues`/`POST /api/issues/query` with `open_only=true`. Its upstream
  query is `ListOpenIssues`, hard-scoped to `WHERE i.workspace_id = $1`
  (`server/pkg/db/queries/issue.sql:399`), and the P04-C3 response filter can
  only remove rows, never add foreign ones. No client call site passes
  `open_only` today (`packages/core/api/client.ts:915` is the only reference),
  so this stays Workspace-bound; revisit only if a caller adopts it.

## Design

### 1. One policy rule

Promote the inclusion rule currently inlined in `r2dProjectCollectionIDs`
(`r2d_project_scope.go:262-265`) into `r2dauth`, the policy owner, as a pure
function over already-loaded facts:

`ProjectIDsForIssueCollection(facts []ProjectFacts, activeWorkspaceID string) []string`

(A pure function rather than a `Store` method: every caller already holds
`R2DListCandidateProjectAccessFacts` results, so this keeps one policy
implementation without adding a second DB access path.)

Include a Project when `r2dauth.Resolve(facts).Can(OperationRead)` holds and at
least one of:

- `OwnerWorkspaceID == activeWorkspaceID`;
- explicit direct or workspace grant, i.e. the same `viewer | member | manager`
  test as `r2dExplicitProjectGrant` (`r2d_project_scope.go:237-244`);
- `global_observer`.

`r2dProjectCollectionIDs` is rewritten to call it so the Project list and the
issue lists cannot drift. Facts come from `R2DListCandidateProjectAccessFacts`;
`r2dauth.Resolve` remains the only policy engine. Dashboard rollups keep their
existing `ListVisibleProjectIDs` behavior; reconciling that divergence is out of
scope.

### 2. Server predicate changes

Table channel (`issue_table_query.go`): the workspace predicate becomes

```
(i.workspace_id = $1 AND i.project_id IS NULL) OR i.project_id = ANY($2::uuid[])
```

with `$2` from the new rule. Projectless rows stay bound to the active
Workspace; project-backed rows match the readable set regardless of owner
Workspace. `ListIssueTableRows`, `...Groups`, `...Facets` and every count/total
derived from the predicate inherit this, so pagination and group totals remain
internally consistent.

Legacy rewrite (`r2d_issue_collection_visibility.go`): the readable set passed
to `r2dApplyReadableProjectValues` comes from the same rule, so
`GET /api/issues`, `GET /api/issues/grouped` and `POST /api/issues/query` agree
with the table channel. `include_no_project=true` is unchanged, keeping
projectless rows visible because the caller already passed the Workspace
membership gate.

`shouldR2DFailClosed` / `r2dUnfilteredIssueSurface` keep their structure: the
migrated surfaces were already ACL-native and stay non-blocking, while any
surface still relying on workspace-wide SQL keeps failing closed when the
Workspace contains an unreadable Project.

### 3. Explicitly unchanged

- Project-scoped reads (`project_id` body scope or URL) keep their existing
  authorize-then-rebind path.
- Entity `GET/PATCH/DELETE /api/issues/{uuid}` keep the Project ACL boundary.
- Writes and batch mutations keep the Workspace-owned readable set
  (`r2d_inbox_acl.go:157`); widening a read set must never widen a write gate.
- Comments, attachments, VCS/repos, secrets, MCP, exports, dashboard rollups.

### 4. UI consequences

Rows already render a Project badge and board/list modes already group by
Project, so no new affordance is required. The risk is foreign assignees: a
Workspace-grant recipient can see issues assigned to members or Agents of the
owner Workspace, who are absent from the active Workspace's member/agent maps.
Display must degrade to the identifier/neutral label rather than break; assignee
resolution logic is not changed.

### 5. Verification

DB-backed Go tests (`server/internal/testutil`, `dbfx` fixtures):

- a Workspace-grant recipient sees the foreign Project's issues in table
  rows/groups/facets, in `GET /api/issues`, `grouped` and `query`, with totals
  that include them;
- same for a direct-grant recipient;
- negative: a projectless issue in the foreign Workspace never appears; an
  ungranted foreign Project's issues never appear, including in counts and
  facets; a revoked grant removes rows and decrements totals on the next read;
- write gate: a foreign `viewer` grantee still cannot mutate;
- cross-endpoint consistency: the same filter yields the same row count through
  the table channel and the legacy list;
- extend the `p07_leakage_test.go` allowlist/audit with the new union.

## Risks

- Workspace issue views now contain rows owned by another Workspace. This is the
  requested behavior and matches the dashboard, but it is the first place the
  Workspace issue list crosses that boundary.
- Foreign assignee rendering (section 4) is the most likely visible regression;
  the tests above plus a manual check cover it.
- Facet/group counts must move together with rows to avoid a surface that shows
  a count for a row it will not render.
