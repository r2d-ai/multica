"use client";

import { use, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useAuthStore } from "@multica/core/auth";
import { paths } from "@multica/core/paths";
import { workspaceListOptions } from "@multica/core/workspace/queries";
import { MulticaIcon } from "@multica/ui/components/common/multica-icon";
import { resolveProjectHostPath } from "@/lib/r2d-project-host";

/**
 * Fallback for canonical `/projects/{id}` links when the authenticated browser
 * has no `last_workspace_slug` cookie. Normally proxy.ts re-homes the URL before
 * Next renders this page. A fresh/cleared browser instead resolves the caller's
 * own Workspace list here, then enters the regular Workspace shell without ever
 * requiring membership in the Project owner's Workspace.
 */
export default function SharedProjectHostPage({
  params,
}: {
  params: Promise<{ projectId: string }>;
}) {
  const { projectId } = use(params);
  const router = useRouter();
  const user = useAuthStore((state) => state.user);
  const authLoading = useAuthStore((state) => state.isLoading);
  const { data: workspaces } = useQuery({
    ...workspaceListOptions(),
    enabled: !!user,
  });

  useEffect(() => {
    if (authLoading) return;
    if (!user) {
      const next = encodeURIComponent(paths.projectShare(projectId));
      router.replace(`${paths.login()}?next=${next}`);
      return;
    }
    if (!workspaces) return;
    router.replace(resolveProjectHostPath(workspaces, projectId));
  }, [authLoading, projectId, router, user, workspaces]);

  return (
    <div className="flex h-svh items-center justify-center">
      <MulticaIcon className="size-6 animate-pulse" />
    </div>
  );
}
