package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// P07-G dashboard project-scope regression coverage.
//
// The six dashboard rollups used to accept a caller-controlled ?project_id=
// and pass it straight to SQL on the strength of Workspace membership alone,
// so any ordinary member could read a private Project's token spend, run time,
// task counts and failure classes by naming it — or by omitting the filter,
// which aggregated every Project in the Workspace into the same response.
//
// These tests assert the exported ROW SET for each endpoint, not just the HTTP
// status: a plain member must see the shared/workspace-visible Project and the
// Workspace's projectless work, and must never see the private Project's rows,
// while the Workspace owner sees all three. The fixture runs in a dedicated
// Workspace so the row set is exact rather than a delta against shared state.

const (
	scopeVisibleModel     = "scope-visible-model"
	scopeHiddenModel      = "scope-hidden-model"
	scopeProjectlessModel = "scope-projectless-model"
	scopeVisibleFailure   = "scope_visible_failure"
	scopeHiddenFailure    = "scope_hidden_failure"
	scopeOrderedProvider  = "scope-provider"
)

// dashboardScopeFixture is one Workspace holding a workspace-visible Project,
// a private Project, and projectless work — each with a terminal task billed to
// the SAME agent, so the per-agent rollups hinge on the Project ACL rather than
// on per-agent visibility.
type dashboardScopeFixture struct {
	workspaceID        string
	ownerID            string
	memberID           string
	agentID            string
	runtimeID          string
	visibleProjectID   string
	hiddenProjectID    string
	visibleIssueID     string
	hiddenIssueID      string
	projectlessIssueID string
}

func newDashboardScopeFixture(t *testing.T) dashboardScopeFixture {
	t.Helper()

	var slug string
	dbfx.QueryRow(t, `SELECT 'dashboard-scope-' || replace(gen_random_uuid()::text, '-', '')`).Scan(&slug)
	workspaceID := dbfx.Workspace(t, "Dashboard scope workspace", slug)

	ownerID := dbfx.Insert(t, "user", testutil.Cols{
		"name":  "Dashboard Scope Owner",
		"email": testutil.Raw("'dashboard-scope-owner-' || replace(gen_random_uuid()::text, '-', '') || '@multica.test'"),
	})
	memberID := dbfx.Insert(t, "user", testutil.Cols{
		"name":  "Dashboard Scope Member",
		"email": testutil.Raw("'dashboard-scope-member-' || replace(gen_random_uuid()::text, '-', '') || '@multica.test'"),
	})
	dbfx.Member(t, workspaceID, ownerID, "owner")
	dbfx.Member(t, workspaceID, memberID, "member")

	// Agent owned by the plain member and public to the Workspace, so neither
	// endpoint has to fold it away: any row-set difference is the Project ACL.
	runtimeID := dbfx.Runtime(t, "dashboard-scope-runtime", testutil.Cols{
		"workspace_id": workspaceID,
		"owner_id":     memberID,
	})
	agentID := dbfx.Agent(t, "dashboard-scope-agent", runtimeID, testutil.Cols{
		"workspace_id":    workspaceID,
		"owner_id":        memberID,
		"visibility":      "workspace",
		"permission_mode": "public_to",
	})

	visibleProjectID := dbfx.Project(t, "dashboard scope visible project", testutil.Cols{"workspace_id": workspaceID})
	hiddenProjectID := dbfx.Project(t, "dashboard scope hidden project", testutil.Cols{"workspace_id": workspaceID})
	dbfx.Exec(t, `INSERT INTO r2d_project_extra (project_id, visibility) VALUES ($1, 'private')`, hiddenProjectID)
	dbfx.Cleanup(t, `DELETE FROM r2d_project_extra WHERE project_id = $1`, hiddenProjectID)

	issue := func(title, projectID string) string {
		over := testutil.Cols{"workspace_id": workspaceID, "creator_id": ownerID}
		if projectID != "" {
			over["project_id"] = projectID
		}
		return dbfx.Issue(t, title, over)
	}

	return dashboardScopeFixture{
		workspaceID:        workspaceID,
		ownerID:            ownerID,
		memberID:           memberID,
		agentID:            agentID,
		runtimeID:          runtimeID,
		visibleProjectID:   visibleProjectID,
		hiddenProjectID:    hiddenProjectID,
		visibleIssueID:     issue("scope visible issue", visibleProjectID),
		hiddenIssueID:      issue("scope hidden issue", hiddenProjectID),
		projectlessIssueID: issue("scope projectless issue", ""),
	}
}

// TestDashboardProjectScopeRowSets covers all six endpoints workspace-wide: the
// plain member's row set must exclude the private Project but keep the visible
// Project and the projectless work, and the owner's must include everything.
func TestDashboardProjectScopeRowSets(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := newDashboardScopeFixture(t)

	started, completed := runFinishedToday(pinDayWindowClock(t, time.Now()), time.UTC)

	seed := func(issueID, status, failureReason, model string) {
		taskID := dbfx.Task(t, fix.agentID, testutil.Cols{
			"issue_id":       issueID,
			"runtime_id":     fix.runtimeID,
			"status":         status,
			"failure_reason": failureReason,
			"started_at":     started,
			"completed_at":   completed,
			"created_at":     started,
		})
		dbfx.Cleanup(t, `DELETE FROM task_usage WHERE task_id = $1`, taskID)
		dbfx.Exec(t, `
			INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
			VALUES ($1, $2, $3, 100, 0, $4)
		`, taskID, scopeOrderedProvider, model, started)
	}
	seed(fix.visibleIssueID, "failed", scopeVisibleFailure, scopeVisibleModel)
	seed(fix.hiddenIssueID, "failed", scopeHiddenFailure, scopeHiddenModel)
	seed(fix.projectlessIssueID, "completed", "", scopeProjectlessModel)

	// The rollup table carries no FKs to the fixture rows; drop its buckets
	// before the tasks that produced them are deleted.
	dbfx.Cleanup(t, `DELETE FROM task_usage_hourly WHERE workspace_id = $1`, fix.workspaceID)
	dbfx.Exec(t, `SELECT rollup_task_usage_hourly_window('1970-01-01'::timestamptz, now() + interval '1 hour')`)

	get := func(userID, path string, handler func(http.ResponseWriter, *http.Request)) (int, []byte) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-User-ID", userID)
		req.Header.Set("X-Workspace-ID", fix.workspaceID)
		handler(w, req)
		return w.Code, w.Body.Bytes()
	}
	read := func(t *testing.T, userID, path string, handler func(http.ResponseWriter, *http.Request)) []map[string]any {
		t.Helper()
		status, body := get(userID, path, handler)
		if status != http.StatusOK {
			t.Fatalf("%s as %s: expected 200, got %d: %s", path, userID, status, body)
		}
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("%s: decode response: %v\nbody: %s", path, err, body)
		}
		return rows
	}

	fieldSet := func(rows []map[string]any, field string) map[string]bool {
		set := make(map[string]bool, len(rows))
		for _, row := range rows {
			if value, ok := row[field].(string); ok {
				set[value] = true
			}
		}
		return set
	}
	requireSet := func(t *testing.T, label string, got map[string]bool, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: row set = %v, want %v", label, sortedSetKeys(got), want)
		}
		for _, value := range want {
			if !got[value] {
				t.Fatalf("%s: row %q missing from %v", label, value, sortedSetKeys(got))
			}
		}
	}

	const window = "days=7&tz=UTC"

	// ---- usage/daily: per-(date, model) ------------------------------------

	for _, view := range []struct {
		label  string
		userID string
		want   []string
	}{
		{"usage/daily (member)", fix.memberID, []string{scopeVisibleModel, scopeProjectlessModel}},
		{"usage/daily (owner)", fix.ownerID, []string{scopeVisibleModel, scopeHiddenModel, scopeProjectlessModel}},
	} {
		rows := read(t, view.userID, "/api/dashboard/usage/daily?"+window, testHandler.GetDashboardUsageDaily)
		requireSet(t, view.label, fieldSet(rows, "model"), view.want...)
	}

	// ---- usage/by-agent: per-(agent, model) --------------------------------

	for _, view := range []struct {
		label  string
		userID string
		want   []string
	}{
		{"usage/by-agent (member)", fix.memberID, []string{scopeVisibleModel, scopeProjectlessModel}},
		{"usage/by-agent (owner)", fix.ownerID, []string{scopeVisibleModel, scopeHiddenModel, scopeProjectlessModel}},
	} {
		rows := read(t, view.userID, "/api/dashboard/usage/by-agent?"+window, testHandler.GetDashboardUsageByAgent)
		requireSet(t, view.label, fieldSet(rows, "model"), view.want...)
	}

	// ---- agent-runtime: per-agent, no model dimension ----------------------

	agentRow := func(t *testing.T, label, userID string) map[string]any {
		t.Helper()
		rows := read(t, userID, "/api/dashboard/agent-runtime?"+window, testHandler.GetDashboardAgentRunTime)
		for _, row := range rows {
			if row["agent_id"] == fix.agentID {
				return row
			}
		}
		t.Fatalf("%s: agent %s missing from %v", label, fix.agentID, rows)
		return nil
	}
	memberAgent := agentRow(t, "agent-runtime (member)", fix.memberID)
	if got := memberAgent["task_count"]; got != float64(2) {
		t.Errorf("agent-runtime (member): task_count = %v, want 2 (visible + projectless)", got)
	}
	if got := memberAgent["failed_count"]; got != float64(1) {
		t.Errorf("agent-runtime (member): failed_count = %v, want 1 (visible project only)", got)
	}
	ownerAgent := agentRow(t, "agent-runtime (owner)", fix.ownerID)
	if got := ownerAgent["task_count"]; got != float64(3) {
		t.Errorf("agent-runtime (owner): task_count = %v, want 3", got)
	}
	if got := ownerAgent["failed_count"]; got != float64(2) {
		t.Errorf("agent-runtime (owner): failed_count = %v, want 2", got)
	}

	// ---- runtime/daily: per-date, no model dimension -----------------------

	sumTaskCount := func(rows []map[string]any) float64 {
		var total float64
		for _, row := range rows {
			if count, ok := row["task_count"].(float64); ok {
				total += count
			}
		}
		return total
	}
	memberDaily := read(t, fix.memberID, "/api/dashboard/runtime/daily?"+window, testHandler.GetDashboardRunTimeDaily)
	if got := sumTaskCount(memberDaily); got != 2 {
		t.Errorf("runtime/daily (member): total task_count = %v, want 2 (hidden Project excluded)", got)
	}
	ownerDaily := read(t, fix.ownerID, "/api/dashboard/runtime/daily?"+window, testHandler.GetDashboardRunTimeDaily)
	if got := sumTaskCount(ownerDaily); got != 3 {
		t.Errorf("runtime/daily (owner): total task_count = %v, want 3", got)
	}

	// ---- failures/daily: per-(date, failure_reason) ------------------------

	for _, view := range []struct {
		label  string
		userID string
		want   []string
	}{
		{"failures/daily (member)", fix.memberID, []string{"", scopeVisibleFailure}},
		{"failures/daily (owner)", fix.ownerID, []string{"", scopeVisibleFailure, scopeHiddenFailure}},
	} {
		rows := read(t, view.userID, "/api/dashboard/failures/daily?"+window, testHandler.GetDashboardFailuresDaily)
		requireSet(t, view.label, fieldSet(rows, "failure_reason"), view.want...)
	}

	// ---- failures/by-agent: per-(agent, failure_reason) --------------------

	for _, view := range []struct {
		label  string
		userID string
		want   []string
	}{
		{"failures/by-agent (member)", fix.memberID, []string{"", scopeVisibleFailure}},
		{"failures/by-agent (owner)", fix.ownerID, []string{"", scopeVisibleFailure, scopeHiddenFailure}},
	} {
		rows := read(t, view.userID, "/api/dashboard/failures/by-agent?"+window, testHandler.GetDashboardFailuresByAgent)
		requireSet(t, view.label, fieldSet(rows, "failure_reason"), view.want...)
	}
}

// TestDashboardProjectScopeSingleProject covers the ?project_id= half of the
// fix: a single-Project request is answered only with Project read. The plain
// member must get a non-disclosing 404 for the private Project on every
// endpoint, read their own visible Project, and the owner must still read the
// private Project in full.
func TestDashboardProjectScopeSingleProject(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	fix := newDashboardScopeFixture(t)

	started, completed := runFinishedToday(pinDayWindowClock(t, time.Now()), time.UTC)
	seed := func(issueID, model string) {
		taskID := dbfx.Task(t, fix.agentID, testutil.Cols{
			"issue_id":     issueID,
			"runtime_id":   fix.runtimeID,
			"status":       "completed",
			"started_at":   started,
			"completed_at": completed,
			"created_at":   started,
		})
		dbfx.Cleanup(t, `DELETE FROM task_usage WHERE task_id = $1`, taskID)
		dbfx.Exec(t, `
			INSERT INTO task_usage (task_id, provider, model, input_tokens, output_tokens, created_at)
			VALUES ($1, $2, $3, 100, 0, $4)
		`, taskID, scopeOrderedProvider, model, started)
	}
	seed(fix.visibleIssueID, scopeVisibleModel)
	seed(fix.hiddenIssueID, scopeHiddenModel)
	dbfx.Cleanup(t, `DELETE FROM task_usage_hourly WHERE workspace_id = $1`, fix.workspaceID)
	dbfx.Exec(t, `SELECT rollup_task_usage_hourly_window('1970-01-01'::timestamptz, now() + interval '1 hour')`)

	endpoints := []struct {
		name    string
		path    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{"usage/daily", "/api/dashboard/usage/daily?days=7&tz=UTC", testHandler.GetDashboardUsageDaily},
		{"usage/by-agent", "/api/dashboard/usage/by-agent?days=7&tz=UTC", testHandler.GetDashboardUsageByAgent},
		{"agent-runtime", "/api/dashboard/agent-runtime?days=7&tz=UTC", testHandler.GetDashboardAgentRunTime},
		{"runtime/daily", "/api/dashboard/runtime/daily?days=7&tz=UTC", testHandler.GetDashboardRunTimeDaily},
		{"failures/daily", "/api/dashboard/failures/daily?days=7&tz=UTC", testHandler.GetDashboardFailuresDaily},
		{"failures/by-agent", "/api/dashboard/failures/by-agent?days=7&tz=UTC", testHandler.GetDashboardFailuresByAgent},
	}

	get := func(userID, path string, handler func(http.ResponseWriter, *http.Request)) (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-User-ID", userID)
		req.Header.Set("X-Workspace-ID", fix.workspaceID)
		handler(w, req)
		return w.Code, w.Body.String()
	}

	for _, ep := range endpoints {
		// The plain member cannot read the private Project, and the rejection
		// must not echo its id or data back.
		status, body := get(fix.memberID, ep.path+"&project_id="+fix.hiddenProjectID, ep.handler)
		if status != http.StatusNotFound {
			t.Errorf("%s (member, private project): expected 404, got %d: %s", ep.name, status, body)
		}
		if includesAny(body, fix.hiddenProjectID, scopeHiddenModel, scopeHiddenFailure) {
			t.Errorf("%s (member, private project): rejection leaked private project data: %s", ep.name, body)
		}

		// The member's own visible Project still reads.
		if status, body = get(fix.memberID, ep.path+"&project_id="+fix.visibleProjectID, ep.handler); status != http.StatusOK {
			t.Errorf("%s (member, visible project): expected 200, got %d: %s", ep.name, status, body)
		}

		// The owner still reads the private Project.
		if status, body = get(fix.ownerID, ep.path+"&project_id="+fix.hiddenProjectID, ep.handler); status != http.StatusOK {
			t.Errorf("%s (owner, private project): expected 200, got %d: %s", ep.name, status, body)
		}
	}

	// Row set for the usage half: the member's visible-Project read names only
	// the visible model, and the owner's private-Project read only the hidden
	// one.
	models := func(t *testing.T, userID, projectID string) map[string]bool {
		t.Helper()
		status, body := get(userID, "/api/dashboard/usage/daily?days=7&tz=UTC&project_id="+projectID,
			testHandler.GetDashboardUsageDaily)
		if status != http.StatusOK {
			t.Fatalf("usage/daily project=%s: expected 200, got %d: %s", projectID, status, body)
		}
		var rows []map[string]any
		if err := json.Unmarshal([]byte(body), &rows); err != nil {
			t.Fatalf("decode usage/daily: %v", err)
		}
		set := map[string]bool{}
		for _, row := range rows {
			if model, ok := row["model"].(string); ok {
				set[model] = true
			}
		}
		return set
	}
	if got := models(t, fix.memberID, fix.visibleProjectID); len(got) != 1 || !got[scopeVisibleModel] {
		t.Errorf("usage/daily (member, visible project): models = %v, want only %s", sortedSetKeys(got), scopeVisibleModel)
	}
	if got := models(t, fix.ownerID, fix.hiddenProjectID); len(got) != 1 || !got[scopeHiddenModel] {
		t.Errorf("usage/daily (owner, private project): models = %v, want only %s", sortedSetKeys(got), scopeHiddenModel)
	}
}

// sortedSetKeys returns the members of a set in a stable order for messages.
func sortedSetKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// includesAny reports whether haystack contains any of needles.
func includesAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
