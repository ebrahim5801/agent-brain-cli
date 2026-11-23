package store

import "testing"

func TestWithinDir(t *testing.T) {
	for _, tc := range []struct {
		dir, path string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "/a/b/repo", true},
		{"/a/b", "/a/b/repo/sub", true},
		{"/a/b", "/a/bc", false},
		{"/a/b", "/a", false},
		{"/a/b", "/x/b", false},
		{"/a/b", "/a/b/..data", true},
	} {
		if got := withinDir(tc.dir, tc.path); got != tc.want {
			t.Errorf("withinDir(%q, %q) = %v, want %v", tc.dir, tc.path, got, tc.want)
		}
	}
}
