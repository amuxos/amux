package isolation

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestIsolationDerivesEngineEnvFromAdapterMatrix guards against re-introducing
// hardcoded engine names or engine-specific HOME env vars inside
// internal/isolation. Engines must declare their isolated HOME env/dir bindings
// via adapter.ExecutionCapabilities instead.
func TestIsolationDerivesEngineEnvFromAdapterMatrix(t *testing.T) {
	forbidden := map[string]bool{
		"CODEX_HOME":          true,
		"CLAUDE_CONFIG_DIR":   true,
		"PI_CODING_AGENT_DIR": true,
		".codex":              true,
		".pi/agent":           true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if forbidden[val] {
				pos := fset.Position(lit.Pos())
				t.Errorf("%s:%d hardcodes engine isolated-HOME literal %q; declare it in adapter.ExecutionCapabilities.IsolatedHomeEnv instead", name, pos.Line, val)
			}
			return true
		})
	}
}
