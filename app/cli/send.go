package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func newSendCmd() *cobra.Command {
	var interrupt, steer bool
	c := &cobra.Command{
		Use:               "send <session-ref> <text>",
		Short:             "Send one message to a session without attaching",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeSendArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			r, err := newSessionResolver()
			if err != nil {
				return err
			}
			defer r.Close()

			resolved, err := r.ResolveSession(ctx, args[0])
			if err != nil {
				return err
			}
			if steer && interrupt {
				return fmt.Errorf("send: --steer and --interrupt are mutually exclusive")
			}
			if steer {
				steerer, ok := resolved.Target.Backend.(steerSubmitter)
				if !ok {
					return fmt.Errorf("send: target backend does not support --steer")
				}
				delivery, reason, err := steerer.SubmitSteer(ctx, resolved.Target.ID, resolved.Session.Name, args[1])
				if err != nil {
					return fmt.Errorf("send: %w", err)
				}
				if reason != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n", delivery, reason)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), delivery)
				}
				return nil
			}
			submit := resolved.Target.Backend.SubmitAsync
			if interrupt {
				submit = resolved.Target.Backend.SubmitPreempt
			}
			if err := submit(ctx, resolved.Target.ID, resolved.Session.Name, args[1]); err != nil {
				return fmt.Errorf("send: %w", err)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&interrupt, "interrupt", false, "interrupt the current turn and run this message ahead of queued inputs")
	c.Flags().BoolVar(&steer, "steer", false, "inject this message into the running turn without interrupting it; falls back to the queue")
	return c
}

// steerSubmitter is implemented by backends that can steer a running turn.
type steerSubmitter interface {
	SubmitSteer(ctx context.Context, targetID, name, text string) (delivery, fallbackReason string, err error)
}
