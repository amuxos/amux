package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func newSendCmd() *cobra.Command {
	var interrupt bool
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
	return c
}
