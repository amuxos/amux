package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/bridge"
	"github.com/amuxos/amux/internal/diagnose"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

type commandDiagnoseSource struct{}

func (commandDiagnoseSource) LoadControlPlane(ctx context.Context) ([]diagnose.Session, []*task.TaskRun, []string) {
	warnings := []string{}
	hub, err := newHubClientWithTimeout(hubAddr(), 3*time.Second)
	if err != nil {
		return nil, nil, []string{err.Error()}
	}
	defer hub.Close()

	entries, err := hub.List(ctx)
	if err != nil {
		warnings = append(warnings, "list sessions: "+err.Error())
	}
	sessions := make([]diagnose.Session, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		sessions = append(sessions, diagnose.Session{TargetID: entry.TargetID, TargetName: entry.TargetName,
			SessionID: entry.Id, SessionName: entry.Name, LaunchID: entry.LaunchId,
			OwnerTaskID: entry.OwnerTaskId, State: entry.State})
	}

	resp, err := pb.NewAmuxTaskClient(hub.conn).ListSubtasks(ctx, &pb.ListSubtasksRequest{})
	if err != nil {
		warnings = append(warnings, "list tasks: "+err.Error())
		return sessions, nil, warnings
	}
	runs := make([]*task.TaskRun, 0, len(resp.TasksJson))
	for _, raw := range resp.TasksJson {
		var run task.TaskRun
		if err := json.Unmarshal(raw, &run); err != nil {
			warnings = append(warnings, "decode task: "+err.Error())
			continue
		}
		runs = append(runs, &run)
	}
	return sessions, runs, warnings
}

func (commandDiagnoseSource) LookupTurnLinks(_ context.Context, query bridge.TurnLinkQuery) ([]bridge.TurnLink, error) {
	return bridge.LookupTurnLinks(bridge.DefaultDBPath(), query)
}

func newSystemDiagnoseCmd() *cobra.Command {
	var messageID, turnID, taskID, sessionID string
	cmd := supportsJSONOutput(&cobra.Command{
		Use:               "diagnose",
		Short:             "Correlate a message, turn, task, or session",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := oneDiagnoseQuery(messageID, turnID, taskID, sessionID)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			report := diagnose.Gather(ctx, commandDiagnoseSource{}, query)
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), report)
			}
			return writeDiagnoseReport(cmd.OutOrStdout(), report)
		},
	})
	cmd.Flags().StringVar(&messageID, "message-id", "", "Feishu message ID")
	cmd.Flags().StringVar(&turnID, "turn-id", "", "amux turn ID")
	cmd.Flags().StringVar(&taskID, "task-id", "", "distributed task ID")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "amux session ID")
	return cmd
}

func oneDiagnoseQuery(messageID, turnID, taskID, sessionID string) (diagnose.Query, error) {
	values := []diagnose.Query{{Kind: diagnose.QueryMessageID, Value: strings.TrimSpace(messageID)},
		{Kind: diagnose.QueryTurnID, Value: strings.TrimSpace(turnID)},
		{Kind: diagnose.QueryTaskID, Value: strings.TrimSpace(taskID)},
		{Kind: diagnose.QuerySessionID, Value: strings.TrimSpace(sessionID)}}
	var selected []diagnose.Query
	for _, value := range values {
		if value.Value != "" {
			selected = append(selected, value)
		}
	}
	if len(selected) != 1 {
		return diagnose.Query{}, fmt.Errorf("exactly one of --message-id, --turn-id, --task-id, or --session-id is required")
	}
	return selected[0], nil
}

func writeDiagnoseReport(out io.Writer, report diagnose.Report) error {
	fmt.Fprintf(out, "diagnose: %s=%s\n", report.Query.Kind, report.Query.Value)
	for _, link := range report.Links {
		fmt.Fprintf(out, "message %s -> turn %s (session %s, launch %s, target %s)\n",
			link.MessageID, link.TurnID, link.SessionID, link.LaunchID, link.TargetID)
	}
	for _, session := range report.Sessions {
		fmt.Fprintf(out, "session %s (%s/%s) launch=%s state=%s owner_task=%s\n",
			session.SessionID, session.TargetID, session.SessionName, session.LaunchID, session.State, session.OwnerTaskID)
	}
	for _, run := range report.Tasks {
		fmt.Fprintf(out, "task %s status=%s turn=%s parent_task=%s parent_turn=%s child_session=%s\n",
			run.ID, run.Status, run.TurnID, run.ParentTaskID, run.ParentTurnID, run.ChildSessionId)
	}
	if len(report.Links) == 0 && len(report.Sessions) == 0 && len(report.Tasks) == 0 {
		fmt.Fprintln(out, "no matching records")
	}
	for _, warning := range report.Warnings {
		fmt.Fprintln(out, "warning:", warning)
	}
	return nil
}
