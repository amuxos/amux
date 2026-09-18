package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/mcp"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type memorySearchArgs struct {
	Query      string `json:"query" desc:"空白分隔的字面关键词；匹配中文连续文本和忽略大小写的英文，不使用正则或语义检索"`
	PathPrefix string `json:"path_prefix,omitempty" desc:"当前授权范围内的相对路径前缀，例如 projects/amux/"`
	Limit      int32  `json:"limit,omitempty" desc:"最多返回的命中文档数；默认 5，最大 20，仍受响应大小限制"`
}

type memoryGetArgs struct {
	Path      string `json:"path" desc:"当前授权范围内的 Markdown 相对路径；新会话先读取 MEMORY.md"`
	StartLine int32  `json:"start_line,omitempty" desc:"读取起始行（从 1 开始）；省略时从开头读取，不能与 offset 同时使用"`
	EndLine   int32  `json:"end_line,omitempty" desc:"读取末行（包含）；省略时读取到响应上限"`
	Offset    int64  `json:"offset,omitempty" desc:"上次 memory_get 返回的 next_offset 字节位置；续读时保留相同 path 和 end_line，并核对 revision"`
}

type memoryUpsertArgs struct {
	Path             string `json:"path" desc:"当前授权范围内的 Markdown 相对路径"`
	Content          string `json:"content" desc:"完整 UTF-8 Markdown 正文；主题文档最多 64 KiB，MEMORY.md 只保留简短偏好和入口（最多 2 KiB、64 行）"`
	ExpectedRevision string `json:"expected_revision" desc:"memory_get 返回的 SHA-256 revision；新建文档必须填 absent，冲突后重读合并"`
}

type memoryForgetArgs struct {
	Path             string `json:"path" desc:"当前授权范围内的 Markdown 相对路径"`
	ExpectedRevision string `json:"expected_revision" desc:"memory_get 返回的 SHA-256 revision；文档已不存在时可填 absent"`
}

func memoryTools() []mcp.Tool {
	tool := func(name, description string, args any) mcp.Tool {
		schema := clientop.JSONSchema(args)
		schema["additionalProperties"] = false
		raw, err := json.Marshal(schema)
		if err != nil {
			panic("build memory schema: " + err.Error())
		}
		return mcp.Tool{Name: name, Description: description, InputSchema: raw}
	}
	return []mcp.Tool{
		tool("amux.memory_search", "按需搜索跨会话记忆；涉及以前的工作、决定或偏好时查询，返回有界命中片段。incomplete=true 表示扫描未完成；无需历史的任务不要检索。", memorySearchArgs{}),
		tool("amux.memory_get", "按需读取记忆文档或指定行段及 revision；新会话读取一次 MEMORY.md，同一上下文复用。truncated=true 时使用 next_offset 续读并核对 revision，路径不是本机文件。", memoryGetArgs{}),
		tool("amux.memory_upsert", "按 revision 条件保存长期记忆；用于用户明确要求记住、稳定偏好和已验证经验。详细内容放主题文档，入口保持简短；不保存秘密、临时授权或推测。冲突须重读合并，成功后才能声称已保存。", memoryUpsertArgs{}),
		tool("amux.memory_forget", "按 revision 条件删除一份记忆文档；不会清除已进入会话的历史。纠正或忘记偏好时还需检查 MEMORY.md 和相关主题，修订重复内容及引用；部分失败须明确报告。", memoryForgetArgs{}),
	}
}

// Keep the complete static registry for documentation. Runtime discovery omits
// unavailable tools, while each handler still validates policy before dialing.
func runtimeAMUXTools(ctx context.Context) []mcp.Tool {
	tools := amuxTools()
	if memoryAvailable(ctx) {
		return tools
	}
	out := make([]mcp.Tool, 0, len(tools))
	for _, tool := range tools {
		if !strings.HasPrefix(tool.Name, "amux.memory_") {
			out = append(out, tool)
		}
	}
	return out
}

// Discovery is bounded and happens once at MCP startup, not on every turn.
// Fail closed when the Hub is unavailable without blocking unrelated tools.
func memoryAvailable(ctx context.Context) bool {
	client, closeFn, binding, err := openMemoryClient()
	if err != nil {
		return false
	}
	defer closeFn()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	capability, err := client.GetMemoryBinding(ctx, &pb.MemoryBindingRequest{})
	return err == nil && capability.GetEnabled() && capability.GetBinding() != "" &&
		(binding == "" || binding == capability.GetBinding())
}

func decodeMemoryArgs(raw json.RawMessage, target any, required ...string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid arguments: expected one JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("invalid arguments: expected an object")
	}
	for _, name := range required {
		value, exists := fields[name]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

func (h *mcpHandler) memorySearch(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input memorySearchArgs
	if err := decodeMemoryArgs(args, &input, "query"); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if strings.TrimSpace(input.Query) == "" || input.Limit < 0 || input.Limit > 20 {
		return mcp.ErrorText("query is required and limit must be between 0 and 20"), nil
	}
	client, closeFn, binding, err := openMemoryClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.MemorySearch(ctx, &pb.MemorySearchRequest{Query: input.Query, PathPrefix: input.PathPrefix, Limit: input.Limit, Binding: binding})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if resp == nil {
		return mcp.ErrorText("hub returned an empty memory search response"), nil
	}
	return mcp.Text(toJSON(memorySearchView(resp))), nil
}

func (h *mcpHandler) memoryGet(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input memoryGetArgs
	if err := decodeMemoryArgs(args, &input, "path"); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if err := validateMemoryRead(input.Path, input.StartLine, input.EndLine, input.Offset); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, binding, err := openMemoryClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.MemoryGet(ctx, &pb.MemoryGetRequest{Path: input.Path, StartLine: input.StartLine, EndLine: input.EndLine, Offset: input.Offset, Binding: binding})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if resp == nil {
		return mcp.ErrorText("hub returned an empty memory document response"), nil
	}
	return mcp.Text(toJSON(memoryDocumentView(resp))), nil
}

func (h *mcpHandler) memoryUpsert(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input memoryUpsertArgs
	if err := decodeMemoryArgs(args, &input, "path", "content", "expected_revision"); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if err := validateMemoryRead(input.Path, 0, 0, 0); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if err := validateMemoryRevision(input.ExpectedRevision); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if err := validateMemoryContent(input.Content); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, binding, err := openMemoryClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.MemoryUpsert(ctx, &pb.MemoryUpsertRequest{Path: input.Path, Content: input.Content, ExpectedRevision: input.ExpectedRevision, Binding: binding})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if resp == nil {
		return mcp.ErrorText("hub returned an empty memory write response"), nil
	}
	return mcp.Text(toJSON(map[string]any{"path": resp.GetPath(), "revision": resp.GetRevision()})), nil
}

func (h *mcpHandler) memoryForget(ctx context.Context, args json.RawMessage) (mcp.ToolResult, error) {
	var input memoryForgetArgs
	if err := decodeMemoryArgs(args, &input, "path", "expected_revision"); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if err := validateMemoryRead(input.Path, 0, 0, 0); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if err := validateMemoryRevision(input.ExpectedRevision); err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	client, closeFn, binding, err := openMemoryClient()
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	defer closeFn()
	resp, err := client.MemoryForget(ctx, &pb.MemoryForgetRequest{Path: input.Path, ExpectedRevision: input.ExpectedRevision, Binding: binding})
	if err != nil {
		return mcp.ErrorText(err.Error()), nil
	}
	if resp == nil {
		return mcp.ErrorText("hub returned an empty memory forget response"), nil
	}
	return mcp.Text(toJSON(map[string]any{"path": resp.GetPath(), "already_absent": resp.GetAlreadyAbsent()})), nil
}
