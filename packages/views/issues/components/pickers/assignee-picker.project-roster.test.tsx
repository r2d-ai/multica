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

vi.mock("@tanstack/react-query", () => ({
  useQuery: ({ queryKey }: { queryKey: unknown[] }) => {
    if (queryKey[0] === "members") return { data: WORKSPACE_MEMBERS };
    if (queryKey[0] === "r2d") return { data: PROJECT_MEMBERS };
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
