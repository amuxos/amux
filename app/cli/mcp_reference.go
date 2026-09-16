package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/amuxos/amux/internal/mcp"
)

//go:generate go run -tags mcp_doc ../../cmd/amux --language en --output ../../docs/reference/mcp.md
//go:generate go run -tags mcp_doc ../../cmd/amux --language zh-CN --output ../../docs/reference/mcp.zh-CN.md

// renderMCPReference renders the public MCP surface from the exact registry
// used by tools/list. Retired tool names are not part of the public contract.
func renderMCPReference(tools []mcp.Tool) ([]byte, error) {
	byName := make(map[string]struct{}, len(tools))
	sortedTools := make([]mcp.Tool, 0, len(tools))
	prettySchemas := make(map[string][]byte, len(tools))

	for _, tool := range tools {
		if !strings.HasPrefix(tool.Name, "amux.") {
			return nil, fmt.Errorf("MCP tool %q must use the amux. prefix", tool.Name)
		}
		if _, exists := byName[tool.Name]; exists {
			return nil, fmt.Errorf("duplicate MCP tool %q", tool.Name)
		}
		if strings.TrimSpace(tool.Description) == "" {
			return nil, fmt.Errorf("MCP tool %q has an empty description", tool.Name)
		}
		pretty, err := prettyJSONSchema(tool.InputSchema)
		if err != nil {
			return nil, fmt.Errorf("MCP tool %q has invalid input schema: %w", tool.Name, err)
		}
		byName[tool.Name] = struct{}{}
		sortedTools = append(sortedTools, tool)
		prettySchemas[tool.Name] = pretty
	}
	sort.Slice(sortedTools, func(i, j int) bool { return sortedTools[i].Name < sortedTools[j].Name })

	var out bytes.Buffer
	out.WriteString("<!-- Code generated from app/cli's MCP tool registry; DO NOT EDIT. -->\n\n")
	out.WriteString("# amux MCP 工具参考\n\n")
	out.WriteString("本文档由 `amux mcp` 的实际 `tools/list` 注册表自动生成，覆盖正式工具、工具说明与完整输入 JSON Schema。修改 MCP 工具名称、可见性、说明或输入字段后，必须运行 `make mcp-doc`；仓库统一门禁会拒绝生成结果不一致的改动。\n\n")
	out.WriteString("MCP 的产品语义、权限与生命周期边界仍以 [`docs/current/cli-product-model.md`](../current/cli-product-model.md) 和 [`docs/distributed-task-model.md`](../distributed-task-model.md) 为准。运行时还会通过 [`amux.result_continue`](#amuxresult_continue) 对超过单页预算的成功结果续传；工具结果本身不是 `tools/list` 注册元数据，因此不在本文档中另维护一份可能漂移的输出 Schema。\n\n")
	fmt.Fprintf(&out, "`tools/list` 正式工具：**%d**。\n\n", len(sortedTools))

	out.WriteString("## 正式工具索引\n\n")
	out.WriteString("| 工具 | 说明 |\n")
	out.WriteString("| --- | --- |\n")
	for _, tool := range sortedTools {
		fmt.Fprintf(&out, "| %s | %s |\n", markdownCode(tool.Name), escapeTable(tool.Description))
	}

	for _, tool := range sortedTools {
		fmt.Fprintf(&out, "\n## %s\n\n", markdownCode(tool.Name))
		out.WriteString(strings.TrimSpace(tool.Description))
		out.WriteString("\n\n**输入 JSON Schema**\n\n```json\n")
		out.Write(prettySchemas[tool.Name])
		out.WriteString("\n```\n")
	}
	return out.Bytes(), nil
}

func prettyJSONSchema(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{"type":"object"}`)
	}
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	object, ok := schema.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("top level must be an object")
	}
	if object["type"] != "object" {
		return nil, fmt.Errorf("top-level type must be object")
	}
	return json.MarshalIndent(object, "", "  ")
}
