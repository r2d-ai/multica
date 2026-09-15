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
