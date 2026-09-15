package r2dauth

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestResolveProjectAccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		facts         ProjectFacts
		wantRole      ProjectRole
		wantObserver  bool
		wantRead      bool
		wantContrib   bool
		wantManage    bool
		wantShare     bool
		wantHasRole   bool
	}{
		{
			name: "workspace member keeps upstream compatible access",
			facts: ProjectFacts{Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleMember},
			wantRole: ProjectRoleMember, wantRead: true, wantContrib: true, wantHasRole: true,
		},
		{
			name: "workspace owner manages private project",
			facts: ProjectFacts{Visibility: VisibilityPrivate, OwnerWorkspaceRole: WorkspaceRoleOwner},
			wantRole: ProjectRoleManager, wantRead: true, wantContrib: true, wantManage: true, wantShare: true, wantHasRole: true,
		},
		{
			name: "workspace admin manages private project",
			facts: ProjectFacts{Visibility: VisibilityPrivate, OwnerWorkspaceRole: WorkspaceRoleAdmin},
			wantRole: ProjectRoleManager, wantRead: true, wantContrib: true, wantManage: true, wantShare: true, wantHasRole: true,
		},
		{
			name: "ordinary member cannot implicitly see private project",
			facts: ProjectFacts{Visibility: VisibilityPrivate, OwnerWorkspaceRole: WorkspaceRoleMember},
		},
		{
			name: "private project direct viewer grant",
			facts: ProjectFacts{Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleViewer},
			wantRole: ProjectRoleViewer, wantRead: true, wantHasRole: true,
		},
		{
			name: "cross workspace member grant",
			facts: ProjectFacts{Visibility: VisibilityPrivate, WorkspaceGrantRole: ProjectRoleMember},
			wantRole: ProjectRoleMember, wantRead: true, wantContrib: true, wantHasRole: true,
		},
		{
			name: "strongest role wins across sources",
			facts: ProjectFacts{
				Visibility: VisibilityWorkspace,
				OwnerWorkspaceRole: WorkspaceRoleMember,
				DirectGrantRole: ProjectRoleViewer,
				WorkspaceGrantRole: ProjectRoleManager,
			},
			wantRole: ProjectRoleManager, wantRead: true, wantContrib: true, wantManage: true, wantShare: true, wantHasRole: true,
		},
		{
			name: "global observer is read only and not a project role",
			facts: ProjectFacts{Visibility: VisibilityPrivate, GlobalObserver: true},
			wantObserver: true, wantRead: true,
		},
		{
			name: "observer does not reduce an explicit member role",
			facts: ProjectFacts{Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleMember, GlobalObserver: true},
			wantRole: ProjectRoleMember, wantObserver: true, wantRead: true, wantContrib: true, wantHasRole: true,
		},
		{
			name: "agent membership gives no implicit project access",
			facts: ProjectFacts{Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleAgent},
		},
		{
			name: "agent may still receive explicit user grant",
			facts: ProjectFacts{Visibility: VisibilityPrivate, OwnerWorkspaceRole: WorkspaceRoleAgent, DirectGrantRole: ProjectRoleViewer},
			wantRole: ProjectRoleViewer, wantRead: true, wantHasRole: true,
		},
		{
			name: "unknown grant role fails closed for that source",
			facts: ProjectFacts{Visibility: VisibilityPrivate, DirectGrantRole: ProjectRole("owner")},
		},
		{
			name: "invalid visibility fails closed even for observer and grants",
			facts: ProjectFacts{Visibility: Visibility("public"), DirectGrantRole: ProjectRoleManager, GlobalObserver: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := Resolve(tt.facts)
			if d.Role != tt.wantRole {
				t.Errorf("role = %q, want %q", d.Role, tt.wantRole)
			}
			if d.GlobalObserver != tt.wantObserver {
				t.Errorf("global observer = %t, want %t", d.GlobalObserver, tt.wantObserver)
			}
			if got := d.Can(OperationRead); got != tt.wantRead {
				t.Errorf("read = %t, want %t", got, tt.wantRead)
			}
			if got := d.Can(OperationContribute); got != tt.wantContrib {
				t.Errorf("contribute = %t, want %t", got, tt.wantContrib)
			}
			if got := d.Can(OperationManage); got != tt.wantManage {
				t.Errorf("manage = %t, want %t", got, tt.wantManage)
			}
			if got := d.Can(OperationShare); got != tt.wantShare {
				t.Errorf("share = %t, want %t", got, tt.wantShare)
			}
			if got := d.HasProjectRole(); got != tt.wantHasRole {
				t.Errorf("HasProjectRole = %t, want %t", got, tt.wantHasRole)
			}
			if d.Can(Operation("unknown")) {
				t.Error("unknown operation must fail closed")
			}
		})
	}
}

type fakeStore struct {
	factsByProject map[string]ProjectFacts
	candidates     []ProjectFacts
	loadErr        error
	listErr        error
}

func (f *fakeStore) LoadProjectFacts(_ context.Context, _, projectID string) (ProjectFacts, error) {
	if f.loadErr != nil {
		return ProjectFacts{}, f.loadErr
	}
	facts, ok := f.factsByProject[projectID]
	if !ok {
		return ProjectFacts{}, ErrProjectNotFound
	}
	return facts, nil
}

func (f *fakeStore) ListCandidateProjectFacts(_ context.Context, _ string) ([]ProjectFacts, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.candidates, nil
}

func TestServiceCanUsesCentralResolver(t *testing.T) {
	t.Parallel()

	store := &fakeStore{factsByProject: map[string]ProjectFacts{
		"p1": {ProjectID: "p1", Visibility: VisibilityPrivate, WorkspaceGrantRole: ProjectRoleMember},
	}}
	service := NewService(store)

	canRead, err := service.Can(context.Background(), "user-1", "p1", OperationRead)
	if err != nil || !canRead {
		t.Fatalf("read = %t, err = %v; want true, nil", canRead, err)
	}
	canManage, err := service.Can(context.Background(), "user-1", "p1", OperationManage)
	if err != nil {
		t.Fatal(err)
	}
	if canManage {
		t.Fatal("member grant unexpectedly allowed manage")
	}
}

func TestServiceListVisibleProjectIDsRechecksPolicyAndDeduplicates(t *testing.T) {
	t.Parallel()

	store := &fakeStore{candidates: []ProjectFacts{
		{ProjectID: "workspace", Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleMember},
		{ProjectID: "private-hidden", Visibility: VisibilityPrivate, OwnerWorkspaceRole: WorkspaceRoleMember},
		{ProjectID: "grant", Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleViewer},
		{ProjectID: "observer", Visibility: VisibilityPrivate, GlobalObserver: true},
		{ProjectID: "invalid", Visibility: Visibility("corrupt"), DirectGrantRole: ProjectRoleManager},
		{ProjectID: "grant", Visibility: VisibilityPrivate, DirectGrantRole: ProjectRoleManager},
		{ProjectID: "", Visibility: VisibilityWorkspace, OwnerWorkspaceRole: WorkspaceRoleOwner},
	}}
	service := NewService(store)

	got, err := service.ListVisibleProjectIDs(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"workspace", "grant", "observer"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visible IDs = %#v, want %#v", got, want)
	}
}

func TestServicePropagatesStoreErrors(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	service := NewService(&fakeStore{loadErr: boom, listErr: boom})

	if _, err := service.Evaluate(context.Background(), "u", "p"); !errors.Is(err, boom) {
		t.Fatalf("Evaluate error = %v, want boom", err)
	}
	if _, err := service.ListVisibleProjectIDs(context.Background(), "u"); !errors.Is(err, boom) {
		t.Fatalf("ListVisibleProjectIDs error = %v, want boom", err)
	}
}
