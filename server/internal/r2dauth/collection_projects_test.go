package r2dauth

import (
	"reflect"
	"testing"
)

func TestProjectIDsForIssueCollection(t *testing.T) {
	t.Parallel()

	facts := []ProjectFacts{
		{ProjectID: "own-visible", OwnerWorkspaceID: "ws-a", Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleMember},
		{ProjectID: "own-private-hidden", OwnerWorkspaceID: "ws-a", Visibility: VisibilityPrivate, OwnerWorkspaceRole: WorkspaceRoleMember},
		{ProjectID: "foreign-direct", OwnerWorkspaceID: "ws-b", Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleViewer},
		{ProjectID: "foreign-ws", OwnerWorkspaceID: "ws-b", Visibility: VisibilityPrivate, WorkspaceGrantRole: ProjectRoleMember},
		{ProjectID: "foreign-ungranted", OwnerWorkspaceID: "ws-b", Visibility: VisibilityWorkspace},
		{ProjectID: "other-workspace-membership", OwnerWorkspaceID: "ws-c", Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleMember},
		{ProjectID: "observer-only", OwnerWorkspaceID: "ws-b", Visibility: VisibilityPrivate, GlobalObserver: true},
		{ProjectID: "corrupt-visibility", OwnerWorkspaceID: "ws-a", Visibility: Visibility("bogus"), OwnerWorkspaceRole: WorkspaceRoleOwner},
	}

	got := ProjectIDsForIssueCollection(facts, "ws-a")
	want := []string{"foreign-direct", "foreign-ws", "observer-only", "own-visible"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
}

func TestProjectIDsForIssueCollectionGlobalObserverSeesEverythingReadable(t *testing.T) {
	t.Parallel()

	facts := []ProjectFacts{
		{ProjectID: "p1", OwnerWorkspaceID: "ws-b", Visibility: VisibilityPrivate, GlobalObserver: true},
		{ProjectID: "p2", OwnerWorkspaceID: "ws-c", Visibility: VisibilityWorkspace, GlobalObserver: true},
	}
	got := ProjectIDsForIssueCollection(facts, "ws-a")
	want := []string{"p1", "p2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
}
