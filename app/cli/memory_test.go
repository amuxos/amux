package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/memory"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/viper"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeMemoryClient struct {
	pb.AmuxdClient
	binding func(context.Context) (*pb.MemoryBindingResponse, error)
	search  func(*pb.MemorySearchRequest) (*pb.MemorySearchResponse, error)
	get     func(*pb.MemoryGetRequest) (*pb.MemoryDocument, error)
	upsert  func(*pb.MemoryUpsertRequest) (*pb.MemoryWriteResponse, error)
	forget  func(*pb.MemoryForgetRequest) (*pb.MemoryForgetResponse, error)
}

func (f *fakeMemoryClient) GetMemoryBinding(ctx context.Context, _ *pb.MemoryBindingRequest, _ ...grpc.CallOption) (*pb.MemoryBindingResponse, error) {
	if f.binding == nil {
		return &pb.MemoryBindingResponse{Enabled: true, Binding: os.Getenv("AMUX_MEMORY_BINDING")}, nil
	}
	return f.binding(ctx)
}

func (f *fakeMemoryClient) MemorySearch(_ context.Context, req *pb.MemorySearchRequest, _ ...grpc.CallOption) (*pb.MemorySearchResponse, error) {
	return f.search(req)
}
func (f *fakeMemoryClient) MemoryGet(_ context.Context, req *pb.MemoryGetRequest, _ ...grpc.CallOption) (*pb.MemoryDocument, error) {
	return f.get(req)
}
func (f *fakeMemoryClient) MemoryUpsert(_ context.Context, req *pb.MemoryUpsertRequest, _ ...grpc.CallOption) (*pb.MemoryWriteResponse, error) {
	return f.upsert(req)
}
func (f *fakeMemoryClient) MemoryForget(_ context.Context, req *pb.MemoryForgetRequest, _ ...grpc.CallOption) (*pb.MemoryForgetResponse, error) {
	return f.forget(req)
}

func useMemoryClient(t *testing.T, client pb.AmuxdClient) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("AMUX_HOME", t.TempDir())
	t.Setenv("AMUX_SESSION_ID", "")
	t.Setenv("AMUX_MEMORY_BINDING", "")
	previous := newMemoryClient
	newMemoryClient = func() (pb.AmuxdClient, func() error, error) {
		if client == nil {
			t.Fatal("invalid memory input reached the Hub client")
		}
		return client, func() error { return nil }, nil
	}
	t.Cleanup(func() { newMemoryClient = previous })
}

func runMemoryCommand(t *testing.T, input string, args ...string) (string, error) {
	t.Helper()
	cmd := newMemoryCmd()
	var output bytes.Buffer
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func TestMemoryCLITransmitsFrozenBindingAndArguments(t *testing.T) {
	revision := strings.Repeat("a", 64)
	fake := &fakeMemoryClient{
		search: func(req *pb.MemorySearchRequest) (*pb.MemorySearchResponse, error) {
			if req.Query != "发布 门禁" || req.PathPrefix != "projects/amux/" || req.Limit != 3 || req.Binding != "binding-1" {
				t.Fatalf("search = %v", req)
			}
			return &pb.MemorySearchResponse{Incomplete: true}, nil
		},
		get: func(req *pb.MemoryGetRequest) (*pb.MemoryDocument, error) {
			if req.Path != "projects/amux.md" || req.StartLine != 0 || req.EndLine != 20 || req.Offset != 12 || req.Binding != "binding-1" {
				t.Fatalf("get = %v", req)
			}
			return &pb.MemoryDocument{Path: req.Path, Exists: true, Content: "片段", Revision: revision, Truncated: true, NextOffset: 18}, nil
		},
		upsert: func(req *pb.MemoryUpsertRequest) (*pb.MemoryWriteResponse, error) {
			if req.Path != "projects/amux.md" || req.Content != "原样 $(do-not-run) `literal`\n" || req.ExpectedRevision != "absent" || req.Binding != "binding-1" {
				t.Fatalf("upsert = %v", req)
			}
			return &pb.MemoryWriteResponse{Path: req.Path, Revision: revision}, nil
		},
		forget: func(req *pb.MemoryForgetRequest) (*pb.MemoryForgetResponse, error) {
			if req.Path != "projects/amux.md" || req.ExpectedRevision != revision || req.Binding != "binding-1" {
				t.Fatalf("forget = %v", req)
			}
			return &pb.MemoryForgetResponse{Path: req.Path, AlreadyAbsent: false}, nil
		},
	}
	useMemoryClient(t, fake)
	t.Setenv("AMUX_SESSION_ID", "session-1")
	t.Setenv("AMUX_MEMORY_BINDING", "binding-1")
	for _, test := range []struct {
		args        []string
		input, want string
	}{
		{[]string{"search", "发布", "门禁", "--path-prefix", "projects/amux/", "--limit", "3"}, "", `"incomplete":true`},
		{[]string{"show", "projects/amux.md", "--offset", "12", "--end-line", "20"}, "", `"next_offset":18`},
		{[]string{"upsert", "projects/amux.md", "--expected-revision", "absent"}, "原样 $(do-not-run) `literal`\n", `"revision":"` + revision + `"`},
		{[]string{"forget", "projects/amux.md", "--expected-revision", revision}, "", `"already_absent":false`},
	} {
		out, err := runMemoryCommand(t, test.input, test.args...)
		if err != nil || !strings.Contains(out, test.want) {
			t.Fatalf("%v: out=%q err=%v", test.args, out, err)
		}
	}
}

func TestMemoryCLIUpsertFileAndEmptyDocument(t *testing.T) {
	var contents []string
	useMemoryClient(t, &fakeMemoryClient{upsert: func(req *pb.MemoryUpsertRequest) (*pb.MemoryWriteResponse, error) {
		if req.Binding != "" {
			t.Fatalf("human CLI unexpectedly supplied binding: %q", req.Binding)
		}
		contents = append(contents, req.Content)
		return &pb.MemoryWriteResponse{Path: req.Path, Revision: strings.Repeat("b", 64)}, nil
	}})
	file := filepath.Join(t.TempDir(), "note.md")
	if err := os.WriteFile(file, []byte("# 文件\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runMemoryCommand(t, "ignored", "upsert", "note.md", "--file", file, "--expected-revision", "absent"); err != nil {
		t.Fatal(err)
	}
	if _, err := runMemoryCommand(t, "", "upsert", "note.md", "--expected-revision", "absent"); err != nil {
		t.Fatal(err)
	}
	if len(contents) != 2 || contents[0] != "# 文件\n" || contents[1] != "" {
		t.Fatalf("contents = %#v", contents)
	}
}

func TestMemoryCLIRejectsInvalidInputBeforeDial(t *testing.T) {
	useMemoryClient(t, nil)
	for _, test := range []struct {
		args  []string
		input string
	}{
		{[]string{"upsert", "note.md"}, "content"},
		{[]string{"upsert", "note.md", "--expected-revision", "latest"}, "content"},
		{[]string{"upsert", "note.md", "--expected-revision", "absent"}, string([]byte{0xff})},
		{[]string{"upsert", "note.md", "--expected-revision", "absent"}, strings.Repeat("a", memory.MaxDocumentBytes+1)},
		{[]string{"forget", "note.md", "--expected-revision", strings.Repeat("A", 64)}, ""},
		{[]string{"show", "note.md", "--start-line", "2", "--offset", "1"}, ""},
		{[]string{"show", "note.md", "--start-line", "5", "--end-line", "4"}, ""},
		{[]string{"search", "query", "--limit", "21"}, ""},
		{[]string{"search", "query", "--principal", "other"}, ""},
	} {
		if _, err := runMemoryCommand(t, test.input, test.args...); err == nil {
			t.Fatalf("accepted %v", test.args)
		}
	}
}

func TestMemoryExportContinuationAndRevisionSafety(t *testing.T) {
	for _, mode := range []string{"complete", "changed", "missing", "stalled", "oversized", "empty"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			useMemoryClient(t, &fakeMemoryClient{get: func(req *pb.MemoryGetRequest) (*pb.MemoryDocument, error) {
				calls++
				if req.Path != "note.md" || req.Binding != "binding" {
					t.Fatalf("get = %v", req)
				}
				if mode == "missing" {
					return &pb.MemoryDocument{Path: req.Path, Revision: "absent"}, nil
				}
				if mode == "empty" {
					return &pb.MemoryDocument{Path: req.Path, Revision: "rev", Exists: true}, nil
				}
				if mode == "oversized" {
					return &pb.MemoryDocument{Path: req.Path, Revision: "rev", Exists: true, Content: strings.Repeat("a", memory.MaxDocumentBytes+1)}, nil
				}
				if calls == 1 {
					if req.Offset != 0 {
						t.Fatalf("first offset = %d", req.Offset)
					}
					doc := &pb.MemoryDocument{Path: req.Path, Revision: "rev", Exists: true, Content: "你好", Truncated: true, NextOffset: 6}
					if mode == "stalled" {
						doc.NextOffset = 0
					}
					return doc, nil
				}
				if calls != 2 || req.Offset != 6 {
					t.Fatalf("continuation = %v calls=%d", req, calls)
				}
				revision := "rev"
				if mode == "changed" {
					revision = "new-rev"
				}
				return &pb.MemoryDocument{Path: req.Path, Revision: revision, Exists: true, Content: "\nworld\n"}, nil
			}})
			t.Setenv("AMUX_MEMORY_BINDING", "binding")
			out, err := runMemoryCommand(t, "", "export", "note.md")
			if mode == "complete" {
				if err != nil || out != "你好\nworld\n" {
					t.Fatalf("export = %q, %v", out, err)
				}
			} else if mode == "empty" {
				if err != nil || out != "" {
					t.Fatalf("empty export = %q, %v", out, err)
				}
			} else if err == nil || out != "" {
				t.Fatalf("unsafe export = %q, %v", out, err)
			}
		})
	}
}

func TestMemoryDisabledAndLegacySessionsHideToolsAndRejectCalls(t *testing.T) {
	for _, mode := range []string{"disabled-binding", "legacy-session", "disabled-hub", "unavailable-hub", "changed-scope", "human", "managed", "local-config-disabled"} {
		t.Run(mode, func(t *testing.T) {
			allowed := mode == "human" || mode == "managed" || mode == "local-config-disabled"
			useMemoryClient(t, &fakeMemoryClient{
				binding: func(ctx context.Context) (*pb.MemoryBindingResponse, error) {
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("unbounded discovery")
					}
					if mode == "unavailable-hub" {
						return nil, status.Error(codes.Unavailable, "offline")
					}
					return &pb.MemoryBindingResponse{Enabled: mode != "disabled-hub", Binding: "global"}, nil
				},
				get: func(req *pb.MemoryGetRequest) (*pb.MemoryDocument, error) {
					if !allowed {
						return nil, status.Error(codes.FailedPrecondition, "memory unavailable")
					}
					return &pb.MemoryDocument{Path: req.Path, Revision: "absent"}, nil
				},
			})
			switch mode {
			case "disabled-binding":
				t.Setenv("AMUX_MEMORY_BINDING", "disabled")
			case "legacy-session":
				t.Setenv("AMUX_SESSION_ID", "old-session")
			case "local-config-disabled":
				// A remote client's default/explicit false is not a second switch.
				if err := os.WriteFile(filepath.Join(os.Getenv("AMUX_HOME"), "config.toml"), []byte("[memory]\nenabled = false\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-scope":
				t.Setenv("AMUX_MEMORY_BINDING", "principal:previous")
			case "managed":
				t.Setenv("AMUX_SESSION_ID", "new-session")
				t.Setenv("AMUX_MEMORY_BINDING", "global")
			}
			count := 0
			for _, tool := range runtimeAMUXTools(context.Background()) {
				if strings.HasPrefix(tool.Name, "amux.memory_") {
					count++
				}
			}
			if allowed && count != 4 || !allowed && count != 0 {
				t.Fatalf("memory discovery count=%d allowed=%t", count, allowed)
			}
			_, err := runMemoryCommand(t, "", "show", "MEMORY.md")
			if (err == nil) != allowed {
				t.Fatalf("CLI err=%v allowed=%t", err, allowed)
			}
			res, err := (&mcpHandler{}).handle(context.Background(), "amux.memory_get", json.RawMessage(`{"path":"MEMORY.md"}`))
			if err != nil || res.IsError == allowed {
				t.Fatalf("MCP result=%+v err=%v allowed=%t", res, err, allowed)
			}
		})
	}
}

func TestMemoryMCPDispatchAndBoundedContinuation(t *testing.T) {
	revision := strings.Repeat("d", 64)
	seen := map[string]bool{}
	useMemoryClient(t, &fakeMemoryClient{
		search: func(req *pb.MemorySearchRequest) (*pb.MemorySearchResponse, error) {
			if req.Binding != "bound" || req.Query != "历史" || req.PathPrefix != "topics/" || req.Limit != 2 {
				t.Fatalf("search = %v", req)
			}
			seen["search"] = true
			return &pb.MemorySearchResponse{Matches: []*pb.MemoryMatch{{Path: "topics/test.md", Line: 3, Snippet: "历史", Revision: revision, Truncated: true}}, Truncated: true, ScannedFiles: 17}, nil
		},
		get: func(req *pb.MemoryGetRequest) (*pb.MemoryDocument, error) {
			if req.Binding != "bound" || req.Path != "topics/test.md" || req.Offset != 8 || req.EndLine != 30 {
				t.Fatalf("get = %v", req)
			}
			seen["get"] = true
			return &pb.MemoryDocument{Path: req.Path, Content: strings.Repeat("记忆", 1200), Revision: revision, Exists: true, Truncated: true, NextOffset: 7208}, nil
		},
		upsert: func(req *pb.MemoryUpsertRequest) (*pb.MemoryWriteResponse, error) {
			if req.Binding != "bound" || req.Content != "记忆" || req.ExpectedRevision != revision {
				t.Fatalf("upsert = %v", req)
			}
			seen["upsert"] = true
			return &pb.MemoryWriteResponse{Path: req.Path, Revision: revision}, nil
		},
		forget: func(req *pb.MemoryForgetRequest) (*pb.MemoryForgetResponse, error) {
			if req.Binding != "bound" || req.ExpectedRevision != revision {
				t.Fatalf("forget = %v", req)
			}
			seen["forget"] = true
			return &pb.MemoryForgetResponse{Path: req.Path, AlreadyAbsent: true}, nil
		},
	})
	t.Setenv("AMUX_SESSION_ID", "session")
	t.Setenv("AMUX_MEMORY_BINDING", "bound")
	handler := &mcpHandler{}
	for _, test := range []struct{ name, args string }{
		{"search", `{"query":"历史","path_prefix":"topics/","limit":2}`},
		{"get", `{"path":"topics/test.md","offset":8,"end_line":30}`},
		{"upsert", fmt.Sprintf(`{"path":"topics/test.md","content":"记忆","expected_revision":%q}`, revision)},
		{"forget", fmt.Sprintf(`{"path":"topics/test.md","expected_revision":%q}`, revision)},
	} {
		result, err := handler.handle(context.Background(), "amux.memory_"+test.name, json.RawMessage(test.args))
		if err != nil || result.IsError || !seen[test.name] {
			t.Fatalf("%s result=%+v err=%v", test.name, result, err)
		}
		if len(result.Content[0].Text) > mcpResultMaxBytes {
			t.Fatal("unbounded MCP response")
		}
		if test.name == "search" {
			var search map[string]any
			if err := json.Unmarshal([]byte(result.Content[0].Text), &search); err != nil {
				t.Fatal(err)
			}
			if search["incomplete"] != false || search["truncated"] != true || search["scanned_files"] != float64(17) {
				t.Fatalf("search lost scan/result limits: %#v", search)
			}
		}
		if test.name == "get" {
			var combined strings.Builder
			for {
				var page struct {
					Chunk    string `json:"content_chunk"`
					Complete bool   `json:"complete"`
					Cursor   string `json:"next_cursor"`
				}
				if err := json.Unmarshal([]byte(result.Content[0].Text), &page); err != nil {
					t.Fatal(err)
				}
				combined.WriteString(page.Chunk)
				if page.Complete {
					break
				}
				if page.Cursor == "" {
					t.Fatal("missing MCP continuation")
				}
				result, err = handler.handle(context.Background(), "amux.result_continue", json.RawMessage(fmt.Sprintf(`{"cursor":%q}`, page.Cursor)))
				if err != nil || result.IsError || len(result.Content[0].Text) > mcpResultMaxBytes {
					t.Fatalf("continuation=%+v err=%v", result, err)
				}
			}
			var document map[string]any
			if err := json.Unmarshal([]byte(combined.String()), &document); err != nil {
				t.Fatal(err)
			}
			if document["next_offset"] != float64(7208) || document["revision"] != revision || document["content"] != strings.Repeat("记忆", 1200) {
				t.Fatal("memory continuation lost metadata or content")
			}
		}
	}
}

func TestMemoryMCPRejectsMalformedArgumentsAndScopeOverrides(t *testing.T) {
	useMemoryClient(t, nil)
	for _, test := range []struct{ name, args string }{
		{"search", `{"query":""}`},
		{"search", `{"query":"test","principal_id":"other"}`},
		{"search", `{"query":"test","binding":"other"}`},
		{"get", `{"path":"test.md","scope":"global"}`},
		{"get", `{"path":"test.md","offset":-1}`},
		{"get", `null`},
		{"get", `{"path":"test.md"} {}`},
		{"upsert", `{"path":"test.md","expected_revision":"absent"}`},
		{"upsert", `{"path":"test.md","content":null,"expected_revision":"absent"}`},
		{"forget", `{"path":"test.md","expected_revision":""}`},
	} {
		result, err := (&mcpHandler{}).handle(context.Background(), "amux.memory_"+test.name, json.RawMessage(test.args))
		if err != nil || !result.IsError {
			t.Fatalf("%s %s accepted: result=%+v err=%v", test.name, test.args, result, err)
		}
	}
	for _, tool := range memoryTools() {
		var schema struct {
			Properties           map[string]any `json:"properties"`
			AdditionalProperties bool           `json:"additionalProperties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.AdditionalProperties {
			t.Fatalf("%s allows scope override fields", tool.Name)
		}
		for _, name := range []string{"principal_id", "scope", "binding", "session_id"} {
			if _, ok := schema.Properties[name]; ok {
				t.Fatalf("%s exposes %s", tool.Name, name)
			}
		}
	}
}

func TestMemoryContinuationHonorsExperimentSwitch(t *testing.T) {
	enabled := true
	useMemoryClient(t, &fakeMemoryClient{
		binding: func(context.Context) (*pb.MemoryBindingResponse, error) {
			return &pb.MemoryBindingResponse{Enabled: enabled, Binding: "global"}, nil
		},
		get: func(req *pb.MemoryGetRequest) (*pb.MemoryDocument, error) {
			return &pb.MemoryDocument{Path: req.Path, Content: strings.Repeat("memory", 1200)}, nil
		},
	})
	t.Setenv("AMUX_MEMORY_BINDING", "global")
	h := &mcpHandler{}
	first, err := h.handle(context.Background(), "amux.memory_get", json.RawMessage(`{"path":"MEMORY.md"}`))
	if err != nil || first.IsError {
		t.Fatalf("first page: %+v, %v", first, err)
	}
	var page struct {
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(first.Content[0].Text), &page); err != nil || page.NextCursor == "" {
		t.Fatalf("missing continuation: %+v, %v", page, err)
	}
	args, _ := json.Marshal(map[string]string{"cursor": page.NextCursor})
	enabled = false
	denied, err := h.handle(context.Background(), "amux.result_continue", args)
	if err != nil || !denied.IsError {
		t.Fatalf("disabled cached memory leaked: %+v, %v", denied, err)
	}
	enabled = true
	restored, err := h.handle(context.Background(), "amux.result_continue", args)
	if err != nil || restored.IsError {
		t.Fatalf("reenabled continuation: %+v, %v", restored, err)
	}
}
