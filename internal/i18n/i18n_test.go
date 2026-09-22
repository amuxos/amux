package i18n

import (
	"strings"
	"testing"
)

func TestLocaleResolution(t *testing.T) {
	for _, test := range []struct {
		preference, client, fallback string
		want                         Locale
	}{
		{"en-US", "zh_cn", "zh-CN", English}, {"", "zh_cn", "en", Chinese}, {"", "fr", "zh-CN", Chinese}, {"xx", "ja", "xx", English}, {"", "zh-Hans-SG", "en", Chinese},
	} {
		if got := Resolve(test.preference, test.client, test.fallback); got != test.want {
			t.Fatalf("%+v: %s", test, got)
		}
	}
}
func TestMessagesPreserveArgumentsAndPluralize(t *testing.T) {
	c, err := New([]byte(`{"count":{"one":"%d runtime","other":"%d runtimes"},"label":{"other":"Name: %s"}}`), []byte(`{"count":{"other":"%d 个运行环境"},"label":{"other":"名称：%s"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Format(English, "count", 1); got != "1 runtime" {
		t.Fatal(got)
	}
	if got := c.Format(English, "count", 2); got != "2 runtimes" {
		t.Fatal(got)
	}
	if got := c.Format(Chinese, "count", 2); got != "2 个运行环境" {
		t.Fatal(got)
	}
	if got := c.Format("fr", "count", 2); got != "2 runtimes" {
		t.Fatal(got)
	}
	userText := "运行正常 /tmp/中文 %s <b>custom</b>"
	if got := c.Text("label", userText).For(English); got != "Name: "+userText {
		t.Fatal(got)
	}
	if got := c.Text("label", c.Text("count", 1)).For(Chinese); got != "名称：1 个运行环境" {
		t.Fatal(got)
	}
	original := c.Text("count", 2)
	changed := original.Map(strings.ToUpper)
	if changed.For(English) != "2 RUNTIMES" || original.For(English) != "2 runtimes" {
		t.Fatal("Map mutated source text")
	}
	if got := c.Format(English, "missing"); got != "missing" {
		t.Fatal(got)
	}
}
func TestCatalogRejectsMissingAndIncompatibleTranslations(t *testing.T) {
	for _, zh := range []string{`{}`, `{"b":{"other":"值 %s"}}`, `{"a":{"other":"值 %d"}}`, `{"a":{"other":""}}`} {
		if _, err := New([]byte(`{"a":{"other":"Value %s"}}`), []byte(zh)); err == nil {
			t.Fatal("accepted", zh)
		}
	}
	_, err := New([]byte(`{"a":{"other":"%d rows","one":"%s row"}}`), []byte(`{"a":{"other":"%d 行"}}`))
	if err == nil || !strings.Contains(err.Error(), "arguments") {
		t.Fatal(err)
	}
}

func TestRenderPreservesLocaleDuringComposition(t *testing.T) {
	c, err := New([]byte(`{"label":{"other":"Name: %s"}}`), []byte(`{"label":{"other":"名称：%s"}}`))
	if err != nil {
		t.Fatal(err)
	}
	literal := "中文 %s"
	text := Render(func(locale Locale) string { return "[" + c.Format(locale, "label", literal) + "]" })
	if text.For(English) != "[Name: 中文 %s]" || text.For(Chinese) != "[名称：中文 %s]" || text.For("unknown") != text.For(English) {
		t.Fatalf("incorrect composition: %+v", text)
	}
}
