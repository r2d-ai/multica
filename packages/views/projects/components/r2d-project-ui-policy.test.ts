import { describe, expect, it } from "vitest";
import type { R2DProjectCapabilities } from "@multica/core/projects/r2d-capabilities";
import { r2dProjectDetailMode } from "./r2d-project-ui-policy";

function caps(overrides: Partial<R2DProjectCapabilities>): R2DProjectCapabilities {
  return {
    project_id: "p1",
    global_observer: false,
    read: true,
    contribute: false,
    manage: false,
    share: false,
    view_resources: false,
    ...overrides,
  };
}

describe("r2dProjectDetailMode", () => {
  it("keeps owner-workspace managers on the full upstream detail", () => {
    expect(r2dProjectDetailMode(caps({ contribute: true, manage: true, share: true, view_resources: true }))).toBe("full");
  });

  it("uses safe manager detail for a manager without owner resource visibility", () => {
    expect(r2dProjectDetailMode(caps({ contribute: true, manage: true, share: true }))).toBe("safe-manage");
  });

  it("keeps member issue contribution controls without project management controls", () => {
    expect(r2dProjectDetailMode(caps({ contribute: true }))).toBe("safe-contribute");
  });

  it("renders viewers and global observers read-only", () => {
    expect(r2dProjectDetailMode(caps({}))).toBe("safe-readonly");
    expect(r2dProjectDetailMode(caps({ global_observer: true }))).toBe("safe-readonly");
  });
});
