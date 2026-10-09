package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/spf13/cobra"
)

type managedLocalActions interface {
	Create(context.Context, agentprofile.ManagedProfileSpec) (agentprofile.ManagedLibraryMutation, error)
	ForkBuiltin(context.Context, string, string) (agentprofile.ManagedLibraryMutation, error)
	UpdateInstructions(context.Context, string, string, string) (agentprofile.ManagedLibraryMutation, error)
	Snapshot(context.Context) (agentprofile.ManagedLibrarySnapshot, error)
	History(context.Context, int) ([]agentprofile.ManagedLibraryVersion, error)
	Restore(context.Context, string, string) (agentprofile.ManagedLibraryMutation, error)
}

var newManagedLocalActions = func() managedLocalActions {
	return agentprofile.NewDefaultManagedLibrary()
}

func newAgentLocalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "local",
		Short:  "Manage the amux-local expert library",
		Hidden: true,
	}
	cmd.AddCommand(
		newAgentLocalCreateCmd(),
		newAgentLocalUpdateCmd(),
		newAgentLocalStatusCmd(),
		newAgentLocalHistoryCmd(),
		newAgentLocalRestoreCmd(),
	)
	return cmd
}

func newAgentForkCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "fork <builtin-profile> <local-profile>",
		Short:             "Fork a read-only builtin profile into the local library",
		Hidden:            true,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			targetNamespace, targetName, ok := strings.Cut(strings.TrimSpace(args[1]), "/")
			if !ok || targetNamespace != agentprofile.ManagedLocalPackNamespace || strings.TrimSpace(targetName) == "" || strings.Contains(targetName, "/") {
				return fmt.Errorf("fork target must be local/<name>")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
			defer cancel()
			result, err := newManagedLocalActions().ForkBuiltin(ctx, args[0], targetName)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Forked %s to %s\nrevision: %s\n", args[0], result.ProfileRef, result.Revision)
			return nil
		},
	}
}

func newAgentLocalCreateCmd() *cobra.Command {
	var description string
	var instructions string
	var instructionsFile string
	cmd := &cobra.Command{
		Use:               "create <name>",
		Short:             "Create an expert in the amux-local library",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := readManagedInstructions(cmd, instructions, instructionsFile)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
			defer cancel()
			result, err := newManagedLocalActions().Create(ctx, agentprofile.ManagedProfileSpec{
				Name:         args[0],
				Description:  description,
				Instructions: content,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created local expert %s\nrevision: %s\n", result.ProfileRef, result.Revision)
			fmt.Fprintf(cmd.OutOrStdout(), "next: amux agent show %s\n", result.ProfileRef)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", commandCatalog.Format(commandLocale(cmd), "agent.description_help"))
	cmd.Flags().StringVar(&instructions, "instructions", "", "inline expert instructions")
	cmd.Flags().StringVar(&instructionsFile, "instructions-file", "", "read expert instructions from a file")
	_ = cmd.RegisterFlagCompletionFunc("instructions-file", completeFiles)
	return cmd
}

func newAgentLocalUpdateCmd() *cobra.Command {
	var instructions string
	var instructionsFile string
	var expectedRevision string
	cmd := &cobra.Command{
		Use:               "update <name>",
		Short:             "Update local expert instructions",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := readManagedInstructions(cmd, instructions, instructionsFile)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
			defer cancel()
			actions := newManagedLocalActions()
			if strings.TrimSpace(expectedRevision) == "" {
				snapshot, err := actions.Snapshot(ctx)
				if err != nil {
					return err
				}
				if !snapshot.Initialized || strings.TrimSpace(snapshot.Revision) == "" {
					return fmt.Errorf("local expert library is not initialized; run `amux agent local create <name> ...`")
				}
				expectedRevision = snapshot.Revision
			}
			result, err := actions.UpdateInstructions(ctx, args[0], content, expectedRevision)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Updated local expert %s\nrevision: %s\n", result.ProfileRef, result.Revision)
			return nil
		},
	}
	cmd.Flags().StringVar(&instructions, "instructions", "", "inline expert instructions")
	cmd.Flags().StringVar(&instructionsFile, "instructions-file", "", "read expert instructions from a file")
	cmd.Flags().StringVar(&expectedRevision, "expected-revision", "", "require this full library revision; omitted uses the current revision")
	_ = cmd.RegisterFlagCompletionFunc("instructions-file", completeFiles)
	_ = cmd.RegisterFlagCompletionFunc("expected-revision", completeNoFiles)
	return cmd
}

func newAgentLocalStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "status",
		Short:             "Show the amux-local expert library",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			snapshot, err := newManagedLocalActions().Snapshot(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "initialized: %t\n", snapshot.Initialized)
			fmt.Fprintf(out, "namespace: %s\n", snapshot.Namespace)
			fmt.Fprintf(out, "origin: %s\n", snapshot.OriginPath)
			if !snapshot.Initialized {
				fmt.Fprintln(out, "next: amux agent local create <name> --instructions-file <path>")
				return nil
			}
			fmt.Fprintf(out, "revision: %s\n", snapshot.Revision)
			if snapshot.MaterializedPath != "" {
				fmt.Fprintf(out, "materialized: %s\n", snapshot.MaterializedPath)
			}
			fmt.Fprintln(out, "experts:")
			for _, name := range snapshot.Profiles {
				fmt.Fprintf(out, "  %s/%s\n", snapshot.Namespace, name)
			}
			return nil
		},
	}
}

func newAgentLocalHistoryCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:               "history",
		Short:             "Show local expert library history",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			versions, err := newManagedLocalActions().History(cmd.Context(), limit)
			if err != nil {
				return err
			}
			for _, version := range versions {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", version.Revision, version.Time.Format(time.RFC3339), version.Subject)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "maximum history entries")
	return cmd
}

func newAgentLocalRestoreCmd() *cobra.Command {
	var expectedRevision string
	cmd := &cobra.Command{
		Use:               "restore <revision>",
		Short:             "Restore a historical local library tree as a new commit",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
			defer cancel()
			actions := newManagedLocalActions()
			if strings.TrimSpace(expectedRevision) == "" {
				snapshot, err := actions.Snapshot(ctx)
				if err != nil {
					return err
				}
				if !snapshot.Initialized || strings.TrimSpace(snapshot.Revision) == "" {
					return fmt.Errorf("local expert library is not initialized")
				}
				expectedRevision = snapshot.Revision
			}
			result, err := actions.Restore(ctx, args[0], expectedRevision)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Restored local expert library\nrevision: %s\n", result.Revision)
			return nil
		},
	}
	cmd.Flags().StringVar(&expectedRevision, "expected-revision", "", "require this full current revision")
	return cmd
}

func readManagedInstructions(cmd *cobra.Command, inline, path string) (string, error) {
	inlineSet := cmd.Flags().Changed("instructions")
	fileSet := cmd.Flags().Changed("instructions-file")
	if inlineSet == fileSet {
		return "", fmt.Errorf("exactly one of --instructions or --instructions-file is required")
	}
	if inlineSet {
		return inline, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read instructions file %s: %w", path, err)
	}
	return string(data), nil
}
