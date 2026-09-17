"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Share2 } from "lucide-react";
import { projectCapabilitiesOptions } from "@multica/core/projects/r2d-capabilities";
import { projectSharingOptions } from "@multica/core/projects/r2d-sharing";
import { useProjectRealtimeScope } from "@multica/core/realtime";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { ProjectDetail as UpstreamProjectDetail } from "./project-detail";
import { ProjectSharingDialog } from "./r2d-project-sharing";
import { R2DSafeProjectDetail } from "./r2d-safe-project-detail";
import { r2dProjectDetailMode } from "./r2d-project-ui-policy";

const COPY = {
  share: "Share",
} as const;

export function R2DProjectDetail({ projectId }: { projectId: string }) {
  // Join the Project realtime room for as long as this surface is mounted so
  // Project-scoped issue / comment events reach a collaborator whose home
  // Workspace is not the Project owner's. Mounted before the read gate on
  // purpose: an unauthorized viewer must still attempt the join so the server
  // can answer with `subscribe_error` rather than silently receive nothing.
  useProjectRealtimeScope(projectId);
  const [sharingOpen, setSharingOpen] = useState(false);
  const capabilities = useQuery(projectCapabilitiesOptions(projectId));
  const sharing = useQuery({
    ...projectSharingOptions(projectId),
    enabled: capabilities.data?.share === true,
  });

  if (capabilities.isLoading) {
    return (
      <div className="w-full space-y-4 px-6 py-8">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-20 w-full max-w-3xl" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  if (capabilities.isError || !capabilities.data?.read) {
    // Fail closed: never fall back to the unrestricted upstream detail when
    // the capability contract is unavailable.
    return (
      <div className="flex h-full flex-1 items-center justify-center text-body-sm text-muted-foreground">
        Project unavailable.
      </div>
    );
  }

  const caps = capabilities.data;
  const mode = r2dProjectDetailMode(caps);

  // Rendered as a real toolbar action rather than an absolutely positioned
  // overlay so it reflows with the header controls (pin / overflow / panel)
  // instead of drifting to a fixed corner across layouts.
  const shareControl =
    caps.share && sharing.data ? (
      <Button
        variant="ghost"
        size="icon-sm"
        className="text-muted-foreground"
        aria-label={COPY.share}
        title={COPY.share}
        onClick={() => setSharingOpen(true)}
      >
        <Share2 />
      </Button>
    ) : null;

  return (
    <div className="relative flex h-full min-h-0 flex-1">
      {mode === "full" ? (
        <UpstreamProjectDetail projectId={projectId} toolbarActions={shareControl} />
      ) : (
        <R2DSafeProjectDetail
          projectId={projectId}
          capabilities={caps}
          toolbarActions={shareControl}
        />
      )}

      {caps.share && sharing.data && (
        <ProjectSharingDialog
          projectId={projectId}
          sharing={sharing.data}
          open={sharingOpen}
          onOpenChange={setSharingOpen}
        />
      )}
    </div>
  );
}
