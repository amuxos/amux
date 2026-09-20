// Package memory stores bounded, scope-isolated Markdown documents on the Hub.
// It does not own authentication: callers derive Scope from trusted execution
// identity before invoking the store. One Hub is the sole online writer.
package memory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	AbsentRevision   = "absent"
	MaxEntryBytes    = 2 << 10
	MaxEntryLines    = 64
	MaxDocumentBytes = 64 << 10
	MaxScopeBytes    = 16 << 20
	MaxScopeFiles    = 1024
	MaxOutputBytes   = 8 << 10
	MaxScanFiles     = 1024
	MaxScanBytes     = 8 << 20
	MaxScanDuration  = 250 * time.Millisecond
	maxPathBytes     = 512
	maxWalkEntries   = MaxScopeFiles * 8
)

var (
	ErrInvalid  = errors.New("invalid memory request")
	ErrConflict = errors.New("memory revision conflict")
	ErrLimit    = errors.New("memory limit exceeded")
	ErrUnsafe   = errors.New("unsafe memory path")
)

// Scope is deliberately explicit: its zero value is invalid, not global.
type Scope struct {
	Global      bool
	PrincipalID string
}

func (s Scope) directory() (string, error) {
	if s.Global && s.PrincipalID == "" {
		return "global", nil
	}
	if s.Global || strings.TrimSpace(s.PrincipalID) == "" || len(s.PrincipalID) > 512 || strings.ContainsRune(s.PrincipalID, 0) {
		return "", fmt.Errorf("%w: scope requires either global or a principal", ErrInvalid)
	}
	hash := sha256.Sum256([]byte(s.PrincipalID))
	return "users/" + hex.EncodeToString(hash[:]), nil
}

// ConflictError includes the observed revision so callers can re-read and merge.
type ConflictError struct{ CurrentRevision string }

func (e *ConflictError) Error() string {
	return ErrConflict.Error() + ": current revision " + e.CurrentRevision
}
func (e *ConflictError) Unwrap() error { return ErrConflict }

type Store struct {
	root  string
	locks sync.Map
	// Tests can reduce scan budgets without changing the production API.
	scanFiles int
	scanBytes int64
	scanTime  time.Duration
}

// New creates a private memory root. Reads do not create user scopes. root must
// not itself be a symbolic link.
func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("%w: empty store root", ErrInvalid)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: store root must be a directory", ErrUnsafe)
	}
	if err := os.Chmod(abs, 0700); err != nil {
		return nil, err
	}
	return &Store{root: abs, scanFiles: MaxScanFiles, scanBytes: MaxScanBytes, scanTime: MaxScanDuration}, nil
}

func (s *Store) scopeLock(key string) *sync.RWMutex {
	value, _ := s.locks.LoadOrStore(key, &sync.RWMutex{})
	return value.(*sync.RWMutex)
}

// openScope uses os.Root to confine all subsequent filesystem operations even
// when a path changes between validation and opening. We reject symlinks in
// addition to Root's escape checks; the private root has a single online writer.
func (s *Store) openScope(key string, create bool) (*os.Root, error) {
	info, err := os.Lstat(s.root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafe
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := checkPath(root, key, create, true); err != nil {
		return nil, err
	}
	return root.OpenRoot(key)
}

func validatePath(name string, prefix bool) (string, error) {
	if prefix {
		if name == "" {
			return ".", nil
		}
		name = strings.TrimSuffix(name, "/")
	}
	if name == "" || len(name) > maxPathBytes || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00\r\n") || path.IsAbs(name) {
		return "", fmt.Errorf("%w: expected a relative Markdown path", ErrInvalid)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.Contains(part, ":") || strings.HasPrefix(part, ".") {
			return "", fmt.Errorf("%w: invalid path component", ErrInvalid)
		}
	}
	if !prefix && !strings.HasSuffix(name, ".md") {
		return "", fmt.Errorf("%w: document path must end in .md", ErrInvalid)
	}
	return name, nil
}

// checkPath rejects all symlinks and non-directory parents. When create is
// true it creates missing directories only, never a document.
func checkPath(root *os.Root, name string, create, directory bool) error {
	if name == "." {
		return nil
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		part := strings.Join(parts[:i+1], "/")
		isDir := i < len(parts)-1 || directory
		info, err := root.Lstat(part)
		if errors.Is(err, fs.ErrNotExist) && create && isDir {
			if err := root.Mkdir(part, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			// Persist the directory entry as well as the eventual document;
			// syncing only the document's parent would not persist new ancestors.
			if err := syncDirectory(root, path.Dir(part)); err != nil {
				return err
			}
			info, err = root.Lstat(part)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (isDir && !info.IsDir()) || (!isDir && !info.Mode().IsRegular()) {
			return fmt.Errorf("%w: %s is not a regular %s", ErrUnsafe, part, map[bool]string{true: "directory", false: "file"}[isDir])
		}
	}
	return nil
}

func readDocument(root *os.Root, name string) ([]byte, string, error) {
	if err := checkPath(root, name, false, false); err != nil {
		return nil, "", err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxDocumentBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > MaxDocumentBytes {
		return nil, "", fmt.Errorf("%w: document exceeds %d bytes", ErrLimit, MaxDocumentBytes)
	}
	if !utf8.Valid(data) {
		return nil, "", fmt.Errorf("%w: document is not UTF-8", ErrInvalid)
	}
	if name == "MEMORY.md" && (len(data) > MaxEntryBytes || countLines(string(data)) > MaxEntryLines) {
		return nil, "", fmt.Errorf("%w: MEMORY.md exceeds its entry budget", ErrLimit)
	}
	hash := sha256.Sum256(data)
	return data, hex.EncodeToString(hash[:]), nil
}

type GetOptions struct {
	StartLine int // 1-based, zero defaults to the first line.
	EndLine   int // inclusive; zero reads until the output limit.
	Offset    int // byte offset for continuing a truncated result, at a UTF-8 boundary.
}

type Document struct {
	Path       string `json:"path"`
	Revision   string `json:"revision"`
	Content    string `json:"content"`
	Exists     bool   `json:"exists"`
	StartLine  int    `json:"start_line,omitempty"`
	EndLine    int    `json:"end_line,omitempty"`
	NextOffset int    `json:"next_offset,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

// Get returns a bounded UTF-8 fragment. An absent document has Exists=false and
// Revision=AbsentRevision, including a not-yet-created MEMORY.md entry.
func (s *Store) Get(ctx context.Context, scope Scope, name string, opts GetOptions) (Document, error) {
	name, err := validatePath(name, false)
	if err != nil {
		return Document{}, err
	}
	key, err := scope.directory()
	if err != nil {
		return Document{}, err
	}
	if opts.StartLine < 0 || opts.EndLine < 0 || opts.Offset < 0 || (opts.EndLine > 0 && opts.EndLine < opts.StartLine) || (opts.Offset > 0 && opts.StartLine > 0) {
		return Document{}, fmt.Errorf("%w: invalid read range", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Document{}, err
	}
	lock := s.scopeLock(key)
	lock.RLock()
	defer lock.RUnlock()
	doc := Document{Path: name, Revision: AbsentRevision}
	root, err := s.openScope(key, false)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return Document{}, err
	}
	defer root.Close()
	data, revision, err := readDocument(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return Document{}, err
	}
	start, end, err := readRange(data, opts)
	if err != nil {
		return Document{}, err
	}
	doc.Exists, doc.Revision = true, revision
	doc.StartLine = 1 + strings.Count(string(data[:start]), "\n")
	// Include JSON escaping in the output budget, rather than assuming content
	// bytes equal response bytes. Binary search keeps even a huge line bounded.
	available := end - start
	lo, hi := 0, available
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		doc.Content = string(data[start : start+mid])
		encoded, _ := json.Marshal(doc)
		if len(encoded) <= MaxOutputBytes-128 {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	stop := start + lo
	for stop > start && stop < len(data) && !utf8.RuneStart(data[stop]) {
		stop--
	}
	doc.Content = string(data[start:stop])
	doc.EndLine = doc.StartLine + strings.Count(doc.Content, "\n")
	if strings.HasSuffix(doc.Content, "\n") {
		doc.EndLine--
	}
	// Continuation applies to the requested range. Marking a complete line
	// range as truncated would make its next read repeat the same empty page.
	doc.Truncated = stop < end
	if doc.Truncated {
		doc.NextOffset = stop
	}
	return doc, nil
}

func readRange(data []byte, opts GetOptions) (int, int, error) {
	start, end := opts.Offset, len(data)
	if start > len(data) || (start < len(data) && !utf8.RuneStart(data[start])) {
		return 0, 0, fmt.Errorf("%w: offset must be a UTF-8 boundary within the document", ErrInvalid)
	}
	line := 1
	if opts.StartLine > 1 {
		start = len(data)
	}
	for i, b := range data {
		if b != '\n' {
			continue
		}
		if opts.EndLine == line {
			end = i + 1
			break
		}
		line++
		if opts.StartLine == line {
			start = i + 1
		}
	}
	if start > end {
		return 0, 0, fmt.Errorf("%w: offset is beyond the requested end line", ErrInvalid)
	}
	return start, end, nil
}

type WriteResult struct {
	Path          string `json:"path"`
	Revision      string `json:"revision"`
	AlreadyAbsent bool   `json:"already_absent,omitempty"`
}

func validateRevision(revision string) error {
	if revision == AbsentRevision {
		return nil
	}
	decoded, err := hex.DecodeString(revision)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(revision) != revision {
		return fmt.Errorf("%w: expected_revision must be absent or a SHA-256 revision", ErrInvalid)
	}
	return nil
}

func (s *Store) Upsert(ctx context.Context, scope Scope, name, content, expectedRevision string) (WriteResult, error) {
	name, err := validatePath(name, false)
	if err != nil {
		return WriteResult{}, err
	}
	key, err := scope.directory()
	if err != nil {
		return WriteResult{}, err
	}
	if err := validateRevision(expectedRevision); err != nil {
		return WriteResult{}, err
	}
	if !utf8.ValidString(content) {
		return WriteResult{}, fmt.Errorf("%w: content must be UTF-8", ErrInvalid)
	}
	if len(content) > MaxDocumentBytes || (name == "MEMORY.md" && (len(content) > MaxEntryBytes || countLines(content) > MaxEntryLines)) {
		return WriteResult{}, fmt.Errorf("%w: move detailed content into bounded topic documents", ErrLimit)
	}
	lock := s.scopeLock(key)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}
	root, err := s.openScope(key, true)
	if err != nil {
		return WriteResult{}, err
	}
	defer root.Close()
	old, revision, err := readDocument(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		revision, err = AbsentRevision, nil
	}
	if err != nil {
		return WriteResult{}, err
	}
	if revision != expectedRevision {
		return WriteResult{}, &ConflictError{CurrentRevision: revision}
	}
	files, size, err := scopeUsage(ctx, root)
	if err != nil {
		return WriteResult{}, err
	}
	if revision == AbsentRevision {
		files++
	}
	if files > MaxScopeFiles || size-int64(len(old))+int64(len(content)) > MaxScopeBytes {
		return WriteResult{}, fmt.Errorf("%w: scope storage quota", ErrLimit)
	}
	if err := checkPath(root, path.Dir(name), true, true); err != nil {
		return WriteResult{}, err
	}
	if err := atomicWrite(ctx, root, name, []byte(content)); err != nil {
		return WriteResult{}, err
	}
	hash := sha256.Sum256([]byte(content))
	return WriteResult{Path: name, Revision: hex.EncodeToString(hash[:])}, nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

func atomicWrite(ctx context.Context, root *os.Root, name string, data []byte) error {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := path.Join(path.Dir(name), ".memory-"+hex.EncodeToString(random[:])+".tmp")
	file, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	return syncDirectory(root, path.Dir(name))
}

func syncDirectory(root *os.Root, name string) error {
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Store) Forget(ctx context.Context, scope Scope, name, expectedRevision string) (WriteResult, error) {
	name, err := validatePath(name, false)
	if err != nil {
		return WriteResult{}, err
	}
	key, err := scope.directory()
	if err != nil {
		return WriteResult{}, err
	}
	if err := validateRevision(expectedRevision); err != nil {
		return WriteResult{}, err
	}
	lock := s.scopeLock(key)
	lock.Lock()
	defer lock.Unlock()
	if err := ctx.Err(); err != nil {
		return WriteResult{}, err
	}
	result := WriteResult{Path: name, Revision: AbsentRevision, AlreadyAbsent: true}
	root, err := s.openScope(key, false)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return WriteResult{}, err
	}
	defer root.Close()
	_, revision, err := readDocument(root, name)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return WriteResult{}, err
	}
	if revision != expectedRevision {
		return WriteResult{}, &ConflictError{CurrentRevision: revision}
	}
	if err := root.Remove(name); err != nil {
		return WriteResult{}, err
	}
	if err := syncDirectory(root, path.Dir(name)); err != nil {
		return WriteResult{}, err
	}
	result.AlreadyAbsent = false
	return result, nil
}

func scopeUsage(ctx context.Context, root *os.Root) (int, int64, error) {
	var files, entries int
	var size int64
	err := walkDocuments(root, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > maxWalkEntries {
			return fmt.Errorf("%w: too many scope directory entries", ErrLimit)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		// Stat through the Root instead of entry.Info(). Go 1.25.0 and 1.25.1
		// gave a child Root a relative name, so a rooted DirEntry resolved
		// against the process working directory: the scan failed outside that
		// directory and could measure a same-named file outside the root.
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files++
			size += info.Size()
		}
		return nil
	})
	return files, size, err
}

type SearchOptions struct {
	PathPrefix string
	Limit      int // defaults to 5, maximum 20 (still subject to output limits).
}

type SearchHit struct {
	Path      string `json:"path"`
	Revision  string `json:"revision"`
	Line      int    `json:"line"`
	Snippet   string `json:"snippet"`
	Truncated bool   `json:"truncated,omitempty"`
	score     int
	labelHits int
}

type SearchResult struct {
	Matches      []SearchHit `json:"matches"`
	Incomplete   bool        `json:"incomplete,omitempty"`
	Truncated    bool        `json:"truncated,omitempty"`
	ScannedFiles int         `json:"scanned_files"`
}

func (s *Store) Search(ctx context.Context, scope Scope, query string, opts SearchOptions) (SearchResult, error) {
	key, err := scope.directory()
	if err != nil {
		return SearchResult{}, err
	}
	prefix, err := validatePath(opts.PathPrefix, true)
	if err != nil {
		return SearchResult{}, err
	}
	terms := strings.Fields(strings.ToLower(query))
	if !utf8.ValidString(query) || len(query) > 512 || len(terms) == 0 || len(terms) > 16 || opts.Limit < 0 || opts.Limit > 20 {
		return SearchResult{}, fmt.Errorf("%w: query requires 1-16 literal words within 512 bytes and limit 1-20", ErrInvalid)
	}
	terms = uniqueTerms(terms)
	if opts.Limit == 0 {
		opts.Limit = 5
	}
	result := SearchResult{Matches: []SearchHit{}}
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	lock := s.scopeLock(key)
	lock.RLock()
	defer lock.RUnlock()
	root, err := s.openScope(key, false)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return SearchResult{}, err
	}
	defer root.Close()
	if err := checkPath(root, prefix, false, true); errors.Is(err, fs.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return SearchResult{}, err
	}
	deadline := time.Now().Add(s.scanTime)
	var scannedBytes int64
	var entries int
	err = walkDocuments(root, prefix, func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > maxWalkEntries || time.Now().After(deadline) {
			result.Incomplete = true
			return fs.SkipAll
		}
		if walkErr != nil {
			result.Incomplete = true
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") && name != "." {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".md") || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		if _, err := validatePath(name, false); err != nil {
			result.Incomplete = true
			return nil
		}
		// See scopeUsage: entry.Info() is not confined by the Root on Go 1.25.0
		// and 1.25.1, which would misreport the scan budget.
		info, err := root.Lstat(name)
		if err != nil {
			result.Incomplete = true
			return nil
		}
		if result.ScannedFiles >= s.scanFiles || scannedBytes+info.Size() > s.scanBytes {
			result.Incomplete = true
			return fs.SkipAll
		}
		result.ScannedFiles++
		scannedBytes += info.Size()
		data, revision, err := readDocument(root, name)
		if err != nil {
			result.Incomplete = true
			return nil
		}
		if hit, ok := matchDocument(name, revision, string(data), terms); ok {
			result.Matches = append(result.Matches, hit)
			sortHits(result.Matches)
			if len(result.Matches) > opts.Limit {
				result.Matches = result.Matches[:opts.Limit]
				result.Truncated = true
			}
		}
		return nil
	})
	if err != nil {
		return SearchResult{}, err
	}
	for {
		encoded, _ := json.Marshal(result)
		// Leave room for transport envelopes and explicit false-valued fields
		// added by the CLI/MCP view. The complete tool payload remains bounded.
		if len(encoded) <= MaxOutputBytes-1024 || len(result.Matches) == 0 {
			break
		}
		result.Matches = result.Matches[:len(result.Matches)-1]
		result.Truncated = true
	}
	return result, nil
}

// walkDocuments reads directory entries in small batches. fs.WalkDir reads and
// sorts an entire directory before its callback can enforce a scan budget; an
// oversized offline directory must not bypass the memory and time bounds here.
// Search ranking is deterministic independently of filesystem traversal order.
func walkDocuments(root *os.Root, name string, visit fs.WalkDirFunc) error {
	info, err := root.Lstat(name)
	if err != nil {
		return visit(name, nil, err)
	}
	var walk func(string, fs.DirEntry) error
	walk = func(name string, entry fs.DirEntry) error {
		if err := visit(name, entry, nil); err != nil {
			if err == fs.SkipDir && entry.IsDir() {
				return nil
			}
			return err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		dir, err := root.Open(name)
		if err != nil {
			return visit(name, entry, err)
		}
		defer dir.Close()
		for {
			entries, err := dir.ReadDir(32)
			if err != nil && err != io.EOF {
				return visit(name, entry, err)
			}
			for _, child := range entries {
				if err := walk(path.Join(name, child.Name()), child); err != nil {
					return err
				}
			}
			if err == io.EOF {
				return nil
			}
		}
	}
	err = walk(name, fs.FileInfoToDirEntry(info))
	if err == fs.SkipAll {
		return nil
	}
	return err
}

func uniqueTerms(terms []string) []string {
	result := make([]string, 0, len(terms))
	seen := make(map[string]bool, len(terms))
	for _, term := range terms {
		if !seen[term] {
			seen[term] = true
			result = append(result, term)
		}
	}
	return result
}

func sortHits(hits []SearchHit) {
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if hits[i].labelHits != hits[j].labelHits {
			return hits[i].labelHits > hits[j].labelHits
		}
		return hits[i].Path < hits[j].Path
	})
}

func matchDocument(name, revision, content string, terms []string) (SearchHit, bool) {
	lower := strings.ToLower(content)
	lowerPath := strings.ToLower(name)
	lines := strings.Split(content, "\n")
	title := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "# ") {
			title = strings.ToLower(line)
			break
		}
	}
	hit := SearchHit{Path: name, Revision: revision, Line: 1}
	for _, term := range terms {
		labelMatch := strings.Contains(lowerPath, term) || strings.Contains(title, term)
		if labelMatch || strings.Contains(lower, term) {
			hit.score++
		}
		if labelMatch {
			hit.labelHits++
		}
	}
	if hit.score == 0 {
		return SearchHit{}, false
	}
	snippet := lines[0]
	for i, line := range lines {
		lowerLine := strings.ToLower(line)
		for _, term := range terms {
			if at := strings.Index(lowerLine, term); at >= 0 {
				hit.Line = i + 1
				// Byte offsets may change during Unicode case mapping; choose
				// a safe rune boundary in the original text for the excerpt.
				start := max(0, min(at, len(line))-128)
				for start > 0 && !utf8.RuneStart(line[start]) {
					start--
				}
				snippet = line[start:]
				hit.Truncated = start > 0
				goto found
			}
		}
	}
found:
	const snippetBytes = 512
	if len(snippet) > snippetBytes {
		end := snippetBytes
		for end > 0 && !utf8.RuneStart(snippet[end]) {
			end--
		}
		snippet = snippet[:end]
		hit.Truncated = true
	}
	hit.Snippet = snippet
	return hit, true
}
