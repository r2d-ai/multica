import { describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { R2DProjectDetail } from "./r2d-project-detail";

const mocks = vi.hoisted(() => ({
  useProjectRealtimeScope: vi.fn(),
  capabilities: {
    data: {
      project_id: "p-1",
      read: true,
      contribute: false,
      manage: false,
      share: false,
      view_resources: false,
      global_observer: false,
    },
    isLoading: false,
    isError: false,
  },
  sharing: {
    data: undefined as { project_id: string; visibility: string; grants: unknown[] } | undefined,
    isLoading: false,
    isError: false,
  },
}));

vi.mock("@multica/core/realtime", () => ({
  useProjectRealtimeScope: mocks.useProjectRealtimeScope,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options?: { queryKey?: readonly unknown[] }) =>
    options?.queryKey?.[0] === "r2d-sharing-test" ? mocks.sharing : mocks.capabilities,
}));

vi.mock("@multica/core/projects/r2d-capabilities", () => ({
  projectCapabilitiesOptions: () => ({
    queryKey: ["r2d-project-capabilities-test"],
    queryFn: async () => ({}),
  }),
}));

vi.mock("@multica/core/projects/r2d-sharing", () => ({
  projectSharingOptions: () => ({ queryKey: ["r2d-sharing-test"] }),
}));

vi.mock("./project-detail", () => ({
  ProjectDetail: ({ toolbarActions }: { toolbarActions?: React.ReactNode }) => (
    <div data-testid="upstream-detail">{toolbarActions}</div>
  ),
}));

vi.mock("./r2d-safe-project-detail", () => ({
  R2DSafeProjectDetail: () => <div data-testid="safe-detail" />,
}));

vi.mock("./r2d-project-sharing", () => ({
  ProjectSharingDialog: () => <div data-testid="sharing-dialog" />,
}));

describe("R2DProjectDetail project realtime scope", () => {
  it("joins the mounted project's realtime scope", () => {
    render(<R2DProjectDetail projectId="p-1" />);

    // Mounted before the read gate on purpose: an unauthorized viewer must
    // still attempt the join so the server can answer with subscribe_error.
    expect(mocks.useProjectRealtimeScope).toHaveBeenCalledWith("p-1");
    expect(screen.getByTestId("safe-detail")).toBeTruthy();
  });

  it("still attempts the join when the viewer is not authorized", () => {
    mocks.capabilities.data.read = false;
    try {
      render(<R2DProjectDetail projectId="p-2" />);

      expect(mocks.useProjectRealtimeScope).toHaveBeenCalledWith("p-2");
      expect(screen.getByText("Project unavailable.")).toBeTruthy();
    } finally {
      mocks.capabilities.data.read = true;
    }
  });
});

describe("R2DProjectDetail toolbar share affordance", () => {
  // `full` detail mode is what the upstream toolbar belongs to, so the manager
  // cases must also carry manage + resource visibility.
  function withFullDetail() {
    mocks.capabilities.data.manage = true;
    mocks.capabilities.data.view_resources = true;
  }

  function reset() {
    mocks.capabilities.data.manage = false;
    mocks.capabilities.data.view_resources = false;
    mocks.capabilities.data.share = false;
    mocks.sharing.data = undefined;
    mocks.sharing.isError = false;
  }

  it("injects the Share control into the upstream toolbar for a project manager", () => {
    withFullDetail();
    mocks.capabilities.data.share = true;
    mocks.sharing.data = { project_id: "p-1", visibility: "workspace", grants: [] };
    try {
      render(<R2DProjectDetail projectId="p-1" />);

      const toolbar = screen.getByTestId("upstream-detail");
      expect(within(toolbar).getByRole("button", { name: "Share" })).toBeTruthy();
    } finally {
      reset();
    }
  });

  it("renders no Share control without the share capability", () => {
    withFullDetail();
    mocks.sharing.data = { project_id: "p-1", visibility: "workspace", grants: [] };
    try {
      render(<R2DProjectDetail projectId="p-1" />);

      expect(within(screen.getByTestId("upstream-detail")).queryByRole("button", { name: "Share" })).toBeNull();
    } finally {
      reset();
    }
  });

  it("renders no Share control when the sharing read fails", () => {
    withFullDetail();
    mocks.capabilities.data.share = true;
    mocks.sharing.isError = true;
    try {
      render(<R2DProjectDetail projectId="p-1" />);

      expect(within(screen.getByTestId("upstream-detail")).queryByRole("button", { name: "Share" })).toBeNull();
    } finally {
      reset();
    }
  });
});
