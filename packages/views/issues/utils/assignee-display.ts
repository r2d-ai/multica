export interface AssigneeDisplayIssue {
  assignee_type?: string | null;
  assignee_id?: string | null;
  assignee_name?: string | null;
  assignee_avatar_url?: string | null;
}

/**
 * Prefers the server-resolved assignee name, which is the only source that can
 * name a collaborator from another Workspace. Falls back to the local
 * members/agents resolver, then to an empty name for an unassigned issue.
 */
export function issueAssigneeDisplay(
  issue: AssigneeDisplayIssue,
  resolve: (type: string, id: string) => string,
): { name: string; avatarUrl: string | null } {
  if (!issue.assignee_type || !issue.assignee_id) {
    return { name: "", avatarUrl: null };
  }
  const name = issue.assignee_name ?? resolve(issue.assignee_type, issue.assignee_id);
  return { name, avatarUrl: issue.assignee_avatar_url ?? null };
}
