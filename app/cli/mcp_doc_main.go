//go:build mcp_doc

package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/amuxos/amux/internal/doclang"
	"github.com/amuxos/amux/internal/extensionstate"
)

func Main(buildVersion string) {
	extensionstate.Freeze()
	version = buildVersion
	output := flag.String("output", "docs/reference/mcp.md", "generated Markdown output path")
	check := flag.Bool("check", false, "fail when the checked-in document differs")
	language := flag.String("language", "", "documentation language: en or zh-CN (default: original distribution metadata)")
	flag.Parse()

	want, err := renderMCPReference(amuxTools())
	if err != nil {
		fmt.Fprintf(os.Stderr, "render generated MCP reference: %v\n", err)
		os.Exit(1)
	}
	want, localeErr := doclang.Reference(want, *language, "mcp")
	if localeErr != nil {
		fmt.Fprintln(os.Stderr, localeErr)
		os.Exit(1)
	}
	if *check {
		got, err := os.ReadFile(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read generated MCP reference: %v\n", err)
			os.Exit(1)
		}
		if !bytes.Equal(got, want) {
			fmt.Fprintf(os.Stderr, "%s is stale; run make mcp-doc\n", *output)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(*output, want, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write generated MCP reference: %v\n", err)
		os.Exit(1)
	}
}
