"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { getApi } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssuesScope } from "@multica/core/issues/stores";
import { projectDetailOptions } from "@multica/core/projects/queries";
import { useUpdateProject } from "@multica/core/projects/mutations";
import type { R2DProjectCapabilities } from "@multica/core/projects/r2d-capabilities";
import type { ProjectPriority, ProjectStatus } from "@multica/core/types";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { IssueSurface } from "../../issues/surface/issue-surface";

const PROJECT_STATUSES: ProjectStatus[] = [
  "planned",
  "in_progress",
  "paused",
  "completed",
  "cancelled",
];
const PROJECT_PRIORITIES: ProjectPriority[] = ["urgent", "high", "medium", "low", "none"];

type InternalApiTransport = {
  fetch<T>(path: string, init?: RequestInit): Promise<T>;
};

type ReadOnlyIssue = {
  id: string;
  identifier: string;
  title: string;
  status: string;
  priority: string;
};

function request<T>(path: string): Promise<T> {
  return (getApi() as unknown as InternalApiTransport).fetch<T>(path);
}

function readOnlyProjectIssuesOptions(projectId: string) {
  return {
    queryKey: ["r2d", "project-readonly-issues", projectId] as const,
    queryFn: async () => {
      const search = new URLSearchParams({ project_id: projectId, limit: "200" });
      const data = await request<{ issues?: ReadOnlyIssue[] }>(`/api/issues?${search.toString()}`);
      return data.issues ?? [];
    },
    retry: false,
  };
}

function ReadOnlyIssues({ projectId }: { projectId: string }) {
  const issues = useQuery(readOnlyProjectIssuesOptions(projectId));
  if (issues.isLoading) {
    return (
      <div className="space-y-2 p-4">
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-9 w-full" />
        <Skeleton className="h-9 w-4/5" />
      </div>
    );
  }
  if (issues.isError) {
    return <div className="p-4 text-caption text-muted-foreground">Issues unavailable.</div>;
  }
  if (!issues.data?.length) {
    return <div className="p-4 text-caption text-muted-foreground">No issues in this project.</div>;
  }
  return (
    <div className="divide-y overflow-y-auto">
      {issues.data.map((issue) => (
        <div key={issue.id} className="flex items-center gap-3 px-4 py-2.5">
          <span className="w-24 shrink-0 text-caption text-muted-foreground">{issue.identifier}</span>
          <span className="min-w-0 flex-1 truncate text-body-sm">{issue.title}</span>
          <span className="hidden text-caption text-muted-foreground sm:inline">{issue.status}</span>
          <span className="hidden text-caption text-muted-foreground md:inline">{issue.priority}</span>
        </div>
      ))}
    </div>
  );
}

export function R2DSafeProjectDetail({
  projectId,
  capabilities,
}: {
  projectId: string;
  capabilities: R2DProjectCapabilities;
}) {
  const wsId = useWorkspaceId();
  const projectQuery = useQuery(projectDetailOptions(wsId, projectId));
  const updateProject = useUpdateProject();
  const issueTab = useIssuesScope(`project:${projectId}`);
  const issueScope = useMemo(
    () => ({ type: "project" as const, projectId, actorKind: issueTab }),
    [projectId, issueTab],
  );

  if (projectQuery.isLoading) {
    return (
      <div className="w-full space-y-4 px-6 py-8">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-20 w-full max-w-3xl" />
        <Skeleton className="h-48 w-full" />
      </div>
    );
  }

  const project = projectQuery.data;
  if (!project) {
    return <div className="flex h-full flex-1 items-center justify-center text-muted-foreground">Project not found.</div>;
  }

  const update = (data: Parameters<typeof updateProject.mutate>[0]) =>
    updateProject.mutate({ ...data, id: project.id });

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      <div className="shrink-0 border-b px-5 py-4 pr-28">
        <div className="flex items-start gap-3">
          <span className="mt-1 text-xl" aria-hidden>{project.icon || "📁"}</span>
          <div className="min-w-0 flex-1 space-y-2">
            {capabilities.manage ? (
              <input
                key={`${project.id}:${project.updated_at}:title`}
                defaultValue={project.title}
                aria-label="Project title"
                className="h-9 w-full max-w-2xl rounded-md border bg-background px-2 text-lg font-semibold outline-none focus:border-ring"
                onBlur={(event) => {
                  const title = event.currentTarget.value.trim();
                  if (title && title !== project.title) update({ id: project.id, title });
                }}
              />
            ) : (
              <h1 className="truncate text-lg font-semibold">{project.title}</h1>
            )}

            {capabilities.manage ? (
              <textarea
                key={`${project.id}:${project.updated_at}:description`}
                defaultValue={project.description ?? ""}
                aria-label="Project description"
                rows={2}
                className="w-full max-w-3xl resize-y rounded-md border bg-background px-2 py-1.5 text-body-sm outline-none focus:border-ring"
                onBlur={(event) => {
                  const description = event.currentTarget.value;
                  if (description !== (project.description ?? "")) {
                    update({ id: project.id, description });
                  }
                }}
              />
            ) : project.description ? (
              <p className="max-w-3xl text-body-sm text-muted-foreground">{project.description}</p>
            ) : null}

            <div className="flex flex-wrap items-center gap-2 text-caption text-muted-foreground">
              {capabilities.manage ? (
                <>
                  <select
                    value={project.status}
                    aria-label="Project status"
                    className="h-8 rounded-md border bg-background px-2"
                    onChange={(event) => update({ id: project.id, status: event.target.value as ProjectStatus })}
                  >
                    {PROJECT_STATUSES.map((status) => <option key={status} value={status}>{status}</option>)}
                  </select>
                  <select
                    value={project.priority}
                    aria-label="Project priority"
                    className="h-8 rounded-md border bg-background px-2"
                    onChange={(event) => update({ id: project.id, priority: event.target.value as ProjectPriority })}
                  >
                    {PROJECT_PRIORITIES.map((priority) => <option key={priority} value={priority}>{priority}</option>)}
                  </select>
                </>
              ) : (
                <>
                  <span>{project.status}</span>
                  <span>·</span>
                  <span>{project.priority}</span>
                </>
              )}
            </div>
          </div>
        </div>
      </div>

      <div className="min-h-0 flex-1">
        {capabilities.contribute ? (
          <IssueSurface
            scope={issueScope}
            modes={["board", "list", "table", "swimlane", "gantt"]}
          />
        ) : (
          <ReadOnlyIssues projectId={projectId} />
        )}
      </div>
    </div>
  );
}
