package r2dcore

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DB is the narrow read surface needed by the cross-workspace core queries.
// Policy remains in r2dauth; this package only loads rows after the caller has
// already selected/authorized their project ids.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type ProjectFilter struct {
	Query    string
	Status   string
	Priority string
	Limit    int
	Offset   int
}

type WorkspaceSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

type ProjectRecord struct {
	Project        db.Project
	OwnerWorkspace WorkspaceSummary
}

type ProjectStore struct {
	db DB
}

func NewProjectStore(database DB) *ProjectStore {
	return &ProjectStore{db: database}
}

const projectColumns = `
p.id, p.workspace_id, p.title, p.description, p.icon, p.status,
p.lead_type, p.lead_id, p.created_at, p.updated_at, p.priority,
p.start_date, p.due_date`

func scanProject(row pgx.Row) (ProjectRecord, error) {
	var out ProjectRecord
	var ownerID pgtype.UUID
	var ownerAvatar pgtype.Text
	err := row.Scan(
		&out.Project.ID,
		&out.Project.WorkspaceID,
		&out.Project.Title,
		&out.Project.Description,
		&out.Project.Icon,
		&out.Project.Status,
		&out.Project.LeadType,
		&out.Project.LeadID,
		&out.Project.CreatedAt,
		&out.Project.UpdatedAt,
		&out.Project.Priority,
		&out.Project.StartDate,
		&out.Project.DueDate,
		&ownerID,
		&out.OwnerWorkspace.Name,
		&out.OwnerWorkspace.Slug,
		&ownerAvatar,
	)
	if err != nil {
		return ProjectRecord{}, err
	}
	out.OwnerWorkspace.ID = uuidString(ownerID)
	if ownerAvatar.Valid {
		out.OwnerWorkspace.AvatarURL = ownerAvatar.String
	}
	return out, nil
}

func (s *ProjectStore) Get(ctx context.Context, projectID string) (ProjectRecord, error) {
	row := s.db.QueryRow(ctx, `
SELECT `+projectColumns+`, w.id, w.name, w.slug, w.avatar_url
FROM project p
JOIN workspace w ON w.id = p.workspace_id
WHERE p.id = $1::uuid`, projectID)
	return scanProject(row)
}

func (s *ProjectStore) List(ctx context.Context, visibleProjectIDs []string, filter ProjectFilter) ([]ProjectRecord, error) {
	if len(visibleProjectIDs) == 0 {
		return []ProjectRecord{}, nil
	}
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 100 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}

	query := strings.TrimSpace(filter.Query)
	rows, err := s.db.Query(ctx, `
SELECT `+projectColumns+`, w.id, w.name, w.slug, w.avatar_url
FROM project p
JOIN workspace w ON w.id = p.workspace_id
WHERE p.id::text = ANY($1::text[])
  AND ($2::text = '' OR p.status = $2)
  AND ($3::text = '' OR p.priority = $3)
  AND (
      $4::text = ''
      OR p.title ILIKE '%' || $4 || '%'
      OR COALESCE(p.description, '') ILIKE '%' || $4 || '%'
      OR w.name ILIKE '%' || $4 || '%'
      OR w.slug ILIKE '%' || $4 || '%'
  )
ORDER BY p.updated_at DESC, p.id ASC
LIMIT $5 OFFSET $6`,
		visibleProjectIDs,
		filter.Status,
		filter.Priority,
		query,
		filter.Limit,
		filter.Offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ProjectRecord, 0)
	for rows.Next() {
		record, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ProjectStore) Exists(ctx context.Context, projectID string) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project WHERE id = $1::uuid)`, projectID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return exists, err
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	b := id.Bytes
	return strings.ToLower(
		hex4(b[0:4]) + "-" +
			hex4(b[4:6]) + "-" +
			hex4(b[6:8]) + "-" +
			hex4(b[8:10]) + "-" +
			hex4(b[10:16]),
	)
}

func hex4(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = digits[v>>4]
		out[i*2+1] = digits[v&0x0f]
	}
	return string(out)
}
