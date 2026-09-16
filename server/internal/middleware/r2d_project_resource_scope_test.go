package middleware

import "testing"

func TestR2DProjectResourceRequest(t *testing.T) {
	t.Parallel()
	projectID := "11111111-1111-1111-1111-111111111111"
	tests := []struct {
		path string
		want bool
	}{
		{"/api/projects/" + projectID + "/resources", true},
		{"/api/projects/" + projectID + "/resources/", true},
		{"/api/projects/" + projectID + "/resources/22222222-2222-2222-2222-222222222222", true},
		{"/api/projects/" + projectID, false},
		{"/api/projects/" + projectID + "/sharing", false},
		{"/api/projects/aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa/resources", false},
	}
	for _, tt := range tests {
		if got := r2dProjectResourceRequest(tt.path, projectID); got != tt.want {
			t.Errorf("r2dProjectResourceRequest(%q)=%v want %v", tt.path, got, tt.want)
		}
	}
}
