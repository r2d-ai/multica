package handler

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/r2dauth"
)

func TestProjectCapabilitiesPayload(t *testing.T) {
	t.Parallel()

	got := projectCapabilitiesPayload("project-1", r2dauth.ProjectCapabilities{
		Role:           r2dauth.ProjectRoleMember,
		GlobalObserver: true,
		Read:           true,
		Contribute:     true,
		Manage:         false,
		Share:          false,
		ViewResources:  false,
	})

	if got.ProjectID != "project-1" || got.Role != r2dauth.ProjectRoleMember || !got.GlobalObserver || !got.Read || !got.Contribute || got.Manage || got.Share || got.ViewResources {
		t.Fatalf("payload = %#v", got)
	}
}
