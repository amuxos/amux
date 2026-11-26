package feishuexchange

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionCodeDoesNotImportBridgeOrAppPackages(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package files: %v", err)
	}
	disallowed := []string{
		"github.com/amuxos/amux/internal/bridge",
		"github.com/amuxos/amux/internal/config",
		"github.com/amuxos/amux/app/",
		"github.com/larksuite/oapi-sdk-go",
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
				if path == blocked || strings.HasPrefix(path, blocked) || strings.HasPrefix(path, blocked+"/") {
					t.Fatalf("%s imports %s; feishuexchange must stay decoupled from bridge, config, app, and Lark SDK", file, path)
				}
			}
		}
	}
}
