package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// taskTokenRequest simulates the auth middleware's post-mat_ state: the
// server-set X-Actor-Source marker plus the task/agent/workspace identity the
// token row stamped. The handler must derive scope from these, never from the
// URL query or the human owner's workspace.
func taskTokenRequest(projectID, taskID, agentID, workspaceID string) *http.Request {
	req := newRequest("GET", "/api/projects/"+projectID+"/resources", nil)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Task-ID", taskID)
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Workspace-ID", workspaceID)
	return withURLParam(req, "id", projectID)
}

// P06-D: a task token may list resources for exactly the Project bound to its
// durable task, and for nothing else in the same Workspace. Before this gate the
// workspace-scoped list let a run enumerate every Project resource in the owner
// Workspace.
func TestListProjectResources_TaskTokenIsBoundToAuthorizedProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	const (
		projectARepoURL = "https://github.com/example/p06d-project-a"
		projectBRepoURL = "https://github.com/example/p06d-project-b"
	)

	projectA := dbfx.Project(t, "P06-D token scope project A")
	dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id":    projectA,
		"workspace_id":  testWorkspaceID,
		"resource_type": "github_repo",
		"resource_ref":  `{"url":"` + projectARepoURL + `"}`,
		"position":      0,
	})
	projectB := dbfx.Project(t, "P06-D token scope project B")
	dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id":    projectB,
		"workspace_id":  testWorkspaceID,
		"resource_type": "github_repo",
		"resource_ref":  `{"url":"` + projectBRepoURL + `"}`,
		"position":      0,
	})

	var agentID, runtimeID string
	dbfx.QueryRow(t,
		`SELECT id, runtime_id FROM agent WHERE workspace_id = $1 LIMIT 1`,
		testWorkspaceID,
	).Scan(&agentID, &runtimeID)

	issueA := dbfx.Issue(t, "P06-D token scope issue A", testutil.Cols{
		"project_id": projectA,
		"priority":   "medium",
		"number":     88301,
	})
	taskA := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID,
		"issue_id":   issueA,
	})

	// The authorized Project is readable and carries only its own resource.
	w := testutil.Call(t, testHandler.ListProjectResources,
		taskTokenRequest(projectA, taskA, agentID, testWorkspaceID)).Want(http.StatusOK)
	if strings.Contains(w.Text(), projectBRepoURL) {
		t.Fatalf("task token for Project A leaked Project B's resource: %s", w.Text())
	}
	var list struct {
		Resources []ProjectResourceResponse `json:"resources"`
		Total     int                       `json:"total"`
	}
	w.JSON(&list)
	if list.Total != 1 || len(list.Resources) != 1 {
		t.Fatalf("authorized list returned %d resources, want 1: %s", list.Total, w.Text())
	}
	if list.Resources[0].ProjectID != projectA {
		t.Errorf("resource project_id = %q, want %q", list.Resources[0].ProjectID, projectA)
	}

	// The same token cannot read another Project in the same Workspace.
	w = testutil.Call(t, testHandler.ListProjectResources,
		taskTokenRequest(projectB, taskA, agentID, testWorkspaceID)).Want(http.StatusNotFound)
	if strings.Contains(w.Text(), projectBRepoURL) {
		t.Fatalf("cross-Project task-token read leaked Project B's resource: %s", w.Text())
	}

	// A projectless task has no Project execution scope at all.
	projectlessIssue := dbfx.Issue(t, "P06-D token scope projectless issue", testutil.Cols{
		"priority": "medium",
		"number":   88302,
	})
	projectlessTask := dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": runtimeID,
		"issue_id":   projectlessIssue,
	})
	testutil.Call(t, testHandler.ListProjectResources,
		taskTokenRequest(projectA, projectlessTask, agentID, testWorkspaceID)).Want(http.StatusNotFound)

	// Rebound issue: once the issue moves to Project B, the task token no
	// longer reads Project A's resources — the old reference is stale.
	dbfx.Exec(t, `UPDATE issue SET project_id = $1 WHERE id = $2`, projectB, issueA)
	testutil.Call(t, testHandler.ListProjectResources,
		taskTokenRequest(projectA, taskA, agentID, testWorkspaceID)).Want(http.StatusNotFound)
	w = testutil.Call(t, testHandler.ListProjectResources,
		taskTokenRequest(projectB, taskA, agentID, testWorkspaceID)).Want(http.StatusOK)
	if strings.Contains(w.Text(), projectARepoURL) {
		t.Fatalf("rebound task leaked the stale Project A resource: %s", w.Text())
	}
}
