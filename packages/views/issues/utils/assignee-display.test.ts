import { describe, expect, it } from "vitest";
import { issueAssigneeDisplay } from "./assignee-display";

describe("issueAssigneeDisplay", () => {
  it("prefers the payload name over the local resolver", () => {
    const got = issueAssigneeDisplay(
      { assignee_type: "member", assignee_id: "u1", assignee_name: "Foreign User", assignee_avatar_url: "https://cdn/a.png" },
      () => "Unknown",
    );
    expect(got).toEqual({ name: "Foreign User", avatarUrl: "https://cdn/a.png" });
  });

  it("falls back to the resolver when the payload omits the name", () => {
    const got = issueAssigneeDisplay(
      { assignee_type: "member", assignee_id: "u1" },
      () => "Local Name",
    );
    expect(got).toEqual({ name: "Local Name", avatarUrl: null });
  });

  it("reports an unassigned issue", () => {
    const got = issueAssigneeDisplay({ assignee_type: null, assignee_id: null }, () => "Unknown");
    expect(got.name).toBe("");
  });
});
