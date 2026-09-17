package middleware

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestR2DReadableIssueProjectIDsIsWired(t *testing.T) {
	t.Parallel()
	// Compile-level guard: the middleware must expose the cross-Workspace
	// helper and no longer the Workspace-bound one.
	var fn func(*db.Queries, *http.Request, string, string) ([]string, error) = r2dReadableIssueProjectIDs
	if fn == nil {
		t.Fatal("r2dReadableIssueProjectIDs is not wired")
	}
}

func TestR2DApplyReadableProjectValuesDefaultScope(t *testing.T) {
	t.Parallel()
	values := make(url.Values)
	r2dApplyReadableProjectValues(values, []string{"b", "a"})
	if got := values.Get("project_ids"); got != "b,a" && got != "a,b" {
		t.Fatalf("project_ids=%q", got)
	}
	if got := values.Get("include_no_project"); got != "true" {
		t.Fatalf("include_no_project=%q want true", got)
	}
}

func TestR2DApplyReadableProjectValuesIntersectsExplicitFilter(t *testing.T) {
	t.Parallel()
	values := url.Values{
		"project_ids":        []string{"p1,p2,p3"},
		"include_no_project": []string{"false"},
	}
	r2dApplyReadableProjectValues(values, []string{"p2", "p4"})
	if got := values.Get("project_ids"); got != "p2" {
		t.Fatalf("project_ids=%q want p2", got)
	}
	if got := values.Get("include_no_project"); got != "false" {
		t.Fatalf("include_no_project=%q want preserved false", got)
	}
}

func TestR2DApplyReadableProjectValuesEmptyIntersectionFailsClosed(t *testing.T) {
	t.Parallel()
	values := url.Values{"project_ids": []string{"hidden"}}
	r2dApplyReadableProjectValues(values, []string{"visible"})
	if got := values.Get("project_ids"); got != r2dNoProjectUUID {
		t.Fatalf("project_ids=%q want zero sentinel", got)
	}
	if values.Get("include_no_project") != "" {
		t.Fatal("explicit project filter unexpectedly widened to projectless issues")
	}
}

func TestR2DApplyReadableProjectValuesNoReadableProjectsKeepsProjectless(t *testing.T) {
	t.Parallel()
	values := make(url.Values)
	r2dApplyReadableProjectValues(values, nil)
	if got := values.Get("project_ids"); got != r2dNoProjectUUID {
		t.Fatalf("project_ids=%q want zero sentinel", got)
	}
	if got := values.Get("include_no_project"); got != "true" {
		t.Fatalf("include_no_project=%q want true", got)
	}
}

func TestR2DIntersectProjectIDs(t *testing.T) {
	t.Parallel()
	got := r2dIntersectProjectIDs(" p3,p1,p3,p2 ", []string{"p1", "p3"})
	want := []string{"p1", "p3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("intersection=%v want %v", got, want)
	}
}

func TestR2DIssueCollectionNeedsVisibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/api/issues", true},
		{http.MethodGet, "/api/issues/grouped", true},
		{http.MethodPost, "/api/issues/query", true},
		{http.MethodGet, "/api/issues/search", false},
		{http.MethodPost, "/api/issues/table/rows", false},
		{http.MethodPost, "/api/issues", false},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, "http://example.test"+tt.path, nil)
		if got := r2dIssueCollectionNeedsVisibility(req); got != tt.want {
			t.Errorf("%s %s got %v want %v", tt.method, tt.path, got, tt.want)
		}
	}
}
