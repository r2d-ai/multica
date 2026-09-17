// The member section's source depends on whether the surface knows its
// Project. Without one it is the active Workspace's member list; with one it is
// the Project's assignable roster, which is the only source that names a
// collaborator from another Workspace. Regression: the picker always read the
// active Workspace, so an owner could never assign a shared Project's foreign
// collaborator.
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import enIssues from "../../../locales/en/issues.json";
import { AssigneePicker } from "./assignee-picker";

const WORKSPACE_MEMBERS = [{ user_id: "user-1", name: "Ada Lovelace", role: "member" }];
const PROJECT_MEMBERS = [
  { type: "member", id: "user-1", name: "Ada Lovelace" },
  { type: "member", id: "user-2", name: "Foreign Collaborator" },
];
const AGENTS = [
  { id: "agent-1", name: "Owner Agent", archived_at: null, visibility: "workspace", owner_id: "user-1" },
];
const SQUADS = [{ id: "squad-1", name: "Owner Squad", archived_at: null, leader_id: "agent-1" }];

const capabilities = vi.hoisted(() => ({ viewResources: true }));

vi.mock("@tanstack/react-query", () => ({
  useQuery: ({ queryKey }: { queryKey: unknown[] }) => {
    if (queryKey[0] === "members") return { data: WORKSPACE_MEMBERS };
    if (queryKey[0] === "r2d" && queryKey[1] === "project-assignable-actors") {
      return { data: PROJECT_MEMBERS };
    }
    if (queryKey[0] === "r2d" && queryKey[1] === "project-capabilities") {
      return { data: { read: true, contribute: true, manage: false, share: false, view_resources: capabilities.viewResources } };
    }
    if (queryKey[0] === "agents") return { data: AGENTS };
    if (queryKey[0] === "squads") return { data: SQUADS };
    return { data: [] };
  },
}));

vi.mock("@multica/core/hooks", () => ({ useWorkspaceId: () => "workspace-1" }));
vi.mock("@multica/core/auth", () => ({ useAuthStore: () => ({ id: "user-1" }) }));
vi.mock("@multica/core/agents", () => ({ isAgentRuntimeBound: () => true }));
vi.mock("@multica/core/permissions", () => ({
  canAssignAgentToIssue: () => ({ allowed: true }),
}));
vi.mock("@multica/core/workspace/hooks", () => ({
  useActorName: () => ({ getActorName: () => "Ada Lovelace" }),
}));
vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members"] }),
  agentListOptions: () => ({ queryKey: ["agents"] }),
  squadListOptions: () => ({ queryKey: ["squads"] }),
  assigneeFrequencyOptions: () => ({ queryKey: ["frequency"] }),
}));
vi.mock("@multica/core/projects/r2d-assignable-actors", () => ({
  projectAssignableActorsOptions: (projectId: string) => ({
    queryKey: ["r2d", "project-assignable-actors", projectId],
  }),
}));
vi.mock("@multica/core/projects/r2d-capabilities", () => ({
  projectCapabilitiesOptions: (projectId: string) => ({
    queryKey: ["r2d", "project-capabilities", projectId],
  }),
}));
vi.mock("../../../common/actor-avatar", () => ({
  ActorAvatar: () => <span data-testid="actor-avatar" />,
}));

function renderPicker(projectId?: string) {
  return render(
    <I18nProvider locale="en" resources={{ en: { issues: enIssues } }}>
      <AssigneePicker
        assigneeType={null}
        assigneeId={null}
        projectId={projectId}
        onUpdate={() => {}}
        open
        onOpenChange={() => {}}
      />
    </I18nProvider>,
  );
}

describe("AssigneePicker member roster", () => {
  it("lists the Project's assignable members when the surface has a Project", () => {
    renderPicker("project-1");

    expect(screen.getByText("Foreign Collaborator")).toBeTruthy();
  });

  it("falls back to the active Workspace's members without a Project", () => {
    renderPicker(undefined);

    expect(screen.getByText("Ada Lovelace")).toBeTruthy();
    expect(screen.queryByText("Foreign Collaborator")).toBeNull();
  });
});

describe("AssigneePicker Agent/Squad inventory boundary", () => {
  // Regression: the picker offered the active Workspace's Agents and Squads to
  // a foreign Project collaborator, and the server rejected every one of them
  // with 403. `view_resources` is the server-owned "human member of the owner
  // Workspace" signal, so it decides whether that inventory is offered at all.
  it("hides Agent/Squad sections from a foreign Project collaborator", () => {
    capabilities.viewResources = false;
    try {
      renderPicker("project-1");

      expect(screen.queryByText("Owner Agent")).toBeNull();
      expect(screen.queryByText("Owner Squad")).toBeNull();
    } finally {
      capabilities.viewResources = true;
    }
  });

  it("keeps Agent/Squad sections for an owner-Workspace member", () => {
    capabilities.viewResources = true;
    renderPicker("project-1");

    expect(screen.getByText("Owner Agent")).toBeTruthy();
    expect(screen.getByText("Owner Squad")).toBeTruthy();
  });

  it("keeps Agent/Squad sections on a surface with no Project", () => {
    capabilities.viewResources = false;
    renderPicker(undefined);

    expect(screen.getByText("Owner Agent")).toBeTruthy();
  });
});
