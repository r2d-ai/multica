package main

import (
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/handler"
)

// registerR2DProjectSharingRoutes mounts project-scoped R2D ACL endpoints under
// the authenticated user group, deliberately outside RequireWorkspaceMember.
// A project may grant access to a user from another workspace; putting these
// routes behind the active workspace membership gate would reject legitimate
// cross-workspace collaborators before r2dauth can evaluate the project.
func registerR2DProjectSharingRoutes(r chi.Router, h *handler.Handler) {
	// Resource routes are still owned by upstream and registered later under
	// RequireWorkspaceMember. Install the R2D guard here, before those routes
	// are mounted, so shared Project access never exposes the owner Workspace's
	// repo/runtime/local-directory inventory. Non-resource routes pass through.
	r.Use(h.ProjectResourceAccess)

	r.Get("/api/projects/{id}/capabilities", h.GetProjectCapabilities)

	r.Route("/api/projects/{id}/sharing", func(r chi.Router) {
		r.Get("/", h.GetProjectSharing)
		r.Patch("/", h.UpdateProjectSharing)
		r.Get("/directory", h.SearchProjectSharingDirectory)
		r.Post("/grants", h.CreateProjectGrant)
		r.Patch("/grants/{grantId}", h.UpdateProjectGrant)
		r.Delete("/grants/{grantId}", h.DeleteProjectGrant)
	})
}
