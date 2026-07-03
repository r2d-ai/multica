// @vitest-environment jsdom

import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiError } from "@multica/core/api";
import { configStore } from "@multica/core/config";
import { COMPOSIO_MCP_APPS_FLAG } from "@multica/core/feature-flags";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enSettings from "../../locales/en/settings.json";

const {
  mockUpdateWorkspace,
  mockSetQueryData,
  mockToastSuccess,
  mockToastError,
} = vi.hoisted(() => ({
  mockUpdateWorkspace: vi.fn(),
  mockSetQueryData: vi.fn(),
  mockToastSuccess: vi.fn(),
  mockToastError: vi.fn(),
}));

const composioErrorRef = vi.hoisted(() => ({
  current: null as Error | null,
}));
const queryCallsRef = vi.hoisted(() => ({
  current: [] as { queryKey: unknown[]; enabled?: boolean }[],
}));

const workspace = {
  id: "ws-1",
  name: "Workspace",
  slug: "workspace",
  description: null,
  context: null,
  settings: {
    existing: "kept",
    telegram: {
      bot_token: "old-token",
      user_id: "old-user",
    },
  },
  repos: [],
  issue_prefix: "WS",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

vi.mock("@multica/core/auth", () => ({
  useAuthStore: (selector: (state: { user: { id: string } }) => unknown) =>
    selector({ user: { id: "user-1" } }),
}));

vi.mock("@multica/core/hooks", () => ({
  useWorkspaceId: () => "ws-1",
}));

vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => workspace,
}));

vi.mock("@multica/core/workspace/queries", () => ({
  memberListOptions: () => ({ queryKey: ["members", "ws-1"] }),
  workspaceKeys: { list: () => ["workspaces"] },
}));

vi.mock("@multica/core/api", async () => {
  const actual = await vi.importActual<typeof import("@multica/core/api")>("@multica/core/api");
  return {
    ...actual,
    api: {
      updateWorkspace: (...args: unknown[]) => mockUpdateWorkspace(...args),
    },
  };
});

vi.mock("@multica/core/composio", () => ({
  composioToolkitsOptions: () => ({ queryKey: ["composio", "toolkits"] }),
}));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>("@tanstack/react-query");
  return {
    ...actual,
    useQuery: (opts: { queryKey: unknown[]; enabled?: boolean }) => {
      queryCallsRef.current.push(opts);
      if (opts.queryKey[0] === "composio") {
        return {
          data: undefined,
          error: opts.enabled === false ? null : composioErrorRef.current,
          isError: opts.enabled !== false && composioErrorRef.current != null,
        };
      }
      return {
        data: [{ user_id: "user-1", role: "owner" }],
      };
    },
    useQueryClient: () => ({
      setQueryData: mockSetQueryData,
    }),
    queryOptions: <T,>(opts: T) => opts,
  };
});

vi.mock("sonner", () => ({
  toast: {
    success: mockToastSuccess,
    error: mockToastError,
  },
}));

vi.mock("./lark-tab", () => ({
  LarkTab: () => <div data-testid="lark-tab" />,
}));

vi.mock("./composio-tab", () => ({
  ComposioTab: () => <div data-testid="composio-tab" />,
}));

vi.mock("./slack-tab", () => ({
  SlackTab: () => <div data-testid="slack-tab" />,
}));

import { IntegrationsTab } from "./integrations-tab";

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

function renderTab() {
  return render(<IntegrationsTab />, { wrapper: I18nWrapper });
}

function composioQueryCall() {
  return queryCallsRef.current.find((c) => c.queryKey[0] === "composio");
}

describe("IntegrationsTab", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    queryCallsRef.current = [];
    composioErrorRef.current = null;
    mockUpdateWorkspace.mockResolvedValue(workspace);
    configStore.getState().setFeatureFlags({ [COMPOSIO_MCP_APPS_FLAG]: true });
  });

  it("merges Telegram settings into existing workspace settings on save", async () => {
    const user = userEvent.setup();
    renderTab();

    const botTokenInput = screen.getByDisplayValue("old-token");
    const userIdInput = screen.getByDisplayValue("old-user");

    await user.clear(botTokenInput);
    await user.type(botTokenInput, "new-token");
    await user.clear(userIdInput);
    await user.type(userIdInput, "new-user");
    await user.click(screen.getByRole("button", { name: /save/i }));

    expect(mockUpdateWorkspace).toHaveBeenCalledWith("ws-1", {
      settings: {
        existing: "kept",
        telegram: {
          bot_token: "new-token",
          user_id: "new-user",
        },
      },
    });
    expect(mockToastSuccess).toHaveBeenCalled();
    expect(mockSetQueryData).toHaveBeenCalled();
  });

  it("includes notify_reactions false when reaction toggle is off", async () => {
    const user = userEvent.setup();
    renderTab();

    const switches = screen.getAllByRole("switch");
    expect(switches).toHaveLength(4);
    await user.click(switches[3]!);
    await user.click(screen.getByRole("button", { name: /save/i }));

    expect(mockUpdateWorkspace).toHaveBeenCalledWith("ws-1", {
      settings: {
        existing: "kept",
        telegram: {
          bot_token: "old-token",
          user_id: "old-user",
          notify_reactions: false,
        },
      },
    });
  });

  it("includes disabled notification filters when toggles are off", async () => {
    const user = userEvent.setup();
    renderTab();

    const switches = screen.getAllByRole("switch");
    expect(switches).toHaveLength(4);
    await user.click(switches[0]!);
    await user.click(switches[1]!);
    await user.click(switches[2]!);
    await user.click(screen.getByRole("button", { name: /save/i }));

    expect(mockUpdateWorkspace).toHaveBeenCalledWith("ws-1", {
      settings: {
        existing: "kept",
        telegram: {
          bot_token: "old-token",
          user_id: "old-user",
          notify_status_changes: false,
          notify_comments: false,
          notify_agent_activity: false,
        },
      },
    });
  });

  it("hides Composio and disables the toolkits query when the feature flag is off", () => {
    configStore.getState().setFeatureFlags({ [COMPOSIO_MCP_APPS_FLAG]: false });

    renderTab();

    expect(screen.queryByTestId("composio-tab")).toBeNull();
    expect(composioQueryCall()?.enabled).toBe(false);
  });

  it("shows Composio when the feature flag is on and the integration is configured", () => {
    renderTab();

    expect(screen.getByTestId("composio-tab")).toBeInTheDocument();
    expect(composioQueryCall()?.enabled).toBe(true);
  });

  it("hides Composio when the feature flag is on but the server reports 503", () => {
    composioErrorRef.current = new ApiError("unavailable", 503, "Service Unavailable");

    renderTab();

    expect(screen.queryByTestId("composio-tab")).toBeNull();
  });
});
