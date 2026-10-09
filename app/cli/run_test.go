package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/viper"
)

func TestRunCommandHasOnAndRepoFlags(t *testing.T) {
	cmd := newRunCmd()
	if cmd.Flags().Lookup("on") == nil {
		t.Fatal("--on flag missing")
	}
	if cmd.Flags().Lookup("repo") == nil {
		t.Fatal("--repo flag missing")
	}
	if cmd.Flags().Lookup("model") == nil {
		t.Fatal("--model flag missing")
	}
	if cmd.Flags().Lookup("runtime") == nil {
		t.Fatal("--runtime flag missing")
	}
}

func TestFillCLIWorkspaceDefaultsToCLIAndScope(t *testing.T) {
	req := &pb.RunRequest{}

	fillCLIWorkspace(req, "devbox")

	if req.WorkspaceSource != "cli" || req.WorkspaceScope != "devbox" {
		t.Fatalf("workspace = %q/%q, want cli/devbox", req.WorkspaceSource, req.WorkspaceScope)
	}
}

func TestBuildRunRequestRejectsLocalRepoPath(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)

	_, err := buildRunRequest([]string{"codex"}, -1, "work", nil, ".", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "remote Git URLs") {
		t.Fatalf("error = %v, want local repo path rejected", err)
	}
}

func TestBuildRunRequestKeepsRemoteRepoURL(t *testing.T) {
	req, err := buildRunRequest([]string{"codex"}, -1, "work", nil, "git@example.com:org/project.git", "", "", "")
	if err != nil {
		t.Fatalf("buildRunRequest: %v", err)
	}
	if req.Repo != "git@example.com:org/project.git" {
		t.Fatalf("repo = %q, want remote URL unchanged", req.Repo)
	}
}

func TestBuildRunRequestRejectsRefWithoutRepo(t *testing.T) {
	_, err := buildRunRequest([]string{"codex"}, -1, "work", nil, "", "stable", "", "")
	if err == nil || !strings.Contains(err.Error(), "--ref requires --repo") {
		t.Fatalf("error = %v, want --ref requires --repo", err)
	}
}

func TestBuildRunRequestAcceptsBuiltinProfileRef(t *testing.T) {
	req, err := buildRunRequest([]string{"builtin/claude"}, -1, "work", nil, "", "", "", "")
	if err != nil {
		t.Fatalf("buildRunRequest: %v", err)
	}
	if req.AgentType != "claude" {
		t.Fatalf("agent_type = %q, want claude", req.AgentType)
	}
	if req.AgentProfile != "builtin/claude" {
		t.Fatalf("agent_profile = %q, want builtin/claude", req.AgentProfile)
	}
}

func TestBuildRunRequestAcceptsPackProfileRef(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	repo := initTestProfilePackRepo(t)
	runGit(t, repo, "checkout", "-b", "stable")
	rev := gitRev(t, repo)
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	req, err := buildRunRequestWithRuntime([]string{"team/coder"}, -1, "work", nil, "", "", "", "codex", "gpt-5-codex")
	if err != nil {
		t.Fatalf("buildRunRequest: %v", err)
	}
	if req.AgentType != "codex" {
		t.Fatalf("agent_type = %q, want codex", req.AgentType)
	}
	if req.AgentProfile != "team/coder@"+rev {
		t.Fatalf("agent_profile = %q, want team/coder@%s", req.AgentProfile, rev)
	}
	if req.ProfileSpec == nil {
		t.Fatal("profile_spec is nil, want resolved profile snapshot")
	}
	if req.ProfileSpec.Ref != "team/coder" || req.ProfileSpec.PinnedRef != "team/coder@"+rev {
		t.Fatalf("profile_spec ref = %q pinned=%q, want team/coder@%s", req.ProfileSpec.Ref, req.ProfileSpec.PinnedRef, rev)
	}
	if req.ProfileSpec.Agents != "AGENTS.md" || req.ProfileSpec.AgentsContent != "# Instructions\n\nYou are the team coder.\n" {
		t.Fatalf("profile_spec agents = %q content = %q", req.ProfileSpec.Agents, req.ProfileSpec.AgentsContent)
	}
	if req.ProfileSpec.Model != "gpt-5-codex" {
		t.Fatalf("profile_spec model = %q, want gpt-5-codex", req.ProfileSpec.Model)
	}
	if got, want := req.ExtraArgs, []string{"--model", "gpt-5-codex"}; !equalRunStrings(got, want) {
		t.Fatalf("extra args = %#v, want %#v", got, want)
	}
	if len(req.Env) != 0 {
		t.Fatalf("env = %#v, want empty manifest env", req.Env)
	}
}

func TestBuildRunRequestUsesCallerModelWithPackContent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	repo := initTestProfilePackRepo(t)
	runGit(t, repo, "checkout", "-b", "stable")
	runAgentPackCmd(t, "add", "team", repo, "--ref", "stable")

	req, err := buildRunRequestWithRuntime([]string{"team/coder"}, -1, "work", nil, "", "", "", "codex", "custom-model")
	if err != nil {
		t.Fatalf("buildRunRequest: %v", err)
	}
	if got, want := req.ExtraArgs, []string{"--model", "custom-model"}; !equalRunStrings(got, want) {
		t.Fatalf("extra args = %#v, want explicit model only: %#v", got, want)
	}
	if req.ProfileSpec == nil || req.ProfileSpec.Model != "custom-model" {
		t.Fatalf("profile_spec = %+v, want explicit model snapshot", req.ProfileSpec)
	}
}

func TestBuildRunRequestRejectsUnknownProfileRef(t *testing.T) {
	_, err := buildRunRequest([]string{"builtin/reviewer"}, -1, "work", nil, "", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "unknown agent profile") {
		t.Fatalf("error = %v, want unknown agent profile", err)
	}
}

func TestBuildRunRequestRejectsAgentArgsWithoutDelimiter(t *testing.T) {
	_, err := buildRunRequest([]string{"codex", "prompt"}, -1, "work", nil, "", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "after --") {
		t.Fatalf("error = %v, want delimiter guidance", err)
	}
}

func TestFillCLIWorkspaceKeepsExistingValues(t *testing.T) {
	req := &pb.RunRequest{WorkspaceSource: "feishu", WorkspaceScope: "oc_xxx"}

	fillCLIWorkspace(req, "devbox")

	if req.WorkspaceSource != "feishu" || req.WorkspaceScope != "oc_xxx" {
		t.Fatalf("workspace = %q/%q, want existing feishu/oc_xxx", req.WorkspaceSource, req.WorkspaceScope)
	}
}

func TestTargetWorkspaceScopePrefersTargetName(t *testing.T) {
	if got := targetWorkspaceScope("runtime-1", "devbox"); got != "devbox" {
		t.Fatalf("targetWorkspaceScope = %q, want devbox", got)
	}
}

func TestEffectiveRunTargetUsesConfiguredDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(home, ".amux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[cli]\ndefault_target = \"devbox\"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if got := effectiveRunTarget(""); got != "devbox" {
		t.Fatalf("effectiveRunTarget(empty) = %q, want devbox", got)
	}
	if got := effectiveRunTarget("local"); got != "local" {
		t.Fatalf("effectiveRunTarget(explicit) = %q, want local", got)
	}
}

func TestHubAddrDefaultsToLocalHub(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	viper.Reset()
	t.Cleanup(viper.Reset)

	if got := configuredHubAddr(); got != "" {
		t.Fatalf("configuredHubAddr() = %q, want empty explicit config", got)
	}
	if got := hubAddr(); got != defaultLocalHubAddr {
		t.Fatalf("hubAddr() = %q, want default local hub %q", got, defaultLocalHubAddr)
	}
}

func TestHubAddrUsesEnvironmentBeforeDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMUX_HOME", "")
	t.Setenv("AMUX_HUB_ADDR", "hub.example.com:7100")
	t.Setenv("AMUX_HUB_TOKEN", "secret")
	viper.Reset()
	t.Cleanup(viper.Reset)

	if got := hubAddr(); got != "hub.example.com:7100" {
		t.Fatalf("hubAddr() = %q, want environment hub", got)
	}
	if got := hubToken(); got != "secret" {
		t.Fatalf("hubToken() = %q, want environment token", got)
	}
}

func TestRunSessionDefaultsToLocalViaHubTarget(t *testing.T) {
	oldResolver := newRunSessionResolver
	t.Cleanup(func() {
		newRunSessionResolver = oldResolver
	})

	var gotTargetID string
	var gotWorkspaceSource string
	var gotWorkspaceScope string
	newRunSessionResolver = func() (*sessionResolver, error) {
		return resolverForTests([]targetSessions{{
			target: targetRef{ID: "rt-local", Name: "local", Backend: runCaptureClient{run: func(_ context.Context, targetID string, req *pb.RunRequest) (*pb.RunResponse, error) {
				gotTargetID = targetID
				gotWorkspaceSource = req.WorkspaceSource
				gotWorkspaceScope = req.WorkspaceScope
				return &pb.RunResponse{Session: &pb.Session{Name: req.Name, AgentType: req.AgentType}}, nil
			}}},
		}}), nil
	}

	_, err := runSession(context.Background(), "", &pb.RunRequest{Name: "work", AgentType: "codex"})
	if err != nil {
		t.Fatalf("runSession: %v", err)
	}
	if gotTargetID != "rt-local" || gotWorkspaceSource != "cli" || gotWorkspaceScope != "local" {
		t.Fatalf("run target=%q workspace=%q/%q, want hub local target cli/local", gotTargetID, gotWorkspaceSource, gotWorkspaceScope)
	}
}

type runTestClient struct{}

type runCaptureClient struct {
	run func(context.Context, string, *pb.RunRequest) (*pb.RunResponse, error)
}

func (c runCaptureClient) Run(ctx context.Context, targetID string, req *pb.RunRequest) (*pb.RunResponse, error) {
	return c.run(ctx, targetID, req)
}

func (c runCaptureClient) List(ctx context.Context) ([]*sessionEntry, error) {
	return runTestClient{}.List(ctx)
}

func (c runCaptureClient) Kill(ctx context.Context, targetID string, name string) error {
	return runTestClient{}.Kill(ctx, targetID, name)
}

func (c runCaptureClient) Restart(ctx context.Context, targetID string, name string, refreshProfile bool) error {
	return runTestClient{}.Restart(ctx, targetID, name, refreshProfile)
}

func (c runCaptureClient) SetModel(ctx context.Context, targetID string, name string, replacement *pb.RunRequest) (*pb.RunResponse, error) {
	return runTestClient{}.SetModel(ctx, targetID, name, replacement)
}

func (c runCaptureClient) ChangeCwd(ctx context.Context, targetID string, req *pb.ChangeCwdRequest) (*pb.ChangeCwdResponse, error) {
	return runTestClient{}.ChangeCwd(ctx, targetID, req)
}

func (c runCaptureClient) Sleep(ctx context.Context, targetID string, name string) error {
	return runTestClient{}.Sleep(ctx, targetID, name)
}

func (c runCaptureClient) Resume(ctx context.Context, targetID string, name string) error {
	return runTestClient{}.Resume(ctx, targetID, name)
}

func (c runCaptureClient) Interrupt(ctx context.Context, targetID string, name string) error {
	return runTestClient{}.Interrupt(ctx, targetID, name)
}

func (c runCaptureClient) Attach(ctx context.Context, targetID string, name string) (pb.Amuxlet_AttachClient, error) {
	return runTestClient{}.Attach(ctx, targetID, name)
}

func (c runCaptureClient) Submit(ctx context.Context, targetID string, name string, text string) error {
	return runTestClient{}.Submit(ctx, targetID, name, text)
}

func (c runCaptureClient) SubmitAsync(ctx context.Context, targetID string, name string, text string) error {
	return runTestClient{}.SubmitAsync(ctx, targetID, name, text)
}

func (c runCaptureClient) SubmitPreempt(ctx context.Context, targetID string, name string, text string) error {
	return runTestClient{}.SubmitPreempt(ctx, targetID, name, text)
}

func (c runCaptureClient) QueueList(ctx context.Context, targetID string, name string) ([]*pb.QueueItem, error) {
	return runTestClient{}.QueueList(ctx, targetID, name)
}

func (c runCaptureClient) QueueClear(ctx context.Context, targetID string, name string) (int32, error) {
	return runTestClient{}.QueueClear(ctx, targetID, name)
}

func (c runCaptureClient) QueueCancel(ctx context.Context, targetID string, name string, id string) error {
	return runTestClient{}.QueueCancel(ctx, targetID, name, id)
}

func (c runCaptureClient) Prune(ctx context.Context, targetID string) (int32, []string, error) {
	return runTestClient{}.Prune(ctx, targetID)
}

func (c runCaptureClient) RespondPermission(ctx context.Context, targetID string, name string, permissionID string, decision string) error {
	return runTestClient{}.RespondPermission(ctx, targetID, name, permissionID, decision)
}

func (c runCaptureClient) Close() error { return nil }

func (runTestClient) Run(context.Context, string, *pb.RunRequest) (*pb.RunResponse, error) {
	return &pb.RunResponse{}, nil
}

func (runTestClient) List(context.Context) ([]*sessionEntry, error) { return nil, nil }

func (runTestClient) Kill(context.Context, string, string) error { return nil }

func (runTestClient) Restart(context.Context, string, string, bool) error { return nil }

func (runTestClient) SetModel(context.Context, string, string, *pb.RunRequest) (*pb.RunResponse, error) {
	return &pb.RunResponse{}, nil
}

func (runTestClient) ChangeCwd(context.Context, string, *pb.ChangeCwdRequest) (*pb.ChangeCwdResponse, error) {
	return &pb.ChangeCwdResponse{}, nil
}

func (runTestClient) Sleep(context.Context, string, string) error { return nil }

func (runTestClient) Resume(context.Context, string, string) error { return nil }

func (runTestClient) Interrupt(context.Context, string, string) error {
	return nil
}

func (runTestClient) Attach(context.Context, string, string) (pb.Amuxlet_AttachClient, error) {
	return nil, nil
}

func (runTestClient) Submit(context.Context, string, string, string) error {
	return nil
}

func (runTestClient) SubmitAsync(context.Context, string, string, string) error {
	return nil
}

func (runTestClient) SubmitPreempt(context.Context, string, string, string) error {
	return nil
}

func (runTestClient) QueueList(context.Context, string, string) ([]*pb.QueueItem, error) {
	return nil, nil
}

func (runTestClient) QueueClear(context.Context, string, string) (int32, error) {
	return 0, nil
}

func (runTestClient) QueueCancel(context.Context, string, string, string) error {
	return nil
}

func (runTestClient) Prune(context.Context, string) (int32, []string, error) {
	return 0, nil, nil
}

func (runTestClient) RespondPermission(context.Context, string, string, string, string) error {
	return nil
}

func (runTestClient) Close() error { return nil }

func equalRunStrings(a, b []string) bool {
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
