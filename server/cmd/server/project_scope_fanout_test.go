package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type fakeProjectResolver struct {
	projects map[string]string
	err      error
	calls    []string
}

func (f *fakeProjectResolver) ResolveIssueProject(_ context.Context, issueID string) (string, error) {
	f.calls = append(f.calls, issueID)
	if f.err != nil {
		return "", f.err
	}
	return f.projects[issueID], nil
}

func TestProjectScopeTarget(t *testing.T) {
	cases := []struct {
		name          string
		payload       any
		wantProjectID string
		wantIssueID   string
	}{
		{
			name:          "issue object carries project_id",
			payload:       map[string]any{"issue": map[string]any{"id": "issue-1", "project_id": "project-1"}},
			wantProjectID: "project-1",
			wantIssueID:   "issue-1",
		},
		{
			name:        "projectless issue object stays projectless",
			payload:     map[string]any{"issue": map[string]any{"id": "issue-1", "project_id": nil}},
			wantIssueID: "issue-1",
		},
		{
			name:        "bare issue_id falls back to the resolver",
			payload:     map[string]any{"issue_id": "issue-2", "labels": []any{}},
			wantIssueID: "issue-2",
		},
		{
			name:        "comment carries issue_id",
			payload:     map[string]any{"comment": map[string]any{"issue_id": "issue-3"}},
			wantIssueID: "issue-3",
		},
		{
			name:    "unrelated payload has no target",
			payload: map[string]any{"hello": "world"},
		},
		{
			name:    "nil payload has no target",
			payload: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projectID, issueID := projectScopeTarget(tc.payload)
			if projectID != tc.wantProjectID || issueID != tc.wantIssueID {
				t.Fatalf("projectScopeTarget = (%q, %q), want (%q, %q)", projectID, issueID, tc.wantProjectID, tc.wantIssueID)
			}
		})
	}
}

// TestStripProjectScopedLeaks pins the P07-B per-event leak boundary: owner
// Workspace inventory and signed read URLs must not survive into a
// Project-scoped frame, at any nesting depth.
func TestStripProjectScopedLeaks(t *testing.T) {
	payload := map[string]any{
		"issue": map[string]any{
			"id":             "issue-1",
			"project_id":     "project-1",
			"title":          "shared work",
			"repo_url":       "git@example.com:owner/private.git",
			"daemon_id":      "daemon-1",
			"runtime_id":     "runtime-1",
			"source_context": map[string]any{"current_source": map[string]any{"url": "https://example.com/issue/1"}},
			"attachments": []any{
				map[string]any{
					"id":           "att-1",
					"filename":     "spec.md",
					"download_url": "https://storage/signed?token=secret",
				},
			},
			"labels": []any{map[string]any{"id": "label-1", "name": "bug"}},
		},
		"members":           []any{map[string]any{"user_id": "member-1"}},
		"vcs_connection_id": "vcs-1",
	}

	stripped, ok := stripProjectScopedLeaks(payload).(map[string]any)
	if !ok {
		t.Fatal("stripped payload is not a map")
	}
	assertNoLeakKeys(t, stripped)

	issue, _ := stripped["issue"].(map[string]any)
	if issue["title"] != "shared work" || issue["project_id"] != "project-1" {
		t.Fatalf("safe issue fields were dropped: %+v", issue)
	}
	attachments, _ := issue["attachments"].([]any)
	if len(attachments) != 1 {
		t.Fatalf("attachment list not preserved: %+v", issue["attachments"])
	}
	if att, _ := attachments[0].(map[string]any); att["filename"] != "spec.md" {
		t.Fatalf("safe attachment fields were dropped: %+v", attachments[0])
	}

	// The input map must not be mutated: other subscribers still receive the
	// untouched owner payload.
	originalIssue, _ := payload["issue"].(map[string]any)
	if _, present := originalIssue["repo_url"]; !present {
		t.Fatal("stripProjectScopedLeaks mutated the producer payload")
	}
}

// TestRegisterListeners_ProjectFanoutMatrix walks every principal x project
// shape that matters for realtime leakage: a Project-backed issue must fan out
// to ScopeProject, a projectless or owner-Workspace-only event must not, and no
// Project frame may carry owner-Workspace-only fields.
func TestRegisterListeners_ProjectFanoutMatrix(t *testing.T) {
	projectIssue := func(projectID any) map[string]any {
		return map[string]any{
			"issue": map[string]any{
				"id":             "issue-1",
				"project_id":     projectID,
				"title":          "shared",
				"repo_url":       "git@example.com:owner/private.git",
				"source_context": map[string]any{"url": "https://example.com/1"},
			},
		}
	}

	cases := []struct {
		name          string
		eventType     string
		payload       any
		wantScopeID   string
		wantWorkspace bool
	}{
		{
			name:          "project-backed issue created fans to project and workspace",
			eventType:     protocol.EventIssueCreated,
			payload:       projectIssue("project-1"),
			wantScopeID:   "project-1",
			wantWorkspace: true,
		},
		{
			name:          "project-backed issue updated fans to project and workspace",
			eventType:     protocol.EventIssueUpdated,
			payload:       projectIssue("project-1"),
			wantScopeID:   "project-1",
			wantWorkspace: true,
		},
		{
			name:          "issue deleted resolves project from issue_id",
			eventType:     protocol.EventIssueDeleted,
			payload:       map[string]any{"issue_id": "issue-1"},
			wantScopeID:   "project-1",
			wantWorkspace: true,
		},
		{
			name:          "comment created resolves project from comment.issue_id",
			eventType:     protocol.EventCommentCreated,
			payload:       map[string]any{"comment": map[string]any{"issue_id": "issue-1"}},
			wantScopeID:   "project-1",
			wantWorkspace: true,
		},
		{
			name:          "labels changed resolves project from issue_id",
			eventType:     protocol.EventIssueLabelsChanged,
			payload:       map[string]any{"issue_id": "issue-1", "labels": []any{}},
			wantScopeID:   "project-1",
			wantWorkspace: true,
		},
		{
			name:          "projectless issue stays owner-workspace-only",
			eventType:     protocol.EventIssueCreated,
			payload:       projectIssue(nil),
			wantWorkspace: true,
		},
		{
			name:          "owner-workspace-only event never reaches project scope",
			eventType:     protocol.EventAgentStatus,
			payload:       map[string]any{"agent_id": "agent-1", "project_id": "project-1"},
			wantWorkspace: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bus := events.New()
			fb := &fakeBroadcaster{}
			resolver := &fakeProjectResolver{projects: map[string]string{"issue-1": "project-1"}}
			registerListeners(bus, fb, resolver)

			bus.Publish(events.Event{
				Type:        tc.eventType,
				WorkspaceID: "ws-owner",
				ActorType:   "member",
				ActorID:     "user-1",
				Payload:     tc.payload,
			})

			if tc.wantWorkspace && len(fb.workspaceCalls) != 1 {
				t.Fatalf("workspace fanout = %d, want 1", len(fb.workspaceCalls))
			}
			if tc.wantScopeID == "" {
				if len(fb.scopeCalls) != 0 {
					t.Fatalf("unexpected project fanout: %+v", fb.scopeCalls)
				}
				return
			}
			if len(fb.scopeCalls) != 1 {
				t.Fatalf("project fanout = %d, want 1", len(fb.scopeCalls))
			}
			call := fb.scopeCalls[0]
			if call.scopeType != realtime.ScopeProject || call.scopeID != tc.wantScopeID {
				t.Fatalf("scope call = (%q, %q), want (%q, %q)", call.scopeType, call.scopeID, realtime.ScopeProject, tc.wantScopeID)
			}

			var frame map[string]any
			if err := json.Unmarshal(call.msg, &frame); err != nil {
				t.Fatalf("decode project frame: %v", err)
			}
			if frame["type"] != tc.eventType {
				t.Fatalf("frame type = %v, want %v", frame["type"], tc.eventType)
			}
			if frame["actor_type"] != "member" || frame["actor_id"] != "user-1" {
				t.Fatalf("frame actor lost: %+v", frame)
			}
			assertNoLeakKeys(t, frame)
		})
	}
}

// TestRegisterListeners_ProjectResolutionFailureIsNotFatal pins fail-safe
// behavior: a resolver error must not panic the synchronous event bus and must
// not emit an unfiltered or misrouted project frame.
func TestRegisterListeners_ProjectResolutionFailureIsNotFatal(t *testing.T) {
	bus := events.New()
	fb := &fakeBroadcaster{}
	registerListeners(bus, fb, &fakeProjectResolver{err: errors.New("db down")})

	bus.Publish(events.Event{
		Type:        protocol.EventIssueDeleted,
		WorkspaceID: "ws-owner",
		Payload:     map[string]any{"issue_id": "issue-1"},
	})

	if len(fb.workspaceCalls) != 1 {
		t.Fatalf("workspace fanout = %d, want 1 (resolution failure must not drop it)", len(fb.workspaceCalls))
	}
	if len(fb.scopeCalls) != 0 {
		t.Fatalf("resolver failure emitted project fanout: %+v", fb.scopeCalls)
	}
}

func assertNoLeakKeys(t *testing.T, value any) {
	t.Helper()
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if projectScopedLeakKeys[key] {
				t.Fatalf("leak key %q survived into a project-scoped frame: %+v", key, value)
			}
			assertNoLeakKeys(t, item)
		}
	case []any:
		for _, item := range v {
			assertNoLeakKeys(t, item)
		}
	}
}
