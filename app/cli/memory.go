package cli

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/amuxos/amux/internal/memory"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

// The authenticated Hub client resolves identity; the binding is only a frozen
// scope precondition. Neither CLI nor MCP accepts a caller-supplied principal.
var newMemoryClient = newAmuxdClient

func memoryBindingFromEnv() (string, error) {
	binding := strings.TrimSpace(os.Getenv("AMUX_MEMORY_BINDING"))
	if binding == "disabled" {
		return "", fmt.Errorf("memory is disabled for this conversation; start a new conversation to change its memory policy")
	}
	if binding == "" && strings.TrimSpace(os.Getenv("AMUX_SESSION_ID")) != "" {
		return "", fmt.Errorf("this managed conversation has no memory binding; start a new conversation to use memory")
	}
	return binding, nil
}

func openMemoryClient() (pb.AmuxdClient, func() error, string, error) {
	binding, err := memoryBindingFromEnv()
	if err != nil {
		return nil, nil, "", err
	}
	_ = loadUserConfig()
	// The Hub owns the experiment policy. A remote client
	// must not need a second opt-in in its local configuration.
	client, closeFn, err := newMemoryClient()
	return client, closeFn, binding, err
}

func validateMemoryRevision(revision string) error {
	if revision == memory.AbsentRevision {
		return nil
	}
	decoded, err := hex.DecodeString(revision)
	if err != nil || len(decoded) != 32 || strings.ToLower(revision) != revision {
		return fmt.Errorf("expected_revision must be a SHA-256 revision returned by memory_get/show, or absent for creation")
	}
	return nil
}

func validateMemoryRead(path string, start, end int32, offset int64) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("path is required")
	}
	if start < 0 || end < 0 || offset < 0 || (end > 0 && end < start) || (start > 0 && offset > 0) {
		return fmt.Errorf("invalid memory range: use non-negative line numbers or a continuation offset; end_line must not precede start_line")
	}
	return nil
}

func validateMemoryContent(content string) error {
	if !utf8.ValidString(content) {
		return fmt.Errorf("memory content must be UTF-8")
	}
	if len(content) > memory.MaxDocumentBytes {
		return fmt.Errorf("memory content exceeds %d bytes", memory.MaxDocumentBytes)
	}
	return nil
}

func memoryDocumentView(doc *pb.MemoryDocument) map[string]any {
	return map[string]any{
		"path": doc.GetPath(), "content": doc.GetContent(), "revision": doc.GetRevision(),
		"exists": doc.GetExists(), "start_line": doc.GetStartLine(), "end_line": doc.GetEndLine(),
		"truncated": doc.GetTruncated(), "next_offset": doc.GetNextOffset(),
	}
}

func memorySearchView(resp *pb.MemorySearchResponse) map[string]any {
	matches := make([]map[string]any, 0, len(resp.GetMatches()))
	for _, match := range resp.GetMatches() {
		matches = append(matches, map[string]any{
			"path": match.GetPath(), "snippet": match.GetSnippet(), "revision": match.GetRevision(),
			"line": match.GetLine(), "truncated": match.GetTruncated(),
		})
	}
	return map[string]any{"matches": matches, "incomplete": resp.GetIncomplete(), "truncated": resp.GetTruncated(), "scanned_files": resp.GetScannedFiles()}
}

func newMemoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "memory", Short: "Manage shared Markdown memory through the Hub",
		Long: "Search, read, conditionally update and forget Markdown memory in the scope selected by the Hub. No Agent Profile is required. Commands return JSON except export, which writes one complete document to standard output.",
	}
	cmd.AddCommand(newMemorySearchCmd(), newMemoryShowCmd(), newMemoryUpsertCmd(), newMemoryForgetCmd(), newMemoryExportCmd())
	return cmd
}

func newMemorySearchCmd() *cobra.Command {
	var prefix string
	var limit int32
	cmd := &cobra.Command{
		Use: "search <keywords>", Short: "Search memory using literal keywords", Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")
			if strings.TrimSpace(query) == "" || limit < 0 || limit > 20 {
				return fmt.Errorf("query is required and limit must be between 0 and 20")
			}
			client, closeFn, binding, err := openMemoryClient()
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := client.MemorySearch(cmd.Context(), &pb.MemorySearchRequest{Query: query, PathPrefix: prefix, Limit: limit, Binding: binding})
			if err != nil {
				return err
			}
			if resp == nil {
				return fmt.Errorf("hub returned an empty memory search response")
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(memorySearchView(resp))
		},
	}
	cmd.Flags().StringVar(&prefix, "path-prefix", "", "Restrict search to a relative memory path prefix")
	cmd.Flags().Int32Var(&limit, "limit", 5, "Maximum matching documents (1-20; 0 uses the server default)")
	return supportsJSONOutput(cmd)
}

func newMemoryShowCmd() *cobra.Command {
	var start, end int32
	var offset int64
	cmd := &cobra.Command{
		Use: "show <path>", Short: "Read a bounded memory document or line range", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateMemoryRead(args[0], start, end, offset); err != nil {
				return err
			}
			client, closeFn, binding, err := openMemoryClient()
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := client.MemoryGet(cmd.Context(), &pb.MemoryGetRequest{Path: args[0], StartLine: start, EndLine: end, Offset: offset, Binding: binding})
			if err != nil {
				return err
			}
			if resp == nil {
				return fmt.Errorf("hub returned an empty memory document response")
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(memoryDocumentView(resp))
		},
	}
	cmd.Flags().Int32Var(&start, "start-line", 0, "First line to read (1-based; 0 starts at the beginning)")
	cmd.Flags().Int32Var(&end, "end-line", 0, "Last line to read, inclusive (0 reads to the response limit)")
	cmd.Flags().Int64Var(&offset, "offset", 0, "Byte offset returned as next_offset by the previous response")
	return supportsJSONOutput(cmd)
}

func newMemoryUpsertCmd() *cobra.Command {
	var file, revision string
	cmd := &cobra.Command{
		Use: "upsert <path>", Short: "Conditionally save a UTF-8 Markdown memory document", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateMemoryRevision(revision); err != nil {
				return err
			}
			reader := cmd.InOrStdin()
			if file != "" && file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()
				reader = f
			}
			content, err := io.ReadAll(io.LimitReader(reader, memory.MaxDocumentBytes+1))
			if err != nil {
				return err
			}
			if err := validateMemoryContent(string(content)); err != nil {
				return err
			}
			client, closeFn, binding, err := openMemoryClient()
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := client.MemoryUpsert(cmd.Context(), &pb.MemoryUpsertRequest{Path: args[0], Content: string(content), ExpectedRevision: revision, Binding: binding})
			if err != nil {
				return err
			}
			if resp == nil {
				return fmt.Errorf("hub returned an empty memory write response")
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"path": resp.GetPath(), "revision": resp.GetRevision()})
		},
	}
	cmd.Flags().StringVar(&file, "file", "-", "Read document content from this file (- reads standard input)")
	cmd.Flags().StringVar(&revision, "expected-revision", "", "Previously read SHA-256 revision, or absent for creation")
	_ = cmd.MarkFlagRequired("expected-revision")
	return supportsJSONOutput(cmd)
}

func newMemoryForgetCmd() *cobra.Command {
	var revision string
	cmd := &cobra.Command{
		Use: "forget <path>", Short: "Conditionally delete a memory document", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateMemoryRevision(revision); err != nil {
				return err
			}
			client, closeFn, binding, err := openMemoryClient()
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := client.MemoryForget(cmd.Context(), &pb.MemoryForgetRequest{Path: args[0], ExpectedRevision: revision, Binding: binding})
			if err != nil {
				return err
			}
			if resp == nil {
				return fmt.Errorf("hub returned an empty memory forget response")
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"path": resp.GetPath(), "already_absent": resp.GetAlreadyAbsent()})
		},
	}
	cmd.Flags().StringVar(&revision, "expected-revision", "", "Previously read SHA-256 revision, or absent if already missing")
	_ = cmd.MarkFlagRequired("expected-revision")
	return supportsJSONOutput(cmd)
}

func newMemoryExportCmd() *cobra.Command {
	return &cobra.Command{
		Use: "export <path>", Short: "Export one complete memory document to standard output", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, closeFn, binding, err := openMemoryClient()
			if err != nil {
				return err
			}
			defer closeFn()
			return exportMemoryDocument(cmd.Context(), client, binding, args[0], cmd.OutOrStdout())
		},
	}
}

func exportMemoryDocument(ctx context.Context, client pb.AmuxdClient, binding, path string, out io.Writer) error {
	var content bytes.Buffer
	var revision string
	var offset int64
	for {
		doc, err := client.MemoryGet(ctx, &pb.MemoryGetRequest{Path: path, Offset: offset, Binding: binding})
		if err != nil {
			return err
		}
		if doc == nil {
			return fmt.Errorf("hub returned an empty memory document response")
		}
		if !doc.GetExists() {
			return fmt.Errorf("memory document %q does not exist", path)
		}
		if revision != "" && doc.GetRevision() != revision {
			return fmt.Errorf("memory document changed during export; retry export")
		}
		if doc.GetRevision() == "" {
			return fmt.Errorf("hub returned a memory document without a revision")
		}
		revision = doc.GetRevision()
		if content.Len()+len(doc.GetContent()) > memory.MaxDocumentBytes {
			return fmt.Errorf("memory export exceeds document size limit")
		}
		content.WriteString(doc.GetContent())
		if !doc.GetTruncated() {
			break
		}
		if doc.GetNextOffset() <= offset || doc.GetNextOffset() != int64(content.Len()) {
			return fmt.Errorf("hub returned an invalid memory continuation offset")
		}
		offset = doc.GetNextOffset()
	}
	// Hold at most one bounded document; failed revision checks produce no mixed
	// output that could accidentally be used as a replacement document.
	_, err := out.Write(content.Bytes())
	return err
}
