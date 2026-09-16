package r2dauth

import (
	"context"
	"testing"
)

func TestResolveCapabilitiesResourcePrivacy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		facts         ProjectFacts
		read          bool
		contribute    bool
		manage        bool
		share         bool
		viewResources bool
	}{
		{
			name: "owner workspace member can read project resources",
			facts: ProjectFacts{Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleMember},
			read: true, contribute: true, viewResources: true,
		},
		{
			name: "owner workspace manager can read project resources",
			facts: ProjectFacts{Visibility: VisibilityPrivate, OwnerWorkspaceRole: WorkspaceRoleAdmin},
			read: true, contribute: true, manage: true, share: true, viewResources: true,
		},
		{
			name: "foreign viewer cannot read owner workspace resources",
			facts: ProjectFacts{Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleViewer},
			read: true,
		},
		{
			name: "foreign manager still cannot read owner workspace resources",
			facts: ProjectFacts{Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleManager},
			read: true, contribute: true, manage: true, share: true,
		},
		{
			name: "global observer cannot read owner workspace resources",
			facts: ProjectFacts{Visibility: VisibilityPrivate, GlobalObserver: true},
			read: true,
		},
		{
			name: "owner workspace member with explicit private grant can read resources",
			facts: ProjectFacts{
				Visibility: VisibilityPrivate,
				OwnerWorkspaceRole: WorkspaceRoleMember,
				DirectGrantRole: ProjectRoleViewer,
			},
			read: true, viewResources: true,
		},
		{
			name: "agent with explicit viewer grant does not gain human workspace resources",
			facts: ProjectFacts{
				Visibility: VisibilityPrivate,
				OwnerWorkspaceRole: WorkspaceRoleAgent,
				DirectGrantRole: ProjectRoleViewer,
			},
			read: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveCapabilities(tt.facts)
			if got.Read != tt.read || got.Contribute != tt.contribute || got.Manage != tt.manage || got.Share != tt.share || got.ViewResources != tt.viewResources {
				t.Fatalf("capabilities = %#v, want read=%t contribute=%t manage=%t share=%t resources=%t", got, tt.read, tt.contribute, tt.manage, tt.share, tt.viewResources)
			}
		})
	}
}

func TestServiceCapabilitiesUsesCentralFacts(t *testing.T) {
	t.Parallel()

	service := NewService(&fakeStore{factsByProject: map[string]ProjectFacts{
		"p1": {
			ProjectID: "p1",
			Visibility: VisibilityPrivate,
			WorkspaceGrantRole: ProjectRoleMember,
		},
	}})

	got, err := service.Capabilities(context.Background(), "user-1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Read || !got.Contribute || got.Manage || got.Share || got.ViewResources {
		t.Fatalf("capabilities = %#v", got)
	}
}
