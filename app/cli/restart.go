package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newSessionRestartCmd() *cobra.Command {
	var running bool
	var refreshProfile bool
	c := &cobra.Command{
		Use:               "restart <session-ref>",
		Short:             "Restart one session or all running local user sessions",
		Args:              restartSessionArgs(&running),
		ValidArgsFunction: completeSingleSessionArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := commandContext(cmd)
			if running {
				if refreshProfile {
					return fmt.Errorf("--refresh-profile cannot be used with --running")
				}
				result, err := restartLocalUserSessions(ctx)
				if err != nil {
					return err
				}
				printRestartedLocalSessions(cmd.OutOrStdout(), result)
				return result.Err()
			}
			resolved, cleanup, err := resolveSessionForCommand(ctx, args[0])
			if err != nil {
				return err
			}
			defer cleanup()
			if err := resolved.Target.Backend.Restart(ctx, resolved.Target.ID, resolved.Session.Name, refreshProfile); err != nil {
				return fmt.Errorf("session restart: %w", err)
			}
			if refreshProfile {
				fmt.Fprintf(cmd.OutOrStdout(), "Session %q restarted with its current Agent Profile.\n", sessionDisplayRef(resolved.Target, resolved.Session))
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Session %q restarted.\n", sessionDisplayRef(resolved.Target, resolved.Session))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&running, "running", false, "restart all running local user sessions")
	c.Flags().BoolVar(&refreshProfile, "refresh-profile", false, "refresh the session from the current Agent Profile revision and start a new provider context")
	return c
}

func restartSessionArgs(running *bool) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if running != nil && *running {
			if len(args) != 0 {
				return fmt.Errorf("--running does not accept a session-ref")
			}
			return nil
		}
		return cobra.ExactArgs(1)(cmd, args)
	}
}

type restartSessionFailure struct {
	Name  string
	Error error
}

type restartLocalSessionsResult struct {
	Restarted []string
	Failed    []restartSessionFailure
}

func (r restartLocalSessionsResult) Err() error {
	if len(r.Failed) == 0 {
		return nil
	}
	return fmt.Errorf("failed to restart %d local session(s):\n%s", len(r.Failed), formatRestartSessionFailures(r.Failed))
}

func restartLocalUserSessions(ctx context.Context) (restartLocalSessionsResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := waitForLocalClient(ctx)
	if err != nil {
		return restartLocalSessionsResult{}, fmt.Errorf("restart local sessions: %w", err)
	}
	defer client.Close()
	sessions, err := client.List(ctx)
	if err != nil {
		return restartLocalSessionsResult{}, fmt.Errorf("list local sessions: %w", err)
	}
	var result restartLocalSessionsResult
	for _, sess := range sessions {
		if !restartableUpdateSession(sess) {
			continue
		}
		if err := client.Restart(ctx, sess.Name, false); err != nil {
			result.Failed = append(result.Failed, restartSessionFailure{Name: sess.Name, Error: err})
			continue
		}
		result.Restarted = append(result.Restarted, sess.Name)
	}
	return result, nil
}

func waitForLocalClient(ctx context.Context) (*localRuntimeClient, error) {
	var lastErr error
	for {
		client, err := newLocalRuntimeClientWithTimeout(300 * time.Millisecond)
		if err == nil {
			return client, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func restartableUpdateSession(sess *sessionEntry) bool {
	if sess == nil {
		return false
	}
	return sess.State == "running" && sess.OwnerTaskId == ""
}

func printRestartedLocalSessions(out io.Writer, result restartLocalSessionsResult) {
	if len(result.Restarted) > 0 {
		fmt.Fprintf(out, "Restarted local session(s): %s\n", strings.Join(result.Restarted, ", "))
	} else {
		fmt.Fprintln(out, "Restarted local session(s): none")
	}
	if len(result.Failed) == 0 {
		return
	}
	fmt.Fprintln(out, "Failed local session restart(s):")
	fmt.Fprintln(out, formatRestartSessionFailures(result.Failed))
}

func formatRestartSessionFailures(failures []restartSessionFailure) string {
	lines := make([]string, 0, len(failures))
	for _, failure := range failures {
		lines = append(lines, fmt.Sprintf("  - %s: %s", failure.Name, failure.Error))
	}
	return strings.Join(lines, "\n")
}
