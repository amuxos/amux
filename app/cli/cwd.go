package cli

import (
	"fmt"
	"os"
	"strings"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

func newSessionCwdCmd() *cobra.Command {
	var interrupt bool
	var noWait bool
	var afterTurn bool
	c := supportsJSONOutput(&cobra.Command{
		Use:               "cwd <session-ref> [path]",
		Short:             "Show or change a session working directory",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeSingleSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if afterTurn && (interrupt || noWait) {
				return fmt.Errorf("--after-turn cannot be combined with --interrupt or --no-wait")
			}
			ctx := commandContext(cmd)
			resolved, cleanup, err := resolveSessionForCommand(ctx, args[0])
			if err != nil {
				return err
			}
			defer cleanup()
			if len(args) == 1 {
				if jsonOutputEnabled(cmd) {
					return printJSON(cmd.OutOrStdout(), map[string]any{
						"schema":      "amux.session.cwd.v1",
						"session_ref": sessionDisplayRef(resolved.Target, resolved.Session),
						"cwd":         resolved.Session.Cwd,
					})
				}
				fmt.Fprintln(cmd.OutOrStdout(), resolved.Session.Cwd)
				return nil
			}
			deferred := afterTurn || shouldAutoDeferCwdSwitch(resolved.Session.Id, interrupt, noWait)
			resp, err := resolved.Target.Backend.ChangeCwd(ctx, resolved.Target.ID, &pb.ChangeCwdRequest{
				Name:           resolved.Session.Name,
				Cwd:            args[1],
				Wait:           !deferred && (!noWait || interrupt),
				Interrupt:      interrupt,
				ExpectedCwd:    resolved.Session.Cwd,
				DeferUntilIdle: deferred,
			})
			if err != nil {
				return fmt.Errorf("session cwd: %w", err)
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":        "amux.session.cwd.v1",
					"session_ref":   sessionDisplayRef(resolved.Target, resolved.Session),
					"previous_cwd":  resp.PreviousCwd,
					"effective_cwd": resp.EffectiveCwd,
					"changed":       resp.Changed,
					"deferred":      resp.Deferred,
				})
			}
			if resp.Deferred {
				fmt.Fprintf(cmd.OutOrStdout(), "Session %q workspace switch scheduled after the current turn:\n  %s -> %s\nConversation will be preserved.\n",
					sessionDisplayRef(resolved.Target, resolved.Session), resp.PreviousCwd, resp.EffectiveCwd)
				return nil
			}
			if !resp.Changed {
				fmt.Fprintf(cmd.OutOrStdout(), "Session %q is already using %s.\n", sessionDisplayRef(resolved.Target, resolved.Session), resp.EffectiveCwd)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Session %q workspace changed:\n  %s -> %s\nConversation preserved.\n",
				sessionDisplayRef(resolved.Target, resolved.Session), resp.PreviousCwd, resp.EffectiveCwd)
			return nil
		},
	})
	c.Flags().BoolVar(&interrupt, "interrupt", false, "interrupt the active turn, then change cwd")
	c.Flags().BoolVar(&noWait, "no-wait", false, "fail instead of waiting for the active turn to finish")
	c.Flags().BoolVar(&afterTurn, "after-turn", false, "queue the cwd switch after the active turn and return immediately")
	return c
}

func shouldAutoDeferCwdSwitch(targetSessionID string, interrupt, noWait bool) bool {
	if interrupt || noWait {
		return false
	}
	selfSessionID := strings.TrimSpace(os.Getenv("AMUX_SESSION_ID"))
	return selfSessionID != "" && selfSessionID == strings.TrimSpace(targetSessionID)
}
