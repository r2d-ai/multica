import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { getApi } from "../api";

export type R2DProjectVisibility = "workspace" | "private";
export type R2DProjectRole = "viewer" | "member" | "manager";
export type R2DPrincipalType = "user" | "workspace";

export interface R2DProjectPrincipal {
  type: R2DPrincipalType;
  id: string;
  name: string;
  secondary?: string;
  avatar_url?: string;
}

export interface R2DProjectGrant {
  id: string;
  project_id: string;
  principal_type: R2DPrincipalType;
  principal_id: string;
  role: R2DProjectRole;
  created_by: string;
  created_at: string;
  updated_at: string;
  principal?: R2DProjectPrincipal;
}

export interface R2DProjectSharing {
  project_id: string;
  visibility: R2DProjectVisibility;
  grants: R2DProjectGrant[];
}

type InternalApiTransport = {
  fetch<T>(path: string, init?: RequestInit): Promise<T>;
};

/**
 * R2D-only transport shim.
 *
 * The upstream ApiClient intentionally keeps its request primitive private.
 * Keeping the shim here avoids adding R2D methods to the 170KB upstream client
 * while still reusing its auth, CSRF, workspace, request-id and error handling.
 * ApiClient uses a TypeScript-private method (not a JS #private field), so the
 * runtime method is available on the singleton.
 */
function request<T>(path: string, init?: RequestInit): Promise<T> {
  const client = getApi() as unknown as InternalApiTransport;
  return client.fetch<T>(path, init);
}

function projectPath(projectId: string): string {
  return `/api/projects/${encodeURIComponent(projectId)}/sharing`;
}

export async function getProjectSharing(projectId: string): Promise<R2DProjectSharing> {
  return request<R2DProjectSharing>(projectPath(projectId));
}

export async function setProjectVisibility(
  projectId: string,
  visibility: R2DProjectVisibility,
): Promise<R2DProjectSharing> {
  return request<R2DProjectSharing>(projectPath(projectId), {
    method: "PATCH",
    body: JSON.stringify({ visibility }),
  });
}

export async function searchProjectSharingDirectory(
  projectId: string,
  principalType: R2DPrincipalType,
  query: string,
): Promise<R2DProjectPrincipal[]> {
  const search = new URLSearchParams({
    q: query.trim(),
    type: principalType,
  });
  const result = await request<{ principals?: R2DProjectPrincipal[] }>(
    `${projectPath(projectId)}/directory?${search.toString()}`,
  );
  return result.principals ?? [];
}

export async function createProjectGrant(
  projectId: string,
  principal: R2DProjectPrincipal,
  role: R2DProjectRole,
): Promise<R2DProjectGrant> {
  return request<R2DProjectGrant>(`${projectPath(projectId)}/grants`, {
    method: "POST",
    body: JSON.stringify({
      principal_type: principal.type,
      principal_id: principal.id,
      role,
    }),
  });
}

export async function updateProjectGrantRole(
  projectId: string,
  grantId: string,
  role: R2DProjectRole,
): Promise<R2DProjectGrant> {
  return request<R2DProjectGrant>(
    `${projectPath(projectId)}/grants/${encodeURIComponent(grantId)}`,
    {
      method: "PATCH",
      body: JSON.stringify({ role }),
    },
  );
}

export async function deleteProjectGrant(projectId: string, grantId: string): Promise<void> {
  await request<void>(
    `${projectPath(projectId)}/grants/${encodeURIComponent(grantId)}`,
    { method: "DELETE" },
  );
}

export const r2dProjectSharingKeys = {
  detail: (projectId: string) => ["r2d", "project-sharing", projectId] as const,
  directory: (projectId: string, type: R2DPrincipalType, query: string) =>
    ["r2d", "project-sharing", projectId, "directory", type, query.trim()] as const,
};

export function projectSharingOptions(projectId: string) {
  return queryOptions({
    queryKey: r2dProjectSharingKeys.detail(projectId),
    queryFn: () => getProjectSharing(projectId),
    retry: false,
  });
}

export function projectSharingDirectoryOptions(
  projectId: string,
  type: R2DPrincipalType,
  query: string,
) {
  const normalized = query.trim();
  return queryOptions({
    queryKey: r2dProjectSharingKeys.directory(projectId, type, normalized),
    queryFn: () => searchProjectSharingDirectory(projectId, type, normalized),
    enabled: normalized.length >= 2,
    staleTime: 15_000,
    retry: false,
  });
}

export function useSetProjectVisibility(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (visibility: R2DProjectVisibility) =>
      setProjectVisibility(projectId, visibility),
    onSuccess: (sharing) => {
      qc.setQueryData(r2dProjectSharingKeys.detail(projectId), sharing);
    },
  });
}

export function useCreateProjectGrant(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ principal, role }: { principal: R2DProjectPrincipal; role: R2DProjectRole }) =>
      createProjectGrant(projectId, principal, role),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: r2dProjectSharingKeys.detail(projectId) });
    },
  });
}

export function useUpdateProjectGrantRole(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ grantId, role }: { grantId: string; role: R2DProjectRole }) =>
      updateProjectGrantRole(projectId, grantId, role),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: r2dProjectSharingKeys.detail(projectId) });
    },
  });
}

export function useDeleteProjectGrant(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (grantId: string) => deleteProjectGrant(projectId, grantId),
    onSettled: () => {
      qc.invalidateQueries({ queryKey: r2dProjectSharingKeys.detail(projectId) });
    },
  });
}
