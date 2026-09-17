package r2dsharing

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/r2dauth"
)

type fakeAuthorizer struct {
	allowed bool
	err     error
	ops     []r2dauth.Operation
}

func (a *fakeAuthorizer) Can(_ context.Context, _, _ string, op r2dauth.Operation) (bool, error) {
	a.ops = append(a.ops, op)
	return a.allowed, a.err
}

type fakeStore struct {
	visibility      r2dauth.Visibility
	grants          []Grant
	principalExists bool
	createGrant     Grant
	createErr       error
	updateGrant     Grant
	updateErr       error
	deleteFound     bool
	deleteErr       error
	principals      []Principal
	setVisibility   r2dauth.Visibility
	searchType      PrincipalType
	searchQuery     string
	searchLimit     int
	members         []Principal
	membersErr      error
}

func (s *fakeStore) GetVisibility(context.Context, string) (r2dauth.Visibility, error) {
	if s.visibility == "" {
		return r2dauth.VisibilityWorkspace, nil
	}
	return s.visibility, nil
}
func (s *fakeStore) SetVisibility(_ context.Context, _ string, v r2dauth.Visibility) error {
	s.setVisibility = v
	return nil
}
func (s *fakeStore) ListGrants(context.Context, string) ([]Grant, error) { return s.grants, nil }
func (s *fakeStore) PrincipalExists(context.Context, PrincipalType, string) (bool, error) {
	return s.principalExists, nil
}
func (s *fakeStore) CreateGrant(_ context.Context, g Grant) (Grant, error) {
	if s.createErr != nil {
		return Grant{}, s.createErr
	}
	if s.createGrant.ID != "" {
		return s.createGrant, nil
	}
	return g, nil
}
func (s *fakeStore) UpdateGrantRole(context.Context, string, string, string) (Grant, error) {
	return s.updateGrant, s.updateErr
}
func (s *fakeStore) DeleteGrant(context.Context, string, string) (bool, error) {
	return s.deleteFound, s.deleteErr
}
func (s *fakeStore) SearchPrincipals(_ context.Context, typ PrincipalType, q string, limit int) ([]Principal, error) {
	s.searchType, s.searchQuery, s.searchLimit = typ, q, limit
	return s.principals, nil
}
func (s *fakeStore) ListAssignableMembers(context.Context, string) ([]Principal, error) {
	return s.members, s.membersErr
}

func TestAssignableActorsRequiresContribute(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	authz := &fakeAuthorizer{allowed: false}
	svc := NewService(store, authz)

	if _, err := svc.AssignableActors(context.Background(), "u1", "p1", PrincipalUser, "", 20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("AssignableActors error = %v, want ErrForbidden", err)
	}
	if !reflect.DeepEqual(authz.ops, []r2dauth.Operation{r2dauth.OperationContribute}) {
		t.Fatalf("auth operations = %#v", authz.ops)
	}
}

func TestAssignableActorsFiltersAndOmitsAgents(t *testing.T) {
	t.Parallel()
	store := &fakeStore{members: []Principal{
		{Type: PrincipalUser, ID: "u1", Name: "Ada Lovelace", Secondary: "ada@example.test"},
		{Type: PrincipalUser, ID: "u2", Name: "Grace Hopper", Secondary: "grace@example.test"},
	}}
	svc := NewService(store, &fakeAuthorizer{allowed: true})

	got, err := svc.AssignableActors(context.Background(), "u9", "p1", PrincipalUser, "ada", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "u1" {
		t.Fatalf("filtered roster = %#v, want only u1", got)
	}

	agents, err := svc.AssignableActors(context.Background(), "u9", "p1", PrincipalType("agent"), "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatalf("agent roster = %#v, want empty", agents)
	}
}

func TestGetRequiresProjectManager(t *testing.T) {
	t.Parallel()
	store := &fakeStore{visibility: r2dauth.VisibilityPrivate}
	authz := &fakeAuthorizer{allowed: false}
	svc := NewService(store, authz)

	_, err := svc.Get(context.Background(), "user-1", "project-1")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Get error = %v, want ErrForbidden", err)
	}
	if !reflect.DeepEqual(authz.ops, []r2dauth.Operation{r2dauth.OperationShare}) {
		t.Fatalf("auth operations = %#v", authz.ops)
	}
}

func TestGetReturnsVisibilityAndGrants(t *testing.T) {
	t.Parallel()
	store := &fakeStore{
		visibility: r2dauth.VisibilityPrivate,
		grants:     []Grant{{ID: "g1", ProjectID: "p1", PrincipalType: PrincipalUser, PrincipalID: "u2", Role: "member"}},
	}
	svc := NewService(store, &fakeAuthorizer{allowed: true})

	got, err := svc.Get(context.Background(), "u1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Visibility != r2dauth.VisibilityPrivate || len(got.Grants) != 1 || got.Grants[0].ID != "g1" {
		t.Fatalf("unexpected sharing payload: %#v", got)
	}
}

func TestCreateGrantValidatesPrincipal(t *testing.T) {
	t.Parallel()
	store := &fakeStore{principalExists: false}
	svc := NewService(store, &fakeAuthorizer{allowed: true})

	_, err := svc.CreateGrant(context.Background(), "u1", "p1", "g1", PrincipalWorkspace, "w2", "member")
	if !errors.Is(err, ErrPrincipalMissing) {
		t.Fatalf("CreateGrant error = %v, want ErrPrincipalMissing", err)
	}
}

func TestCreateGrantRejectsUnknownRoleBeforeDB(t *testing.T) {
	t.Parallel()
	store := &fakeStore{principalExists: true}
	authz := &fakeAuthorizer{allowed: true}
	svc := NewService(store, authz)

	_, err := svc.CreateGrant(context.Background(), "u1", "p1", "g1", PrincipalUser, "u2", "owner")
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("CreateGrant error = %v, want ErrInvalidQuery", err)
	}
	if len(authz.ops) != 0 {
		t.Fatalf("invalid request unexpectedly reached auth/store path")
	}
}

func TestSetVisibilityValidatesValue(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	svc := NewService(store, &fakeAuthorizer{allowed: true})

	if err := svc.SetVisibility(context.Background(), "u1", "p1", r2dauth.Visibility("public")); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("SetVisibility error = %v, want ErrInvalidQuery", err)
	}
	if err := svc.SetVisibility(context.Background(), "u1", "p1", r2dauth.VisibilityPrivate); err != nil {
		t.Fatal(err)
	}
	if store.setVisibility != r2dauth.VisibilityPrivate {
		t.Fatalf("stored visibility = %q", store.setVisibility)
	}
}

func TestDeleteGrantReturnsNotFound(t *testing.T) {
	t.Parallel()
	svc := NewService(&fakeStore{deleteFound: false}, &fakeAuthorizer{allowed: true})
	if err := svc.DeleteGrant(context.Background(), "u1", "p1", "missing"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("DeleteGrant error = %v, want ErrGrantNotFound", err)
	}
}

func TestDirectoryIsBoundedAndRequiresQuery(t *testing.T) {
	t.Parallel()
	store := &fakeStore{principals: []Principal{{Type: PrincipalUser, ID: "u2", Name: "Alice"}}}
	svc := NewService(store, &fakeAuthorizer{allowed: true})

	if _, err := svc.SearchDirectory(context.Background(), "u1", "p1", PrincipalUser, "a", 100); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("short query error = %v, want ErrInvalidQuery", err)
	}
	got, err := svc.SearchDirectory(context.Background(), "u1", "p1", PrincipalUser, "  al  ", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "u2" {
		t.Fatalf("unexpected directory result: %#v", got)
	}
	if store.searchType != PrincipalUser || store.searchQuery != "al" || store.searchLimit != 50 {
		t.Fatalf("search args = (%q, %q, %d)", store.searchType, store.searchQuery, store.searchLimit)
	}
}
