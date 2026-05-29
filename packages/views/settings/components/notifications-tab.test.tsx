import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/notification-preferences/queries", () => ({
  notificationPreferenceOptions: () => ({ queryKey: ["notification-preferences", "ws-1"] }),
}));

const mockMutate = vi.fn();

vi.mock("@multica/core/notification-preferences/mutations", () => ({
  useUpdateNotificationPreferences: () => ({
    mutate: mockMutate,
    isPending: false,
  }),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQuery: () => ({
      data: { preferences: {} },
    }),
  };
});

vi.mock("sonner", () => ({
  toast: { error: vi.fn() },
}));

import { NotificationsTab } from "./notifications-tab";

describe("NotificationsTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders inbox notification toggles", () => {
    render(<NotificationsTab />);
    expect(screen.getByText("Inbox Notifications")).toBeInTheDocument();
  });

  it("updates notification preferences when a toggle changes", async () => {
    const user = userEvent.setup();
    render(<NotificationsTab />);

    const [firstSwitch] = screen.getAllByRole("switch");
    if (!firstSwitch) {
      throw new Error("expected at least one notification toggle");
    }
    await user.click(firstSwitch);

    expect(mockMutate).toHaveBeenCalled();
  });
});
