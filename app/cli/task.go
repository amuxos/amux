package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

// newTaskClient dials the hub AmuxTask control plane. amuxd schedules tasks
// across registered runtimes and routes them to the selected amuxlet. It is a
// var so tests can inject a fake client.
var newTaskClient = func() (pb.AmuxTaskClient, func() error, error) {
	return newTaskClientWithTimeout(3 * time.Second)
}

func newTaskClientWithTimeout(timeout time.Duration) (pb.AmuxTaskClient, func() error, error) {
	_ = loadUserConfig()
	addr := hubAddr()
	hc, err := newHubClientWithTimeout(addr, timeout)
	if err != nil {
		return nil, nil, err
	}
	return pb.NewAmuxTaskClient(hc.conn), hc.Close, nil
}

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Run and manage subagent tasks",
		Long: "Submit and manage subagent tasks. A task runs an agent in an isolated " +
			"per-run sandbox with a credential profile and permission policy, and returns " +
			"a structured result. The CLI submits tasks to the hub, which schedules them " +
			"onto a registered runtime.",
	}
	cmd.AddCommand(
		newTaskSubmitCmd(),
		newTaskShowCommand("show <task-id>"),
		newTaskListCommand("list"),
		newTaskLogsCmd(),
		newTaskPruneCmd(),
		newTaskCancelCmd(),
		newTaskRespondCmd(),
	)
	return cmd
}

func newTaskSubmitCmd() *cobra.Command {
	var (
		agent            string
		profile          string
		runtimeRef       string
		model            string
		isolation        string
		permission       string
		workspace        string
		repoDir          string
		commit           string
		credProfile      string
		timeout          int
		wait             bool
		legacyTrustedAck bool
		extraArgs        []string
		runAs            string
	)
	cmd := supportsJSONOutput(&cobra.Command{
		Use:   "submit <task prompt>",
		Short: "Submit a subagent task",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			selectedAgent := agent
			if profile != "" {
				selectedAgent = profile
			}
			spec := task.Spec{
				Agent:             selectedAgent,
				Runtime:           runtimeRef,
				Model:             model,
				Task:              joinArgs(args),
				Isolation:         task.IsolationMode(isolation),
				PermissionPolicy:  task.PermissionPolicy(permission),
				CredentialProfile: credProfile,
				TimeoutSeconds:    timeout,
				ExtraArgs:         extraArgs,
				Blocking:          wait,
				RunAs:             strings.TrimSpace(runAs),
				Workspace: task.WorkspaceSpec{
					Policy:   task.WorkspacePolicy(workspace),
					LocalDir: repoDir,
				},
			}
			if commit != "" {
				spec.Workspace.Repo = &task.RepoSpec{Kind: "git", Commit: commit}
			}
			specJSON, err := json.Marshal(spec)
			if err != nil {
				return err
			}
			client, closeFn, err := newTaskClient()
			if err != nil {
				return err
			}
			defer closeFn()

			resp, err := client.SubmitSubtask(cmd.Context(), &pb.SubmitSubtaskRequest{SpecJson: specJSON})
			if err != nil {
				return err
			}
			run, err := decodeTask(resp.TaskJson)
			if err != nil {
				return err
			}
			if !wait {
				if jsonOutputEnabled(cmd) {
					return printJSON(cmd.OutOrStdout(), map[string]any{
						"schema": "amux.task.submit.v1",
						"task":   run,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "submitted task %s (%s)\n", run.ID, run.Status)
				fmt.Fprintf(cmd.OutOrStdout(), "follow with: amux task logs %s\n", run.ID)
				return nil
			}
			// Blocking: stream to completion, then print the final record.
			streamOut := cmd.OutOrStdout()
			if jsonOutputEnabled(cmd) {
				streamOut = io.Discard
			}
			if err := streamTask(cmd.Context(), client, run.ID, streamOut, false, false); err != nil {
				return err
			}
			final, err := getTask(cmd.Context(), client, run.ID)
			if err != nil {
				return err
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema": "amux.task.submit.v1",
					"task":   final,
				})
			}
			return printTask(cmd.OutOrStdout(), final, false)
		},
	})
	cmd.Flags().StringVar(&agent, "agent", agentprofile.DefaultProfileRef, "agent profile ref or legacy agent type to run")
	cmd.Flags().StringVar(&profile, "profile", "", "agent profile ref to resolve before scheduling (overrides --agent)")
	cmd.Flags().StringVar(&runtimeRef, "runtime", "", commandCatalog.Format(commandLocale(cmd), "mcp.task.runtime"))
	cmd.Flags().StringVar(&model, "model", "", commandCatalog.Format(commandLocale(cmd), "mcp.task.model"))
	cmd.Flags().StringVar(&isolation, "isolation", "", "isolation: process | private-home (default process)")
	cmd.Flags().StringVar(&permission, "permission", "", "permission policy: policy-allow | escalate-to-main | deny-all (default policy-allow)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "workspace policy: temp-dir | git-worktree (default temp-dir)")
	cmd.Flags().StringVar(&repoDir, "repo-dir", "", "repo cache dir for git-worktree workspace")
	cmd.Flags().StringVar(&commit, "commit", "", "git commit/ref to base a git-worktree on (default HEAD)")
	cmd.Flags().StringVar(&credProfile, "credential-profile", "", "credential profile name (control-plane resolved)")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "task timeout in seconds (0 = runtime default)")
	cmd.Flags().BoolVar(&wait, "wait", false, "block until the task finishes and print the result")
	cmd.Flags().BoolVar(&legacyTrustedAck, "trusted-mode-ack", true, "legacy no-op: long-lived secrets inject under weak isolation by default (v0.38)")
	cmd.Flags().StringArrayVar(&extraArgs, "arg", nil, "extra agent CLI arg (repeatable)")
	cmd.Flags().StringVar(&runAs, "run-as", "", "run as an enrolled principal alias (resolved by amuxd)")
	return cmd
}

func newTaskShowCommand(use string) *cobra.Command {
	cmd := supportsJSONOutput(&cobra.Command{
		Use:   use,
		Short: "Show a task's status and result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, closeFn, err := newTaskClient()
			if err != nil {
				return err
			}
			defer closeFn()
			run, err := getTask(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			return printTask(cmd.OutOrStdout(), run, jsonOutputEnabled(cmd))
		},
	})
	return cmd
}

func newTaskListCommand(use string) *cobra.Command {
	cmd := supportsJSONOutput(&cobra.Command{
		Use:   use,
		Short: "List tasks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, closeFn, err := newTaskClient()
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := client.ListSubtasks(cmd.Context(), &pb.ListSubtasksRequest{})
			if err != nil {
				return err
			}
			runs := make([]*task.TaskRun, 0, len(resp.TasksJson))
			for _, b := range resp.TasksJson {
				r, err := decodeTask(b)
				if err != nil {
					return err
				}
				runs = append(runs, r)
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema": "amux.task.list.v1",
					"tasks":  runs,
				})
			}
			out := cmd.OutOrStdout()
			if len(runs) == 0 {
				fmt.Fprintln(out, "no tasks")
				return nil
			}
			fmt.Fprintf(out, "%-40s  %-10s  %-16s  %s\n", "ID", "AGENT", "STATUS", "TASK")
			for _, r := range runs {
				fmt.Fprintf(out, "%-40s  %-10s  %-16s  %s\n", r.ID, r.AgentType, r.Status, truncate(r.Task, 50))
			}
			return nil
		},
	})
	return cmd
}

func newTaskPruneCmd() *cobra.Command {
	var (
		dryRun    bool
		olderThan string
		statusCSV string
	)
	cmd := supportsJSONOutput(&cobra.Command{
		Use:   "prune",
		Short: "Delete terminal task history",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &pb.PruneSubtasksRequest{DryRun: dryRun}
			if olderThan != "" {
				d, err := parseTaskPruneDuration(olderThan)
				if err != nil {
					return err
				}
				req.OlderThanSeconds = int64(d / time.Second)
			}
			if statusCSV != "" {
				statuses, err := parseTaskPruneStatuses(statusCSV)
				if err != nil {
					return err
				}
				req.Statuses = statuses
			}
			client, closeFn, err := newTaskClient()
			if err != nil {
				return err
			}
			defer closeFn()
			resp, err := client.PruneSubtasks(cmd.Context(), req)
			if err != nil {
				return err
			}
			if jsonOutputEnabled(cmd) {
				tasks := resp.Tasks
				if tasks == nil {
					tasks = []*pb.PrunedSubtask{}
				}
				skipped := resp.Skipped
				if skipped == nil {
					skipped = []*pb.SkippedSubtask{}
				}
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema": "amux.task.prune.v1",
					"result": map[string]any{
						"deleted_count": resp.DeletedCount,
						"tasks":         tasks,
						"skipped":       skipped,
					},
				})
			}
			return printPruneSubtasks(cmd.OutOrStdout(), resp, dryRun)
		},
	})
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be deleted without deleting anything")
	cmd.Flags().StringVar(&olderThan, "older-than", "", "only prune terminal tasks older than duration (for example 24h or 7d)")
	cmd.Flags().StringVar(&statusCSV, "status", "", "comma-separated terminal statuses to prune (succeeded,failed,cancelled,timed_out)")
	return cmd
}

func parseTaskPruneDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if strings.HasSuffix(raw, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid --older-than %q", raw)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("invalid --older-than %q", raw)
	}
	return d, nil
}

func parseTaskPruneStatuses(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		st := task.Status(strings.TrimSpace(part))
		if st == "" {
			continue
		}
		if !st.IsTerminal() {
			return nil, fmt.Errorf("status %q is not terminal", st)
		}
		out = append(out, string(st))
	}
	return out, nil
}

func printPruneSubtasks(out io.Writer, resp *pb.PruneSubtasksResponse, dryRun bool) error {
	verb := "deleted"
	if dryRun {
		verb = "would delete"
	}
	fmt.Fprintf(out, "%s %d terminal task(s)\n", verb, resp.DeletedCount)
	for _, t := range resp.Tasks {
		fmt.Fprintf(out, "- %s %s", t.Id, t.Status)
		if t.TargetId != "" {
			fmt.Fprintf(out, " target=%s", t.TargetId)
		}
		if t.DeletedChildSession {
			fmt.Fprint(out, " child_session=deleted")
		}
		if t.DeletedSandbox {
			fmt.Fprint(out, " sandbox=deleted")
		}
		fmt.Fprintln(out)
	}
	for _, s := range resp.Skipped {
		fmt.Fprint(out, "skipped")
		if s.Id != "" {
			fmt.Fprintf(out, " task=%s", s.Id)
		}
		if s.TargetId != "" {
			fmt.Fprintf(out, " target=%s", s.TargetId)
		}
		fmt.Fprintf(out, ": %s\n", s.Reason)
	}
	return nil
}

func newTaskLogsCmd() *cobra.Command {
	cmd := supportsJSONOutput(&cobra.Command{
		Use:   "logs <task-id>",
		Short: "Stream a task's events until it finishes (read-only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, closeFn, err := newTaskClient()
			if err != nil {
				return err
			}
			defer closeFn()
			return streamTask(cmd.Context(), client, args[0], cmd.OutOrStdout(), true, jsonOutputEnabled(cmd))
		},
	})
	return cmd
}

func newTaskCancelCmd() *cobra.Command {
	cmd := supportsJSONOutput(&cobra.Command{
		Use:   "cancel <task-id>",
		Short: "Cancel a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, closeFn, err := newTaskClient()
			if err != nil {
				return err
			}
			defer closeFn()
			if _, err := client.CancelSubtask(cmd.Context(), &pb.CancelSubtaskRequest{Id: args[0]}); err != nil {
				return err
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":    "amux.task.cancel.v1",
					"task_id":   args[0],
					"cancelled": true,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "cancelled %s\n", args[0])
			return nil
		},
	})
	return cmd
}

func newTaskRespondCmd() *cobra.Command {
	var (
		permID string
		allow  bool
		deny   bool
		option string
		reason string
	)
	cmd := supportsJSONOutput(&cobra.Command{
		Use:   "respond <task-id>",
		Short: "Answer an escalated permission request (escalate-to-main)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if allow == deny {
				return fmt.Errorf("specify exactly one of --allow or --deny")
			}
			client, closeFn, err := newTaskClient()
			if err != nil {
				return err
			}
			defer closeFn()
			_, err = client.RespondSubtaskPermission(cmd.Context(), &pb.RespondSubtaskPermissionRequest{
				Id:           args[0],
				PermissionId: permID,
				Allow:        allow,
				OptionId:     option,
				Reason:       reason,
			})
			if err != nil {
				return err
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":        "amux.task.respond.v1",
					"task_id":       args[0],
					"permission_id": permID,
					"allow":         allow,
					"option_id":     option,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "responded to %s (allow=%v)\n", args[0], allow)
			return nil
		},
	})
	cmd.Flags().StringVar(&permID, "permission", "", "permission id to answer (from the escalation event)")
	cmd.Flags().BoolVar(&allow, "allow", false, "approve the request")
	cmd.Flags().BoolVar(&deny, "deny", false, "reject the request")
	cmd.Flags().StringVar(&option, "option", "", "explicit provider option id (optional)")
	cmd.Flags().StringVar(&reason, "reason", "", "reason recorded in diagnostics")
	return cmd
}

// ── helpers ──────────────────────────────────────────────────────────────────

func getTask(ctx context.Context, client pb.AmuxTaskClient, id string) (*task.TaskRun, error) {
	resp, err := client.GetSubtask(ctx, &pb.GetSubtaskRequest{Id: id})
	if err != nil {
		return nil, err
	}
	return decodeTask(resp.TaskJson)
}

// streamTask consumes a task's event stream until it ends. When verbose is true
// it prints agent output chunks too; otherwise it prints status/permission/result
// transitions only.
func streamTask(ctx context.Context, client pb.AmuxTaskClient, id string, out io.Writer, verbose, jsonLines bool) error {
	stream, err := client.StreamSubtask(ctx, &pb.StreamSubtaskRequest{Id: id})
	if err != nil {
		return err
	}
	for {
		ev, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var te task.TaskEvent
		if err := json.Unmarshal(ev.EventJson, &te); err != nil {
			if jsonLines {
				if writeErr := printJSONLine(out, map[string]any{
					"schema":            "amux.task.event.v1",
					"decode_error":      err.Error(),
					"event_json_base64": ev.EventJson,
				}); writeErr != nil {
					return writeErr
				}
			}
			continue
		}
		if jsonLines {
			if err := printJSONLine(out, map[string]any{
				"schema": "amux.task.event.v1",
				"event":  newTaskEventJSONView(te),
			}); err != nil {
				return err
			}
			continue
		}
		switch te.Kind {
		case task.TaskEventStatus:
			fmt.Fprintf(out, "[status] %s\n", te.Status)
		case task.TaskEventPermission:
			if te.Permission != nil {
				fmt.Fprintf(out, "[permission] %s wants %s %s — respond with: amux task respond %s --permission %s --allow|--deny\n",
					id, te.Permission.Tool, te.Permission.Command, id, te.Permission.PermissionId)
			}
		case task.TaskEventResult:
			if te.Result != nil {
				fmt.Fprintf(out, "[result] status=%s summary=%s\n", te.Result.Status, te.Result.Summary)
			}
		case task.TaskEventOutput:
			if verbose && len(te.Output) > 0 {
				out.Write(te.Output)
			}
		}
	}
}

type taskEventJSONView struct {
	TaskID     string                  `json:"task_id"`
	TurnID     string                  `json:"turn_id,omitempty"`
	Seq        uint64                  `json:"seq"`
	Kind       task.TaskEventKind      `json:"kind"`
	Status     task.Status             `json:"status,omitempty"`
	Output     []byte                  `json:"output,omitempty"`
	Usage      json.RawMessage         `json:"usage,omitempty"`
	Permission *taskPermissionJSONView `json:"permission,omitempty"`
	Result     *task.TaskResult        `json:"result,omitempty"`
	At         time.Time               `json:"at"`
}

type taskPermissionJSONView struct {
	PermissionID string   `json:"permission_id,omitempty"`
	Tool         string   `json:"tool,omitempty"`
	Command      string   `json:"command,omitempty"`
	Paths        []string `json:"paths"`
	Write        bool     `json:"write"`
	Network      bool     `json:"network"`
	Raw          []byte   `json:"raw,omitempty"`
}

func newTaskEventJSONView(event task.TaskEvent) taskEventJSONView {
	view := taskEventJSONView{
		TaskID: event.TaskID,
		TurnID: event.TurnID,
		Seq:    event.Seq,
		Kind:   event.Kind,
		Status: event.Status,
		Output: event.Output,
		Usage:  event.Usage,
		Result: event.Result,
		At:     event.At,
	}
	if event.Permission != nil {
		paths := event.Permission.Paths
		if paths == nil {
			paths = []string{}
		}
		view.Permission = &taskPermissionJSONView{
			PermissionID: event.Permission.PermissionId,
			Tool:         event.Permission.Tool,
			Command:      event.Permission.Command,
			Paths:        paths,
			Write:        event.Permission.Write,
			Network:      event.Permission.Network,
			Raw:          event.Permission.Raw,
		}
	}
	return view
}

func printTask(out io.Writer, run *task.TaskRun, jsonOut bool) error {
	if jsonOut {
		return printJSON(out, map[string]any{
			"schema": "amux.task.show.v1",
			"task":   run,
		})
	}
	fmt.Fprintf(out, "id:        %s\n", run.ID)
	fmt.Fprintf(out, "agent:     %s\n", run.AgentType)
	fmt.Fprintf(out, "status:    %s\n", run.Status)
	if run.Error != "" {
		fmt.Fprintf(out, "error:     %s\n", run.Error)
	}
	if run.ChildSessionId != "" {
		fmt.Fprintf(out, "session:   target=%s child=%s\n", run.TargetID, run.ChildSessionId)
	}
	if run.Result != nil {
		fmt.Fprintf(out, "result:    %s\n", run.Result.Status)
		if run.Result.Summary != "" {
			fmt.Fprintf(out, "summary:   %s\n", run.Result.Summary)
		}
		for _, cf := range run.Result.ChangedFiles {
			fmt.Fprintf(out, "  changed: %s (%s)\n", cf.Path, cf.ChangeType)
		}
		for _, d := range run.Result.Diagnostics {
			fmt.Fprintf(out, "  [%s] %s\n", d.Severity, d.Message)
		}
	}
	return nil
}

func decodeTask(b []byte) (*task.TaskRun, error) {
	var run task.TaskRun
	if err := json.Unmarshal(b, &run); err != nil {
		return nil, fmt.Errorf("decode task: %w", err)
	}
	return &run, nil
}

func joinArgs(args []string) string {
	s := ""
	for i, a := range args {
		if i > 0 {
			s += " "
		}
		s += a
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
