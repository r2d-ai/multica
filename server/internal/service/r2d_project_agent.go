package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// EnqueueProjectAgentTask is the narrow P06-B enqueue seam for a human who
// explicitly selects one Agent to work on an already-authorized Project Issue.
// It deliberately uses the mention-style task path: the foreign Workspace Agent
// is task-scoped and is NOT persisted into issue.assignee_id, so sharing the
// Project does not turn Agent identity/configuration into Project-visible data.
//
// actorUserID is stamped as the accountable direct human. Authorization must be
// completed before calling this method; TaskService remains unaware of R2D ACL.
func (s *TaskService) EnqueueProjectAgentTask(ctx context.Context, issue db.Issue, agentID, actorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(
		ctx,
		issue,
		agentID,
		pgtype.UUID{}, // no trigger comment: this is an explicit Project run
		false,         // direct Agent run, not a Squad leader task
		pgtype.UUID{},
		false, // normal session rules; manual rerun owns force-fresh semantics
		"",
		actorUserID,
		pgtype.UUID{}, // not a rerun
		OriginNamed,
	)
}
