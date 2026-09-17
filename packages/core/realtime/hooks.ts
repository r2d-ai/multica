"use client";

import { useEffect } from "react";
import type { WSEventType, WSSubscriptionScope } from "../types";
import { useWS } from "./provider";

type EventHandler = (payload: unknown, actorId?: string, actorType?: string) => void;

/**
 * Hook that subscribes to a WebSocket event and calls the handler.
 * Automatically unsubscribes on cleanup.
 */
export function useWSEvent(event: WSEventType, handler: EventHandler) {
  const { subscribe } = useWS();

  useEffect(() => {
    const unsub = subscribe(event, handler);
    return unsub;
  }, [event, handler, subscribe]);
}

/**
 * Hook that registers a callback to run on WebSocket reconnection.
 * Useful for refetching component-local data after a network interruption.
 */
export function useWSReconnect(callback: () => void) {
  const { onReconnect } = useWS();

  useEffect(() => {
    const unsub = onReconnect(callback);
    return unsub;
  }, [callback, onReconnect]);
}

/**
 * Join a resource-backed WS scope for as long as the owning surface is
 * mounted. A null/empty `id` joins nothing. The provider ref-counts and
 * replays the subscription across reconnects, and releases it on cleanup or
 * when the id changes.
 */
export function useWSSubscription(
  scope: WSSubscriptionScope,
  id: string | null | undefined,
) {
  const { subscribeScope } = useWS();

  useEffect(() => {
    if (!id) return;
    return subscribeScope(scope, id);
  }, [scope, id, subscribeScope]);
}

/**
 * Join the Project's realtime room while a Project surface is mounted.
 *
 * Project-scoped issue / comment events are fanned out to this room in
 * addition to the owner Workspace broadcast, which is what lets a collaborator
 * whose home Workspace is not the Project owner's receive them. The server
 * authorizes the join (and re-authorizes on every delivery), so an
 * unauthorized viewer simply gets a `subscribe_error` and no events.
 */
export function useProjectRealtimeScope(projectId: string | null | undefined) {
  useWSSubscription("project", projectId);
}
