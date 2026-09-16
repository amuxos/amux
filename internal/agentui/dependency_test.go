package agentui

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionCodeDoesNotImportRuntimeOrSurfacePackages(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package files: %v", err)
	}
	disallowed := []string{
		"github.com/amuxos/amux/internal/acp",
		"github.com/amuxos/amux/internal/adapter",
		"github.com/amuxos/amux/internal/bridge",
		"github.com/amuxos/amux/internal/gateway",
		"github.com/amuxos/amux/internal/hubweb",
		"github.com/amuxos/amux/internal/provider",
		"github.com/amuxos/amux/internal/session",
		"github.com/amuxos/amux/internal/sourceframe",
		"github.com/amuxos/amux/internal/task",
		"github.com/amuxos/amux/pkg/acptui",
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse imports for %s: %v", file, err)
		}
		for _, imp := range parsed.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s in %s: %v", imp.Path.Value, file, err)
			}
			for _, blocked := range disallowed {
				if path == blocked || strings.HasPrefix(path, blocked+"/") {
					t.Fatalf("%s imports %s; agentui should only depend on AttachEvent and stdlib helpers", file, path)
				}
			}
		}
	}
}
