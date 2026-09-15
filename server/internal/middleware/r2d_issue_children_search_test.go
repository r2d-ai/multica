package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestR2DDirectIssueChildrenPath(t *testing.T) {
	t.Parallel()
	id := "11111111-1111-1111-1111-111111111111"
	for _, path := range []string{
		"/api/issues/children",
		"/api/issues/not-a-uuid/children",
		"/api/issues/" + id + "/comments",
		"/api/issues/" + id + "/children/more",
	} {
		if r2dDirectIssueChildrenPath(path) {
			t.Errorf("non-direct children path %q accepted", path)
		}
	}
	if !r2dDirectIssueChildrenPath("/api/issues/" + id + "/children") {
		t.Fatal("UUID direct children path rejected")
	}
}

func TestR2DParseIssueParentIDs(t *testing.T) {
	t.Parallel()
	id1 := "11111111-1111-1111-1111-111111111111"
	id2 := "22222222-2222-2222-2222-222222222222"
	got, ok := r2dParseIssueParentIDs("  " + id1 + "," + id2 + "," + id1 + " ")
	if !ok || len(got) != 2 || got[0] != id1 || got[1] != id2 {
		t.Fatalf("deduped parent ids = %#v, %v", got, ok)
	}
	if _, ok := r2dParseIssueParentIDs(id1 + ",not-a-uuid"); ok {
		t.Fatal("invalid parent id accepted")
	}
	tooMany := strings.TrimSuffix(strings.Repeat(id1+",", r2dBatchIssueChildrenLimit+1), ",")
	if _, ok := r2dParseIssueParentIDs(tooMany); ok {
		t.Fatal("oversized parent id set accepted")
	}
}

func TestR2DProjectSearchWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		query               string
		limit, offset, need int
		ok                  bool
	}{
		{"", 20, 0, 20, true},
		{"?limit=100&offset=10", 50, 10, 60, true},
		{"?limit=-1&offset=-2", 20, 0, 20, true},
		{"?offset=490", 20, 490, 510, false},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/api/issues/search"+tt.query, nil)
		limit, offset, need, ok := r2dProjectSearchWindow(r)
		if limit != tt.limit || offset != tt.offset || need != tt.need || ok != tt.ok {
			t.Errorf("%s => (%d,%d,%d,%v), want (%d,%d,%d,%v)", tt.query, limit, offset, need, ok, tt.limit, tt.offset, tt.need, tt.ok)
		}
	}
}

func TestR2DFilterProjectSearchPage(t *testing.T) {
	t.Parallel()
	projectID := "11111111-1111-1111-1111-111111111111"
	otherID := "22222222-2222-2222-2222-222222222222"
	rows := []json.RawMessage{
		json.RawMessage(`{"id":"a","project_id":"` + projectID + `"}`),
		json.RawMessage(`{"id":"b","project_id":"` + otherID + `"}`),
		json.RawMessage(`{"id":"c","project_id":null}`),
		json.RawMessage(`{"id":"d","project_id":"` + projectID + `"}`),
		json.RawMessage(`{"id":`),
	}
	got := r2dFilterProjectSearchPage(rows, projectID)
	if len(got) != 2 {
		t.Fatalf("filtered rows = %d, want 2", len(got))
	}
	var first, second struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(got[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got[1], &second); err != nil {
		t.Fatal(err)
	}
	if first.ID != "a" || second.ID != "d" {
		t.Fatalf("ranking changed: got %q then %q", first.ID, second.ID)
	}
}

func TestR2DBatchChildrenNoLongerFailClosed(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest(http.MethodGet, "http://example.test/api/issues/children?parent_ids=11111111-1111-1111-1111-111111111111", nil)
	if r2dUnfilteredIssueSurface(r) {
		t.Fatal("ACL-aware batched children route still marked fail-closed")
	}
}
