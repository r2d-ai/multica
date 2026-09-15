package r2dauth

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// DB is the narrow pgx surface needed by the R2D ACL fact store. *pgxpool.Pool
// and *pgx.Conn both satisfy it, while tests can provide focused fakes.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type PostgresStore struct {
	db DB
}

func NewPostgresStore(db DB) *PostgresStore {
	return &PostgresStore{db: db}
}

const projectFactsSQL = `
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
WHERE p.id = $2::uuid
`

func (s *PostgresStore) LoadProjectFacts(ctx context.Context, userID, projectID string) (ProjectFacts, error) {
	var f ProjectFacts
	var visibility, ownerRole, directRole, workspaceRole string
	err := s.db.QueryRow(ctx, projectFactsSQL, userID, projectID).Scan(
		&f.ProjectID,
		&f.OwnerWorkspaceID,
		&visibility,
		&ownerRole,
		&directRole,
		&workspaceRole,
		&f.GlobalObserver,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectFacts{}, ErrProjectNotFound
	}
	if err != nil {
		return ProjectFacts{}, err
	}
	f.Visibility = Visibility(visibility)
	f.OwnerWorkspaceRole = WorkspaceRole(ownerRole)
	f.DirectGrantRole = ProjectRole(directRole)
	f.WorkspaceGrantRole = ProjectRole(workspaceRole)
	return f, nil
}

// candidateProjectFactsSQL intentionally over-selects only the small set of
// Projects that can plausibly resolve to readable access. Resolve remains the
// final authority in Service.ListVisibleProjectIDs, so changes to the role
// matrix cannot accidentally turn this query into a second authorization
// implementation.
const candidateProjectFactsSQL = `
SELECT
    p.id::text,
    p.workspace_id::text,
    COALESCE(extra.visibility, 'workspace') AS visibility,
    COALESCE(owner_member.role, '') AS owner_workspace_role,
    COALESCE(direct_grant.role, '') AS direct_grant_role,
    COALESCE(workspace_grant.role, '') AS workspace_grant_role,
    observer.global_observer
FROM project p
LEFT JOIN r2d_project_extra extra
  ON extra.project_id = p.id::text
LEFT JOIN member owner_member
  ON owner_member.workspace_id = p.workspace_id
 AND owner_member.user_id = $1::uuid
LEFT JOIN LATERAL (
    SELECT g.role
    FROM r2d_project_grants g
    WHERE g.project_id = p.id::text
      AND g.principal_type = 'user'
      AND g.principal_id = $1::text
    LIMIT 1
) direct_grant ON true
LEFT JOIN LATERAL (
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
) workspace_grant ON true
CROSS JOIN LATERAL (
    SELECT EXISTS (
        SELECT 1
        FROM r2d_global_roles global_role
        WHERE global_role.user_id = $1::text
          AND global_role.role = 'global_observer'
    ) AS global_observer
) observer
WHERE observer.global_observer
   OR owner_member.role IN ('owner', 'admin')
   OR (
        owner_member.role = 'member'
        AND COALESCE(extra.visibility, 'workspace') = 'workspace'
   )
   OR direct_grant.role IS NOT NULL
   OR workspace_grant.role IS NOT NULL
ORDER BY p.id
`

func (s *PostgresStore) ListCandidateProjectFacts(ctx context.Context, userID string) ([]ProjectFacts, error) {
	rows, err := s.db.Query(ctx, candidateProjectFactsSQL, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	facts := make([]ProjectFacts, 0)
	for rows.Next() {
		var f ProjectFacts
		var visibility, ownerRole, directRole, workspaceRole string
		if err := rows.Scan(
			&f.ProjectID,
			&f.OwnerWorkspaceID,
			&visibility,
			&ownerRole,
			&directRole,
			&workspaceRole,
			&f.GlobalObserver,
		); err != nil {
			return nil, err
		}
		f.Visibility = Visibility(visibility)
		f.OwnerWorkspaceRole = WorkspaceRole(ownerRole)
		f.DirectGrantRole = ProjectRole(directRole)
		f.WorkspaceGrantRole = ProjectRole(workspaceRole)
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return facts, nil
}
