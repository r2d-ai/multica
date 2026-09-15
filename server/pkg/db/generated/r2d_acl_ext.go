package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// R2DProjectAccessFacts is a storage-only projection consumed by the R2D
// authorization layer. It deliberately lives beside sqlc's Queries so the fork
// can reuse the existing DBTX without editing upstream query definitions.
type R2DProjectAccessFacts struct {
	ProjectID          string
	OwnerWorkspaceID   string
	Visibility         string
	OwnerWorkspaceRole string
	DirectGrantRole    string
	WorkspaceGrantRole string
	GlobalObserver     bool
}

const r2dProjectAccessFactsSelect = `
SELECT
    p.id::text,
    p.workspace_id::text,
    COALESCE(extra.visibility, 'workspace') AS visibility,
    COALESCE(owner_member.role, '') AS owner_workspace_role,
    COALESCE((
        SELECT g.role
        FROM r2d_project_grants g
        WHERE g.project_id = p.id::text
          AND g.principal_type = 'user'
          AND g.principal_id = $1::text
        LIMIT 1
    ), '') AS direct_grant_role,
    COALESCE((
        SELECT g.role
        FROM r2d_project_grants g
        JOIN member grantee_member
          ON grantee_member.workspace_id::text = g.principal_id
         AND grantee_member.user_id = $1::uuid
        WHERE g.project_id = p.id::text
          AND g.principal_type = 'workspace'
        ORDER BY CASE g.role
            WHEN 'manager' THEN 3
            WHEN 'member' THEN 2
            WHEN 'viewer' THEN 1
            ELSE 0
        END DESC
        LIMIT 1
    ), '') AS workspace_grant_role,
    EXISTS (
        SELECT 1
        FROM r2d_global_roles global_role
        WHERE global_role.user_id = $1::text
          AND global_role.role = 'global_observer'
    ) AS global_observer
FROM project p
LEFT JOIN r2d_project_extra extra
  ON extra.project_id = p.id::text
LEFT JOIN member owner_member
  ON owner_member.workspace_id = p.workspace_id
 AND owner_member.user_id = $1::uuid
`

// R2DLoadProjectAccessFacts resolves authorization inputs without applying
// policy. Policy remains centralized in internal/r2dauth.Resolve.
func (q *Queries) R2DLoadProjectAccessFacts(ctx context.Context, userID, projectID string) (R2DProjectAccessFacts, error) {
	var f R2DProjectAccessFacts
	err := q.db.QueryRow(ctx, r2dProjectAccessFactsSelect+` WHERE p.id = $2::uuid`, userID, projectID).Scan(
		&f.ProjectID,
		&f.OwnerWorkspaceID,
		&f.Visibility,
		&f.OwnerWorkspaceRole,
		&f.DirectGrantRole,
		&f.WorkspaceGrantRole,
		&f.GlobalObserver,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return R2DProjectAccessFacts{}, pgx.ErrNoRows
	}
	return f, err
}

// R2DListProjectAccessFacts is the batched form used when filtering an
// upstream list/search response. One query covers all project IDs, preventing
// an authorization N+1.
func (q *Queries) R2DListProjectAccessFacts(ctx context.Context, userID string, projectIDs []string) ([]R2DProjectAccessFacts, error) {
	if len(projectIDs) == 0 {
		return []R2DProjectAccessFacts{}, nil
	}
	rows, err := q.db.Query(ctx, r2dProjectAccessFactsSelect+`
WHERE p.id::text = ANY($2::text[])
ORDER BY p.id`, userID, projectIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]R2DProjectAccessFacts, 0, len(projectIDs))
	for rows.Next() {
		var f R2DProjectAccessFacts
		if err := rows.Scan(
			&f.ProjectID,
			&f.OwnerWorkspaceID,
			&f.Visibility,
			&f.OwnerWorkspaceRole,
			&f.DirectGrantRole,
			&f.WorkspaceGrantRole,
			&f.GlobalObserver,
		); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// R2DListWorkspaceProjectAccessFacts is used only as a fail-closed migration
// guard for collection/aggregate surfaces that P04 has not made ACL-native yet.
func (q *Queries) R2DListWorkspaceProjectAccessFacts(ctx context.Context, userID, workspaceID string) ([]R2DProjectAccessFacts, error) {
	rows, err := q.db.Query(ctx, r2dProjectAccessFactsSelect+`
WHERE p.workspace_id = $2::uuid
ORDER BY p.id`, userID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]R2DProjectAccessFacts, 0)
	for rows.Next() {
		var f R2DProjectAccessFacts
		if err := rows.Scan(
			&f.ProjectID,
			&f.OwnerWorkspaceID,
			&f.Visibility,
			&f.OwnerWorkspaceRole,
			&f.DirectGrantRole,
			&f.WorkspaceGrantRole,
			&f.GlobalObserver,
		); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// R2DIssueACLTarget performs only the global entity lookup needed before a
// workspace has been authorized. A projectless issue returns an empty
// ProjectID and therefore falls back to the ordinary Workspace gate.
type R2DIssueACLTarget struct {
	WorkspaceID string
	ProjectID   string
}

func (q *Queries) R2DLoadIssueACLTarget(ctx context.Context, issueID string) (R2DIssueACLTarget, error) {
	var target R2DIssueACLTarget
	err := q.db.QueryRow(ctx, `
SELECT workspace_id::text, COALESCE(project_id::text, '')
FROM issue
WHERE id = $1::uuid`, issueID).Scan(&target.WorkspaceID, &target.ProjectID)
	return target, err
}
