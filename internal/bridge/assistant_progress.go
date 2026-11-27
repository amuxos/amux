package bridge

import "github.com/amuxos/amux/internal/agentui"

const (
	maxAssistantProgressRunes = agentui.MaxProgressRunes
	omittedAssistantProgress  = agentui.OmittedProgressMarker
)

// assistantProgress aliases agentui.ReplyProjection so Feishu stream rendering
// shares canonical narration, reconciliation, and steered-turn merging with
// other first-party surfaces.
type assistantProgress = agentui.ReplyProjection

var (
	truncateAssistantProgress = agentui.TruncateProgress
	trimExactResultSuffix     = agentui.TrimExactResultSuffix
	collapseRepeatedText      = agentui.CollapseRepeatedText
	mergeSteeredFinalAnswer   = agentui.MergeSteeredFinalAnswer
)
