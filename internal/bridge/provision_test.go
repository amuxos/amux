package bridge

import (
	"bytes"
	"context"
	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRequiredFeishuAppManifestIsExactAndMinimal(t *testing.T) {
	want := FeishuAppManifest{
		Preset: false,
		TenantScopes: []string{
			"application:app_slash_command:read",
			"application:app_slash_command:write",
			"application:bot.basic_info:read",
			"cardkit:card:write",
			"docs:document.comment:create",
			"docs:document.comment:read",
			"docs:event:subscribe",
			"docx:document:readonly",
			"im:chat.members:read",
			"im:chat:read",
			"im:message.group_at_msg.include_bot:readonly",
			"im:message.group_at_msg:readonly",
			"im:message.group_msg",
			"im:message.p2p_msg:readonly",
			"im:message.reactions:write_only",
			"im:message:readonly",
			"im:message:send_as_bot",
		},
		UserScopes: nil,
		TenantEvents: []string{
			"application.bot.menu_v6",
			"drive.notice.comment_add_v1",
			"im.chat.access_event.bot_p2p_chat_entered_v1",
			"im.message.receive_v1",
		},
		UserEvents: nil,
		Callbacks:  []string{"card.action.trigger"},
	}
	if got := RequiredFeishuAppManifest(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RequiredFeishuAppManifest():\n got: %#v\nwant: %#v", got, want)
	}
}

func TestRegistrationAddonsForProvisioningUsesMinimalBase(t *testing.T) {
	configured := registrationAddonsForProvisioning(false)
	if configured.Preset == nil || *configured.Preset {
		t.Fatalf("configured preset = %v, want false", configured.Preset)
	}
	if got := configured.Scopes.Tenant; !reflect.DeepEqual(got, RequiredFeishuAppManifest().TenantScopes) {
		t.Fatalf("configured tenant scopes = %v", got)
	}
	if got := configured.Events.Items.Tenant; !reflect.DeepEqual(got, RequiredFeishuAppManifest().TenantEvents) {
		t.Fatalf("configured tenant events = %v", got)
	}
	if got := configured.Callbacks.Items; !reflect.DeepEqual(got, RequiredFeishuAppManifest().Callbacks) {
		t.Fatalf("configured callbacks = %v", got)
	}

	skipped := registrationAddonsForProvisioning(true)
	if skipped.Preset == nil || *skipped.Preset {
		t.Fatalf("skipped preset = %v, want false", skipped.Preset)
	}
	if len(skipped.Scopes.Tenant) != 0 || len(skipped.Scopes.User) != 0 ||
		len(skipped.Events.Items.Tenant) != 0 || len(skipped.Events.Items.User) != 0 ||
		len(skipped.Callbacks.Items) != 0 {
		t.Fatalf("skipped Addons must contain no increments: %#v", skipped)
	}
}

func TestProvisionFeishuAppPassesOfficialAddonsAndAllowsExistingApp(t *testing.T) {
	oldRegister := registerAppFn
	defer func() { registerAppFn = oldRegister }()

	var captured *registration.Options
	registerAppFn = func(_ context.Context, opts *registration.Options) (*registration.RegisterAppResult, error) {
		captured = opts
		if opts.AppPreset == nil || opts.AppPreset.Desc != "Powered by amux" {
			t.Fatalf("AppPreset = %#v, want default description Powered by amux", opts.AppPreset)
		}
		return &registration.RegisterAppResult{
			ClientID:     "cli_test",
			ClientSecret: "secret",
		}, nil
	}

	got, err := ProvisionFeishuApp(context.Background(), ProvisionOptions{
		QRWriter: io.Discard,
		AppID:    "cli_existing",
	})
	if err != nil {
		t.Fatalf("ProvisionFeishuApp: %v", err)
	}
	if got.AppID != "cli_test" || got.AppSecret != "secret" {
		t.Fatalf("result = %+v", got)
	}
	if captured == nil {
		t.Fatal("registration.RegisterApp was not called")
	}
	if captured.CreateOnly {
		t.Fatal("CreateOnly = true, want false so the confirmation page can select an existing app")
	}
	if captured.AppID != "cli_existing" {
		t.Fatalf("AppID = %q, want cli_existing", captured.AppID)
	}
	if captured.Addons == nil || captured.Addons.Preset == nil || *captured.Addons.Preset {
		t.Fatalf("Addons preset = %#v, want false", captured.Addons)
	}
	if !reflect.DeepEqual(captured.Addons.Scopes.Tenant, RequiredFeishuAppManifest().TenantScopes) ||
		!reflect.DeepEqual(captured.Addons.Events.Items.Tenant, RequiredFeishuAppManifest().TenantEvents) ||
		!reflect.DeepEqual(captured.Addons.Callbacks.Items, RequiredFeishuAppManifest().Callbacks) {
		t.Fatalf("registration Addons do not match manifest: %#v", captured.Addons)
	}

	if _, err := ProvisionFeishuApp(context.Background(), ProvisionOptions{QRWriter: io.Discard}); err != nil {
		t.Fatalf("ProvisionFeishuApp(new profile): %v", err)
	}
	if captured.CreateOnly {
		t.Fatal("new profile CreateOnly = true, want false")
	}
	if captured.AppID != "" {
		t.Fatalf("new profile AppID = %q, want empty so the confirmation page offers app selection", captured.AppID)
	}
	if _, err := ProvisionFeishuApp(context.Background(), ProvisionOptions{QRWriter: io.Discard, SkipAddons: true}); err != nil {
		t.Fatalf("ProvisionFeishuApp(skip addons): %v", err)
	}
}

func TestRenderQRCodeUsesCompactHalfBlocks(t *testing.T) {
	var out bytes.Buffer
	renderQRCode(&out, "https://example.com/amux-registration")
	rendered := strings.TrimSuffix(out.String(), "\n")

	if strings.Contains(rendered, "\x1b[") {
		t.Fatal("compact QR output contains full-cell ANSI background sequences")
	}
	lines := strings.Split(rendered, "\n")
	if len(lines) != 16 {
		t.Fatalf("compact QR uses %d terminal rows, want exactly 16", len(lines))
	}
	maxWidth := 0
	for _, line := range lines {
		if width := utf8.RuneCountInString(line); width > maxWidth {
			maxWidth = width
		}
	}
	if maxWidth != 31 {
		t.Fatalf("compact QR uses %d terminal columns, want exactly 31", maxWidth)
	}
}
