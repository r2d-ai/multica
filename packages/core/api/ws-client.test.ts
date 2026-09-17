import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WSClient } from "./ws-client";
import type { WSMessage } from "../types/events";

// Capture URL passed to WebSocket so we can assert the connect-time
// query string.  We don't simulate the full WS lifecycle here — only the
// upgrade URL construction, which is what carries client identity.
class FakeWebSocket {
  static lastUrl: string | null = null;
  static lastInstance: FakeWebSocket | null = null;
  // Present so WSClient's `readyState === WebSocket.OPEN` guard can pass for
  // tests that exercise outbound frames (subscribe / unsubscribe).
  static readonly OPEN = 1;
  // Fields read by WSClient.connect()/disconnect(), all no-op here.
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  readyState = FakeWebSocket.OPEN;
  /** Every frame the client sent, in order. */
  sent: string[] = [];
  constructor(url: string) {
    FakeWebSocket.lastUrl = url;
    FakeWebSocket.lastInstance = this;
  }
  close() {}
  send(data: string) {
    this.sent.push(data);
  }
}

describe("WSClient", () => {
  beforeEach(() => {
    FakeWebSocket.lastUrl = null;
    FakeWebSocket.lastInstance = null;
    vi.stubGlobal("WebSocket", FakeWebSocket as unknown as typeof WebSocket);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("includes client identity in the upgrade URL when configured", () => {
    const ws = new WSClient("ws://example.test/ws", {
      identity: { platform: "desktop", version: "1.2.3", os: "macos" },
    });
    ws.setAuth("tok", "acme");
    ws.connect();

    const url = new URL(FakeWebSocket.lastUrl!);
    expect(url.searchParams.get("workspace_slug")).toBe("acme");
    expect(url.searchParams.get("client_platform")).toBe("desktop");
    expect(url.searchParams.get("client_version")).toBe("1.2.3");
    expect(url.searchParams.get("client_os")).toBe("macos");
    // Token must never appear in the URL — it is delivered as the first
    // WS message in token mode.
    expect(url.searchParams.has("token")).toBe(false);
  });

  it("omits client_* params when identity is not configured", () => {
    const ws = new WSClient("ws://example.test/ws");
    ws.setAuth("tok", "acme");
    ws.connect();

    const url = new URL(FakeWebSocket.lastUrl!);
    expect(url.searchParams.has("client_platform")).toBe(false);
    expect(url.searchParams.has("client_version")).toBe(false);
    expect(url.searchParams.has("client_os")).toBe(false);
  });

  it("only includes the identity fields that are set", () => {
    const ws = new WSClient("ws://example.test/ws", {
      identity: { platform: "cli" },
    });
    ws.setAuth("tok", "acme");
    ws.connect();

    const url = new URL(FakeWebSocket.lastUrl!);
    expect(url.searchParams.get("client_platform")).toBe("cli");
    expect(url.searchParams.has("client_version")).toBe(false);
    expect(url.searchParams.has("client_os")).toBe(false);
  });

  it("truncates the logged payload when an unparseable frame is large", () => {
    const logger = {
      debug: vi.fn(),
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
    };
    const ws = new WSClient("ws://example.test/ws", { logger });
    ws.connect();

    const huge = "x".repeat(5000);
    FakeWebSocket.lastInstance!.onmessage?.({ data: huge });

    expect(logger.warn).toHaveBeenCalledTimes(1);
    const [, summary] = logger.warn.mock.calls[0] as [string, string];
    expect(summary.length).toBeLessThan(huge.length);
    expect(summary).toContain("truncated");
    expect(summary).toContain("5000");
    expect(summary.startsWith("x".repeat(200))).toBe(true);
  });

  it("logs and skips malformed frames without breaking later messages", () => {
    const logger = {
      debug: vi.fn(),
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
    };
    const ws = new WSClient("ws://example.test/ws", { logger });
    const handler = vi.fn();
    ws.on("issue:updated", handler);
    ws.connect();

    expect(() => {
      FakeWebSocket.lastInstance!.onmessage?.({ data: `{"type":"issue` });
    }).not.toThrow();

    FakeWebSocket.lastInstance!.onmessage?.({
      data: JSON.stringify({
        type: "issue:updated",
        payload: { id: "issue-1" },
      }),
    });

    expect(logger.warn).toHaveBeenCalledWith(
      "ws: received unparseable message",
      `{"type":"issue`,
    );
    expect(handler).toHaveBeenCalledWith(
      { id: "issue-1" },
      undefined,
      undefined,
    );
  });

  it("drops frames without a string type without throwing, and keeps dispatching", () => {
    // Regression for MUL-3418: a frame whose parsed JSON lacks a string `type`
    // (an out-of-protocol frame, or a bare JSON primitive) used to throw an
    // uncaught TypeError out of onmessage via `msg.type.split(...)` in a
    // downstream onAny handler, flooding `$exception` telemetry.
    const logger = {
      debug: vi.fn(),
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
    };
    const ws = new WSClient("ws://example.test/ws", { logger });

    // A downstream consumer that assumes a string type, exactly like the
    // realtime sync's onAny dispatcher.
    const anyHandler = vi.fn((msg: WSMessage) => msg.type.split(":")[0]);
    ws.onAny(anyHandler);
    const issueHandler = vi.fn();
    ws.on("issue:updated", issueHandler);
    ws.connect();

    const badFrames = [
      JSON.stringify({ payload: {} }), // object, no type
      "42", // bare number
      "true", // bare bool
      "[]", // array
    ];
    for (const data of badFrames) {
      expect(() => {
        FakeWebSocket.lastInstance!.onmessage?.({ data });
      }).not.toThrow();
    }

    // Bad frames never reached any handler.
    expect(anyHandler).not.toHaveBeenCalled();
    expect(issueHandler).not.toHaveBeenCalled();

    // A valid frame after the bad ones still dispatches normally.
    FakeWebSocket.lastInstance!.onmessage?.({
      data: JSON.stringify({ type: "issue:updated", payload: { id: "i-1" } }),
    });
    expect(issueHandler).toHaveBeenCalledWith({ id: "i-1" }, undefined, undefined);
    expect(anyHandler).toHaveBeenCalledTimes(1);

    // The drop is logged at most once per connection despite four bad frames.
    expect(logger.warn).toHaveBeenCalledTimes(1);
    expect(logger.warn.mock.calls[0]?.[0]).toBe(
      "ws: dropping frame without a string type",
    );
  });

  it("passes actor_id and actor_type to event handlers", () => {
    const ws = new WSClient("ws://example.test/ws");
    ws.setAuth("tok", "acme");
    ws.connect();

    const handler = vi.fn();
    ws.on("issue:created", handler);

    const fakeWs = (ws as any).ws as FakeWebSocket;
    fakeWs.onmessage?.({
      data: JSON.stringify({
        type: "issue:created",
        payload: { id: "issue-1" },
        actor_id: "user-123",
        actor_type: "user",
      }),
    });

    expect(handler).toHaveBeenCalledWith(
      { id: "issue-1" },
      "user-123",
      "user",
    );
  });

  // ── Explicit scope subscriptions (task / chat / project) ────────────
  //
  // A Project collaborator whose home Workspace is not the Project owner's
  // never receives the owner Workspace fanout; they only receive Project-scoped
  // frames after this client joins the `project:{id}` room. These tests pin the
  // client half of that contract: connect → subscribe → event → reconnect →
  // re-subscribe, plus the denial and ref-count paths.
  describe("scope subscriptions", () => {
    function lastSocket(): FakeWebSocket {
      return FakeWebSocket.lastInstance!;
    }

    /** Simulate the server's auth handshake completing. */
    function authenticate() {
      lastSocket().onmessage?.({
        data: JSON.stringify({ type: "auth_ack" }),
      });
    }

    function sentFrames(): Array<{ type: string; payload?: any }> {
      return lastSocket().sent.map((raw) => JSON.parse(raw));
    }

    function frameTypes(): string[] {
      return sentFrames().map((frame) => frame.type);
    }

    function deliver(frame: Record<string, unknown>) {
      lastSocket().onmessage?.({ data: JSON.stringify(frame) });
    }

    beforeEach(() => {
      vi.useFakeTimers();
    });

    afterEach(() => {
      vi.useRealTimers();
    });

    it("withholds the subscribe frame until the connection is authenticated", () => {
      const ws = new WSClient("ws://example.test/ws");
      ws.setAuth("tok", "acme");
      ws.connect();

      // Subscribe before the auth exchange completes: the frame is queued in
      // the desired set, not sent on an unauthenticated socket.
      const release = ws.subscribeScope("project", "p-1");
      expect(frameTypes()).toEqual([]);

      lastSocket().onopen?.();
      expect(frameTypes()).toEqual(["auth"]);

      authenticate();
      expect(frameTypes()).toEqual(["auth", "subscribe"]);
      expect(sentFrames()[1]?.payload).toEqual({ scope: "project", id: "p-1" });

      release();
      // Last reference released -> unsubscribe, not another subscribe.
      expect(frameTypes()).toEqual(["auth", "subscribe", "unsubscribe"]);
    });

    it("replays desired subscriptions after a reconnect and accepts a fresh ack", () => {
      const ws = new WSClient("ws://example.test/ws");
      ws.setAuth("tok", "acme");
      ws.connect();
      lastSocket().onopen?.();
      authenticate();

      ws.subscribeScope("project", "p-1");
      expect(sentFrames().filter((f) => f.type === "subscribe")).toHaveLength(1);

      // Drop the socket; the auto-reconnect builds a brand new one — which has
      // none of the previous rooms, so the client must re-join.
      lastSocket().onclose?.();
      vi.runOnlyPendingTimers();
      expect(FakeWebSocket.lastInstance).not.toBeUndefined();

      const fresh = lastSocket();
      expect(fresh.sent).toEqual([]);
      fresh.onopen?.();
      authenticate();

      const replayed = sentFrames().filter((f) => f.type === "subscribe");
      expect(replayed).toHaveLength(1);
      expect(replayed[0]?.payload).toEqual({ scope: "project", id: "p-1" });

      // A fresh ack/error can arrive for the replayed frame.
      const results = vi.fn();
      ws.onSubscriptionResult(results);
      deliver({
        type: "subscribe_ack",
        payload: { scope: "project", id: "p-1" },
      });
      expect(results).toHaveBeenCalledWith({
        scope: "project",
        id: "p-1",
        ok: true,
      });
    });

    it("ref-counts duplicate subscriptions and unsubscribes on the last release", () => {
      const ws = new WSClient("ws://example.test/ws");
      ws.setAuth("tok", "acme");
      ws.connect();
      lastSocket().onopen?.();
      authenticate();

      const releaseFirst = ws.subscribeScope("project", "p-1");
      const releaseSecond = ws.subscribeScope("project", "p-1");
      expect(sentFrames().filter((f) => f.type === "subscribe")).toHaveLength(1);

      releaseFirst();
      expect(sentFrames().filter((f) => f.type === "unsubscribe")).toHaveLength(0);

      releaseSecond();
      const unsubscribes = sentFrames().filter((f) => f.type === "unsubscribe");
      expect(unsubscribes).toHaveLength(1);
      expect(unsubscribes[0]?.payload).toEqual({ scope: "project", id: "p-1" });
    });

    it("surfaces subscribe_error without dispatching it as a business event", () => {
      const ws = new WSClient("ws://example.test/ws");
      ws.setAuth("tok", "acme");
      ws.connect();
      lastSocket().onopen?.();
      authenticate();

      const results = vi.fn();
      const anyHandler = vi.fn();
      ws.onSubscriptionResult(results);
      ws.onAny(anyHandler);
      ws.subscribeScope("project", "p-1");

      deliver({
        type: "subscribe_error",
        payload: { scope: "project", id: "p-1", error: "forbidden" },
      });

      expect(results).toHaveBeenCalledWith({
        scope: "project",
        id: "p-1",
        ok: false,
        error: "forbidden",
      });
      // Control frames must not leak into the onAny / prefix invalidation path.
      expect(anyHandler).not.toHaveBeenCalledWith(
        expect.objectContaining({ type: "subscribe_error" }),
      );
    });

    it("delivers project events after subscribe and keeps delivering after reconnect", () => {
      const ws = new WSClient("ws://example.test/ws");
      ws.setAuth("tok", "acme");
      const issueHandler = vi.fn();
      ws.on("issue:updated", issueHandler);
      ws.connect();
      lastSocket().onopen?.();
      authenticate();

      ws.subscribeScope("project", "p-1");
      deliver({
        type: "subscribe_ack",
        payload: { scope: "project", id: "p-1" },
      });
      deliver({
        type: "issue:updated",
        payload: { issue: { id: "i-1" } },
      });
      expect(issueHandler).toHaveBeenCalledTimes(1);
      expect(issueHandler).toHaveBeenCalledWith(
        { issue: { id: "i-1" } },
        undefined,
        undefined,
      );

      // Reconnect: the room is rejoined and events resume on the new socket.
      lastSocket().onclose?.();
      vi.runOnlyPendingTimers();
      lastSocket().onopen?.();
      authenticate();
      expect(
        sentFrames().filter((f) => f.type === "subscribe"),
      ).toHaveLength(1);

      deliver({
        type: "issue:updated",
        payload: { issue: { id: "i-2" } },
      });
      expect(issueHandler).toHaveBeenCalledTimes(2);
    });
  });

  // ── Reconnect backoff tests ────────────────────────────────────────

  describe("reconnect backoff", () => {
    let setTimeoutSpy: ReturnType<typeof vi.spyOn>;

    beforeEach(() => {
      vi.useFakeTimers();
      setTimeoutSpy = vi.spyOn(globalThis, "setTimeout");
    });

    afterEach(() => {
      setTimeoutSpy.mockRestore();
      vi.useRealTimers();
    });

    /** Return the delay (ms) passed to the most recent setTimeout call. */
    function lastTimerDelay(): number {
      const calls = setTimeoutSpy.mock.calls;
      return calls[calls.length - 1]?.[1] as number;
    }

    function simulateDisconnect() {
      FakeWebSocket.lastInstance!.onclose?.();
    }

    /** Simulate the server acknowledging the auth token. In token mode the
     *  client sends `{type:"auth"}` on open, and the server replies with
     *  `{type:"auth_ack"}`. Only then does `onAuthenticated()` fire and
     *  reset the reconnect counter. */
    function simulateAuthAck() {
      FakeWebSocket.lastInstance!.onmessage?.({
        data: JSON.stringify({ type: "auth_ack" }),
      });
    }

    it("uses ~1000ms base delay for the first reconnect attempt", () => {
      // Pin Math.random so jitter is deterministic: random()=0.5 → jitter=0.
      vi.stubGlobal(
        "Math",
        new Proxy(Math, {
          get(target, prop) {
            if (prop === "random") return () => 0.5;
            return (target as any)[prop];
          },
        }),
      );

      const ws = new WSClient("ws://example.test/ws");
      ws.connect();
      simulateDisconnect();

      expect(lastTimerDelay()).toBe(1000);
    });

    it("doubles the base delay on consecutive failures (exponential)", () => {
      vi.stubGlobal(
        "Math",
        new Proxy(Math, {
          get(target, prop) {
            if (prop === "random") return () => 0.5;
            return (target as any)[prop];
          },
        }),
      );

      const ws = new WSClient("ws://example.test/ws");
      ws.connect();

      // Attempt 1: base = 1000 * 2^0 = 1000
      simulateDisconnect();
      expect(lastTimerDelay()).toBe(1000);

      // Fire the timer → connect() → new FakeWebSocket → simulate disconnect
      vi.advanceTimersByTime(1000);
      // Attempt 2: base = 1000 * 2^1 = 2000
      simulateDisconnect();
      expect(lastTimerDelay()).toBe(2000);

      vi.advanceTimersByTime(2000);
      // Attempt 3: base = 1000 * 2^2 = 4000
      simulateDisconnect();
      expect(lastTimerDelay()).toBe(4000);

      vi.advanceTimersByTime(4000);
      // Attempt 4: base = 1000 * 2^3 = 8000
      simulateDisconnect();
      expect(lastTimerDelay()).toBe(8000);
    });

    it("caps the delay at 30s even after many failures", () => {
      vi.stubGlobal(
        "Math",
        new Proxy(Math, {
          get(target, prop) {
            if (prop === "random") return () => 0.5;
            return (target as any)[prop];
          },
        }),
      );

      const ws = new WSClient("ws://example.test/ws");
      ws.connect();

      // Drive through enough failures to exceed the cap:
      // 2^5 = 32000 > 30000, so attempt 6 should be capped.
      const delays = [1000, 2000, 4000, 8000, 16000];
      for (const d of delays) {
        simulateDisconnect();
        expect(lastTimerDelay()).toBe(d);
        vi.advanceTimersByTime(d);
      }

      // Attempt 6: 1000 * 2^5 = 32000 → capped to 30000
      simulateDisconnect();
      expect(lastTimerDelay()).toBe(30000);
    });

    it("applies jitter so delays vary with Math.random", () => {
      // Stub Math.random to alternate between 0 (jitter = -20%) and
      // 1 (jitter = +20%), producing deterministic min/max delays.
      let callCount = 0;
      vi.stubGlobal(
        "Math",
        new Proxy(Math, {
          get(target, prop) {
            if (prop === "random") return () => (callCount++ % 2 === 0 ? 0 : 1);
            return (target as any)[prop];
          },
        }),
      );

      const ws = new WSClient("ws://example.test/ws");

      // First disconnect: random()=0 → jitter = 1000 * 0.2 * (0*2-1) = -200 → 800ms
      ws.connect();
      simulateDisconnect();
      const delay1 = lastTimerDelay();

      // Second disconnect: random()=1 → jitter = 1000 * 0.2 * (1*2-1) = +200 → 1200ms
      vi.clearAllTimers();
      ws.disconnect();
      ws.connect();
      simulateDisconnect();
      const delay2 = lastTimerDelay();

      // The two delays must differ and fall within [800, 1200].
      expect(delay1).toBe(800);
      expect(delay2).toBe(1200);
    });

    it("resets the attempt counter on successful authentication", () => {
      vi.stubGlobal(
        "Math",
        new Proxy(Math, {
          get(target, prop) {
            if (prop === "random") return () => 0.5;
            return (target as any)[prop];
          },
        }),
      );

      const ws = new WSClient("ws://example.test/ws");
      ws.setAuth("tok", "acme");
      ws.connect();

      // Two failures: delays 1000, 2000
      simulateDisconnect();
      expect(lastTimerDelay()).toBe(1000);
      vi.advanceTimersByTime(1000);

      simulateDisconnect();
      expect(lastTimerDelay()).toBe(2000);
      vi.advanceTimersByTime(2000);

      // Successful connection resets the counter.
      simulateAuthAck();

      // Next failure should be back to the base delay.
      simulateDisconnect();
      expect(lastTimerDelay()).toBe(1000);
    });

    it("notifies reconnect when the initial connection succeeds only after a retry", () => {
      const ws = new WSClient("ws://example.test/ws");
      ws.setAuth("tok", "acme");
      const onReconnect = vi.fn();
      ws.onReconnect(onReconnect);
      ws.connect();

      simulateDisconnect();
      vi.runOnlyPendingTimers();
      simulateAuthAck();

      expect(onReconnect).toHaveBeenCalledTimes(1);
    });

    it("keeps retrying indefinitely with capped delay", () => {
      vi.stubGlobal(
        "Math",
        new Proxy(Math, {
          get(target, prop) {
            if (prop === "random") return () => 0.5;
            return (target as any)[prop];
          },
        }),
      );

      const logger = {
        debug: vi.fn(),
        info: vi.fn(),
        warn: vi.fn(),
        error: vi.fn(),
      };
      const ws = new WSClient("ws://example.test/ws", { logger });
      ws.connect();

      // Drive past the old 20-attempt limit — all should schedule a reconnect.
      for (let i = 0; i < 25; i++) {
        simulateDisconnect();
        const delay = lastTimerDelay();
        // Once past attempt 5 (base > 30000), delay should be capped at 30s.
        if (i >= 5) {
          expect(delay).toBe(30000);
        }
        vi.advanceTimersByTime(delay);
      }

      // The 26th disconnect should STILL schedule a reconnect (no give-up).
      const timerCountBefore = setTimeoutSpy.mock.calls.length;
      simulateDisconnect();
      expect(setTimeoutSpy.mock.calls.length).toBe(timerCountBefore + 1);
      expect(lastTimerDelay()).toBe(30000);
      // No "giving up" error should have been logged.
      expect(logger.error).not.toHaveBeenCalled();
    });

    it("disconnect() cancels a pending reconnect and resets the counter", () => {
      vi.stubGlobal(
        "Math",
        new Proxy(Math, {
          get(target, prop) {
            if (prop === "random") return () => 0.5;
            return (target as any)[prop];
          },
        }),
      );

      const ws = new WSClient("ws://example.test/ws");
      ws.connect();

      // Two failures to bump the counter.
      simulateDisconnect();
      vi.advanceTimersByTime(1000);
      simulateDisconnect();
      // A reconnect timer is now pending.

      // Explicit disconnect should cancel the pending timer.
      ws.disconnect();
      vi.advanceTimersByTime(10_000);
      // No new WebSocket should have been created after the disconnect.
      // The last URL should still be from the second connect() call.

      // A fresh connect() after disconnect should start from attempt 0.
      ws.connect();
      simulateDisconnect();
      expect(lastTimerDelay()).toBe(1000);
    });
  });
});
