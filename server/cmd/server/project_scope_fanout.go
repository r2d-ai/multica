package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// projectScopedEventTypes is the allowlist of event types eligible for
// ScopeProject fanout. It is an allowlist, not a denylist, on purpose: a new
// event type must be added deliberately after its payload is reviewed for
// owner-Workspace-only fields. Events that never describe Project content
// (member:*, agent:*, daemon:*, vcs_connection:*, project_resource:*) are
// absent, so they can never reach a foreign collaborator even when the
// producer runs in the Project owner's Workspace.
var projectScopedEventTypes = map[string]bool{
	protocol.EventIssueCreated:            true,
	protocol.EventIssueUpdated:            true,
	protocol.EventIssueDeleted:            true,
	protocol.EventIssueAttachmentsChanged: true,
	protocol.EventIssueMetadataChanged:    true,
	protocol.EventIssueLabelsChanged:      true,
	protocol.EventIssuePropertiesChanged:  true,
	protocol.EventIssueReactionAdded:      true,
	protocol.EventIssueReactionRemoved:    true,
	protocol.EventCommentCreated:          true,
	protocol.EventCommentUpdated:          true,
	protocol.EventCommentDeleted:          true,
	protocol.EventCommentResolved:         true,
	protocol.EventCommentUnresolved:       true,
}

// projectScopedLeakKeys are owner-Workspace-only keys that must never appear in
// a Project-scoped frame. They are stripped recursively so a nested object
// (issue -> attachment, comment -> author, ...) cannot smuggle one through.
//
// The list mirrors the P07 leak boundary: repository URLs and VCS connection
// ids belong to the owner Workspace's integrations, daemon/runtime ids to its
// execution inventory, member lists to its membership, and source_context /
// signed download URLs to owner-only read paths. A cross-Workspace Project
// grant must not widen any of them.
var projectScopedLeakKeys = map[string]bool{
	"source_context":          true,
	"repo_url":                true,
	"repository_url":          true,
	"clone_url":               true,
	"download_url":            true,
	"attachment_download_url": true,
	"daemon_id":               true,
	"runtime_id":              true,
	"vcs_connection_id":       true,
	"workspace_members":       true,
	"members":                 true,
	// In-process-only diff fields; already dropped by projectOutbound on the
	// Workspace path, kept here so the Project path cannot drift from it.
	"prev_description": true,
	"prev_title":       true,
}

// projectScopeResolver resolves the owning Project of an Issue. A projectless
// (or unknown) issue returns an empty id, which keeps the event on the owner
// Workspace fanout only.
type projectScopeResolver interface {
	ResolveIssueProject(ctx context.Context, issueID string) (string, error)
}

// dbProjectScopeResolver resolves issue -> project with the same query the HTTP
// ACL path uses (R2DLoadIssueACLTarget), so realtime and HTTP cannot disagree
// about which Project an issue belongs to.
type dbProjectScopeResolver struct{ q *db.Queries }

func newProjectScopeResolver(q *db.Queries) *dbProjectScopeResolver {
	return &dbProjectScopeResolver{q: q}
}

func (r *dbProjectScopeResolver) ResolveIssueProject(ctx context.Context, issueID string) (string, error) {
	target, err := r.q.R2DLoadIssueACLTarget(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return target.ProjectID, nil
}

const projectScopeResolveTimeout = 2 * time.Second

// broadcastProjectScoped fans an issue-shaped event out to ScopeProject when
// the payload names a Project. It runs in addition to the Workspace fanout,
// never instead of it, and is a no-op for projectless issues and for event
// types outside the allowlist.
func broadcastProjectScoped(e events.Event, b realtime.Broadcaster, resolver projectScopeResolver) {
	if resolver == nil || e.WorkspaceID == "" || !projectScopedEventTypes[e.Type] {
		return
	}

	projectID, issueID := projectScopeTarget(e.Payload)
	if projectID == "" && issueID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), projectScopeResolveTimeout)
		resolved, err := resolver.ResolveIssueProject(ctx, issueID)
		cancel()
		if err != nil {
			// A resolution failure must not drop the owner-Workspace fanout that
			// already happened; log and skip the Project room for this event.
			slog.Warn("realtime: project scope resolution failed",
				"event_type", e.Type, "issue_id", issueID, "error", err)
			return
		}
		projectID = resolved
	}
	if projectID == "" {
		return
	}

	frame, err := json.Marshal(map[string]any{
		"type":       e.Type,
		"payload":    projectScopedPayload(e.Type, e.Payload),
		"actor_id":   e.ActorID,
		"actor_type": e.ActorType,
	})
	if err != nil {
		slog.Error("realtime: failed to marshal project-scoped event", "event_type", e.Type, "error", err)
		return
	}
	realtime.M.RecordEvent(e.Type)
	b.BroadcastToScope(realtime.ScopeProject, projectID, frame)
}

// projectScopeTarget extracts the Project id and Issue id from an issue-shaped
// payload. IssueResponse fields win over the bare issue_id keys used by
// auxiliary events (labels, properties, reactions, attachments, deleted).
func projectScopeTarget(payload any) (projectID, issueID string) {
	if payload == nil {
		return "", ""
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", ""
	}
	var probe struct {
		Issue *struct {
			ID        string `json:"id"`
			ProjectID string `json:"project_id"`
		} `json:"issue"`
		Comment *struct {
			IssueID string `json:"issue_id"`
		} `json:"comment"`
		IssueID string `json:"issue_id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", ""
	}
	// A full issue object is authoritative: its project_id may legitimately be
	// null (projectless), in which case no resolver round-trip is needed.
	if probe.Issue != nil {
		return probe.Issue.ProjectID, probe.Issue.ID
	}
	if probe.IssueID != "" {
		return "", probe.IssueID
	}
	if probe.Comment != nil && probe.Comment.IssueID != "" {
		return "", probe.Comment.IssueID
	}
	return "", ""
}

// projectScopedPayload renders the Project-safe payload for one event: the same
// internal-key projection the Workspace fanout uses, with every owner-Workspace
// key stripped.
func projectScopedPayload(eventType string, payload any) any {
	return stripProjectScopedLeaks(projectOutbound(eventType, payload))
}

// stripProjectScopedLeaks walks maps and slices and removes every leak key. The
// input is never mutated: projectOutbound may already have copied the top-level
// map, but a payload this function did not copy must stay intact for any other
// subscriber.
func stripProjectScopedLeaks(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, item := range value {
			if projectScopedLeakKeys[k] {
				continue
			}
			out[k] = stripProjectScopedLeaks(item)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = stripProjectScopedLeaks(item)
		}
		return out
	default:
		return v
	}
}
