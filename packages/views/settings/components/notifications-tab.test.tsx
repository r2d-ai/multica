import type { ReactNode } from "react";
import { describe, expect, it, beforeEach, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

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

const TEST_RESOURCES = {
  en: { common: enCommon, settings: enSettings },
};

function I18nWrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

describe("NotificationsTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders inbox notification toggles", () => {
    render(<NotificationsTab />, { wrapper: I18nWrapper });
    expect(screen.getByText("Inbox Notifications")).toBeInTheDocument();
  });

  it("updates notification preferences when a toggle changes", async () => {
    const user = userEvent.setup();
    render(<NotificationsTab />, { wrapper: I18nWrapper });

    const [firstSwitch] = screen.getAllByRole("switch");
    if (!firstSwitch) {
      throw new Error("expected at least one notification toggle");
    }
    await user.click(firstSwitch);

    expect(mockMutate).toHaveBeenCalled();
  });
});
