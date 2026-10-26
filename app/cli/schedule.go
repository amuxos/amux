package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/amuxos/amux/internal/clientop"
	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func newScheduleCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "schedule",
		Short: "Schedule prompts or subtasks on a timer",
		Long: `Manage scheduled prompts and scheduled subtasks. At the scheduled time
amuxd either injects a prompt into a target session (the same as typing it) or
submits an ephemeral subtask from a saved task spec. Schedules live in the hub
(amuxd), so a hub address must be configured.

Time spec forms:
  every <duration>   e.g. "every 30m", "every 2h" (minimum 1m)
  daily <HH:MM>      e.g. "daily 09:00" (server local time)
  at <when>          e.g. "at 2026-06-01 09:00" (one-shot)
  cron <expr>        e.g. "cron 0 9 * * 1-5" (standard 5-field)`,
	}
	c.AddCommand(newScheduleCreateCmd(), newScheduleCreateTaskCmd(), newScheduleRunsCmd(), newScheduleListCmd(), newScheduleUpdateCmd(), newScheduleRemoveCommand("remove <id>"), newScheduleToggleCmd(true), newScheduleToggleCmd(false))
	return c
}

func newScheduleCreateCmd() *cobra.Command {
	var name string
	var runMode string
	c := supportsJSONOutput(&cobra.Command{
		Use:   "create <session-ref> <spec> <prompt>",
		Short: "Create a scheduled prompt",
		Example: `  amux schedule create local/c1 "every 30m" "/daily-report run"
  amux schedule create local/c1 "at 2026-06-01 09:00" "good morning"
  amux schedule create local/c1 "cron 0 9 * * 1-5" "standup reminder"
  amux schedule create local/c1 "every 1h" "/daily-report run" --run-mode new-session-per-run`,
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: completeScheduleCreateArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := schedule.NormalizeRunMode(runMode); err != nil {
				return err
			}
			ctx := cmdContext(cmd)

			// Resolve the session ref to a concrete target/session.
			r, err := newSessionResolver()
			if err != nil {
				return err
			}
			defer r.Close()
			resolved, err := r.ResolveSession(ctx, args[0])
			if err != nil {
				return err
			}

			client, closeClient, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeClient()

			result, err := clientop.ScheduleCreateSessionPrompt(ctx, client, clientop.ScheduleCreateSessionPromptRequest{
				TargetID:    resolved.Target.ID,
				TargetName:  resolved.Target.Name,
				SessionName: resolved.Session.Name,
				Spec:        args[1],
				RunMode:     runMode,
				Prompt:      args[2],
				Name:        name,
				CreatedBy:   "cli",
			})
			if err != nil {
				return fmt.Errorf("create schedule: %s", cliScheduleCreateError(err))
			}
			next := "—"
			if result.NextRunAt > 0 {
				next = time.Unix(result.NextRunAt, 0).Format("2006-01-02 15:04")
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":      "amux.schedule.create.v1",
					"schedule_id": result.ID,
					"next_run_at": result.NextRunAt,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created schedule #%d (next run: %s)\n", result.ID, next)
			return nil
		},
	})
	c.Flags().StringVar(&name, "name", "", "optional label for the schedule")
	c.Flags().StringVar(&runMode, "run-mode", schedule.RunModeReuseSession, "execution mode: reuse-session or new-session-per-run")
	_ = c.RegisterFlagCompletionFunc("name", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("run-mode", completeScheduleRunModes)
	return c
}

type scheduleTaskFlags struct {
	extraArgs            []string
	name                 string
	agent                string
	runtime              string
	model                string
	reasoningEffort      string
	taskPrompt           string
	isolation            string
	credentialProfile    string
	permissionPolicy     string
	workspacePolicy      string
	localDir             string
	repoKind             string
	repoRemote           string
	repoCacheKey         string
	repoCommit           string
	repoSubdir           string
	timeoutSeconds       int
	legacyTrustedModeAck bool
	runAs                string
}

// cliScheduleFieldNames renders clientop validation errors in flag vocabulary.
// FieldPermissionPolicy, FieldWorkspaceLocalDir, and FieldRepoCacheKey cannot
// reach err.Render today: cliScheduleValidationMessage special-cases those
// fields first to preserve the CLI's pre-clientop wording. Their entries stay
// here anyway as a safety net, so a future clientop rule that reaches
// err.Render through a path this function does not special-case still gets a
// flag name instead of falling back to the bare clientop.Field identifier.
var cliScheduleFieldNames = map[clientop.Field]string{
	clientop.FieldAgent:             "--agent",
	clientop.FieldTask:              "--task",
	clientop.FieldPermissionPolicy:  "permission policy",
	clientop.FieldWorkspaceLocalDir: "--local-dir",
	clientop.FieldRepoCacheKey:      "--repo-cache-key",
}

func (f scheduleTaskFlags) clientopSpec() clientop.ScheduledTaskSpec {
	spec := clientop.ScheduledTaskSpec{
		ExtraArgs:         append([]string(nil), f.extraArgs...),
		Agent:             f.agent,
		Runtime:           f.runtime,
		Model:             f.model,
		ReasoningEffort:   f.reasoningEffort,
		Task:              f.taskPrompt,
		RunAs:             f.runAs,
		Isolation:         f.isolation,
		CredentialProfile: f.credentialProfile,
		PermissionPolicy:  f.permissionPolicy,
		Workspace: clientop.WorkspaceSpec{
			Policy:   f.workspacePolicy,
			LocalDir: f.localDir,
		},
		TimeoutSeconds: f.timeoutSeconds,
	}
	spec.Workspace.Repo = &clientop.RepoSpec{
		Kind:     f.repoKind,
		Remote:   f.repoRemote,
		CacheKey: f.repoCacheKey,
		Commit:   f.repoCommit,
		Subdir:   f.repoSubdir,
	}
	return spec
}

// cliScheduleCreateError renders a ScheduleCreateTask failure for the CLI. A
// validation failure is a *clientop.FieldError and must be phrased in flag
// vocabulary; anything else (a connection error, a hub-side refusal) passes
// through as-is.
func cliScheduleCreateError(err error) string {
	var fe *clientop.FieldError
	if errors.As(err, &fe) {
		return cliScheduleValidationMessage(fe)
	}
	return err.Error()
}

// cliScheduleValidationMessage keeps the CLI's established wording. Two rules
// read differently here than their field-generic form, so they are spelled out
// rather than rendered: the permission-policy messages predate clientop and are
// asserted verbatim by the CLI's tests.
func cliScheduleValidationMessage(err *clientop.FieldError) string {
	if err.Field == clientop.FieldPermissionPolicy {
		if err.RawValue == string(task.PolicyEscalateToMain) {
			return "permission-policy=escalate-to-main is not supported for scheduled subtasks"
		}
		return fmt.Sprintf("invalid permission policy %q (expected default, policy-allow, or deny-all)", err.RawValue)
	}
	if err.Field == clientop.FieldWorkspaceLocalDir && err.Also == clientop.FieldRepoCacheKey {
		return "--local-dir or --repo-cache-key is required with --workspace-policy=git-worktree"
	}
	return err.Render(cliScheduleFieldNames)
}

func newScheduleCreateTaskCmd() *cobra.Command {
	flags := scheduleTaskFlags{workspacePolicy: string(task.WorkspaceTempDir)}
	c := supportsJSONOutput(&cobra.Command{
		Use:   "create-task <spec>",
		Short: "Create a scheduled subtask",
		Example: `  amux schedule create-task "every 1h" --agent team/reviewer --task "review recent changes"
  amux schedule create-task "daily 09:00" --agent builtin/codex --task "run smoke tests" --workspace-policy git-worktree --local-dir /path/to/repo`,
		Args: cobra.ExactArgs(1), ValidArgsFunction: completeScheduleSpec,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, closeClient, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeClient()
			result, err := clientop.ScheduleCreateTask(cmdContext(cmd), client, clientop.ScheduleCreateTaskRequest{Spec: args[0], Name: flags.name, CreatedBy: "cli", Task: flags.clientopSpec()})
			if err != nil {
				return fmt.Errorf("create scheduled subtask: %s", cliScheduleCreateError(err))
			}
			next := "—"
			if result.NextRunAt > 0 {
				next = time.Unix(result.NextRunAt, 0).Format("2006-01-02 15:04")
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":      "amux.schedule.create.v1",
					"schedule_id": result.ID,
					"next_run_at": result.NextRunAt,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created scheduled subtask #%d (next run: %s)\n", result.ID, next)
			return nil
		},
	})
	c.Flags().StringVar(&flags.name, "name", "", "optional label for the schedule")
	registerScheduleTaskFlags(c, &flags)
	registerScheduleTaskFlagCompletions(c)
	_ = c.RegisterFlagCompletionFunc("name", completeNoFiles)
	return c
}

// registerScheduleTaskFlags binds the task-spec flags shared by `create-task`
// and `update`, so the two commands cannot drift on what a scheduled task can
// carry. `name`/`spec` are command-specific and stay with each command.
func registerScheduleTaskFlags(c *cobra.Command, flags *scheduleTaskFlags) {
	c.Flags().StringArrayVar(&flags.extraArgs, "arg", nil, commandCatalog.Format(commandLocale(c), "task.arg"))
	c.Flags().StringVar(&flags.agent, "agent", "", "agent profile ref or legacy agent type to run")
	c.Flags().StringVar(&flags.runtime, "runtime", "", commandCatalog.Format(commandLocale(c), "mcp.task.runtime"))
	c.Flags().StringVar(&flags.model, "model", "", commandCatalog.Format(commandLocale(c), "mcp.schedule.model"))
	c.Flags().StringVar(&flags.reasoningEffort, "reasoning-effort", "", commandCatalog.Format(commandLocale(c), "mcp.schedule.reasoning_effort"))
	c.Flags().StringVar(&flags.taskPrompt, "task", "", "task prompt to run on each schedule fire")
	c.Flags().StringVar(&flags.isolation, "isolation", "", "isolation: process | private-home (default process)")
	c.Flags().StringVar(&flags.credentialProfile, "credential-profile", "", "credential profile name (control-plane resolved)")
	c.Flags().StringVar(&flags.permissionPolicy, "permission-policy", "", commandCatalog.Format(commandLocale(c), "mcp.schedule.permission_policy"))
	c.Flags().StringVar(&flags.workspacePolicy, "workspace-policy", string(task.WorkspaceTempDir), "workspace policy: temp-dir | git-worktree")
	c.Flags().StringVar(&flags.localDir, "local-dir", "", "target-local working directory override")
	c.Flags().StringVar(&flags.repoKind, "repo-kind", "", "workspace repo kind (for example: git)")
	c.Flags().StringVar(&flags.repoRemote, "repo-remote", "", "workspace repo remote URL or stable id")
	c.Flags().StringVar(&flags.repoCacheKey, "repo-cache-key", "", "workspace repo cache key")
	c.Flags().StringVar(&flags.repoCommit, "repo-commit", "", "workspace repo commit/ref")
	c.Flags().StringVar(&flags.repoSubdir, "repo-subdir", "", "workspace repo subdirectory")
	c.Flags().IntVar(&flags.timeoutSeconds, "timeout", 0, "task timeout in seconds (0 = runtime default)")
	c.Flags().BoolVar(&flags.legacyTrustedModeAck, "trusted-mode-ack", true, "legacy no-op: long-lived secrets inject under weak isolation by default (v0.38)")
	c.Flags().StringVar(&flags.runAs, "run-as", "", "run as an enrolled principal alias (resolved and frozen when the schedule is saved)")
}

// registerScheduleTaskFlagCompletions wires shell completion for the shared
// task-spec flags.
func registerScheduleTaskFlagCompletions(c *cobra.Command) {
	_ = c.RegisterFlagCompletionFunc("agent", completeAgentProfileRefs)
	_ = c.RegisterFlagCompletionFunc("task", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("isolation", completeScheduleIsolation)
	_ = c.RegisterFlagCompletionFunc("credential-profile", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("permission-policy", completeSchedulePermissionPolicy)
	_ = c.RegisterFlagCompletionFunc("workspace-policy", completeScheduleWorkspacePolicy)
	_ = c.RegisterFlagCompletionFunc("local-dir", completeDirectories)
	_ = c.RegisterFlagCompletionFunc("repo-kind", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("repo-remote", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("repo-cache-key", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("repo-commit", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("repo-subdir", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("timeout", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("trusted-mode-ack", completeBoolValues)
	_ = c.RegisterFlagCompletionFunc("run-as", completeNoFiles)
}
func newScheduleUpdateCmd() *cobra.Command {
	flags := scheduleTaskFlags{workspacePolicy: string(task.WorkspaceTempDir)}
	var name, spec string
	c := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a scheduled prompt or subtask in place",
		Example: `  amux schedule update 8 --name nightly-report
  amux schedule update 8 --spec "daily 09:00"
  amux schedule update 8 --agent builtin/codex --task "run smoke tests"`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeScheduleIDs(scheduleAny),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid schedule id %q", args[0])
			}
			req := clientop.ScheduleUpdateRequest{ID: id}
			if cmd.Flags().Changed("name") {
				req.Name = &name
			}
			if cmd.Flags().Changed("spec") {
				req.Spec = &spec
			}
			if cmd.Flags().Changed("agent") || cmd.Flags().Changed("task") || cmd.Flags().Changed("runtime") || cmd.Flags().Changed("model") || cmd.Flags().Changed("arg") {
				taskSpec := flags.clientopSpec()
				req.Task = &taskSpec
			}
			client, closeClient, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeClient()
			out, err := clientop.ScheduleUpdate(cmdContext(cmd), client, req)
			if err != nil {
				var fe *clientop.FieldError
				if errors.As(err, &fe) {
					return errors.New(cliScheduleValidationMessage(fe))
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "updated schedule #%d", out.ID)
			if out.NextRunAt > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), " (next run: %s)", time.Unix(out.NextRunAt, 0).Format("2006-01-02 15:04"))
			}
			fmt.Fprintln(cmd.OutOrStdout())
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "new label for the schedule")
	c.Flags().StringVar(&spec, "spec", "", "new time rule (every <duration>, daily <HH:MM>, at <when>, or cron <expr>); recomputes next run")
	registerScheduleTaskFlags(c, &flags)
	registerScheduleTaskFlagCompletions(c)
	_ = c.RegisterFlagCompletionFunc("name", completeNoFiles)
	_ = c.RegisterFlagCompletionFunc("spec", completeScheduleSpec)
	return c
}
func newScheduleRunsCmd() *cobra.Command {
	return supportsJSONOutput(&cobra.Command{
		Use:               "runs <id>",
		Short:             "List runs for a schedule",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeScheduleIDs(scheduleAny),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid schedule id %q", args[0])
			}
			client, closeClient, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeClient()

			item, err := clientop.ScheduleGet(cmdContext(cmd), client, clientop.ScheduleIDRequest{ID: id, IncludeRuns: true})
			if err != nil {
				return fmt.Errorf("get schedule: %w", err)
			}
			if item == nil {
				return fmt.Errorf("schedule #%d not found", id)
			}
			if jsonOutputEnabled(cmd) {
				runs := item.Runs
				if runs == nil {
					runs = []*pb.ScheduleRun{}
				}
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":      "amux.schedule.runs.v1",
					"schedule_id": id,
					"runs":        runs,
				})
			}
			if len(item.Runs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no runs")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "FIRED_AT\tTASK_ID\tSTATUS")
			for _, run := range item.Runs {
				if run == nil {
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", fmtUnix(run.FiredAt), dashIfEmpty(run.TaskId), dashIfEmpty(run.Status))
			}
			return w.Flush()
		},
	})
}

func newScheduleListCmd() *cobra.Command {
	return supportsJSONOutput(&cobra.Command{
		Use:               "list",
		Short:             "List scheduled prompts and subtasks",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmdContext(cmd)
			client, closeClient, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeClient()

			items, err := clientop.ScheduleList(ctx, client)
			if err != nil {
				return fmt.Errorf("list schedules: %w", err)
			}
			if jsonOutputEnabled(cmd) {
				schedules := items
				if schedules == nil {
					schedules = []*pb.ScheduleItem{}
				}
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":    "amux.schedule.list.v1",
					"schedules": schedules,
				})
			}
			if len(items) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no schedules")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tTYPE\tTARGET\tMODE\tSPEC\tNEXT\tLAST\tSTATUS\tENABLED")
			for _, s := range items {
				name := s.Name
				if name == "" {
					name = "—"
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%v\n",
					s.Id, name, dashIfEmpty(s.ScheduleType), scheduleListTarget(s), scheduleListMode(s), s.Spec,
					fmtUnix(s.NextRunAt), fmtUnix(s.LastRunAt), dashIfEmpty(s.LastStatus), s.Enabled)
			}
			return w.Flush()
		},
	})
}

func newScheduleRemoveCommand(use string) *cobra.Command {
	return supportsJSONOutput(&cobra.Command{
		Use:               use,
		Short:             "Delete a scheduled prompt",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeScheduleIDs(scheduleAny),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid schedule id %q", args[0])
			}
			ctx := cmdContext(cmd)
			client, closeClient, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeClient()

			_, err = clientop.ScheduleDelete(ctx, client, clientop.ScheduleIDRequest{ID: id})
			if err != nil {
				return fmt.Errorf("delete schedule: %w", err)
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":      "amux.schedule.remove.v1",
					"schedule_id": id,
					"deleted":     true,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "deleted schedule #%d\n", id)
			return nil
		},
	})
}

func newScheduleToggleCmd(enable bool) *cobra.Command {
	use, short := "disable <id>", "Disable a scheduled prompt"
	if enable {
		use, short = "enable <id>", "Enable a scheduled prompt"
	}
	filter := scheduleEnabledOnly
	if enable {
		filter = scheduleDisabledOnly
	}
	return supportsJSONOutput(&cobra.Command{
		Use:               use,
		Short:             short,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeScheduleIDs(filter),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid schedule id %q", args[0])
			}
			ctx := cmdContext(cmd)
			client, closeClient, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeClient()
			// Reuse create/delete surface: toggling is done by re-creating is
			// overkill; route through a dedicated lightweight call instead.
			if _, err := clientop.ScheduleSetEnabled(ctx, client, id, enable); err != nil {
				return fmt.Errorf("update schedule: %w", err)
			}
			verb := "disabled"
			if enable {
				verb = "enabled"
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":      "amux.schedule.toggle.v1",
					"schedule_id": id,
					"enabled":     enable,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s schedule #%d\n", verb, id)
			return nil
		},
	})
}

func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func fmtUnix(sec int64) string {
	if sec <= 0 {
		return "—"
	}
	return time.Unix(sec, 0).Format("01-02 15:04")
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func scheduleListTarget(s *pb.ScheduleItem) string {
	if s == nil {
		return "—"
	}
	if s.ScheduleType == schedule.ScheduleTypeSubtask {
		return "task:" + scheduledTaskAgent(s.TaskSpecJson)
	}
	sess := s.SessionName
	if s.TargetName != "" {
		sess = s.TargetName + "/" + s.SessionName
	}
	return dashIfEmpty(sess)
}

func scheduleListMode(s *pb.ScheduleItem) string {
	if s == nil || s.ScheduleType == schedule.ScheduleTypeSubtask {
		return "—"
	}
	return dashIfEmpty(s.RunMode)
}

func scheduledTaskAgent(specJSON []byte) string {
	if len(specJSON) == 0 {
		return "—"
	}
	var spec task.Spec
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return "—"
	}
	return dashIfEmpty(strings.TrimSpace(spec.Agent))
}

func completeSchedulePermissionPolicy(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return schedulePermissionPolicyCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
}

func schedulePermissionPolicyCompletions(toComplete string) []string {
	return filterCompletionValues([]string{
		"default\tuse profile or task defaults",
		string(task.PolicyAllow) + "\tautomatically allow permitted requests",
		string(task.PolicyDenyAll) + "\tdeny all permission requests",
	}, toComplete)
}

func completeScheduleIsolation(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return scheduleIsolationCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
}

func scheduleIsolationCompletions(toComplete string) []string {
	return filterCompletionValues([]string{
		string(task.IsolationProcess) + "\tno isolation boundary",
		string(task.IsolationPrivateHome) + "\tseparate HOME/workspace",
	}, toComplete)
}

func completeScheduleWorkspacePolicy(_ *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	return scheduleWorkspacePolicyCompletions(toComplete), cobra.ShellCompDirectiveNoFileComp
}

func scheduleWorkspacePolicyCompletions(toComplete string) []string {
	return filterCompletionValues([]string{
		string(task.WorkspaceTempDir) + "\ttemporary workspace",
		string(task.WorkspaceGitWorktree) + "\tgit worktree workspace",
	}, toComplete)
}
