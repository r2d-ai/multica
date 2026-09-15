package middleware

import (
	"net/http"
	"net/http/httptest"
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
		{http.MethodGet, "/api/issues/table/groups", true},
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
