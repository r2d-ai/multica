"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Building2,
  Loader2,
  LockKeyhole,
  Search,
  Share2,
  Trash2,
  UserRound,
  Users,
} from "lucide-react";
import { ApiError } from "@multica/core/api";
import {
  projectSharingDirectoryOptions,
  projectSharingOptions,
  useCreateProjectGrant,
  useDeleteProjectGrant,
  useSetProjectVisibility,
  useUpdateProjectGrantRole,
  type R2DPrincipalType,
  type R2DProjectGrant,
  type R2DProjectPrincipal,
  type R2DProjectRole,
  type R2DProjectSharing,
  type R2DProjectVisibility,
} from "@multica/core/projects/r2d-sharing";
import { Button } from "@multica/ui/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { ProjectDetail as UpstreamProjectDetail } from "./project-detail";

const COPY = {
  share: "Share",
  sharingUnavailable: "Sharing unavailable",
  title: "Project sharing",
  description: "Control who can discover and work in this project.",
  visibility: "Visibility",
  workspaceVisibility: "Owner workspace",
  workspaceVisibilityDescription: "Everyone in the owner workspace can access the project.",
  privateVisibility: "Private",
  privateVisibilityDescription: "Only explicit grants and privileged observers can access the project.",
  access: "People and workspaces",
  noGrants: "No explicit access grants yet.",
  addAccess: "Add access",
  users: "Users",
  workspaces: "Workspaces",
  searchUsers: "Search users by name or email",
  searchWorkspaces: "Search workspaces by name",
  searchHint: "Type at least 2 characters to search.",
  searching: "Searching…",
  noResults: "No matching principals.",
  searchFailed: "Directory search failed.",
  selectPrincipal: "Select a result to grant access.",
  grant: "Grant access",
  viewer: "Viewer",
  member: "Member",
  manager: "Manager",
  viewerDescription: "Read-only project access",
  memberDescription: "Can work on project content",
  managerDescription: "Can manage the project and sharing",
  mutationFailed: "The sharing change could not be saved.",
  remove: "Remove access",
} as const;

const ROLE_LABELS: Record<R2DProjectRole, string> = {
  viewer: COPY.viewer,
  member: COPY.member,
  manager: COPY.manager,
};

const ROLE_DESCRIPTIONS: Record<R2DProjectRole, string> = {
  viewer: COPY.viewerDescription,
  member: COPY.memberDescription,
  manager: COPY.managerDescription,
};

function errorMessage(error: unknown): string {
  return error instanceof Error && error.message ? error.message : COPY.mutationFailed;
}

function PrincipalAvatar({ principal }: { principal?: R2DProjectPrincipal }) {
  if (principal?.avatar_url) {
    return (
      <img
        src={principal.avatar_url}
        alt=""
        className="size-8 shrink-0 rounded-full object-cover"
      />
    );
  }
  const initial = principal?.name?.trim().charAt(0).toUpperCase() || "?";
  return (
    <div className="flex size-8 shrink-0 items-center justify-center rounded-full bg-muted text-caption font-semibold text-muted-foreground">
      {initial}
    </div>
  );
}

function RoleSelect({
  value,
  disabled,
  onChange,
}: {
  value: R2DProjectRole;
  disabled?: boolean;
  onChange: (role: R2DProjectRole) => void;
}) {
  return (
    <select
      value={value}
      disabled={disabled}
      onChange={(event) => onChange(event.target.value as R2DProjectRole)}
      className="h-8 rounded-md border bg-background px-2 text-caption outline-none transition-colors focus:border-ring disabled:cursor-not-allowed disabled:opacity-50"
    >
      {(Object.keys(ROLE_LABELS) as R2DProjectRole[]).map((role) => (
        <option key={role} value={role}>
          {ROLE_LABELS[role]}
        </option>
      ))}
    </select>
  );
}

function VisibilityChoice({
  value,
  active,
  icon,
  title,
  description,
  pending,
  onSelect,
}: {
  value: R2DProjectVisibility;
  active: boolean;
  icon: React.ReactNode;
  title: string;
  description: string;
  pending: boolean;
  onSelect: (value: R2DProjectVisibility) => void;
}) {
  return (
    <button
      type="button"
      disabled={pending}
      onClick={() => onSelect(value)}
      className={cn(
        "flex flex-1 items-start gap-3 rounded-lg border p-3 text-left transition-colors",
        active ? "border-foreground/25 bg-accent/60" : "hover:bg-accent/40",
        pending && "cursor-wait opacity-60",
      )}
    >
      <span className="mt-0.5 text-muted-foreground">{icon}</span>
      <span className="min-w-0">
        <span className="block text-body-sm font-medium">{title}</span>
        <span className="mt-0.5 block text-caption leading-relaxed text-muted-foreground">
          {description}
        </span>
      </span>
    </button>
  );
}

function GrantRow({
  grant,
  rolePending,
  deletePending,
  onRoleChange,
  onDelete,
}: {
  grant: R2DProjectGrant;
  rolePending: boolean;
  deletePending: boolean;
  onRoleChange: (role: R2DProjectRole) => void;
  onDelete: () => void;
}) {
  const principal = grant.principal;
  const fallbackName = grant.principal_type === "workspace" ? COPY.workspaces : COPY.users;
  return (
    <div className="flex items-center gap-3 rounded-lg border px-3 py-2.5">
      <PrincipalAvatar principal={principal} />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-1.5">
          {grant.principal_type === "workspace" ? (
            <Building2 className="size-3.5 shrink-0 text-muted-foreground" />
          ) : (
            <UserRound className="size-3.5 shrink-0 text-muted-foreground" />
          )}
          <span className="truncate text-body-sm font-medium">
            {principal?.name || fallbackName}
          </span>
        </div>
        <div className="truncate text-caption text-muted-foreground">
          {principal?.secondary || grant.principal_id}
        </div>
      </div>
      <RoleSelect
        value={grant.role}
        disabled={rolePending || deletePending}
        onChange={onRoleChange}
      />
      <Button
        variant="ghost"
        size="icon-sm"
        disabled={rolePending || deletePending}
        title={COPY.remove}
        onClick={onDelete}
        className="text-muted-foreground hover:text-destructive"
      >
        {deletePending ? <Loader2 className="animate-spin" /> : <Trash2 />}
      </Button>
    </div>
  );
}

export function ProjectSharingDialog({
  projectId,
  sharing,
  open,
  onOpenChange,
}: {
  projectId: string;
  sharing: R2DProjectSharing;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [principalType, setPrincipalType] = useState<R2DPrincipalType>("user");
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<R2DProjectPrincipal | null>(null);
  const [newRole, setNewRole] = useState<R2DProjectRole>("member");

  const normalizedQuery = query.trim();
  const directory = useQuery({
    ...projectSharingDirectoryOptions(projectId, principalType, normalizedQuery),
    enabled: open && normalizedQuery.length >= 2,
  });
  const setVisibility = useSetProjectVisibility(projectId);
  const createGrant = useCreateProjectGrant(projectId);
  const updateRole = useUpdateProjectGrantRole(projectId);
  const deleteGrant = useDeleteProjectGrant(projectId);

  const grantedKeys = useMemo(
    () => new Set(sharing.grants.map((grant) => `${grant.principal_type}:${grant.principal_id}`)),
    [sharing.grants],
  );
  const availablePrincipals = (directory.data ?? []).filter(
    (principal) => !grantedKeys.has(`${principal.type}:${principal.id}`),
  );
  const mutationError =
    setVisibility.error || createGrant.error || updateRole.error || deleteGrant.error;

  const resetSearch = (nextType?: R2DPrincipalType) => {
    if (nextType) setPrincipalType(nextType);
    setQuery("");
    setSelected(null);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{COPY.title}</DialogTitle>
          <DialogDescription>{COPY.description}</DialogDescription>
        </DialogHeader>

        <section className="space-y-2">
          <div className="flex items-center gap-2 text-body-sm font-medium">
            <LockKeyhole className="size-4 text-muted-foreground" />
            <span>{COPY.visibility}</span>
            {setVisibility.isPending && <Loader2 className="size-3.5 animate-spin text-muted-foreground" />}
          </div>
          <div className="flex flex-col gap-2 sm:flex-row">
            <VisibilityChoice
              value="workspace"
              active={sharing.visibility === "workspace"}
              icon={<Users className="size-4" />}
              title={COPY.workspaceVisibility}
              description={COPY.workspaceVisibilityDescription}
              pending={setVisibility.isPending}
              onSelect={(visibility) => {
                if (visibility !== sharing.visibility) setVisibility.mutate(visibility);
              }}
            />
            <VisibilityChoice
              value="private"
              active={sharing.visibility === "private"}
              icon={<LockKeyhole className="size-4" />}
              title={COPY.privateVisibility}
              description={COPY.privateVisibilityDescription}
              pending={setVisibility.isPending}
              onSelect={(visibility) => {
                if (visibility !== sharing.visibility) setVisibility.mutate(visibility);
              }}
            />
          </div>
        </section>

        <section className="space-y-2 border-t pt-4">
          <div className="flex items-center gap-2 text-body-sm font-medium">
            <Users className="size-4 text-muted-foreground" />
            <span>{COPY.access}</span>
          </div>
          {sharing.grants.length === 0 ? (
            <div className="rounded-lg border border-dashed px-3 py-5 text-center text-caption text-muted-foreground">
              {COPY.noGrants}
            </div>
          ) : (
            <div className="space-y-2">
              {sharing.grants.map((grant) => (
                <GrantRow
                  key={grant.id}
                  grant={grant}
                  rolePending={updateRole.isPending && updateRole.variables?.grantId === grant.id}
                  deletePending={deleteGrant.isPending && deleteGrant.variables === grant.id}
                  onRoleChange={(role) => updateRole.mutate({ grantId: grant.id, role })}
                  onDelete={() => deleteGrant.mutate(grant.id)}
                />
              ))}
            </div>
          )}
        </section>

        <section className="space-y-3 border-t pt-4">
          <div className="text-body-sm font-medium">{COPY.addAccess}</div>
          <div className="flex items-center gap-2">
            <div className="flex rounded-md border bg-muted/30 p-0.5">
              {(["user", "workspace"] as R2DPrincipalType[]).map((type) => (
                <button
                  key={type}
                  type="button"
                  onClick={() => resetSearch(type)}
                  className={cn(
                    "rounded px-2.5 py-1 text-caption transition-colors",
                    principalType === type
                      ? "bg-background font-medium shadow-sm"
                      : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {type === "user" ? COPY.users : COPY.workspaces}
                </button>
              ))}
            </div>
            <RoleSelect value={newRole} onChange={setNewRole} disabled={createGrant.isPending} />
          </div>

          <div className="relative">
            <Search className="absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <input
              value={query}
              onChange={(event) => {
                setQuery(event.target.value);
                setSelected(null);
              }}
              placeholder={principalType === "user" ? COPY.searchUsers : COPY.searchWorkspaces}
              className="h-9 w-full rounded-md border bg-background pl-8 pr-3 text-body-sm outline-none transition-colors placeholder:text-muted-foreground focus:border-ring"
            />
          </div>

          {normalizedQuery.length < 2 ? (
            <div className="text-caption text-muted-foreground">{COPY.searchHint}</div>
          ) : directory.isLoading ? (
            <div className="flex items-center gap-2 py-2 text-caption text-muted-foreground">
              <Loader2 className="size-3.5 animate-spin" />
              <span>{COPY.searching}</span>
            </div>
          ) : directory.isError ? (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-caption text-destructive">
              {COPY.searchFailed}
            </div>
          ) : availablePrincipals.length === 0 ? (
            <div className="text-caption text-muted-foreground">{COPY.noResults}</div>
          ) : (
            <div className="max-h-44 space-y-1 overflow-y-auto rounded-md border p-1">
              {availablePrincipals.map((principal) => {
                const active = selected?.type === principal.type && selected.id === principal.id;
                return (
                  <button
                    key={`${principal.type}:${principal.id}`}
                    type="button"
                    onClick={() => setSelected(principal)}
                    className={cn(
                      "flex w-full items-center gap-2 rounded-md px-2 py-2 text-left transition-colors",
                      active ? "bg-accent" : "hover:bg-accent/60",
                    )}
                  >
                    <PrincipalAvatar principal={principal} />
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-body-sm font-medium">{principal.name}</span>
                      {principal.secondary && (
                        <span className="block truncate text-caption text-muted-foreground">
                          {principal.secondary}
                        </span>
                      )}
                    </span>
                  </button>
                );
              })}
            </div>
          )}

          <div className="flex items-center justify-between gap-3">
            <div className="min-w-0 text-caption text-muted-foreground">
              {selected ? (
                <>
                  <span className="font-medium text-foreground">{selected.name}</span>
                  <span> · {ROLE_DESCRIPTIONS[newRole]}</span>
                </>
              ) : (
                COPY.selectPrincipal
              )}
            </div>
            <Button
              size="sm"
              disabled={!selected || createGrant.isPending}
              onClick={() => {
                if (!selected) return;
                createGrant.mutate(
                  { principal: selected, role: newRole },
                  { onSuccess: () => resetSearch() },
                );
              }}
            >
              {createGrant.isPending && <Loader2 className="animate-spin" />}
              {COPY.grant}
            </Button>
          </div>
        </section>

        {mutationError && (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-caption text-destructive">
            {errorMessage(mutationError)}
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

/**
 * Low-conflict integration point around the upstream ProjectDetail.
 * The upstream component stays untouched; only the barrel export points here.
 */
export function R2DProjectDetail({ projectId }: { projectId: string }) {
  const [open, setOpen] = useState(false);
  const sharing = useQuery(projectSharingOptions(projectId));

  const forbidden = sharing.error instanceof ApiError && sharing.error.status === 403;

  return (
    <div className="relative flex h-full min-h-0 flex-1">
      <UpstreamProjectDetail projectId={projectId} />

      {sharing.isLoading && (
        <Skeleton className="absolute right-28 top-2 z-20 h-7 w-16" />
      )}

      {sharing.isError && !forbidden && (
        <div className="absolute right-28 top-2 z-20 rounded-md border bg-background/95 px-2 py-1 text-caption text-muted-foreground shadow-sm">
          {COPY.sharingUnavailable}
        </div>
      )}

      {sharing.data && (
        <>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => setOpen(true)}
            className="absolute right-28 top-1.5 z-20 h-7 gap-1.5 bg-background/80 px-2 text-muted-foreground backdrop-blur hover:text-foreground"
            title={COPY.share}
          >
            <Share2 className="size-3.5" />
            <span className="hidden sm:inline">{COPY.share}</span>
          </Button>
          <ProjectSharingDialog
            projectId={projectId}
            sharing={sharing.data}
            open={open}
            onOpenChange={setOpen}
          />
        </>
      )}
    </div>
  );
}
