package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

func newInterruptCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "interrupt <name>",
		Short:             "Interrupt the current turn without killing the session",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSingleSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			r, err := newSessionResolver()
			if err != nil {
				return err
			}
			defer r.Close()

			ctx := context.Background()
			resolved, err := r.ResolveSession(ctx, ref)
			if err != nil {
				return err
			}
			if err := resolved.Target.Backend.Interrupt(ctx, resolved.Target.ID, resolved.Session.Name); err != nil {
				return fmt.Errorf("interrupt: %w", err)
			}
			return nil
		},
	}
}
