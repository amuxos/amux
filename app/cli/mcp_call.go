package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/mcp"
	"github.com/spf13/cobra"
)

func newMCPCallCmd() *cobra.Command {
	var inputFile string
	var describe bool
	tr := func(cmd *cobra.Command, key string) string {
		return commandCatalog.Format(commandLocale(cmd), "mcp.call."+key)
	}
	cmd := &cobra.Command{Use: "call <tool>", Args: cobra.ExactArgs(1), ValidArgsFunction: completeNoFiles}
	cmd.Short = tr(cmd, "short")
	cmd.Long = tr(cmd, "long")
	cmd.Flags().StringVar(&inputFile, "input-file", "-", tr(cmd, "input_file"))
	cmd.Flags().BoolVar(&describe, "describe", false, tr(cmd, "describe"))
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		_ = loadUserConfig()
		if !config.GetMCP().Enabled {
			return errors.New(tr(cmd, "disabled"))
		}
		tools := runtimeAMUXTools(cmd.Context())
		var selected *mcp.Tool
		for i := range tools {
			if tools[i].Name == args[0] {
				selected = &tools[i]
				break
			}
		}
		if selected == nil {
			return fmt.Errorf("%s: %s", tr(cmd, "unknown_tool"), args[0])
		}
		if describe {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(selected)
		}
		in := cmd.InOrStdin()
		if inputFile != "-" {
			f, err := os.Open(inputFile)
			if err != nil {
				return fmt.Errorf("%s: %w", tr(cmd, "input_error"), err)
			}
			defer f.Close()
			in = f
		}
		data, err := io.ReadAll(io.LimitReader(in, (8<<20)+1))
		if err != nil {
			return fmt.Errorf("%s: %w", tr(cmd, "input_error"), err)
		}
		if len(data) > 8<<20 {
			return errors.New(tr(cmd, "input_too_large"))
		}
		if strings.TrimSpace(string(data)) == "" {
			data = []byte(`{}`)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil || object == nil {
			return errors.New(tr(cmd, "input_object"))
		}
		h := &mcpHandler{}
		server := mcp.NewServer("amux", version, tools, h.handle)
		result, err := callMCPComplete(cmd.Context(), server.CallTool, args[0], data)
		if err != nil {
			var failure mcpCallFailure
			if errors.As(err, &failure) {
				err = errors.New(tr(cmd, string(failure)))
			}
			return fmt.Errorf("%s: %w", tr(cmd, "failed"), err)
		}
		if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
			return err
		}
		if result.IsError {
			return errors.New(tr(cmd, "tool_error"))
		}
		return nil
	}
	return cmd
}

type mcpCallFailure string

func (e mcpCallFailure) Error() string { return string(e) }

// Only transport chunks are joined here. Tool-level pagination stays explicit,
// so a write tool can never be replayed while completing its response.
func callMCPComplete(ctx context.Context, call func(context.Context, string, json.RawMessage) (mcp.ToolResult, error), name string, args json.RawMessage) (mcp.ToolResult, error) {
	result, err := call(ctx, name, args)
	if err != nil {
		return mcp.ToolResult{}, err
	}
	var text strings.Builder
	seen := map[string]bool{}
	originalBytes := -1
	originalError := false
	for {
		var page struct {
			Schema   string `json:"schema_version"`
			Complete bool   `json:"complete"`
			Chunk    string `json:"content_chunk"`
			Bytes    int    `json:"original_bytes"`
			Error    bool   `json:"original_error"`
			Cursor   string `json:"next_cursor"`
			Tool     string `json:"next_tool"`
		}
		isPage := len(result.Content) == 1 && json.Unmarshal([]byte(result.Content[0].Text), &page) == nil && page.Schema == "mcp.result_page.v1"
		if !isPage || result.IsError {
			if originalBytes >= 0 && !result.IsError {
				return mcp.ToolResult{}, mcpCallFailure("continuation_response")
			}
			return result, nil
		}
		if originalBytes < 0 {
			originalBytes = page.Bytes
			originalError = page.Error
		}
		if page.Bytes != originalBytes || page.Error != originalError || page.Bytes < 0 {
			return mcp.ToolResult{}, mcpCallFailure("continuation_metadata")
		}
		text.WriteString(page.Chunk)
		if text.Len() > originalBytes {
			return mcp.ToolResult{}, mcpCallFailure("continuation_size")
		}
		if page.Complete {
			if text.Len() != originalBytes {
				return mcp.ToolResult{}, mcpCallFailure("continuation_incomplete")
			}
			result = mcp.Text(text.String())
			result.IsError = originalError
			return result, nil
		}
		if page.Tool != "amux.result_continue" || page.Cursor == "" || seen[page.Cursor] || page.Chunk == "" {
			return mcp.ToolResult{}, mcpCallFailure("continuation_cursor")
		}
		seen[page.Cursor] = true
		next, _ := json.Marshal(map[string]string{"cursor": page.Cursor})
		result, err = call(ctx, page.Tool, next)
		if err != nil {
			return mcp.ToolResult{}, err
		}
	}
}
