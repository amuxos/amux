package bridge

import (
	"context"
	"database/sql"
	"errors"
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

	migrationFailure := errors.New("forced extension migration failure")
	failMigration := false
	if err := RegisterExtension(Extension{
		ActivityPossiblyQueued: func(agent string, activity *agentui.ActivityView) bool { return agent == "custom" && activity != nil },
		ProviderErrorNotice:    func(text string) string { return "notice:" + text },
		ReplyCardAgent:         func(agent string) bool { return agent == "custom" },
		AgentTypeName:          func(agent string) string { return "name:" + agent },
		CoordinationProfile:    func(ref string) bool { return ref == "custom/coordinator" },
		ConsoleSetup: func(_ context.Context, appID string, opts ConsoleOptions) ConsoleResult {
			return ConsoleResult{OK: true, Message: appID, SessionFile: opts.SessionFilePath}
		},
		MigrateDefaults: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE defaults SET value='new'`); err != nil {
				return err
			}
			if failMigration {
				return migrationFailure
			}
			return nil
		},
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
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE defaults (value TEXT); INSERT INTO defaults VALUES ('old')`); err != nil {
		t.Fatal(err)
	}
	bridgeDB := &bridgeDB{db: db}
	if err := bridgeDB.migrateDistributionDefaults(); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT value FROM defaults`).Scan(&got); err != nil || got != "new" {
		t.Fatalf("committed migration value = %q, error = %v", got, err)
	}
	if _, err := db.Exec(`UPDATE defaults SET value='old'`); err != nil {
		t.Fatal(err)
	}
	failMigration = true
	if err := bridgeDB.migrateDistributionDefaults(); !errors.Is(err, migrationFailure) {
		t.Fatalf("migration failure = %v", err)
	}
	if err := db.QueryRow(`SELECT value FROM defaults`).Scan(&got); err != nil || got != "old" {
		t.Fatalf("failed migration was not rolled back: value = %q, error = %v", got, err)
	}
}
