package service

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// EnqueueProjectSquadTask is the narrow P06-C enqueue seam for a human who
// explicitly selects one Squad to work on an already-authorized Project Issue.
// It reuses the mention-style leader path: the task is stamped is_leader_task
// and squad_id so the daemon injects the Squad briefing, while the foreign
// Workspace Squad leader is task-scoped and is NOT persisted into
// issue.assignee_id. Sharing the Project therefore never turns Squad identity,
// roster or configuration into Project-visible data.
//
// Squad remains Workspace-owned: leaderID and squadID are resolved by the caller
// from the durable Squad row, and authorization must be completed before this
// method is called. TaskService remains unaware of R2D ACL.
func (s *TaskService) EnqueueProjectSquadTask(ctx context.Context, issue db.Issue, leaderID, squadID, actorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTask(
		ctx,
		issue,
		leaderID,
		pgtype.UUID{}, // no trigger comment: this is an explicit Project run
		true,          // squad leader task: injects the squad briefing at claim time
		squadID,
		false, // normal session rules; manual rerun owns force-fresh semantics
		"",
		actorUserID,
		pgtype.UUID{}, // not a rerun
		OriginNamed,
	)
}
