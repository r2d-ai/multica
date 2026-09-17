import { queryOptions } from "@tanstack/react-query";
import { getApi } from "../api";
import type { R2DProjectPrincipal } from "./r2d-sharing";

type InternalApiTransport = {
  fetch<T>(path: string, init?: RequestInit): Promise<T>;
};

/**
 * R2D-only transport shim, mirroring `r2d-sharing.ts`: the upstream ApiClient
 * keeps its request primitive private, and the runtime method is available on
 * the singleton.
 */
function request<T>(path: string, init?: RequestInit): Promise<T> {
  const client = getApi() as unknown as InternalApiTransport;
  return client.fetch<T>(path, init);
}

/**
 * The assignee roster for one Project. Unlike the manager-only sharing
 * directory this is contributor-readable, and it is the only source that names
 * a collaborator whose home Workspace is not the Project owner's — the
 * Workspace member list cannot see them.
 *
 * The server returns members by default; Agent/Squad inventory is served only
 * to a human member of the owner Workspace and is not requested here.
 */
export async function getProjectAssignableActors(
  projectId: string,
): Promise<R2DProjectPrincipal[]> {
  const body = await request<{ actors?: R2DProjectPrincipal[] }>(
    `/api/projects/${encodeURIComponent(projectId)}/assignable-actors`,
  );
  return body.actors ?? [];
}

export const r2dAssignableActorKeys = {
  members: (projectId: string) =>
    ["r2d", "project-assignable-actors", projectId] as const,
};

/**
 * Query options for a Project's member roster. A null/absent Project returns a
 * permanently disabled query so a surface without a Project keeps the active
 * Workspace's member list without firing a request.
 */
export function projectAssignableActorsOptions(projectId: string | null | undefined) {
  if (!projectId) {
    return {
      queryKey: r2dAssignableActorKeys.members(""),
      queryFn: () => Promise.resolve([] as R2DProjectPrincipal[]),
      enabled: false,
    };
  }
  return queryOptions({
    queryKey: r2dAssignableActorKeys.members(projectId),
    queryFn: () => getProjectAssignableActors(projectId),
    retry: false,
    staleTime: 30_000,
  });
}
