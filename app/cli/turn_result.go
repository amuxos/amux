package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/i18n"
	"github.com/amuxos/amux/internal/turnresult"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

const (
	defaultTurnResultWaitTimeout = 60 * time.Second
	turnResultPollInterval       = 500 * time.Millisecond
)

func newTurnCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "turn",
		Short: commandCatalog.Format(commandLocale(&cobra.Command{}), "turn.cmd.short"),
	}
	c.AddCommand(newTurnResultCmd())
	return c
}

func newTurnResultCmd() *cobra.Command {
	var feishuMessageID string
	var bridgeID string
	var wait bool
	var timeout time.Duration

	c := supportsJSONOutput(&cobra.Command{
		Use:   "result [session-ref] [turn-id]",
		Short: commandCatalog.Format(commandLocale(&cobra.Command{}), "turn.result.short"),
		Example: `  amux turn result local/chat turn_123 --json
  amux turn result local/chat latest
  amux turn result --feishu-message om_xxx --json
  amux turn result local/chat turn_123 --wait --timeout 60s --json`,
		Args: cobra.RangeArgs(0, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			locale := commandLocale(cmd)
			feishuMessageID = strings.TrimSpace(feishuMessageID)
			bridgeID = strings.TrimSpace(bridgeID)
			if wait && timeout <= 0 {
				return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "turn.result.invalid_timeout")))
			}

			req := &pb.AmuxdGetTurnResultRequest{}
			switch {
			case feishuMessageID != "" && len(args) == 0:
				req.FeishuMessageId = feishuMessageID
				req.BridgeId = bridgeID
			case feishuMessageID == "" && len(args) == 2:
				ctx := cmdContext(cmd)
				r, err := newSessionResolver()
				if err != nil {
					return err
				}
				defer r.Close()
				resolved, err := r.ResolveSession(ctx, args[0])
				if err != nil {
					return err
				}
				req.TargetId = resolved.Target.ID
				req.SessionId = resolved.Session.Id
				req.SessionName = resolved.Session.Name
				req.TurnId = strings.TrimSpace(args[1])
			default:
				return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "turn.result.invalid_selector")))
			}

			client, closeClient, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeClient()

			resp, err := fetchTurnResult(cmdContext(cmd), client, req, wait, timeout)
			if err != nil {
				return fmt.Errorf("get turn result: %w", err)
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), turnResultJSONEnvelope(resp))
			}
			renderTurnResultText(cmd.OutOrStdout(), locale, resp)
			return nil
		},
	})
	locale := commandLocale(c)
	c.Flags().StringVar(&feishuMessageID, "feishu-message", "", commandCatalog.Format(locale, "turn.result.flag.feishu_message"))
	c.Flags().StringVar(&bridgeID, "bridge", "", commandCatalog.Format(locale, "turn.result.flag.bridge"))
	c.Flags().BoolVar(&wait, "wait", false, commandCatalog.Format(locale, "turn.result.flag.wait"))
	c.Flags().DurationVar(&timeout, "timeout", defaultTurnResultWaitTimeout, commandCatalog.Format(locale, "turn.result.flag.timeout"))
	return c
}

func newScheduleResultCmd() *cobra.Command {
	var executionID int64
	var wait bool
	var timeout time.Duration

	c := supportsJSONOutput(&cobra.Command{
		Use:   "result <id>",
		Short: commandCatalog.Format(commandLocale(&cobra.Command{}), "schedule.result.short"),
		Example: `  amux schedule result 8
  amux schedule result 8 --json
  amux schedule result 8 --execution 42 --json`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeScheduleIDs(scheduleAny),
		RunE: func(cmd *cobra.Command, args []string) error {
			locale := commandLocale(cmd)
			scheduleID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || scheduleID <= 0 {
				return commandValidationError(fmt.Errorf("invalid schedule id %q", args[0]))
			}
			if executionID < 0 {
				return commandValidationError(fmt.Errorf("invalid execution id %d", executionID))
			}
			if wait && timeout <= 0 {
				return commandValidationError(fmt.Errorf("%s", commandCatalog.Format(locale, "turn.result.invalid_timeout")))
			}

			client, closeClient, err := newAmuxdClient()
			if err != nil {
				return err
			}
			defer closeClient()

			req := &pb.AmuxdGetTurnResultRequest{
				ScheduleId:  scheduleID,
				ExecutionId: executionID,
			}
			resp, err := fetchTurnResult(cmdContext(cmd), client, req, wait, timeout)
			if err != nil {
				return fmt.Errorf("get schedule result: %w", err)
			}
			if jsonOutputEnabled(cmd) {
				return printJSON(cmd.OutOrStdout(), turnResultJSONEnvelope(resp))
			}
			renderTurnResultText(cmd.OutOrStdout(), locale, resp)
			return nil
		},
	})
	locale := commandLocale(c)
	c.Flags().Int64Var(&executionID, "execution", 0, commandCatalog.Format(locale, "schedule.result.flag.execution"))
	c.Flags().BoolVar(&wait, "wait", false, commandCatalog.Format(locale, "turn.result.flag.wait"))
	c.Flags().DurationVar(&timeout, "timeout", defaultTurnResultWaitTimeout, commandCatalog.Format(locale, "turn.result.flag.timeout"))
	return c
}

func fetchTurnResult(ctx context.Context, client pb.AmuxdClient, req *pb.AmuxdGetTurnResultRequest, wait bool, timeout time.Duration) (*pb.AmuxdGetTurnResultResponse, error) {
	if !wait {
		return client.GetTurnResult(ctx, req)
	}
	deadline := time.Now().Add(timeout)
	for {
		resp, err := client.GetTurnResult(ctx, req)
		if err != nil {
			return nil, err
		}
		if resp.GetStatus() != turnresult.StatusNotReady {
			return resp, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return resp, nil
		}
		sleep := turnResultPollInterval
		if remaining < sleep {
			sleep = remaining
		}
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func turnResultJSONEnvelope(resp *pb.AmuxdGetTurnResultResponse) map[string]any {
	out := map[string]any{
		"schema": "amux.turn.result.v1",
		"status": resp.GetStatus(),
	}
	if reason := resp.GetReason(); reason != "" {
		out["reason"] = reason
	}
	if r := resp.GetResult(); r != nil {
		out["result"] = turnResultPayloadView(r)
	}
	return out
}

func turnResultPayloadView(r *pb.TurnResultPayload) map[string]any {
	ref := map[string]any{}
	if v := r.GetRef(); v != nil {
		if v.GetTargetId() != "" {
			ref["target_id"] = v.GetTargetId()
		}
		if v.GetSessionId() != "" {
			ref["session_id"] = v.GetSessionId()
		}
		if v.GetSessionName() != "" {
			ref["session_name"] = v.GetSessionName()
		}
		if v.GetLaunchId() != "" {
			ref["launch_id"] = v.GetLaunchId()
		}
		if v.GetTurnId() != "" {
			ref["turn_id"] = v.GetTurnId()
		}
	}

	source := map[string]any{}
	if v := r.GetSource(); v != nil {
		if v.GetKind() != "" {
			source["kind"] = v.GetKind()
		}
		if v.GetBridgeId() != "" {
			source["bridge_id"] = v.GetBridgeId()
		}
		if v.GetChatId() != "" {
			source["chat_id"] = v.GetChatId()
		}
		if v.GetRootMessageId() != "" {
			source["root_message_id"] = v.GetRootMessageId()
		}
		if len(v.GetMessageIds()) > 0 {
			source["message_ids"] = v.GetMessageIds()
		}
		if v.GetScheduleId() > 0 {
			source["schedule_id"] = v.GetScheduleId()
		}
		if v.GetExecutionId() > 0 {
			source["execution_id"] = v.GetExecutionId()
		}
		if v.GetScheduleType() != "" {
			source["schedule_type"] = v.GetScheduleType()
		}
		if v.GetRunMode() != "" {
			source["run_mode"] = v.GetRunMode()
		}
		if v.GetScheduledAt() > 0 {
			source["scheduled_at"] = v.GetScheduledAt()
		}
		if v.GetTaskId() != "" {
			source["task_id"] = v.GetTaskId()
		}
	}

	execution := map[string]any{}
	if v := r.GetExecution(); v != nil {
		if v.GetStatus() != "" {
			execution["status"] = v.GetStatus()
		}
		if v.GetCode() != "" {
			execution["code"] = v.GetCode()
		}
		if v.GetStartedAt() > 0 {
			execution["started_at"] = v.GetStartedAt()
		}
		if v.GetFinishedAt() > 0 {
			execution["finished_at"] = v.GetFinishedAt()
		}
	}

	output := map[string]any{}
	if v := r.GetOutput(); v != nil {
		if v.GetStatus() != "" {
			output["status"] = v.GetStatus()
		}
		if v.GetReason() != "" {
			output["reason"] = v.GetReason()
		}
		if v.GetFormat() != "" {
			output["format"] = v.GetFormat()
		}
		if v.GetStatus() == turnresult.OutputAvailable {
			output["content"] = v.GetContent()
		}
	}

	deliveries := make([]map[string]any, 0, len(r.GetDeliveries()))
	for _, d := range r.GetDeliveries() {
		item := map[string]any{
			"channel": d.GetChannel(),
			"status":  d.GetStatus(),
		}
		if d.GetReason() != "" {
			item["reason"] = d.GetReason()
		}
		if d.GetBridgeId() != "" {
			item["bridge_id"] = d.GetBridgeId()
		}
		if d.GetChatId() != "" {
			item["chat_id"] = d.GetChatId()
		}
		if d.GetRootMessageId() != "" {
			item["root_message_id"] = d.GetRootMessageId()
		}
		if len(d.GetMessages()) > 0 {
			msgs := make([]map[string]any, 0, len(d.GetMessages()))
			for _, m := range d.GetMessages() {
				entry := map[string]any{
					"kind":       m.GetKind(),
					"message_id": m.GetMessageId(),
				}
				if m.GetName() != "" {
					entry["name"] = m.GetName()
				}
				msgs = append(msgs, entry)
			}
			item["messages"] = msgs
		}
		deliveries = append(deliveries, item)
	}

	return map[string]any{
		"version":    r.GetVersion(),
		"ref":        ref,
		"source":     source,
		"execution":  execution,
		"output":     output,
		"deliveries": deliveries,
	}
}

func renderTurnResultText(w io.Writer, locale i18n.Locale, resp *pb.AmuxdGetTurnResultResponse) {
	st := resp.GetStatus()
	switch st {
	case turnresult.StatusUnavailable:
		if resp.GetReason() == turnresult.ReasonDisabled {
			fmt.Fprintln(w, commandCatalog.Format(locale, "turn.result.disabled_hint"))
			return
		}
		fmt.Fprintln(w, commandCatalog.Format(locale, "turn.result.unavailable", dashIfEmpty(resp.GetReason())))
		return
	case turnresult.StatusNotFound:
		fmt.Fprintln(w, commandCatalog.Format(locale, "turn.result.not_found"))
		return
	case turnresult.StatusNotReady:
		fmt.Fprintln(w, commandCatalog.Format(locale, "turn.result.not_ready"))
		return
	}

	r := resp.GetResult()
	if r == nil {
		fmt.Fprintf(w, "%s: %s\n", commandCatalog.Format(locale, "turn.result.label.status"), st)
		return
	}
	fmt.Fprintf(w, "%s: %s\n", commandCatalog.Format(locale, "turn.result.label.status"), st)
	if ref := r.GetRef(); ref != nil {
		sessionLabel := strings.Trim(strings.Join([]string{ref.GetTargetId(), firstNonEmptyString(ref.GetSessionName(), ref.GetSessionId())}, "/"), "/")
		if sessionLabel != "" {
			fmt.Fprintf(w, "%s: %s\n", commandCatalog.Format(locale, "turn.result.label.session"), sessionLabel)
		}
		if ref.GetTurnId() != "" {
			fmt.Fprintf(w, "%s: %s\n", commandCatalog.Format(locale, "turn.result.label.turn"), ref.GetTurnId())
		}
	}
	if src := r.GetSource(); src != nil && src.GetKind() != "" {
		detail := src.GetKind()
		if src.GetScheduleId() > 0 {
			detail += fmt.Sprintf(" #%d (exec %d)", src.GetScheduleId(), src.GetExecutionId())
		}
		fmt.Fprintf(w, "%s: %s\n", commandCatalog.Format(locale, "turn.result.label.source"), detail)
	}
	if exec := r.GetExecution(); exec != nil {
		line := exec.GetStatus()
		if exec.GetCode() != "" {
			line += " (" + exec.GetCode() + ")"
		}
		fmt.Fprintf(w, "%s: %s\n", commandCatalog.Format(locale, "turn.result.label.execution"), line)
	}
	if len(r.GetDeliveries()) == 0 {
		fmt.Fprintf(w, "%s: %s\n", commandCatalog.Format(locale, "turn.result.label.deliveries"), commandCatalog.Format(locale, "turn.result.deliveries.none"))
	} else {
		parts := make([]string, 0, len(r.GetDeliveries()))
		for _, d := range r.GetDeliveries() {
			s := fmt.Sprintf("%s=%s", d.GetChannel(), d.GetStatus())
			if len(d.GetMessages()) > 0 {
				ids := make([]string, 0, len(d.GetMessages()))
				for _, m := range d.GetMessages() {
					ids = append(ids, m.GetKind()+":"+m.GetMessageId())
				}
				s += " [" + strings.Join(ids, ", ") + "]"
			}
			parts = append(parts, s)
		}
		fmt.Fprintf(w, "%s: %s\n", commandCatalog.Format(locale, "turn.result.label.deliveries"), strings.Join(parts, "; "))
	}
	if out := r.GetOutput(); out != nil {
		line := out.GetStatus()
		if out.GetReason() != "" {
			line += " (" + out.GetReason() + ")"
		}
		fmt.Fprintf(w, "%s: %s\n", commandCatalog.Format(locale, "turn.result.label.output"), line)
		if out.GetStatus() == turnresult.OutputAvailable && out.GetContent() != "" {
			fmt.Fprintln(w)
			fmt.Fprintln(w, out.GetContent())
		}
	}
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
