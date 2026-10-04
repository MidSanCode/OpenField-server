package handler

import (
	"os"
	"path/filepath"
	"testing"
)

// The cleanup that removes a superseded bundle must never be able to unlink a
// file outside the plugin data directory, whatever the file_path column holds.
func TestWithinDataDir(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"inside", filepath.Join(dir, "demo-1.0.0.zip"), true},
		{"nested", filepath.Join(dir, "sub", "demo-1.0.0.zip"), true},
		{"outside", filepath.Join(os.TempDir(), "elsewhere.zip"), false},
		{"traversal", filepath.Join(dir, "..", "escaped.zip"), false},
		{"parent itself", dir, true}, // the dir is not "outside" itself
	}
	for _, c := range cases {
		if got := withinDataDir(dir, c.path); got != c.want {
			t.Errorf("%s: withinDataDir(%q, %q) = %v, want %v", c.name, dir, c.path, got, c.want)
		}
	}
}
