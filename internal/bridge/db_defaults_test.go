package bridge

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestDefaultProfileMigrationPreservesOtherValues(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE feishu_workspaces (default_agent TEXT, args TEXT)`,
		`CREATE TABLE feishu_profile_defaults (default_agent TEXT)`,
		`CREATE TABLE feishu_bot_profiles (default_agent_profile TEXT)`,
		`CREATE TABLE historical_sessions (agent TEXT)`,
		`INSERT INTO feishu_workspaces VALUES ('legacy', '--owner-choice'), ('builtin/legacy', ''), ('codex', ''), ('custom/legacy', '')`,
		`INSERT INTO feishu_profile_defaults VALUES ('legacy'), ('builtin/legacy'), ('claude')`,
		`INSERT INTO feishu_bot_profiles VALUES ('legacy'), ('builtin/legacy'), ('builtin/custom')`,
		`INSERT INTO historical_sessions VALUES ('legacy')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	previous := bridgeExtensions.snapshot().DefaultProfileReplacements
	bridgeExtensions.mu.Lock()
	bridgeExtensions.extension.DefaultProfileReplacements = map[string]string{"legacy": "builtin/current", "builtin/legacy": "builtin/current"}
	bridgeExtensions.mu.Unlock()
	t.Cleanup(func() {
		bridgeExtensions.mu.Lock()
		bridgeExtensions.extension.DefaultProfileReplacements = previous
		bridgeExtensions.mu.Unlock()
	})
	store := &bridgeDB{db: db}
	for i := 0; i < 2; i++ {
		if err := store.migrateDistributionDefaults(); err != nil {
			t.Fatal(err)
		}
	}

	for _, query := range []string{
		`SELECT count(*) FROM feishu_workspaces WHERE default_agent='builtin/current'`,
		`SELECT count(*) FROM feishu_profile_defaults WHERE default_agent='builtin/current'`,
		`SELECT count(*) FROM feishu_bot_profiles WHERE default_agent_profile='builtin/current'`,
	} {
		var count int
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 2 {
			t.Fatalf("migration count = %d, error = %v", count, err)
		}
	}
	for query, want := range map[string]string{
		`SELECT args FROM feishu_workspaces WHERE args != ''`:                                                "--owner-choice",
		`SELECT agent FROM historical_sessions`:                                                              "legacy",
		`SELECT default_agent FROM feishu_workspaces WHERE default_agent='custom/legacy'`:                    "custom/legacy",
		`SELECT default_agent FROM feishu_profile_defaults WHERE default_agent='claude'`:                     "claude",
		`SELECT default_agent_profile FROM feishu_bot_profiles WHERE default_agent_profile='builtin/custom'`: "builtin/custom",
	} {
		var got string
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("preserved value = %q, want %q, error = %v", got, want, err)
		}
	}
}

func TestPublicWorkspaceDefaultsUseCodex(t *testing.T) {
	loadBridgeTestConfig(t, "")
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	workspace, err := db.GetWorkspace("feishu:example", "example-chat")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.DefaultAgent != "builtin/codex" {
		t.Fatalf("default agent = %q", workspace.DefaultAgent)
	}
	if _, err := db.db.Exec(`INSERT INTO feishu_workspaces (bridge_id, chat_id) VALUES (?, ?)`, "feishu:example", "sql-default"); err != nil {
		t.Fatal(err)
	}
	var agent string
	if err := db.db.QueryRow(`SELECT default_agent FROM feishu_workspaces WHERE chat_id = ?`, "sql-default").Scan(&agent); err != nil {
		t.Fatal(err)
	}
	if agent != "builtin/codex" {
		t.Fatalf("SQL default agent = %q", agent)
	}
}

func TestDefaultProfileMigrationRollsBackAllTables(t *testing.T) {
	previous := bridgeExtensions.snapshot().DefaultProfileReplacements
	bridgeExtensions.mu.Lock()
	bridgeExtensions.extension.DefaultProfileReplacements = map[string]string{"legacy": "current"}
	bridgeExtensions.mu.Unlock()
	t.Cleanup(func() {
		bridgeExtensions.mu.Lock()
		bridgeExtensions.extension.DefaultProfileReplacements = previous
		bridgeExtensions.mu.Unlock()
	})
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The last table is deliberately absent: failure must roll back earlier writes.
	if _, err := db.Exec(`CREATE TABLE feishu_workspaces (default_agent TEXT);
 INSERT INTO feishu_workspaces VALUES ('legacy');
 CREATE TABLE feishu_profile_defaults (default_agent TEXT);
 INSERT INTO feishu_profile_defaults VALUES ('legacy');`); err != nil {
		t.Fatal(err)
	}
	if err := (&bridgeDB{db: db}).migrateDistributionDefaults(); err == nil {
		t.Fatal("missing table was accepted")
	}
	for _, query := range []string{`SELECT default_agent FROM feishu_workspaces`, `SELECT default_agent FROM feishu_profile_defaults`} {
		var got string
		if err := db.QueryRow(query).Scan(&got); err != nil || got != "legacy" {
			t.Fatalf("migration was not atomic: got %q, error %v", got, err)
		}
	}
}
