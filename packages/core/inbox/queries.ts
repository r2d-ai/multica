import { queryOptions, useQuery } from "@tanstack/react-query";
import { api } from "../api";
import type { InboxItem, InboxWorkspaceUnread } from "../types";

export const inboxKeys = {
  all: (wsId: string) => ["inbox", wsId] as const,
  list: (wsId: string) => [...inboxKeys.all(wsId), "list"] as const,
  archived: (wsId: string) => [...inboxKeys.all(wsId), "archived"] as const,
  // Account-level (not workspace-scoped): a single shared cache entry that
  // holds unread counts for every workspace the user belongs to.
  unreadSummary: () => ["inbox", "unread-summary"] as const,
};

export function inboxListOptions(wsId: string) {
  return queryOptions({
    queryKey: inboxKeys.list(wsId),
    queryFn: () => api.listInbox(),
  });
}

/**
 * Archived notifications, backing the inbox's "Archived" sub-view. A separate
 * cache entry from the main list rather than one flat cache split locally
 * (which is what chat does): the archive grows without end, so it is fetched
 * from its own capped endpoint, and the server — not the client — decides
 * which issues belong in which list.
 */
export function archivedInboxListOptions(wsId: string) {
  return queryOptions({
    queryKey: inboxKeys.archived(wsId),
    queryFn: () => api.listArchivedInbox(),
  });
}

/**
 * Cross-workspace unread inbox summary. One cache entry shared across all
 * workspaces — the data is account-level, so switching workspaces does not
 * refetch it; only the derived "is this for another workspace" view changes.
 */
export function inboxUnreadSummaryOptions() {
  return queryOptions({
    queryKey: inboxKeys.unreadSummary(),
    queryFn: () => api.getInboxUnreadSummary(),
  });
}

/**
 * Whether any workspace OTHER than `currentWsId` has unread inbox items.
 * Drives the workspace-switcher dot: the active workspace's own unread is
 * already surfaced by the Inbox nav count, so it is excluded here to avoid a
 * duplicate signal.
 */
export function hasOtherWorkspaceUnread(
  summary: InboxWorkspaceUnread[],
  currentWsId: string | null | undefined,
): boolean {
  return summary.some((s) => s.workspace_id !== currentWsId && s.count > 0);
}

/**
 * Set of workspace ids that have unread inbox items. Lets the workspace
 * switcher dropdown mark WHICH workspace a pending message lives in (the
 * aggregate switcher dot only says "somewhere else"). Workspaces with a zero
 * count are excluded.
 */
export function unreadWorkspaceIds(summary: InboxWorkspaceUnread[]): Set<string> {
  return new Set(summary.filter((s) => s.count > 0).map((s) => s.workspace_id));
}

/**
 * Unread inbox count for one workspace within the cross-workspace summary.
 * A workspace with nothing unread is absent from the response entirely, so a
 * missing entry means zero rather than "not loaded yet".
 */
export function unreadCountForWorkspace(
  summary: InboxWorkspaceUnread[],
  wsId: string | null | undefined,
): number {
  if (!wsId) return 0;
  return summary.find((s) => s.workspace_id === wsId)?.count ?? 0;
}

/**
 * Unread inbox count for one workspace — the number the sidebar nav badge and
 * the desktop dock badge render.
 *
 * Workspace-scoped on purpose. The count must equal the rows the Inbox list
 * shows for the ACTIVE workspace, and that list is recipient-scoped
 * server-side: a Project-grant notification from another Workspace is shown
 * while any of the recipient's workspaces is active. The account-level
 * `unread-summary` cannot serve that — it attributes each row to the Workspace
 * that owns the issue, so a collaborator who is not a member there reads 0
 * forever (MUL-6967 follow-up). `GET /api/inbox/unread-count` is
 * recipient-scoped and applies the same newest-per-issue dedup as the list.
 *
 * The query key sits under `inboxKeys.all(wsId)`, so `onInboxInvalidate`
 * refreshes it with the list. `unread-summary` stays for the workspace-switcher
 * dot, which needs the per-workspace breakdown.
 */
export function inboxUnreadCountOptions(wsId: string) {
  return queryOptions({
    queryKey: [...inboxKeys.all(wsId), "unread-count"] as const,
    queryFn: async () => (await api.getUnreadInboxCount()).count,
  });
}

export function useInboxUnreadCount(wsId: string | null | undefined): number {
  const { data } = useQuery({
    ...inboxUnreadCountOptions(wsId ?? ""),
    enabled: !!wsId,
  });
  return data ?? 0;
}

/**
 * Deduplicate inbox items by issue_id (one entry per issue, Linear-style).
 * Exported for consumers to use in useMemo — not in queryOptions select
 * (to avoid new array references on every cache update).
 */
export function deduplicateInboxItems(items: InboxItem[]): InboxItem[] {
  return groupInboxItemsByIssue(items.filter((i) => !i.archived));
}

/**
 * Same grouping for the archived sub-view. The `archived` filter is what makes
 * an optimistic unarchive drop the row out of the archived list immediately —
 * exactly mirroring how `deduplicateInboxItems`' filter drops an optimistically
 * archived row out of the main list.
 */
export function deduplicateArchivedInboxItems(items: InboxItem[]): InboxItem[] {
  return groupInboxItemsByIssue(items.filter((i) => i.archived));
}

function groupInboxItemsByIssue(items: InboxItem[]): InboxItem[] {
  const groups = new Map<string, InboxItem[]>();
  for (const item of items) {
    const key = item.issue_id ?? item.id;
    const group = groups.get(key) ?? [];
    group.push(item);
    groups.set(key, group);
  }
  const merged: InboxItem[] = [];
  for (const group of groups.values()) {
    group.sort(
      (a, b) =>
        new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
    );
    const newest = group[0];
    if (!newest) continue;

    const commentId =
      newest.details?.comment_id ??
      group.find((item) => item.details?.comment_id)?.details?.comment_id;

    if (commentId && newest.details?.comment_id !== commentId) {
      merged.push({
        ...newest,
        details: { ...(newest.details ?? {}), comment_id: commentId },
      });
      continue;
    }

    merged.push(newest);
  }
  return merged.sort(
    (a, b) =>
      new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
  );
}
