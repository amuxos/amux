package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/amuxos/amux/internal/doclang"
	"github.com/amuxos/amux/internal/mcp"
)

func TestRenderMCPReferenceUsesLiveToolRegistry(t *testing.T) {
	tools := amuxTools()
	gotBytes, err := renderMCPReference(tools)
	if err != nil {
		t.Fatalf("render MCP reference: %v", err)
	}
	got := string(gotBytes)

	for _, want := range []string{
		"Code generated from app/cli's MCP tool registry; DO NOT EDIT.",
		"# amux MCP 工具参考",
		fmt.Sprintf("`tools/list` 正式工具：**%d**。", len(tools)),
		"## `amux.profile_list`",
		"分页列出非开发版 Agent Profile",
		`"namespace":`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated MCP reference missing %q", want)
		}
	}

	for _, tool := range tools {
		heading := "## `" + tool.Name + "`\n"
		if count := strings.Count(got, heading); count != 1 {
			t.Errorf("tool %q has %d generated sections, want 1", tool.Name, count)
		}
	}
}

func TestMCPReferenceDescriptionsAreChinese(t *testing.T) {
	for _, tool := range amuxTools() {
		if !containsHan(tool.Description) {
			t.Errorf("tool %q description is not Chinese: %q", tool.Name, tool.Description)
		}
		var schema any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("decode %s schema: %v", tool.Name, err)
		}
		walkSchemaDescriptions(t, tool.Name, schema)
	}

	got, err := renderMCPReference(amuxTools())
	if err != nil {
		t.Fatalf("render MCP reference: %v", err)
	}
	for _, want := range []string{"## 正式工具索引", "工具说明与完整输入 JSON Schema"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("generated Chinese MCP reference missing %q", want)
		}
	}
}

func walkSchemaDescriptions(t *testing.T, toolName string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "description" {
				description, ok := child.(string)
				if !ok || !containsHan(description) {
					t.Errorf("tool %q schema description is not Chinese: %v", toolName, child)
				}
			}
			walkSchemaDescriptions(t, toolName, child)
		}
	case []any:
		for _, child := range typed {
			walkSchemaDescriptions(t, toolName, child)
		}
	}
}

func containsHan(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0
}

func TestRenderMCPReferenceIsDeterministic(t *testing.T) {
	first, err := renderMCPReference(amuxTools())
	if err != nil {
		t.Fatalf("first render: %v", err)
	}
	second, err := renderMCPReference(amuxTools())
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("generated MCP reference is not deterministic")
	}
}

func TestLocalizedMCPReferencePreservesSchemaContracts(t *testing.T) {
	tools := amuxTools()
	source, err := renderMCPReference(tools)
	if err != nil {
		t.Fatal(err)
	}
	// Descriptions are documentation; every validation field and literal must
	// retain the runtime registry's exact value in both published languages.
	var withoutDescriptions func(any)
	withoutDescriptions = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			if _, ok := value["description"].(string); ok {
				delete(value, "description")
			}
			for _, child := range value {
				withoutDescriptions(child)
			}
		case []any:
			for _, child := range value {
				withoutDescriptions(child)
			}
		}
	}
	for _, language := range []string{"en", "zh-CN"} {
		t.Run(language, func(t *testing.T) {
			localized, err := doclang.Reference(source, language, "mcp")
			if err != nil {
				t.Fatal(err)
			}
			for _, tool := range tools {
				_, section, found := strings.Cut(string(localized), "## `"+tool.Name+"`\n")
				if !found {
					t.Fatalf("missing tool %q", tool.Name)
				}
				section, _, _ = strings.Cut(section, "\n## ")
				_, schema, found := strings.Cut(section, "```json\n")
				if !found {
					t.Fatalf("missing schema for %q", tool.Name)
				}
				schema, _, _ = strings.Cut(schema, "\n```")
				var actual, documented any
				if err := json.Unmarshal(tool.InputSchema, &actual); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(schema), &documented); err != nil {
					t.Fatalf("decode documented %s schema: %v", tool.Name, err)
				}
				withoutDescriptions(actual)
				withoutDescriptions(documented)
				if !reflect.DeepEqual(actual, documented) {
					t.Errorf("localization changed the %s schema contract", tool.Name)
				}
			}
		})
	}
}

func TestRenderMCPReferenceRejectsInvalidRegistry(t *testing.T) {
	validSchema := json.RawMessage(`{"type":"object","properties":{}}`)
	tests := []struct {
		name    string
		tools   []mcp.Tool
		wantErr string
	}{
		{
			name: "duplicate tool",
			tools: []mcp.Tool{
				{Name: "amux.task_get", Description: "first", InputSchema: validSchema},
				{Name: "amux.task_get", Description: "second", InputSchema: validSchema},
			},
			wantErr: "duplicate MCP tool",
		},
		{
			name: "invalid schema",
			tools: []mcp.Tool{
				{Name: "amux.task_get", Description: "Get a task", InputSchema: json.RawMessage(`{`)},
			},
			wantErr: "invalid input schema",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderMCPReference(tc.tools)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("render error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
