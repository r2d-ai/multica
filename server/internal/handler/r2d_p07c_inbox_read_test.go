package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// P07-C read-path end-to-end coverage (GitHub #68).
//
// Project-grant notification rows are written under the issue owner's
// Workspace, so a foreign collaborator's active Workspace never matches the
// row's workspace_id. These tests pin the full persistence/revocation contract
// on the read side: grant -> row persisted -> list/single-item/count expose it
// -> revoke (without deleting the row) -> nothing exposes it. They also pin the
// fail-closed edges: a foreign projectless row, another recipient's row, and an
// inaccessible native Project's row.

type p07cInboxFixture struct {
	ownerWorkspace   string
	grantedWorkspace string // workspace-principal grant target
	activeWorkspace  string // the foreign collaborator's active Workspace
	ownerID          string
	collaboratorID   string
	otherUserID      string
	projectID        string
	privateProjectID string
	issueID          string
	privateIssueID   string
	projectlessIssue string
}

func setupP07CInboxFixture(t *testing.T) p07cInboxFixture {
	t.Helper()

	slug := func(prefix string) string { return prefix + "-" + uuid.NewString() }
	user := func(label string) string {
		return dbfx.User(t, "P07C "+label, "p07c-inbox-"+label+"-"+uuid.NewString()+"@multica.test")
	}

	fix := p07cInboxFixture{}
	fix.ownerWorkspace = dbfx.Workspace(t, "P07C owner", slug("p07c-owner"))
	fix.grantedWorkspace = dbfx.Workspace(t, "P07C granted", slug("p07c-granted"))
	fix.activeWorkspace = dbfx.Workspace(t, "P07C active", slug("p07c-active"))

	fix.ownerID = user("owner")
	fix.collaboratorID = user("collaborator")
	fix.otherUserID = user("other")

	dbfx.Member(t, fix.ownerWorkspace, fix.ownerID, "owner")
	// The collaborator belongs to both the granted Workspace and the active
	// one, so membership removal from the grant Workspace can be observed
	// while a valid active Workspace remains.
	dbfx.Member(t, fix.grantedWorkspace, fix.collaboratorID, "member")
	dbfx.Member(t, fix.activeWorkspace, fix.collaboratorID, "member")
	// Same active Workspace as the collaborator, so only the recipient pair
	// keeps this user out.
	dbfx.Member(t, fix.activeWorkspace, fix.otherUserID, "member")

	fix.projectID = dbfx.Project(t, "P07C shared project", testutil.Cols{
		"workspace_id": fix.ownerWorkspace,
	})
	fix.privateProjectID = dbfx.Project(t, "P07C private project", testutil.Cols{
		"workspace_id": fix.ownerWorkspace,
	})
	dbfx.Exec(t, `INSERT INTO r2d_project_extra (project_id, visibility) VALUES ($1, 'private')`, fix.privateProjectID)
	dbfx.Cleanup(t, `DELETE FROM r2d_project_extra WHERE project_id = $1`, fix.privateProjectID)

	fix.issueID = dbfx.Issue(t, "P07C shared issue", testutil.Cols{
		"workspace_id": fix.ownerWorkspace,
		"project_id":   fix.projectID,
	})
	fix.privateIssueID = dbfx.Issue(t, "P07C private issue", testutil.Cols{
		"workspace_id": fix.ownerWorkspace,
		"project_id":   fix.privateProjectID,
	})
	fix.projectlessIssue = dbfx.Issue(t, "P07C projectless issue", testutil.Cols{
		"workspace_id": fix.ownerWorkspace,
	})
	return fix
}

// p07cGrant inserts a Project grant and returns its id so a test can revoke it
// without deleting the persisted notification rows.
func p07cGrant(t *testing.T, projectID, principalType, principalID, role string) string {
	t.Helper()
	return dbfx.Insert(t, "r2d_project_grants", testutil.Cols{
		"id":             uuid.NewString(),
		"project_id":     projectID,
		"principal_type": principalType,
		"principal_id":   principalID,
		"role":           role,
		"created_by":     testUserID,
	})
}

func p07cRevoke(t *testing.T, grantID string) {
	t.Helper()
	dbfx.Exec(t, `DELETE FROM r2d_project_grants WHERE id = $1`, grantID)
}

func p07cInsertInboxRow(t *testing.T, workspaceID, recipientID, issueID, title string, archived bool) string {
	t.Helper()
	cols := testutil.Cols{
		"workspace_id":   workspaceID,
		"recipient_type": "member",
		"recipient_id":   recipientID,
		"type":           "status_changed",
		"severity":       "info",
		"title":          title,
	}
	if issueID != "" {
		cols["issue_id"] = issueID
	}
	if archived {
		cols["archived"] = true
	}
	return dbfx.Insert(t, "inbox_item", cols)
}

func p07cRequest(userID, workspaceID, method, path string) *http.Request {
	return testutil.WithHeaders(
		testutil.JSONRequest(method, path, nil),
		"X-User-ID", userID,
		"X-Workspace-ID", workspaceID,
	)
}

func p07cListInbox(t *testing.T, userID, workspaceID string) []InboxItemResponse {
	t.Helper()
	var items []InboxItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListInbox),
		p07cRequest(userID, workspaceID, http.MethodGet, "/api/inbox")).
		Want(http.StatusOK).
		JSON(&items)
	return items
}

func p07cListArchivedInbox(t *testing.T, userID, workspaceID string) []InboxItemResponse {
	t.Helper()
	var items []InboxItemResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.ListArchivedInbox),
		p07cRequest(userID, workspaceID, http.MethodGet, "/api/inbox/archived")).
		Want(http.StatusOK).
		JSON(&items)
	return items
}

func p07cUnreadCount(t *testing.T, userID, workspaceID string) int64 {
	t.Helper()
	var out map[string]int64
	testutil.Call(t, inboxWorkspaceHandler(testHandler.CountUnreadInbox),
		p07cRequest(userID, workspaceID, http.MethodGet, "/api/inbox/unread-count")).
		Want(http.StatusOK).
		JSON(&out)
	return out["count"]
}

func p07cUnreadSummary(t *testing.T, userID, workspaceID string) []InboxWorkspaceUnreadResponse {
	t.Helper()
	var out []InboxWorkspaceUnreadResponse
	testutil.Call(t, inboxWorkspaceHandler(testHandler.UnreadInboxSummary),
		p07cRequest(userID, workspaceID, http.MethodGet, "/api/inbox/unread-summary")).
		Want(http.StatusOK).
		JSON(&out)
	return out
}

func p07cContainsIssue(items []InboxItemResponse, issueID string) bool {
	for _, item := range items {
		if item.IssueID != nil && *item.IssueID == issueID {
			return true
		}
	}
	return false
}

func p07cSummaryCount(rows []InboxWorkspaceUnreadResponse, workspaceID string) int64 {
	for _, row := range rows {
		if row.WorkspaceID == workspaceID {
			return row.Count
		}
	}
	return 0
}

// TestP07CInboxDirectGrantReadAndRevoke is the headline persistence contract: a
// direct grant recipient reads the owner-Workspace row, and revoking the grant
// (leaving the row in place) hides it from the list.
func TestP07CInboxDirectGrantReadAndRevoke(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupP07CInboxFixture(t)
	grantID := p07cGrant(t, fix.projectID, "user", fix.collaboratorID, "viewer")
	p07cInsertInboxRow(t, fix.ownerWorkspace, fix.collaboratorID, fix.issueID, "P07C direct grant notification", false)

	if got := p07cListInbox(t, fix.collaboratorID, fix.activeWorkspace); !p07cContainsIssue(got, fix.issueID) {
		t.Fatalf("direct grant recipient cannot read the persisted project notification: %+v", got)
	}
	if got := p07cUnreadCount(t, fix.collaboratorID, fix.activeWorkspace); got != 1 {
		t.Fatalf("unread count with grant = %d, want 1", got)
	}

	p07cRevoke(t, grantID)

	if got := p07cListInbox(t, fix.collaboratorID, fix.activeWorkspace); p07cContainsIssue(got, fix.issueID) {
		t.Fatalf("revoked direct grant still exposes the project notification: %+v", got)
	}
	if got := p07cUnreadCount(t, fix.collaboratorID, fix.activeWorkspace); got != 0 {
		t.Fatalf("unread count after revoke = %d, want 0", got)
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE issue_id = $1`, fix.issueID); got != 1 {
		t.Fatalf("revocation must hide the stored row, not delete it: row count = %d, want 1", got)
	}
}

// TestP07CInboxWorkspaceGrantAndMembershipRemoval covers the workspace-principal
// path: members of the granted Workspace read the row, and both grant revocation
// and membership removal fail closed.
func TestP07CInboxWorkspaceGrantAndMembershipRemoval(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupP07CInboxFixture(t)
	grantID := p07cGrant(t, fix.projectID, "workspace", fix.grantedWorkspace, "member")
	p07cInsertInboxRow(t, fix.ownerWorkspace, fix.collaboratorID, fix.issueID, "P07C workspace grant notification", false)

	if got := p07cListInbox(t, fix.collaboratorID, fix.activeWorkspace); !p07cContainsIssue(got, fix.issueID) {
		t.Fatalf("workspace grant recipient cannot read the persisted project notification: %+v", got)
	}

	p07cRevoke(t, grantID)
	if got := p07cListInbox(t, fix.collaboratorID, fix.activeWorkspace); p07cContainsIssue(got, fix.issueID) {
		t.Fatalf("revoked workspace grant still exposes the project notification: %+v", got)
	}

	// Re-grant, then remove the collaborator from the granted Workspace: the
	// grant row still exists, but the principal no longer expands to them.
	fix2Grant := p07cGrant(t, fix.projectID, "workspace", fix.grantedWorkspace, "member")
	dbfx.Exec(t, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, fix.grantedWorkspace, fix.collaboratorID)
	if got := p07cListInbox(t, fix.collaboratorID, fix.activeWorkspace); p07cContainsIssue(got, fix.issueID) {
		t.Fatalf("membership removal must fail closed, still exposed via grant %s: %+v", fix2Grant, got)
	}
}

// TestP07CInboxReadsAreRecipientBound proves the recipient pair is the
// enumeration boundary: another user in the same Workspace sees nothing and a
// per-item mutation answers the same non-disclosing 404.
func TestP07CInboxReadsAreRecipientBound(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupP07CInboxFixture(t)
	p07cGrant(t, fix.projectID, "user", fix.collaboratorID, "viewer")
	itemID := p07cInsertInboxRow(t, fix.ownerWorkspace, fix.collaboratorID, fix.issueID, "P07C recipient bound", false)

	if got := p07cListInbox(t, fix.otherUserID, fix.activeWorkspace); p07cContainsIssue(got, fix.issueID) {
		t.Fatalf("another user enumerated a foreign recipient's row: %+v", got)
	}

	req := testutil.WithURLParams(
		p07cRequest(fix.otherUserID, fix.activeWorkspace, http.MethodPost, "/api/inbox/"+itemID+"/read"),
		"id", itemID)
	testutil.Call(t, inboxWorkspaceHandler(testHandler.MarkInboxRead), req).Want(http.StatusNotFound)

	if got := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = true`, itemID); got != 0 {
		t.Fatalf("another user mutated a foreign recipient's row")
	}
}

// TestP07CInboxForeignProjectlessRowHidden proves a grant never widens a
// projectless issue: its notification row stays Workspace-private.
func TestP07CInboxForeignProjectlessRowHidden(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupP07CInboxFixture(t)
	p07cGrant(t, fix.projectID, "user", fix.collaboratorID, "viewer")
	p07cInsertInboxRow(t, fix.ownerWorkspace, fix.collaboratorID, fix.projectlessIssue, "P07C foreign projectless", false)

	if got := p07cListInbox(t, fix.collaboratorID, fix.activeWorkspace); p07cContainsIssue(got, fix.projectlessIssue) {
		t.Fatalf("a foreign projectless row leaked to a Project collaborator: %+v", got)
	}
}

// TestP07CInboxArchivedAppliesSameBoundary pins the archived sub-view to the
// same contract as the main list.
func TestP07CInboxArchivedAppliesSameBoundary(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupP07CInboxFixture(t)
	grantID := p07cGrant(t, fix.projectID, "user", fix.collaboratorID, "viewer")
	p07cInsertInboxRow(t, fix.ownerWorkspace, fix.collaboratorID, fix.issueID, "P07C archived notification", true)

	if got := p07cListArchivedInbox(t, fix.collaboratorID, fix.activeWorkspace); !p07cContainsIssue(got, fix.issueID) {
		t.Fatalf("archived project notification not readable by grant recipient: %+v", got)
	}

	p07cRevoke(t, grantID)
	if got := p07cListArchivedInbox(t, fix.collaboratorID, fix.activeWorkspace); p07cContainsIssue(got, fix.issueID) {
		t.Fatalf("revoked grant still exposes the archived notification: %+v", got)
	}
}

// TestP07CInboxMutationRevokeNoByIdOracle covers the single-item mutation path:
// the grant recipient may mark the stored row read, and after revocation that
// id answers 404 rather than revealing the row.
func TestP07CInboxMutationRevokeNoByIdOracle(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupP07CInboxFixture(t)
	grantID := p07cGrant(t, fix.projectID, "user", fix.collaboratorID, "viewer")
	itemID := p07cInsertInboxRow(t, fix.ownerWorkspace, fix.collaboratorID, fix.issueID, "P07C mutation", false)

	req := testutil.WithURLParams(
		p07cRequest(fix.collaboratorID, fix.activeWorkspace, http.MethodPost, "/api/inbox/"+itemID+"/read"),
		"id", itemID)
	testutil.Call(t, inboxWorkspaceHandler(testHandler.MarkInboxRead), req).Want(http.StatusOK)
	if got := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = true`, itemID); got != 1 {
		t.Fatalf("grant recipient could not mark their own row read")
	}

	p07cRevoke(t, grantID)

	// Reset so a successful unauthorized mutation would be observable.
	dbfx.Exec(t, `UPDATE inbox_item SET read = false WHERE id = $1`, itemID)
	testutil.Call(t, inboxWorkspaceHandler(testHandler.MarkInboxRead), req).Want(http.StatusNotFound)
	if got := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = true`, itemID); got != 0 {
		t.Fatalf("revoked grant mutated a row it may no longer read")
	}

	unknown := uuid.NewString()
	unknownReq := testutil.WithURLParams(
		p07cRequest(fix.collaboratorID, fix.activeWorkspace, http.MethodPost, "/api/inbox/"+unknown+"/read"),
		"id", unknown)
	testutil.Call(t, inboxWorkspaceHandler(testHandler.MarkInboxRead), unknownReq).Want(http.StatusNotFound)
}

// TestP07CInboxCountsDoNotRevealInaccessibleProjects pins both count surfaces:
// an ordinary member's unread count and summary include their readable native
// rows but never the private Project's row they cannot read.
func TestP07CInboxCountsDoNotRevealInaccessibleProjects(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupP07CInboxFixture(t)
	// The owner is a plain member here so the private Project is unreadable.
	memberID := fix.ownerID
	dbfx.Exec(t, `UPDATE member SET role = 'member' WHERE workspace_id = $1 AND user_id = $2`, fix.ownerWorkspace, memberID)

	p07cInsertInboxRow(t, fix.ownerWorkspace, memberID, fix.projectlessIssue, "P07C projectless unread", false)
	p07cInsertInboxRow(t, fix.ownerWorkspace, memberID, fix.issueID, "P07C shared project unread", false)
	p07cInsertInboxRow(t, fix.ownerWorkspace, memberID, fix.privateIssueID, "P07C private project unread", false)

	if got := p07cUnreadCount(t, memberID, fix.ownerWorkspace); got != 2 {
		t.Fatalf("unread count = %d, want 2 (projectless + readable shared project, private excluded)", got)
	}
	summary := p07cUnreadSummary(t, memberID, fix.ownerWorkspace)
	if got := p07cSummaryCount(summary, fix.ownerWorkspace); got != 2 {
		t.Fatalf("summary count for owner workspace = %d, want 2 (private project excluded): %+v", got, summary)
	}
}

// TestP07CInboxOwnerNativeBehaviorUnchanged confirms the owner still reads
// projectless, shared and private rows, so the ACL added for collaborators does
// not narrow the native inbox.
func TestP07CInboxOwnerNativeBehaviorUnchanged(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupP07CInboxFixture(t)
	p07cInsertInboxRow(t, fix.ownerWorkspace, fix.ownerID, fix.projectlessIssue, "P07C owner projectless", false)
	p07cInsertInboxRow(t, fix.ownerWorkspace, fix.ownerID, fix.issueID, "P07C owner shared", false)
	p07cInsertInboxRow(t, fix.ownerWorkspace, fix.ownerID, fix.privateIssueID, "P07C owner private", false)

	items := p07cListInbox(t, fix.ownerID, fix.ownerWorkspace)
	for _, issueID := range []string{fix.projectlessIssue, fix.issueID, fix.privateIssueID} {
		if !p07cContainsIssue(items, issueID) {
			t.Fatalf("owner lost native inbox row for issue %s: %+v", issueID, items)
		}
	}
	if got := p07cUnreadCount(t, fix.ownerID, fix.ownerWorkspace); got != 3 {
		t.Fatalf("owner unread count = %d, want 3", got)
	}
}

// TestP07CInboxInaccessibleNativeProjectHidden pins the native read side too: an
// ordinary Workspace member cannot read a private Project, so its notification
// is absent from the list and from batch mutations, and the batch count does not
// reveal it.
func TestP07CInboxInaccessibleNativeProjectHidden(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := setupP07CInboxFixture(t)
	dbfx.Exec(t, `UPDATE member SET role = 'member' WHERE workspace_id = $1 AND user_id = $2`, fix.ownerWorkspace, fix.ownerID)

	sharedItem := p07cInsertInboxRow(t, fix.ownerWorkspace, fix.ownerID, fix.issueID, "P07C shared unread", false)
	projectlessItem := p07cInsertInboxRow(t, fix.ownerWorkspace, fix.ownerID, fix.projectlessIssue, "P07C projectless unread", false)
	privateItem := p07cInsertInboxRow(t, fix.ownerWorkspace, fix.ownerID, fix.privateIssueID, "P07C private unread", false)

	items := p07cListInbox(t, fix.ownerID, fix.ownerWorkspace)
	if p07cContainsIssue(items, fix.privateIssueID) {
		t.Fatalf("ordinary member read a private Project notification: %+v", items)
	}

	var out map[string]int64
	testutil.Call(t, inboxWorkspaceHandler(testHandler.MarkAllInboxRead),
		p07cRequest(fix.ownerID, fix.ownerWorkspace, http.MethodPost, "/api/inbox/mark-all-read")).
		Want(http.StatusOK).
		JSON(&out)
	if out["count"] != 2 {
		t.Fatalf("mark-all-read count = %d, want 2 (private Project excluded)", out["count"])
	}
	for _, marked := range []string{sharedItem, projectlessItem} {
		if got := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = true`, marked); got != 1 {
			t.Fatalf("readable item %s was not marked read", marked)
		}
	}
	if got := dbfx.Count(t, `SELECT count(*) FROM inbox_item WHERE id = $1 AND read = false`, privateItem); got != 1 {
		t.Fatalf("batch mutation touched an inaccessible Project's row")
	}
}
