package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestValidateAssigneePair_AcceptsGrantedForeignMember(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := uuid.NewString()

	projectID := dbfx.Project(t, "Seam "+suffix)
	foreignUserID := dbfx.User(t, "Seam User "+suffix, "seam-"+suffix+"@example.test")
	dbfx.Insert(t, "r2d_project_grants", testutil.Cols{
		"id":             uuid.NewString(),
		"project_id":     projectID,
		"principal_type": "user",
		"principal_id":   foreignUserID,
		"role":           "member",
		"created_by":     testUserID,
	})

	assigneeID, err := util.ParseUUID(foreignUserID)
	if err != nil {
		t.Fatalf("parse assignee: %v", err)
	}
	status, msg := testHandler.validateAssigneePair(
		ctx, newRequest("POST", "/api/issues", nil), testWorkspaceID, projectID,
		pgtype.Text{String: "member", Valid: true}, assigneeID,
	)
	if status != 0 {
		t.Fatalf("granted foreign member rejected: %d %s", status, msg)
	}
}

func TestValidateAssigneePair_RejectsUngrantedForeignMember(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := uuid.NewString()

	projectID := dbfx.Project(t, "Seam Reject "+suffix)
	strangerID := dbfx.User(t, "Stranger "+suffix, "stranger-"+suffix+"@example.test")

	assigneeID, err := util.ParseUUID(strangerID)
	if err != nil {
		t.Fatalf("parse assignee: %v", err)
	}
	status, _ := testHandler.validateAssigneePair(
		ctx, newRequest("POST", "/api/issues", nil), testWorkspaceID, projectID,
		pgtype.Text{String: "member", Valid: true}, assigneeID,
	)
	if status != http.StatusBadRequest {
		t.Fatalf("ungranted foreign member: status=%d want %d", status, http.StatusBadRequest)
	}
}

func TestValidateAssigneePair_RejectsAfterGrantRevoked(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	suffix := uuid.NewString()

	projectID := dbfx.Project(t, "Seam Revoke "+suffix)
	granteeID := dbfx.User(t, "Revoked "+suffix, "revoked-"+suffix+"@example.test")
	grantID := dbfx.Insert(t, "r2d_project_grants", testutil.Cols{
		"id":             uuid.NewString(),
		"project_id":     projectID,
		"principal_type": "user",
		"principal_id":   granteeID,
		"role":           "member",
		"created_by":     testUserID,
	})

	assigneeID, err := util.ParseUUID(granteeID)
	if err != nil {
		t.Fatalf("parse assignee: %v", err)
	}
	before, _ := testHandler.validateAssigneePair(
		ctx, newRequest("POST", "/api/issues", nil), testWorkspaceID, projectID,
		pgtype.Text{String: "member", Valid: true}, assigneeID,
	)
	if before != 0 {
		t.Fatalf("granted member rejected before revocation: %d", before)
	}

	dbfx.Exec(t, `DELETE FROM r2d_project_grants WHERE id = $1`, grantID)

	after, _ := testHandler.validateAssigneePair(
		ctx, newRequest("POST", "/api/issues", nil), testWorkspaceID, projectID,
		pgtype.Text{String: "member", Valid: true}, assigneeID,
	)
	if after != http.StatusBadRequest {
		t.Fatalf("revoked member: status=%d want %d", after, http.StatusBadRequest)
	}
}
