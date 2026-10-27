package isolation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func readHomeFilesWorkerForTest(t *testing.T, home string, rels []string) (map[string][]byte, error) {
	t.Helper()
	var out bytes.Buffer
	if err := ReadHomeFilesWorker(home, rels, &out); err != nil {
		return nil, err
	}
	var payload homeFilesPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload.Files, nil
}

func TestReadHomeFilesWorkerReadsExistingAndSkipsMissing(t *testing.T) {
	home := t.TempDir()
	rel := ".local/share/token.json"
	if err := os.MkdirAll(filepath.Join(home, filepath.FromSlash(filepath.Dir(rel))), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, filepath.FromSlash(rel)), []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, err := readHomeFilesWorkerForTest(t, home, []string{rel, ".local/share/missing.json"})
	if err != nil {
		t.Fatal(err)
	}
	if string(files[rel]) != "token" {
		t.Fatalf("files = %v", files)
	}
	if _, ok := files[".local/share/missing.json"]; ok {
		t.Fatal("missing file was reported")
	}
}

func TestReadHomeFilesWorkerRejectsInvalidPaths(t *testing.T) {
	home := t.TempDir()
	for _, rel := range []string{"../escape", "a/../../b", "/abs", "", ".."} {
		if _, err := readHomeFilesWorkerForTest(t, home, []string{rel}); err == nil {
			t.Fatalf("rel %q: expected rejection", rel)
		}
	}
}

func TestReadHomeFilesWorkerBoundsInputs(t *testing.T) {
	home := t.TempDir()
	if _, err := readHomeFilesWorkerForTest(t, home, nil); err == nil {
		t.Fatal("expected empty rels rejection")
	}
	tooMany := make([]string, MaxHomeReadFiles+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("f%d.json", i)
	}
	if _, err := readHomeFilesWorkerForTest(t, home, tooMany); err == nil {
		t.Fatal("expected too-many rejection")
	}
	if err := os.WriteFile(filepath.Join(home, "big.json"), bytes.Repeat([]byte("x"), MaxHomeReadFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readHomeFilesWorkerForTest(t, home, []string{"big.json"}); err == nil {
		t.Fatal("expected size cap rejection")
	}
}

func TestReadHomeFilesWorkerRejectsNonRegularFile(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "adir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readHomeFilesWorkerForTest(t, home, []string{"adir"}); err == nil {
		t.Fatal("expected directory rejection")
	}
}

func TestReadHomeFilesWorkerConfinesSymlinks(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "escape.json")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := readHomeFilesWorkerForTest(t, home, []string{"escape.json"}); err == nil {
		t.Fatal("a symlink escaping HOME was followed")
	}
}
