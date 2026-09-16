import { paths } from "@multica/core/paths";
import type { NavigationAdapter } from "./types";

/**
 * Project pages live under the viewer's active Workspace so the normal app
 * shell keeps working, but a copied link must not carry the owner Workspace
 * slug: a cross-Workspace grantee may not belong to it. `/projects/{id}` is a
 * canonical hand-off URL; the web proxy re-homes it into a Workspace the
 * recipient actually belongs to before rendering Project detail.
 */
function canonicalSharePathname(pathname: string): string {
  const match = pathname.match(/^\/[^/]+\/projects\/([^/]+)\/?$/);
  if (!match?.[1]) return pathname;
  try {
    return paths.projectShare(decodeURIComponent(match[1]));
  } catch {
    return pathname;
  }
}

/**
 * Rebuild the adapter's location as a single in-app path —
 * `/pathname?search#fragment` — the form `getShareableUrl()` expects.
 *
 * Every caller that turns "where the user is" into a link must go through
 * here. Composing only `pathname` + `searchParams` silently drops the
 * fragment, which downgrades a `#comment-…` deep link to the whole issue.
 *
 * Project detail is the one R2D exception: its workspace-scoped viewing URL is
 * canonicalized to `/projects/{id}` so sharing a Project never implies that the
 * recipient joined the owner's Workspace.
 */
export function currentPath(
  navigation: Pick<NavigationAdapter, "pathname" | "searchParams" | "hash">,
): string {
  const search = navigation.searchParams.toString();
  const pathname = canonicalSharePathname(navigation.pathname);
  return `${pathname}${search ? `?${search}` : ""}${navigation.hash}`;
}
