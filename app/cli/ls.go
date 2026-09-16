package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

type lsOptions struct {
	All          bool
	IncludeTasks bool
}

func newLsCmd() *cobra.Command {
	return newLsCmdWithResolver(newSessionResolver)
}

func newLsCmdWithResolver(newResolver func() (*sessionResolver, error)) *cobra.Command {
	var opts lsOptions
	cmd := supportsJSONOutput(&cobra.Command{
		Use:               "ls [target]",
		Short:             "List sessions",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeSingleTargetArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := newResolver()
			if err != nil {
				return err
			}
			defer r.Close()

			ctx := context.Background()
			targets, err := r.listTargets(ctx)
			if err != nil {
				return fmt.Errorf("list: %w", err)
			}
			var sessions []*resolvedSession
			for _, ts := range targets {
				if len(args) == 1 && !targetMatchesRef(ts.target, args[0]) {
					continue
				}
				for _, sess := range ts.sessions {
					if !shouldShowSessionForLs(sess, opts) {
						continue
					}
					sessions = append(sessions, &resolvedSession{Target: ts.target, Session: sess})
				}
			}
			return formatSessionList(cmd.OutOrStdout(), sessions, jsonOutputEnabled(cmd))
		},
	})
	cmd.Flags().BoolVar(&opts.All, "all", false, "show all sessions, including task child sessions")
	cmd.Flags().BoolVar(&opts.IncludeTasks, "include-tasks", false, "include task child sessions")
	return cmd
}

func shouldShowSessionForLs(s *sessionEntry, opts lsOptions) bool {
	if s == nil {
		return false
	}
	if opts.All || opts.IncludeTasks {
		return true
	}
	// Task-owned child sessions are an execution detail of a task and should
	// not pollute the default `amux session list` listing, regardless of their state.
	// Use --all or --include-tasks to surface them for diagnostics.
	return s.OwnerTaskId == ""
}

func formatSessionList(out io.Writer, sessions []*resolvedSession, jsonOutput bool) error {
	if jsonOutput {
		views := make([]sessionJSONView, 0, len(sessions))
		for _, resolved := range sessions {
			views = append(views, newSessionJSONView(resolved))
		}
		return printJSON(out, map[string]any{
			"schema":   "amux.session.list.v1",
			"sessions": views,
		})
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tPROFILE\tSTATE\tPID\tTARGET\tCAPS\tIDLE_TIMEOUT\tLAST_ACTIVITY\tSLEEP_REASON\tAUTO_SLEEP\tCWD\tCREATED\tUPDATED")
	for _, resolved := range sessions {
		s := resolved.Session
		created := time.Unix(s.CreatedAt, 0).Format("2006-01-02 15:04:05")
		updated := "-"
		if s.UpdatedAt > 0 {
			updated = time.Unix(s.UpdatedAt, 0).Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			s.Name, s.AgentType, valueOrDash(s.AgentProfile), s.State, s.Pid, targetDisplayName(resolved.Target), sessionCapabilities(s),
			formatIdleTimeout(s.IdleTimeoutSeconds), formatUnixTime(s.LastActivityAt), valueOrDash(s.LastSleepReason), boolText(s.AutoSleepEligible),
			valueOrDash(s.Cwd), created, updated)
	}
	return w.Flush()
}

type sessionJSONView struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	AgentType         string `json:"agent_type"`
	AgentProfile      string `json:"agent_profile,omitempty"`
	EffectiveModel    string `json:"effective_model,omitempty"`
	State             string `json:"state"`
	PID               int32  `json:"pid"`
	TargetID          string `json:"target_id,omitempty"`
	TargetName        string `json:"target_name"`
	ACP               bool   `json:"acp"`
	Interrupt         bool   `json:"interrupt"`
	IdleTimeout       int64  `json:"idle_timeout_seconds"`
	LastActivityAt    int64  `json:"last_activity_at,omitempty"`
	LastSleepReason   string `json:"last_sleep_reason,omitempty"`
	AutoSleepEligible bool   `json:"auto_sleep_eligible"`
	Cwd               string `json:"cwd,omitempty"`
	OwnerTaskID       string `json:"owner_task_id,omitempty"`
	CreatedAt         int64  `json:"created_at"`
	UpdatedAt         int64  `json:"updated_at,omitempty"`
}

func newSessionJSONView(resolved *resolvedSession) sessionJSONView {
	if resolved == nil || resolved.Session == nil {
		return sessionJSONView{}
	}
	session := resolved.Session
	return sessionJSONView{
		ID:                session.Id,
		Name:              session.Name,
		AgentType:         session.AgentType,
		AgentProfile:      session.AgentProfile,
		EffectiveModel:    session.EffectiveModel,
		State:             session.State,
		PID:               session.Pid,
		TargetID:          resolved.Target.ID,
		TargetName:        targetDisplayName(resolved.Target),
		ACP:               session.Acp,
		Interrupt:         session.Interrupt,
		IdleTimeout:       session.IdleTimeoutSeconds,
		LastActivityAt:    session.LastActivityAt,
		LastSleepReason:   session.LastSleepReason,
		AutoSleepEligible: session.AutoSleepEligible,
		Cwd:               session.Cwd,
		OwnerTaskID:       session.OwnerTaskId,
		CreatedAt:         session.CreatedAt,
		UpdatedAt:         session.UpdatedAt,
	}
}

func targetDisplayName(target targetRef) string {
	if target.Name != "" {
		return target.Name
	}
	if target.ID != "" {
		return target.ID
	}
	return "local"
}

func sessionCapabilities(s *sessionEntry) string {
	if s == nil {
		return "-"
	}
	var caps []string
	if s.Acp {
		caps = append(caps, "acp")
	}
	if s.Interrupt {
		caps = append(caps, "interrupt")
	}
	if len(caps) == 0 {
		return "-"
	}
	return strings.Join(caps, ",")
}

func valueOrDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

func formatIdleTimeout(seconds int64) string {
	if seconds <= 0 {
		return "-"
	}
	return fmt.Sprintf("%ds", seconds)
}

func formatUnixTime(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

func boolText(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
