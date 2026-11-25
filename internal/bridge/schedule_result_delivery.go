package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/rs/zerolog/log"
)

const (
	scheduleResultCardMaxRunes = 6000
)

func scheduleResultDeliveryUUID(botID string, scheduleID, runID int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("schedule-result:%s:%d:%d", botID, scheduleID, runID)))
	return fmt.Sprintf("%x", sum[:16])
}

func truncateScheduleDeliveryBody(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > scheduleResultCardMaxRunes {
		return string(r[:scheduleResultCardMaxRunes]) + "\n…"
	}
	return string(r)
}

func scheduleDeliveryAgentLabel(t schedule.Task) string {
	if t.ScheduleType == schedule.ScheduleTypeSubtask && len(t.TaskSpecJson) > 0 {
		var spec task.Spec
		if json.Unmarshal(t.TaskSpecJson, &spec) == nil {
			agent := strings.TrimSpace(spec.Agent)
			model := strings.TrimSpace(spec.Model)
			if agent != "" && model != "" {
				return agent + " (" + model + ")"
			}
			if agent != "" {
				return agent
			}
		}
	}
	if strings.TrimSpace(t.SessionName) != "" {
		return t.SessionName
	}
	return "—"
}

func renderScheduleResultDeliveryCard(entry schedule.Entry, run schedule.Execution) string {
	theme := "green"
	switch run.Status {
	case "partial":
		theme = "orange"
	case "failed", "timed_out", "cancelled", "missed", "not_dispatched":
		theme = "red"
	}

	sourceKey := "delivery.source.scheduled"
	if run.TriggerSource == schedule.TriggerSourceManual {
		sourceKey = "delivery.source.manual"
	}
	finishedAt := run.FinishedAt
	if finishedAt <= 0 {
		finishedAt = run.ObservedAt
	}
	if finishedAt <= 0 {
		finishedAt = run.ScheduledAt
	}

	statusCopy := scheduleText("status." + run.Status)
	if run.Status == "" {
		statusCopy = scheduleText("status.not_run")
	}

	elements := []cardElement{
		managementTextElement(
			managementConcat(
				statusCopy,
				" · ",
				scheduleText(sourceKey),
				" · ",
				scheduleTime(finishedAt),
			),
			"notation",
			"grey",
			nil,
		),
		managementDividerElement(),
	}

	body := truncateScheduleDeliveryBody(run.Result)
	if body != "" {
		elements = append(elements, cardElement{Tag: "markdown", Content: body})
	} else if run.Reason != "" {
		elements = append(elements, managementTextElement(scheduleReason(run.Reason), "normal", "default", nil))
	} else {
		elements = append(elements, managementTextElement(scheduleText("delivery.no_output"), "normal", "grey", nil))
	}

	footerParts := []any{
		scheduleText("task_identity", entry.Task.ID, scheduleName(entry.Task)),
		" · ",
		scheduleDeliveryAgentLabel(entry.Task),
	}
	if entry.Task.LifecycleState() == schedule.StateCompleted {
		footerParts = append(footerParts, "\n", scheduleText("delivery.footer.once_done"))
	} else if next := scheduleNext(entry); next != "—" {
		footerParts = append(footerParts, "\n", scheduleText("next", next))
	}
	elements = append(elements,
		managementDividerElement(),
		managementTextElement(managementConcat(footerParts...), "notation", "grey", nil),
	)

	if entry.Task.ID > 0 {
		rerunBtn := scheduleButton("delivery.action.rerun", "trigger", entry.Task.ID, map[string]string{"source": "delivery"})
		rerunBtn.Size = "small"
		actions := []cardElement{rerunBtn}
		if entry.Task.Enabled && entry.Task.NextRunAt != nil {
			pauseBtn := scheduleButton("delivery.action.pause", "pause", entry.Task.ID, map[string]string{"source": "delivery"})
			pauseBtn.Size = "small"
			actions = append(actions, pauseBtn)
		}
		manageBtn := scheduleButton("delivery.action.manage", "detail", entry.Task.ID, map[string]string{"source": "delivery"})
		manageBtn.Size = "small"
		actions = append(actions, manageBtn)
		elements = append(elements, managementFormButtonRowElement(actions...))
	}

	title := managementConcat(scheduleText("delivery.title"), " · ", scheduleName(entry.Task))
	return marshalNativeManagementCard(managementCardView{
		Title:    title,
		Summary:  title,
		Theme:    theme,
		Elements: elements,
	})
}

func isPermanentFeishuDeliveryError(code int) bool {
	switch code {
	case 230001, 230002, 230006, 230011, 230013, 230027, 231003, 99991672:
		return true
	default:
		return false
	}
}

func (b *Bridge) sendScheduleResultCard(ctx context.Context, d schedule.ResultDelivery, card string) (string, int, error) {
	if b.client == nil {
		return "", 0, fmt.Errorf("feishu client unavailable")
	}
	reqUUID := scheduleResultDeliveryUUID(d.BotID, d.ScheduleID, d.RunID)

	sendDM := func(openID string) (string, int, error) {
		resp, err := b.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
			ReceiveIdType("open_id").
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(openID).
				MsgType("interactive").
				Content(card).
				Uuid(reqUUID).
				Build()).
			Build())
		if err != nil {
			return "", 0, err
		}
		if resp == nil {
			return "", 0, fmt.Errorf("empty feishu response")
		}
		if !resp.Success() {
			return "", resp.Code, fmt.Errorf("code=%d msg=%s", resp.Code, resp.Msg)
		}
		msgID := ""
		if resp.Data != nil && resp.Data.MessageId != nil {
			msgID = *resp.Data.MessageId
		}
		return msgID, 0, nil
	}

	sendChat := func(chatID string) (string, int, error) {
		resp, err := b.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().
			ReceiveIdType("chat_id").
			Body(larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType("interactive").
				Content(card).
				Uuid(reqUUID).
				Build()).
			Build())
		if err != nil {
			return "", 0, err
		}
		if resp == nil {
			return "", 0, fmt.Errorf("empty feishu response")
		}
		if !resp.Success() {
			return "", resp.Code, fmt.Errorf("code=%d msg=%s", resp.Code, resp.Msg)
		}
		msgID := ""
		if resp.Data != nil && resp.Data.MessageId != nil {
			msgID = *resp.Data.MessageId
		}
		return msgID, 0, nil
	}

	if d.Target == schedule.DeliveryTargetCreatorDM {
		if strings.TrimSpace(d.OpenID) == "" {
			return "", 230001, fmt.Errorf("creator open_id missing")
		}
		return sendDM(d.OpenID)
	}

	// Target == "origin": prefer replying in the originating thread root, then
	// originating chat_id, then creator DM.
	if root := strings.TrimSpace(d.OriginRootMsgID); root != "" && !strings.HasPrefix(root, "singleton:") {
		resp, err := b.client.Im.Message.Reply(ctx, larkim.NewReplyMessageReqBuilder().
			MessageId(root).
			Body(larkim.NewReplyMessageReqBodyBuilder().
				MsgType("interactive").
				Content(card).
				ReplyInThread(true).
				Uuid(reqUUID).
				Build()).
			Build())
		if err == nil && resp != nil && resp.Success() {
			msgID := ""
			if resp.Data != nil && resp.Data.MessageId != nil {
				msgID = *resp.Data.MessageId
			}
			return msgID, 0, nil
		}
		// If the root message was deleted/withdrawn or doesn't accept thread
		// replies, fall back to the originating chat_id if known.
		if err == nil && resp != nil && !resp.Success() {
			if chatID := strings.TrimSpace(d.OriginChatID); chatID != "" && isPermanentFeishuDeliveryError(resp.Code) {
				return sendChat(chatID)
			}
			return "", resp.Code, fmt.Errorf("code=%d msg=%s", resp.Code, resp.Msg)
		}
		if err != nil {
			return "", 0, err
		}
		return "", 0, fmt.Errorf("empty feishu reply response")
	}

	if chatID := strings.TrimSpace(d.OriginChatID); chatID != "" {
		return sendChat(chatID)
	}
	if openID := strings.TrimSpace(d.OpenID); openID != "" {
		return sendDM(openID)
	}
	return "", 230001, fmt.Errorf("no delivery destination available")
}

// deliverScheduleResults drains queued proactive result deliveries for this
// Bridge's Bot. Unlike admin alert summaries, explicit user-configured result
// deliveries are not delayed by quiet hours.
func (b *Bridge) deliverScheduleResults(ctx context.Context, now time.Time) error {
	if b.schedules == nil {
		return nil
	}
	store := b.schedules.Store()
	if store == nil {
		return nil
	}
	bot := b.currentBridgeId()
	if bot == "" {
		return nil
	}
	pending, err := store.PendingResultDeliveries(bot, now)
	if err != nil {
		return err
	}
	for _, d := range pending {
		// Re-verify live creator authorization before posting any execution output.
		entry, err := b.schedules.Get(ctx, bot, d.OpenID, d.ScheduleID)
		if err != nil {
			_ = store.MarkResultDelivery(d.RunID, schedule.DeliveryStatusDenied, true, err.Error(), now)
			continue
		}
		// If the user switched delivery_target to "none" before delivery flushed,
		// respect the updated preference and skip.
		if entry.Task.DeliveryTarget == schedule.DeliveryTargetNone {
			_ = store.MarkResultDelivery(d.RunID, schedule.DeliveryStatusSkipped, true, "", now)
			continue
		}
		rawCard := renderScheduleResultDeliveryCard(entry, d.Run)
		card := b.managementCardForRecipient(d.OpenID, rawCard)
		msgID, code, sendErr := b.sendScheduleResultCard(ctx, d, card)
		if sendErr == nil {
			log.Info().Int64("schedule_id", d.ScheduleID).Int64("run_id", d.RunID).Str("target", d.Target).Str("message_id", msgID).Msg("bridge: delivered schedule result card")
			_ = store.MarkResultDelivery(d.RunID, schedule.DeliveryStatusDelivered, true, "", now)
			continue
		}
		log.Warn().Err(sendErr).Int64("schedule_id", d.ScheduleID).Int64("run_id", d.RunID).Str("target", d.Target).Int("code", code).Msg("bridge: schedule result delivery failed")
		_ = store.MarkResultDelivery(d.RunID, schedule.DeliveryStatusFailed, isPermanentFeishuDeliveryError(code), sendErr.Error(), now)
	}
	return nil
}
