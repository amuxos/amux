package runtime

import (
	"os"

	"github.com/amuxos/amux/internal/i18n"
)

var executionCatalog = func() *i18n.Catalog {
	c, err := i18n.New([]byte(`{
  "acp_agent_type": {"other": "Adapter that maps execution settings to ACP configuration"},
  "acp_model": {"other": "Frozen model selection for the ACP session"},
  "acp_reasoning_effort": {"other": "Frozen reasoning effort for the ACP session"},
  "turn_idle": {"other": "Turn idle timeout in seconds (-1=default, 0=disabled)"},
  "turn_hard_cap": {"other": "Turn duration limit in seconds (-1=default, 0=disabled)"},
  "tool_idle": {"other": "Reserved tool progress timeout; ACP currently uses the turn duration limit"},
  "repeat_tool": {"other": "Consecutive identical tool call limit (-1=default, 0=disabled)"},
  "updated": {"other": "Updated execution model/reasoning without restarting session"}
}`), []byte(`{
  "acp_agent_type": {"other": "将执行设置映射到 ACP 配置的适配器"},
  "acp_model": {"other": "ACP 会话冻结的模型选择"},
  "acp_reasoning_effort": {"other": "ACP 会话冻结的推理档位"},
  "turn_idle": {"other": "Turn 无有效活动超时秒数（-1 使用默认值，0 禁用）"},
  "turn_hard_cap": {"other": "Turn 绝对时长上限秒数（-1 使用默认值，0 禁用）"},
  "tool_idle": {"other": "保留的工具进度超时参数；ACP 当前使用 turn 绝对时长上限"},
  "repeat_tool": {"other": "连续相同工具调用次数上限（-1 使用默认值，0 禁用）"},
  "updated": {"other": "已更新执行模型和推理档位，会话继续运行"}
}`))
	if err != nil {
		panic(err)
	}
	return c
}()

func executionMessage(key string) string {
	return executionCatalog.Format(i18n.Resolve(os.Getenv("AMUX_LANGUAGE"), "", ""), key)
}
