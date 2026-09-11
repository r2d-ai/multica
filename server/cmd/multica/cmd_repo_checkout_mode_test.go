package main

import "testing"

func TestEffectiveRepoCheckoutMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		goos       string
		configured string
		want       string
	}{
		{name: "Linux drops historical isolated mode", goos: "linux", configured: "isolated", want: ""},
		{name: "Windows keeps isolated mode", goos: "windows", configured: "isolated", want: "isolated"},
		{name: "macOS keeps configured mode", goos: "darwin", configured: "isolated", want: "isolated"},
		{name: "Linux leaves default worktree mode alone", goos: "linux", configured: "", want: ""},
		{name: "mode is trimmed", goos: "windows", configured: " isolated ", want: "isolated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveRepoCheckoutMode(tt.goos, tt.configured); got != tt.want {
				t.Fatalf("effectiveRepoCheckoutMode(%q, %q) = %q, want %q", tt.goos, tt.configured, got, tt.want)
			}
		})
	}
}
