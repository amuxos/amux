package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/mcp"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestMCPCallCompletesChunksOnSameRegistry(t *testing.T) {
	for _, isError := range []bool{false, true} {
		original := strings.Repeat("完整结果🧪", 8000)
		h := &mcpHandler{}
		calls := 0
		server := mcp.NewServer("test", "1", []mcp.Tool{{Name: "amux.test"}, {Name: "amux.result_continue"}}, func(ctx context.Context, name string, args json.RawMessage) (mcp.ToolResult, error) {
			if name == "amux.result_continue" {
				return h.continueResult(ctx, args), nil
			}
			calls++
			r := mcp.Text(original)
			r.IsError = isError
			return h.boundResult(r), nil
		})
		result, err := callMCPComplete(context.Background(), server.CallTool, "amux.test", json.RawMessage(`{}`))
		if err != nil || result.IsError != isError || len(result.Content) != 1 || result.Content[0].Text != original {
			t.Fatalf("complete result lost content or error: err=%v isError=%v", err, result.IsError)
		}
		if calls != 1 {
			t.Fatalf("original tool replayed %d times", calls)
		}
	}
}

func TestMCPCallContinuationFailureDoesNotReturnPartialSuccess(t *testing.T) {
	for _, mode := range []string{"expired", "invalid", "transport"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			call := func(ctx context.Context, name string, args json.RawMessage) (mcp.ToolResult, error) {
				calls++
				if calls == 1 {
					return mcp.Text(toJSON(continuationEnvelope("partial", "next", false, 20, false))), nil
				}
				if mode == "expired" {
					return mcp.ErrorText("expired"), nil
				}
				if mode == "transport" {
					return mcp.ToolResult{}, errors.New("disconnected")
				}
				return mcp.Text(`{"partial":true}`), nil
			}
			result, err := callMCPComplete(context.Background(), call, "amux.test", json.RawMessage(`{}`))
			if err == nil && !result.IsError {
				t.Fatalf("partial success: %+v", result)
			}
		})
	}
}

func stubUnavailableMemoryClient(t *testing.T) {
	t.Helper()
	previous := newMemoryClient
	newMemoryClient = func() (pb.AmuxdClient, func() error, error) {
		return nil, nil, errors.New("hub unavailable")
	}
	t.Cleanup(func() { newMemoryClient = previous })
}

func TestMCPCallRejectsInputAndDisabledTools(t *testing.T) {
	stubUnavailableMemoryClient(t)
	for _, tc := range []struct{ name, input, config string }{{"object", `[]`, ""}, {"null", `null`, ""}, {"disabled", `{}`, "[mcp]\nenabled=false\n"}} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AMUX_HOME", "")
			t.Setenv("AMUX_LANGUAGE", "en")
			writeUserConfig(t, home, tc.config)
			cmd := newMcpCmd()
			cmd.SetArgs([]string{"call", "amux.profile_list"})
			cmd.SetIn(strings.NewReader(tc.input))
			cmd.SilenceUsage = true
			var out strings.Builder
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			if err := cmd.Execute(); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestMCPCallDescribeUsesRuntimeRegistryBothLanguages(t *testing.T) {
	stubUnavailableMemoryClient(t)
	for _, lang := range []string{"en", "zh-CN"} {
		t.Run(lang, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("AMUX_HOME", "")
			t.Setenv("AMUX_LANGUAGE", lang)
			writeUserConfig(t, home, "")
			cmd := newMcpCmd()
			cmd.SetArgs([]string{"call", "amux.schedule_create", "--describe"})
			var out strings.Builder
			cmd.SetOut(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var tool mcp.Tool
			if err := json.Unmarshal([]byte(out.String()), &tool); err != nil {
				t.Fatal(err)
			}
			if tool.Name != "amux.schedule_create" || !strings.Contains(string(tool.InputSchema), "schedule_type") {
				t.Fatalf("schema=%s", out.String())
			}
			if strings.Contains(cmd.Commands()[0].Short, "mcp.call.") {
				t.Fatal("raw key")
			}
		})
	}
}
