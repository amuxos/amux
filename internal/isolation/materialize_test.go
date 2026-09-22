package isolation

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeWorkspaceCopiesRegularTree(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "file"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeWorkspace(source, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "nested", "file"))
	if err != nil || string(data) != "ok" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestMaterializeWorkspaceRejectsSymlink(t *testing.T) {
	source, destination := t.TempDir(), t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeWorkspace(source, destination); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestEmptyBundlePreparesCodexHomeAndPreservesExistingFiles(t *testing.T) {
	home := t.TempDir()
	apply := func() error {
		return ApplyMaterializeBundle(bytes.NewReader(nil), t.TempDir(), t.TempDir(), home)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".codex")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("private Codex directory: %v %v", info, err)
	}
	file := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(file, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "existing" {
		t.Fatalf("existing file changed: %q %v", data, err)
	}
	linkedHome := t.TempDir()
	if err := os.Symlink(dir, filepath.Join(linkedHome, ".codex")); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMaterializeBundle(bytes.NewReader(nil), t.TempDir(), t.TempDir(), linkedHome); err == nil {
		t.Fatal("symlink CODEX_HOME accepted")
	}
}

func TestMaterializeBundleRoundTripAndSecretMode(t *testing.T) {
	sourceRoot := t.TempDir()
	workspaceSource := filepath.Join(sourceRoot, "workspace")
	secretSource := filepath.Join(sourceRoot, "secret")
	if err := os.MkdirAll(filepath.Join(workspaceSource, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(secretSource, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspaceSource, "nested", "file"), []byte("workspace"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretSource, "token"), []byte("canary"), 0o644); err != nil {
		t.Fatal(err)
	}
	var bundle bytes.Buffer
	if err := WriteMaterializeBundle(&bundle, []BundleSource{
		{Source: workspaceSource, Prefix: "workspace"},
		{Source: secretSource, Prefix: "secret"},
	}); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	workspace := filepath.Join(destination, "workspace")
	run := filepath.Join(destination, "run")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMaterializeBundle(bytes.NewReader(bundle.Bytes()), workspace, run, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "nested", "file")); err != nil || string(data) != "workspace" {
		t.Fatalf("workspace data=%q err=%v", data, err)
	}
	secret := filepath.Join(run, "secret", "token")
	if data, err := os.ReadFile(secret); err != nil || string(data) != "canary" {
		t.Fatalf("secret data=%q err=%v", data, err)
	}
	info, err := os.Stat(secret)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode=%v, want 0600", info.Mode().Perm())
	}
}

func TestApplyMaterializeBundleRejectsTraversalAndSymlinkTarget(t *testing.T) {
	var bundle bytes.Buffer
	tw := tar.NewWriter(&bundle)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Size: 1, Mode: 0o600}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	run := filepath.Join(root, "run")
	_ = os.Mkdir(workspace, 0o700)
	_ = os.Mkdir(run, 0o700)
	if err := ApplyMaterializeBundle(bytes.NewReader(bundle.Bytes()), workspace, run, t.TempDir()); err == nil {
		t.Fatal("traversal bundle was accepted")
	}

	var valid bytes.Buffer
	if err := WriteMaterializeBundle(&valid, []BundleSource{{Source: t.TempDir(), Prefix: "secret"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(run, "secret")); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMaterializeBundle(bytes.NewReader(valid.Bytes()), workspace, run, t.TempDir()); err == nil {
		t.Fatal("symlink destination was accepted")
	}
}

// An isolated principal starts with an empty HOME, so a provider whose cloud
// credential lives in the user's HOME has nothing to authenticate with. The
// bundle carries those files under their own logical prefix.
func TestMaterializeBundleWritesHomePrefix(t *testing.T) {
	sourceRoot := t.TempDir()
	homeSource := filepath.Join(sourceRoot, "home")
	if err := os.MkdirAll(filepath.Join(homeSource, ".trae", "cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homeSource, ".trae", "cli", "auth.json"), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	var bundle bytes.Buffer
	if err := WriteMaterializeBundle(&bundle, []BundleSource{{Source: homeSource, Prefix: "home"}}); err != nil {
		t.Fatal(err)
	}

	workspace, run, home := t.TempDir(), t.TempDir(), t.TempDir()
	if err := ApplyMaterializeBundle(bytes.NewReader(bundle.Bytes()), workspace, run, home); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".trae", "cli", "auth.json")
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "token" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("home credential mode = %04o, want owner-only", info.Mode().Perm())
	}
	if entries, err := os.ReadDir(run); err != nil || len(entries) != 0 {
		t.Fatalf("home entries leaked into the run root: %v (err=%v)", entries, err)
	}
}

func TestApplyMaterializeBundleRejectsHomeEscape(t *testing.T) {
	var bundle bytes.Buffer
	tw := tar.NewWriter(&bundle)
	if err := tw.WriteHeader(&tar.Header{Name: "home/../escape", Typeflag: tar.TypeReg, Mode: 0o600, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	err := ApplyMaterializeBundle(bytes.NewReader(bundle.Bytes()), t.TempDir(), t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("expected the escaping home entry to be rejected")
	}
}
