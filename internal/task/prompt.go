package task

import "strings"

// childPrompt wraps a task's prompt with the fallback result-envelope contract
// when injectEnvelope is true.
//
// For agents whose provider emits a native turn result (capability NativeResult),
// the runner passes injectEnvelope=false and records the summary from that native
// event, so no envelope instruction is added. For agents without native results
// the envelope is the fallback per the design's "降级 fallback：文本 result envelope":
// the child prints an `amux_task_result` line (see ParseEnvelope) so a read-only
// task that answers in prose still yields a collectable summary instead of being
// judged failed:missing_result.
//
// The instruction is appended (not prepended) so the user's task stays the
// primary directive. Parsing remains tolerant (ParseEnvelope scans for the
// marker), so it still works if the agent wraps the JSON in a code fence.
func childPrompt(taskText string, injectEnvelope bool) string {
	if !injectEnvelope {
		return taskText
	}
	var b strings.Builder
	b.WriteString(taskText)
	b.WriteString(envelopeInstruction)
	return b.String()
}

const envelopeInstruction = "\n\n" +
	"---\n" +
	"When you have finished the task above, end your final message with a single line\n" +
	"containing exactly one JSON object, so the runtime can record your result:\n" +
	`{"amux_task_result": {"status": "ok", "summary": "<one concise sentence describing the outcome>"}}` + "\n" +
	`Set "status" to "ok" if you completed the task, or "error" with a summary of what ` +
	"blocked you otherwise. Do not omit this line.\n"
