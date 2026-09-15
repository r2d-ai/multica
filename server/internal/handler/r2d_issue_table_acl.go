package handler

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func r2dHandlerProjectFacts(f db.R2DProjectAccessFacts) r2dauth.ProjectFacts {
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

// r2dReadableWorkspaceProjectIDs returns the Project subset a subject may read
// inside exactly one owning Workspace. SQL deliberately supplies facts only;
// r2dauth.Resolve remains the sole policy engine.
//
// Projectless Issues are not represented here. Callers decide whether their
// Workspace boundary permits projectless rows separately.
func (h *Handler) r2dReadableWorkspaceProjectIDs(ctx context.Context, userID, workspaceID string) ([]pgtype.UUID, error) {
	facts, err := h.Queries.R2DListWorkspaceProjectAccessFacts(ctx, userID, workspaceID)
	if err != nil {
		return nil, err
	}

	ids := make([]pgtype.UUID, 0, len(facts))
	for _, fact := range facts {
		if fact.OwnerWorkspaceID != workspaceID {
			// Defensive storage invariant: this query is expected to return only
			// Projects owned by workspaceID. Never broaden scope on corrupt data.
			continue
		}
		if !r2dauth.Resolve(r2dHandlerProjectFacts(fact)).Can(r2dauth.OperationRead) {
			continue
		}
		id, err := util.ParseUUID(fact.ProjectID)
		if err != nil {
			return nil, fmt.Errorf("invalid readable project id %q: %w", fact.ProjectID, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}
