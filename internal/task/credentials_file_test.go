package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProfile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
}

// An empty profile name injects nothing (parity with NoCredentials) and never
// errors, so a task without a credential_profile still runs.
func TestFileCredentialsEmptyProfileIsNoop(t *testing.T) {
	fc := FileCredentials{Dir: t.TempDir()}
	mat, err := fc.Materialize(context.Background(), "", "claude")
	if err != nil {
		t.Fatalf("empty profile should not error: %v", err)
	}
	if len(mat.Env) != 0 || len(mat.Files) != 0 || mat.Kind != "" {
		t.Fatalf("empty profile should be empty material, got %+v", mat)
	}
}

func TestFileCredentialsMissingProfileErrors(t *testing.T) {
	fc := FileCredentials{Dir: t.TempDir()}
	_, err := fc.Materialize(context.Background(), "nope", "claude")
	if err == nil {
		t.Fatal("missing profile must error")
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error should name the profile, got %v", err)
	}
}

func TestFileCredentialsRejectsUnsafeProfileNames(t *testing.T) {
	fc := FileCredentials{Dir: t.TempDir()}
	for _, profile := range []string{"../outside", "team/provider", `team\provider`, "bad..name"} {
		t.Run(profile, func(t *testing.T) {
			_, err := fc.Materialize(context.Background(), profile, "claude")
			if err == nil {
				t.Fatal("unsafe profile name succeeded, want validation error")
			}
			if !strings.Contains(err.Error(), "invalid credential profile") {
				t.Fatalf("error = %v, want invalid credential profile", err)
			}
		})
	}
}

func TestFileCredentialsParsesEnvFilesKind(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "anthropic", `{
		"kind": "long-lived",
		"env": { "ANTHROPIC_API_KEY": "sk-secret" },
		"files": { ".config/creds.json": "{\"token\":\"t\"}" }
	}`)
	fc := FileCredentials{Dir: dir}
	mat, err := fc.Materialize(context.Background(), "anthropic", "claude")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if mat.Kind != "long-lived" {
		t.Fatalf("kind = %q", mat.Kind)
	}
	if len(mat.Env) != 1 || mat.Env[0] != "ANTHROPIC_API_KEY=sk-secret" {
		t.Fatalf("env = %v", mat.Env)
	}
	if mat.Files[".config/creds.json"] != `{"token":"t"}` {
		t.Fatalf("files = %v", mat.Files)
	}
}

func TestFileCredentialsMaterializeEnvByNameFindsGlobalEnv(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "push-sre", `{
		"kind": "short-lived",
		"env": { "DEMOTION_TOKEN": "demo-secret", "OTHER": "ignored" }
	}`)
	writeProfile(t, dir, "github", `{
		"kind": "short-lived",
		"env": { "GITHUB_TOKEN": "gh-secret" }
	}`)
	fc := FileCredentials{Dir: dir}

	mat, err := fc.MaterializeEnvByName(context.Background(), "coco", []string{"DEMOTION_TOKEN"})
	if err != nil {
		t.Fatalf("MaterializeEnvByName: %v", err)
	}
	if got, want := mat.Profiles, []string{"push-sre"}; !equalCredentialStringSlices(got, want) {
		t.Fatalf("profiles = %v, want %v", got, want)
	}
	if got, want := mat.Env, []string{"DEMOTION_TOKEN=demo-secret"}; !equalCredentialStringSlices(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
	if mat.Kind != "short-lived" {
		t.Fatalf("kind = %q, want short-lived", mat.Kind)
	}
}

func TestFileCredentialsMaterializeEnvByNameRequiresEnv(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "github", `{
		"kind": "short-lived",
		"env": { "GITHUB_TOKEN": "gh-secret" }
	}`)
	fc := FileCredentials{Dir: dir}

	_, err := fc.MaterializeEnvByName(context.Background(), "coco", []string{"DEMOTION_TOKEN"})
	if err == nil || !strings.Contains(err.Error(), "credential store does not define env DEMOTION_TOKEN") {
		t.Fatalf("error = %v, want missing env", err)
	}
}

func TestFileCredentialsMaterializeEnvByNameRejectsDuplicateEnv(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "a", `{
		"kind": "short-lived",
		"env": { "DEMOTION_TOKEN": "a-secret" }
	}`)
	writeProfile(t, dir, "b", `{
		"kind": "short-lived",
		"env": { "DEMOTION_TOKEN": "b-secret" }
	}`)
	fc := FileCredentials{Dir: dir}

	_, err := fc.MaterializeEnvByName(context.Background(), "coco", []string{"DEMOTION_TOKEN"})
	if err == nil || !strings.Contains(err.Error(), `credential env DEMOTION_TOKEN is provided by multiple profiles`) {
		t.Fatalf("error = %v, want duplicate env conflict", err)
	}
}

func TestFileCredentialsRejectsBadJSON(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "broken", `{not json`)
	fc := FileCredentials{Dir: dir}
	if _, err := fc.Materialize(context.Background(), "broken", "claude"); err == nil {
		t.Fatal("malformed profile must error")
	}
}

func TestFileCredentialsSetEnvPreservesProfileAndReturnsMetadata(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "openai-compat", `{
		"kind":"short-lived",
		"env":{"OTHER":"keep"},
		"files":{".config/tool/token":"content"}
	}`)
	fc := FileCredentials{Dir: dir}

	meta, err := fc.SetEnv("openai-compat", "GLM_API_KEY", "user-secret")
	if err != nil {
		t.Fatalf("SetEnv: %v", err)
	}
	if meta.Profile != "openai-compat" || meta.Kind != "short-lived" {
		t.Fatalf("metadata = %+v", meta)
	}
	if got, want := strings.Join(meta.EnvNames, ","), "GLM_API_KEY,OTHER"; got != want {
		t.Fatalf("env names = %q, want %q", got, want)
	}
	if got, want := strings.Join(meta.FileNames, ","), ".config/tool/token"; got != want {
		t.Fatalf("file names = %q, want %q", got, want)
	}
	mat, err := fc.Materialize(context.Background(), "openai-compat", "codex")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if got := strings.Join(mat.Env, "\n"); !strings.Contains(got, "GLM_API_KEY=user-secret") || !strings.Contains(got, "OTHER=keep") {
		t.Fatalf("materialized env = %q", got)
	}
	if info, err := os.Stat(meta.Path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode: info=%v err=%v", info, err)
	}
}

func TestFileCredentialsListMetadataDoesNotExposeValues(t *testing.T) {
	dir := t.TempDir()
	fc := FileCredentials{Dir: dir}
	if _, err := fc.SetEnv("z-profile", "Z_TOKEN", "z-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := fc.SetEnv("a-profile", "A_TOKEN", "a-secret"); err != nil {
		t.Fatal(err)
	}

	profiles, err := fc.ListMetadata()
	if err != nil {
		t.Fatalf("ListMetadata: %v", err)
	}
	if len(profiles) != 2 || profiles[0].Profile != "a-profile" || profiles[1].Profile != "z-profile" {
		t.Fatalf("profiles = %+v", profiles)
	}
	if got := fmt.Sprintf("%+v", profiles); strings.Contains(got, "a-secret") || strings.Contains(got, "z-secret") {
		t.Fatalf("metadata leaked secret: %s", got)
	}
}

func equalCredentialStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
