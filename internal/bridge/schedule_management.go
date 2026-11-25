package bridge

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/schedule"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/rs/zerolog/log"
)

// ScheduleControl is implemented by the Hub; every read and mutation checks the
// live creator binding and execution authorization, even on shared deployments.
type ScheduleControl interface {
	List(context.Context, string, string) ([]schedule.Entry, error)
	Get(context.Context, string, string, int64) (schedule.Entry, error)
	Act(context.Context, string, string, int64, string) error
	Creators(string) ([]string, error)
	Store() *schedule.Store
}

func (b *Bridge) SetScheduleControl(c ScheduleControl) { b.schedules = c }
func scheduleText(key string, args ...any) any         { return managementCopy("schedule."+key, args...) }
func scheduleButton(key, action string, id int64, values map[string]string) cardElement {
	if values == nil {
		values = map[string]string{}
	}
	values["schedule_id"] = strconv.FormatInt(id, 10)
	return managementButtonElement(managementButton{Text: scheduleText(key), Type: "default", Action: "schedule." + action, Name: fmt.Sprintf("schedule.%s.%d.%s", action, id, key), Values: values})
}
func scheduleTime(n int64) string {
	if n <= 0 {
		return "—"
	}
	return time.Unix(n, 0).Format("2006-01-02 15:04 MST")
}
func scheduleNext(e schedule.Entry) string {
	if !e.Task.Enabled || e.Task.NextRunAt == nil {
		return "—"
	}
	return scheduleTime(*e.Task.NextRunAt)
}
func scheduleStatus(e schedule.Entry) any {
	status := e.Visibility.Latest.Status
	if status == "completed" && e.Visibility.IncidentAt != 0 {
		status = "recovered"
	}
	if status == "" {
		status = "not_run"
	}
	return scheduleText("status." + status)
}
func scheduleSummary(entries []schedule.Entry) any {
	if len(entries) == 0 {
		return scheduleText("none")
	}
	enabled, running, attention := 0, 0, 0
	completed, failed, missed := 0, 0, 0
	for _, e := range entries {
		completed += e.Counts.Completed
		failed += e.Counts.Failed
		missed += e.Counts.Missed
		if e.Task.Enabled && e.Task.NextRunAt != nil {
			enabled++
		}
		if e.Visibility.Latest.Status == "running" || e.Visibility.Latest.Status == "submitted" {
			running++
		}
		if scheduleNeedsAttention(e) {
			attention++
		}
	}
	return managementConcat(scheduleText("summary", enabled, running, attention), "\n", scheduleText("recent", completed, failed, missed))
}
func renderSchedules(entries []schedule.Entry, filter string, page int) string {
	if filter == "" {
		filter = "all"
	}
	elements := []cardElement{scheduleBackToOverview(), managementDividerElement(), managementTextElement(scheduleSummary(entries), "normal", "default", nil), managementTextElement(scheduleText("policy"), "notation", "grey", nil)}
	completedCount := 0
	for _, e := range entries {
		if e.Task.LifecycleState() == schedule.StateCompleted {
			completedCount++
		}
	}
	var filters []cardElement
	for _, f := range []string{"all", "attention", "running", "paused", "completed"} {
		btn := scheduleButton("filter."+f, "list", 0, map[string]string{"filter": f})
		btn.Size = "small"
		if f == filter {
			btn.Type = "primary"
		}
		filters = append(filters, btn)
	}
	elements = append(elements, managementFormButtonRowElement(filters...))
	if completedCount > 0 && (filter == "all" || filter == "completed") {
		pruneBtn := managementButtonElement(managementButton{
			Text:   scheduleText("prune_completed", completedCount),
			Type:   "default",
			Action: "schedule.prune_completed",
			Name:   "schedule.prune_completed.0",
			Values: map[string]string{"filter": filter},
		})
		pruneBtn.Size = "small"
		elements = append(elements, managementFormButtonRowElement(pruneBtn))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		rank := func(e schedule.Entry) int {
			if scheduleNeedsAttention(e) {
				return 0
			}
			if e.Visibility.Latest.Status == "running" || e.Visibility.Latest.Status == "submitted" {
				return 1
			}
			switch e.Task.LifecycleState() {
			case schedule.StateEnabled:
				return 2
			case schedule.StatePaused:
				return 3
			default:
				return 4
			}
		}
		return rank(entries[i]) < rank(entries[j])
	})
	var rows []schedule.Entry
	for _, e := range entries {
		switch filter {
		case "attention":
			if !scheduleNeedsAttention(e) {
				continue
			}
		case "running":
			if e.Visibility.Latest.Status != "running" && e.Visibility.Latest.Status != "submitted" {
				continue
			}
		case "paused":
			if e.Task.LifecycleState() != schedule.StatePaused {
				continue
			}
		case "completed":
			if e.Task.LifecycleState() != schedule.StateCompleted {
				continue
			}
		}
		rows = append(rows, e)
	}
	pages := max(1, (len(rows)+9)/10)
	page = max(0, min(page, pages-1))
	end := min(len(rows), (page+1)*10)
	for _, e := range rows[page*10 : end] {
		detailBtn := scheduleButton("detail", "detail", e.Task.ID, nil)
		detailBtn.Type = "primary_text"
		detailBtn.Size = "small"
		header := cardElement{
			Tag:               "column_set",
			FlexMode:          "none",
			HorizontalSpacing: "8px",
			Columns: []cardColumn{
				{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(scheduleName(e.Task), "normal", "default", nil)}},
				{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{detailBtn}},
			},
		}
		statusEl := managementTextElement(scheduleStatus(e), "notation", "default", nil)
		if e.Visibility.AutoPausedAt > 0 {
			statusEl = managementTextElement(
				managementConcat(scheduleStatus(e), " · ", scheduleText("auto_paused_short")),
				"notation",
				"orange",
				nil,
			)
		}
		item := managementVerticalSummaryElement(fmt.Sprintf("amux_schedule_%d", e.Task.ID),
			header,
			statusEl,
			managementTextElement(
				managementConcat(scheduleText("next", scheduleNext(e)), " · ", scheduleDeliverySummary(e.Task)),
				"notation",
				"grey",
				nil,
			),
		)
		item.Columns[0].VerticalSpacing = "2px"
		elements = append(elements, item)
	}
	if len(rows) == 0 {
		elements = append(elements, managementTextElement(scheduleText("empty"), "normal", "grey", nil))
	}
	pageColumns := []cardColumn{{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(scheduleText("page", page+1, pages, scheduleTime(time.Now().Unix())), "notation", "grey", nil)}}}
	if page > 0 {
		prev := scheduleButton("previous", "list", 0, map[string]string{"page": strconv.Itoa(page - 1), "filter": filter})
		prev.Size = "small"
		pageColumns = append(pageColumns, cardColumn{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{prev}})
	}
	if page+1 < pages {
		next := scheduleButton("next_page", "list", 0, map[string]string{"page": strconv.Itoa(page + 1), "filter": filter})
		next.Size = "small"
		pageColumns = append(pageColumns, cardColumn{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{next}})
	}
	refresh := scheduleButton("refresh", "list", 0, map[string]string{"page": strconv.Itoa(page), "filter": filter})
	refresh.Size = "small"
	refresh.Type = "text"
	refresh.Icon = managementIconPtr("refresh_outlined", "grey")
	pageColumns = append(pageColumns, cardColumn{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{refresh}})
	elements = append(elements, cardElement{Tag: "column_set", FlexMode: "none", HorizontalSpacing: "8px", Columns: pageColumns})
	return marshalNativeManagementCard(managementCardView{Title: scheduleText("title"), Summary: scheduleText("title"), Theme: "wathet", Icon: managementIcon("setting_outlined", "blue"), Elements: elements})
}
func scheduleLifecycleTag(e schedule.Entry) cardTextTag {
	if e.Visibility.AutoPausedAt > 0 {
		return cardTextTag{Tag: "text_tag", Text: managementPlainText(scheduleText("tag.auto_paused")), Color: "orange"}
	}
	if scheduleNeedsAttention(e) {
		return cardTextTag{Tag: "text_tag", Text: managementPlainText(scheduleText("tag.attention")), Color: "orange"}
	}
	switch e.Task.LifecycleState() {
	case schedule.StatePaused:
		return cardTextTag{Tag: "text_tag", Text: managementPlainText(scheduleText("tag.paused")), Color: "neutral"}
	case schedule.StateCompleted:
		return cardTextTag{Tag: "text_tag", Text: managementPlainText(scheduleText("tag.completed")), Color: "blue"}
	default:
		return cardTextTag{Tag: "text_tag", Text: managementPlainText(scheduleText("tag.enabled")), Color: "green"}
	}
}

func renderScheduleDetail(e schedule.Entry, result bool, pages ...int) string {
	page := 0
	if len(pages) > 0 {
		page = pages[0]
	}
	return renderScheduleDetailWithViewer(e, result, page, nil)
}

func renderScheduleDetailWithViewer(e schedule.Entry, result bool, page int, viewerURL func(targetID, sessionName string) string) string {
	page = max(0, min(page, max(0, (len(e.Runs)-1)/5)))
	v := e.Visibility

	back := managementNavigationBackElement(scheduleText("back"), "schedule.list", "schedule.back.list")
	if result {
		back = managementButtonRowElement([]managementButton{{Text: scheduleText("back_detail"), Type: "text", Action: "schedule.detail", Name: "schedule.back.detail", Values: map[string]string{"schedule_id": strconv.FormatInt(e.Task.ID, 10)}, Icon: managementIconPtr("left_outlined", "grey")}})
		if len(back.Columns) > 0 && len(back.Columns[0].Elements) > 0 {
			back.Columns[0].Elements[0].Size = "small"
		}
	}
	breadcrumb := scheduleBackToOverview()
	breadcrumb.Columns = append(breadcrumb.Columns, back.Columns...)

	titleRow := cardElement{
		Tag:               "column_set",
		FlexMode:          "none",
		HorizontalSpacing: "8px",
		Columns: []cardColumn{
			{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(scheduleName(e.Task), "heading-3", "default", nil)}},
		},
	}
	if e.Task.ID > 0 {
		titleRow.Columns = append(titleRow.Columns, cardColumn{
			Tag:           "column",
			Width:         "auto",
			VerticalAlign: "center",
			Elements:      []cardElement{managementTextElement(fmt.Sprintf("#%d", e.Task.ID), "notation", "grey", nil)},
		})
	}

	elements := []cardElement{breadcrumb, managementDividerElement(), titleRow}

	// Surface warnings prominently at the top only when action or attention is needed.
	var warnings []cardElement
	if v.AutoPausedAt > 0 {
		failures := v.ConsecutiveFailures
		if failures < schedule.DefaultAutoPauseFailureThreshold {
			failures = schedule.DefaultAutoPauseFailureThreshold
		}
		warnings = append(warnings, managementTextElement(scheduleText("auto_paused", failures, scheduleTime(v.AutoPausedAt)), "notation", "orange", managementIconPtr("warning_outlined", "orange")))
	}
	if v.IncidentAt != 0 {
		warnings = append(warnings, managementTextElement(scheduleText("incident", v.IncidentCount, scheduleTime(v.IncidentAt)), "notation", "orange", managementIconPtr("warning_outlined", "orange")))
	}
	if v.DeliveryError == "missing_admin" {
		warnings = append(warnings, managementTextElement(scheduleText("missing_admin"), "notation", "orange", managementIconPtr("warning_outlined", "orange")))
	} else if v.Blocked {
		recheckBtn := scheduleButton("recheck", "recheck", e.Task.ID, nil)
		recheckBtn.Size = "small"
		warnings = append(warnings, managementTextElement(scheduleText("blocked."+v.DeliveryError), "notation", "orange", managementIconPtr("warning_outlined", "orange")), managementFormButtonRowElement(recheckBtn))
	} else if v.DeliveryError != "" {
		warnings = append(warnings, managementTextElement(scheduleText("delivery_retry"), "notation", "orange", managementIconPtr("warning_outlined", "orange")))
	}
	if len(warnings) > 0 {
		warnBox := managementVerticalSummaryElement(fmt.Sprintf("amux_schedule_warn_%d", e.Task.ID), warnings...)
		warnBox.Columns[0].BackgroundStyle = "orange-50"
		elements = append(elements, warnBox)
	}

	// Structured 2-column summary card matching /amux overview styling.
	infoRows := []cardElement{
		managementOverviewKeyValueRowElement(scheduleText("detail.field.status"), scheduleStatus(e)),
		managementOverviewKeyValueRowElement(scheduleText("detail.field.schedule"), scheduleSpecDisplay(e.Task)),
	}
	if e.Task.LifecycleState() != schedule.StateCompleted {
		infoRows = append(infoRows, managementOverviewKeyValueRowElement(scheduleText("detail.field.next_run"), scheduleNext(e)))
	}
	if v.LastCompletedAt > 0 {
		infoRows = append(infoRows, managementOverviewKeyValueRowElement(scheduleText("detail.field.last_completed"), scheduleTime(v.LastCompletedAt)))
	}
	infoRows = append(infoRows, managementOverviewKeyValueRowElement(
		scheduleText("detail.field.execution"),
		managementConcat(scheduleExecutionModeCopy(e.Task), " · ", scheduleDeliveryAgentLabel(e.Task)),
	))
	infoBox := managementVerticalSummaryElement(fmt.Sprintf("amux_schedule_info_%d", e.Task.ID), infoRows...)
	infoBox.Columns[0].BackgroundStyle = "blue-50"
	infoBox.Columns[0].VerticalSpacing = "4px"
	elements = append(elements, infoBox)

	// Primary task actions right below the summary box.
	var primaryRow []cardElement
	if !result {
		btn := scheduleButton("results", "result", e.Task.ID, nil)
		btn.Size = "small"
		primaryRow = append(primaryRow, btn)
	}
	triggerBtn := scheduleButton("trigger", "trigger", e.Task.ID, nil)
	triggerBtn.Size = "small"
	primaryRow = append(primaryRow, triggerBtn)
	if e.Task.Enabled && e.Task.NextRunAt != nil {
		pauseBtn := scheduleButton("pause", "pause", e.Task.ID, nil)
		pauseBtn.Size = "small"
		primaryRow = append(primaryRow, pauseBtn)
	} else if e.Task.Kind != schedule.KindOnce {
		resumeBtn := scheduleButton("resume", "resume", e.Task.ID, nil)
		resumeBtn.Size = "small"
		primaryRow = append(primaryRow, resumeBtn)
	}
	deleteBtn := scheduleButton("delete", "delete", e.Task.ID, nil)
	deleteBtn.Size = "small"
	deleteBtn.Type = "danger"
	primaryRow = append(primaryRow, deleteBtn)
	elements = append(elements, managementFormButtonRowElement(primaryRow...))

	if result {
		elements = append(elements, managementDividerElement(), managementSectionTitleElement(scheduleText("results")))
		if len(e.Runs) == 0 {
			elements = append(elements, managementTextElement(scheduleText("no_result"), "normal", "grey", nil))
		}
		for idx, r := range e.Runs[page*5 : min(len(e.Runs), (page+1)*5)] {
			runIdx := page*5 + idx
			viewBtn := managementButtonElement(managementButton{
				Text:   scheduleText("run.view_full"),
				Type:   "primary_text",
				Action: "schedule.run_detail",
				Name:   fmt.Sprintf("schedule.run_detail.%d.%d", e.Task.ID, runIdx),
				Values: map[string]string{
					"schedule_id": strconv.FormatInt(e.Task.ID, 10),
					"run_id":      strconv.FormatInt(r.ID, 10),
					"run_index":   strconv.Itoa(runIdx),
					"page":        strconv.Itoa(page),
				},
			})
			viewBtn.Size = "small"
			runHeader := cardElement{
				Tag:               "column_set",
				FlexMode:          "none",
				HorizontalSpacing: "8px",
				Columns: []cardColumn{
					{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(scheduleText("status."+r.Status), "normal", "default", nil)}},
					{Tag: "column", Width: "auto", VerticalAlign: "center", Elements: []cardElement{viewBtn}},
				},
			}
			runTime := r.FinishedAt
			if runTime <= 0 {
				runTime = r.StartedAt
			}
			if runTime <= 0 {
				runTime = r.ScheduledAt
			}
			metaParts := []any{scheduleTime(runTime)}
			switch r.DeliveryStatus {
			case schedule.DeliveryStatusDelivered, schedule.DeliveryStatusFailed, schedule.DeliveryStatusDenied, schedule.DeliveryStatusSkipped, schedule.DeliveryStatusPending:
				metaParts = append(metaParts, " · ", scheduleText("delivery_status."+r.DeliveryStatus))
			}
			runRows := []cardElement{
				runHeader,
				managementTextElement(managementConcat(metaParts...), "notation", "grey", nil),
			}
			if r.Result != "" {
				runRows = append(runRows, managementTextElement(truncateScheduleResult(r.Result), "normal", "default", nil))
			}
			if r.Reason != "" {
				runRows = append(runRows, managementTextElement(scheduleReason(r.Reason), "notation", "grey", nil))
			}
			if viewerURL != nil {
				if link := strings.TrimSpace(viewerURL(r.TargetID, r.SessionName)); link != "" {
					sessionBtn := managementButtonElement(managementButton{
						Text: scheduleText("run.view_session"),
						Type: "default",
						Name: fmt.Sprintf("schedule.run_session.%d.%d", e.Task.ID, runIdx),
						URL:  link,
					})
					sessionBtn.Name = fmt.Sprintf("schedule.run_session.%d.%d", e.Task.ID, runIdx)
					sessionBtn.Size = "small"
					runRows = append(runRows, managementFormButtonRowElement(sessionBtn))
				}
			}
			elements = append(elements, managementVerticalSummaryElement(fmt.Sprintf("amux_schedule_run_%d_%d", e.Task.ID, idx), runRows...))
			if len(elements) > 55 {
				break
			}
		}
		var pageBtns []cardElement
		if page > 0 {
			prev := scheduleButton("previous", "result", e.Task.ID, map[string]string{"page": strconv.Itoa(page - 1)})
			prev.Size = "small"
			pageBtns = append(pageBtns, prev)
		}
		if (page+1)*5 < len(e.Runs) {
			next := scheduleButton("next_page", "result", e.Task.ID, map[string]string{"page": strconv.Itoa(page + 1)})
			next.Size = "small"
			pageBtns = append(pageBtns, next)
		}
		if len(pageBtns) > 0 {
			elements = append(elements, managementFormButtonRowElement(pageBtns...))
		}
	} else {
		// Section 1: Result delivery configuration (clearly grouped in a shaded panel).
		isReuseSession := e.Task.ScheduleType != schedule.ScheduleTypeSubtask && (e.Task.RunMode == "" || e.Task.RunMode == schedule.RunModeReuseSession)
		effectiveTarget := scheduleEffectiveDeliveryTarget(e.Task)
		targetOptions := []string{schedule.DeliveryTargetOrigin, schedule.DeliveryTargetCreatorDM, schedule.DeliveryTargetNone}
		if isReuseSession {
			targetOptions = []string{schedule.DeliveryTargetCreatorDM, schedule.DeliveryTargetNone}
		}
		var targetRow []cardElement
		for _, target := range targetOptions {
			btn := scheduleButton("delivery_target."+target, "delivery_target", e.Task.ID, map[string]string{"value": target})
			btn.Size = "small"
			if target == effectiveTarget {
				btn.Type = "primary"
			}
			targetRow = append(targetRow, btn)
		}
		deliveryRows := []cardElement{
			managementTextElement(scheduleText("detail.delivery_target_label"), "notation", "grey", nil),
			managementFormButtonRowElement(targetRow...),
		}
		if effectiveTarget != schedule.DeliveryTargetNone {
			effectivePolicy := scheduleEffectiveDeliveryPolicy(e.Task)
			var policyRow []cardElement
			for _, policy := range []string{schedule.DeliveryPolicyAlways, schedule.DeliveryPolicyOnNonEmpty, schedule.DeliveryPolicyOnFailure} {
				btn := scheduleButton("delivery_policy."+policy, "delivery_policy", e.Task.ID, map[string]string{"value": policy})
				btn.Size = "small"
				if policy == effectivePolicy {
					btn.Type = "primary"
				}
				policyRow = append(policyRow, btn)
			}
			deliveryRows = append(deliveryRows,
				managementTextElement(scheduleText("detail.delivery_policy_label"), "notation", "grey", nil),
				managementFormButtonRowElement(policyRow...),
			)
		}
		deliveryPanel := managementVerticalSummaryElement(fmt.Sprintf("amux_schedule_delivery_%d", e.Task.ID), deliveryRows...)
		deliveryPanel.Columns[0].VerticalSpacing = "6px"

		elements = append(elements,
			managementDividerElement(),
			managementSectionTitleElement(scheduleText("detail.section.delivery")),
			deliveryPanel,
		)

		// Section 2: Admin status alerts (clearly separated from result delivery).
		alertRows := []cardElement{
			managementTextElement(scheduleText("recipient"), "notation", "grey", nil),
		}
		if v.Acknowledged {
			alertRows = append(alertRows, managementTextElement(scheduleText("acknowledged"), "notation", "grey", nil))
		}
		if v.SnoozeUntil > time.Now().Unix() {
			alertRows = append(alertRows, managementTextElement(scheduleText("snoozed", scheduleTime(v.SnoozeUntil)), "notation", "grey", nil))
		}
		var noticeRow []cardElement
		if v.NotificationsOff {
			btn := scheduleButton("on", "on", e.Task.ID, nil)
			btn.Size = "small"
			noticeRow = append(noticeRow, btn)
		} else {
			btn := scheduleButton("off", "off", e.Task.ID, nil)
			btn.Size = "small"
			noticeRow = append(noticeRow, btn)
		}
		ackBtn := scheduleButton("ack", "ack", e.Task.ID, nil)
		ackBtn.Size = "small"
		snoozeBtn := scheduleButton("snooze", "snooze", e.Task.ID, nil)
		snoozeBtn.Size = "small"
		noticeRow = append(noticeRow, ackBtn, snoozeBtn)
		alertRows = append(alertRows, managementFormButtonRowElement(noticeRow...))
		alertPanel := managementVerticalSummaryElement(fmt.Sprintf("amux_schedule_alerts_%d", e.Task.ID), alertRows...)
		alertPanel.Columns[0].VerticalSpacing = "6px"

		elements = append(elements,
			managementDividerElement(),
			managementSectionTitleElement(scheduleText("detail.section.alerts")),
			alertPanel,
		)
	}

	elements = append(elements, managementHintElement(scheduleText("pause_note")))
	return marshalNativeManagementCard(managementCardView{
		Title:    scheduleText("title"),
		Summary:  scheduleText("title"),
		Theme:    "wathet",
		Icon:     managementIcon("setting_outlined", "blue"),
		Tags:     []cardTextTag{scheduleLifecycleTag(e)},
		Elements: elements,
	})
}
func truncateScheduleResult(s string) string {
	r := []rune(s)
	if len(r) > 400 {
		return string(r[:400]) + "…"
	}
	return s
}

func scheduleRunStatusTag(status string) cardTextTag {
	color := "blue"
	switch status {
	case "completed":
		color = "green"
	case "partial":
		color = "orange"
	case "failed", "timed_out", "cancelled", "missed", "not_dispatched":
		color = "red"
	}
	key := "status." + status
	if status == "" {
		key = "status.not_run"
	}
	return cardTextTag{Tag: "text_tag", Text: managementPlainText(scheduleText(key)), Color: color}
}

func findScheduleRun(runs []schedule.Execution, runID int64, runIndex int) (schedule.Execution, bool) {
	if runID > 0 {
		for _, r := range runs {
			if r.ID == runID {
				return r, true
			}
		}
	}
	if runIndex >= 0 && runIndex < len(runs) {
		return runs[runIndex], true
	}
	return schedule.Execution{}, false
}

func renderScheduleRunDetail(e schedule.Entry, r schedule.Execution, page int, viewerURL string) string {
	backHistory := managementButtonRowElement([]managementButton{{
		Text:   scheduleText("run.back_history"),
		Type:   "text",
		Action: "schedule.result",
		Name:   "schedule.back.result",
		Values: map[string]string{
			"schedule_id": strconv.FormatInt(e.Task.ID, 10),
			"page":        strconv.Itoa(max(0, page)),
		},
		Icon: managementIconPtr("left_outlined", "grey"),
	}})
	if len(backHistory.Columns) > 0 && len(backHistory.Columns[0].Elements) > 0 {
		backHistory.Columns[0].Elements[0].Size = "small"
	}
	breadcrumb := scheduleBackToOverview()
	breadcrumb.Columns = append(breadcrumb.Columns, backHistory.Columns...)

	titleRow := cardElement{
		Tag:               "column_set",
		FlexMode:          "none",
		HorizontalSpacing: "8px",
		Columns: []cardColumn{
			{Tag: "column", Width: "weighted", Weight: 1, VerticalAlign: "center", Elements: []cardElement{managementTextElement(scheduleName(e.Task), "heading-3", "default", nil)}},
		},
	}
	if e.Task.ID > 0 {
		titleRow.Columns = append(titleRow.Columns, cardColumn{
			Tag:           "column",
			Width:         "auto",
			VerticalAlign: "center",
			Elements:      []cardElement{managementTextElement(fmt.Sprintf("#%d", e.Task.ID), "notation", "grey", nil)},
		})
	}

	statusVal := scheduleText("status." + r.Status)
	if r.Status == "" {
		statusVal = scheduleText("status.not_run")
	}
	if r.TriggerSource == schedule.TriggerSourceManual {
		statusVal = managementConcat(statusVal, " · ", scheduleText("delivery.source.manual"))
	} else if r.TriggerSource == schedule.TriggerSourceScheduled {
		statusVal = managementConcat(statusVal, " · ", scheduleText("delivery.source.scheduled"))
	}

	metaRows := []cardElement{
		managementOverviewKeyValueRowElement(scheduleText("detail.field.status"), statusVal),
		managementOverviewKeyValueRowElement(scheduleText("run.field.planned"), scheduleTime(r.ScheduledAt)),
		managementOverviewKeyValueRowElement(scheduleText("run.field.duration"), scheduleTime(r.StartedAt)+" → "+scheduleTime(r.FinishedAt)),
	}
	switch r.DeliveryStatus {
	case schedule.DeliveryStatusDelivered, schedule.DeliveryStatusFailed, schedule.DeliveryStatusDenied, schedule.DeliveryStatusSkipped, schedule.DeliveryStatusPending:
		metaRows = append(metaRows, managementOverviewKeyValueRowElement(scheduleText("detail.field.delivery"), scheduleText("delivery_status."+r.DeliveryStatus)))
	}
	if strings.TrimSpace(r.TaskID) != "" {
		metaRows = append(metaRows, managementOverviewKeyValueRowElement(scheduleText("run.field.task"), strings.TrimSpace(r.TaskID)))
	}
	if strings.TrimSpace(r.SessionName) != "" {
		metaRows = append(metaRows, managementOverviewKeyValueRowElement(scheduleText("run.field.session"), strings.TrimSpace(r.SessionName)))
	}
	if strings.TrimSpace(r.TurnID) != "" {
		metaRows = append(metaRows, managementOverviewKeyValueRowElement(scheduleText("run.field.turn"), strings.TrimSpace(r.TurnID)))
	}
	metaBox := managementVerticalSummaryElement(fmt.Sprintf("amux_schedule_run_meta_%d_%d", e.Task.ID, r.ID), metaRows...)
	metaBox.Columns[0].BackgroundStyle = "blue-50"
	metaBox.Columns[0].VerticalSpacing = "4px"

	elements := []cardElement{
		breadcrumb,
		managementDividerElement(),
		titleRow,
		metaBox,
		managementDividerElement(),
		managementSectionTitleElement(scheduleText("run.section.output")),
	}

	body := truncateScheduleDeliveryBody(r.Result)
	if body != "" {
		elements = append(elements, cardElement{Tag: "markdown", Content: body})
	} else if r.Reason != "" {
		elements = append(elements, managementTextElement(scheduleReason(r.Reason), "normal", "default", nil))
	} else {
		elements = append(elements, managementTextElement(scheduleText("delivery.no_output"), "normal", "grey", nil))
	}
	if r.Reason != "" && body != "" {
		elements = append(elements, managementTextElement(scheduleReason(r.Reason), "notation", "orange", managementIconPtr("warning_outlined", "orange")))
	}

	var actions []cardElement
	if link := strings.TrimSpace(viewerURL); link != "" {
		openBtn := managementButtonElement(managementButton{
			Text: scheduleText("run.view_session"),
			Type: "primary",
			Name: fmt.Sprintf("schedule.run_open_session.%d.%d", e.Task.ID, r.ID),
			URL:  link,
		})
		openBtn.Name = fmt.Sprintf("schedule.run_open_session.%d.%d", e.Task.ID, r.ID)
		openBtn.Size = "small"
		actions = append(actions, openBtn)
	}
	backBtn := scheduleButton("run.back_history", "result", e.Task.ID, map[string]string{"page": strconv.Itoa(max(0, page))})
	backBtn.Size = "small"
	detailBtn := scheduleButton("detail", "detail", e.Task.ID, nil)
	detailBtn.Size = "small"
	actions = append(actions, backBtn, detailBtn)
	elements = append(elements, managementDividerElement(), managementFormButtonRowElement(actions...))
	if strings.TrimSpace(viewerURL) == "" {
		elements = append(elements, managementHintElement(scheduleText("run.timeline_hint")))
	}

	return marshalNativeManagementCard(managementCardView{
		Title:    scheduleText("title"),
		Summary:  scheduleText("title"),
		Theme:    "wathet",
		Icon:     managementIcon("setting_outlined", "blue"),
		Tags:     []cardTextTag{scheduleRunStatusTag(r.Status)},
		Elements: elements,
	})
}
func (b *Bridge) scheduleCardAction(ctx context.Context, event *callback.CardActionTriggerEvent, action string) (*callback.CardActionTriggerResponse, bool, error) {
	if !strings.HasPrefix(action, "schedule.") {
		return nil, false, nil
	}
	if b.schedules == nil || event.Event.Operator == nil {
		return cardToast("error", scheduleText("unavailable")), true, nil
	}
	open := event.Event.Operator.OpenID
	bot := b.currentBridgeId()
	message := ""
	if event.Event.Context != nil {
		message = event.Event.Context.OpenMessageID
	}
	privateCard := b.db != nil && b.db.isPrivateManagementCard(bot, open, message)
	var version uint64
	if privateCard {
		version = b.advanceModelServiceEditor(message)
	}
	op := strings.TrimPrefix(action, "schedule.")
	id, _ := strconv.ParseInt(managementCallbackString(event.Event.Action, "schedule_id"), 10, 64)
	source := managementCallbackString(event.Event.Action, "source")
	toastKey := "updated"
	var card string
	var err error
	if op == "home" {
		if b.canUseBridge(open) && !b.ownerConfirmed() {
			return cardToast("error", scheduleText("unavailable")), true, nil
		}
		card, err = b.amuxManagementHome(ctx, open)
	} else if op == "alerts" || strings.HasPrefix(op, "alert_") {
		// Administrator authority applies only to notification metadata and controls.
		// Never route these callbacks through the creator's execution authority.
		if !b.isBotOwner(open) {
			return cardToast("error", scheduleText("unavailable")), true, nil
		}
		if err = b.schedules.Store().SetNoticeRecipient(bot, open, time.Now()); err != nil {
			return nil, true, err
		}
		var entries []schedule.Entry
		entries, err = b.scheduleNotificationEntries(ctx)
		if err == nil && op != "alerts" {
			control := strings.TrimPrefix(op, "alert_")
			allowed := control == "ack" || control == "snooze" || control == "recheck"
			found := false
			for _, entry := range entries {
				if entry.Task.ID == id {
					found = true
					break
				}
			}
			if !allowed || !found {
				return cardToast("error", scheduleText("unavailable")), true, nil
			}
			err = b.schedules.Store().ChangeVisibility(id, control, time.Now())
			if err == nil {
				entries, err = b.scheduleNotificationEntries(ctx)
			}
		}
		page, _ := strconv.Atoi(managementCallbackString(event.Event.Action, "page"))
		card = renderScheduleAlerts(entries, page)
	} else if op == "list" {
		var entries []schedule.Entry
		entries, err = b.schedules.List(ctx, bot, open)
		page, _ := strconv.Atoi(managementCallbackString(event.Event.Action, "page"))
		card = renderSchedules(entries, managementCallbackString(event.Event.Action, "filter"), page)
	} else if op == "prune_completed" {
		err = b.schedules.Act(ctx, bot, open, 0, "prune_completed")
		if err == nil {
			var entries []schedule.Entry
			entries, err = b.schedules.List(ctx, bot, open)
			card = renderSchedules(entries, managementCallbackString(event.Event.Action, "filter"), 0)
			toastKey = "pruned"
		}
	} else if op == "delete" {
		err = b.schedules.Act(ctx, bot, open, id, "delete")
		if err == nil {
			if !privateCard && source == "delivery" {
				return cardToast("success", scheduleText("delivery.deleted")), true, nil
			}
			var entries []schedule.Entry
			entries, err = b.schedules.List(ctx, bot, open)
			card = renderSchedules(entries, "all", 0)
			toastKey = "deleted"
		}
	} else if op == "pause" && !privateCard && source == "delivery" {
		// On shared result/created delivery cards, the creator can pause normally;
		// the Bot owner also has a scoped emergency pause that only disables
		// future runs without exposing private details or execution authority.
		if actErr := b.schedules.Act(ctx, bot, open, id, "pause"); actErr == nil {
			return cardToast("success", scheduleText("delivery.paused")), true, nil
		}
		if b.isBotOwner(open) {
			if adminErr := b.schedules.Act(ctx, bot, open, id, "admin_pause"); adminErr == nil {
				return cardToast("success", scheduleText("delivery.admin_paused")), true, nil
			}
		}
		return cardToast("error", scheduleText("unavailable")), true, nil
	} else if op == "trigger" && !privateCard && source == "delivery" {
		if actErr := b.schedules.Act(ctx, bot, open, id, "trigger"); actErr != nil {
			return cardToast("error", scheduleText("unavailable")), true, nil
		}
		return cardToast("success", scheduleText("delivery.triggered")), true, nil
	} else if op == "run_detail" {
		var e schedule.Entry
		e, err = b.schedules.Get(ctx, bot, open, id)
		if err == nil {
			runID, _ := strconv.ParseInt(managementCallbackString(event.Event.Action, "run_id"), 10, 64)
			runIndex := -1
			if rawIdx := managementCallbackString(event.Event.Action, "run_index"); rawIdx != "" {
				if parsed, parseErr := strconv.Atoi(rawIdx); parseErr == nil {
					runIndex = parsed
				}
			}
			page, _ := strconv.Atoi(managementCallbackString(event.Event.Action, "page"))
			run, ok := findScheduleRun(e.Runs, runID, runIndex)
			if !ok {
				return cardToast("error", scheduleText("unavailable")), true, nil
			}
			card = renderScheduleRunDetail(e, run, page, b.sessionViewerURL(run.TargetID, run.SessionName))
		}
	} else {
		if op != "detail" && op != "result" {
			actOp := op
			if op == "delivery_target" || op == "delivery_policy" {
				actOp = op + ":" + managementCallbackString(event.Event.Action, "value")
			}
			err = b.schedules.Act(ctx, bot, open, id, actOp)
		}
		if err == nil {
			var e schedule.Entry
			e, err = b.schedules.Get(ctx, bot, open, id)
			if err == nil {
				page, _ := strconv.Atoi(managementCallbackString(event.Event.Action, "page"))
				card = renderScheduleDetailWithViewer(e, op == "result", page, b.sessionViewerURL)
			}
		}
	}
	if err != nil {
		return cardToast("error", scheduleText("unavailable")), true, nil
	}
	// Never write a private view into a forwarded/shared card.
	if privateCard {
		return b.managementCardResponse(open, message, card, version, cardToast("success", scheduleText(toastKey))), true, nil
	}
	// Quick actions on a result delivery card give an immediate toast without
	// dumping an unsolicited management card into DM, except when the user
	// explicitly asks to open management details.
	if source == "delivery" && op == "detail" {
		err = b.sendManagementCardToDM(ctx, open, card)
		return cardToast("success", scheduleText("delivery.detail_sent")), true, err
	}
	err = b.sendManagementCardToDM(ctx, open, card)
	return cardToast("success", scheduleText(toastKey)), true, err
}
func (b *Bridge) runScheduleNotices(ctx context.Context) {
	if b.schedules == nil {
		return
	}
	wake := b.ensureDeliveryWake()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			if err := b.deliverScheduleResults(ctx, time.Now()); err != nil {
				log.Warn().Err(err).Msg("bridge: schedule result delivery")
			}
		case <-ticker.C:
			now := time.Now()
			if err := b.deliverScheduleResults(ctx, now); err != nil {
				log.Warn().Err(err).Msg("bridge: schedule result delivery")
			}
			ticks++
			if ticks%4 == 0 {
				if err := b.deliverScheduleNotices(ctx, now); err != nil {
					log.Warn().Err(err).Msg("bridge: schedule notifications")
				}
			}
		}
	}
}

// Only currently authorized creator facts enter administrator status views.
func (b *Bridge) scheduleNotificationEntries(ctx context.Context) ([]schedule.Entry, error) {
	c := b.schedules
	bot := b.currentBridgeId()
	creators, err := c.Creators(bot)
	if err != nil {
		return nil, err
	}
	var all []schedule.Entry
	seen := map[int64]bool{}
	for _, open := range creators {
		entries, err := c.List(ctx, bot, open)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Visibility.Creator.BotID == bot && !seen[entry.Task.ID] {
				all = append(all, entry)
				seen[entry.Task.ID] = true
			}
		}
	}
	return all, nil
}
func (b *Bridge) deliverScheduleNotices(ctx context.Context, now time.Time) error {
	c := b.schedules
	bot := b.currentBridgeId()
	recipient := ""
	if b.ownerConfirmed() {
		recipient = strings.TrimSpace(b.secrets.OpenId)
	}
	all, err := b.scheduleNotificationEntries(ctx)
	if err != nil {
		return err
	}
	if err = c.Store().PrepareNotices(bot, recipient, all, now); err != nil {
		return err
	}
	if recipient == "" {
		return nil
	}
	notices, err := c.Store().Outbox(bot)
	if err != nil {
		return err
	}
	for _, n := range notices {
		if n.OpenID != recipient {
			continue
		}
		if n.RetryAt > now.Unix() || now.Hour() >= 22 || now.Hour() < 8 {
			continue
		}
		if n.MessageID == "" {
			allowed, e := c.Store().CanSendNotice(bot, n.OpenID, now)
			if e != nil {
				return e
			}
			if !allowed {
				continue
			}
		}
		var entries []schedule.Entry
		for _, id := range n.ScheduleIDs {
			v, err := c.Store().Visibility(id)
			if err != nil || v.Creator.BotID != bot {
				continue
			}
			e, err := c.Get(ctx, bot, v.Creator.OpenID, id)
			if err != nil || e.Visibility.Blocked || e.Visibility.NotificationsOff || e.Visibility.SnoozeUntil > now.Unix() {
				continue
			}
			if e.Visibility.PendingAt > now.Unix() || (e.Visibility.RecoveryAt > 0 && e.Visibility.Latest.Status != "completed") {
				continue
			}
			if n.MessageID == "" && e.Visibility.MessageID != "" && e.Visibility.Acknowledged {
				continue
			}
			entries = append(entries, e)
			n.Versions[id] = e.Visibility.Version
		}
		if len(entries) == 0 {
			if err = c.Store().DropNotice(n.ID); err != nil {
				return err
			}
			continue
		}
		// Re-render current authorized facts before every attempt, including retries.
		n.ScheduleIDs = nil
		for _, e := range entries {
			n.ScheduleIDs = append(n.ScheduleIDs, e.Task.ID)
		}
		renderEntries := append([]schedule.Entry(nil), entries...)
		if n.MessageID != "" {
			own, e := b.scheduleNotificationEntries(ctx)
			if e != nil {
				return e
			}
			seen := map[int64]bool{}
			for _, v := range entries {
				seen[v.Task.ID] = true
			}
			for _, v := range own {
				if v.Visibility.MessageID == n.MessageID && !seen[v.Task.ID] {
					renderEntries = append(renderEntries, v)
				}
			}
		}
		card := b.managementCardForRecipient(n.OpenID, renderScheduleNotice(renderEntries))
		sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		message, code, sendErr := b.sendScheduleNotice(sendCtx, n, card)
		cancel()
		if sendErr == nil {
			b.rememberPrivateManagementCard(n.OpenID, message)
		}
		if err = c.Store().NoticeResult(n, message, code, sendErr, now); err != nil {
			return err
		}
	}
	return nil
}

// Administrator cards intentionally omit prompts, raw diagnostics, results and
// execution controls. User-authored task names and creator IDs remain plain text.
func scheduleCreatorLabel(e schedule.Entry) string {
	name := []rune(strings.TrimSpace(e.CreatorName))
	if len(name) == 0 || string(name) == e.Visibility.Creator.OpenID {
		return e.Visibility.Creator.OpenID
	}
	if len(name) > 80 {
		name = append(name[:80], '…')
	}
	return string(name) + " (" + e.Visibility.Creator.OpenID + ")"
}
func scheduleAlertElements(entries []schedule.Entry, page int) []cardElement {
	elements := []cardElement{}
	for _, e := range entries {
		v := e.Visibility
		if v.IncidentAt != 0 && v.RecoveryAt == 0 {
			e.Visibility.Latest.Status = v.IncidentStatus
		}
		values := map[string]string{"page": strconv.Itoa(page)}
		elements = append(elements, managementDividerElement(),
			managementTextElement(scheduleText("task_identity", e.Task.ID, scheduleName(e.Task)), "heading-3", "default", nil),
			managementTextElement(scheduleText("creator", scheduleCreatorLabel(e)), "normal", "default", nil),
			managementTextElement(scheduleStatus(e), "normal", "default", nil),
			managementTextElement(scheduleText("next", scheduleNext(e)), "notation", "grey", nil))
		if v.Acknowledged {
			elements = append(elements, managementTextElement(scheduleText("acknowledged"), "notation", "grey", nil))
		}
		if v.SnoozeUntil > time.Now().Unix() {
			elements = append(elements, managementTextElement(scheduleText("snoozed", scheduleTime(v.SnoozeUntil)), "notation", "grey", nil))
		}
		if v.NotificationsOff {
			elements = append(elements, managementTextElement(scheduleText("alerts_off"), "notation", "grey", nil))
		}
		if v.Blocked {
			elements = append(elements, managementTextElement(scheduleText("blocked."+v.DeliveryError), "normal", "orange", nil))
		} else if v.DeliveryError != "" {
			key := "delivery_retry"
			if v.DeliveryError == "missing_admin" {
				key = "missing_admin"
			}
			elements = append(elements, managementTextElement(scheduleText(key), "normal", "orange", nil))
		}
		elements = append(elements, scheduleButton("ack", "alert_ack", e.Task.ID, values), scheduleButton("snooze", "alert_snooze", e.Task.ID, values))
		if v.DeliveryError != "" {
			elements = append(elements, scheduleButton("recheck", "alert_recheck", e.Task.ID, values))
		}
	}
	return elements
}
func renderScheduleNotice(entries []schedule.Entry) string {
	elements := []cardElement{managementTextElement(scheduleText("notice_intro", len(entries)), "notation", "grey", nil)}
	elements = append(elements, scheduleAlertElements(entries, 0)...)
	elements = append(elements, scheduleButton("alerts", "alerts", 0, nil))
	return marshalNativeManagementCard(managementCardView{Title: scheduleText("notice_title"), Summary: scheduleText("notice_title"), Theme: "orange", Elements: elements})
}
func renderScheduleAlerts(entries []schedule.Entry, page int) string {
	sort.SliceStable(entries, func(i, j int) bool {
		if scheduleNeedsAttention(entries[i]) != scheduleNeedsAttention(entries[j]) {
			return scheduleNeedsAttention(entries[i])
		}
		return entries[i].Task.ID > entries[j].Task.ID
	})
	pages := max(1, (len(entries)+4)/5)
	page = max(0, min(page, pages-1))
	elements := []cardElement{scheduleBackToOverview(), managementDividerElement(), managementTextElement(scheduleText("admin_policy"), "notation", "grey", nil)}
	elements = append(elements, scheduleAlertElements(entries[page*5:min(len(entries), (page+1)*5)], page)...)
	if len(entries) == 0 {
		elements = append(elements, managementTextElement(scheduleText("empty"), "normal", "grey", nil))
	}
	elements = append(elements, managementTextElement(scheduleText("page", page+1, pages, scheduleTime(time.Now().Unix())), "notation", "grey", nil))
	if page > 0 {
		elements = append(elements, scheduleButton("previous", "alerts", 0, map[string]string{"page": strconv.Itoa(page - 1)}))
	}
	if page+1 < pages {
		elements = append(elements, scheduleButton("next_page", "alerts", 0, map[string]string{"page": strconv.Itoa(page + 1)}))
	}
	elements = append(elements, scheduleButton("refresh", "alerts", 0, map[string]string{"page": strconv.Itoa(page)}))
	return marshalNativeManagementCard(managementCardView{Title: scheduleText("alerts"), Summary: scheduleText("alerts"), Theme: "wathet", Icon: managementIcon("setting_outlined", "blue"), Elements: elements})
}
func (b *Bridge) sendScheduleNotice(ctx context.Context, n schedule.Notice, card string) (string, int, error) {
	if n.MessageID != "" {
		resp, err := b.client.Im.Message.Patch(ctx, larkim.NewPatchMessageReqBuilder().MessageId(n.MessageID).Body(larkim.NewPatchMessageReqBodyBuilder().Content(card).Build()).Build())
		if err != nil {
			return "", 0, err
		}
		if !resp.Success() {
			return "", resp.Code, fmt.Errorf("schedule message update: %d", resp.Code)
		}
		return n.MessageID, 0, nil
	}
	resp, err := b.client.Im.Message.Create(ctx, larkim.NewCreateMessageReqBuilder().ReceiveIdType("open_id").Body(larkim.NewCreateMessageReqBodyBuilder().ReceiveId(n.OpenID).MsgType("interactive").Content(card).Uuid(n.ID).Build()).Build())
	if err != nil {
		return "", 0, err
	}
	if !resp.Success() {
		return "", resp.Code, fmt.Errorf("schedule message delivery: %d", resp.Code)
	}
	if resp.Data == nil || resp.Data.MessageId == nil {
		return "", 0, fmt.Errorf("schedule delivery missing receipt")
	}
	// A repeated UUID may return a previously created card without applying
	// the new body. Patch the known message before acknowledging this version,
	// including recovery after a crash between network success and DB commit.
	message := *resp.Data.MessageId
	n.MessageID = message
	_, code, patchErr := b.sendScheduleNotice(ctx, n, card)
	return message, code, patchErr
}

func scheduleReason(reason string) any {
	switch reason {
	case "not_dispatched", "admission_rejected", "admission_unconfirmed", "observation_unavailable":
		return scheduleText("reason." + reason)
	default:
		return reason
	}
}

func scheduleName(t schedule.Task) string {
	name := []rune(t.Name)
	if len(name) == 0 {
		return "#" + strconv.FormatInt(t.ID, 10)
	}
	if len(name) > 120 {
		return string(name[:120]) + "…"
	}
	return t.Name
}

func scheduleNeedsAttention(e schedule.Entry) bool {
	return (e.Visibility.IncidentAt != 0 && e.Visibility.RecoveryAt == 0) || e.Visibility.Blocked ||
		(e.Visibility.IncidentAt == 0 && schedule.Attention(e.Visibility.Latest.Status))
}

func scheduleBackToOverview() cardElement {
	return managementNavigationBackElement(managementCopy("management.back.to.overview"), "schedule.home", "schedule.back.overview")
}
