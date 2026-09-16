package task

import "github.com/amuxos/amux/internal/agentstream"

func projectToolActivity(change agentstream.ToolChanged, red *Redactor) ToolActivity {
	status := string(change.Phase)
	if change.Phase == agentstream.ToolPhaseStarted || change.Phase == agentstream.ToolPhaseUpdated {
		status = "running"
	}
	return ToolActivity{
		ID:     redactStr(red, change.Tool.ID),
		Name:   redactStr(red, change.Tool.Name),
		Title:  redactStr(red, change.Tool.Title),
		Status: status,
	}
}
