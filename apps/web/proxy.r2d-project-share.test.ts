import { describe, expect, it } from "vitest";
import { NextRequest } from "next/server";
import { MULTICA_LOCALE_HEADER } from "./lib/locale-routing";
import { proxy } from "./proxy";

function request(path: string, cookies: Record<string, string> = {}) {
  const cookie = Object.entries(cookies)
    .map(([key, value]) => `${key}=${value}`)
    .join("; ");
  return new NextRequest(`https://app.multica.test${path}`, {
    headers: cookie ? { cookie } : undefined,
  });
}

describe("R2D canonical Project share links", () => {
  it("re-homes a shared Project into the recipient's last Workspace", () => {
    const response = proxy(
      request("/projects/project-123?view=board", {
        multica_logged_in: "1",
        last_workspace_slug: "recipient-team",
      }),
    );

    expect(response.headers.get("location")).toBe(
      "https://app.multica.test/recipient-team/projects/project-123?view=board",
    );
  });

  it("preserves the canonical Project URL through login", () => {
    const response = proxy(request("/projects/project-123?view=board"));

    expect(response.headers.get("location")).toBe(
      "https://app.multica.test/login?next=%2Fprojects%2Fproject-123%3Fview%3Dboard",
    );
  });

  it("lets an authenticated user without a last-Workspace cookie reach the fallback resolver", () => {
    const response = proxy(
      request("/projects/project-123", { multica_logged_in: "1" }),
    );

    expect(response.headers.get("location")).toBeNull();
    expect(
      response.headers.get(`x-middleware-request-${MULTICA_LOCALE_HEADER}`),
    ).toBe("en");
  });
});
