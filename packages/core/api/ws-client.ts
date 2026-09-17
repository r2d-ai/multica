import type {
  WSMessage,
  WSEventType,
  WSSubscriptionScope,
  WSSubscriptionResult,
} from "../types/events";
import { type Logger, noopLogger } from "../logger";

type EventHandler = (payload: unknown, actorId?: string, actorType?: string) => void;
type SubscriptionResultHandler = (result: WSSubscriptionResult) => void;

/** Desired scope subscription, ref-counted across local subscribers. */
interface ScopeSubscription {
  scope: WSSubscriptionScope;
  id: string;
  refs: number;
}

/** Key used to deduplicate desired subscriptions of the same scope + id. */
function scopeSubscriptionKey(scope: string, id: string): string {
  return `${scope}:${id}`;
}

// Cap how much of an unparseable frame we put into the log. A malformed or
// rogue server can stream arbitrarily large garbage, and the warn handler may
// be a console / IPC bridge whose buffers we don't want to blow.
const UNPARSEABLE_LOG_MAX_CHARS = 200;

// Reconnect backoff parameters. A flat delay causes a thundering herd when many
// clients reconnect after a server restart; exponential backoff with jitter
// spreads the reconnection attempts over time. The client retries indefinitely
// (capped at RECONNECT_MAX_DELAY_MS) because the web/desktop UI does not yet
// expose a visible disconnected state or manual retry action.
const RECONNECT_BASE_DELAY_MS = 1_000;
const RECONNECT_MAX_DELAY_MS = 30_000;

function summarizeUnparseable(data: unknown): string {
  const text = typeof data === "string" ? data : String(data);
  if (text.length <= UNPARSEABLE_LOG_MAX_CHARS) return text;
  return `${text.slice(0, UNPARSEABLE_LOG_MAX_CHARS)}… (truncated, ${text.length} chars total)`;
}

/** Identifies the WS client to the server. Sent as `client_platform`,
 *  `client_version`, and `client_os` query parameters on the upgrade URL —
 *  browsers cannot set custom headers on WebSocket handshakes, so query
 *  params are the only portable channel. */
export interface WSClientIdentity {
  platform?: string;
  version?: string;
  os?: string;
}

export class WSClient {
  private ws: WebSocket | null = null;
  private baseUrl: string;
  private token: string | null = null;
  private workspaceSlug: string | null = null;
  private cookieAuth = false;
  private identity: WSClientIdentity | undefined;
  private handlers = new Map<WSEventType, Set<EventHandler>>();
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempt = 0;
  private hasConnectedBefore = false;
  // One-shot per connection. A non-conforming frame can repeat hundreds of
  // times per session, so we log the first drop and suppress the rest. Reset
  // on each connect() so a fresh connection logs once again.
  private badFrameLogged = false;
  private onReconnectCallbacks = new Set<() => void>();
  private anyHandlers = new Set<(msg: WSMessage) => void>();
  // Explicit scope subscriptions (task / chat / project). Desired state is
  // kept here — not on the server — so a reconnect can replay it: a new socket
  // starts with only the auto-joined workspace/user rooms, and every explicit
  // room must be re-joined. Ref-counted so two mounted surfaces that need the
  // same Project room send one frame and unsubscribe only when both leave.
  private subscriptions = new Map<string, ScopeSubscription>();
  private subscriptionResultHandlers = new Set<SubscriptionResultHandler>();
  // True once the connection is authenticated (cookie mode: on open; token
  // mode: on auth_ack). Subscribe frames are withheld until then — the server
  // gates them on the connection identity, which is only established after the
  // auth exchange.
  private authenticated = false;
  private logger: Logger;

  constructor(
    url: string,
    options?: {
      logger?: Logger;
      cookieAuth?: boolean;
      identity?: WSClientIdentity;
    },
  ) {
    this.baseUrl = url;
    this.logger = options?.logger ?? noopLogger;
    this.cookieAuth = options?.cookieAuth ?? false;
    this.identity = options?.identity;
  }

  setAuth(token: string | null, workspaceSlug: string) {
    this.token = token;
    this.workspaceSlug = workspaceSlug;
  }

  connect() {
    this.badFrameLogged = false;
    this.authenticated = false;
    const url = new URL(this.baseUrl);
    // Token is never sent as a URL query parameter — it would be logged by
    // proxies, CDNs, and browser history.  In cookie mode the HttpOnly cookie
    // is sent automatically with the upgrade request.  In token mode the token
    // is delivered as the first WebSocket message after the connection opens.
    if (this.workspaceSlug)
      url.searchParams.set("workspace_slug", this.workspaceSlug);
    if (this.identity?.platform)
      url.searchParams.set("client_platform", this.identity.platform);
    if (this.identity?.version)
      url.searchParams.set("client_version", this.identity.version);
    if (this.identity?.os)
      url.searchParams.set("client_os", this.identity.os);

    this.ws = new WebSocket(url.toString());

    this.ws.onopen = () => {
      if (!this.cookieAuth && this.token) {
        this.ws!.send(
          JSON.stringify({ type: "auth", payload: { token: this.token } }),
        );
        return;
      }

      this.onAuthenticated();
    };

    this.ws.onmessage = (event) => {
      let msg: WSMessage;
      try {
        msg = JSON.parse(event.data as string) as WSMessage;
      } catch {
        this.logger.warn(
          "ws: received unparseable message",
          summarizeUnparseable(event.data),
        );
        return;
      }
      // Trust boundary: a frame must be an object carrying a string `type`.
      // The server protocol guarantees this for every frame, but a
      // non-conforming frame — an out-of-protocol frame injected by a proxy /
      // browser extension, or a bare JSON primitive — must degrade to a no-op
      // here. Without this guard every downstream consumer (the onAny
      // dispatcher and every ws.on subscriber) runs against a bad shape;
      // `msg.type.split(...)` in the realtime sync threw an uncaught TypeError
      // out of onmessage and surfaced as a flood of global `$exception` events
      // (MUL-3418). Validate once at the boundary, trust the shape downstream.
      if (!msg || typeof (msg as { type?: unknown }).type !== "string") {
        if (!this.badFrameLogged) {
          this.badFrameLogged = true;
          this.logger.warn(
            "ws: dropping frame without a string type",
            summarizeUnparseable(event.data),
          );
        }
        return;
      }
      if ((msg as any).type === "auth_ack") {
        this.onAuthenticated();
        return;
      }
      // Subscription control frames are transport-level, not business events;
      // intercept them before the generic event dispatch so they never reach
      // `onAny` / prefix invalidation handlers.
      const frameType = (msg as { type: string }).type;
      if (frameType === "subscribe_ack" || frameType === "subscribe_error") {
        this.handleSubscriptionResult(
          frameType === "subscribe_ack",
          (msg as { payload?: unknown }).payload,
        );
        return;
      }
      this.logger.debug("received", msg.type);
      const eventHandlers = this.handlers.get(msg.type);
      if (eventHandlers) {
        for (const handler of eventHandlers) {
          handler(msg.payload, msg.actor_id, msg.actor_type);
        }
      }
      for (const handler of this.anyHandlers) {
        handler(msg);
      }
    };

    this.ws.onclose = () => {
      this.authenticated = false;
      this.scheduleReconnect();
    };

    this.ws.onerror = () => {
      // Suppress — onclose handles reconnect; errors during StrictMode
      // double-fire are expected in dev and harmless.
    };
  }

  /**
   * Schedule a reconnection attempt with exponential backoff and jitter.
   * Retries indefinitely with a capped delay because the web/desktop UI
   * does not yet expose a visible disconnected state or manual retry action.
   */
  private scheduleReconnect() {
    const base = Math.min(
      RECONNECT_BASE_DELAY_MS * 2 ** this.reconnectAttempt,
      RECONNECT_MAX_DELAY_MS,
    );
    // ±20 % jitter so clients that disconnected at the same time don't
    // reconnect in lockstep.
    const jitter = base * 0.2 * (Math.random() * 2 - 1);
    const delay = Math.round(
      Math.min(base + jitter, RECONNECT_MAX_DELAY_MS),
    );

    this.reconnectAttempt++;
    this.logger.warn(
      `ws: disconnected, reconnecting in ${delay}ms (attempt ${this.reconnectAttempt})`,
    );
    this.reconnectTimer = setTimeout(() => this.connect(), delay);
  }

  private onAuthenticated() {
    this.logger.info("connected");
    this.authenticated = true;
    const recoveredConnection = this.hasConnectedBefore || this.reconnectAttempt > 0;
    this.reconnectAttempt = 0;
    if (recoveredConnection) {
      for (const cb of this.onReconnectCallbacks) {
        try {
          cb();
        } catch {
          // ignore reconnect callback errors
        }
      }
    }
    this.hasConnectedBefore = true;
    // A fresh socket only has the auto-joined workspace/user rooms; replay
    // every desired scope so task / chat / project rooms are rejoined after a
    // reconnect. The server re-runs the ACL check per join and answers a fresh
    // ack / error, so a revoked grant is never silently re-admitted.
    this.replaySubscriptions();
  }

  /** Re-send a `subscribe` frame for every locally desired scope. Safe to
   *  call when nothing is subscribed (no-op). */
  private replaySubscriptions() {
    for (const { scope, id } of this.subscriptions.values()) {
      this.sendScopeFrame("subscribe", scope, id);
    }
  }

  /** Send a subscribe / unsubscribe frame if the socket is open and the
   *  connection has completed its auth exchange. */
  private sendScopeFrame(
    kind: "subscribe" | "unsubscribe",
    scope: WSSubscriptionScope,
    id: string,
  ) {
    if (!this.authenticated) return;
    const ws = this.ws;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ type: kind, payload: { scope, id } }));
  }

  private handleSubscriptionResult(ok: boolean, payload: unknown) {
    const raw = (payload ?? {}) as {
      scope?: unknown;
      id?: unknown;
      error?: unknown;
    };
    const scope = typeof raw.scope === "string" ? raw.scope : "";
    const id = typeof raw.id === "string" ? raw.id : "";
    const error = typeof raw.error === "string" ? raw.error : undefined;
    if (ok) {
      this.logger.debug(`ws: subscribed ${scope}:${id}`);
    } else {
      this.logger.warn(`ws: subscribe ${scope}:${id} rejected`, error ?? "unknown");
    }
    const result: WSSubscriptionResult = {
      scope,
      id,
      ok,
      ...(error ? { error } : {}),
    };
    for (const handler of this.subscriptionResultHandlers) {
      try {
        handler(result);
      } catch {
        // ignore subscription result handler errors
      }
    }
  }

  /**
   * Register local interest in a scope and join its room.
   *
   * Ref-counted: repeated calls for the same scope + id join once, and only
   * the last release sends an `unsubscribe` frame. The desired subscription is
   * remembered so `connect()` / reconnect replays it. Returns an idempotent
   * release function.
   */
  subscribeScope(scope: WSSubscriptionScope, id: string): () => void {
    if (!id) return () => {};
    const key = scopeSubscriptionKey(scope, id);
    const existing = this.subscriptions.get(key);
    if (existing) {
      existing.refs += 1;
      return () => this.releaseScope(key);
    }
    this.subscriptions.set(key, { scope, id, refs: 1 });
    // No-op until the socket is open and authenticated; `onAuthenticated`
    // replays the desired set, so subscribing before connect still joins.
    this.sendScopeFrame("subscribe", scope, id);
    return () => this.releaseScope(key);
  }

  private releaseScope(key: string) {
    const entry = this.subscriptions.get(key);
    if (!entry) return;
    entry.refs -= 1;
    if (entry.refs > 0) return;
    this.subscriptions.delete(key);
    this.sendScopeFrame("unsubscribe", entry.scope, entry.id);
  }

  /** Observe the outcome of every explicit subscribe frame. Returns an
   *  unsubscribe function. Used by tests and for surfacing denials. */
  onSubscriptionResult(handler: SubscriptionResultHandler) {
    this.subscriptionResultHandlers.add(handler);
    return () => {
      this.subscriptionResultHandlers.delete(handler);
    };
  }

  disconnect() {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.ws) {
      // Remove handlers before close to prevent onclose from scheduling a reconnect
      this.ws.onclose = null;
      this.ws.onerror = null;
      this.ws.close();
      this.ws = null;
    }
    this.hasConnectedBefore = false;
    this.reconnectAttempt = 0;
    this.authenticated = false;
    this.handlers.clear();
    this.anyHandlers.clear();
    this.onReconnectCallbacks.clear();
    this.subscriptionResultHandlers.clear();
    // Explicit teardown (sign-out / workspace switch) drops desired rooms; a
    // reconnect is handled by `onclose` -> `scheduleReconnect`, which keeps
    // them for replay. A still-mounted surface re-subscribes through its hook
    // once the provider builds the replacement client.
    this.subscriptions.clear();
  }

  on(event: WSEventType, handler: EventHandler) {
    if (!this.handlers.has(event)) {
      this.handlers.set(event, new Set());
    }
    this.handlers.get(event)!.add(handler);
    return () => {
      this.handlers.get(event)?.delete(handler);
    };
  }

  onAny(handler: (msg: WSMessage) => void) {
    this.anyHandlers.add(handler);
    return () => {
      this.anyHandlers.delete(handler);
    };
  }

  onReconnect(callback: () => void) {
    this.onReconnectCallbacks.add(callback);
    return () => {
      this.onReconnectCallbacks.delete(callback);
    };
  }

  send(message: WSMessage) {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify(message));
    }
  }
}
