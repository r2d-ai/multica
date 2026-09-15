# Single-Workspace Team and Project ACL Plan

Status: **Planned / implementation-ready**  
Tracker: [#12 — R2D patch stack & upstream sync cursor](https://github.com/r2d-ai/multica/issues/12)  
Architecture: [R2D Extension Architecture](./r2d-extension-architecture.md)

## Goal

Run the company inside a single Multica workspace while preserving meaningful privacy between teams and allowing selected projects to be shared across teams.

The target model is:

```text
Workspace = organization / identity boundary
Team      = default ownership and privacy boundary
Project   = collaboration boundary
Issue     = inherits project ACL, or team ACL when projectless
```

V1 is intentionally **single-workspace**. It does not require or implement cross-workspace collaboration.

The existing upstream workspace membership check remains the outer authentication/organization boundary. R2D ACL is evaluated after workspace membership and controls which teams, projects, issues and derived resources a workspace member may discover or manipulate.

## Why this replaces the previous cross-workspace plan

The previous design treated each workspace as a team/privacy boundary and added project grants across workspaces. That is not the target deployment model.

The desired deployment is one company workspace containing R&D/System, Game Development, Software and future teams. Users must be able to collaborate across selected projects without receiving blanket visibility into every team's work.

Keeping all collaborators in one workspace also avoids bypassing or duplicating upstream `RequireWorkspaceMember` routing semantics. R2D only adds authorization inside an already-authorized workspace.

## Non-goals

V1 does not:

- share projects between different workspaces;
- add arbitrary per-issue user ACLs;
- introduce explicit deny rules;
- replace upstream workspace owner/admin semantics;
- expose repository credentials, agent secrets or other secrets to read-only observers;
- require upstream project-permission PR `multica-ai/multica#7540`;
- rewrite upstream project, issue, user or workspace tables.

## Core semantics

### Workspace membership is necessary but not sufficient

A user must first be an active member of the workspace. Workspace membership alone does not imply visibility of every restricted team/project once R2D ACL is enabled for that resource.

### Teams are first-class R2D principals

Multica currently has no stable team abstraction suitable for the required ACL model, so R2D owns the team model.

A team is scoped to exactly one workspace and has active members. Team membership role is:

```text
member
leader
```

A user may belong to more than one team.

### Projects have an owning team and visibility

A project keeps its upstream `workspace_id`. R2D adds an optional owning team and visibility mode through a side table.

Visibility modes:

| Visibility | Effective default access |
|---|---|
| `workspace` | Existing upstream-compatible behavior; active workspace members can access the project subject to upstream rules |
| `team` | Owning-team members receive implicit project access; explicit grants may add other users/teams |
| `restricted` | Only workspace owner/admin, global observer for read-only access, and explicit project grants can access |

No R2D project row means `visibility=workspace`. This preserves existing projects and avoids a destructive backfill.

For new deployments, the UI may default newly-created projects to `team` after the team model is configured, but that is a rollout policy rather than a migration-side behavior change.

### Projectless issues are team-scoped

A project issue inherits the project's effective ACL and owning team.

A projectless issue may carry an R2D `team_id`. When present, visibility is limited to that team plus workspace owner/admin and the global observer. This provides the required "issues limited by Project or Team" behavior without adding arbitrary issue-level grants.

A legacy projectless issue without an R2D scope row retains upstream workspace visibility until explicitly classified. This avoids making existing data disappear immediately after enabling the feature.

New projectless issues created after rollout SHOULD be assigned a team from the active UI/agent context when possible.

### Cross-team collaboration happens through project grants

A project owned by one team may grant another team or individual user access without exposing unrelated work.

Example:

```text
Company Workspace
├─ R&D/System Team
│  ├─ private/team project A
│  └─ Internal Portal project
│      ├─ R&D/System          implicit owner-team access
│      └─ Software Team       explicit `member` grant
├─ Software Team
│  └─ private/team project B
└─ Game Development Team
   └─ unrelated projects     invisible to the above users
```

## Roles

### Workspace roles

Upstream workspace owner/admin semantics remain authoritative for administration.

R2D additionally supports a workspace-scoped read-only role:

```text
global_observer
```

A global observer may view all teams, projects, issues and normal project-derived activity in the workspace, but may not mutate them merely because of that role. Sensitive secrets/credentials remain governed by their existing security controls and are not exposed by observer access.

This role is intended for Technical Manager / management / audit visibility without granting full administrative mutation rights.

### Team roles

| Team role | Effective policy |
|---|---|
| `member` | Normal participation in team-visible resources |
| `leader` | Member rights plus implicit project-manager rights for projects owned by that team |

Team leadership is organizational authority, not workspace administration.

### Project roles

Project grants support:

| Project role | Rights |
|---|---|
| `viewer` | Read project and permitted project-scoped resources |
| `member` | Viewer rights plus normal issue/comment/resource participation |
| `manager` | Member rights plus project settings and ACL/grant management |

A "project leader" is represented by an explicit `manager` project grant. No separate project-leader table is required in V1.

### Effective-role ordering

When more than one rule applies, use the strongest effective role:

```text
viewer < member < manager
```

Policy precedence is conceptual rather than handler-specific:

1. inactive/non-member workspace user -> no access;
2. workspace owner/admin -> existing administrative access;
3. global observer -> read-only visibility across ACL scopes;
4. explicit project user/team grant;
5. owning-team implicit access (`leader => manager`, `member => member`);
6. `workspace` visibility -> normal upstream-compatible access;
7. otherwise -> no access.

V1 has no explicit deny rule. Revocation means removing the grant or team membership that produced access.

## Operation matrix

The central authorization service must encode operations rather than only answering a generic boolean.

| Operation | viewer | member | manager | team leader on owned project | global observer |
|---|---:|---:|---:|---:|---:|
| View project/issues/comments | yes | yes | yes | yes | yes |
| Create/comment on project issue | no | yes | yes | yes | no |
| Edit/move normal project issue | no | yes | yes | yes | no |
| Change project settings | no | no | yes | yes | no |
| Grant/revoke project ACL | no | no | yes | yes | no |
| Delete project | no | no | yes* | yes* | no |

`*` Project deletion remains subject to existing upstream admin/ownership safeguards in addition to R2D role checks.

Handlers and frontend controls must not reimplement this matrix independently.

## R2D storage model

All custom state follows `docs/r2d-extension-architecture.md`: side tables only, no custom columns in upstream-owned tables, no DB foreign keys from R2D tables into upstream-owned tables by default.

### `r2d_teams`

Conceptual schema:

```text
id
workspace_id
name
slug
created_by
created_at
updated_at
```

Required constraints/indexes:

- unique `(workspace_id, slug)`;
- workspace -> teams lookup;
- backend validates referenced upstream workspace/user objects.

### `r2d_team_members`

```text
team_id
user_id
role              member | leader
created_by
created_at
updated_at
```

Required constraints/indexes:

- unique `(team_id, user_id)`;
- team -> users;
- user -> teams.

Disabled/deleted/non-workspace users produce no effective membership even if an audit row remains.

### `r2d_workspace_roles`

```text
workspace_id
user_id
role              global_observer
created_by
created_at
updated_at
```

Required constraint:

- unique `(workspace_id, user_id, role)`.

### `r2d_project_extra`

```text
project_id
owner_team_id      nullable for legacy/workspace-visible projects
visibility         workspace | team | restricted
created_at
updated_at
```

No row means `visibility=workspace` and `owner_team_id=NULL`.

`team` visibility requires a valid active `owner_team_id` in the same workspace as the project.

### `r2d_project_grants`

```text
id
project_id
principal_type     user | team
principal_id
role               viewer | member | manager
created_by
created_at
updated_at
```

Required constraints/indexes:

- unique `(project_id, principal_type, principal_id)`;
- project -> grants;
- principal -> granted projects;
- project + principal -> effective role.

A team grant is valid only when the team belongs to the same workspace as the project. Cross-workspace grants are rejected in V1.

### `r2d_issue_extra`

```text
issue_id
team_id             nullable; used for projectless issue scope
created_at
updated_at
```

Rules:

- if `issue.project_id` is set, project ACL wins and `r2d_issue_extra.team_id` is ignored for authorization;
- if projectless and `team_id` is set, team ACL applies;
- if projectless and no row/team is set, retain legacy workspace-visible behavior;
- moving an issue out of a project must either select a destination team scope or intentionally preserve legacy workspace scope according to the API/UI action.

## Central authorization service

Authorization must live in one R2D service/repository boundary.

Suggested API shape:

```go
Can(ctx, subject, operation, resource)
CanAccessTeam(ctx, subject, teamID)
EffectiveProjectRole(ctx, subject, projectID)
VisibleProjectIDs(ctx, subject)
VisibleIssueScope(ctx, subject)
CanManageProject(ctx, subject, projectID)
```

Exact names should follow repository conventions.

The service must support batched/list authorization and SQL/query predicates for hot paths. Do not fetch ACL rows per project/issue in loops.

## Query and leakage enforcement

Backend enforcement is mandatory. UI hiding is not authorization.

Audit and enforce at least:

- team list/get/member management;
- project list/get/direct-ID access;
- project create/update/settings/delete;
- project grant list/create/update/revoke;
- issue list/get/direct-ID access;
- issue create/update/move/project assignment;
- projectless issue team assignment;
- global/workspace search;
- dashboards/counts/activity/analytics;
- assignee, team and project pickers;
- comments and attachments;
- notifications/inbox;
- realtime/websocket subscriptions and events;
- agents/squads and project-scoped execution;
- repositories/VCS resources;
- knowledge/resources;
- export/reporting;
- API and MCP/tool endpoints.

Negative tests must prove that an unauthorized user cannot infer hidden project existence through counts, identifiers, search hits, autocomplete, notifications or realtime events.

## Agent and squad policy

Human ACL and agent execution scope must follow the same project/team boundary.

V1 target:

- agents are owned/scoped by a team for discovery and default context;
- squads are reusable team-owned orchestration groups;
- agents/squads may be explicitly assigned to projects;
- a project-assigned agent executes with project scope and must not gain unrelated team/workspace context merely because its daemon/runtime belongs to the workspace;
- sharing a project with another team does not expose unrelated agent memory, chat, credentials or projectless work;
- agent code/review roles remain separate operational concerns from ACL roles.

This hybrid model avoids duplicating an agent per project while keeping team ownership and project assignment explicit.

## Rollout compatibility

The ACL layer must be feature-gated until query enforcement is complete enough to avoid partial security semantics.

Compatibility rules:

- feature disabled -> upstream behavior unchanged;
- feature enabled, project has no R2D row -> existing workspace-visible behavior;
- legacy projectless issue without scope -> existing workspace-visible behavior;
- restricted/team projects become private only after explicit configuration;
- revoking a grant/team membership must take effect on the next authorization check;
- no ACL decision may be cached past membership/grant changes without explicit invalidation.

## Implementation plan

Implementation is split into replayable patches to minimize upstream conflict.

### P00 — Correct specification and extension guardrails

Deliverables:

- this document becomes the authoritative ACL product spec;
- previous cross-workspace sharing plan is removed/superseded;
- tracker #12 records the single-workspace target and patch order;
- reserved R2D migration range remains `900000-999999`;
- confirm feature-flag/config location.

Acceptance:

- no runtime behavior change;
- target deployment and role semantics are unambiguous.

### P01 — Team and ACL schema

Deliverables:

- first R2D migration in the reserved range;
- `r2d_teams`;
- `r2d_team_members`;
- `r2d_workspace_roles`;
- `r2d_project_extra`;
- `r2d_project_grants`;
- `r2d_issue_extra`;
- required uniqueness/query indexes;
- matching down migration and migration tests.

Acceptance:

- fresh and existing databases migrate successfully;
- rollback removes only R2D-owned schema;
- no upstream table is altered;
- no data backfill is required to preserve existing behavior.

### P02 — Central authorization service

Deliverables:

- team/membership repositories;
- effective project-role evaluation;
- projectless issue team-scope evaluation;
- global observer semantics;
- batched visible-project/issue-scope queries;
- table-driven policy tests.

Acceptance:

- all role precedence is tested outside HTTP handlers;
- no handler contains ad-hoc grant logic;
- list evaluation avoids N+1 ACL queries.

### P03 — Team and ACL management APIs

Deliverables:

- team CRUD and membership management;
- global observer assignment/removal restricted to proper admins;
- project owner-team/visibility management;
- project grant list/create/update/revoke;
- minimal pickers/directories limited to the current workspace.

Acceptance:

- cross-workspace principals cannot be granted;
- team leaders can manage ACL only for projects owned by their team unless stronger upstream authority applies;
- project managers can manage grants for their projects;
- normal users cannot escalate themselves.

### P04 — Core query enforcement

Integrate central authorization into:

- project list/get;
- issue list/get;
- issue create/update/move;
- project assignment;
- projectless team scope;
- search;
- dashboards/counts/activity.

Acceptance:

- users see only workspace-visible resources plus resources reachable through team/project ACL;
- no hidden metadata leaks through direct ID/search/aggregate paths;
- legacy unscoped data keeps compatibility semantics.

### P05 — Web UI

Deliverables:

- Team management UI;
- Team Leader/member management;
- project owning-team and visibility controls;
- Project Leader/manager, member and viewer grants;
- cross-team project sharing UI;
- global observer management for authorized admins;
- permission-aware project/issue navigation and pickers.

Acceptance:

- one workspace is sufficient for all teams;
- a user can collaborate on a granted project without seeing unrelated projects from either team;
- UI controls mirror backend policy but never replace backend checks.

### P06 — Agents and squads

Deliverables:

- team ownership/scope for agents/squads;
- explicit project assignment;
- project-scoped agent execution authorization;
- negative tests for memory/resource/context leakage.

### P07 — Secondary surfaces and hardening

Audit and enforce:

- notifications/realtime;
- comments/attachments;
- repositories/VCS;
- knowledge/resources;
- API/MCP;
- export/reporting;
- orphan reconciliation for R2D logical references.

Acceptance:

- authorization coverage checklist is complete;
- grant/team-membership revocation propagates correctly;
- negative leakage tests exist for every sensitive surface.

## Minimum test matrix

Principals:

```text
workspace owner/admin
workspace normal member with no team
team member
team leader
project viewer
project member
project manager
member of second team granted to project
global observer
revoked project user
removed team member
disabled/inactive user
```

Resources:

```text
workspace-visible project
team-owned project
restricted project
cross-team shared project
unrelated project
project issue
team-scoped projectless issue
legacy unscoped projectless issue
search/aggregate result
project-scoped agent/resource
unrelated team agent/resource
```

Tests must cover positive access, write denial and absence of metadata leakage.

## Upstream/backport strategy

This remains an ordered R2D patch stack over `multica-ai/multica`.

Before each ACL patch and upstream sync:

1. inspect upstream changes to workspace/project/issue/auth/search/realtime code;
2. inspect `multica-ai/multica#7540` or any successor;
3. reuse upstream capability only when it genuinely matches this single-workspace Team/Project ACL model;
4. keep R2D state in `r2d_*` side tables and edits to upstream files as small integration hooks;
5. update tracker #12 with upstream cursor, migration cursor, active patches and conflicts/manual ports.

Upstream PR #7540 is a reference/watch item, not a dependency. Its current project-permission design must not be adopted automatically if it conflicts with the R2D role/scope model.

## Definition of done

V1 is done when:

- all company teams can operate inside one Multica workspace;
- team-private work remains private from unrelated normal users;
- selected projects can be shared with other teams/users;
- Team Leader and Project Leader authority is enforced centrally;
- a global observer can view all normal team/project work without receiving mutation/admin power;
- project issues inherit project ACL;
- projectless issues can be scoped to a team;
- search, dashboards, realtime, notifications, API/MCP and direct-ID loads cannot leak hidden metadata;
- agent/squad execution respects the same team/project boundaries;
- no upstream-owned table schema is modified;
- R2D migrations stay in the reserved range;
- tracker #12 accurately records the patch stack and upstream cursor.
