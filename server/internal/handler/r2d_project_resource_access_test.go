package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/r2dauth"
)

func TestProjectResourceProjectID(t *testing.T) {
	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{"/api/projects/p1/resources", "p1", true},
		{"/api/projects/p1/resources/r1", "p1", true},
		{"/api/projects/p1/resources/", "p1", true},
		{"/api/projects/p1", "", false},
		{"/api/projects/p1/sharing", "", false},
		{"/api/issues/i1", "", false},
	}
	for _, tt := range tests {
		got, ok := projectResourceProjectID(tt.path)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("projectResourceProjectID(%q) = (%q, %v), want (%q, %v)", tt.path, got, ok, tt.want, tt.ok)
		}
	}
}

func TestProjectResourceAccessStatus(t *testing.T) {
	tests := []struct {
		name   string
		caps   r2dauth.ProjectCapabilities
		method string
		want   int
	}{
		{
			name:   "no project read is undisclosed",
			caps:   r2dauth.ProjectCapabilities{},
			method: http.MethodGet,
			want:   http.StatusNotFound,
		},
		{
			name:   "foreign viewer cannot inspect owner resources",
			caps:   r2dauth.ProjectCapabilities{Read: true},
			method: http.MethodGet,
			want:   http.StatusForbidden,
		},
		{
			name:   "foreign manager cannot inspect owner resources",
			caps:   r2dauth.ProjectCapabilities{Read: true, Manage: true},
			method: http.MethodGet,
			want:   http.StatusForbidden,
		},
		{
			name:   "owner workspace member can read resources",
			caps:   r2dauth.ProjectCapabilities{Read: true, ViewResources: true},
			method: http.MethodGet,
			want:   0,
		},
		{
			name:   "owner workspace member cannot mutate resources",
			caps:   r2dauth.ProjectCapabilities{Read: true, ViewResources: true},
			method: http.MethodPost,
			want:   http.StatusForbidden,
		},
		{
			name:   "owner workspace manager can mutate resources",
			caps:   r2dauth.ProjectCapabilities{Read: true, ViewResources: true, Manage: true},
			method: http.MethodPatch,
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectResourceAccessStatus(tt.caps, tt.method); got != tt.want {
				t.Fatalf("projectResourceAccessStatus() = %d, want %d", got, tt.want)
			}
		})
	}
}
