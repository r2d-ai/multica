package r2dauth

import (
	"encoding/json"
	"strings"
)

// TaskResourceFact is one Project resource binding loaded server-side for the
// authorized execution decision. WorkspaceID is the row's OWN workspace_id,
// not the Project's: keeping both lets the policy reject a corrupt or rebound
// row instead of trusting that project_resource was written consistently.
type TaskResourceFact struct {
	ID           string
	WorkspaceID  string
	ResourceType string
	ResourceRef  string
	Label        string
}

// TaskResourceScopeResource is one resource inside an authorized execution
// scope. ResourceRef stays raw JSON so the runtime handoff can serialize it
// unchanged; the policy only inspects the well-known fields it must validate.
type TaskResourceScopeResource struct {
	ID           string
	ResourceType string
	ResourceRef  string
	Label        string
}

// TaskResourceRepo is the checkout-relevant projection of a github_repo
// resource inside an execution scope.
type TaskResourceRepo struct {
	URL string
	Ref string
}

// TaskResourceScope is the exact resource handoff an authorized task may
// consume. It is computed server-side from the execution decision and the
// Project owner Workspace bindings; a caller can neither supply nor widen it.
//
// Only resources whose own workspace_id equals ProjectWorkspaceID are admitted.
// A foreign row is dropped, never carried into the scope, so a corrupt
// project_resource reference cannot lift another tenant's repo URLs or local
// paths into a task.
type TaskResourceScope struct {
	ProjectID          string
	ProjectWorkspaceID string
	Resources          []TaskResourceScopeResource
	Repos              []TaskResourceRepo
	LocalPaths         []string
	DaemonIDs          []string
}

// Empty reports whether the scope carries no Project binding at all. A resolved
// Project with zero attached resources is NOT empty: ProjectID is set, so the
// caller knows the empty list is authoritative rather than a failed lookup.
func (s TaskResourceScope) Empty() bool {
	return s.ProjectID == ""
}

// ResolveTaskResourceScope derives the execution resource scope for an already
// allowed Project-scoped decision. It fails closed (ok == false) when the
// decision is not an allowed Project execution or the Project owner Workspace
// is unknown, so a task never receives resources without an authoritative
// Project-Workspace binding behind them.
//
// Resource rows whose own workspace_id disagrees with the Project owner
// Workspace are dropped: they are the corrupt-data shape Project ACLs must not
// turn into cross-tenant resource access. A github_repo or local_directory row
// with an unusable reference is dropped too, for the same fail-closed reason —
// the scope never carries a resource the runtime cannot safely materialize.
func ResolveTaskResourceScope(d TaskExecutionDecision, resources []TaskResourceFact) (TaskResourceScope, bool) {
	if !d.Allowed || d.ProjectID == "" || d.ProjectWorkspaceID == "" {
		return TaskResourceScope{}, false
	}

	scope := TaskResourceScope{ProjectID: d.ProjectID, ProjectWorkspaceID: d.ProjectWorkspaceID}
	for _, r := range resources {
		if r.ID == "" || r.WorkspaceID != d.ProjectWorkspaceID {
			continue
		}

		var repo *TaskResourceRepo
		var localPath, daemonID string
		switch r.ResourceType {
		case "github_repo":
			var payload struct {
				URL string `json:"url"`
				Ref string `json:"ref"`
			}
			if err := json.Unmarshal([]byte(r.ResourceRef), &payload); err != nil || strings.TrimSpace(payload.URL) == "" {
				continue
			}
			repo = &TaskResourceRepo{URL: payload.URL, Ref: strings.TrimSpace(payload.Ref)}
		case "local_directory":
			var payload struct {
				LocalPath string `json:"local_path"`
				DaemonID  string `json:"daemon_id"`
			}
			if err := json.Unmarshal([]byte(r.ResourceRef), &payload); err != nil || strings.TrimSpace(payload.LocalPath) == "" {
				continue
			}
			localPath = payload.LocalPath
			daemonID = strings.TrimSpace(payload.DaemonID)
		}

		scope.Resources = append(scope.Resources, TaskResourceScopeResource{
			ID:           r.ID,
			ResourceType: r.ResourceType,
			ResourceRef:  r.ResourceRef,
			Label:        r.Label,
		})
		if repo != nil {
			scope.Repos = append(scope.Repos, *repo)
		}
		if localPath != "" {
			scope.LocalPaths = append(scope.LocalPaths, localPath)
		}
		if daemonID != "" {
			scope.DaemonIDs = append(scope.DaemonIDs, daemonID)
		}
	}
	return scope, true
}
