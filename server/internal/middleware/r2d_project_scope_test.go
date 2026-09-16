package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/r2dauth"
)

func TestR2DProjectOperation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		method string
		suffix string
		want   r2dauth.Operation
	}{
		{http.MethodGet, "", r2dauth.OperationRead},
		{http.MethodGet, "/resources", r2dauth.OperationRead},
		{http.MethodPut, "", r2dauth.OperationManage},
		{http.MethodDelete, "", r2dauth.OperationManage},
		{http.MethodPost, "/resources", r2dauth.OperationManage},
		{http.MethodPatch, "/sharing", r2dauth.OperationShare},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, "http://example.test/api/projects/p", nil)
		if got := r2dProjectOperation(req, tt.suffix); got != tt.want {
			t.Errorf("%s %s: operation=%q want %q", tt.method, tt.suffix, got, tt.want)
		}
	}
}

func TestR2DIssueOperation(t *testing.T) {
	t.Parallel()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(method, "http://example.test/api/issues/i", nil)
		if got := r2dIssueOperation(req); got != r2dauth.OperationRead {
			t.Errorf("%s: operation=%q want read", method, got)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req := httptest.NewRequest(method, "http://example.test/api/issues/i", nil)
		if got := r2dIssueOperation(req); got != r2dauth.OperationContribute {
			t.Errorf("%s: operation=%q want contribute", method, got)
		}
	}
}

func TestR2DExplicitProjectGrant(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"viewer", "member", "manager"} {
		if !r2dExplicitProjectGrant(role) {
			t.Errorf("valid explicit project role %q rejected", role)
		}
	}
	for _, role := range []string{"", "owner", "admin", "global_observer"} {
		if r2dExplicitProjectGrant(role) {
			t.Errorf("non-project grant role %q accepted", role)
		}
	}
}

func TestR2DRawProjectRows(t *testing.T) {
	t.Parallel()
	rows, err := r2dRawProjectRows([]string{
		`{"id":"p1","workspace_id":"w1","title":"one"}`,
		`{"id":"p2","workspace_id":"w2","title":"two"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || !json.Valid(rows[0]) || !json.Valid(rows[1]) {
		t.Fatalf("unexpected raw rows: %#v", rows)
	}
	if _, err := r2dRawProjectRows([]string{`{"id":`}); err == nil {
		t.Fatal("invalid JSON row accepted")
	}
}

func TestShouldR2DFilterCollection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/api/projects", true},
		{http.MethodGet, "/api/projects/search", true},
		{http.MethodGet, "/api/issues", true},
		{http.MethodGet, "/api/issues/search", true},
		{http.MethodPost, "/api/projects", false},
		{http.MethodGet, "/api/squads", false},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, "http://example.test"+tt.path, nil)
		if got := shouldR2DFilterCollection(req); got != tt.want {
			t.Errorf("%s %s: got %v want %v", tt.method, tt.path, got, tt.want)
		}
	}
}

func TestR2DUnfilteredIssueSurface(t *testing.T) {
	t.Parallel()
	id := "11111111-1111-1111-1111-111111111111"
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/api/issues", false},
		{http.MethodPost, "/api/issues", true},
		{http.MethodGet, "/api/issues/search", false},
		{http.MethodPost, "/api/issues/table/groups", false},
		{http.MethodPost, "/api/issues/table/rows", false},
		{http.MethodPost, "/api/issues/table/facets", false},
		{http.MethodPost, "/api/issues/query", true},
		{http.MethodPost, "/api/issues/batch-update", true},
		{http.MethodGet, "/api/issues/ABC-42", true},
		{http.MethodGet, "/api/issues/" + id, false},
		{http.MethodPut, "/api/issues/" + id, false},
		{http.MethodGet, "/api/projects", false},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, "http://example.test"+tt.path, nil)
		if got := r2dUnfilteredIssueSurface(req); got != tt.want {
			t.Errorf("%s %s: got %v want %v", tt.method, tt.path, got, tt.want)
		}
	}
}

func TestR2DIssueTableProjectScope(t *testing.T) {
	t.Parallel()
	projectID := "11111111-1111-1111-1111-111111111111"
	req := httptest.NewRequest(
		http.MethodPost,
		"http://example.test/api/issues/table/rows",
		strings.NewReader(`{"query":{"scope":{"kind":"project","project_id":"`+projectID+`"}}}`),
	)

	for i := 0; i < 2; i++ {
		got, ok := r2dIssueTableProjectScope(req)
		if !ok || got != projectID {
			t.Fatalf("pass %d: project scope=(%q,%v), want (%q,true)", i+1, got, ok, projectID)
		}
	}

	workspaceReq := httptest.NewRequest(
		http.MethodPost,
		"http://example.test/api/issues/table/groups",
		strings.NewReader(`{"query":{"scope":{"kind":"workspace"}}}`),
	)
	if got, ok := r2dIssueTableProjectScope(workspaceReq); ok || got != "" {
		t.Fatalf("workspace scope widened unexpectedly: (%q,%v)", got, ok)
	}

	invalidReq := httptest.NewRequest(
		http.MethodPost,
		"http://example.test/api/issues/table/facets",
		strings.NewReader(`{"query":{"scope":{"kind":"project","project_id":"not-a-uuid"}}}`),
	)
	if got, ok := r2dIssueTableProjectScope(invalidReq); ok || got != "" {
		t.Fatalf("invalid project scope widened unexpectedly: (%q,%v)", got, ok)
	}
}

func TestParseR2DUUID(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"11111111-1111-1111-1111-111111111111",
		"11111111111111111111111111111111",
	} {
		if _, err := parseR2DUUID(value); err != nil {
			t.Errorf("valid UUID %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"", "not-a-uuid", "zzzzzzzz-1111-1111-1111-111111111111"} {
		if _, err := parseR2DUUID(value); err == nil {
			t.Errorf("invalid UUID %q accepted", value)
		}
	}
}
