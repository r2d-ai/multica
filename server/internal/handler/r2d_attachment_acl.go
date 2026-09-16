package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// r2dAuthorizeAttachmentRead reports whether userID may read an attachment
// whose Project binding was resolved by R2DLoadAttachmentACLTarget.
//
// A Project-backed attachment follows the central Project policy (viewer,
// grant, owner-Workspace member, or global observer). A projectless attachment
// has no Project to share and stays Workspace-private: membership only, no
// cross-Workspace grant and no observer widening.
func (h *Handler) r2dAuthorizeAttachmentRead(ctx context.Context, userID string, target db.R2DAttachmentACLTarget) (bool, error) {
	if target.ProjectID == "" {
		return h.r2dAttachmentWorkspaceMember(ctx, userID, target.WorkspaceID)
	}
	facts, err := h.Queries.R2DLoadProjectAccessFacts(ctx, userID, target.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if facts.OwnerWorkspaceID != target.WorkspaceID {
		// Project-backed Issues are expected to remain in the Project owner's
		// Workspace. Refuse corrupt/mismatched rows rather than widening scope.
		return false, nil
	}
	return r2dauth.Resolve(r2dHandlerProjectFacts(facts)).Can(r2dauth.OperationRead), nil
}

// r2dAuthorizeAttachmentManage reports whether userID holds Project manage over
// the attachment's owning Issue. A projectless attachment has no Project to
// manage, so it keeps DeleteAttachment's uploader/Workspace-admin rules.
func (h *Handler) r2dAuthorizeAttachmentManage(ctx context.Context, userID string, target db.R2DAttachmentACLTarget) (bool, error) {
	if target.ProjectID == "" {
		return false, nil
	}
	facts, err := h.Queries.R2DLoadProjectAccessFacts(ctx, userID, target.ProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if facts.OwnerWorkspaceID != target.WorkspaceID {
		return false, nil
	}
	return r2dauth.Resolve(r2dHandlerProjectFacts(facts)).Can(r2dauth.OperationManage), nil
}

// r2dAttachmentWorkspaceMember is the projectless-attachment boundary: the
// caller must be a member of the owning Workspace. It never consults Project
// grants, so a foreign collaborator cannot reach Workspace-private files.
func (h *Handler) r2dAttachmentWorkspaceMember(ctx context.Context, userID, workspaceID string) (bool, error) {
	if h.MembershipCache.Get(ctx, userID, workspaceID) {
		return true, nil
	}
	if _, err := h.getWorkspaceMember(ctx, userID, workspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	h.MembershipCache.Set(ctx, userID, workspaceID)
	return true, nil
}
