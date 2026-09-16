package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

func newSessionCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "session",
		Short: "Manage agent sessions",
	}
	list := newLsCmd()
	list.Use = "list [target]"
	attach := newAttachCmd()
	attach.Use = "attach <session-ref>"
	interrupt := newInterruptCmd()
	interrupt.Use = "interrupt <session-ref>"
	sleep := newSleepCmd()
	sleep.Use = "sleep <session-ref>"
	resume := newResumeCmd()
	resume.Use = "resume <session-ref>"
	stop := newStopCmd()
	stop.Use = "stop <session-ref>"
	c.AddCommand(
		newRunCmd(),
		list,
		attach,
		newSendCmd(),
		interrupt,
		sleep,
		resume,
		stop,
		newSessionRestartCmd(),
		newSessionSetModelCmd(),
		newSessionCwdCmd(),
		newSessionQueueCmd(),
		newSessionPruneCmd(),
	)
	return c
}

func newSessionQueueCmd() *cobra.Command {
	queue := supportsJSONOutput(&cobra.Command{
		Use:               "queue <session-ref>",
		Short:             "Show pending input queue for a session",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSingleSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := commandContext(cmd)
			resolved, cleanup, err := resolveSessionForCommand(ctx, args[0])
			if err != nil {
				return err
			}
			defer cleanup()
			items, err := resolved.Target.Backend.QueueList(ctx, resolved.Target.ID, resolved.Session.Name)
			if err != nil {
				return fmt.Errorf("session queue: %w", err)
			}
			if jsonOutputEnabled(cmd) {
				if items == nil {
					items = []*pb.QueueItem{}
				}
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":      "amux.session.queue.v1",
					"session_ref": sessionDisplayRef(resolved.Target, resolved.Session),
					"items":       items,
				})
			}
			if len(items) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No pending input.")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-8s %-8s %-8s %s\n", "ID", "SOURCE", "AGE", "TEXT")
			for _, item := range items {
				age := "-"
				if item.CreatedAt > 0 {
					age = shortDuration(time.Since(time.Unix(item.CreatedAt, 0)))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-8s %-8s %-8s %s\n", item.Id, item.Source, age, oneLine(item.Text, 80))
			}
			return nil
		},
	})
	queue.AddCommand(
		supportsJSONOutput(&cobra.Command{
			Use:               "clear <session-ref>",
			Short:             "Clear pending input queue for a session",
			Args:              cobra.ExactArgs(1),
			ValidArgsFunction: completeSingleSessionArg,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := commandContext(cmd)
				resolved, cleanup, err := resolveSessionForCommand(ctx, args[0])
				if err != nil {
					return err
				}
				defer cleanup()
				cleared, err := resolved.Target.Backend.QueueClear(ctx, resolved.Target.ID, resolved.Session.Name)
				if err != nil {
					return fmt.Errorf("session queue clear: %w", err)
				}
				if jsonOutputEnabled(cmd) {
					return printJSON(cmd.OutOrStdout(), map[string]any{
						"schema":      "amux.session.queue.clear.v1",
						"session_ref": sessionDisplayRef(resolved.Target, resolved.Session),
						"cleared":     cleared,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Cleared %d pending input(s).\n", cleared)
				return nil
			},
		}),
		supportsJSONOutput(&cobra.Command{
			Use:               "cancel <session-ref> <queue-id>",
			Short:             "Cancel one pending input",
			Args:              cobra.ExactArgs(2),
			ValidArgsFunction: completeQueueCancelArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := commandContext(cmd)
				resolved, cleanup, err := resolveSessionForCommand(ctx, args[0])
				if err != nil {
					return err
				}
				defer cleanup()
				if err := resolved.Target.Backend.QueueCancel(ctx, resolved.Target.ID, resolved.Session.Name, args[1]); err != nil {
					return fmt.Errorf("session queue cancel: %w", err)
				}
				if jsonOutputEnabled(cmd) {
					return printJSON(cmd.OutOrStdout(), map[string]any{
						"schema":      "amux.session.queue.cancel.v1",
						"session_ref": sessionDisplayRef(resolved.Target, resolved.Session),
						"queue_id":    args[1],
						"cancelled":   true,
					})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Cancelled pending input %s.\n", args[1])
				return nil
			},
		}),
	)
	return queue
}

// newSessionPruneCmd removes archived (dead) sessions from the default target
// through the hub control plane.
func newSessionPruneCmd() *cobra.Command {
	return supportsJSONOutput(&cobra.Command{
		Use:               "prune",
		Short:             "Remove archived (invalid) sessions from the default target",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := commandContext(cmd)
			r, err := newSessionResolver()
			if err != nil {
				return err
			}
			defer r.Close()
			target, err := r.ResolveTarget(ctx, effectiveRunTarget(""))
			if err != nil {
				return err
			}
			count, names, err := target.Backend.Prune(ctx, target.ID)
			if err != nil {
				return fmt.Errorf("session prune: %w", err)
			}
			if jsonOutputEnabled(cmd) {
				if names == nil {
					names = []string{}
				}
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema": "amux.session.prune.v1",
					"count":  count,
					"names":  names,
				})
			}
			if count == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No archived sessions to prune.")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Pruned %d session(s): %s\n", count, strings.Join(names, ", "))
			return nil
		},
	})
}

func commandContext(cmd *cobra.Command) context.Context {
	ctx := cmd.Context()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func resolveSessionForCommand(ctx context.Context, ref string) (*resolvedSession, func(), error) {
	r, err := newSessionResolver()
	if err != nil {
		return nil, func() {}, err
	}
	resolved, err := r.ResolveSession(ctx, ref)
	if err != nil {
		_ = r.Close()
		return nil, func() {}, err
	}
	return resolved, func() { _ = r.Close() }, nil
}

func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func oneLine(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if max > 0 && len(runes) > max {
		return string(runes[:max-3]) + "..."
	}
	return text
}
