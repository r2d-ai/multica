import { paths } from "@multica/core/paths";
import type { Workspace } from "@multica/core/types";

/**
 * Pick a Workspace shell owned by the recipient for a canonical Project link.
 *
 * The Project owner's Workspace is intentionally not an input. Project ACL is
 * authorized by Project id on the backend and rebinds API context there; the
 * browser only needs a Workspace the current user actually belongs to so the
 * normal app shell/providers can mount.
 */
export function resolveProjectHostPath(
  workspaces: Workspace[],
  projectId: string,
): string {
  const host = workspaces[0];
  return host
    ? paths.workspace(host.slug).projectDetail(projectId)
    : paths.newWorkspace();
}
