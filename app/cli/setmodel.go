package cli

import (
	"fmt"

	"github.com/amuxos/amux/internal/clientop"
	"github.com/spf13/cobra"
)

// newSetModelSessionResolver is replaceable in tests.
var newSetModelSessionResolver = newSessionResolver

func newSessionSetModelCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set-model <session-ref> <model>",
		Short: "Switch a session's model while retaining its conversation history",
		Long: `Switch the model a session runs on while retaining its amux session
identity, workspace, and conversation history.

The model may be a native model name or a provider-backed model ref declared
by the session's Agent Profile (for example custom/example-model); the bare
upstream model name (for example example-model) resolves to the matching
provider-backed option when it is unambiguous. Switching to or across a
provider-backed model starts a fresh provider conversation and carries the
history over through an amux conversation handoff.

When the session is idle and the runtime supports in-place reconfiguration
(Codex-compatible app-server with an unchanged Agent Profile and a native target model), the
model switches without restarting the agent process. Otherwise the session
restarts in place and the conversation carries over through the provider's
native resume, or through an amux conversation handoff when the runtime
cannot resume.

Sessions owned by a task or agent instance do not support set-model.`,
		Example: `  amux session set-model mysession gpt-5.1-codex-mini
  amux session set-model mysession custom/example-model
  amux session set-model remote/mysession claude-sonnet-4-5`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeSingleSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := commandContext(cmd)
			r, err := newSetModelSessionResolver()
			if err != nil {
				return err
			}
			defer func() { _ = r.Close() }()
			resolved, err := r.ResolveSession(ctx, args[0])
			if err != nil {
				return err
			}
			replacement, err := clientop.BuildSetModelReplacement(&resolved.Session.Session, args[1])
			if err != nil {
				return err
			}
			resp, err := resolved.Target.Backend.SetModel(ctx, resolved.Target.ID, resolved.Session.Name, replacement)
			if err != nil {
				return fmt.Errorf("session set-model: %w", err)
			}
			effective := args[1]
			if resp != nil && resp.Session.GetEffectiveModel() != "" {
				effective = resp.Session.GetEffectiveModel()
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Session %q now runs model %q; conversation history retained.\n",
				sessionDisplayRef(resolved.Target, resolved.Session), effective)
			return nil
		},
	}
	return c
}
