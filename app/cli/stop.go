package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "stop <name>",
		Short:             "Stop a session and its agent process",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSingleSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			r, err := newSessionResolver()
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			defer r.Close()

			ctx := context.Background()
			resolved, err := r.ResolveSession(ctx, ref)
			if err != nil {
				return err
			}
			if err := resolved.Target.Backend.Kill(ctx, resolved.Target.ID, resolved.Session.Name); err != nil {
				return fmt.Errorf("stop: %w", err)
			}
			fmt.Printf("Session %q stopped.\n", sessionDisplayRef(resolved.Target, resolved.Session))
			return nil
		},
	}
}
