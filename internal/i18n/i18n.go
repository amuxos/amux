// Package i18n renders product-owned messages independently of their UI surface.
// User content, protocol values and agent output must be passed as arguments,
// never used as catalog keys or translated by matching their text.
package i18n

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type Locale string

const (
	English Locale = "en"
	Chinese Locale = "zh-CN"
)

// Normalize accepts language tags used by clients and configuration. Unsupported
// languages return an empty locale so Resolve can continue through its fallbacks.
func Normalize(value string) Locale {
	value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-"))
	switch {
	case value == "en" || strings.HasPrefix(value, "en-"):
		return English
	case value == "zh" || value == "zh-cn" || value == "zh-hans" || strings.HasPrefix(value, "zh-hans-"):
		return Chinese
	default:
		return ""
	}
}

// Resolve applies explicit preference, client language, then distribution default.
// English is the final fallback. No locale is stored in process-global state.
func Resolve(preference, client, fallback string) Locale {
	for _, value := range []string{preference, client, fallback} {
		if locale := Normalize(value); locale != "" {
			return locale
		}
	}
	return English
}

// Entry optionally supplies singular/plural forms. The first format argument
// must be a count when One is provided; Chinese normally needs only Other.
type Entry struct {
	Other string `json:"other"`
	One   string `json:"one,omitempty"`
}

type Catalog struct{ messages map[Locale]map[string]Entry }

// New verifies identical keys and format argument contracts before publishing an
// immutable catalog. English is the fallback catalog for unsupported languages.
func New(english, chinese []byte) (*Catalog, error) {
	c := &Catalog{messages: map[Locale]map[string]Entry{}}
	for locale, data := range map[Locale][]byte{English: english, Chinese: chinese} {
		var messages map[string]Entry
		if err := json.Unmarshal(data, &messages); err != nil {
			return nil, fmt.Errorf("i18n %s: %w", locale, err)
		}
		c.messages[locale] = messages
	}
	if len(c.messages[English]) != len(c.messages[Chinese]) {
		return nil, fmt.Errorf("i18n catalogs have different key counts")
	}
	for key, en := range c.messages[English] {
		zh, ok := c.messages[Chinese][key]
		if !ok || strings.TrimSpace(key) == "" || en.Other == "" || zh.Other == "" {
			return nil, fmt.Errorf("i18n missing message %q", key)
		}
		signature := formatSignature(en.Other)
		for _, form := range []string{en.One, zh.Other, zh.One} {
			if form != "" && !slices.Equal(signature, formatSignature(form)) {
				return nil, fmt.Errorf("i18n inconsistent arguments for %q", key)
			}
		}
	}
	return c, nil
}

var formatVerb = regexp.MustCompile(`%(?:\[[0-9]+\])?[+#\- 0]*(?:[0-9]+|\*)?(?:\.(?:[0-9]+|\*))?[a-zA-Z%]`)

func formatSignature(value string) []string {
	var verbs []string
	for _, verb := range formatVerb.FindAllString(value, -1) {
		if verb != "%%" {
			verbs = append(verbs, verb)
		}
	}
	return verbs
}

func (c *Catalog) Format(locale Locale, key string, args ...any) string {
	if c == nil {
		return key
	}
	messages, ok := c.messages[locale]
	if !ok {
		messages = c.messages[English]
	}
	entry, ok := messages[key]
	if !ok {
		return key
	}
	format := entry.Other
	if entry.One != "" && len(args) > 0 && isOne(args[0]) {
		format = entry.One
	}
	if len(args) == 0 {
		return format
	}
	resolved := make([]any, len(args))
	for i, arg := range args {
		if text, ok := arg.(Text); ok {
			resolved[i] = text.For(locale)
		} else {
			resolved[i] = arg
		}
	}
	return fmt.Sprintf(format, resolved...)
}
func isOne(value any) bool {
	switch n := value.(type) {
	case int:
		return n == 1
	case int64:
		return n == 1
	case uint:
		return n == 1
	case uint64:
		return n == 1
	}
	return false
}

// Text keeps translations attached to product copy through composition. Literal
// arguments are identical in every language, even if they resemble catalog text.
type Text struct{ values map[Locale]string }

// Render composes localized presentation fragments without losing their locale.
// The callback must be pure and use catalog entries for product-owned prose.
func Render(render func(Locale) string) Text {
	return Text{values: map[Locale]string{English: render(English), Chinese: render(Chinese)}}
}

func Literal(value string) Text {
	return Text{values: map[Locale]string{English: value, Chinese: value}}
}
func (c *Catalog) Text(key string, args ...any) Text {
	return Text{values: map[Locale]string{English: c.Format(English, key, args...), Chinese: c.Format(Chinese, key, args...)}}
}
func (t Text) For(locale Locale) string {
	if value, ok := t.values[locale]; ok {
		return value
	}
	return t.values[English]
}
func (t Text) String() string { return t.For(English) }

// Join preserves translations when assembling presentation-only fragments.
func Join(texts ...Text) Text {
	values := map[Locale]string{}
	for _, locale := range []Locale{English, Chinese} {
		var b strings.Builder
		for _, text := range texts {
			b.WriteString(text.For(locale))
		}
		values[locale] = b.String()
	}
	return Text{values: values}
}

// Map applies a presentation-only transformation independently to every locale.
func (t Text) Map(transform func(string) string) Text {
	return Text{values: map[Locale]string{English: transform(t.For(English)), Chinese: transform(t.For(Chinese))}}
}
