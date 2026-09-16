import { describe, expect, it } from "vitest";
import type { Workspace } from "@multica/core/types";
import { resolveProjectHostPath } from "./r2d-project-host";

describe("resolveProjectHostPath", () => {
  it("hosts a shared Project under a Workspace the recipient belongs to", () => {
    const workspaces = [
      { id: "recipient-ws", slug: "recipient-team" },
      { id: "other-ws", slug: "other-team" },
    ] as Workspace[];

    expect(resolveProjectHostPath(workspaces, "project-123")).toBe(
      "/recipient-team/projects/project-123",
    );
  });

  it("does not need owner Workspace metadata and falls back to workspace creation", () => {
    expect(resolveProjectHostPath([], "project-123")).toBe("/workspaces/new");
  });
});
