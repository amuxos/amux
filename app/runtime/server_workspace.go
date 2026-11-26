package runtime

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/amuxos/amux/internal/workspace"
)

func sessionCwdPath(amuxDir string, req *pb.RunRequest) (string, bool, error) {
	if req.Cwd != "" {
		st, err := os.Stat(req.Cwd)
		if err != nil {
			return "", false, err
		}
		if !st.IsDir() {
			return "", false, fmt.Errorf("%s is not a directory", req.Cwd)
		}
		return req.Cwd, false, nil
	}
	dir := workspace.DefaultDir(amuxDir, req.WorkspaceSource, req.WorkspaceScope, req.Name)
	return dir, true, nil
}
func materializeRepoWorkspace(ctx context.Context, req *pb.RunRequest, cwd string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if req == nil || strings.TrimSpace(req.Repo) == "" {
		if req != nil && strings.TrimSpace(req.RepoRef) != "" {
			return "", fmt.Errorf("repo_ref requires repo")
		}
		return "", nil
	}
	repo := strings.TrimSpace(req.Repo)
	if !isRemoteGitRepoURL(repo) {
		return "", fmt.Errorf("repo input only supports remote Git URLs for now; local paths such as %q are not supported", repo)
	}
	if strings.TrimSpace(cwd) == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	if err := os.MkdirAll(cwd, 0700); err != nil {
		return "", err
	}
	dest := filepath.Join(cwd, "repo")
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s already exists", dest)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", repo, dest)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return "", fmt.Errorf("git clone: %w: %s", err, msg)
		}
		return "", fmt.Errorf("git clone: %w", err)
	}
	if ref := strings.TrimSpace(req.RepoRef); ref != "" {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		checkout := exec.CommandContext(ctx, "git", "-C", dest, "checkout", ref)
		out, err := checkout.CombinedOutput()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				return "", fmt.Errorf("git checkout %s: %w: %s", ref, err, msg)
			}
			return "", fmt.Errorf("git checkout %s: %w", ref, err)
		}
	}
	return "repo", nil
}
func isRemoteGitRepoURL(repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	if isSCPStyleGitURL(repo) {
		return true
	}
	u, err := url.Parse(repo)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "http", "https", "ssh", "git":
		return true
	default:
		return strings.HasPrefix(u.Scheme, "git+")
	}
}
func isSCPStyleGitURL(repo string) bool {
	before, after, ok := strings.Cut(repo, ":")
	return ok && strings.Contains(before, "@") && after != ""
}
func materializeProfileWorkspace(req *pb.RunRequest, cwd string, repoPath string, amuxDir string) error {
	if req == nil || req.ProfileSpec == nil {
		return nil
	}
	profile, err := agentprofile.FromPBProfileSpec(req.ProfileSpec)
	if err != nil {
		return err
	}
	if profile.Type == "" {
		profile.Type = req.AgentType
	}
	profile, err = agentprofile.AttachLocalPackPath(profile, filepath.Join(amuxDir, "agent-packs", "registry.json"))
	if err != nil {
		return err
	}
	_, err = agentprofile.RenderProfileWorkspace(profile, cwd)
	return err
}
func materializeAmuxMCPForRun(agentType, sessionID, amuxDir, cwd, actorToken string, memoryBinding ...string) ([]string, error) {
	root := profileSessionDir(amuxDir, sessionID)
	sb := &task.Sandbox{TaskID: sessionID, Isolation: task.IsolationProcess, Root: root, Tmp: filepath.Join(root, "tmp"), Workspace: cwd, AgentConfig: filepath.Join(root, "agent-config")}
	if err := os.MkdirAll(sb.AgentConfig, 0700); err != nil {
		return nil, err
	}
	// The session credential file lets the MCP server pick up a credential the
	// hub re-mints after this launch (see applyAgentActorToken).
	return agentprofile.MaterializeAmuxMCP(agentType, sb, adapter.Layout, amuxDir, actorToken, sessionActorTokenPath(amuxDir, sessionID), memoryBinding...)
}
func runArgsContainAmuxMCP(agentType string, args []string) bool {
	return adapter.ArgsContainAmuxMCP(agentType, args)
}

func stripAmuxMCPArgs(agentType string, args []string) []string {
	return adapter.StripAmuxMCPArgs(agentType, args)
}
func profileSessionDir(amuxDir, sessionID string) string {
	return filepath.Join(amuxDir, "sessions", sessionID)
}
func (s *server) cleanupWorkspaceTrustIfUnused(agentType, cwd string, env []string, exceptSessionID string) {
	for _, other := range s.mgr.List() {
		snap := other.Snapshot()
		if snap.ID != exceptSessionID && snap.AgentType == agentType && snap.State == session.StateRunning && filepath.Clean(snap.Cwd) == filepath.Clean(cwd) {
			return
		}
	}
	a, err := adapter.Get(agentType)
	if err != nil {
		return
	}
	if t, ok := a.(adapter.WorkspaceTruster); ok {
		_ = t.CleanupWorkspaceTrust(cwd, env)
	}
}
func isRuntimeOwnedWorkspace(amuxDir string, snap session.Snapshot) bool {
	if strings.TrimSpace(amuxDir) == "" || strings.TrimSpace(snap.Cwd) == "" {
		return false
	}
	if launchSpecExplicitCwd(snap.LaunchSpec) != "" {
		return false
	}
	want := workspace.DefaultDir(amuxDir, snap.WorkspaceSource, snap.WorkspaceScope, snap.Name)
	return filepath.Clean(snap.Cwd) == filepath.Clean(want)
}
