package cli

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

var newRunSessionResolver = func() (*sessionResolver, error) {
	return newSessionResolver()
}

func newRunCmd() *cobra.Command {
	var name string
	var envVars []string
	var onTarget string
	var repo string
	var repoRef string
	var cwd string
	var runtimeRef string
	var model string
	var runAs string

	cmd := supportsJSONOutput(&cobra.Command{
		Use:               "run <agent-profile|agent-type> [-- <agent-args>...]",
		Short:             "Start an agent and register it as a session",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeAgentProfileRefs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req, err := buildRunRequestWithRuntime(args, cmd.Flags().ArgsLenAtDash(), name, envVars, repo, repoRef, cwd, runtimeRef, model)
			if err != nil {
				return err
			}
			req.RunAs = strings.TrimSpace(runAs)
			resp, err := runSession(cmd.Context(), onTarget, req)
			if err != nil {
				return fmt.Errorf("run: %w", err)
			}
			s := resp.Session
			// Non-fatal launch findings: the session is up, but something about it
			// needs attention (e.g. an expired provider credential that will make
			// every turn come back empty). Warnings go to stderr so they surface in
			// --json mode too without polluting the JSON document on stdout.
			for _, warning := range resp.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", warning)
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":  "amux.session.run.v1",
					"session": s,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Session started: %s (id=%s, pid=%d)\n", s.Name, s.Id, s.Pid)
			return nil
		},
	})
	cmd.Flags().StringVarP(&name, "name", "n", "", "session name (required)")
	cmd.Flags().StringArrayVarP(&envVars, "env", "e", nil, "set env var KEY=VAL (repeatable)")
	cmd.Flags().StringVar(&onTarget, "on", "", "target to run on (default: local)")
	cmd.Flags().StringVar(&repo, "repo", "", "remote Git repository URL to materialize at ./repo")
	cmd.Flags().StringVar(&repoRef, "ref", "", "git ref to checkout after materializing --repo")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory on the target runtime")
	cmd.Flags().StringVar(&runtimeRef, "runtime", "", "runtime adapter to execute the Agent Profile")
	cmd.Flags().StringVar(&model, "model", "", "override the Agent Profile model for this session")
	cmd.Flags().StringVar(&runAs, "run-as", "", "run as an enrolled principal alias (resolved by amuxd)")
	_ = cmd.RegisterFlagCompletionFunc("name", completeNoFiles)
	_ = cmd.RegisterFlagCompletionFunc("env", completeNoFiles)
	_ = cmd.RegisterFlagCompletionFunc("on", completeTargets)
	_ = cmd.RegisterFlagCompletionFunc("repo", completeNoFiles)
	_ = cmd.RegisterFlagCompletionFunc("ref", completeNoFiles)
	_ = cmd.RegisterFlagCompletionFunc("cwd", completeDirectories)
	_ = cmd.RegisterFlagCompletionFunc("runtime", completeRuntimeRefs)
	_ = cmd.RegisterFlagCompletionFunc("model", completeRuntimeModels("runtime"))
	_ = cmd.RegisterFlagCompletionFunc("run-as", completeNoFiles)
	_ = cmd.Flags().MarkHidden("cwd")
	return cmd
}

func buildRunRequest(args []string, argsLenAtDash int, name string, envVars []string, repo string, repoRef string, cwd string, model string) (*pb.RunRequest, error) {
	return buildRunRequestWithRuntime(args, argsLenAtDash, name, envVars, repo, repoRef, cwd, "", model)
}

func buildRunRequestWithRuntime(args []string, argsLenAtDash int, name string, envVars []string, repo string, repoRef string, cwd string, runtimeRef string, model string) (*pb.RunRequest, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("agent type is required")
	}
	if name == "" {
		return nil, fmt.Errorf("--name is required")
	}
	if len(args) > 1 && argsLenAtDash < 0 {
		return nil, fmt.Errorf("agent args must be passed after --")
	}
	var err error
	repo, err = normalizeRepoInput(repo)
	if err != nil {
		return nil, err
	}
	repoRef = strings.TrimSpace(repoRef)
	if repo == "" && repoRef != "" {
		return nil, fmt.Errorf("--ref requires --repo")
	}
	agentRef := args[0]
	extraArgs := append([]string{}, args[1:]...)
	launch, err := projectRunLaunchWithRuntime(agentRef, extraArgs, envVars, runtimeRef, model, resolveAgentProfile)
	if err != nil {
		return nil, err
	}
	return &pb.RunRequest{
		AgentProfile: launch.AgentProfile,
		ProfileSpec:  launch.ProfileSpec,
		AgentType:    launch.AgentType,
		Name:         name,
		Env:          launch.Env,
		Cwd:          cwd,
		Repo:         repo,
		RepoRef:      repoRef,
		ExtraArgs:    launch.ExtraArgs,
	}, nil
}

func projectRunLaunch(
	agentRef string,
	explicitArgs []string,
	explicitEnv []string,
	model string,
	resolve func(string) (agentprofile.Profile, error),
) (agentprofile.LaunchProjection, error) {
	return projectRunLaunchWithRuntime(agentRef, explicitArgs, explicitEnv, "", model, resolve)
}

func projectRunLaunchWithRuntime(
	agentRef string,
	explicitArgs []string,
	explicitEnv []string,
	runtimeRef string,
	model string,
	resolve func(string) (agentprofile.Profile, error),
) (agentprofile.LaunchProjection, error) {
	profile, err := resolve(agentRef)
	if err == nil {
		return agentprofile.ProjectProfileLaunchChecked(
			profile,
			explicitArgs,
			explicitEnv,
			agentprofile.LaunchOptions{Runtime: runtimeRef, Model: model},
		)
	}
	if strings.Contains(agentRef, "/") || !agentprofile.IsProfileNotFound(err) {
		return agentprofile.LaunchProjection{}, err
	}
	if _, err := adapter.Get(agentRef); err != nil {
		return agentprofile.LaunchProjection{}, err
	}
	if strings.TrimSpace(runtimeRef) != "" {
		return agentprofile.LaunchProjection{}, fmt.Errorf("--runtime requires an Agent Profile, got legacy agent type %q", agentRef)
	}
	return agentprofile.ProjectLegacyLaunch(
		agentRef,
		explicitArgs,
		agentprofile.LaunchOptions{Model: model},
	), nil
}

func normalizeRepoInput(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return repo, nil
	}
	if !isRemoteGitRepoURL(repo) {
		return "", fmt.Errorf("--repo only supports remote Git URLs for now; local paths such as %q are not supported", repo)
	}
	return repo, nil
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

func runSession(ctx context.Context, onTarget string, req *pb.RunRequest) (*pb.RunResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	onTarget = effectiveRunTarget(onTarget)
	r, err := newRunSessionResolver()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	target, err := r.ResolveTarget(ctx, onTarget)
	if err != nil {
		return nil, err
	}
	fillCLIWorkspace(req, targetWorkspaceScope(target.ID, target.Name))
	return target.Backend.Run(ctx, target.ID, req)
}

func fillCLIWorkspace(req *pb.RunRequest, scope string) {
	if req.WorkspaceSource == "" {
		req.WorkspaceSource = "cli"
	}
	if req.WorkspaceScope == "" {
		req.WorkspaceScope = scope
	}
}

func targetWorkspaceScope(targetID, targetName string) string {
	if targetName != "" {
		return targetName
	}
	if targetID != "" {
		return targetID
	}
	return "local"
}

func effectiveRunTarget(explicit string) string {
	if target := configuredRunTarget(explicit); target != "" {
		return target
	}
	return "local"
}

func configuredRunTarget(explicit string) string {
	if explicit != "" {
		return explicit
	}
	_ = loadUserConfig()
	if target := config.GetCLI().DefaultTarget; target != "" {
		return target
	}
	return ""
}
