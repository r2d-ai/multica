package db

import "context"

// R2DIssueACLTargetBatch is the minimum storage projection needed to authorize
// issue mutations before an owner Workspace has been admitted. Projectless
// issues carry an empty ProjectID and therefore retain the Workspace boundary.
type R2DIssueACLTargetBatch struct {
	IssueID     string
	WorkspaceID string
	ProjectID   string
}

// R2DListIssueACLTargets resolves a set of issue IDs in one query. It applies no
// authorization policy; callers batch the distinct Project IDs through
// R2DListProjectAccessFacts and keep r2dauth.Resolve as the sole policy engine.
func (q *Queries) R2DListIssueACLTargets(ctx context.Context, issueIDs []string) ([]R2DIssueACLTargetBatch, error) {
	if len(issueIDs) == 0 {
		return []R2DIssueACLTargetBatch{}, nil
	}
	rows, err := q.db.Query(ctx, `
SELECT id::text, workspace_id::text, COALESCE(project_id::text, '')
FROM issue
WHERE id::text = ANY($1::text[])
ORDER BY id`, issueIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]R2DIssueACLTargetBatch, 0, len(issueIDs))
	for rows.Next() {
		var target R2DIssueACLTargetBatch
		if err := rows.Scan(&target.IssueID, &target.WorkspaceID, &target.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type R2DChildIssueProgressRow struct {
	ParentIssueID string
	Total         int64
	Done          int64
}

// R2DChildIssueProgressVisible aggregates only after both child and parent have
// passed the caller-supplied readable Project set. Projectless rows are allowed
// here because the caller is the Workspace-member read path; foreign Project
// reads never enter this aggregate.
func (q *Queries) R2DChildIssueProgressVisible(ctx context.Context, workspaceID string, readableProjectIDs, terminalStatusKeys []string) ([]R2DChildIssueProgressRow, error) {
	rows, err := q.db.Query(ctx, `
SELECT i.parent_issue_id::text,
       COUNT(*)::bigint AS total,
       COUNT(*) FILTER (WHERE i.status = ANY($3::text[]))::bigint AS done
FROM issue i
JOIN issue p
  ON p.id = i.parent_issue_id
 AND p.workspace_id = i.workspace_id
WHERE i.workspace_id::text = $1
  AND i.parent_issue_id IS NOT NULL
  AND (i.project_id IS NULL OR i.project_id::text = ANY($2::text[]))
  AND (p.project_id IS NULL OR p.project_id::text = ANY($2::text[]))
GROUP BY i.parent_issue_id
ORDER BY i.parent_issue_id`, workspaceID, readableProjectIDs, terminalStatusKeys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]R2DChildIssueProgressRow, 0)
	for rows.Next() {
		var row R2DChildIssueProgressRow
		if err := rows.Scan(&row.ParentIssueID, &row.Total, &row.Done); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (q *Queries) R2DIsGlobalObserver(ctx context.Context, userID string) (bool, error) {
	var exists bool
	err := q.db.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM r2d_global_roles
    WHERE user_id::text = $1 AND role = 'global_observer'
)`, userID).Scan(&exists)
	return exists, err
}
