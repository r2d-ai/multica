# Project Sharing and Cross-Workspace ACL Plan

Status: **Planned**  
Tracker: [#12 — R2D patch stack & upstream sync cursor](https://github.com/r2d-ai/multica/issues/12)  
Architecture: [R2D Extension Architecture](./r2d-extension-architecture.md)

## Goal

Allow multiple teams/workspaces to collaborate on selected projects without collapsing Multica's existing workspace privacy boundary.

The target model is:

```text
Workspace = private/team boundary
Project   = optional cross-workspace collaboration boundary
```

This is an additive R2D capability. Existing Multica workspace/project ownership semantics must continue to work unchanged when no R2D sharing configuration exists.

## Non-goals

V1 does not:

- merge workspaces;
- make every issue belong to a project;
- make projectless issues globally visible;
- add issue-level ACL as the normal authorization model;
- expose foreign workspace data merely because one project is shared;
- make foreign collaborators members of the project's owning workspace;
- rewrite upstream project ownership or membership tables;
- depend on upstream project-permission PR `multica-ai/multica#7540`.

## Required semantics

### Workspace-private by default

A workspace remains a meaningful privacy and agent-context boundary.

Projectless issues remain visible according to the owning workspace's existing rules. This includes issues created directly by users and issues created by agents without a project context.

### Project sharing is explicit

A project belongs to its existing upstream workspace. Cross-workspace access exists only when an R2D project grant explicitly makes it available to another principal.

Example:

```text
R&D Workspace
├─ projectless issue                 R&D only
├─ agent-created projectless issue   R&D only
└─ Project: Internal Portal
   ├─ R&D members                    local access
   └─ Software Workspace             explicit project grant
```

A user from Software who can access `Internal Portal` must not gain access to unrelated R&D projects, projectless issues, chats, agents, repositories, knowledge, or workspace configuration.

### Project resources inherit project access

Resources whose authorization scope is the project must use the effective project ACL. This includes project issues and, as integration phases are completed, project-scoped comments/attachments, agents, repositories, knowledge, notifications and API/MCP surfaces.

The project itself is the collaboration boundary. V1 should not introduce a second issue-level ACL unless a concrete product requirement cannot be represented at project scope.

## Roles

### Workspace roles

Preserve upstream workspace role semantics. Existing owner/admin behavior is not replaced by R2D ACL.

### Project roles

R2D project grants support:

| Role | Intent |
|---|---|
| `viewer` | Read project and project-scoped resources allowed by the feature surface |
| `member` | Viewer rights plus normal project participation/edit operations |
| `manager` | Member rights plus project sharing/membership management |

The exact operation matrix must be encoded centrally in the authorization service, not independently in handlers or frontend controls.

### Organizational actors

Expected policy:

- root/system admin: global audit/override according to existing administrative semantics;
- workspace admin/team leader: may discover minimal identities needed for collaboration;
- project owner/manager: may manage sharing for projects they control;
- normal user: cannot grant project access.

`directory lookup` and `project access` are separate permissions. Finding a user/workspace in a minimal organization directory never grants access to its data.

## Principals

V1 supports:

```text
user
workspace
```

A future `team` principal may be added if Multica's team model provides a stable identity and a real requirement emerges. Do not block V1 on a new team abstraction.

Whole-workspace sharing means current active members of that workspace inherit the project role. Removing or disabling a user must immediately remove inherited effective access without rewriting every project grant.

## R2D storage model

Per the R2D extension architecture, no custom field is added to upstream `project` or other upstream tables.

### `r2d_project_extra`

Conceptual schema:

```text
project_id       primary logical reference to upstream project
visibility       workspace | private
created_at
updated_at
```

No row means:

```text
visibility = workspace
```

This default preserves current behavior and avoids backfilling existing projects.

`private` is reserved for project-specific restrictions if/when the authorization matrix needs it. It must not silently broaden access.

### `r2d_project_grants`

Conceptual schema:

```text
id
project_id
principal_type    user | workspace
principal_id
role              viewer | member | manager
created_by
created_at
updated_at
```

Required invariants:

- one effective grant per `(project_id, principal_type, principal_id)`;
- indexes for project lookup and principal-to-project lookup;
- no DB foreign key into upstream project/user/workspace tables;
- backend validates referenced objects and principal eligibility;
- grants for disabled/deleted principals are ineffective even if audit rows remain.

The first R2D migration for this feature must use migration version `900000` unless tracker #12 already shows that number allocated.

## Effective authorization

Conceptually:

```text
allowed =
  principal_is_active
  AND (
    existing_local_workspace_access
    OR explicit_user_project_grant
    OR membership_in_granted_workspace
  )
```

Where multiple grants could apply, the authorization service should derive the highest effective project role according to one documented role ordering.

Administrative override behavior must be explicit and tested. Do not spread `if admin` bypasses across handlers.

Suggested central API shape:

```go
CanAccessProject(ctx, principal, projectID)
CanManageProject(ctx, principal, projectID)
EffectiveProjectRole(ctx, principal, projectID)
VisibleProjectIDs(ctx, principal, workspaceContext)
```

Names may change to match repository conventions; the key requirement is a single policy implementation.

## Organization directory

Cross-workspace sharing needs a minimal directory lookup, not cross-workspace data access.

Expose only data required to identify a principal, for example:

```text
workspace name
user display name
title/team where already non-sensitive and appropriate
active/inactive status
```

Do not expose unrelated workspace membership data, private profile data, project lists, issue counts, agent state, credentials, or repository access through this lookup.

## Agent behavior

Workspace agent chat remains workspace-context by default.

An agent operating explicitly in a shared project context may create or manipulate project-scoped resources subject to the same project authorization checks as a user operation.

Sharing a project must never expose the owning workspace agent's unrelated chat history, memory, resources or projectless issues to foreign collaborators.

## Authorization surfaces

Project ACL enforcement is incomplete until every relevant read/write path is covered.

Audit at least:

- project list and direct project load;
- project update/settings/delete operations;
- issue list and direct issue load;
- issue create/update/move and project assignment;
- global/workspace search;
- dashboards, counts and activity feeds;
- notifications/inbox;
- realtime/websocket subscriptions and events;
- assignee/user/project pickers;
- comments and attachments;
- agent/squad operations;
- repositories/VCS resources;
- knowledge/resources;
- API endpoints;
- MCP/tool endpoints.

A hidden UI control is not authorization. Backend enforcement is mandatory.

## Implementation plan

Implementation is intentionally split into small replayable patches to reduce upstream backport conflict.

### P00 — Extension foundation and guardrails

Deliverables:

- merge `docs/r2d-extension-architecture.md` and this plan;
- reserve migration range `900000-999999` in tracker #12;
- add/identify the R2D package/module boundary;
- verify tests can detect accidental custom alteration of upstream schema where practical;
- define the Project Sharing feature flag if rollout requires one.

Verified during planning:

- current migration files are sorted lexicographically;
- the complete basename is the migration version;
- the migration readiness model checks all migration versions, including out-of-order additions;
- therefore future upstream migrations below `900000` remain discoverable after an R2D migration has been applied.

Acceptance:

- no production behavior change;
- no upstream schema mutation;
- tracker #12 is the authoritative cursor.

### P01 — R2D project extension schema

Deliverables:

- `900000_r2d_project_sharing.up.sql`;
- matching down migration;
- `r2d_project_extra`;
- `r2d_project_grants`;
- uniqueness and query-path indexes;
- migration tests.

Constraints:

- no `ALTER TABLE` on upstream objects;
- no backfill of existing projects;
- no DB FK into upstream tables.

Acceptance:

- fresh install migrates successfully;
- existing database migrates successfully;
- rollback removes only R2D-owned schema;
- upstream project schema is byte-for-byte logically unchanged by P01.

### P02 — Central authorization service

Deliverables:

- R2D repository/service for project extras and grants;
- effective-role calculation;
- centralized `CanAccessProject` / `CanManageProject` / visible-project logic;
- active-principal validation;
- tests covering local workspace access, user grants, workspace grants, revoked/disabled principals and administrative policy.

Acceptance:

- policy tests are table-driven and independent from HTTP handlers;
- handlers do not implement their own grant logic;
- no N+1 grant lookup on project lists.

### P03 — Sharing API and directory lookup

Deliverables:

- list project grants;
- grant/update role;
- revoke grant;
- minimal organization user/workspace lookup;
- audit attribution through `created_by` and logs/events as appropriate.

Acceptance:

- only authorized project managers/admins can modify grants;
- lookup permission does not imply data permission;
- invalid/inactive principals cannot receive effective access;
- foreign users remain non-members of the owner workspace.

### P04 — Core query enforcement

Integrate the centralized policy into the minimum surfaces required for safe cross-workspace collaboration:

- project list/get;
- issue list/get;
- project issue creation and updates;
- project/issue search;
- project assignment/move paths;
- dashboards/counts/activity that can leak project existence or issue metadata.

Prefer narrow hooks into upstream query/handler paths over copying or rewriting upstream implementations.

Acceptance:

- a granted foreign user can use the shared project at their effective role;
- the same user cannot discover unrelated owner-workspace data through list, direct ID, search or aggregate endpoints;
- projectless issues remain owner-workspace-only;
- revocation takes effect on the next authorization check.

### P05 — Web UI

Deliverables:

- project sharing management UI for eligible managers;
- principal lookup using the minimal directory API;
- role selection/revoke flow;
- representation/discovery of projects shared with the current user/workspace;
- permission-aware controls.

Acceptance:

- UI does not require foreign users to switch/join the owning workspace merely to access the shared project;
- controls reflect backend capability but do not substitute for backend authorization;
- no unrelated upstream page/layout rewrite.

### P06 — Secondary surfaces and hardening

Audit and enforce all remaining project-derived surfaces, especially:

- notifications/realtime/websocket;
- comments/attachments;
- agents/squads and project-scoped agent actions;
- repositories/VCS;
- knowledge/resources;
- API/MCP tools;
- export/reporting surfaces.

Acceptance:

- authorization coverage checklist is complete;
- negative leakage tests exist for sensitive surfaces;
- project grant/revoke changes propagate correctly to realtime/background behavior.

## Test matrix

At minimum test these principals:

```text
owner-workspace admin
owner-workspace normal member
foreign user with viewer grant
foreign user with member grant
foreign user with manager grant
member of foreign workspace with workspace grant
foreign user with no grant
revoked user
disabled/inactive user
system/root admin where applicable
```

And these resource types:

```text
shared project
unshared project in same owner workspace
project issue
projectless issue
search/aggregate result
project-scoped agent/resource
workspace-private agent/resource
```

Tests must cover both positive authorization and absence of metadata leakage.

## Upstream/backport strategy

Project Sharing is maintained as an ordered R2D patch stack, not a permanent rewrite of upstream project authorization.

Before each patch and each upstream sync:

1. inspect changes to upstream project/issue/auth/search/realtime code;
2. inspect `multica-ai/multica#7540` or any successor if it changes/lands;
3. reuse upstream capability when it genuinely replaces an R2D patch;
4. otherwise keep R2D behavior behind the smallest integration hooks;
5. update tracker #12 with conflicts, retired patches and the new upstream cursor.

If upstream eventually implements equivalent project sharing semantics, migrate data/behavior deliberately and retire the corresponding R2D patches instead of maintaining duplicate authorization systems.

## Definition of done

Project Sharing V1 is done when:

- existing non-shared workspace/project behavior is unchanged;
- selected users/workspaces can collaborate on an explicitly shared project;
- foreign collaborators gain no owner-workspace membership or unrelated visibility;
- projectless issues remain workspace-private;
- all backend read/write/query/realtime surfaces enforce the same central policy;
- custom state exists only in `r2d_*` tables;
- no upstream table schema was modified;
- R2D migrations use the reserved range;
- tests cover authorization and metadata-leakage cases;
- tracker #12 accurately records the patch and upstream cursors.
