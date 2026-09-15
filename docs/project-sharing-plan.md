# Project Sharing and Cross-Workspace ACL Plan

Status: **Planned / implementation-ready**  
Tracker: [#12 — R2D patch stack & upstream sync cursor](https://github.com/r2d-ai/multica/issues/12)  
Architecture: [R2D Extension Architecture](./r2d-extension-architecture.md)

## Goal

Allow teams to collaborate on selected projects across Multica workspaces without collapsing the privacy boundary of either workspace.

The product vocabulary for this deployment is explicit:

```text
Team      = Workspace
Project   = optional cross-workspace collaboration boundary
Squad     = Workspace-owned execution/orchestration resource
```

There is **no separate R2D Team entity** in V1. A Multica Workspace is the stable identity for a team.

A project keeps its upstream owning workspace. Foreign users/workspaces may receive project access without joining the owning workspace and without gaining visibility into unrelated workspace data.

## Non-goals

V1 does not:

- create a second Team model above or below Workspace;
- merge workspaces;
- require foreign collaborators to join the owning workspace;
- make every issue belong to a project;
- add arbitrary per-issue ACL as the normal policy model;
- share a workspace's squads, agents, chats, repositories, knowledge or credentials merely because one project is shared;
- make Squad ownership project-scoped;
- rewrite upstream project ownership or workspace membership tables;
- depend on upstream project-permission PR `multica-ai/multica#7540`.

`multica-ai/multica#7540` is a watch/reference item only. Its current permission model is not the target architecture for R2D.

## Required semantics

### Workspace is the team/private boundary

A workspace represents one team and remains the default privacy, membership, agent-context and squad-ownership boundary.

Projectless issues remain private to their workspace according to upstream membership rules. This includes user-created and agent-created projectless issues.

In requirement language, "limit an issue by Team" therefore means **scope it to its Workspace**. No additional team id is required.

### Project sharing is explicit

A project remains owned by its existing `projects.workspace_id`.

Cross-workspace access exists only through an explicit R2D project grant to a user or workspace.

Example:

```text
R&D Workspace (= Team R&D)
├─ projectless issue                       R&D only
├─ Squad: Architecture                     R&D only
└─ Project: Internal Portal
   ├─ R&D members                          local workspace access
   ├─ Software Workspace                   explicit project grant
   └─ selected user from Game Workspace    explicit user grant
```

A Software user granted access to `Internal Portal` must not gain access to unrelated R&D projects, projectless issues, squads, agents, chats, repositories, knowledge, credentials, integrations or workspace settings.

### Project resources inherit project ACL

Resources whose authorization scope is the project inherit the project's effective ACL.

At minimum this includes project issues and, as implementation phases are completed, project-scoped comments/attachments, notifications, activity, repository bindings, knowledge/resources and API/MCP surfaces.

The project is the collaboration boundary. V1 does not add a second user-grant ACL on individual project issues.

### Projectless issues remain workspace-scoped

If `issue.project_id` is null, the issue remains governed by its workspace/team boundary.

This satisfies the desired rule:

```text
issue scope = project ACL when project_id exists
           | workspace/team ACL when projectless
```

Moving an issue into a shared project changes its effective visibility to that project's ACL. Moving it out returns it to the destination workspace's normal workspace visibility.

## Role model

### Team Leader

**Team = Workspace**, therefore Team Leader uses existing upstream workspace governance roles rather than a new R2D role table.

For V1:

```text
workspace owner/admin => Team Leader authority
workspace member      => normal Team member
```

Team Leader authority remains workspace-scoped. It does not automatically grant management rights over projects owned by another workspace.

### Project roles

R2D project grants support:

| Role | Intent |
|---|---|
| `viewer` | Read the project and permitted project-scoped resources |
| `member` | Viewer rights plus normal project participation/write operations |
| `manager` | Member rights plus project settings and project ACL management |

A **Project Leader** is represented by effective project role `manager`. No separate Project Leader entity/table is needed.

Role ordering:

```text
viewer < member < manager
```

Where multiple grants apply, the strongest effective role wins.

### Global Observer

R2D requires a deployment-level read-only role:

```text
global_observer
```

The intended user is management/audit that must observe all teams/workspaces and projects without receiving normal administrative mutation authority.

A global observer may read normal workspace/project/issue/activity surfaces across this R2D deployment but must not receive write rights or secret/credential access solely from the observer role.

Because Multica currently has no organization object above Workspace, this is an explicit R2D deployment-level role, stored separately from upstream workspace membership/roles.

Global observer semantics must be centralized and tested; handlers must not scatter ad-hoc bypasses.

## Principals

Project grants support exactly these V1 principal types:

```text
user
workspace
```

`workspace` is the Team principal. Do not introduce a separate `team` principal.

A workspace grant is dynamic: all current active members of that workspace inherit the project role. Removing/disabling a member immediately removes that inherited access without rewriting project grants.

A user grant may grant one selected user access independently from their workspace peers.

## R2D storage model

R2D state follows `docs/r2d-extension-architecture.md`: side tables only; no custom columns in upstream-owned tables; no DB foreign keys from R2D tables into upstream objects by default.

### `r2d_project_extra`

Conceptual schema:

```text
project_id
visibility       workspace | private
created_at
updated_at
```

No row means:

```text
visibility = workspace
```

This preserves existing project behavior and avoids backfill.

Semantics:

- `workspace`: owner-workspace members retain normal upstream-compatible access; explicit foreign grants may add collaborators;
- `private`: owner-workspace membership alone is not sufficient for normal members; explicit project grants and documented owner/admin policy determine access.

`private` must never broaden access.

### `r2d_project_grants`

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

- unique effective grant per `(project_id, principal_type, principal_id)`;
- indexes for project -> grants;
- indexes for principal -> projects;
- backend validates referenced project/user/workspace existence and eligibility;
- grants to deleted/disabled/ineligible principals are ineffective;
- no DB foreign key from this table into upstream project/user/workspace tables.

### `r2d_global_roles`

```text
user_id
role              global_observer
created_by
created_at
updated_at
```

Required invariants:

- unique `(user_id, role)`;
- user must be active/eligible according to deployment policy;
- observer role is read-only and never implies secret visibility or mutation rights.

The first R2D migration allocated to this feature uses version `900000` unless tracker #12 records that version as already consumed.

## Effective project authorization

Conceptually:

```text
subject_is_active
AND (
  owner_workspace_governance_override
  OR global_observer_read
  OR owner_workspace_local_access
  OR explicit_user_project_grant
  OR membership_in_granted_workspace
)
```

The authorization service must evaluate an **operation**, not only a generic visibility boolean.

Suggested API shape:

```go
Can(ctx, subject, operation, resource)
CanAccessProject(ctx, subject, projectID)
CanManageProject(ctx, subject, projectID)
EffectiveProjectRole(ctx, subject, projectID)
VisibleProjectIDs(ctx, subject, workspaceContext)
IsGlobalObserver(ctx, userID)
```

Exact names should follow repository conventions. The invariant is one central policy implementation.

## Operation matrix

The central policy should encode at least:

| Operation | viewer | member | manager | Team Leader of owner workspace | global observer |
|---|---:|---:|---:|---:|---:|
| View project/issues/comments | yes | yes | yes | yes | yes |
| Create/comment on project issue | no | yes | yes | yes | no |
| Edit/move normal project issue | no | yes | yes | yes | no |
| Change project settings | no | no | yes | yes | no |
| Grant/revoke project ACL | no | no | yes | yes | no |
| Delete project | no | no | yes* | yes* | no |

`*` remains subject to existing upstream ownership/admin safety rules.

A foreign Team Leader is not automatically a Project Leader. They need an explicit project grant unless another documented administrative rule applies.

## Organization directory

Cross-workspace project sharing needs a minimal deployment directory for selecting user/workspace principals.

Expose only identification data required for sharing, for example:

```text
workspace name
user display name
active/inactive status
```

Do not expose unrelated project lists, workspace membership detail, issue counts, agent/squad state, credentials, repository access or private profile fields merely for principal discovery.

Directory lookup permission and project access permission are separate capabilities.

## Agent and Squad semantics

### Ownership

Agents and Squads remain **workspace-owned** resources.

In particular:

```text
Squad belongs to Workspace
Squad does NOT belong to Project
```

Sharing a project never exposes the owning workspace's Squads or Agent inventory to foreign collaborators.

### Project execution

A workspace-local Agent/Squad may operate on a project only when the execution context has effective access to that project.

If a later phase adds project-to-squad association, it is a reference/assignment only; Squad ownership, configuration, credentials, memory and discovery remain scoped to its Workspace.

A foreign collaborator's workspace may use its own Squad against a shared project only through an explicitly authorized project-scoped execution path. Project sharing must not provide a mechanism to browse or invoke the owner workspace's private Squad simply because the project is visible.

Workspace agent chat remains workspace-context by default. A project-scoped run must not inherit unrelated workspace projectless issues, chat history, memories, repositories, credentials or knowledge.

## Authorization surfaces

Project ACL is not complete until every relevant path uses the central policy.

Audit at least:

- project list and direct project load;
- project update/settings/delete;
- project ACL list/grant/revoke;
- issue list and direct issue load;
- issue create/update/move/project assignment;
- project and issue search;
- dashboards/counts/activity/analytics;
- notifications/inbox;
- realtime/WebSocket subscriptions/events;
- assignee/user/workspace/project pickers;
- comments and attachments;
- Agents/Squads and project-scoped execution;
- repository/VCS bindings;
- knowledge/resources;
- export/reporting;
- API endpoints;
- MCP/tool endpoints.

Backend enforcement is mandatory. Hidden frontend controls are not authorization.

Negative tests must prove an unauthorized user cannot infer hidden project metadata through direct IDs, counts, search results, autocomplete, notifications or realtime events.

## Implementation plan

Implementation is intentionally split into small replayable patches to reduce upstream/backport conflict.

### P00 — Specification and extension guardrails

Deliverables:

- this document is authoritative for Team=Workspace semantics;
- `docs/r2d-extension-architecture.md` remains normative for fork isolation;
- tracker #12 records patch/upstream cursor;
- migration range `900000-999999` remains reserved;
- identify R2D package/module boundary and feature flag.

Acceptance:

- no runtime behavior change;
- no custom upstream schema mutation;
- no new R2D Team abstraction.

### P01 — R2D ACL schema

Deliverables:

- `900000_r2d_project_sharing.up.sql`;
- matching down migration;
- `r2d_project_extra`;
- `r2d_project_grants`;
- `r2d_global_roles`;
- uniqueness/query-path indexes;
- migration tests.

Acceptance:

- fresh install migrates successfully;
- existing database migrates successfully;
- rollback removes only R2D-owned schema;
- upstream schema remains unchanged;
- no project backfill is required.

### P02 — Central authorization service

Deliverables:

- repositories/services for project extras, grants and global roles;
- effective project-role calculation;
- operation policy matrix;
- workspace-grant expansion through current active membership;
- global-observer read-only policy;
- batched visible-project queries;
- table-driven authorization tests.

Acceptance:

- no handler implements its own grant/observer logic;
- list paths avoid N+1 ACL queries;
- revoked/disabled membership takes effect on the next authorization check.

### P03 — Sharing and directory APIs

Deliverables:

- list project grants;
- grant/update role;
- revoke grant;
- minimal user/workspace directory lookup;
- global-observer administration restricted to the proper system authority;
- audit attribution/logging.

Acceptance:

- Team Leaders can manage projects they govern but do not gain automatic rights in foreign workspaces;
- Project Leaders (`manager`) can manage their project's ACL;
- normal users cannot self-escalate;
- foreign collaborators remain non-members of the owner workspace.

### P04 — Core query enforcement

Integrate central policy into:

- project list/get;
- issue list/get;
- project issue create/update;
- issue project assignment/move;
- project/issue search;
- dashboards/counts/activity.

Acceptance:

- granted foreign users/workspaces can use the shared project at their effective role;
- unrelated owner-workspace data remains undiscoverable;
- projectless issues remain workspace/team-private;
- global observer reads but cannot mutate;
- revocation applies immediately on the next check.

### P05 — Web UI

Deliverables:

- project sharing UI;
- user/workspace principal search;
- `viewer | member | manager` role management;
- Project Leader representation through `manager`;
- shared-project discovery/navigation;
- global-observer administration for eligible operators;
- permission-aware controls.

Acceptance:

- foreign users do not have to join or switch into the owner workspace to use a shared project;
- Team=Workspace terminology remains clear;
- no separate Team-management UI is introduced.

### P06 — Agents and Squads

Deliverables:

- enforce workspace ownership/privacy for Agent/Squad surfaces;
- project-scoped execution checks;
- prevent project ACL from exposing foreign workspace Squad/Agent inventory;
- optional project association only as a reference, never ownership transfer;
- negative tests for memory/context/credential leakage.

### P07 — Secondary surfaces and hardening

Audit and enforce:

- notifications/realtime;
- comments/attachments;
- repositories/VCS;
- knowledge/resources;
- API/MCP;
- export/reporting;
- cleanup/reconciliation for R2D logical references.

Acceptance:

- authorization coverage checklist complete;
- metadata-leakage tests cover sensitive surfaces;
- grant/revoke/membership changes propagate correctly.

## Minimum test matrix

Principals:

```text
owner-workspace owner/admin (Team Leader)
owner-workspace normal member
foreign user with viewer grant
foreign user with member grant
foreign user with manager grant (Project Leader)
member of foreign workspace with workspace grant
foreign Team Leader without project grant
foreign user with no grant
global observer
revoked user
disabled/inactive user
system/root admin where applicable
```

Resources:

```text
shared project
unshared project in owner workspace
project issue
projectless workspace/team issue
search/aggregate result
owner-workspace Squad/Agent
foreign-workspace Squad/Agent
project-scoped run/resource
workspace-private run/resource
```

Tests must cover positive access, write denial and absence of metadata leakage.

## Upstream/backport strategy

Project Sharing remains an ordered R2D patch stack over `multica-ai/multica`.

Before each patch and upstream sync:

1. inspect upstream project/issue/auth/search/realtime/agent/squad changes;
2. inspect `multica-ai/multica#7540` and any successor only as reference material;
3. inspect other upstream permission/Space work only for reusable primitives, not as the target product model;
4. reuse upstream capability only when it exactly satisfies an R2D requirement;
5. otherwise keep R2D code in side tables/additive packages with the smallest integration hooks;
6. update tracker #12 with upstream cursor, migration cursor, conflicts, retired patches and manual ports.

If upstream eventually implements equivalent cross-workspace project ACL semantics, migrate deliberately and retire corresponding R2D patches rather than running two authorization systems.

## Definition of done

Project Sharing V1 is done when:

- Team is represented by Workspace with no duplicate team abstraction;
- selected users/workspaces can collaborate across workspaces on explicitly shared projects;
- Team Leader maps to upstream workspace owner/admin governance;
- Project Leader maps to project `manager`;
- global observer can read across teams/projects without mutation or secret access;
- foreign collaborators gain no owner-workspace membership or unrelated visibility;
- project issues inherit project ACL;
- projectless issues remain workspace/team-private;
- Squads remain workspace-owned and are not exposed by project sharing;
- all backend read/write/query/realtime/API/MCP surfaces enforce the same central policy;
- custom state exists only in `r2d_*` tables;
- no upstream table schema is modified;
- R2D migrations use the reserved range;
- tests cover authorization and metadata leakage;
- tracker #12 accurately records patch and upstream cursors.
