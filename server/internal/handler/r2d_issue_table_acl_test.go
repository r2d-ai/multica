package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// r2dSharedProjectFixture is the cross-Workspace read fixture for the Issue
// collection surfaces: a foreign Workspace with its own member, a Project owned
// by the test Workspace, a workspace grant for the foreign Workspace, and one
// Issue in that Project. The foreign member is the caller and its active
// Workspace is the foreign one, so the Project is visible only through the
// explicit grant.
type r2dSharedProjectFixture struct {
	foreignWorkspaceID string
	foreignUserID      string
	projectID          string
	issueID            string
}

func setupR2DSharedProjectFixture(t *testing.T, prefix string) r2dSharedProjectFixture {
	t.Helper()

	fix := r2dSharedProjectFixture{}
	fix.foreignWorkspaceID = dbfx.Workspace(
		t, prefix+" foreign workspace", "r2d-shared-"+prefix+"-"+uuid.NewString(),
	)
	fix.foreignUserID = dbfx.User(
		t, prefix+" foreign user", "r2d-shared-"+prefix+"-"+uuid.NewString()+"@multica.test",
	)
	dbfx.Member(t, fix.foreignWorkspaceID, fix.foreignUserID, "member")

	fix.projectID = dbfx.Project(t, prefix+" shared project")
	dbfx.Insert(t, "r2d_project_grants", testutil.Cols{
		"id":             uuid.NewString(),
		"project_id":     fix.projectID,
		"principal_type": "workspace",
		"principal_id":   fix.foreignWorkspaceID,
		"role":           "member",
		"created_by":     testUserID,
	})
	fix.issueID = dbfx.Issue(t, prefix+" shared issue", testutil.Cols{
		"project_id": fix.projectID,
	})
	return fix
}

func r2dSharedProjectRequest(t *testing.T, path string, body any, fix r2dSharedProjectFixture) *http.Request {
	t.Helper()
	return testutil.WithHeaders(
		testutil.JSONRequest(http.MethodPost, path, body),
		"X-User-ID", fix.foreignUserID,
		"X-Workspace-ID", fix.foreignWorkspaceID,
	)
}

func TestListIssueTableRows_IncludesExplicitlySharedForeignProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	fix := setupR2DSharedProjectFixture(t, "rows")
	req := r2dSharedProjectRequest(t, "/api/issues/table/rows", issueTableRowsRequest{
		Query: issueTableQuerySpec{Scope: issueTableScope{Kind: "workspace"}},
	}, fix)

	var rows issueTableRowsResponse
	testutil.Call(t, testHandler.ListIssueTableRows, req).Want(http.StatusOK).JSON(&rows)

	for _, row := range rows.Rows {
		if row.Issue.ID == fix.issueID {
			return
		}
	}
	t.Fatalf("shared Project issue %s missing from workspace rows: %+v", fix.issueID, rows)
}

func TestListIssueTableGroupsAndFacets_IncludeSharedForeignProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	fix := setupR2DSharedProjectFixture(t, "groups")

	groupsReq := r2dSharedProjectRequest(t, "/api/issues/table/groups", issueTableGroupsRequest{
		Query: issueTableQuerySpec{Scope: issueTableScope{Kind: "workspace"}},
		Group: issueTableGroupSpec{Kind: "status"},
	}, fix)
	var groups issueTableGroupsResponse
	testutil.Call(t, testHandler.ListIssueTableGroups, groupsReq).Want(http.StatusOK).JSON(&groups)
	if groups.Total < 1 {
		t.Fatalf("workspace groups excluded the shared Project issue: %+v", groups)
	}
	todoGroup := int64(0)
	for _, group := range groups.Groups {
		if group.Key == "status:todo" {
			todoGroup = group.Count
		}
	}
	if todoGroup < 1 {
		t.Fatalf("status:todo group excluded the shared Project issue: %+v", groups)
	}

	facetsReq := r2dSharedProjectRequest(t, "/api/issues/table/facets", issueTableFacetsRequest{
		Query:  issueTableQuerySpec{Scope: issueTableScope{Kind: "workspace"}},
		Facets: []issueTableFacetSpec{{Kind: "status"}},
	}, fix)
	var facets issueTableFacetsResponse
	testutil.Call(t, testHandler.ListIssueTableFacets, facetsReq).Want(http.StatusOK).JSON(&facets)
	todoFacet := int64(0)
	for _, facet := range facets.Facets {
		if facet.Kind != "status" {
			continue
		}
		for _, value := range facet.Values {
			if value.Key == "todo" {
				todoFacet = value.Count
			}
		}
	}
	if todoFacet < 1 {
		t.Fatalf("status facet excluded the shared Project issue: %+v", facets)
	}
}

func TestListIssueTableRows_ExcludesProjectlessIssuesOfForeignWorkspace(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	fix := setupR2DSharedProjectFixture(t, "projectless")

	// Projectless issue in the test Workspace: it must never surface to a user
	// whose active Workspace is the foreign one.
	projectlessID := dbfx.Issue(t, "projectless in test workspace", testutil.Cols{})

	req := r2dSharedProjectRequest(t, "/api/issues/table/rows", issueTableRowsRequest{
		Query: issueTableQuerySpec{Scope: issueTableScope{Kind: "workspace"}},
	}, fix)
	var rows issueTableRowsResponse
	testutil.Call(t, testHandler.ListIssueTableRows, req).Want(http.StatusOK).JSON(&rows)
	for _, row := range rows.Rows {
		if row.Issue.ID == projectlessID {
			t.Fatalf("projectless issue leaked into a foreign workspace list: %+v", rows)
		}
	}
}

func TestListIssueTableRows_ExcludesUngrantedProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	fix := setupR2DSharedProjectFixture(t, "ungranted")

	// A second Project in the test Workspace with no grant for the foreign
	// Workspace: readability must stay gated by the explicit grant.
	ungrantedProjectID := dbfx.Project(t, "ungranted project")
	ungrantedIssueID := dbfx.Issue(t, "ungranted project issue", testutil.Cols{
		"project_id": ungrantedProjectID,
	})

	req := r2dSharedProjectRequest(t, "/api/issues/table/rows", issueTableRowsRequest{
		Query: issueTableQuerySpec{Scope: issueTableScope{Kind: "workspace"}},
	}, fix)
	var rows issueTableRowsResponse
	testutil.Call(t, testHandler.ListIssueTableRows, req).Want(http.StatusOK).JSON(&rows)
	for _, row := range rows.Rows {
		if row.Issue.ID == ungrantedIssueID {
			t.Fatalf("ungranted Project issue leaked into a foreign workspace list: %+v", rows)
		}
	}
}
