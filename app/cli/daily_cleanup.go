package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/amuxos/amux/internal/cleanup"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

func cleanupText(cmd *cobra.Command, key string, args ...any) string {
	return commandCatalog.Text("cleanup."+key, args...).For(commandLocale(cmd))
}
func newSystemCleanupCmd() *cobra.Command {
	var apply, dry bool
	var target string
	cmd := supportsJSONOutput(&cobra.Command{Use: "cleanup", Args: cobra.NoArgs, ValidArgsFunction: completeNoFiles})
	cmd.Short = cleanupText(cmd, "short")
	cmd.Flags().BoolVar(&apply, "apply", false, cleanupText(cmd, "apply"))
	cmd.Flags().BoolVar(&dry, "dry-run", false, cleanupText(cmd, "dry_run"))
	cmd.Flags().StringVar(&target, "target", "", cleanupText(cmd, "target"))
	cmd.MarkFlagsMutuallyExclusive("apply", "dry-run")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := loadUserConfig(); err != nil {
			return err
		}
		client, err := newHubClient(hubAddr())
		if err != nil {
			return err
		}
		defer client.Close()
		ctx, cancel := context.WithTimeout(commandContext(cmd), 15*time.Minute)
		defer cancel()
		result, err := client.c.Cleanup(ctx, &pb.CleanupRequest{TargetId: target, DryRun: !apply})
		if err != nil {
			return fmt.Errorf("%s: %w", cleanupText(cmd, "failed"), err)
		}
		reports := []cleanup.Report{}
		failed := false
		for _, item := range result.Reports {
			r := cleanup.FromProto(item)
			reports = append(reports, r)
			failed = failed || r.Failed()
		}
		if jsonOutputEnabled(cmd) {
			if err := printJSON(cmd.OutOrStdout(), map[string]any{"schema": "amux.system.cleanup.v1", "reports": reports}); err != nil {
				return err
			}
		} else {
			mode := cleanupText(cmd, "preview")
			if apply {
				mode = cleanupText(cmd, "applied")
			}
			fmt.Fprintln(cmd.OutOrStdout(), mode)
			for _, r := range reports {
				fmt.Fprintln(cmd.OutOrStdout(), r.Target)
				for _, s := range r.Steps {
					fmt.Fprintln(cmd.OutOrStdout(), cleanupText(cmd, "step", cleanupStepText(cmd, s.Name), s.Candidates, s.Deleted, s.Skipped, float64(s.Bytes)/(1024*1024), len(s.Errors)))
				}
			}
		}
		if failed {
			return fmt.Errorf("%s", cleanupText(cmd, "partial"))
		}
		return nil
	}
	return cmd
}

func cleanupStepText(cmd *cobra.Command, name string) string {
	switch name {
	case "sessions", "tasks", "workspaces", "session_artifacts", "provider_rollouts", "provider_trust", "provider_home", "logs", "database", "isolation", "references":
	default:
		name = "other"
	}
	return cleanupText(cmd, "step."+name)
}
