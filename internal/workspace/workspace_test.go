package workspace

import (
	"path/filepath"
	"testing"
)

func TestDefaultDirUsesSourceScopeAndSanitizedSessionName(t *testing.T) {
	base := t.TempDir()

	got := DefaultDir(base, "feishu", "oc_xxx/yyy", "review bug #1")
	want := filepath.Join(base, "workspaces", "feishu", "oc_xxx_yyy", "review_bug__1")
	if got != want {
		t.Fatalf("DefaultDir = %q, want %q", got, want)
	}
}

func TestDefaultDirFallsBackForEmptyParts(t *testing.T) {
	base := t.TempDir()

	got := DefaultDir(base, "", "", "")
	want := filepath.Join(base, "workspaces", "default", "default", "default")
	if got != want {
		t.Fatalf("DefaultDir = %q, want %q", got, want)
	}
}
