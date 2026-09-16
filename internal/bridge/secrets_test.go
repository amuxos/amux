package bridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/bridge/threadcontext"
)

func TestProfileSecretsPathUsesDefaultProfileDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ProfileSecretsPath(DefaultProfileName)
	if err != nil {
		t.Fatalf("ProfileSecretsPath: %v", err)
	}
	want := filepath.Join(home, ".amux", "secrets", "feishu", "default.json")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestDefaultPathsUseAMUXHome(t *testing.T) {
	amuxHome := filepath.Join(t.TempDir(), "amux-home")
	t.Setenv("AMUX_HOME", amuxHome)

	secrets, err := ProfileSecretsPath(DefaultProfileName)
	if err != nil {
		t.Fatalf("ProfileSecretsPath: %v", err)
	}
	for _, got := range []string{
		DefaultSecretsDir(),
		secrets,
		DefaultDBPath(),
		threadcontext.DefaultDBPath(),
	} {
		if !strings.HasPrefix(got, amuxHome+string(os.PathSeparator)) {
			t.Fatalf("path %q is not under AMUX_HOME %q", got, amuxHome)
		}
	}
}

func TestProfileSecretsPathRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{"", " planner ", "../planner", "team/planner", "planner.json", "planner disabled"} {
		if _, err := ProfileSecretsPath(name); err == nil {
			t.Fatalf("ProfileSecretsPath(%q) accepted unsafe profile", name)
		}
	}
}

func TestProfileSecretsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := &Secrets{AppId: "cli_planner", AppSecret: "secret", OpenId: "ou_owner", BotName: "planner"}
	if err := SaveProfileSecrets("planner", want); err != nil {
		t.Fatalf("SaveProfileSecrets: %v", err)
	}
	got, err := LoadProfileSecrets("planner")
	if err != nil {
		t.Fatalf("LoadProfileSecrets: %v", err)
	}
	if got.AppId != want.AppId || got.AppSecret != want.AppSecret || got.OpenId != want.OpenId || got.BotName != want.BotName {
		t.Fatalf("secrets = %+v, want %+v", got, want)
	}
}

func TestListSecretProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, profile := range []string{"coder", "default", "planner"} {
		if err := SaveProfileSecrets(profile, &Secrets{AppId: "cli_" + profile, AppSecret: "secret"}); err != nil {
			t.Fatalf("SaveProfileSecrets(%s): %v", profile, err)
		}
	}
	disabled, err := ProfileSecretsPath("disabled")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(disabled+".disabled", []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := ListSecretProfiles()
	if err != nil {
		t.Fatalf("ListSecretProfiles: %v", err)
	}
	want := []string{"coder", "default", "planner"}
	if len(got) != len(want) {
		t.Fatalf("profiles = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("profiles = %v, want %v", got, want)
		}
	}
}

func TestBridgeIdFromAppId(t *testing.T) {
	got := BridgeIdFromAppId(" cli_planner ")
	if got != "feishu:cli_planner" {
		t.Fatalf("bridge id = %q, want feishu:cli_planner", got)
	}
	if BridgeIdFromAppId("") != "" {
		t.Fatalf("empty app id should produce empty bridge id")
	}
}

func TestCarrySecretsForApp(t *testing.T) {
	existing := &Secrets{
		AppId:        "cli_old",
		AppSecret:    "sec_old",
		OpenId:       "ou_owner_old",
		NotifyChatId: "oc_notify",
		BotName:      "old-bot",
		BotOpenId:    "ou_bot_old",
	}

	// Same app: rerun keeps owner and bot identity.
	same := CarrySecretsForApp(existing, "cli_old")
	if same.OpenId != "ou_owner_old" || same.BotName != "old-bot" || same.BotOpenId != "ou_bot_old" {
		t.Fatalf("same-app carry lost identity fields: %+v", same)
	}
	if same.NotifyChatId != "oc_notify" || same.AppId != "cli_old" {
		t.Fatalf("same-app carry = %+v", same)
	}

	// App changed: open_id/bot identity are scoped to the old app and must be
	// dropped so owner/bot identity re-establish under the new app.
	changed := CarrySecretsForApp(existing, "cli_new")
	if changed.OpenId != "" || changed.BotName != "" || changed.BotOpenId != "" {
		t.Fatalf("app change must drop app-scoped identity, got %+v", changed)
	}
	if changed.NotifyChatId != "oc_notify" {
		t.Fatalf("notify chat is tenant-scoped and should survive, got %q", changed.NotifyChatId)
	}
	if changed.AppId != "cli_new" || changed.AppSecret != "" {
		t.Fatalf("app change carry = %+v", changed)
	}

	// Nil existing: fresh secrets with just the app id.
	fresh := CarrySecretsForApp(nil, "cli_new")
	if fresh.AppId != "cli_new" || fresh.OpenId != "" || fresh.NotifyChatId != "" {
		t.Fatalf("nil existing carry = %+v", fresh)
	}

	// No recorded app_id: provenance unknown, v0.25 "rerun keeps owner" applies.
	legacy := CarrySecretsForApp(&Secrets{OpenId: "ou_owner", BotName: "b", BotOpenId: "ou_b"}, "cli_new")
	if legacy.OpenId != "ou_owner" || legacy.BotName != "b" || legacy.BotOpenId != "ou_b" {
		t.Fatalf("empty old app_id must keep identity fields, got %+v", legacy)
	}
}
