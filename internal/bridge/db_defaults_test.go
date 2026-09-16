package bridge

import (
	"path/filepath"
	"testing"
)

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
