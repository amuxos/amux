package bridge

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionCodeRespectsDecoupledBoundaries(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package files: %v", err)
	}
	disallowedExact := map[string]string{
		"github.com/amuxos/amux/internal/coordination": "use internal/coordination/feishuexchange port instead of importing coordination core directly",
	}
	disallowedPrefix := []string{
		"github.com/amuxos/amux/app/",
		"github.com/amuxos/amux/internal/acp",
		"github.com/amuxos/amux/internal/memory",
		"github.com/amuxos/amux/internal/provider",
		"github.com/amuxos/amux/internal/registry",
		"github.com/amuxos/amux/internal/task",
	}
	disallowedDirectConfigCalls := map[string]bool{
		"ReadExperimentsSettings":         true,
		"UpdateExperimentsSettings":       true,
		"ReadDailyCleanupSettings":        true,
		"ReadDailyCleanupSettingsContext": true,
		"SaveDailyCleanupEnabled":         true,
		"SaveDailyCleanupEnabledContext":  true,
	}

	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, imp := range parsed.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s in %s: %v", imp.Path.Value, file, err)
			}
			if reason, blocked := disallowedExact[path]; blocked {
				t.Fatalf("%s imports %s: %s", file, path, reason)
			}
			for _, prefix := range disallowedPrefix {
				if path == prefix || strings.HasPrefix(path, prefix) || strings.HasPrefix(path, prefix+"/") {
					t.Fatalf("%s imports %s; bridge must stay decoupled from runtime/control-plane implementation packages", file, path)
				}
			}
		}
		if file == "management_config.go" {
			continue
		}
		if file == "stream.go" {
			disallowedRawStreamEvents := map[string]bool{
				"goal_changed":       true,
				"available_commands": true,
			}
			ast.Inspect(parsed, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				if disallowedRawStreamEvents[val] {
					pos := fset.Position(lit.Pos())
					t.Errorf("%s:%d decodes raw attach event %q directly; route session/goal/command events through internal/agentui", file, pos.Line, val)
				}
				return true
			})
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "config" {
				return true
			}
			if disallowedDirectConfigCalls[sel.Sel.Name] {
				pos := fset.Position(sel.Pos())
				t.Errorf("%s:%d calls config.%s directly; route installation-wide config operations through ManagementConfigActions", file, pos.Line, sel.Sel.Name)
			}
			return true
		})
	}
}
