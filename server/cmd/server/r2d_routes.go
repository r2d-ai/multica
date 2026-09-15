package main

import (
	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/handler"
)

// registerR2DProjectSharingRoutes mounts project-scoped ACL management under
// the authenticated user group, deliberately outside RequireWorkspaceMember.
// A project may grant a manager role to a user from another workspace; putting
// these routes behind the active workspace membership gate would reject that
// legitimate cross-workspace manager before r2dauth can evaluate the project.
func registerR2DProjectSharingRoutes(r chi.Router, h *handler.Handler) {
	r.Route("/api/projects/{id}/sharing", func(r chi.Router) {
		r.Get("/", h.GetProjectSharing)
		r.Patch("/", h.UpdateProjectSharing)
		r.Get("/directory", h.SearchProjectSharingDirectory)
		r.Post("/grants", h.CreateProjectGrant)
		r.Patch("/grants/{grantId}", h.UpdateProjectGrant)
		r.Delete("/grants/{grantId}", h.DeleteProjectGrant)
	})
}
