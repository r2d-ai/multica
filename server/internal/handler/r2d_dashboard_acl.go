package handler

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/r2dauth"
	"github.com/multica-ai/multica/server/internal/util"
)

// dashboardProjectScope is the ACL-resolved ?project_id= scope for one
// dashboard request. Exactly one field is populated: Single names the one
// Project the caller asked for and is authorized to read, while Visible is the
// readable-Project set a workspace-wide report must narrow to.
type dashboardProjectScope struct {
	Single  pgtype.UUID
	Visible []pgtype.UUID
}

// dashboardResolveProjectScope applies the central Project ACL to the
// caller-controlled ?project_id= filter the six dashboard rollups share.
//
// A single-Project request is answered only when r2dauth grants OperationRead
// on that Project; every other outcome is reported as nonexistent (404),
// matching GetProjectCapabilities and the other R2D read paths instead of
// confirming that a private Project exists. A workspace-wide request keeps its
// projectless rows — the caller already passed the Workspace-membership gate,
// and projectless work is Workspace-private — but narrows project-backed rows
// to the Projects r2dauth reports readable, so omitting the filter can no
// longer expose a private Project's spend.
func (h *Handler) dashboardResolveProjectScope(
	w http.ResponseWriter,
	r *http.Request,
) (dashboardProjectScope, bool) {
	projectID, ok := parseProjectIDParam(w, r)
	if !ok {
		return dashboardProjectScope{}, false
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return dashboardProjectScope{}, false
	}

	authz := r2dauth.NewService(r2dauth.NewPostgresStore(h.DB))
	if projectID.Valid {
		allowed, err := authz.Can(r.Context(), userID, util.UUIDToString(projectID), r2dauth.OperationRead)
		if err != nil && !errors.Is(err, r2dauth.ErrProjectNotFound) {
			writeError(w, http.StatusInternalServerError, "failed to resolve project access")
			return dashboardProjectScope{}, false
		}
		if !allowed {
			writeError(w, http.StatusNotFound, "project not found")
			return dashboardProjectScope{}, false
		}
		return dashboardProjectScope{Single: projectID}, true
	}

	visibleIDs, err := authz.ListVisibleProjectIDs(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve project visibility")
		return dashboardProjectScope{}, false
	}
	visible := make([]pgtype.UUID, 0, len(visibleIDs))
	for _, id := range visibleIDs {
		parsed, err := util.ParseUUID(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to resolve project visibility")
			return dashboardProjectScope{}, false
		}
		visible = append(visible, parsed)
	}
	return dashboardProjectScope{Visible: visible}, true
}
