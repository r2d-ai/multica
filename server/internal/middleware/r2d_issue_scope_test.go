package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestR2DReadJSONFieldsRestoresBody(t *testing.T) {
	t.Parallel()
	body := `{"project_id":"11111111-1111-1111-1111-111111111111","title":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "http://example.test/api/issues", strings.NewReader(body))

	fields, err := r2dReadJSONFields(req)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["project_id"]; !ok {
		t.Fatal("project_id missing after decode")
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != body {
		t.Fatalf("restored body = %q, want %q", restored, body)
	}
	if req.ContentLength != int64(len(body)) {
		t.Fatalf("content length = %d, want %d", req.ContentLength, len(body))
	}
}

func TestR2DRawUUID(t *testing.T) {
	t.Parallel()
	const id = "11111111-1111-1111-1111-111111111111"
	got, err := r2dRawUUID(json.RawMessage(`"` + id + `"`))
	if err != nil || got != id {
		t.Fatalf("valid UUID = %q, %v; want %q, nil", got, err, id)
	}
	got, err = r2dRawUUID(json.RawMessage(`null`))
	if err != nil || got != "" {
		t.Fatalf("null UUID = %q, %v; want empty, nil", got, err)
	}
	for _, raw := range []string{`""`, `"not-a-uuid"`, `123`} {
		if _, err := r2dRawUUID(json.RawMessage(raw)); err == nil {
			t.Errorf("invalid UUID payload %s accepted", raw)
		}
	}
}

func TestR2DForeignProjectRestrictedFields(t *testing.T) {
	t.Parallel()

	for _, field := range []string{
		"assignee_type", "assignee_id", "attachment_ids", "label_ids",
		"origin_type", "origin_id",
	} {
		fields := map[string]json.RawMessage{field: json.RawMessage(`[]`)}
		if field == "assignee_type" || field == "origin_type" {
			fields[field] = json.RawMessage(`"member"`)
		}
		if !r2dForeignProjectRestrictedFields(fields) {
			t.Errorf("restricted field %q was not rejected", field)
		}
	}

	if r2dForeignProjectRestrictedFields(map[string]json.RawMessage{
		"title":      json.RawMessage(`"safe"`),
		"project_id": json.RawMessage(`"11111111-1111-1111-1111-111111111111"`),
	}) {
		t.Fatal("project-native fields unexpectedly treated as workspace-owned")
	}
	if r2dForeignProjectRestrictedFields(map[string]json.RawMessage{
		"assignee_id": json.RawMessage(`null`),
	}) {
		t.Fatal("null restricted create field should not request workspace inventory")
	}
}

func TestR2DAssigneeFieldDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fields     map[string]json.RawMessage
		assignable bool
		wantOK     bool
	}{
		{"unrelated field", map[string]json.RawMessage{"title": json.RawMessage(`"x"`)}, false, true},
		{"assignee null clears", map[string]json.RawMessage{"assignee_type": json.RawMessage(`null`), "assignee_id": json.RawMessage(`null`)}, false, true},
		{"assignee absent clears", map[string]json.RawMessage{"title": json.RawMessage(`"x"`), "assignee_id": json.RawMessage(`null`)}, false, true},
		{"member assignee allowed when assignable", map[string]json.RawMessage{"assignee_type": json.RawMessage(`"member"`), "assignee_id": json.RawMessage(`"u1"`)}, true, true},
		{"member assignee rejected when not assignable", map[string]json.RawMessage{"assignee_type": json.RawMessage(`"member"`), "assignee_id": json.RawMessage(`"u1"`)}, false, false},
		{"agent assignee always rejected", map[string]json.RawMessage{"assignee_type": json.RawMessage(`"agent"`), "assignee_id": json.RawMessage(`"a1"`)}, true, false},
		{"squad assignee always rejected", map[string]json.RawMessage{"assignee_type": json.RawMessage(`"squad"`), "assignee_id": json.RawMessage(`"s1"`)}, true, false},
		{"attachments always rejected", map[string]json.RawMessage{"attachment_ids": json.RawMessage(`["f1"]`)}, true, false},
		{"labels always rejected", map[string]json.RawMessage{"label_ids": json.RawMessage(`["l1"]`)}, true, false},
	}

	for _, tt := range tests {
		got, _ := r2dAssigneeFieldDecision(tt.fields, tt.assignable)
		if got != tt.wantOK {
			t.Errorf("%s: allowed=%v want %v", tt.name, got, tt.wantOK)
		}
	}
}
