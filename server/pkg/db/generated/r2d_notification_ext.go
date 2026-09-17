package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

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

// R2DInboxItemRow is a recipient-scoped inbox row plus the backing Issue's
// Project id (empty for a projectless or issue-less notification). It embeds
// ListInboxItemsRow so the ordinary response mapper keeps working, and carries
// only the extra authorization input the read path needs.
//
// Delivery writes every Project-grant row under the issue OWNER Workspace, so a
// foreign collaborator's active Workspace never matches these rows. Read paths
// therefore have to look rows up by recipient and decide visibility from the
// current Project ACL instead of trusting the Workspace filter alone.
type R2DInboxItemRow struct {
	ListInboxItemsRow
	ProjectID string
}

// R2DInboxUnreadRow is the minimal projection an unread count needs: where the
// row lives and which Project backs it. Visibility policy stays in the handler.
type R2DInboxUnreadRow struct {
	WorkspaceID string
	ProjectID   string
}

// R2DInboxWorkspaceCountRow is one workspace's deduplicated unread count in the
// cross-workspace summary.
type R2DInboxWorkspaceCountRow struct {
	WorkspaceID string
	Count       int64
}

func scanR2DInboxItemRows(rows pgx.Rows) ([]R2DInboxItemRow, error) {
	out := make([]R2DInboxItemRow, 0)
	for rows.Next() {
		var r R2DInboxItemRow
		if err := rows.Scan(
			&r.ID,
			&r.WorkspaceID,
			&r.RecipientType,
			&r.RecipientID,
			&r.Type,
			&r.Severity,
			&r.IssueID,
			&r.Title,
			&r.Body,
			&r.Read,
			&r.Archived,
			&r.CreatedAt,
			&r.ActorType,
			&r.ActorID,
			&r.Details,
			&r.IssueStatus,
			&r.IssuePriority,
			&r.ProjectID,
		); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

const r2dInboxItemReadColumns = `
       iss.status AS issue_status,
       iss.priority AS issue_priority,
       COALESCE(iss.project_id::text, '') AS project_id`

// R2DListInboxItemsForRecipient returns every non-archived inbox row addressed
// to recipientID, across ALL workspaces. Authorization is deliberately not in
// the WHERE clause: rows are recipient-bound here so no caller can enumerate
// another user's rows, and the caller applies Project ACL visibility before
// turning any row into a response.
func (q *Queries) R2DListInboxItemsForRecipient(ctx context.Context, recipientID string) ([]R2DInboxItemRow, error) {
	rows, err := q.db.Query(ctx, `
SELECT i.*,`+r2dInboxItemReadColumns+`
FROM inbox_item i
LEFT JOIN issue iss ON iss.id = i.issue_id
WHERE i.recipient_type = 'member'
  AND i.recipient_id = $1::uuid
  AND i.archived = false
ORDER BY i.created_at DESC, i.id DESC`, recipientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanR2DInboxItemRows(rows)
}

// R2DListArchivedInboxItemsForRecipient is the archived counterpart of
// R2DListInboxItemsForRecipient: recipient-scoped across workspaces, no ACL
// predicate, same group/dedup contract as the upstream workspace-scoped query
// (see inbox.sql). Group ids are UUIDs, so grouping is safe across workspaces.
func (q *Queries) R2DListArchivedInboxItemsForRecipient(ctx context.Context, recipientID string) ([]R2DInboxItemRow, error) {
	rows, err := q.db.Query(ctx, `
WITH eligible_archived AS MATERIALIZED (
    SELECT i.id,
           COALESCE(i.issue_id, i.id) AS group_id,
           i.created_at,
           i.details
    FROM inbox_item i
    WHERE i.recipient_type = 'member'
      AND i.recipient_id = $1::uuid
      AND i.archived = true
      AND (i.issue_id IS NULL OR NOT EXISTS (
          SELECT 1
          FROM inbox_item active
          WHERE active.workspace_id = i.workspace_id
            AND active.recipient_type = i.recipient_type
            AND active.recipient_id = i.recipient_id
            AND active.issue_id = i.issue_id
            AND active.archived = false
      ))
), newest_groups AS (
    SELECT DISTINCT ON (group_id)
           group_id,
           id AS newest_id,
           created_at AS newest_created_at
    FROM eligible_archived
    ORDER BY group_id, created_at DESC, id DESC
), limited_groups AS (
    SELECT group_id, newest_id
    FROM newest_groups
    ORDER BY newest_created_at DESC, newest_id DESC
    LIMIT 200
), comment_anchors AS (
    SELECT DISTINCT ON (archived.group_id)
           archived.group_id,
           archived.id
    FROM eligible_archived archived
    JOIN limited_groups selected USING (group_id)
    WHERE NULLIF(archived.details->>'comment_id', '') IS NOT NULL
    ORDER BY archived.group_id, archived.created_at DESC, archived.id DESC
), selected_ids AS (
    SELECT newest_id AS id FROM limited_groups
    UNION
    SELECT id FROM comment_anchors
)
SELECT i.*,`+r2dInboxItemReadColumns+`
FROM inbox_item i
JOIN selected_ids selected ON selected.id = i.id
LEFT JOIN issue iss ON iss.id = i.issue_id
ORDER BY i.created_at DESC, i.id DESC`, recipientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanR2DInboxItemRows(rows)
}

// R2DGetInboxItemForRecipient loads one inbox row by id and recipient,
// regardless of Workspace. The (id, recipient) pair is the enumeration
// boundary: another user's row, or a recipient id that does not own the row,
// is simply absent, so mutation callers get a non-disclosing 404. ProjectID is
// returned separately for the caller's current-ACL check.
func (q *Queries) R2DGetInboxItemForRecipient(ctx context.Context, itemID, recipientID string) (InboxItem, string, error) {
	var item InboxItem
	var projectID string
	err := q.db.QueryRow(ctx, `
SELECT i.*, COALESCE(iss.project_id::text, '') AS project_id
FROM inbox_item i
LEFT JOIN issue iss ON iss.id = i.issue_id
WHERE i.id = $1::uuid
  AND i.recipient_type = 'member'
  AND i.recipient_id = $2::uuid`, itemID, recipientID).Scan(
		&item.ID,
		&item.WorkspaceID,
		&item.RecipientType,
		&item.RecipientID,
		&item.Type,
		&item.Severity,
		&item.IssueID,
		&item.Title,
		&item.Body,
		&item.Read,
		&item.Archived,
		&item.CreatedAt,
		&item.ActorType,
		&item.ActorID,
		&item.Details,
		&projectID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return InboxItem{}, "", pgx.ErrNoRows
	}
	return item, projectID, err
}

// R2DListUnreadInboxRowsForRecipient returns the (workspace, project) pair of
// every VISIBLE unread inbox row for one recipient, across workspaces, after
// the same newest-per-issue dedup the Inbox list applies. The dedup key and
// ordering mirror R2DListUnreadInboxSummaryRows so the sidebar badge and the
// list cannot disagree; grouping before the caller's Project ACL filter is safe
// because every row in a group shares one Project. The caller filters by
// current Project ACL and counts; the payload stays tiny so a count endpoint
// never loads notification bodies.
func (q *Queries) R2DListUnreadInboxRowsForRecipient(ctx context.Context, recipientID string) ([]R2DInboxUnreadRow, error) {
	rows, err := q.db.Query(ctx, `
SELECT newest.workspace_id::text, newest.project_id
FROM (
    SELECT DISTINCT ON (i.workspace_id, COALESCE(i.issue_id, i.id))
        i.workspace_id,
        COALESCE(iss.project_id::text, '') AS project_id,
        i.read
    FROM inbox_item i
    LEFT JOIN issue iss ON iss.id = i.issue_id
    WHERE i.recipient_type = 'member'
      AND i.recipient_id = $1::uuid
      AND i.archived = false
    ORDER BY i.workspace_id, COALESCE(i.issue_id, i.id), i.created_at DESC, i.id DESC
) newest
WHERE newest.read = false`, recipientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]R2DInboxUnreadRow, 0)
	for rows.Next() {
		var r R2DInboxUnreadRow
		if err := rows.Scan(&r.WorkspaceID, &r.ProjectID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// R2DListUnreadInboxSummaryRows is the ACL-filtered cross-workspace unread
// summary. It keeps the upstream newest-row-per-issue dedup and the member join
// (so a workspace the user has left never contributes), and drops rows whose
// backing Project the user cannot currently read. readableProjectIDs is the
// complete set of Projects the user may read; an empty set leaves only
// projectless rows, which is the fail-closed default.
func (q *Queries) R2DListUnreadInboxSummaryRows(ctx context.Context, recipientID string, readableProjectIDs []string) ([]R2DInboxWorkspaceCountRow, error) {
	rows, err := q.db.Query(ctx, `
SELECT newest.workspace_id::text, count(*) AS count
FROM (
    SELECT DISTINCT ON (i.workspace_id, COALESCE(i.issue_id, i.id))
        i.workspace_id, i.read
    FROM inbox_item i
    JOIN member m ON m.workspace_id = i.workspace_id AND m.user_id = i.recipient_id
    LEFT JOIN issue iss ON iss.id = i.issue_id
    WHERE i.recipient_type = 'member'
      AND i.recipient_id = $1::uuid
      AND i.archived = false
      AND (iss.project_id IS NULL OR iss.project_id::text = ANY($2::text[]))
    ORDER BY i.workspace_id, COALESCE(i.issue_id, i.id), i.created_at DESC, i.id DESC
) newest
WHERE newest.read = false
GROUP BY newest.workspace_id
ORDER BY newest.workspace_id`, recipientID, readableProjectIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]R2DInboxWorkspaceCountRow, 0)
	for rows.Next() {
		var r R2DInboxWorkspaceCountRow
		if err := rows.Scan(&r.WorkspaceID, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// r2dInboxVisiblePredicate is the shared batch-mutation visibility guard. It
// keeps a row when it is projectless (including an issue that no longer exists,
// preserving native behavior) or when its Project is in the caller's readable
// set. The parameter position is $3, matching the four callers below.
const r2dInboxVisiblePredicate = `
  AND (
      i.issue_id IS NULL
      OR NOT EXISTS (
          SELECT 1 FROM issue iss
          WHERE iss.id = i.issue_id AND iss.project_id IS NOT NULL
      )
      OR EXISTS (
          SELECT 1 FROM issue iss
          WHERE iss.id = i.issue_id AND iss.project_id::text = ANY($3::text[])
      )
  )`

// R2DMarkAllInboxReadVisible is the ACL-aware MarkAllInboxRead: only rows whose
// backing Project the caller can read (or projectless rows) are touched, so the
// returned count cannot reveal an inaccessible Project's notifications.
func (q *Queries) R2DMarkAllInboxReadVisible(ctx context.Context, workspaceID, recipientID string, readableProjectIDs []string) (int64, error) {
	tag, err := q.db.Exec(ctx, `
UPDATE inbox_item i SET read = true
WHERE i.workspace_id = $1::uuid
  AND i.recipient_type = 'member'
  AND i.recipient_id = $2::uuid
  AND i.archived = false
  AND i.read = false`+r2dInboxVisiblePredicate, workspaceID, recipientID, readableProjectIDs)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// R2DArchiveAllInboxVisible is the ACL-aware ArchiveAllInbox.
func (q *Queries) R2DArchiveAllInboxVisible(ctx context.Context, workspaceID, recipientID string, readableProjectIDs []string) (int64, error) {
	tag, err := q.db.Exec(ctx, `
UPDATE inbox_item i SET archived = true
WHERE i.workspace_id = $1::uuid
  AND i.recipient_type = 'member'
  AND i.recipient_id = $2::uuid
  AND i.archived = false`+r2dInboxVisiblePredicate, workspaceID, recipientID, readableProjectIDs)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// R2DArchiveAllReadInboxVisible is the ACL-aware ArchiveAllReadInbox: the
// newest-per-issue group is computed over visible rows only, so an inaccessible
// Project's rows never influence the archive decision or the returned count.
func (q *Queries) R2DArchiveAllReadInboxVisible(ctx context.Context, workspaceID, recipientID string, readableProjectIDs []string) (int64, error) {
	tag, err := q.db.Exec(ctx, `
WITH newest_groups AS (
    SELECT DISTINCT ON (COALESCE(i.issue_id, i.id))
           COALESCE(i.issue_id, i.id) AS group_id,
           i.read
    FROM inbox_item i
    WHERE i.workspace_id = $1::uuid
      AND i.recipient_type = 'member'
      AND i.recipient_id = $2::uuid
      AND i.archived = false`+r2dInboxVisiblePredicate+`
    ORDER BY COALESCE(i.issue_id, i.id), i.created_at DESC, i.id DESC
), read_groups AS (
    SELECT group_id
    FROM newest_groups
    WHERE read = true
)
UPDATE inbox_item i SET archived = true
FROM read_groups selected
WHERE i.workspace_id = $1::uuid
  AND i.recipient_type = 'member'
  AND i.recipient_id = $2::uuid
  AND i.archived = false
  AND COALESCE(i.issue_id, i.id) = selected.group_id`, workspaceID, recipientID, readableProjectIDs)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// R2DArchiveCompletedInboxVisible is the ACL-aware ArchiveCompletedInbox.
func (q *Queries) R2DArchiveCompletedInboxVisible(ctx context.Context, workspaceID, recipientID string, terminalStatusKeys, readableProjectIDs []string) (int64, error) {
	tag, err := q.db.Exec(ctx, `
UPDATE inbox_item i SET archived = true
WHERE i.workspace_id = $1::uuid
  AND i.recipient_type = 'member'
  AND i.recipient_id = $2::uuid
  AND i.archived = false
  AND i.issue_id IN (
      SELECT id FROM issue
      WHERE workspace_id = $1::uuid
        AND status = ANY($3::text[])
  )
  AND (
      NOT EXISTS (
          SELECT 1 FROM issue iss
          WHERE iss.id = i.issue_id AND iss.project_id IS NOT NULL
      )
      OR EXISTS (
          SELECT 1 FROM issue iss
          WHERE iss.id = i.issue_id AND iss.project_id::text = ANY($4::text[])
      )
  )`, workspaceID, recipientID, terminalStatusKeys, readableProjectIDs)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
