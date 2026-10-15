package bridge

import (
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/viper"

	"github.com/amuxos/amux/internal/config"
)

func TestForegroundInstructionsOffByDefault(t *testing.T) {
	b := &Bridge{}
	chat := &LarkChat{BridgeId: "b1", RootMsgID: singletonStreamKey("b1"), TargetID: "t", SessionName: "s"}
	if got := b.sessionSystemInstructions(chat); got != "" {
		t.Fatalf("switch off must send no protocol, got %q", got)
	}
	if got := b.sessionStartInstructions(); got != "" {
		t.Fatalf("switch off must start sessions without protocol, got %q", got)
	}
	if b.busyInputSteers() {
		t.Fatal("switch off must keep legacy interrupt busy input")
	}
}

func TestForegroundInstructionsEveryTurnOutOfBand(t *testing.T) {
	b := &Bridge{}
	b.steerForeground.Store(true)
	singleton := &LarkChat{BridgeId: "b1", RootMsgID: singletonStreamKey("b1"), TargetID: "t", SessionName: "main"}
	thread := &LarkChat{BridgeId: "b1", RootMsgID: "om-a", TargetID: "t", SessionName: "feishu-a"}
	for i := 0; i < 25; i++ {
		for _, chat := range []*LarkChat{singleton, thread} {
			got := b.sessionSystemInstructions(chat)
			if !strings.Contains(got, foregroundInstructions) || !strings.Contains(got, "amux 文件交付说明") {
				t.Fatalf("submit %d of %s lacks protocol/file note: %q", i, chat.RootMsgID, got)
			}
		}
	}
	if b.sessionSystemInstructions(&LarkChat{BridgeId: "b1"}) != "" {
		t.Fatal("chat without root must get nothing")
	}
	b.setSteerForeground(false)
	if b.sessionSystemInstructions(thread) != "" {
		t.Fatal("live toggle off must stop the protocol on the next turn")
	}
}

func TestForegroundProtocolContent(t *testing.T) {
	text := foregroundInstructions
	if n := len([]rune(text)); n >= 300 {
		t.Fatalf("protocol too long: %d runes", n)
	}
	for _, want := range []string{"自行判断", "另起一段", "自然语言", "不要复述", "由你决定"} {
		if !strings.Contains(text, want) {
			t.Fatalf("protocol lacks %q", want)
		}
	}
	if regexp.MustCompile(`\d+\s*(秒|分钟|s\b)`).MatchString(text) {
		t.Fatal("protocol must not prescribe fixed time thresholds")
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(text, "["), "]")
	if strings.ContainsAny(inner, "[]") {
		t.Fatal("protocol must not contain brackets (breaks guidance stripping)")
	}
	full := "你好\n\n" + strings.TrimSpace(artifactInstructions) + "\n\n" + text
	if got := stripAmuxGuidance(full); got != "你好" {
		t.Fatalf("inline fallback not stripped: %q", got)
	}
}

func TestBusyInputEnvInterruptOverridesSwitch(t *testing.T) {
	b := &Bridge{}
	b.steerForeground.Store(true)
	if !b.busyInputSteers() {
		t.Fatal("switch on must steer")
	}
	t.Setenv(busyInputEnv, "interrupt")
	if b.busyInputSteers() {
		t.Fatal("AMUX_FEISHU_BUSY_INPUT=interrupt must force legacy behavior")
	}
}

func TestSteerForegroundConfigDefaultOff(t *testing.T) {
	prev := viper.Get(config.FeishuSteerForegroundKey)
	t.Cleanup(func() { viper.Set(config.FeishuSteerForegroundKey, prev) })
	viper.Set(config.FeishuSteerForegroundKey, nil)
	if config.GetBridgeFeishuSteerForeground() {
		t.Fatal("steer_foreground must default off")
	}
	viper.Set(config.FeishuSteerForegroundKey, true)
	if !config.GetBridgeFeishuSteerForeground() {
		t.Fatal("steer_foreground=true must enable")
	}
}
