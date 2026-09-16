package db

import "context"

// R2DProjectGrantRecipient is one user who reaches a Project through an
// explicit r2d_project_grants row. It is a delivery projection, not an
// authorization decision: the role filter was already applied by the query, and
// policy stays centralized in internal/r2dauth.
type R2DProjectGrantRecipient struct {
	UserID string
	Role   string
}

// R2DListProjectGrantRecipients resolves the users who hold one of roles on a
// Project, expanding a workspace principal to the individual members of that
// workspace. Owner-Workspace members are deliberately absent: they have
// implicit access but no grant row, and subscriber delivery already reaches
// them. A projectless issue therefore contributes no grant recipients.
//
// roles come from r2dauth.ProjectRolesForOperation so the caller never
// re-implements the Project role ladder.
//
// The query is evaluated when a notification is delivered, not when a
// subscription is recorded. That is what makes revocation propagate: a grant
// deleted between the event and its delivery is simply not in the result set,
// and a member removed from a workspace principal is dropped by the inner join.
func (q *Queries) R2DListProjectGrantRecipients(ctx context.Context, projectID string, roles []string) ([]R2DProjectGrantRecipient, error) {
	if projectID == "" || len(roles) == 0 {
		return []R2DProjectGrantRecipient{}, nil
	}

	rows, err := q.db.Query(ctx, `
SELECT DISTINCT
    CASE
        WHEN g.principal_type = 'user' THEN g.principal_id
        ELSE m.user_id::text
    END AS recipient_id,
    g.role
FROM r2d_project_grants g
LEFT JOIN member m
  ON g.principal_type = 'workspace'
 AND m.workspace_id::text = g.principal_id
WHERE g.project_id = $1
  AND g.role = ANY($2::text[])
  AND (
      (
          g.principal_type = 'user'
          AND EXISTS (SELECT 1 FROM "user" u WHERE u.id::text = g.principal_id)
      )
      OR (g.principal_type = 'workspace' AND m.user_id IS NOT NULL)
  )
ORDER BY recipient_id`, projectID, roles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]R2DProjectGrantRecipient, 0)
	for rows.Next() {
		var r R2DProjectGrantRecipient
		if err := rows.Scan(&r.UserID, &r.Role); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
