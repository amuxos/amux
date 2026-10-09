package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestAgentCanonicalCommandSurface(t *testing.T) {
	root := newRootCmd()
	found, _, err := root.Find([]string{"agent", "list"})
	if err != nil || found.Name() != "list" {
		t.Fatalf("find agent list: found=%v err=%v", found, err)
	}
	if len(found.Aliases) != 0 {
		t.Fatalf("agent list aliases = %v, want none", found.Aliases)
	}
	if err := found.Args(found, nil); err != nil {
		t.Fatalf("agent list rejected no args: %v", err)
	}
	if err := found.Args(found, []string{"extra"}); err == nil {
		t.Fatal("agent list accepted an extra arg")
	}
	if found, _, err := root.Find([]string{"agent", "ls"}); err == nil && found != nil && found.Name() == "ls" {
		t.Fatalf("agent ls should not be registered: found=%v", found)
	}

	credential, _, err := root.Find([]string{"agent", "credential"})
	if err != nil || credential.Name() != "credential" {
		t.Fatalf("missing canonical agent credential: found=%v err=%v", credential, err)
	}
	for _, child := range []string{"setup", "set", "show", "ls"} {
		found, _, err := root.Find([]string{"agent", "credential", child})
		if err != nil || found.Name() != child {
			t.Fatalf("missing agent credential %s: found=%v err=%v", child, found, err)
		}
	}

	found, _, err = root.Find([]string{"agent", "pack", "list"})
	if err != nil || found.Name() != "ls" {
		t.Fatalf("missing agent pack list alias: found=%v err=%v", found, err)
	}
	autosyncStatus, _, err := root.Find([]string{"agent", "pack", "autosync", "status"})
	if err != nil || autosyncStatus.Name() != "status" {
		t.Fatalf("missing agent pack autosync status: found=%v err=%v", autosyncStatus, err)
	}
	autosyncRun, _, err := root.Find([]string{"agent", "pack", "autosync", "run"})
	if err != nil || !autosyncRun.Hidden {
		t.Fatalf("agent pack autosync run hidden = %v, want true; err=%v", autosyncRun != nil && autosyncRun.Hidden, err)
	}

	var help bytes.Buffer
	root.SetOut(&help)
	root.SetErr(&help)
	root.SetArgs([]string{"agent", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("agent --help: %v", err)
	}
	for _, want := range []string{"  list", "  credential", "  pack"} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("agent help missing %q:\n%s", want, help.String())
		}
	}
	if strings.Contains(help.String(), "  ls") {
		t.Fatalf("agent help exposed hidden ls alias:\n%s", help.String())
	}

	root = newRootCmd()
	var completion bytes.Buffer
	root.SetOut(&completion)
	root.SetErr(&completion)
	root.SetArgs([]string{"__complete", "agent", ""})
	if err := root.Execute(); err != nil {
		t.Fatalf("complete agent: %v", err)
	}
	for _, want := range []string{"list\t", "credential\t", "pack\t"} {
		if !strings.Contains(completion.String(), want) {
			t.Fatalf("agent completion missing %q:\n%s", want, completion.String())
		}
	}
	if strings.Contains(completion.String(), "ls\t") {
		t.Fatalf("agent completion exposed hidden ls alias:\n%s", completion.String())
	}
}

func TestAgentListSupportsRootJSONFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"--json", "agent", "list"})
	if err := root.Execute(); err != nil {
		t.Fatalf("agent list --json: %v", err)
	}
	var got struct {
		Schema   string `json:"schema"`
		Profiles []struct {
			Ref       string `json:"ref"`
			AgentType string `json:"agent_type"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if strings.Contains(out.String(), ": null") {
		t.Fatalf("agent list JSON contains null collection: %s", out.String())
	}
	if got.Schema != "amux.agent.list.v1" || len(got.Profiles) == 0 {
		t.Fatalf("agent JSON = %#v", got)
	}
	if got.Profiles[0].Ref == "" || got.Profiles[0].AgentType != "" {
		t.Fatalf("first profile = %#v", got.Profiles[0])
	}
}

func TestAgentListAndShowIncludePackProfiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	repo := initTestProfilePackRepo(t)
	runGit(t, repo, "checkout", "-b", "stable")
	rev := gitRev(t, repo)
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent list: %v", err)
	}
	if !strings.Contains(out.String(), "team/coder") || !strings.Contains(out.String(), "Team coding profile") {
		t.Fatalf("agent list output missing pack profile:\n%s", out.String())
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "team/coder"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent show team/coder: %v", err)
	}
	for _, want := range []string{"ref: team/coder", "pinned_ref: team/coder@" + rev, "source: pack", "agents: AGENTS.md", "skills: amux-dev"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("agent show output missing %q:\n%s", want, out.String())
		}
	}
}

func TestAgentShowBuiltinProfile(t *testing.T) {
	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "builtin/claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent show: %v", err)
	}

	text := out.String()
	for _, want := range []string{"ref: builtin/claude", "source: builtin", "description: Claude Code"} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent show output missing %q:\n%s", want, text)
		}
	}
}

func TestAgentShowCredentialEnvRefsIncludeSetupHint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	repo := initTestProfilePackRepo(t)
	writeTestProfilePackManifestWithEnvRefs(t, repo)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "add credential env refs")
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "team/coder"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent show team/coder: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"env_refs: credential:github:GITHUB_TOKEN",
		"credential_ref: github:GITHUB_TOKEN",
		"credential: missing",
		"credential_setup: amux agent credential set github GITHUB_TOKEN",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent show output missing %q:\n%s", want, text)
		}
	}
}

func TestAgentShowProfileEnvsResolveGlobalCredentialSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := writeCredentialProfile("shared-demotion", cliCredentialFile{
		Kind: "long-lived",
		Env:  map[string]string{"DEMOTION_TOKEN": "secret-value"},
	}); err != nil {
		t.Fatalf("write shared credential profile: %v", err)
	}
	repo := initTestProfilePackRepo(t)
	writeTestProfilePackManifestWithEnvs(t, repo)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "add profile envs")
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "team/coder"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent show team/coder: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"envs: DEMOTION_TOKEN",
		"credential_env: DEMOTION_TOKEN",
		"credential_source: shared-demotion",
		"credential: configured",
		"credential_setup: amux agent credential set shared-demotion DEMOTION_TOKEN",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent show output missing %q:\n%s", want, text)
		}
	}
}

func TestAgentShowProfileEnvsOmitsMissingSetupWithoutProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	repo := initTestProfilePackRepo(t)
	writeTestProfilePackManifestWithEnvs(t, repo)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "add profile envs")
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "team/coder"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent show team/coder: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"credential_env: DEMOTION_TOKEN",
		"credential: missing (credential store does not define env DEMOTION_TOKEN)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent show output missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "credential_env: coder:DEMOTION_TOKEN") ||
		strings.Contains(text, "credential_setup") {
		t.Fatalf("agent show output guesses a credential profile for an unowned env:\n%s", text)
	}
}

func TestAgentDoctorBuiltinProfile(t *testing.T) {
	prependFakeAgentExecutable(t, "claude", "exit 0")
	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"doctor", "builtin/claude"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent doctor: %v", err)
	}

	text := out.String()
	for _, want := range []string{"Agent profile doctor", "[OK] profile", "[OK] adapter", "builtin/claude"} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestAgentDoctorSmokeUsesSelectedRuntimeAndModel(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	finalJSON, err := json.Marshal(task.TaskRun{ID: "ready-composite", Status: task.StatusSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	client := &captureTaskClient{streamEvents: []*pb.SubtaskEvent{}, getResp: &pb.GetSubtaskResponse{TaskJson: finalJSON}}
	original := newTaskClient
	newTaskClient = func() (pb.AmuxTaskClient, func() error, error) {
		return client, func() error { return nil }, nil
	}
	t.Cleanup(func() { newTaskClient = original })

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"doctor", "builtin/claude", "--runtime", "codex", "--model", "gpt-5.4", "--smoke", "--smoke-timeout", "30"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent doctor composite smoke: %v\n%s", err, out.String())
	}
	var spec task.Spec
	if err := json.Unmarshal(client.specJSON, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Agent != "builtin/claude" || spec.Runtime != "codex" || spec.Model != "gpt-5.4" {
		t.Fatalf("smoke spec = %+v, want Claude/Codex/gpt-5.4", spec)
	}
	if !strings.Contains(out.String(), "[OK] adapter: codex") {
		t.Fatalf("doctor output did not diagnose selected runtime:\n%s", out.String())
	}
}

func TestAgentDoctorCredentialEnvRefsIncludeSetupHint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	originalPath := os.Getenv("PATH")
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+originalPath)
	repo := initTestProfilePackRepo(t)
	writeTestProfilePackManifestWithEnvRefs(t, repo)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "add credential env refs")
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"doctor", "team/coder", "--runtime", "codex"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "credential profile github") {
		t.Fatalf("agent doctor team/coder error = %v, want missing credential failure", err)
	}

	text := out.String()
	for _, want := range []string{
		"[FAIL] credential_ref: missing env GITHUB_TOKEN in profile github",
		"[INFO] credential setup: amux agent credential set github GITHUB_TOKEN",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestAgentDoctorProfileEnvsResolveGlobalCredentialSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	prependFakeAgentExecutable(t, "codex", "exit 0")
	if err := writeCredentialProfile("shared-demotion", cliCredentialFile{
		Kind: "long-lived",
		Env:  map[string]string{"DEMOTION_TOKEN": "secret-value"},
	}); err != nil {
		t.Fatalf("write shared credential profile: %v", err)
	}
	repo := initTestProfilePackRepo(t)
	writeTestProfilePackManifestWithEnvs(t, repo)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "add profile envs")
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"doctor", "team/coder", "--runtime", "codex"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent doctor team/coder: %v", err)
	}

	text := out.String()
	for _, want := range []string{
		"[OK] credential_env: profile shared-demotion uniquely defines DEMOTION_TOKEN",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("agent doctor output missing %q:\n%s", want, text)
		}
	}
}

func TestAgentDoctorProfileEnvsReportsAmbiguousGlobalCredentialSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	prependFakeAgentExecutable(t, "codex", "exit 0")
	for _, profile := range []string{"demotion-a", "demotion-b"} {
		if err := writeCredentialProfile(profile, cliCredentialFile{
			Kind: "long-lived",
			Env:  map[string]string{"DEMOTION_TOKEN": "secret-value"},
		}); err != nil {
			t.Fatalf("write %s credential profile: %v", profile, err)
		}
	}
	repo := initTestProfilePackRepo(t)
	writeTestProfilePackManifestWithEnvs(t, repo)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "add profile envs")
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"doctor", "team/coder", "--runtime", "codex"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "required credential env DEMOTION_TOKEN is ambiguous") {
		t.Fatalf("agent doctor team/coder error = %v, want ambiguous credential failure", err)
	}

	text := out.String()
	if !strings.Contains(text, `credential env DEMOTION_TOKEN is provided by multiple profiles: "demotion-a" and "demotion-b"`) {
		t.Fatalf("agent doctor output missing ambiguous source error:\n%s", text)
	}
	if strings.Contains(text, "suggested credential setup") {
		t.Fatalf("agent doctor suggests adding a third source for an ambiguous env:\n%s", text)
	}
}

func TestCredentialEnvStateRejectsUnsafeProfileName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	amuxDir := filepath.Join(home, ".amux")
	if err := os.MkdirAll(amuxDir, 0700); err != nil {
		t.Fatalf("mkdir amux dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(amuxDir, "escape.json"), []byte(`{"kind":"long-lived","env":{"DEMOTION_TOKEN":"secret"}}`), 0600); err != nil {
		t.Fatalf("write escape credential: %v", err)
	}

	state, detail := credentialEnvState("../escape", "DEMOTION_TOKEN")
	if state != "invalid" {
		t.Fatalf("state = %q detail = %q, want invalid without reading outside secrets dir", state, detail)
	}
	if !strings.Contains(detail, "credential profile must match") {
		t.Fatalf("detail = %q, want validation error", detail)
	}
}

func TestAgentProviderCredentialStateUsesEffectiveCredentialProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	if err := writeCredentialProfile("shared-example", cliCredentialFile{
		Kind: "long-lived",
		Env:  map[string]string{"EXAMPLE_API_KEY": "secret-value"},
	}); err != nil {
		t.Fatalf("write credential profile: %v", err)
	}
	profile := agentprofile.Profile{Provider: &agentprofile.Provider{
		CredentialProfile: "shared-example",
		Platform:          "openai-compat",
		APIKeyEnv:         "EXAMPLE_API_KEY",
	}}
	state, detail := agentProviderCredentialState(profile)
	if state != "configured" || !strings.Contains(detail, "shared-example.json") {
		t.Fatalf("agentProviderCredentialState() = %q, %q, want shared-example configured", state, detail)
	}
}

func TestAgentDoctorRejectsUnknownProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")

	cmd := newAgentCmd()
	cmd.SetArgs([]string{"doctor", "team/missing"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown agent profile") {
		t.Fatalf("agent doctor error = %v, want unknown profile", err)
	}
}

func TestAgentDoctorAcceptsPackProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	prependFakeAgentExecutable(t, "codex", "exit 0")
	repo := initTestProfilePackRepo(t)
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"doctor", "team/coder", "--runtime", "codex"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent doctor team/coder: %v", err)
	}
	for _, want := range []string{"[OK] profile: team/coder", "[OK] adapter: codex"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("agent doctor output missing %q:\n%s", want, out.String())
		}
	}
}

func prependFakeAgentExecutable(t *testing.T, name, body string) string {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func TestAgentRenderWritesProjectionWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	root := t.TempDir()
	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"render", "builtin/claude", "--out", root})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent render: %v", err)
	}

	for _, name := range []string{"AGENTS.md", "CLAUDE.md", "amux-profile.json", "projection-report.md"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Fatalf("rendered file %s exists, want absent", name)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat rendered file %s: %v", name, err)
		}
	}
	if !strings.Contains(out.String(), "Rendered builtin/claude") {
		t.Fatalf("render output = %q, want rendered summary", out.String())
	}
	if strings.Contains(out.String(), "AGENTS.md") || strings.Contains(out.String(), "CLAUDE.md") {
		t.Fatalf("render output = %q, do not want agent files without agent-facing content", out.String())
	}
}

func TestAgentRenderPackProfileWritesProjectionWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	repo := initTestProfilePackRepo(t)
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")
	root := t.TempDir()

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"render", "team/coder", "--runtime", "codex", "--out", root})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent render team/coder: %v", err)
	}
	for _, name := range []string{"AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("expected rendered file %s: %v", name, err)
		}
	}
	for _, name := range []string{"amux-profile.json", "projection-report.md"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Fatalf("rendered file %s exists, want absent", name)
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat rendered file %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	if strings.Contains(string(data), "team/coder") || strings.Contains(string(data), "Team coding profile") || !strings.Contains(string(data), "You are the team coder.") {
		t.Fatalf("AGENTS.md should contain source agent instructions only:\n%s", string(data))
	}
}

func TestAgentCredentialPreservesLocalCredentialBehavior(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	cmd := newAgentCmd()
	cmd.SetIn(strings.NewReader("secret-value\n"))
	cmd.SetArgs([]string{"credential", "set", "provider", "PROVIDER_AK"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent credential set: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, ".amux", "secrets", "provider.json"))
	if err != nil {
		t.Fatalf("stat nested credential file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("nested credential file mode = %o, want 0600", got)
	}
}
