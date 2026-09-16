package handler

import (
	"errors"
	"net/http"

	"github.com/multica-ai/multica/server/internal/r2dauth"
)

type projectCapabilitiesResponse struct {
	ProjectID      string              `json:"project_id"`
	Role           r2dauth.ProjectRole `json:"role,omitempty"`
	GlobalObserver bool                `json:"global_observer"`
	Read           bool                `json:"read"`
	Contribute     bool                `json:"contribute"`
	Manage         bool                `json:"manage"`
	Share          bool                `json:"share"`
	ViewResources  bool                `json:"view_resources"`
}

func projectCapabilitiesPayload(projectID string, caps r2dauth.ProjectCapabilities) projectCapabilitiesResponse {
	return projectCapabilitiesResponse{
		ProjectID:      projectID,
		Role:           caps.Role,
		GlobalObserver: caps.GlobalObserver,
		Read:           caps.Read,
		Contribute:     caps.Contribute,
		Manage:         caps.Manage,
		Share:          caps.Share,
		ViewResources:  caps.ViewResources,
	}
}

// GetProjectCapabilities is the client affordance contract for Project ACL.
// The response is derived from central r2dauth facts on every request; clients
// must not reconstruct role ordering from Workspace state. The endpoint itself
// requires Project read and preserves non-disclosure for callers with no access.
func (h *Handler) GetProjectCapabilities(w http.ResponseWriter, r *http.Request) {
	userID, projectID, ok := projectSharingRequestContext(w, r)
	if !ok {
		return
	}

	authz := r2dauth.NewService(r2dauth.NewPostgresStore(h.DB))
	caps, err := authz.Capabilities(r.Context(), userID, projectID)
	if err != nil {
		if errors.Is(err, r2dauth.ErrProjectNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
		} else {
			writeError(w, http.StatusInternalServerError, "project capability lookup failed")
		}
		return
	}
	if !caps.Read {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}

	writeJSON(w, http.StatusOK, projectCapabilitiesPayload(projectID, caps))
}
