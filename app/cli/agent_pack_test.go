package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
)

func TestAgentPackDevelopmentLeaseRegisterAndCleanup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_SESSION_ID", "session-owner")
	repo := initTestPackRepo(t, "v1")
	runGit(t, repo, "remote", "add", "origin", repo)
	worktree := filepath.Join(t.TempDir(), "task-worktree")
	branch := "amux-skill/task-a"
	runGit(t, repo, "worktree", "add", "-b", branch, worktree, "HEAD")
	registryPath := agentprofile.DefaultPackRegistryPath()
	if err := agentprofile.SavePackRegistry(registryPath, agentprofile.PackRegistry{
		Packs: map[string]agentprofile.Pack{
			"team": {
				Namespace:        "team",
				SourceKind:       agentprofile.PackSourceGit,
				GitURL:           repo,
				MaterializedPath: filepath.Join(home, ".amux", "agent-packs", "team", "repo"),
			},
		},
	}); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{
		"pack", "lease", "register", "team-task-a-dev",
		"--dev-of", "team",
		"--repo", repo,
		"--worktree", worktree,
		"--branch", branch,
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack lease register: %v", err)
	}
	leases, err := agentprofile.LoadDevelopmentLeaseRegistry(registryPath)
	if err != nil {
		t.Fatalf("LoadDevelopmentLeaseRegistry: %v", err)
	}
	if got := leases.Leases["team-task-a-dev"].OwnerSessionID; got != "session-owner" {
		t.Fatalf("lease owner = %q, want session-owner", got)
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "lease", "cleanup", "team-task-a-dev"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack lease cleanup: %v", err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree still exists after lease cleanup: %v", err)
	}
	showRefCmd := exec.Command("git", "-C", repo, "show-ref", "--verify", "refs/heads/"+branch)
	if output, err := showRefCmd.CombinedOutput(); err != nil {
		t.Fatalf("lease cleanup removed the task branch: %v\n%s", err, output)
	}
}

func TestAgentPackDevelopmentLeaseResolveReturnsFormalPackContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AMUX_HOME", home)
	registryPath := agentprofile.DefaultPackRegistryPath()
	if err := agentprofile.SavePackRegistry(registryPath, agentprofile.PackRegistry{
		Packs: map[string]agentprofile.Pack{
			"team": {
				Namespace:  "team",
				SourceKind: agentprofile.PackSourceGit,
				GitURL:     "git@example.com:org/team.git",
				Ref:        "stable",
			},
			"team-task-dev": {
				Namespace:     "team-task-dev",
				SourceKind:    agentprofile.PackSourceGit,
				GitURL:        "git@example.com:org/team.git",
				Ref:           "amux-skill/task",
				DevelopmentOf: "team",
				Temporary:     true,
			},
		},
	}); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	for _, test := range []struct {
		field string
		want  string
	}{
		{field: "formal-namespace", want: "team\n"},
		{field: "git-url", want: "git@example.com:org/team.git\n"},
		{field: "ref", want: "stable\n"},
	} {
		t.Run(test.field, func(t *testing.T) {
			cmd := newAgentCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"pack", "lease", "resolve", "team-task-dev/coder", "--field", test.field})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("agent pack lease resolve: %v", err)
			}
			if got := out.String(); got != test.want {
				t.Fatalf("resolve %s output = %q, want %q", test.field, got, test.want)
			}
		})
	}
}

func TestAgentPackAddListVersionsAndSyncGitRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "v1")
	runGit(t, repo, "checkout", "-b", "stable")
	rev1 := gitRev(t, repo)

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "add", "team", repo, "--ref", "stable"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack add: %v", err)
	}
	if !strings.Contains(out.String(), "Synced pack team") || !strings.Contains(out.String(), rev1[:12]) {
		t.Fatalf("pack add output = %q, want synced revision", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".amux", "agent-packs", "team", "repo", ".git")); err != nil {
		t.Fatalf("expected materialized repo git dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".amux", "agent-packs", "team", "source")); err == nil {
		t.Fatal("old materialized source dir exists, want absent")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat old source dir: %v", err)
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "ls"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack ls: %v", err)
	}
	if !strings.Contains(out.String(), "team") || !strings.Contains(out.String(), "stable") || !strings.Contains(out.String(), rev1[:12]) || !strings.Contains(out.String(), filepath.Join(home, ".amux", "agent-packs", "team", "repo")) {
		t.Fatalf("pack ls output = %q, want team stable revision and repo path", out.String())
	}

	writePackFile(t, repo, "v2")
	rev2 := gitRev(t, repo)
	if rev1 == rev2 {
		t.Fatal("test repo revision did not change")
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "sync", "team"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack sync: %v", err)
	}
	if !strings.Contains(out.String(), rev2[:12]) {
		t.Fatalf("pack sync output = %q, want second revision", out.String())
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "versions", "team"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack versions: %v", err)
	}
	if !strings.Contains(out.String(), rev1[:12]) || !strings.Contains(out.String(), rev2[:12]) {
		t.Fatalf("pack versions output = %q, want both revisions", out.String())
	}
}

func TestAgentPackAddDoesNotAutoRegisterDevPackWhenDevBranchExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "official-v1")
	baseBranch := gitBranch(t, repo)
	runGit(t, repo, "checkout", "-b", "amux-dev")
	writePackFile(t, repo, "dev-v1")
	runGit(t, repo, "checkout", baseBranch)
	officialRev := gitRev(t, repo)

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "add", "expert-agents", repo})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack add: %v", err)
	}
	if !strings.Contains(out.String(), "Synced pack expert-agents at "+officialRev[:12]) {
		t.Fatalf("pack add output = %q, want official synced revision", out.String())
	}
	if strings.Contains(out.String(), "expert-agents-dev") {
		t.Fatalf("pack add output = %q, want no implicit dev pack", out.String())
	}

	registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	official := registry.Packs["expert-agents"]
	if official.Namespace == "" {
		t.Fatalf("registry packs = %#v, want official pack", registry.Packs)
	}
	if _, ok := registry.Packs["expert-agents-dev"]; ok {
		t.Fatalf("registry packs = %#v, want no implicit dev pack", registry.Packs)
	}
	if !official.AutoSync || official.SyncInterval != "30s" {
		t.Fatalf("official auto_sync=%v interval=%q, want true/30s", official.AutoSync, official.SyncInterval)
	}
}

func TestAgentPackLegacyPairingFlagsAreRemoved(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "official-v1")
	for _, args := range [][]string{
		{"pack", "add", "team", repo, "--no-dev"},
		{"pack", "add", "team", repo, "--dev-ref", "preview"},
		{"pack", "sync", "team", "--no-dev"},
		{"pack", "watch", "team", "--no-dev"},
	} {
		cmd := newAgentCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("agent %v error = %v, want unknown flag", args, err)
		}
	}
}

func TestAgentPackAddAutoSyncFlagsAndListStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "official-v1")

	runAgentPackCmd(t, "add", "expert-agents", repo, "--no-auto-sync", "--sync-interval", "10s")

	registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	pack := registry.Packs["expert-agents"]
	if pack.AutoSync {
		t.Fatal("AutoSync = true after --no-auto-sync, want false")
	}
	if pack.SyncInterval != "10s" {
		t.Fatalf("SyncInterval = %q, want 10s", pack.SyncInterval)
	}

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "ls"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack ls: %v", err)
	}
	if !strings.Contains(out.String(), "AUTO_SYNC") || !strings.Contains(out.String(), "false") || !strings.Contains(out.String(), "10s") {
		t.Fatalf("pack ls output = %q, want auto sync status columns", out.String())
	}
}

func TestPrintAgentPacksCompactsLastError(t *testing.T) {
	var out bytes.Buffer
	printAgentPacks(&out, []agentprofile.Pack{{
		Namespace:        "team",
		SourceKind:       agentprofile.PackSourceGit,
		Ref:              "stable",
		CurrentRevision:  "1234567890abcdef",
		MaterializedPath: "/tmp/team",
		AutoSync:         true,
		SyncInterval:     "30s",
		LastError:        "first line\nsecond line " + strings.Repeat("x", 120),
	}})
	text := out.String()
	if strings.Contains(text, "first line\nsecond line") {
		t.Fatalf("pack ls output kept multiline error: %q", text)
	}
	if strings.Contains(text, strings.Repeat("x", 100)) {
		t.Fatalf("pack ls output did not truncate long error: %q", text)
	}
	if !strings.Contains(text, "first line second line") || !strings.Contains(text, "...") {
		t.Fatalf("pack ls output = %q, want compact truncated error", text)
	}
}

func TestAgentPackAutoSyncServiceShape(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	svc := agentPackAutoSyncService()
	if svc.Name != "agent-pack-autosync" {
		t.Fatalf("service name = %q, want agent-pack-autosync", svc.Name)
	}
	if svc.Binary != "amux" {
		t.Fatalf("service binary = %q, want amux", svc.Binary)
	}
	if got, want := strings.Join(svc.Args, " "), "agent pack autosync run"; got != want {
		t.Fatalf("service args = %q, want %q", got, want)
	}
	if got, want := svc.PIDPath, filepath.Join(home, ".amux", "agent-pack-autosync.pid"); got != want {
		t.Fatalf("PIDPath = %q, want %q", got, want)
	}
	if got, want := svc.LogPath, filepath.Join(home, ".amux", "agent-pack-autosync.log"); got != want {
		t.Fatalf("LogPath = %q, want %q", got, want)
	}
}

func TestRunAgentPackAutoSyncOnceSyncsDueAutoSyncPacksOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "team-v1")
	manualRepo := initTestPackRepo(t, "manual-v1")
	runAgentPackCmd(t, "add", "team", repo)
	runAgentPackCmd(t, "add", "manual", manualRepo, "--no-auto-sync")

	writePackFile(t, repo, "team-v2")
	teamRev2 := gitRev(t, repo)
	writePackFile(t, manualRepo, "manual-v2")
	manualRev2 := gitRev(t, manualRepo)

	var out bytes.Buffer
	if err := runAgentPackAutoSyncOnce(context.Background(), &out, defaultAgentPackRegistryPath()); err != nil {
		t.Fatalf("runAgentPackAutoSyncOnce: %v", err)
	}
	registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	if got := registry.Packs["team"].CurrentRevision; got != teamRev2 {
		t.Fatalf("team revision = %q, want %q", got, teamRev2)
	}
	if got := registry.Packs["manual"].CurrentRevision; got == manualRev2 {
		t.Fatalf("manual revision = %q, want unchanged because auto_sync=false", got)
	}
	if !strings.Contains(out.String(), "Synced pack team at "+teamRev2[:12]) {
		t.Fatalf("autosync output = %q, want team sync line", out.String())
	}
	if strings.Contains(out.String(), "manual") {
		t.Fatalf("autosync output = %q, want manual pack skipped", out.String())
	}
}

func TestRunAgentPackAutoSyncOnceBoundsEachPackSync(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	registryPath := defaultAgentPackRegistryPath()
	if err := agentprofile.SavePackRegistry(registryPath, agentprofile.PackRegistry{
		Packs: map[string]agentprofile.Pack{
			"team": {
				Namespace:    "team",
				SourceKind:   agentprofile.PackSourceGit,
				GitURL:       "ssh://example.invalid/team.git",
				AutoSync:     true,
				SyncInterval: "30s",
			},
		},
	}); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	oldTimeout := agentPackAutoSyncTimeout
	oldSync := syncPackForAutoSync
	t.Cleanup(func() {
		agentPackAutoSyncTimeout = oldTimeout
		syncPackForAutoSync = oldSync
	})
	agentPackAutoSyncTimeout = 20 * time.Millisecond
	syncCalls := 0
	syncPackForAutoSync = func(ctx context.Context, _, namespace string) (agentprofile.Pack, error) {
		syncCalls++
		if namespace != "team" {
			t.Fatalf("sync namespace = %q, want team", namespace)
		}
		<-ctx.Done()
		return agentprofile.Pack{}, ctx.Err()
	}

	started := time.Now()
	var out bytes.Buffer
	if err := runAgentPackAutoSyncOnce(context.Background(), &out, registryPath); err != nil {
		t.Fatalf("runAgentPackAutoSyncOnce: %v", err)
	}
	if syncCalls != 1 {
		t.Fatalf("sync calls = %d, want 1", syncCalls)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("auto sync elapsed = %s, want bounded Pack sync", elapsed)
	}
	if !strings.Contains(out.String(), "context deadline exceeded") {
		t.Fatalf("auto sync output = %q, want timeout error", out.String())
	}
}

func TestAgentPackAddSameRepoDevNamespaceRequiresExplicitParent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "official-v1")
	baseBranch := gitBranch(t, repo)
	runGit(t, repo, "checkout", "-b", "my-debug-branch")
	writePackFile(t, repo, "dev-v1")
	runGit(t, repo, "checkout", baseBranch)

	runAgentPackCmd(t, "add", "expert-agents", repo)
	cmd := newAgentCmd()
	cmd.SetArgs([]string{"pack", "add", "expert-agents-dev", repo, "--ref", "my-debug-branch"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `already registered as namespace "expert-agents"`) {
		t.Fatalf("implicit dev pack add error = %v, want duplicate source rejection", err)
	}
	runAgentPackCmd(t, "add", "expert-agents-dev", repo, "--ref", "my-debug-branch", "--dev-of", "expert-agents", "--no-auto-sync")

	registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	dev := registry.Packs["expert-agents-dev"]
	if dev.Ref != "my-debug-branch" {
		t.Fatalf("dev ref = %q, want explicit branch", dev.Ref)
	}
	if dev.DevelopmentOf != "expert-agents" {
		t.Fatalf("dev parent = %q, want expert-agents", dev.DevelopmentOf)
	}
	if got, want := readPackFile(t, dev.MaterializedPath), "dev-v1"; got != want {
		t.Fatalf("dev materialized file = %q, want %q", got, want)
	}
}

func TestAgentPackAddTemporaryDevelopmentPack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestProfilePackRepo(t)
	baseBranch := gitBranch(t, repo)
	taskBranch := "amux-skill/task-a"
	runGit(t, repo, "checkout", "-b", taskBranch)
	writeProfileDescription(t, repo, "Task A coding profile")
	runGit(t, repo, "checkout", baseBranch)

	runAgentPackCmd(t, "add", "team", repo)
	runAgentPackCmd(t,
		"add", "team-task-a-dev", repo,
		"--ref", taskBranch,
		"--dev-of", "team",
		"--temporary",
	)

	registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	temporary := registry.Packs["team-task-a-dev"]
	if temporary.DevelopmentOf != "team" || !temporary.Temporary {
		t.Fatalf("temporary development pack = %#v", temporary)
	}
	if temporary.AutoSync {
		t.Fatal("temporary development pack AutoSync = true, want false")
	}

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent list: %v", err)
	}
	if strings.Contains(out.String(), "team-task-a-dev/coder") {
		t.Fatalf("agent list output = %q, want temporary dev profile hidden", out.String())
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--dev"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent list --dev: %v", err)
	}
	if !strings.Contains(out.String(), "team-task-a-dev/coder") {
		t.Fatalf("agent list --dev output = %q, want temporary dev profile", out.String())
	}
}

func TestAgentPackDevNamespaceProfilesResolveOnlyWhenExplicit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestProfilePackRepo(t)
	baseBranch := gitBranch(t, repo)
	runGit(t, repo, "checkout", "-b", "preview")
	writeProfileDescription(t, repo, "Dev coding profile")
	runGit(t, repo, "checkout", baseBranch)

	runAgentPackCmd(t, "add", "expert-agents", repo)
	runAgentPackCmd(t, "add", "expert-agents-dev", repo, "--ref", "preview", "--dev-of", "expert-agents", "--no-auto-sync")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent list: %v", err)
	}
	if !strings.Contains(out.String(), "expert-agents/coder") {
		t.Fatalf("agent list output = %q, want official profile ref", out.String())
	}
	if strings.Contains(out.String(), "expert-agents-dev/coder") {
		t.Fatalf("agent list output = %q, want dev profile hidden by default", out.String())
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--all"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent list --all: %v", err)
	}
	if !strings.Contains(out.String(), "expert-agents/coder") || !strings.Contains(out.String(), "expert-agents-dev/coder") {
		t.Fatalf("agent list --all output = %q, want official and dev profile refs", out.String())
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--dev"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent list --dev: %v", err)
	}
	if strings.Contains(out.String(), "expert-agents/coder") || !strings.Contains(out.String(), "expert-agents-dev/coder") {
		t.Fatalf("agent list --dev output = %q, want only dev profile ref", out.String())
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "expert-agents/coder"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent show official: %v", err)
	}
	if !strings.Contains(out.String(), "description: Team coding profile") || strings.Contains(out.String(), "Dev coding profile") {
		t.Fatalf("official profile output = %q, want official description only", out.String())
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "expert-agents-dev/coder"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent show dev: %v", err)
	}
	if !strings.Contains(out.String(), "description: Dev coding profile") {
		t.Fatalf("dev profile output = %q, want dev description", out.String())
	}
}

func TestAgentListHidesSuffixOnlyDevNamespaceByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	formalRepo := initTestProfilePackRepo(t)
	repo := initTestProfilePackRepo(t)
	runAgentPackCmd(t, "add", "tools", formalRepo)
	runAgentPackCmd(t, "add", "tools-dev", repo)

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent list: %v", err)
	}
	if strings.Contains(out.String(), "tools-dev/coder") {
		t.Fatalf("agent list output = %q, want unpaired dev namespace hidden", out.String())
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--dev"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent list --dev: %v", err)
	}
	if !strings.Contains(out.String(), "tools-dev/coder") {
		t.Fatalf("agent list --dev output = %q, want unpaired dev namespace visible", out.String())
	}
}

func TestAgentPackSyncFormalAndExplicitDevelopmentPacksIndependently(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "official-v1")
	baseBranch := gitBranch(t, repo)
	runGit(t, repo, "checkout", "-b", "preview")
	writePackFile(t, repo, "dev-v1")
	runGit(t, repo, "checkout", baseBranch)
	runAgentPackCmd(t, "add", "expert-agents", repo)
	runAgentPackCmd(t, "add", "expert-agents-preview-dev", repo, "--ref", "preview", "--dev-of", "expert-agents", "--no-auto-sync")
	registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	devRev1 := registry.Packs["expert-agents-preview-dev"].CurrentRevision

	writePackFile(t, repo, "official-v2")
	officialRev2 := gitRev(t, repo)
	runGit(t, repo, "checkout", "preview")
	writePackFile(t, repo, "dev-v2")
	devRev2 := gitRev(t, repo)
	runGit(t, repo, "checkout", baseBranch)

	runAgentPackCmd(t, "sync", "expert-agents")
	registry, err = agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	if got, want := registry.Packs["expert-agents"].CurrentRevision, officialRev2; got != want {
		t.Fatalf("official revision = %q, want %q", got, want)
	}
	if got := registry.Packs["expert-agents-preview-dev"].CurrentRevision; got != devRev1 {
		t.Fatalf("dev revision after formal sync = %q, want unchanged %q", got, devRev1)
	}

	runAgentPackCmd(t, "sync", "expert-agents-preview-dev")
	registry, err = agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
	if err != nil {
		t.Fatalf("LoadPackRegistry after explicit dev sync: %v", err)
	}
	if got := registry.Packs["expert-agents-preview-dev"].CurrentRevision; got != devRev2 {
		t.Fatalf("dev revision after explicit sync = %q, want %q", got, devRev2)
	}
	if got := registry.Packs["expert-agents"].CurrentRevision; got != officialRev2 {
		t.Fatalf("official revision after explicit dev sync = %q, want %q", got, officialRev2)
	}
}

func TestAgentPackSyncNeverAutoRegistersDevPack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "official-v1")
	baseBranch := gitBranch(t, repo)
	runGit(t, repo, "checkout", "-b", "amux-dev")
	writePackFile(t, repo, "dev-v1")
	runGit(t, repo, "checkout", baseBranch)
	if _, err := agentprofile.AddGitPackWithOptions(context.Background(), defaultAgentPackRegistryPath(), "expert-agents", repo, agentprofile.AddGitPackOptions{
		AutoSync:     true,
		SyncInterval: "30s",
	}); err != nil {
		t.Fatalf("AddGitPackWithOptions official: %v", err)
	}

	for _, args := range [][]string{{"sync", "expert-agents"}, {"sync"}} {
		runAgentPackCmd(t, args...)
		registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
		if err != nil {
			t.Fatalf("LoadPackRegistry after %v: %v", args, err)
		}
		if _, ok := registry.Packs["expert-agents-dev"]; ok {
			t.Fatalf("registry packs after %v = %#v, want no implicit dev pack", args, registry.Packs)
		}
	}
}

func TestAgentPackSyncStartsAutoSyncForRegisteredPacks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "v1")
	if _, err := agentprofile.AddGitPackWithOptions(context.Background(), defaultAgentPackRegistryPath(), "team", repo, agentprofile.AddGitPackOptions{
		AutoSync:     true,
		SyncInterval: "30s",
	}); err != nil {
		t.Fatalf("AddGitPackWithOptions: %v", err)
	}

	starts := 0
	oldStart := startAgentPackAutoSyncService
	startAgentPackAutoSyncService = func() error {
		starts++
		return nil
	}
	t.Cleanup(func() {
		startAgentPackAutoSyncService = oldStart
	})

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "sync", "team"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack sync: %v", err)
	}
	if starts != 1 {
		t.Fatalf("auto sync starts = %d, want 1", starts)
	}
	if !strings.Contains(out.String(), "Agent pack auto sync started") {
		t.Fatalf("sync output = %q, want auto sync start message", out.String())
	}
}

func TestAgentPackWatchPerformsForegroundSyncCycle(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "v1")
	runAgentPackCmd(t, "add", "team", repo)

	writePackFile(t, repo, "v2")
	rev2 := gitRev(t, repo)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- watchAgentPack(ctx, &out, defaultAgentPackRegistryPath(), "team", time.Hour)
	}()

	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		registry, err := agentprofile.LoadPackRegistry(defaultAgentPackRegistryPath())
		if err != nil {
			t.Fatalf("LoadPackRegistry: %v", err)
		}
		if registry.Packs["team"].CurrentRevision == rev2 {
			cancel()
			if err := <-done; err != context.Canceled {
				t.Fatalf("watchAgentPack error = %v, want context.Canceled", err)
			}
			if !strings.Contains(out.String(), "Synced pack team at "+rev2[:12]) {
				t.Fatalf("watch output = %q, want synced revision", out.String())
			}
			return
		}
		select {
		case <-deadline:
			cancel()
			t.Fatalf("watch did not sync to %s; output=%q", rev2, out.String())
		case <-ticker.C:
		}
	}
}

func TestAgentPackRollbackChecksOutRecordedRevision(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "v1")
	runGit(t, repo, "checkout", "-b", "stable")
	rev1 := gitRev(t, repo)
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	writePackFile(t, repo, "v2")
	runAgentPackCmd(t, "sync", "team")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "rollback", "team", rev1})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack rollback: %v", err)
	}
	if !strings.Contains(out.String(), "Rolled back pack team") || !strings.Contains(out.String(), rev1[:12]) {
		t.Fatalf("rollback output = %q, want first revision", out.String())
	}

	data, err := os.ReadFile(filepath.Join(home, ".amux", "agent-packs", "team", "repo", "profiles", "coder.md"))
	if err != nil {
		t.Fatalf("read materialized file: %v", err)
	}
	if strings.TrimSpace(string(data)) != "v1" {
		t.Fatalf("materialized file = %q, want v1", string(data))
	}
}

func TestAgentPackRemoveDeletesRegistryEntryAndMaterializedFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "v1")
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")
	materializedRoot := filepath.Join(home, ".amux", "agent-packs", "team")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "remove", "team"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack remove: %v", err)
	}
	if !strings.Contains(out.String(), "Removed pack team") || !strings.Contains(out.String(), materializedRoot) {
		t.Fatalf("remove output = %q, want removed pack and path", out.String())
	}
	if _, err := os.Stat(materializedRoot); err == nil {
		t.Fatal("materialized pack root still exists, want removed")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat materialized root: %v", err)
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "ls"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack ls: %v", err)
	}
	if strings.Contains(out.String(), "team") {
		t.Fatalf("pack ls output = %q, want team removed", out.String())
	}
}

func TestAgentPackRemoveKeepFilesOnlyUnregistersPack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := initTestPackRepo(t, "v1")
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")
	materializedRepo := filepath.Join(home, ".amux", "agent-packs", "team", "repo", ".git")

	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "rm", "team", "--keep-files"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack rm --keep-files: %v", err)
	}
	if !strings.Contains(out.String(), "Removed pack team") || strings.Contains(out.String(), "Removed files") {
		t.Fatalf("remove output = %q, want unregister without removed files line", out.String())
	}
	if _, err := os.Stat(materializedRepo); err != nil {
		t.Fatalf("materialized repo was not kept: %v", err)
	}

	out.Reset()
	cmd = newAgentCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"pack", "ls"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack ls: %v", err)
	}
	if strings.Contains(out.String(), "team") {
		t.Fatalf("pack ls output = %q, want team unregistered", out.String())
	}
}

func initTestPackRepo(t *testing.T, value string) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "amux test")
	runGit(t, repo, "config", "user.email", "amux@example.com")
	writePackFile(t, repo, value)
	return repo
}

func initTestProfilePackRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init")
	runGit(t, repo, "config", "user.name", "amux test")
	runGit(t, repo, "config", "user.email", "amux@example.com")
	if err := os.MkdirAll(filepath.Join(repo, "profiles"), 0700); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "profiles", "coder.toml"), []byte(`name = "coder"
type = "codex"
model = "gpt-5-codex"
description = "Team coding profile"
agents = "AGENTS.md"
skills = ["amux-dev"]

[invocation]
args = ["--sandbox", "workspace-write"]
env = ["TEAM=amux"]

[placement.requires]
agent_types = ["codex"]
runtime_tools = ["git"]
`), 0600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Instructions\n\nYou are the team coder.\n"), 0600); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	skillDir := filepath.Join(repo, "skills", "amux-dev")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: amux-dev\ndescription: Drive amux dev workflows\n---\n\nUse this skill.\n"), 0600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	writeTestProfilePackManifest(t, repo, "Team coding profile", false)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "add coder profile")
	return repo
}

func writeTestProfilePackManifest(t *testing.T, repo, coderDescription string, includeExtra bool) {
	t.Helper()
	profiles := map[string]any{"coder": map[string]any{
		"namespace": "common", "description": coderDescription, "instructions": []string{"AGENTS.md"},
		"skills": []string{"amux-dev"}, "subagents": []string{},
	}}
	if includeExtra {
		profiles["example"] = map[string]any{"instructions": []string{"AGENTS.md"}}
	}
	data, err := json.Marshal(map[string]any{"schema_version": "v2", "pack_namespace": "team", "profiles": profiles})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "dist", "agent-pack.manifest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func writeTestProfilePackManifestWithEnvRefs(t *testing.T, repo string) {
	t.Helper()
	body := `{
  "schema_version": "v2",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "namespace": "common",
      "description": "Team coding profile",
      "instructions": [
        "AGENTS.md"
      ],
      "skills": [
        "amux-dev"
      ],
      "subagents": [],
      "env_refs": [
        "credential:github:GITHUB_TOKEN"
      ],
      "extends": null
    }
  },
  "namespaces": {
    "common": {
      "profiles": [
        "coder"
      ]
    }
  }
}`
	path := filepath.Join(repo, "dist", "agent-pack.manifest.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func writeTestProfilePackManifestWithEnvs(t *testing.T, repo string) {
	t.Helper()
	body := `{
  "schema_version": "v2",
  "pack_namespace": "team",
  "profiles": {
    "coder": {
      "namespace": "common",
      "description": "Team coding profile",
      "instructions": [
        "AGENTS.md"
      ],
      "skills": [
        "credential-hints"
      ],
      "subagents": [],
      "envs": [
        "DEMOTION_TOKEN"
      ],
      "extends": null
    }
  },
  "namespaces": {
    "common": {
      "profiles": [
        "coder"
      ]
    }
  }
}`
	path := filepath.Join(repo, "dist", "agent-pack.manifest.json")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func writePackFile(t *testing.T, repo, value string) {
	t.Helper()
	dir := filepath.Join(repo, "profiles")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "coder.md"), []byte(value+"\n"), 0600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("# Instructions\n\nUse this pack.\n"), 0600); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	writeTestProfilePackManifest(t, repo, "Test pack profile", false)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "update profile")
}

func writeProfileDescription(t *testing.T, repo, description string) {
	t.Helper()
	path := filepath.Join(repo, "profiles", "coder.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	next := strings.Replace(string(data), `description = "Team coding profile"`, `description = "`+description+`"`, 1)
	if err := os.WriteFile(path, []byte(next), 0600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	writeTestProfilePackManifest(t, repo, description, false)
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "update profile description")
}

func runAgentPackCmd(t *testing.T, args ...string) {
	t.Helper()
	cmd := newAgentCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(append([]string{"pack"}, args...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("agent pack %s: %v", strings.Join(args, " "), err)
	}
}

func gitRev(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "rev-parse", "HEAD")
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func gitBranch(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "branch", "--show-current")
	data, err := cmd.Output()
	if err != nil {
		t.Fatalf("git branch --show-current: %v", err)
	}
	branch := strings.TrimSpace(string(data))
	if branch == "" {
		t.Fatal("git branch --show-current returned empty branch")
	}
	return branch
}

func readPackFile(t *testing.T, materializedPath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(materializedPath, "profiles", "coder.md"))
	if err != nil {
		t.Fatalf("read materialized pack file: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
}
