import React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ProjectSharingDialog, R2DProjectDetail } from "./r2d-project-sharing";

const mocks = vi.hoisted(() => {
  class MockApiError extends Error {
    status: number;

    constructor(status: number) {
      super(`status ${status}`);
      this.status = status;
    }
  }

  return {
    MockApiError,
    sharingResult: {} as Record<string, unknown>,
    directoryResult: {} as Record<string, unknown>,
    setVisibility: vi.fn(),
    createGrant: vi.fn(),
    updateRole: vi.fn(),
    deleteGrant: vi.fn(),
  };
});

vi.mock("@multica/core/api", () => ({
  ApiError: mocks.MockApiError,
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey?: readonly unknown[] }) => {
    if (options.queryKey?.[0] === "r2d-sharing-test") return mocks.sharingResult;
    if (options.queryKey?.[0] === "r2d-directory-test") return mocks.directoryResult;
    return { data: undefined, isLoading: false, isError: false, error: null };
  },
}));

vi.mock("@multica/core/projects/r2d-sharing", () => ({
  projectSharingOptions: () => ({ queryKey: ["r2d-sharing-test"] }),
  projectSharingDirectoryOptions: () => ({ queryKey: ["r2d-directory-test"] }),
  useSetProjectVisibility: () => ({
    mutate: mocks.setVisibility,
    isPending: false,
    error: null,
  }),
  useCreateProjectGrant: () => ({
    mutate: mocks.createGrant,
    isPending: false,
    error: null,
  }),
  useUpdateProjectGrantRole: () => ({
    mutate: mocks.updateRole,
    isPending: false,
    variables: undefined,
    error: null,
  }),
  useDeleteProjectGrant: () => ({
    mutate: mocks.deleteGrant,
    isPending: false,
    variables: undefined,
    error: null,
  }),
}));

vi.mock("./project-detail", () => ({
  ProjectDetail: ({ projectId }: { projectId: string }) => (
    <div data-testid="upstream-project-detail">{projectId}</div>
  ),
}));

const SHARING = {
  project_id: "project-1",
  visibility: "private" as const,
  grants: [
    {
      id: "grant-1",
      project_id: "project-1",
      principal_type: "user" as const,
      principal_id: "user-1",
      role: "member" as const,
      created_by: "manager-1",
      created_at: "2026-09-16T00:00:00Z",
      updated_at: "2026-09-16T00:00:00Z",
      principal: {
        type: "user" as const,
        id: "user-1",
        name: "Existing User",
        secondary: "existing@example.com",
      },
    },
  ],
};

describe("R2D project sharing", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.sharingResult = {
      data: SHARING,
      isLoading: false,
      isError: false,
      error: null,
    };
    mocks.directoryResult = {
      data: [],
      isLoading: false,
      isError: false,
      error: null,
    };
  });

  it("renders management controls only after the manager-only sharing query succeeds", () => {
    const { rerender } = render(<R2DProjectDetail projectId="project-1" />);

    expect(screen.getByRole("button", { name: "Share" })).toBeInTheDocument();

    mocks.sharingResult = {
      data: undefined,
      isLoading: false,
      isError: true,
      error: new mocks.MockApiError(403),
    };
    rerender(<R2DProjectDetail projectId="project-1" />);

    expect(screen.queryByRole("button", { name: "Share" })).not.toBeInTheDocument();
    expect(screen.queryByText("Sharing unavailable")).not.toBeInTheDocument();
  });

  it("updates a grant role inline", async () => {
    const user = userEvent.setup();
    render(
      <ProjectSharingDialog
        projectId="project-1"
        sharing={SHARING}
        open
        onOpenChange={vi.fn()}
      />,
    );

    const roleSelect = screen.getAllByDisplayValue("Member")[0];
    await user.selectOptions(roleSelect, "manager");

    expect(mocks.updateRole).toHaveBeenCalledWith({
      grantId: "grant-1",
      role: "manager",
    });
  });

  it("deletes a grant inline", async () => {
    const user = userEvent.setup();
    render(
      <ProjectSharingDialog
        projectId="project-1"
        sharing={SHARING}
        open
        onOpenChange={vi.fn()}
      />,
    );

    await user.click(screen.getByTitle("Remove access"));
    expect(mocks.deleteGrant).toHaveBeenCalledWith("grant-1");
  });

  it("creates a grant from the bounded principal directory", async () => {
    const user = userEvent.setup();
    const alice = {
      type: "user" as const,
      id: "user-2",
      name: "Alice Example",
      secondary: "alice@example.com",
    };
    mocks.directoryResult = {
      data: [alice],
      isLoading: false,
      isError: false,
      error: null,
    };

    render(
      <ProjectSharingDialog
        projectId="project-1"
        sharing={SHARING}
        open
        onOpenChange={vi.fn()}
      />,
    );

    await user.type(screen.getByPlaceholderText("Search users by name or email"), "al");
    await user.click(screen.getByText("Alice Example"));
    await user.click(screen.getByRole("button", { name: "Grant access" }));

    expect(mocks.createGrant).toHaveBeenCalled();
    expect(mocks.createGrant.mock.calls[0]?.[0]).toEqual({
      principal: alice,
      role: "member",
    });
  });
});
