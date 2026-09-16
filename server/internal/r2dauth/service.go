// Package r2dauth contains R2D-owned authorization policy that extends Multica
// without changing upstream-owned workspace/project tables.
package r2dauth

import (
	"context"
	"errors"
)

var ErrProjectNotFound = errors.New("project not found")

// ProjectRole is an explicit/effective role inside one Project. It is separate
// from the upstream workspace membership role: Team == Workspace, while a
// Project may be shared across Workspace boundaries.
type ProjectRole string

const (
	ProjectRoleViewer  ProjectRole = "viewer"
	ProjectRoleMember  ProjectRole = "member"
	ProjectRoleManager ProjectRole = "manager"
)

type Visibility string

const (
	VisibilityWorkspace Visibility = "workspace"
	VisibilityPrivate   Visibility = "private"
)

type WorkspaceRole string

const (
	WorkspaceRoleOwner  WorkspaceRole = "owner"
	WorkspaceRoleAdmin  WorkspaceRole = "admin"
	WorkspaceRoleMember WorkspaceRole = "member"
	WorkspaceRoleAgent  WorkspaceRole = "agent"
)

// Operation is deliberately project-scoped. Workspace secrets, credentials,
// Agent inventory and Squad inventory are not Project operations and must not
// become reachable merely because a Project was shared.
type Operation string

const (
	OperationRead       Operation = "read"
	OperationContribute Operation = "contribute"
	OperationManage     Operation = "manage"
	OperationShare      Operation = "share"
)

// ProjectFacts are the authorization inputs for one user/project pair. Empty
// roles mean that source supplied no role. The PostgreSQL store reads these
// facts from upstream project/member tables plus R2D side tables.
type ProjectFacts struct {
	ProjectID            string
	OwnerWorkspaceID     string
	Visibility           Visibility
	OwnerWorkspaceRole   WorkspaceRole
	DirectGrantRole      ProjectRole
	WorkspaceGrantRole   ProjectRole
	GlobalObserver       bool
}

// Decision is the resolved authorization state for one Project. GlobalObserver
// intentionally remains a separate bit instead of being synthesized into a
// viewer role: observers can read but are not Project members and gain no
// write/manage/share capability.
type Decision struct {
	Role           ProjectRole
	GlobalObserver bool
	valid          bool
}

func (d Decision) Can(op Operation) bool {
	if !d.valid {
		return false
	}

	rank := projectRoleRank(d.Role)
	switch op {
	case OperationRead:
		return d.GlobalObserver || rank >= projectRoleRank(ProjectRoleViewer)
	case OperationContribute:
		return rank >= projectRoleRank(ProjectRoleMember)
	case OperationManage, OperationShare:
		return rank >= projectRoleRank(ProjectRoleManager)
	default:
		return false
	}
}

// HasProjectRole distinguishes an observer-only decision from a real Project
// membership/grant.
func (d Decision) HasProjectRole() bool {
	return d.valid && projectRoleRank(d.Role) > 0
}

// ProjectCapabilities is the UI-safe projection of one central authorization
// decision. It exists so clients never need to duplicate role ranking or infer
// permission from Workspace state. ViewResources is intentionally stricter than
// Project read: Project resources can carry owner-Workspace repo URLs, daemon
// ids and local paths, so a cross-Workspace Project grant does not expose them.
type ProjectCapabilities struct {
	Role           ProjectRole
	GlobalObserver bool
	Read           bool
	Contribute     bool
	Manage         bool
	Share          bool
	ViewResources  bool
}

func isHumanOwnerWorkspaceMember(role WorkspaceRole) bool {
	switch role {
	case WorkspaceRoleOwner, WorkspaceRoleAdmin, WorkspaceRoleMember:
		return true
	default:
		return false
	}
}

// ResolveCapabilities derives every UI-facing capability from the same facts
// and Decision used by server authorization. Do not duplicate these rules in
// HTTP handlers or TypeScript.
func ResolveCapabilities(f ProjectFacts) ProjectCapabilities {
	d := Resolve(f)
	read := d.Can(OperationRead)
	return ProjectCapabilities{
		Role:           d.Role,
		GlobalObserver: d.GlobalObserver,
		Read:           read,
		Contribute:     d.Can(OperationContribute),
		Manage:         d.Can(OperationManage),
		Share:          d.Can(OperationShare),
		ViewResources:  read && isHumanOwnerWorkspaceMember(f.OwnerWorkspaceRole),
	}
}

// Store supplies policy facts without owning policy. Keeping resolution here
// prevents HTTP handlers and SQL queries from each inventing subtly different
// ACL semantics.
type Store interface {
	LoadProjectFacts(ctx context.Context, userID, projectID string) (ProjectFacts, error)
	ListCandidateProjectFacts(ctx context.Context, userID string) ([]ProjectFacts, error)
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Evaluate(ctx context.Context, userID, projectID string) (Decision, error) {
	facts, err := s.store.LoadProjectFacts(ctx, userID, projectID)
	if err != nil {
		return Decision{}, err
	}
	return Resolve(facts), nil
}

func (s *Service) Capabilities(ctx context.Context, userID, projectID string) (ProjectCapabilities, error) {
	facts, err := s.store.LoadProjectFacts(ctx, userID, projectID)
	if err != nil {
		return ProjectCapabilities{}, err
	}
	return ResolveCapabilities(facts), nil
}

func (s *Service) Can(ctx context.Context, userID, projectID string, op Operation) (bool, error) {
	decision, err := s.Evaluate(ctx, userID, projectID)
	if err != nil {
		return false, err
	}
	return decision.Can(op), nil
}

// ListVisibleProjectIDs returns Projects for which the same resolver used by
// per-Project checks allows OperationRead. The store may over-select candidate
// rows for efficiency; the policy service remains the final authority.
func (s *Service) ListVisibleProjectIDs(ctx context.Context, userID string) ([]string, error) {
	facts, err := s.store.ListCandidateProjectFacts(ctx, userID)
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(facts))
	seen := make(map[string]struct{}, len(facts))
	for _, f := range facts {
		if f.ProjectID == "" || !Resolve(f).Can(OperationRead) {
			continue
		}
		if _, ok := seen[f.ProjectID]; ok {
			continue
		}
		seen[f.ProjectID] = struct{}{}
		ids = append(ids, f.ProjectID)
	}
	return ids, nil
}

// Resolve combines every applicable source and keeps the strongest Project
// role. Unknown values never grant access. An invalid visibility is treated as
// structural corruption and fails closed for the entire decision.
func Resolve(f ProjectFacts) Decision {
	if f.Visibility != VisibilityWorkspace && f.Visibility != VisibilityPrivate {
		return Decision{}
	}

	decision := Decision{GlobalObserver: f.GlobalObserver, valid: true}

	// Team Leader governance: owner/admin of the Project's owning Workspace can
	// manage all of its Projects, including private ones. Ordinary members keep
	// upstream-compatible Project access only for workspace-visible Projects.
	switch f.OwnerWorkspaceRole {
	case WorkspaceRoleOwner, WorkspaceRoleAdmin:
		decision.Role = ProjectRoleManager
	case WorkspaceRoleMember:
		if f.Visibility == VisibilityWorkspace {
			decision.Role = ProjectRoleMember
		}
	case WorkspaceRoleAgent, "":
		// Agents receive no implicit Project role in P02. Agent/Squad execution
		// policy is intentionally deferred to P06.
	default:
		// Unknown workspace roles fail closed for this source.
	}

	decision.Role = strongerProjectRole(decision.Role, f.DirectGrantRole)
	decision.Role = strongerProjectRole(decision.Role, f.WorkspaceGrantRole)
	return decision
}

func strongerProjectRole(a, b ProjectRole) ProjectRole {
	if projectRoleRank(b) > projectRoleRank(a) {
		return b
	}
	return a
}

func projectRoleRank(role ProjectRole) int {
	switch role {
	case ProjectRoleViewer:
		return 1
	case ProjectRoleMember:
		return 2
	case ProjectRoleManager:
		return 3
	default:
		return 0
	}
}
