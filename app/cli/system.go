package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/system"
	"github.com/amuxos/amux/internal/updatecheck"
	"github.com/spf13/cobra"
)

type systemLifecycleOpener func(io.Writer) (system.Lifecycle, func() error, error)

var systemUpdateStateReader = func() (updatecheck.State, error) {
	return updatecheck.ReadState(updatecheck.DefaultStatePath())
}

func newSystemCmd() *cobra.Command {
	return newSystemCmdWithOpener(openSystemLifecycle)
}

func newSystemCmdWithOpener(open systemLifecycleOpener) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "system",
		Short:             "Inspect and manage this amux installation",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		SilenceErrors:     true,
		SilenceUsage:      true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		newSystemStatusCmd(open),
		newSystemMutationCmd(open, system.ActionStart),
		newSystemMutationCmd(open, system.ActionStop),
		newSystemMutationCmd(open, system.ActionRestart),
		newSystemDoctorCmd(open),
		newSystemOperationCmd(open),
		newSystemLogsCmd(),
		newSystemDiagnoseCmd(),
		newSystemServiceCmd(),
		newConfigCmd(),
		newSystemTargetCmd(),
		newUninstallCmd(),
	)
	return cmd
}

func newSystemLogsCmd() *cobra.Command {
	return supportsJSONOutput(&cobra.Command{
		Use:               "logs",
		Short:             "Show installation log paths",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hubLog := filepath.Join(defaultAmuxDir(), "hub.log")
			runtimeLog := filepath.Join(defaultAmuxDir(), "runtime.log")
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"schema":  "amux.system.logs.v1",
					"hub":     hubLog,
					"runtime": runtimeLog,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "hub: %s\nruntime: %s\n",
				hubLog,
				runtimeLog,
			)
			return nil
		},
	})
}

func newSystemMutationCmd(open systemLifecycleOpener, action system.Action) *cobra.Command {
	var force bool
	var planOnly bool
	cmd := supportsJSONOutput(&cobra.Command{
		Use:               string(action),
		Short:             fmt.Sprintf("%s this amux installation", titleCaseAction(action)),
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := validatedConfiguredHubAddress(); err != nil {
				return err
			}
			request := system.ChangeRequest{
				Action: action,
				Scope:  system.CurrentScope(),
				Force:  force,
			}
			if planOnly {
				return runSystemPlanPreview(cmd, open, request, jsonOutputEnabled(cmd))
			}
			return runJournaledSystemChange(cmd, open, request, jsonOutputEnabled(cmd))
		},
	})
	cmd.Flags().BoolVar(&planOnly, "plan", false, "preview the lifecycle operation without changing anything")
	if action == system.ActionStop || action == system.ActionRestart {
		cmd.Flags().BoolVar(&force, "force", false, "continue even when active local work would be disrupted")
	}
	return cmd
}

func runSystemPlanPreview(cmd *cobra.Command, open systemLifecycleOpener, request system.ChangeRequest, jsonOutput bool) error {
	return withSystemLifecycle(cmd, open, func(lifecycle system.Lifecycle) error {
		plan, err := lifecycle.Plan(cmd.Context(), request)
		if err != nil {
			return err
		}
		preview := legacySystemPlanPreview(cmd.Context(), plan)
		return writeSystemPlanPreview(cmd.OutOrStdout(), preview, jsonOutput)
	})
}

func runJournaledSystemChange(cmd *cobra.Command, open systemLifecycleOpener, request system.ChangeRequest, jsonOutput bool) error {
	return withSystemLifecycle(cmd, open, func(lifecycle system.Lifecycle) error {
		ctx := cmd.Context()
		plan, err := lifecycle.Plan(ctx, request)
		if err != nil {
			return err
		}
		operationID, applyErr := lifecycle.Apply(ctx, plan)
		if operationID == "" {
			operationID = plan.OperationID
		}
		if operationID == "" {
			if applyErr != nil {
				return applyErr
			}
			return errors.New("system lifecycle returned an empty operation ID")
		}
		operation, getErr := lifecycle.GetOperation(ctx, operationID)
		if applyErr != nil {
			operationErr := fmt.Errorf("system operation %s failed: %w", operationID, applyErr)
			if getErr != nil {
				return errors.Join(operationErr, fmt.Errorf("get system operation %s after apply: %w", operationID, getErr))
			}
			return operationErr
		}
		if getErr != nil {
			return fmt.Errorf("get system operation %s after apply: %w", operationID, getErr)
		}
		if operation.State != system.OperationSucceeded {
			cause := strings.TrimSpace(operation.Error)
			if cause == "" {
				cause = fmt.Sprintf("terminal state is %s", operation.State)
			}
			return fmt.Errorf("system operation %s did not succeed: %s", operationID, cause)
		}
		if jsonOutput {
			return writeSystemOperation(cmd.OutOrStdout(), operation, true)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "system %s: complete (operation %s)\n", request.Action, operationID)
		return nil
	})
}

func titleCaseAction(action system.Action) string {
	value := string(action)
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func newSystemStatusCmd(open systemLifecycleOpener) *cobra.Command {
	cmd := supportsJSONOutput(&cobra.Command{
		Use:               "status",
		Short:             "Show installation status",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withSystemLifecycle(cmd, open, func(lifecycle system.Lifecycle) error {
				ctx, cancel := context.WithTimeout(cmd.Context(), 8*time.Second)
				defer cancel()
				snapshot, err := lifecycle.Inspect(ctx, system.CurrentScope())
				if err != nil {
					return err
				}
				enrichSystemSnapshotWithCachedUpdate(&snapshot)
				return writeSystemSnapshot(cmd.OutOrStdout(), snapshot, jsonOutputEnabled(cmd))
			})
		},
	})
	return cmd
}

func enrichSystemSnapshotWithCachedUpdate(snapshot *system.Snapshot) {
	if snapshot == nil {
		return
	}
	state, err := systemUpdateStateReader()
	if err != nil {
		return
	}
	currentVersion := strings.TrimSpace(snapshot.Version)
	if currentVersion == "" {
		currentVersion = state.CurrentVersion
	}
	if updatecheck.CompareVersions(currentVersion, state.LatestVersion) != updatecheck.ComparisonNewer {
		return
	}
	snapshot.Update = &system.UpdateSummary{
		Available:       true,
		CurrentVersion:  currentVersion,
		LatestVersion:   state.LatestVersion,
		CheckedAt:       state.CheckedAt,
		PromptDismissed: state.DismissedVersion == state.LatestVersion,
	}
}

func newSystemDoctorCmd(open systemLifecycleOpener) *cobra.Command {
	cmd := supportsJSONOutput(&cobra.Command{
		Use:               "doctor",
		Short:             "Diagnose this installation without changing it",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withSystemLifecycle(cmd, open, func(lifecycle system.Lifecycle) error {
				ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
				defer cancel()
				snapshot, err := lifecycle.Inspect(ctx, system.CurrentScope())
				if err != nil {
					return err
				}
				if err := writeSystemSnapshot(cmd.OutOrStdout(), snapshot, jsonOutputEnabled(cmd)); err != nil {
					return err
				}
				return requiredSystemChecksError(snapshot.Checks)
			})
		},
	})
	return cmd
}

func newSystemOperationCmd(open systemLifecycleOpener) *cobra.Command {
	cmd := &cobra.Command{
		Use:               "operation",
		Short:             "Inspect and cancel lifecycle operations",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
	}
	cmd.AddCommand(
		supportsJSONOutput(&cobra.Command{
			Use:               "list",
			Short:             "List lifecycle operations",
			Args:              cobra.NoArgs,
			ValidArgsFunction: completeNoFiles,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return withSystemLifecycle(cmd, open, func(lifecycle system.Lifecycle) error {
					snapshot, err := lifecycle.Inspect(cmd.Context(), system.Scope{Kind: system.ScopeOperations})
					if err != nil {
						return err
					}
					return writeSystemOperationList(cmd.OutOrStdout(), snapshot, jsonOutputEnabled(cmd))
				})
			},
		}),
		supportsJSONOutput(&cobra.Command{
			Use:               "show <operation-id>",
			Short:             "Show a lifecycle operation",
			Args:              cobra.ExactArgs(1),
			ValidArgsFunction: completeNoFiles,
			RunE: func(cmd *cobra.Command, args []string) error {
				return withSystemLifecycle(cmd, open, func(lifecycle system.Lifecycle) error {
					operation, err := lifecycle.GetOperation(cmd.Context(), system.OperationID(args[0]))
					if err != nil {
						return err
					}
					return writeSystemOperation(cmd.OutOrStdout(), operation, jsonOutputEnabled(cmd))
				})
			},
		}),
		supportsJSONOutput(&cobra.Command{
			Use:               "cancel <operation-id>",
			Short:             "Cancel a pending lifecycle operation",
			Args:              cobra.ExactArgs(1),
			ValidArgsFunction: completeNoFiles,
			RunE: func(cmd *cobra.Command, args []string) error {
				return withSystemLifecycle(cmd, open, func(lifecycle system.Lifecycle) error {
					result, err := lifecycle.Cancel(cmd.Context(), system.OperationID(args[0]))
					if err != nil {
						return err
					}
					if err := writeSystemCancelResult(cmd.OutOrStdout(), result, jsonOutputEnabled(cmd)); err != nil {
						return err
					}
					if !result.Accepted {
						reason := strings.TrimSpace(result.Reason)
						if reason == "" {
							reason = "cancellation was rejected"
						}
						return fmt.Errorf("system operation %s was not cancelled: %s (state %s)", result.OperationID, reason, result.State)
					}
					return nil
				})
			},
		}),
	)
	return cmd
}

func withSystemLifecycle(cmd *cobra.Command, open systemLifecycleOpener, run func(system.Lifecycle) error) (err error) {
	if open == nil {
		return errors.New("system lifecycle opener is required")
	}
	lifecycleOut := cmd.OutOrStdout()
	if jsonOutputEnabled(cmd) {
		lifecycleOut = io.Discard
	}
	lifecycle, closeLifecycle, err := open(lifecycleOut)
	if err != nil {
		return err
	}
	if lifecycle == nil {
		return errors.New("system lifecycle is required")
	}
	if closeLifecycle != nil {
		defer func() {
			if closeErr := closeLifecycle(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		}()
	}
	return run(lifecycle)
}

func requiredSystemChecksError(checks []system.Check) error {
	failed := make([]string, 0)
	for _, check := range checks {
		if check.Required && check.State != "pass" {
			failed = append(failed, check.Name)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("required system checks failed: %s", strings.Join(failed, ", "))
}
