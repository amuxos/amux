package store

import (
	"path/filepath"
	"testing"
)

func TestGoalGrantSurvivesReopenAndSessionDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions(id,name,agent_type,state,created_at,updated_at) VALUES('s1','one','codex','running',1,1)`); err != nil {
		t.Fatal(err)
	}
	const grant = `{"thread_id":"native","objective":"task","enabled":true}`
	if err := db.SaveSessionGoalGrant("s1", grant); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if value, err := db.SessionGoalGrant("s1"); err != nil || value != grant {
		t.Fatalf("grant=%q err=%v", value, err)
	}
	if err := db.DeleteSession("s1"); err != nil {
		t.Fatal(err)
	}
	if value, err := db.SessionGoalGrant("s1"); err != nil || value != "" {
		t.Fatalf("orphan grant=%q err=%v", value, err)
	}
}
