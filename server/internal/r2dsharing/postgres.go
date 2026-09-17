package r2dsharing

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/r2dauth"
)

type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// r2dAssignableMembersSQL is duplicated verbatim from
// pkg/db/generated/r2d_acl_ext.go (the middleware copy). The store owns a
// different DB interface and pkg/db/generated must not import internal/, so the
// two texts cannot share a constant. Keep them byte-identical.
const r2dAssignableMembersSQL = `
SELECT DISTINCT u.id::text, u.name, COALESCE(u.email, ''), COALESCE(u.avatar_url, '')
FROM "user" u
JOIN member m ON m.user_id = u.id
WHERE m.workspace_id = (SELECT p.workspace_id FROM project p WHERE p.id = $1::uuid)
   OR m.workspace_id::text IN (
        SELECT g.principal_id
        FROM r2d_project_grants g
        WHERE g.project_id = $1::text
          AND g.principal_type = 'workspace'
   )
   OR u.id::text IN (
        SELECT g.principal_id
        FROM r2d_project_grants g
        WHERE g.project_id = $1::text
          AND g.principal_type = 'user'
   )
ORDER BY u.name`

type PostgresStore struct {
	db DB
}

func NewPostgresStore(db DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) GetVisibility(ctx context.Context, projectID string) (r2dauth.Visibility, error) {
	var visibility string
	err := s.db.QueryRow(ctx, `
SELECT COALESCE((
    SELECT visibility FROM r2d_project_extra WHERE project_id = $1
), 'workspace')`, projectID).Scan(&visibility)
	return r2dauth.Visibility(visibility), err
}

func (s *PostgresStore) SetVisibility(ctx context.Context, projectID string, visibility r2dauth.Visibility) error {
	_, err := s.db.Exec(ctx, `
INSERT INTO r2d_project_extra (project_id, visibility)
VALUES ($1, $2)
ON CONFLICT (project_id) DO UPDATE
SET visibility = EXCLUDED.visibility, updated_at = NOW()`, projectID, string(visibility))
	return err
}

func (s *PostgresStore) ListGrants(ctx context.Context, projectID string) ([]Grant, error) {
	rows, err := s.db.Query(ctx, `
SELECT
    g.id, g.project_id, g.principal_type, g.principal_id, g.role,
    g.created_by, g.created_at, g.updated_at,
    COALESCE(u.name, w.name, ''),
    CASE
        WHEN g.principal_type = 'user' THEN COALESCE(u.email, '')
        WHEN g.principal_type = 'workspace' THEN COALESCE(w.slug, '')
        ELSE ''
    END,
    COALESCE(u.avatar_url, w.avatar_url, '')
FROM r2d_project_grants g
LEFT JOIN "user" u
  ON g.principal_type = 'user' AND u.id::text = g.principal_id
LEFT JOIN workspace w
  ON g.principal_type = 'workspace' AND w.id::text = g.principal_id
WHERE g.project_id = $1
ORDER BY g.created_at ASC, g.id ASC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Grant, 0)
	for rows.Next() {
		var g Grant
		var principalType string
		var createdAt, updatedAt time.Time
		var name, secondary, avatar string
		if err := rows.Scan(
			&g.ID, &g.ProjectID, &principalType, &g.PrincipalID, &g.Role,
			&g.CreatedBy, &createdAt, &updatedAt,
			&name, &secondary, &avatar,
		); err != nil {
			return nil, err
		}
		g.PrincipalType = PrincipalType(principalType)
		g.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		g.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		if name != "" {
			g.Principal = &Principal{Type: g.PrincipalType, ID: g.PrincipalID, Name: name, Secondary: secondary, AvatarURL: avatar}
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) PrincipalExists(ctx context.Context, principalType PrincipalType, principalID string) (bool, error) {
	var exists bool
	var err error
	switch principalType {
	case PrincipalUser:
		err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM "user" WHERE id::text = $1)`, principalID).Scan(&exists)
	case PrincipalWorkspace:
		err = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workspace WHERE id::text = $1)`, principalID).Scan(&exists)
	default:
		return false, nil
	}
	return exists, err
}

func (s *PostgresStore) CreateGrant(ctx context.Context, grant Grant) (Grant, error) {
	var principalType string
	var createdAt, updatedAt time.Time
	err := s.db.QueryRow(ctx, `
INSERT INTO r2d_project_grants (
    id, project_id, principal_type, principal_id, role, created_by
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (project_id, principal_type, principal_id) DO NOTHING
RETURNING id, project_id, principal_type, principal_id, role, created_by, created_at, updated_at`,
		grant.ID, grant.ProjectID, string(grant.PrincipalType), grant.PrincipalID, grant.Role, grant.CreatedBy,
	).Scan(
		&grant.ID, &grant.ProjectID, &principalType, &grant.PrincipalID,
		&grant.Role, &grant.CreatedBy, &createdAt, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrDuplicateGrant
	}
	if err != nil {
		return Grant{}, err
	}
	grant.PrincipalType = PrincipalType(principalType)
	grant.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	grant.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return grant, nil
}

func (s *PostgresStore) UpdateGrantRole(ctx context.Context, projectID, grantID, role string) (Grant, error) {
	var g Grant
	var principalType string
	var createdAt, updatedAt time.Time
	err := s.db.QueryRow(ctx, `
UPDATE r2d_project_grants
SET role = $3, updated_at = NOW()
WHERE project_id = $1 AND id = $2
RETURNING id, project_id, principal_type, principal_id, role, created_by, created_at, updated_at`,
		projectID, grantID, role,
	).Scan(
		&g.ID, &g.ProjectID, &principalType, &g.PrincipalID,
		&g.Role, &g.CreatedBy, &createdAt, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrGrantNotFound
	}
	if err != nil {
		return Grant{}, err
	}
	g.PrincipalType = PrincipalType(principalType)
	g.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	g.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return g, nil
}

func (s *PostgresStore) DeleteGrant(ctx context.Context, projectID, grantID string) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM r2d_project_grants WHERE project_id = $1 AND id = $2`, projectID, grantID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ListAssignableMembers returns the people who may be assigned on the Project:
// owner-Workspace members, direct grantees, and members of granted Workspaces.
// Authorization is the caller's responsibility (see Service.AssignableActors).
func (s *PostgresStore) ListAssignableMembers(ctx context.Context, projectID string) ([]Principal, error) {
	rows, err := s.db.Query(ctx, r2dAssignableMembersSQL, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Principal, 0)
	for rows.Next() {
		var p Principal
		p.Type = PrincipalUser
		var email string
		if err := rows.Scan(&p.ID, &p.Name, &email, &p.AvatarURL); err != nil {
			return nil, err
		}
		p.Secondary = email
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) SearchPrincipals(ctx context.Context, principalType PrincipalType, query string, limit int) ([]Principal, error) {
	var (
		rows pgx.Rows
		err  error
	)
	switch principalType {
	case PrincipalUser:
		rows, err = s.db.Query(ctx, `
SELECT id::text, name, email, COALESCE(avatar_url, '')
FROM "user"
WHERE name ILIKE '%' || $1 || '%' OR email ILIKE '%' || $1 || '%'
ORDER BY
    CASE WHEN lower(email) = lower($1) THEN 0 ELSE 1 END,
    lower(name), lower(email), id
LIMIT $2`, query, limit)
	case PrincipalWorkspace:
		rows, err = s.db.Query(ctx, `
SELECT id::text, name, slug, COALESCE(avatar_url, '')
FROM workspace
WHERE name ILIKE '%' || $1 || '%' OR slug ILIKE '%' || $1 || '%'
ORDER BY
    CASE WHEN lower(slug) = lower($1) THEN 0 ELSE 1 END,
    lower(name), lower(slug), id
LIMIT $2`, query, limit)
	default:
		return nil, ErrInvalidQuery
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Principal, 0)
	for rows.Next() {
		var p Principal
		p.Type = principalType
		if err := rows.Scan(&p.ID, &p.Name, &p.Secondary, &p.AvatarURL); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
