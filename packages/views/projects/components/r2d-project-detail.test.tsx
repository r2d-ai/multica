import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
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
}));

vi.mock("@multica/core/realtime", () => ({
  useProjectRealtimeScope: mocks.useProjectRealtimeScope,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: () => mocks.capabilities,
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
  ProjectDetail: () => <div data-testid="upstream-detail" />,
}));

vi.mock("./r2d-safe-project-detail", () => ({
  R2DSafeProjectDetail: () => <div data-testid="safe-detail" />,
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
