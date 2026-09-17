"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { getApi } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { useIssuesScope } from "@multica/core/issues/stores";
import { projectDetailOptions } from "@multica/core/projects/queries";
import { useUpdateProject } from "@multica/core/projects/mutations";
import type { R2DProjectCapabilities } from "@multica/core/projects/r2d-capabilities";
import type {
  ListProjectResourcesResponse,
  LocalDirectoryResourceRef,
  GithubRepoResourceRef,
  ProjectPriority,
  ProjectStatus,
  UpdateProjectRequest,
} from "@multica/core/types";
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

function projectResourcesOptions(projectId: string, enabled: boolean) {
  return {
    queryKey: ["r2d", "project-resources-readonly", projectId] as const,
    queryFn: () => request<ListProjectResourcesResponse>(`/api/projects/${encodeURIComponent(projectId)}/resources`),
    enabled,
    retry: false,
  };
}

function resourceSummary(resource: ListProjectResourcesResponse["resources"][number]): string {
  if (resource.resource_type === "github_repo") {
    const ref = resource.resource_ref as GithubRepoResourceRef;
    return ref.url || "Git repository";
  }
  if (resource.resource_type === "local_directory") {
    const ref = resource.resource_ref as LocalDirectoryResourceRef;
    return ref.local_path || "Local directory";
  }
  return resource.resource_type;
}

function ReadOnlyResources({ projectId }: { projectId: string }) {
  const resources = useQuery(projectResourcesOptions(projectId, true));
  if (resources.isLoading) {
    return <Skeleton className="h-8 w-full max-w-xl" />;
  }
  if (resources.isError || !resources.data?.resources.length) return null;
  return (
    <div className="flex flex-wrap gap-2">
      {resources.data.resources.map((resource) => (
        <div
          key={resource.id}
          className="max-w-full rounded-md border bg-muted/20 px-2 py-1 text-caption text-muted-foreground"
          title={resourceSummary(resource)}
        >
          <span className="font-medium text-foreground">{resource.label || resource.resource_type}</span>
          <span className="mx-1">·</span>
          <span className="break-all">{resourceSummary(resource)}</span>
        </div>
      ))}
    </div>
  );
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
  toolbarActions,
}: {
  projectId: string;
  capabilities: R2DProjectCapabilities;
  /** Extra header controls (e.g. the manager-only Share affordance). */
  toolbarActions?: React.ReactNode;
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

  const update = (data: UpdateProjectRequest) =>
    updateProject.mutate({ id: project.id, ...data });

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col">
      <div className="shrink-0 space-y-3 border-b px-5 py-4">
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
                  if (title && title !== project.title) update({ title });
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
                  if (description !== (project.description ?? "")) update({ description });
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
                    onChange={(event) => update({ status: event.target.value as ProjectStatus })}
                  >
                    {PROJECT_STATUSES.map((status) => <option key={status} value={status}>{status}</option>)}
                  </select>
                  <select
                    value={project.priority}
                    aria-label="Project priority"
                    className="h-8 rounded-md border bg-background px-2"
                    onChange={(event) => update({ priority: event.target.value as ProjectPriority })}
                  >
                    {PROJECT_PRIORITIES.map((priority) => <option key={priority} value={priority}>{priority}</option>)}
                  </select>
                  <label className="flex items-center gap-1">
                    Start
                    <input
                      type="date"
                      value={project.start_date ?? ""}
                      onChange={(event) => update({ start_date: event.target.value || null })}
                      className="h-8 rounded-md border bg-background px-2"
                    />
                  </label>
                  <label className="flex items-center gap-1">
                    Due
                    <input
                      type="date"
                      value={project.due_date ?? ""}
                      onChange={(event) => update({ due_date: event.target.value || null })}
                      className="h-8 rounded-md border bg-background px-2"
                    />
                  </label>
                </>
              ) : (
                <>
                  <span>{project.status}</span>
                  <span>·</span>
                  <span>{project.priority}</span>
                  {project.start_date && <><span>·</span><span>Start {project.start_date}</span></>}
                  {project.due_date && <><span>·</span><span>Due {project.due_date}</span></>}
                </>
              )}
            </div>
          </div>
          {toolbarActions ? (
            <div className="flex shrink-0 items-center gap-1">{toolbarActions}</div>
          ) : null}
        </div>

        {capabilities.view_resources && <ReadOnlyResources projectId={projectId} />}
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
