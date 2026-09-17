import { queryOptions } from "@tanstack/react-query";
import { getApi } from "../api";
import type { R2DProjectRole } from "./r2d-sharing";

export interface R2DProjectCapabilities {
  project_id: string;
  role?: R2DProjectRole;
  global_observer: boolean;
  read: boolean;
  contribute: boolean;
  manage: boolean;
  share: boolean;
  view_resources: boolean;
}

type InternalApiTransport = {
  fetch<T>(path: string, init?: RequestInit): Promise<T>;
};

function request<T>(path: string, init?: RequestInit): Promise<T> {
  const client = getApi() as unknown as InternalApiTransport;
  return client.fetch<T>(path, init);
}

export async function getProjectCapabilities(projectId: string): Promise<R2DProjectCapabilities> {
  return request<R2DProjectCapabilities>(
    `/api/projects/${encodeURIComponent(projectId)}/capabilities`,
  );
}

export const r2dProjectCapabilityKeys = {
  detail: (projectId: string) => ["r2d", "project-capabilities", projectId] as const,
};

/**
 * Query options for a Project's capability contract. A null/absent Project
 * returns a permanently disabled query so a surface without a Project does not
 * fire a request it cannot use.
 */
export function projectCapabilitiesOptions(projectId: string | null | undefined) {
  if (!projectId) {
    return {
      queryKey: r2dProjectCapabilityKeys.detail(""),
      queryFn: () => Promise.resolve(undefined as unknown as R2DProjectCapabilities),
      enabled: false,
    };
  }
  return queryOptions({
    queryKey: r2dProjectCapabilityKeys.detail(projectId),
    queryFn: () => getProjectCapabilities(projectId),
    retry: false,
    staleTime: 15_000,
  });
}
