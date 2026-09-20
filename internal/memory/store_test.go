package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func put(t *testing.T, s *Store, scope Scope, name, content string) WriteResult {
	t.Helper()
	r, err := s.Upsert(context.Background(), scope, name, content, AbsentRevision)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestScopeIsolationAndPersistence(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	global, alice, bob := Scope{Global: true}, Scope{PrincipalID: "alice/../☃"}, Scope{PrincipalID: "bob"}
	for _, test := range []struct {
		scope   Scope
		content string
	}{{global, "global preference"}, {alice, "alice preference"}, {bob, "bob preference"}} {
		before, err := s.Get(ctx, test.scope, "MEMORY.md", GetOptions{})
		if err != nil || before.Exists || before.Revision != AbsentRevision {
			t.Fatalf("absent: %+v, %v", before, err)
		}
		put(t, s, test.scope, "MEMORY.md", test.content)
	}
	// Reopening demonstrates plain files are authoritative without an index.
	reopened, err := New(s.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		scope   Scope
		content string
	}{{global, "global preference"}, {alice, "alice preference"}, {bob, "bob preference"}} {
		doc, err := reopened.Get(ctx, test.scope, "MEMORY.md", GetOptions{})
		if err != nil || doc.Content != test.content || len(doc.Revision) != 64 {
			t.Fatalf("scope get: %+v, %v", doc, err)
		}
		found, err := reopened.Search(ctx, test.scope, "preference", SearchOptions{})
		if err != nil || len(found.Matches) != 1 || found.Matches[0].Snippet != test.content {
			t.Fatalf("scope search: %+v, %v", found, err)
		}
	}
	for _, scope := range []Scope{{}, {Global: true, PrincipalID: "alice"}, {PrincipalID: " "}} {
		if _, err := s.Get(ctx, scope, "MEMORY.md", GetOptions{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid scope accepted: %+v, %v", scope, err)
		}
	}
}

func TestConcurrentCompareAndSwap(t *testing.T) {
	s := testStore(t)
	scope := Scope{Global: true}
	written := put(t, s, scope, "topic.md", "original")
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Upsert(context.Background(), scope, "topic.md", "updated", written.Revision)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		var conflict *ConflictError
		if !errors.As(err, &conflict) || !errors.Is(err, ErrConflict) || len(conflict.CurrentRevision) != 64 {
			t.Fatalf("unexpected update error: %v", err)
		}
		conflicts++
	}
	if successes != 1 || conflicts != 15 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	if _, err := s.Upsert(context.Background(), scope, "topic.md", "blind overwrite", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unconditional write accepted: %v", err)
	}
	if _, err := s.Upsert(context.Background(), scope, "topic.md", "blind overwrite", AbsentRevision); !errors.Is(err, ErrConflict) {
		t.Fatalf("create overwrote existing document: %v", err)
	}
}

func TestForgetChecksRevisionAndIsIdempotent(t *testing.T) {
	s := testStore(t)
	scope := Scope{Global: true}
	written := put(t, s, scope, "topic.md", "known")
	if _, err := s.Forget(context.Background(), scope, "topic.md", AbsentRevision); !errors.Is(err, ErrConflict) {
		t.Fatalf("forget ignored revision: %v", err)
	}
	for i := range 2 {
		result, err := s.Forget(context.Background(), scope, "topic.md", written.Revision)
		if err != nil || result.AlreadyAbsent != (i == 1) || result.Revision != AbsentRevision {
			t.Fatalf("forget %d: %+v, %v", i, result, err)
		}
	}
}

func TestUnsafePathsAndSymlinks(t *testing.T) {
	s := testStore(t)
	ctx, scope := context.Background(), Scope{Global: true}
	for _, name := range []string{"", "/etc/secret.md", "../secret.md", "a/../secret.md", "a//b.md", "a\\b.md", "a/.hidden.md", "a/./b.md", "file.txt", "C:/secret.md", "a\x00.md", ".memory-temp.md", strings.Repeat("a", maxPathBytes) + ".md"} {
		if _, err := s.Upsert(ctx, scope, name, "secret", AbsentRevision); !errors.Is(err, ErrInvalid) {
			t.Errorf("unsafe write path %q: %v", name, err)
		}
		if _, err := s.Get(ctx, scope, name, GetOptions{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("unsafe read path %q: %v", name, err)
		}
	}
	put(t, s, scope, "safe.md", "public")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("private token"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, link := range []struct{ target, name string }{
		{outside, "linked"}, {filepath.Join(outside, "secret.md"), "linked.md"}, {"safe.md", "internal.md"},
	} {
		if err := os.Symlink(link.target, filepath.Join(s.root, "global", link.name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"linked/secret.md", "linked.md", "internal.md"} {
		if _, err := s.Get(ctx, scope, name, GetOptions{}); !errors.Is(err, ErrUnsafe) {
			t.Errorf("symlink get %q: %v", name, err)
		}
		if _, err := s.Upsert(ctx, scope, name, "overwrite", AbsentRevision); !errors.Is(err, ErrUnsafe) {
			t.Errorf("symlink upsert %q: %v", name, err)
		}
		if _, err := s.Forget(ctx, scope, name, AbsentRevision); !errors.Is(err, ErrUnsafe) {
			t.Errorf("symlink forget %q: %v", name, err)
		}
	}
	for _, prefix := range []string{"../", "linked/", "/"} {
		if _, err := s.Search(ctx, scope, "private", SearchOptions{PathPrefix: prefix}); err == nil {
			t.Errorf("unsafe search prefix %q", prefix)
		}
	}
	result, err := s.Search(ctx, scope, "private", SearchOptions{})
	if err != nil || len(result.Matches) != 0 {
		t.Fatalf("search followed symlink: %+v, %v", result, err)
	}
	if data, err := os.ReadFile(filepath.Join(outside, "secret.md")); err != nil || string(data) != "private token" {
		t.Fatalf("outside document changed: %q, %v", data, err)
	}
	// A symlink at a scope boundary must not alias another principal's scope.
	if err := os.Symlink("global", filepath.Join(s.root, "users")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, Scope{PrincipalID: "alice"}, "safe.md", GetOptions{}); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("scope symlink accepted: %v", err)
	}
}

func TestEntryAndDocumentLimits(t *testing.T) {
	s := testStore(t)
	scope := Scope{Global: true}
	for _, test := range []struct{ path, content string }{
		{"MEMORY.md", strings.Repeat("a", MaxEntryBytes+1)},
		{"MEMORY.md", strings.Repeat("\n", MaxEntryLines+1)},
		{"topic.md", strings.Repeat("a", MaxDocumentBytes+1)},
	} {
		if _, err := s.Upsert(context.Background(), scope, test.path, test.content, AbsentRevision); !errors.Is(err, ErrLimit) {
			t.Fatalf("limit not enforced: %q, %v", test.path, err)
		}
	}
	put(t, s, scope, "MEMORY.md", strings.Repeat("a", MaxEntryBytes))
	put(t, s, scope, "topic.md", strings.Repeat("a", MaxDocumentBytes))
	if _, err := s.Upsert(context.Background(), scope, "bad.md", string([]byte{0xff}), AbsentRevision); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 accepted: %v", err)
	}
}

func TestBoundedReadAndUTF8Continuation(t *testing.T) {
	s := testStore(t)
	scope, ctx := Scope{Global: true}, context.Background()
	// JSON control-character escaping is deliberately more expensive than its
	// raw byte length; a single long line must still be readable to completion.
	content := strings.Repeat("中☃\\\t", 5000)
	put(t, s, scope, "long.md", content)
	var got strings.Builder
	var offset int
	for attempts := 0; attempts < 100; attempts++ {
		doc, err := s.Get(ctx, scope, "long.md", GetOptions{Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(doc)
		if err != nil || len(encoded) > MaxOutputBytes || !utf8.ValidString(doc.Content) {
			t.Fatalf("unbounded or invalid result: %d bytes, %v", len(encoded), err)
		}
		got.WriteString(doc.Content)
		if !doc.Truncated {
			break
		}
		if doc.NextOffset <= offset {
			t.Fatalf("continuation did not advance: %+v", doc)
		}
		offset = doc.NextOffset
	}
	if got.String() != content {
		t.Fatalf("continuation lost content: got %d bytes, want %d", got.Len(), len(content))
	}
	if _, err := s.Get(ctx, scope, "long.md", GetOptions{Offset: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("offset splitting rune accepted: %v", err)
	}
	put(t, s, scope, "lines.md", "one\ntwo\nthree\nfour")
	doc, err := s.Get(ctx, scope, "lines.md", GetOptions{StartLine: 2, EndLine: 3})
	if err != nil || doc.Content != "two\nthree\n" || doc.StartLine != 2 || doc.EndLine != 3 || doc.Truncated || doc.NextOffset != 0 {
		t.Fatalf("line range: %+v, %v", doc, err)
	}
	put(t, s, scope, "range.md", content+"\nnot requested\n")
	got.Reset()
	offset = 0
	for attempts := 0; attempts < 100; attempts++ {
		doc, err := s.Get(ctx, scope, "range.md", GetOptions{Offset: offset, EndLine: 1})
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(doc.Content)
		if !doc.Truncated {
			break
		}
		if doc.NextOffset <= offset {
			t.Fatalf("bounded range continuation did not advance: %+v", doc)
		}
		offset = doc.NextOffset
	}
	if got.String() != content+"\n" {
		t.Fatalf("continued range did not stop at requested line: got %d bytes", got.Len())
	}
}

func TestLiteralSearchRankingAndBounds(t *testing.T) {
	s := testStore(t)
	ctx, scope := context.Background(), Scope{Global: true}
	for _, test := range []struct{ name, content string }{
		{"a.md", "# body\nRELEASE 检查 body"},
		{"release.md", "# title\n检查 body"},
		{"b.md", "# Release 检查\nbody"},
		{"one.md", "release only"},
		{"projects/amux.md", "literal a.*b is not a regex"},
		{"projects/other.md", "literal axxxb"},
	} {
		put(t, s, scope, test.name, test.content)
	}
	result, err := s.Search(ctx, scope, "release 检查 release", SearchOptions{})
	if err != nil || len(result.Matches) != 4 {
		t.Fatalf("search: %+v, %v", result, err)
	}
	for i, expected := range []string{"b.md", "release.md", "a.md", "one.md"} {
		if result.Matches[i].Path != expected {
			t.Errorf("rank %d: got %s want %s", i, result.Matches[i].Path, expected)
		}
	}
	result, err = s.Search(ctx, scope, "a.*b", SearchOptions{PathPrefix: "projects/"})
	if err != nil || len(result.Matches) != 1 || result.Matches[0].Path != "projects/amux.md" {
		t.Fatalf("literal/prefix search: %+v, %v", result, err)
	}
	result, err = s.Search(ctx, scope, "release", SearchOptions{Limit: 1})
	if err != nil || len(result.Matches) != 1 || !result.Truncated || result.Incomplete {
		t.Fatalf("limit search: %+v, %v", result, err)
	}
	for _, query := range []string{"", " \t\n", strings.Repeat("x", 513), strings.Repeat("x ", 17)} {
		if _, err := s.Search(ctx, scope, query, SearchOptions{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid query accepted: %q, %v", query, err)
		}
	}
	// Long Unicode/control-character snippets remain valid and the aggregate
	// response includes escaped JSON overhead in its hard size bound.
	for i := range 20 {
		name := strings.Repeat("a", 180) + string(rune('a'+i)) + ".md"
		put(t, s, scope, name, strings.Repeat("中\t", 3000)+"needle"+strings.Repeat("\t", 2000))
	}
	result, err = s.Search(ctx, scope, "needle", SearchOptions{Limit: 20})
	if err != nil || len(result.Matches) == 0 || !result.Truncated {
		t.Fatalf("bounded search: %+v, %v", result, err)
	}
	encoded, _ := json.Marshal(result)
	if len(encoded) > MaxOutputBytes {
		t.Fatalf("search output is %d bytes", len(encoded))
	}
	for _, hit := range result.Matches {
		if !utf8.ValidString(hit.Snippet) || !strings.Contains(hit.Snippet, "needle") || !hit.Truncated {
			t.Fatalf("bad snippet: %+v", hit)
		}
	}
}

func TestSearchScanBudgetsAndCancellation(t *testing.T) {
	for _, budget := range []string{"files", "bytes", "time"} {
		t.Run(budget, func(t *testing.T) {
			s := testStore(t)
			scope := Scope{Global: true}
			put(t, s, scope, "a.md", "known")
			put(t, s, scope, "b.md", "needle")
			switch budget {
			case "files":
				s.scanFiles = 1
			case "bytes":
				s.scanBytes = 5
			case "time":
				s.scanTime = -time.Second
			}
			result, err := s.Search(context.Background(), scope, "needle", SearchOptions{})
			if err != nil || !result.Incomplete || result.ScannedFiles > 1 {
				t.Fatalf("budget masked incomplete scan: %+v, %v", result, err)
			}
		})
	}
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(ctx, Scope{Global: true}, "needle", SearchOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestAtomicWriteFailurePreservesOriginal(t *testing.T) {
	s := testStore(t)
	scope := Scope{Global: true}
	put(t, s, scope, "topic.md", "original")
	root, err := os.OpenRoot(filepath.Join(s.root, "global"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := atomicWrite(ctx, root, "topic.md", []byte("replacement")); !errors.Is(err, context.Canceled) {
		t.Fatalf("atomic canceled write: %v", err)
	}
	if err := root.Mkdir("directory.md", 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(context.Background(), root, "directory.md", []byte("replacement")); err == nil {
		t.Fatal("expected rename over directory to fail")
	}
	data, err := root.ReadFile("topic.md")
	if err != nil || string(data) != "original" {
		t.Fatalf("original was damaged: %q, %v", data, err)
	}
	if err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(entry.Name(), ".memory-") {
			t.Errorf("temporary file leaked: %s", name)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestScopeQuotaAndPermissions(t *testing.T) {
	s := testStore(t)
	scope := Scope{Global: true}
	put(t, s, scope, "topic.md", "original")
	// Seed offline files to reach the byte quota without many fsync operations.
	for i := 0; i < MaxScopeBytes/MaxDocumentBytes; i++ {
		name := filepath.Join(s.root, "global", "quota-"+string(rune(0x4e00+i))+".md")
		if err := os.WriteFile(name, []byte(strings.Repeat("a", MaxDocumentBytes)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Upsert(context.Background(), scope, "new.md", "new", AbsentRevision); !errors.Is(err, ErrLimit) {
		t.Fatalf("scope quota not enforced: %v", err)
	}
	if info, err := os.Stat(filepath.Join(s.root, "global", "topic.md")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("document permissions: %v, %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(s.root, "global")); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("scope permissions: %v, %v", info, err)
	}
}

func TestConcurrentWritesShareFileQuota(t *testing.T) {
	s := testStore(t)
	scope := Scope{Global: true}
	put(t, s, scope, "original.md", "original")
	for i := 1; i < MaxScopeFiles; i++ {
		name := filepath.Join(s.root, "global", "quota-"+string(rune(0x4e00+i))+".md")
		if err := os.WriteFile(name, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(s.root, "global", "original.md")); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"a.md", "b.md"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Upsert(context.Background(), scope, name, "new", AbsentRevision)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var successes, limits int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrLimit) {
			limits++
		} else {
			t.Fatalf("unexpected write error: %v", err)
		}
	}
	if successes != 1 || limits != 1 {
		t.Fatalf("concurrent quota: successes=%d limits=%d", successes, limits)
	}
}

// TestScanIsConfinedToStoreRoot pins the scope scan to the store root. Go 1.25.0
// and 1.25.1 named a child Root relatively, so a rooted DirEntry resolved against
// the process working directory and a same-named path outside the root could be
// measured instead of the document. The scan budget makes that observable
// without touching the real quota.
func TestScanIsConfinedToStoreRoot(t *testing.T) {
	decoyDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(decoyDir, "global"), 0700); err != nil {
		t.Fatal(err)
	}
	// Larger than the scan budget below, so an unconfined scan skips the document.
	if err := os.WriteFile(filepath.Join(decoyDir, "global", "topic.md"), []byte(strings.Repeat("d", 4096)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(decoyDir)

	s := testStore(t)
	s.scanBytes = 64
	scope := Scope{Global: true}
	put(t, s, scope, "topic.md", "original")

	found, err := s.Search(context.Background(), scope, "original", SearchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if found.Incomplete || len(found.Matches) != 1 || found.Matches[0].Path != "topic.md" {
		t.Fatalf("scan escaped the store root: %+v", found)
	}
}
