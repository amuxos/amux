package bridge

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/distribution"
)

func TestExtensionRegistrationLifecycle(t *testing.T) {
	var fresh extensionRegistry
	if err := fresh.register(Extension{}); err != nil {
		t.Fatal(err)
	}
	if err := fresh.register(Extension{}); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	var used extensionRegistry
	used.snapshot()
	if err := used.register(Extension{}); err == nil {
		t.Fatal("registration after first use accepted")
	}
}

func TestBridgeExtensionWiring(t *testing.T) {
	const childEnv = "AMUX_TEST_BRIDGE_EXTENSION_CHILD"
	if os.Getenv(childEnv) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBridgeExtensionWiring$")
		cmd.Env = append(os.Environ(), childEnv+"=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated extension test: %v\n%s", err, output)
		}
		return
	}

	if err := distribution.Register(distribution.Options{
		CodexCompatibleLabel: "Custom Agent", CodexCompatibleGroup: "Custom Controls",
		DelegationProtocol: "urn:custom:delegation", BrandFooterMarkdown: "Custom Footer",
	}); err != nil {
		t.Fatal(err)
	}
	if amuxExternalBotDelegationProtocolURL() != "urn:custom:delegation" || amuxBrandFooterMarkdown() != "Custom Footer" {
		t.Fatal("branding captured the defaults before registration")
	}
	activeHelp := command.Help(command.ScopeActiveTopic)
	var foundSkill bool
	for _, item := range activeHelp {
		if item.Command == "/skill" {
			foundSkill = strings.Contains(item.Description, "Custom Agent")
		}
	}
	if !foundSkill {
		t.Fatalf("active help omitted registered provider: %+v", activeHelp)
	}
	for _, item := range command.Help(command.ScopePrivate) {
		if item.Command == "/skill" && item.Section != "Custom Controls" {
			t.Fatalf("private help captured old group: %+v", item)
		}
	}

	if err := RegisterExtension(Extension{
		ActivityPossiblyQueued: func(agent string, activity *agentui.ActivityView) bool { return agent == "custom" && activity != nil },
		ProviderErrorNotice:    func(text string) string { return "notice:" + text },
		ReplyCardAgent:         func(agent string) bool { return agent == "custom" },
		AgentTypeName:          func(agent string) string { return "name:" + agent },
		CoordinationProfile:    func(ref string) bool { return ref == "custom/coordinator" },
		ConsoleSetup: func(_ context.Context, appID string, opts ConsoleOptions) ConsoleResult {
			return ConsoleResult{OK: true, Message: appID, SessionFile: opts.SessionFilePath}
		},
		DefaultProfileReplacements: map[string]string{"old": "new"},
	}); err != nil {
		t.Fatal(err)
	}
	if !distributionActivityPossiblyQueued("custom", &agentui.ActivityView{}) ||
		distributionProviderErrorNotice("expired") != "notice:expired" ||
		!distributionReplyCardAgent("custom") || distributionAgentTypeName("custom") != "name:custom" ||
		!distributionCoordinationProfile("custom/coordinator") {
		t.Fatal("provider hooks were not dispatched")
	}
	result := AutomateOpenPlatformSetup(context.Background(), "app", ConsoleOptions{SessionFilePath: "test-session"})
	if !result.OK || result.Message != "app" || result.SessionFile != "test-session" {
		t.Fatalf("console hook result = %+v", result)
	}
}

func TestDefaultProfileReplacementPolicyIsImmutableAndIdempotent(t *testing.T) {
	for _, replacements := range []map[string]string{
		{"": "current"}, {"old": ""}, {"old": "old"}, {"old": "middle", "middle": "current"},
	} {
		var registry extensionRegistry
		if err := registry.register(Extension{DefaultProfileReplacements: replacements}); err == nil {
			t.Fatalf("invalid replacements accepted: %v", replacements)
		}
	}
	replacements := map[string]string{"old": "current"}
	var registry extensionRegistry
	if err := registry.register(Extension{DefaultProfileReplacements: replacements}); err != nil {
		t.Fatal(err)
	}
	replacements["old"] = "changed"
	if got := registry.snapshot().DefaultProfileReplacements["old"]; got != "current" {
		t.Fatalf("caller mutated registered policy: %q", got)
	}
}
