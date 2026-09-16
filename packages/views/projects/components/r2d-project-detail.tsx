"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Share2 } from "lucide-react";
import { projectCapabilitiesOptions } from "@multica/core/projects/r2d-capabilities";
import { projectSharingOptions } from "@multica/core/projects/r2d-sharing";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { ProjectDetail as UpstreamProjectDetail } from "./project-detail";
import { ProjectSharingDialog } from "./r2d-project-sharing";
import { R2DSafeProjectDetail } from "./r2d-safe-project-detail";
import { r2dProjectDetailMode } from "./r2d-project-ui-policy";

export function R2DProjectDetail({ projectId }: { projectId: string }) {
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

  return (
    <div className="relative flex h-full min-h-0 flex-1">
      {mode === "full" ? (
        <UpstreamProjectDetail projectId={projectId} />
      ) : (
        <R2DSafeProjectDetail projectId={projectId} capabilities={caps} />
      )}

      {caps.share && sharing.isLoading && (
        <Skeleton className="absolute right-28 top-2 z-20 h-7 w-16" />
      )}

      {caps.share && sharing.isError && (
        <div className="absolute right-28 top-2 z-20 rounded-md border bg-background/95 px-2 py-1 text-caption text-muted-foreground shadow-sm">
          Sharing unavailable
        </div>
      )}

      {caps.share && sharing.data && (
        <>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setSharingOpen(true)}
            className="absolute right-28 top-1.5 z-20 h-7 gap-1.5 bg-background/80 px-2 text-muted-foreground backdrop-blur hover:text-foreground"
            title="Share"
          >
            <Share2 className="size-3.5" />
            <span className="hidden sm:inline">Share</span>
          </Button>
          <ProjectSharingDialog
            projectId={projectId}
            sharing={sharing.data}
            open={sharingOpen}
            onOpenChange={setSharingOpen}
          />
        </>
      )}
    </div>
  );
}
