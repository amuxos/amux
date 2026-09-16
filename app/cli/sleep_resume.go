package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newSleepCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "sleep <name>",
		Short:             "Stop a session process while keeping it resumable",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSingleSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withResolvedCommandSession(args[0], func(resolved *resolvedSession) error {
				if err := resolved.Target.Backend.Sleep(context.Background(), resolved.Target.ID, resolved.Session.Name); err != nil {
					return fmt.Errorf("sleep: %w", err)
				}
				fmt.Printf("Session %q sleeping.\n", sessionDisplayRef(resolved.Target, resolved.Session))
				return nil
			})
		},
	}
}

func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "resume <name>",
		Short:             "Resume a sleeping session on its current runtime",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeSingleSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withResolvedCommandSession(args[0], func(resolved *resolvedSession) error {
				if err := resolved.Target.Backend.Resume(context.Background(), resolved.Target.ID, resolved.Session.Name); err != nil {
					return fmt.Errorf("resume: %w", err)
				}
				fmt.Printf("Session %q resumed.\n", sessionDisplayRef(resolved.Target, resolved.Session))
				return nil
			})
		},
	}
}

func withResolvedCommandSession(ref string, fn func(*resolvedSession) error) error {
	r, err := newSessionResolver()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer r.Close()
	resolved, err := r.ResolveSession(context.Background(), ref)
	if err != nil {
		return err
	}
	return fn(resolved)
}
