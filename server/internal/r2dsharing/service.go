package r2dsharing

import (
	"context"
	"errors"
	"strings"

	"github.com/multica-ai/multica/server/internal/r2dauth"
)

var (
	ErrForbidden        = errors.New("project sharing forbidden")
	ErrGrantNotFound    = errors.New("project grant not found")
	ErrDuplicateGrant   = errors.New("project grant already exists")
	ErrPrincipalMissing = errors.New("project grant principal not found")
	ErrInvalidQuery     = errors.New("invalid directory query")
)

type PrincipalType string

const (
	PrincipalUser      PrincipalType = "user"
	PrincipalWorkspace PrincipalType = "workspace"
)

type Grant struct {
	ID            string        `json:"id"`
	ProjectID     string        `json:"project_id"`
	PrincipalType PrincipalType `json:"principal_type"`
	PrincipalID   string        `json:"principal_id"`
	Role          string        `json:"role"`
	CreatedBy     string        `json:"created_by"`
	CreatedAt     string        `json:"created_at"`
	UpdatedAt     string        `json:"updated_at"`
	Principal     *Principal    `json:"principal,omitempty"`
}

type Principal struct {
	Type      PrincipalType `json:"type"`
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Secondary string        `json:"secondary,omitempty"`
	AvatarURL string        `json:"avatar_url,omitempty"`
}

type Sharing struct {
	ProjectID  string              `json:"project_id"`
	Visibility r2dauth.Visibility  `json:"visibility"`
	Grants     []Grant             `json:"grants"`
}

type Authorizer interface {
	Can(ctx context.Context, userID, projectID string, op r2dauth.Operation) (bool, error)
}

type Store interface {
	GetVisibility(ctx context.Context, projectID string) (r2dauth.Visibility, error)
	SetVisibility(ctx context.Context, projectID string, visibility r2dauth.Visibility) error
	ListGrants(ctx context.Context, projectID string) ([]Grant, error)
	PrincipalExists(ctx context.Context, principalType PrincipalType, principalID string) (bool, error)
	CreateGrant(ctx context.Context, grant Grant) (Grant, error)
	UpdateGrantRole(ctx context.Context, projectID, grantID, role string) (Grant, error)
	DeleteGrant(ctx context.Context, projectID, grantID string) (bool, error)
	SearchPrincipals(ctx context.Context, principalType PrincipalType, query string, limit int) ([]Principal, error)
}

type Service struct {
	store Store
	authz Authorizer
}

func NewService(store Store, authz Authorizer) *Service {
	return &Service{store: store, authz: authz}
}

func (s *Service) Get(ctx context.Context, userID, projectID string) (Sharing, error) {
	if err := s.requireManager(ctx, userID, projectID); err != nil {
		return Sharing{}, err
	}
	visibility, err := s.store.GetVisibility(ctx, projectID)
	if err != nil {
		return Sharing{}, err
	}
	if !validVisibility(visibility) {
		return Sharing{}, ErrForbidden
	}
	grants, err := s.store.ListGrants(ctx, projectID)
	if err != nil {
		return Sharing{}, err
	}
	return Sharing{ProjectID: projectID, Visibility: visibility, Grants: grants}, nil
}

func (s *Service) SetVisibility(ctx context.Context, userID, projectID string, visibility r2dauth.Visibility) error {
	if !validVisibility(visibility) {
		return ErrInvalidQuery
	}
	if err := s.requireManager(ctx, userID, projectID); err != nil {
		return err
	}
	return s.store.SetVisibility(ctx, projectID, visibility)
}

func (s *Service) CreateGrant(ctx context.Context, userID, projectID, grantID string, principalType PrincipalType, principalID, role string) (Grant, error) {
	if !validPrincipalType(principalType) || !validRole(role) || strings.TrimSpace(principalID) == "" {
		return Grant{}, ErrInvalidQuery
	}
	if err := s.requireManager(ctx, userID, projectID); err != nil {
		return Grant{}, err
	}
	exists, err := s.store.PrincipalExists(ctx, principalType, principalID)
	if err != nil {
		return Grant{}, err
	}
	if !exists {
		return Grant{}, ErrPrincipalMissing
	}
	return s.store.CreateGrant(ctx, Grant{
		ID: grantID, ProjectID: projectID, PrincipalType: principalType,
		PrincipalID: principalID, Role: role, CreatedBy: userID,
	})
}

func (s *Service) UpdateGrantRole(ctx context.Context, userID, projectID, grantID, role string) (Grant, error) {
	if !validRole(role) {
		return Grant{}, ErrInvalidQuery
	}
	if err := s.requireManager(ctx, userID, projectID); err != nil {
		return Grant{}, err
	}
	return s.store.UpdateGrantRole(ctx, projectID, grantID, role)
}

func (s *Service) DeleteGrant(ctx context.Context, userID, projectID, grantID string) error {
	if err := s.requireManager(ctx, userID, projectID); err != nil {
		return err
	}
	deleted, err := s.store.DeleteGrant(ctx, projectID, grantID)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrGrantNotFound
	}
	return nil
}

func (s *Service) SearchDirectory(ctx context.Context, userID, projectID string, principalType PrincipalType, query string, limit int) ([]Principal, error) {
	query = strings.TrimSpace(query)
	if !validPrincipalType(principalType) || len([]rune(query)) < 2 {
		return nil, ErrInvalidQuery
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	if err := s.requireManager(ctx, userID, projectID); err != nil {
		return nil, err
	}
	return s.store.SearchPrincipals(ctx, principalType, query, limit)
}

func (s *Service) requireManager(ctx context.Context, userID, projectID string) error {
	allowed, err := s.authz.Can(ctx, userID, projectID, r2dauth.OperationShare)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}

func validPrincipalType(t PrincipalType) bool {
	return t == PrincipalUser || t == PrincipalWorkspace
}

func validRole(role string) bool {
	switch r2dauth.ProjectRole(role) {
	case r2dauth.ProjectRoleViewer, r2dauth.ProjectRoleMember, r2dauth.ProjectRoleManager:
		return true
	default:
		return false
	}
}

func validVisibility(v r2dauth.Visibility) bool {
	return v == r2dauth.VisibilityWorkspace || v == r2dauth.VisibilityPrivate
}
