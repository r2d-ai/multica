/**
 * @vitest-environment jsdom
 */
import { renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  const release = vi.fn();
  const subscribeScope = vi.fn(() => release);
  return { release, subscribeScope };
});

// The provider owns the real socket; these tests pin the lifecycle contract the
// hook exposes to a Project surface (join while mounted, release on unmount /
// id change, no join without an id) without standing up a WS connection.
vi.mock("./provider", () => ({
  useWS: () => ({
    subscribe: vi.fn(() => () => {}),
    onReconnect: vi.fn(() => () => {}),
    subscribeScope: mocks.subscribeScope,
    onSubscriptionResult: vi.fn(() => () => {}),
  }),
}));

import { useProjectRealtimeScope, useWSSubscription } from "./hooks";

beforeEach(() => {
  mocks.release.mockClear();
  mocks.subscribeScope.mockReset();
  mocks.subscribeScope.mockReturnValue(mocks.release);
});

describe("useWSSubscription", () => {
  it("joins the scope while mounted and releases on unmount", () => {
    const { unmount } = renderHook(() =>
      useWSSubscription("project", "p-1"),
    );

    expect(mocks.subscribeScope).toHaveBeenCalledWith("project", "p-1");
    expect(mocks.release).not.toHaveBeenCalled();

    unmount();
    expect(mocks.release).toHaveBeenCalledTimes(1);
  });

  it("does not join until an id is available", () => {
    const { rerender } = renderHook(
      ({ id }: { id: string | null }) => useWSSubscription("project", id),
      { initialProps: { id: null as string | null } },
    );

    expect(mocks.subscribeScope).not.toHaveBeenCalled();

    rerender({ id: "p-1" });
    expect(mocks.subscribeScope).toHaveBeenCalledWith("project", "p-1");
  });

  it("moves the subscription when the project id changes", () => {
    const { rerender } = renderHook(
      ({ id }: { id: string }) => useProjectRealtimeScope(id),
      { initialProps: { id: "p-1" } },
    );

    expect(mocks.subscribeScope).toHaveBeenCalledWith("project", "p-1");

    rerender({ id: "p-2" });

    // The old room is released before the new one is joined.
    expect(mocks.release).toHaveBeenCalledTimes(1);
    expect(mocks.subscribeScope).toHaveBeenCalledWith("project", "p-2");
  });
});
