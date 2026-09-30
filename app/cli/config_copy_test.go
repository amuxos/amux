package cli

import (
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/spf13/cobra"
)

func TestGroupWatchConfigErrorLanguages(t *testing.T) {
	for _, tc := range []struct {
		locale i18n.Locale
		want   string
	}{{i18n.English, "must be an integer"}, {i18n.Chinese, "必须为"}, {"fr", "must be an integer"}} {
		err := setConfigValueLocale(cliConfigFile{}, "bridge.feishu.group_watch.dedup.window_seconds", "x", tc.locale)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("locale %s: %v", tc.locale, err)
		}
	}
	for _, key := range []string{"bridge.feishu.group_watch.chat_sender_types.oc_test", "bridge.feishu.profiles.bot.group_watch.chat_sender_types.oc_test", "bridge.feishu.group_watch.dedup.enabled"} {
		en := setConfigValueLocale(cliConfigFile{}, key, "invalid", i18n.English)
		zh := setConfigValueLocale(cliConfigFile{}, key, "invalid", i18n.Chinese)
		if en == nil || zh == nil || en.Error() == zh.Error() || !strings.Contains(en.Error(), key) || !strings.Contains(zh.Error(), key) {
			t.Fatalf("key %s: en=%v zh=%v", key, en, zh)
		}
	}
}

func TestConfigCommandLocalePrecedence(t *testing.T) {
	cmd := &cobra.Command{}
	for _, key := range []string{"AMUX_LANGUAGE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(key, "")
	}
	t.Setenv("LANG", "zh_CN.UTF-8")
	if got := commandLocale(cmd); got != i18n.Chinese {
		t.Fatal(got)
	}
	t.Setenv("AMUX_LANGUAGE", "en")
	if got := commandLocale(cmd); got != i18n.English {
		t.Fatal(got)
	}
	t.Setenv("AMUX_LANGUAGE", "fr")
	t.Setenv("LANG", "C")
	if got := commandLocale(cmd); got != i18n.English {
		t.Fatal(got)
	}
}
