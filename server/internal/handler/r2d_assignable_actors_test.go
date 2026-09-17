package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// A foreign Workspace grant recipient is offered the owner Workspace's members
// and their own Workspace's members, but never the owner Workspace's Agents.
func TestGetProjectAssignableActors_MemberRosterRespectsProjectACL(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	suffix := uuid.NewString()
	projectID := dbfx.Project(t, "Actors "+suffix)
	foreignWorkspaceID := dbfx.Workspace(t, "Actors WS "+suffix, "actors-"+suffix)
	foreignUserID := dbfx.User(t, "Granted "+suffix, "granted-"+suffix+"@example.test")
	dbfx.Member(t, foreignWorkspaceID, foreignUserID, "member")
	dbfx.Insert(t, "r2d_project_grants", testutil.Cols{
		"id":             uuid.NewString(),
		"project_id":     projectID,
		"principal_type": "workspace",
		"principal_id":   foreignWorkspaceID,
		"role":           "member",
		"created_by":     testUserID,
	})

	req := newRequest("GET", "/api/projects/"+projectID+"/assignable-actors?type=member", nil)
	req = withURLParam(req, "id", projectID)
	req.Header.Set("X-User-ID", foreignUserID)
	req.Header.Set("X-Workspace-ID", foreignWorkspaceID)

	recorder := httptest.NewRecorder()
	testHandler.GetProjectAssignableActors(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("actors: expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Actors []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"actors"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode actors: %v", err)
	}
	foundSelf := false
	for _, actor := range body.Actors {
		if actor.Type != "member" {
			t.Fatalf("unexpected actor type %q", actor.Type)
		}
		if actor.ID == foreignUserID {
			foundSelf = true
		}
	}
	if !foundSelf {
		t.Fatalf("granted member %s missing from roster: %s", foreignUserID, recorder.Body.String())
	}
}

// A foreign caller asking for agents must receive an empty list, not an error
// and not the owner Workspace's inventory (P06 boundary).
func TestGetProjectAssignableActors_ForeignCallerGetsNoAgents(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	suffix := uuid.NewString()
	projectID := dbfx.Project(t, "Actors agents "+suffix)
	foreignWorkspaceID := dbfx.Workspace(t, "Actors agents WS "+suffix, "actors-agents-"+suffix)
	foreignUserID := dbfx.User(t, "Granted agent caller "+suffix, "granted-agent-"+suffix+"@example.test")
	dbfx.Member(t, foreignWorkspaceID, foreignUserID, "member")
	dbfx.Insert(t, "r2d_project_grants", testutil.Cols{
		"id":             uuid.NewString(),
		"project_id":     projectID,
		"principal_type": "workspace",
		"principal_id":   foreignWorkspaceID,
		"role":           "member",
		"created_by":     testUserID,
	})

	req := newRequest("GET", "/api/projects/"+projectID+"/assignable-actors?type=agent", nil)
	req = withURLParam(req, "id", projectID)
	req.Header.Set("X-User-ID", foreignUserID)
	req.Header.Set("X-Workspace-ID", foreignWorkspaceID)

	recorder := httptest.NewRecorder()
	testHandler.GetProjectAssignableActors(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("actors: expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Actors []json.RawMessage `json:"actors"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode actors: %v", err)
	}
	if len(body.Actors) != 0 {
		t.Fatalf("foreign caller received %d agent actors: %s", len(body.Actors), recorder.Body.String())
	}
}
