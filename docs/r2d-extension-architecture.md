# R2D Extension Architecture

Status: **Normative**  
Tracker: [#12 — R2D patch stack & upstream sync cursor](https://github.com/r2d-ai/multica/issues/12)

## Purpose

`r2d-ai/multica` follows upstream `multica-ai/multica` closely. R2D-specific features must therefore be implemented as a thin, replayable patch stack that minimizes conflict with upstream schema, migrations, and high-churn core code.

This document defines the mandatory extension rules for all R2D customizations.

## Core invariant

> **Upstream tables are schema-read-only. R2D features extend upstream objects through namespaced side tables and backend projections.**

R2D code MUST NOT add custom columns to upstream-owned tables unless an explicit exception is documented, reviewed, and approved.

Examples of prohibited customizations:

```sql
ALTER TABLE project ADD COLUMN visibility ...;
ALTER TABLE issue ADD COLUMN r2d_status ...;
ALTER TABLE "user" ADD COLUMN department_id ...;
```

Preferred model:

```text
project                 upstream-owned
└── r2d_project_extra   R2D-owned extension

issue                   upstream-owned
└── r2d_issue_extra     R2D-owned extension, only when required

user                    upstream-owned
└── r2d_user_extra      R2D-owned extension, only when required
```

## Ownership boundary

### Upstream-owned

The following are treated as upstream implementation details and must remain easy to replace during a sync:

- upstream table definitions and columns;
- upstream migration files and numbering;
- upstream ownership semantics such as `project.workspace_id`;
- generated DB models derived from upstream schema;
- core handlers and query paths except for the smallest required integration hooks.

### R2D-owned

R2D owns:

- tables prefixed `r2d_`;
- R2D migrations in the reserved migration range;
- R2D authorization/domain services;
- R2D API handlers and DTO wrappers;
- R2D frontend feature modules;
- tests that enforce the extension boundary.

## Side-table design

Use a typed side table for each upstream object that needs additional R2D state. Do not create one generic EAV table for unrelated object types.

Example:

```text
r2d_project_extra
- project_id       PK/reference value
- visibility
- created_at
- updated_at
```

A missing extension row SHOULD represent the upstream-compatible default whenever possible. This avoids backfilling every existing upstream row and makes rollback/sync safer.

For example, Project Sharing defines absence of `r2d_project_extra` as `visibility=workspace`.

### Relations are separate tables

Multi-value data and ACLs are relations, not fields packed into the extra table:

```text
r2d_project_grants
- id
- project_id
- principal_type
- principal_id
- role
- created_by
- created_at
- updated_at
```

Use explicit columns and indexes for authorization-critical data. Do not hide ACL state inside JSON blobs.

## Foreign-key policy

By default, **do not create database foreign keys from R2D tables to upstream tables**.

Example:

```text
r2d_project_extra.project_id  -> logical reference to upstream project.id
                                no DB FK
```

Reasons:

- upstream may change delete/constraint behavior;
- upstream migrations must not become dependent on R2D schema;
- R2D migrations remain independently replayable;
- upstream schema replacement/backport has a smaller conflict surface.

Backend write paths MUST validate that referenced upstream objects exist. Orphan R2D rows SHOULD be removed through explicit cleanup/reconciliation logic.

Foreign keys between R2D-owned tables are allowed when useful.

## Backend projection rule

Storage separation is an implementation detail. API consumers and the UI should receive a coherent domain object.

Preferred flow:

```text
upstream query
    + R2D side-table query/join
    + authorization/computed state
    -> service/domain projection
    -> API DTO
```

R2D fields SHOULD be added in service/API DTO wrappers rather than by modifying generated upstream DB models.

Centralize repeated extension lookups behind an R2D service/repository package. Do not scatter direct `r2d_*` SQL across upstream handlers.

For hot list/search paths, use batched lookups or SQL joins rather than N+1 per-object queries.

## Migration namespace

R2D reserves migration versions:

```text
900000-999999
```

Naming convention:

```text
900000_r2d_<feature>.up.sql
900000_r2d_<feature>.down.sql
900001_r2d_<feature>.up.sql
900001_r2d_<feature>.down.sql
```

Rules:

1. Never allocate `current upstream migration + 1` for R2D work.
2. Never renumber or modify an upstream migration during a backport.
3. Every R2D migration must have a corresponding down migration unless the tracker explicitly documents why rollback is impossible.
4. Record the latest allocated R2D migration in tracker #12.
5. R2D table/index/constraint names must be namespaced to avoid name collisions.

### Why the high range works

The current migration loader sorts migration filenames lexicographically and uses the complete filename basename as the migration version. It does not parse a fixed-width three-digit integer. Its readiness logic also checks every migration version, specifically allowing lower-numbered migrations introduced after a higher version was already applied.

Therefore a deployed `900000_r2d_*` migration does not prevent a future upstream `475_*`, `476_*`, etc. from being detected and applied.

The reserved range is intentionally far from upstream's current sequence and must remain R2D-only.

## Indexing rules

Every R2D table must be indexed for its actual access paths. At minimum:

- primary/reference object lookup;
- principal lookup for ACL tables;
- uniqueness required by business semantics;
- list/filter paths used by authorization.

For Project Sharing, expected access paths include:

```text
project -> grants
principal -> visible projects
project + principal -> effective role
```

Index design belongs to the R2D migration, not to an upstream table.

## Code isolation

Prefer additive packages/modules such as:

```text
server/internal/r2d/...
server/pkg/r2d/...
apps/web/features/r2d-.../
```

Exact paths may follow existing repository conventions, but the ownership boundary must remain obvious.

Changes to high-churn upstream files should normally be limited to small integration hooks, for example:

```go
access.VisibleProjectIDs(...)
access.CanAccessProject(...)
access.CanManageProject(...)
```

Do not duplicate an upstream handler or query wholesale merely to insert R2D behavior.

## Patch-stack discipline

R2D custom development is an ordered patch stack over upstream.

Each patch/phase must:

- have one feature purpose;
- avoid unrelated refactors/format churn;
- be independently reviewable;
- keep migrations separate from unrelated behavior;
- include tests at its authorization/data boundary;
- be replayable/rebasable after an upstream sync.

Do not mix upstream sync/backport commits and R2D feature implementation in the same commit series.

## Upstream synchronization

Tracker #12 is the source of truth for:

- latest upstream release observed;
- upstream SHA cursor;
- fork baseline SHA;
- R2D migration cursor;
- active R2D patch stack;
- conflicts/manual ports during each sync.

For every upstream sync:

1. record the previous cursor;
2. compare upstream changes;
3. sync upstream on a dedicated branch;
4. preserve upstream migrations unchanged;
5. replay/rebase R2D patches in order;
6. resolve conflicts patch-by-patch;
7. run affected migration/backend/frontend tests;
8. update tracker #12;
9. merge through a PR targeting **`r2d-ai/multica` only**.

R2D must never open its customization/backport PRs against `multica-ai/multica`.

## Review checklist

A custom feature is not ready if any answer below is wrong:

- Does it leave upstream table definitions untouched?
- Are custom fields stored in `r2d_*` side tables?
- Does a missing extra row have a safe/default interpretation where possible?
- Are authorization-critical relations modeled explicitly rather than hidden JSON?
- Are DB FKs into upstream avoided unless an exception is justified?
- Is the backend projection centralized and free of N+1 behavior?
- Does the migration use the reserved R2D range and namespace?
- Are upstream integration edits minimal?
- Can this patch be replayed independently after the next upstream sync?
- Is tracker #12 updated when the migration/backport cursor changes?

## Cross-workspace assignment

A Project grant makes its holder assignable on that Project. `assignee_type`
stays `member`; `assignee_id` is a user id that may belong to a Workspace other
than the Project owner's. Assignable users are owner-Workspace members, direct
grantees, and members of granted Workspaces (`GET
/api/projects/{id}/assignable-actors`). Owner-Workspace Agents and Squads stay
unassignable for foreign collaborators. Issue payloads carry `assignee_name`
and `assignee_avatar_url` when the caller may enumerate the assignee, so a
cross-Workspace assignee never renders as `Unknown`.

The read path is ACL-native: `r2dauth` remains the sole role resolver, and the
assignee write gate (`r2dAssigneeFieldDecision` plus `R2DIsAssignableMember`)
replaces the former "must be a Workspace member" rule. Projectless Issues keep
the Workspace boundary. Project-grant notification reads stay grant-aware
through `r2dInboxVisibleFor`, which re-checks the current Project ACL on every
read and mutation.

## Issue collection visibility

Workspace-scoped issue reads union the Projects an issue collection may show:
Projects owned by the active Workspace, foreign Projects the user holds an
explicit user/workspace grant on, and a global observer's readable set. The rule
is `r2dauth.ProjectIDsForIssueCollection` and is shared with the Project list so
Projects and Issues cannot drift. Projectless issues stay Workspace-private, and
write/batch paths keep the Workspace-owned readable set.

## Cross-workspace inbox badge and mentions

The sidebar unread badge reads the workspace-scoped `GET /api/inbox/unread-count`,
whose query is deduped to the newest row per issue, so it matches the
recipient-scoped Inbox list (which includes Project-grant rows from other
Workspaces). The account-level `unread-summary` remains only for the
workspace-switcher dot, which needs the per-workspace breakdown.

The `@`mention list uses the Project's assignable roster when the editor has a
Project context (the issue comment composer passes the issue's `project_id`),
and the active Workspace's member list otherwise.
