package bridge

import (
	"context"
	"fmt"
	"time"

	"github.com/amuxos/amux/internal/bridge/command"
	"github.com/amuxos/amux/internal/distribution"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func (b *Bridge) executeSessionControl(ctx context.Context, chat *LarkChat, cmd command.Command) (string, bool, error) {
	if chat == nil {
		return "", false, nil
	}
	switch cmd {
	case command.CommandStatus:
		return bindingStatusText(chat), true, nil
	case command.CommandInterrupt:
		interruptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if _, err := b.hub.Route(interruptCtx, chat.TargetID, "Interrupt", &pb.InterruptRequest{Name: chat.SessionName}); err != nil {
			return "", true, fmt.Errorf("中断失败：%w", err)
		}
		return "⏹ 已请求中断当前 Agent 执行", true, nil
	case command.CommandRestart:
		restartCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if _, err := b.hub.Route(restartCtx, chat.TargetID, "Restart", &pb.RestartRequest{Name: chat.SessionName}); err != nil {
			return "", true, fmt.Errorf("重启失败：%w", err)
		}
		return "🔁 已重启 session " + chat.SessionName, true, nil
	case command.CommandCompact, command.CommandClear:
		if !supportsNativeSkillDiscovery(chat.AgentType) {
			return fmt.Sprintf("/%s 当前仅支持 %s session。", cmd, distribution.CodexCompatibleChoice), true, nil
		}
		if err := b.submitSessionControl(ctx, chat, string(cmd)); err != nil {
			return "", true, fmt.Errorf("/%s 提交失败：%w", cmd, err)
		}
		if cmd == command.CommandCompact {
			return "🗜️ 已将上下文压缩加入队列；前序输入完成后执行。", true, nil
		}
		return "🧹 已将上下文清理加入队列；执行后将在当前任务中开始新对话。", true, nil
	default:
		return "", false, nil
	}
}
