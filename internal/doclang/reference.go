// Package doclang localizes generated documentation without changing runtime
// command metadata or MCP schemas.
package doclang

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

//go:embed translations.json
var catalogJSON []byte

type translation struct {
	English string `json:"en"`
	Chinese string `json:"zh-CN"`
}

var catalog = func() map[string]translation {
	var entries map[string]translation
	if err := json.Unmarshal(catalogJSON, &entries); err != nil {
		panic(err)
	}
	return entries
}()

var schemaDescription = regexp.MustCompile(`^(\s*"description": )(".*")(,?)$`)
var markdownLink = regexp.MustCompile(`\]\(([^)#]+\.md)(#[^)]*)?\)`)
var codeFence = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")

// Reference preserves code examples, identifiers, and schema validation fields.
// Only prose (including JSON Schema descriptions) is translated. An empty
// language keeps the distribution's original renderer output. Public generation
// explicitly selects en or zh-CN and rejects missing translations.
func Reference(source []byte, language, name string) ([]byte, error) {
	if language == "" {
		return source, nil
	}
	if language != "en" && language != "zh-CN" {
		return nil, fmt.Errorf("unsupported documentation language %q (use en or zh-CN)", language)
	}
	missing := map[string]bool{}
	translate := func(text string) string {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" || codeSpan(trimmed) || !hasLetters(trimmed) {
			return text
		}
		entry, ok := catalog[trimmed]
		value := entry.English
		if language == "zh-CN" {
			value = entry.Chinese
		}
		if !ok || value == "" {
			missing[trimmed] = true
			return text
		}
		return strings.Replace(text, trimmed, value, 1)
	}
	lines := strings.Split(string(source), "\n")
	fence, fenceLanguage := "", ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := codeFence.FindStringSubmatch(line); match != nil {
			if fence == "" {
				fence, fenceLanguage = match[1], strings.TrimSpace(match[2])
			} else if match[1][0] == fence[0] && len(match[1]) >= len(fence) && strings.TrimSpace(match[2]) == "" {
				fence = ""
			}
			continue
		}
		if fence != "" {
			if fenceLanguage == "json" {
				if match := schemaDescription.FindStringSubmatch(line); match != nil {
					var description string
					if err := json.Unmarshal([]byte(match[2]), &description); err != nil {
						return nil, err
					}
					encoded, err := json.Marshal(translate(description))
					if err != nil {
						return nil, err
					}
					lines[i] = match[1] + string(encoded) + match[3]
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "<!--") || strings.HasPrefix(trimmed, "## ") && codeSpan(strings.TrimPrefix(trimmed, "## ")) {
			continue
		}
		if strings.HasPrefix(trimmed, "|") {
			cells := tableCells(line)
			for j := range cells {
				cells[j] = translate(cells[j])
			}
			lines[i] = strings.Join(cells, "|")
		} else {
			lines[i] = translate(line)
		}
		if language == "zh-CN" {
			lines[i] = markdownLink.ReplaceAllStringFunc(lines[i], func(link string) string {
				match := markdownLink.FindStringSubmatch(link)
				target, err := url.Parse(match[1])
				if err != nil || target.IsAbs() || target.Host != "" || strings.HasSuffix(target.Path, ".zh-CN.md") {
					return link
				}
				return "](" + strings.TrimSuffix(match[1], ".md") + ".zh-CN.md" + match[2] + ")"
			})
		}
	}
	if len(missing) > 0 {
		keys := make([]string, 0, len(missing))
		for key := range missing {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("missing %s documentation translations in internal/doclang/translations.json:\n%s", language, strings.Join(keys, "\n"))
	}
	for i, line := range lines {
		if strings.HasPrefix(line, "# ") {
			base := path.Base(name)
			lines[i] += "\n\n[English](" + base + ".md) | [简体中文](" + base + ".zh-CN.md)"
			break
		}
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func hasLetters(value string) bool {
	return strings.IndexFunc(value, unicode.IsLetter) >= 0
}

func codeSpan(value string) bool {
	if !strings.HasPrefix(value, "`") {
		return false
	}
	marker := value[:len(value)-len(strings.TrimLeft(value, "`"))]
	rest := strings.TrimPrefix(value, marker)
	return len(rest) >= len(marker) && strings.Index(rest, marker) == len(rest)-len(marker)
}

func tableCells(line string) []string {
	var cells []string
	start, escaped := 0, false
	for i, r := range line {
		if r == '|' && !escaped {
			cells = append(cells, line[start:i])
			start = i + 1
		}
		if r == '\\' {
			escaped = !escaped
		} else {
			escaped = false
		}
	}
	return append(cells, line[start:])
}
