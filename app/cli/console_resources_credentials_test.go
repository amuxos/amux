package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsoleCredentialsCRUDNeverReturnsValues(t *testing.T) {
	consoleTestOperator(t)
	backend := &consoleBackend{}
	run := func(action, data string) (string, error) {
		t.Helper()
		value, err := backend.executeResource(context.Background(), "credentials", action, json.RawMessage(data))
		if err != nil {
			return "", err
		}
		return consoleTestJSON(t, value), nil
	}
	raw, err := run("set_env", `{"profile":"team","name":"API_TOKEN","value":"env-secret-1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "env-secret") || !strings.Contains(raw, `"env_names":["API_TOKEN"]`) {
		t.Fatalf("set_env = %s", raw)
	}
	if _, err := run("set_env", `{"profile":"team","name":"API_TOKEN","value":"env-secret-2"}`); err != nil {
		t.Fatal(err)
	}
	raw, err = run("set_file", `{"profile":"team","name":".config/tool/auth.json","value":"{\"token\":\"file-secret\"}"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "file-secret") || !strings.Contains(raw, `"file_names":[".config/tool/auth.json"]`) {
		t.Fatalf("set_file = %s", raw)
	}
	data, err := os.ReadFile(credentialProfilePath("team"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "env-secret-2") || !strings.Contains(string(data), "file-secret") {
		t.Fatalf("store = %s", data)
	}
	snapshot, err := backend.snapshotResource(context.Background(), "credentials", nil)
	if err != nil {
		t.Fatal(err)
	}
	raw = consoleTestJSON(t, snapshot)
	if strings.Contains(raw, "secret-2") || !strings.Contains(raw, `"set_env"`) || !strings.Contains(raw, `"delete"`) {
		t.Fatalf("snapshot = %s", raw)
	}
	for name, request := range map[string][2]string{
		"bad env name":   {"set_env", `{"profile":"team","name":"1BAD","value":"x"}`},
		"empty value":    {"set_env", `{"profile":"team","name":"OK","value":""}`},
		"newline value":  {"set_env", `{"profile":"team","name":"OK","value":"a\nb"}`},
		"parent path":    {"set_file", `{"profile":"team","name":"../escape","value":"x"}`},
		"absolute path":  {"set_file", `{"profile":"team","name":"/etc/passwd","value":"x"}`},
		"bad profile":    {"set_env", `{"profile":"../team","name":"OK","value":"x"}`},
		"unknown field":  {"set_env", `{"profile":"team","name":"OK","value":"x","kind":"short"}`},
		"no confirm":     {"delete", `{"profile":"team"}`},
		"wrong confirm":  {"delete", `{"profile":"team","confirm":"other"}`},
		"unknown action": {"rotate", `{"profile":"team"}`},
	} {
		if _, err := run(request[0], request[1]); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	_, err = run("unset_env", `{"profile":"team","name":"MISSING"}`)
	consoleTestCode(t, err, "not_found")
	if _, err := run("unset_env", `{"profile":"team","name":"API_TOKEN"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run("unset_file", `{"profile":"team","name":".config/tool/auth.json"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := run("delete", `{"profile":"team","confirm":"team"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(credentialProfilePath("team")); !os.IsNotExist(err) {
		t.Fatalf("profile still exists: %v", err)
	}
	_, err = run("delete", `{"profile":"team","confirm":"team"}`)
	consoleTestCode(t, err, "not_found")
}

func TestConsoleCredentialsNeverOverwriteOtherStoreFiles(t *testing.T) {
	consoleTestOperator(t)
	path := filepath.Join(defaultAmuxDir(), "secrets", "bot.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"app_id":"cli_x","app_secret":"bot-secret"}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	backend := &consoleBackend{}
	for _, request := range [][2]string{
		{"set_env", `{"profile":"bot","name":"TOKEN","value":"x"}`},
		{"set_file", `{"profile":"bot","name":"auth","value":"x"}`},
		{"delete", `{"profile":"bot","confirm":"bot"}`},
	} {
		_, err := backend.executeResource(context.Background(), "credentials", request[0], json.RawMessage(request[1]))
		consoleTestCode(t, err, "credential_profile_conflict")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(original) {
		t.Fatalf("store file changed: %s %v", data, err)
	}
}
