package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/doclang"
)

func main() {
	output := flag.String("output", "docs/reference/configuration.md", "generated Markdown output path")
	check := flag.Bool("check", false, "fail when the checked-in document differs")
	language := flag.String("language", "", "documentation language: en or zh-CN (default: original distribution metadata)")
	flag.Parse()

	want := config.RenderConfigurationReference()
	want, localeErr := doclang.Reference(want, *language, "configuration")
	if localeErr != nil {
		fmt.Fprintln(os.Stderr, localeErr)
		os.Exit(1)
	}
	if *check {
		got, err := os.ReadFile(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read generated config reference: %v\n", err)
			os.Exit(1)
		}
		if !bytes.Equal(got, want) {
			fmt.Fprintf(os.Stderr, "%s is stale; run make config-doc\n", *output)
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(*output, want, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write generated config reference: %v\n", err)
		os.Exit(1)
	}
}
