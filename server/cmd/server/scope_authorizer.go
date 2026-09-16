package main

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// scopeAuthQuerier is the narrow subset of db.Queries used by the scope
// authorizer. Declared as an interface so the authorizer can be unit tested
// with an in-memory fake (no DB required).
type scopeAuthQuerier interface {
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	GetIssue(ctx context.Context, id pgtype.UUID) (db.Issue, error)
	GetChatSession(ctx context.Context, id pgtype.UUID) (db.ChatSession, error)
	R2DLoadProjectAccessFacts(ctx context.Context, userID, projectID string) (db.R2DProjectAccessFacts, error)
}

// dbScopeAuthorizer implements realtime.ScopeAuthorizer for the per-task,
// per-chat, and per-project scopes (workspace/user scopes are validated by the
// hub itself against the connection identity). For task/chat it returns true
// only when the requested resource exists, belongs to the caller's workspace,
// and — for chat resources — was created by the caller (mirroring the HTTP
// creator-only access model). For project it defers to the central R2D project
// policy, which is not workspace-bound: a shared project may be owned by
// another workspace.
type dbScopeAuthorizer struct{ q scopeAuthQuerier }

func newScopeAuthorizer(q scopeAuthQuerier) *dbScopeAuthorizer { return &dbScopeAuthorizer{q: q} }

// scopeLookupErr converts a scope-resource query error into an authorizer
// result. A missing resource (pgx.ErrNoRows) is a legitimate denial — the
// HTTP layer treats not-found as 404 rather than 403, so the realtime layer
// reports it as a plain "forbidden" refusal. Any other error (pool
// exhaustion, a cancelled context, a network blip) is a transient lookup
// failure and must propagate so handleSubscribe reports "lookup_failed"
// instead of masking a database outage as a wave of permission denials.
func scopeLookupErr(err error) (bool, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func (a *dbScopeAuthorizer) AuthorizeScope(ctx context.Context, userID, workspaceID, scopeType, scopeID string) (bool, error) {
	if workspaceID == "" || scopeID == "" {
		return false, nil
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		return false, nil
	}
	idUUID, err := util.ParseUUID(scopeID)
	if err != nil {
		return false, nil
	}
	switch scopeType {
	case realtime.ScopeTask:
		task, err := a.q.GetAgentTask(ctx, idUUID)
		if err != nil {
			return scopeLookupErr(err)
		}
		// Issue tasks: visible to any workspace member.
		if task.IssueID.Valid {
			issue, err := a.q.GetIssue(ctx, task.IssueID)
			if err != nil {
				return scopeLookupErr(err)
			}
			return issue.WorkspaceID == wsUUID, nil
		}
		// Chat tasks: only the chat session's creator may subscribe, mirroring
		// the HTTP layer's creator-only access on chat resources.
		if task.ChatSessionID.Valid {
			sess, err := a.q.GetChatSession(ctx, task.ChatSessionID)
			if err != nil {
				return scopeLookupErr(err)
			}
			if sess.WorkspaceID != wsUUID {
				return false, nil
			}
			uidUUID, err := util.ParseUUID(userID)
			if err != nil || sess.CreatorID != uidUUID {
				return false, nil
			}
			return true, nil
		}
		return false, nil
	case realtime.ScopeChat:
		sess, err := a.q.GetChatSession(ctx, idUUID)
		if err != nil {
			return scopeLookupErr(err)
		}
		if sess.WorkspaceID != wsUUID {
			return false, nil
		}
		// Chat sessions are private to their creator (see handler/chat.go:
		// GetChatSession / SendChatMessage / MarkChatSessionRead all enforce
		// CreatorID == userID). The realtime layer must not weaken this:
		// otherwise any workspace member who learns a session_id could
		// subscribe to chat:message / chat:done / chat:session_read for a
		// peer's private chat.
		uidUUID, err := util.ParseUUID(userID)
		if err != nil || sess.CreatorID != uidUUID {
			return false, nil
		}
		return true, nil
	case realtime.ScopeProject:
		// Cross-Workspace Project room. The connection's workspaceID is the
		// collaborator's HOME workspace (HandleWebSocket checks membership in
		// it) and is deliberately not consulted here: Project authorization is
		// resolved from the Project's own facts, which may belong to another
		// workspace. Read access is the join requirement — the same predicate
		// the HTTP Project read path uses (r2dauth.Resolve(...).Can(read)).
		if strings.TrimSpace(userID) == "" {
			return false, nil
		}
		facts, err := a.q.R2DLoadProjectAccessFacts(ctx, userID, scopeID)
		if err != nil {
			return scopeLookupErr(err)
		}
		return r2dauth.Resolve(r2dScopeProjectFacts(facts)).Can(r2dauth.OperationRead), nil
	default:
		return false, nil
	}
}

// r2dScopeProjectFacts maps the storage projection onto the single policy
// input type. Policy stays in r2dauth.Resolve so the realtime join check cannot
// drift from the HTTP Project authorization.
func r2dScopeProjectFacts(f db.R2DProjectAccessFacts) r2dauth.ProjectFacts {
	return r2dauth.ProjectFacts{
		ProjectID:          f.ProjectID,
		OwnerWorkspaceID:   f.OwnerWorkspaceID,
		Visibility:         r2dauth.Visibility(f.Visibility),
		OwnerWorkspaceRole: r2dauth.WorkspaceRole(f.OwnerWorkspaceRole),
		DirectGrantRole:    r2dauth.ProjectRole(f.DirectGrantRole),
		WorkspaceGrantRole: r2dauth.ProjectRole(f.WorkspaceGrantRole),
		GlobalObserver:     f.GlobalObserver,
	}
}
