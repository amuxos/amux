package doclang_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/doclang"
)

func TestReferencePreservesExecutableContent(t *testing.T) {
	examples := strings.Join([]string{
		"````text",
		"```sh",
		"amux session run --profile example/agent",
		"```",
		"Untranslated example output",
		"````",
		"~~~sh",
		"echo 'literal text'",
		"~~~",
	}, "\n")
	source := []byte("# amux CLI 命令参考\n\n| 说明 | `--flag=a\\|b` |\n\n" + examples + "\n")
	original := bytes.Clone(source)
	for _, tc := range []struct {
		language, heading, cell string
	}{
		{"en", "# amux CLI Reference", "Description"},
		{"zh-CN", "# amux CLI 命令参考", "说明"},
	} {
		t.Run(tc.language, func(t *testing.T) {
			got, err := doclang.Reference(source, tc.language, "cli")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				tc.heading,
				"[English](cli.md) | [简体中文](cli.zh-CN.md)",
				"| " + tc.cell + " | `--flag=a\\|b` |",
				examples,
			} {
				if !bytes.Contains(got, []byte(want)) {
					t.Errorf("localized reference missing %q", want)
				}
			}
			if !bytes.Equal(source, original) {
				t.Fatal("localization modified the original renderer output")
			}
		})
	}
}

func TestReferenceRequiresExplicitCompleteLanguage(t *testing.T) {
	source := []byte("# Distribution-specific metadata\n\nNo catalog entry\n")
	got, err := doclang.Reference(source, "", "cli")
	if err != nil || !bytes.Equal(got, source) {
		t.Fatalf("default language must preserve distribution output: %q, %v", got, err)
	}
	for _, language := range []string{"en", "zh-CN"} {
		for _, text := range []string{string(source), "``x"} {
			got, err := doclang.Reference([]byte(text), language, "cli")
			if err == nil || got != nil || !strings.Contains(err.Error(), "missing "+language+" documentation translations") {
				t.Errorf("missing translation must reject output: %q, %v", got, err)
			}
		}
	}
	if _, err := doclang.Reference(source, "fr", "cli"); err == nil {
		t.Fatal("unsupported language accepted")
	}
}

func TestTextLooksUpCatalogEntries(t *testing.T) {
	if got, ok := doclang.Text("## 飞书实验功能", "en"); !ok || got != "## Experimental Feishu features" {
		t.Fatalf("en heading = %q, %v", got, ok)
	}
	if got, ok := doclang.Text(" 0 或 >=60 ", "zh-CN"); !ok || got != "0 或 >=60" {
		t.Fatalf("zh-CN = %q, %v", got, ok)
	}
	if _, ok := doclang.Text("not catalogued text", "en"); ok {
		t.Fatal("unknown text reported as translated")
	}
}
