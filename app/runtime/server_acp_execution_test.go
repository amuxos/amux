package runtime

import (
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/session"
	"github.com/amuxos/amux/internal/store"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

type executionConfigTestAdapter struct{}

var executionConfigTestRegistration sync.Once

func (*executionConfigTestAdapter) Cmd(args []string) *exec.Cmd {
	return exec.Command("unused", args...)
}
func (*executionConfigTestAdapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{Protocol: adapter.ProtocolACP}
}
func (*executionConfigTestAdapter) ACPExecutionConfig(model, effort string) (map[string]string, error) {
	if model == "invalid" {
		return nil, errors.New("unsupported model")
	}
	return map[string]string{"model": model + "/" + effort}, nil
}

func TestACPHotReconfigureUsesFrozenSelection(t *testing.T) {
	agentType := "acp-execution-test"
	executionConfigTestRegistration.Do(func() {
		if err := adapter.Register(agentType, func() adapter.Adapter { return &executionConfigTestAdapter{} }); err != nil {
			t.Fatal(err)
		}
	})
	for _, tt := range []struct {
		name     string
		oldModel string
		newModel string
		effort   string
		provider bool
		wantHot  bool
	}{
		{name: "frozen selection", oldModel: "provider/old", newModel: "provider/new", effort: "high", wantHot: true},
		{name: "default variant", oldModel: "provider/old", newModel: "provider/new", wantHot: true},
		{name: "unknown previous default", newModel: "provider/new"},
		{name: "invalid mapping", oldModel: "provider/old", newModel: "invalid"},
		{name: "provider change requires relaunch", oldModel: "provider/old", newModel: "provider/new", provider: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "runtime.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mgr := session.NewManager(db)
			sess, err := mgr.Create("acp-model", agentType, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			oldReq := &pb.RunRequest{AgentProfile: "team/agent", AgentType: agentType,
				ProfileSpec: &pb.AgentProfileSpec{Model: tt.oldModel, ReasoningEffort: "medium"}}
			if err := mgr.RecordLaunchParams(sess, nil, nil, "cli", "singleton", oldReq.AgentProfile,
				"live/model/variant", launchSpecSnapshot(oldReq)); err != nil {
				t.Fatal(err)
			}
			if err := mgr.RecordAgentSessionID(sess, "native-session"); err != nil {
				t.Fatal(err)
			}
			proc := &serverTestProc{}
			sess.SetProc(proc)
			if err := mgr.SetRunning(sess); err != nil {
				t.Fatal(err)
			}
			replacement := &pb.RunRequest{AgentProfile: oldReq.AgentProfile, AgentType: agentType,
				ProfileSpec: &pb.AgentProfileSpec{Model: tt.newModel, ReasoningEffort: tt.effort}}
			if tt.provider {
				replacement.ProfileSpec.Provider = &pb.AgentProfileProvider{Ref: "custom"}
			}
			hot, err := (&server{mgr: mgr}).tryHotReconfigureExecution(sess, sess.Snapshot(), replacement,
				nil, nil, tt.newModel, launchSpecSnapshot(replacement))
			if err != nil || hot != tt.wantHot {
				t.Fatalf("hot=%v err=%v, want hot=%v", hot, err, tt.wantHot)
			}
			if tt.wantHot {
				if !reflect.DeepEqual(proc.reconfigurations, []string{tt.newModel + ":" + tt.effort}) {
					t.Fatalf("reconfigurations=%v", proc.reconfigurations)
				}
				got := sess.Snapshot()
				if got.AgentSessionID != "native-session" || got.EffectiveModel != tt.newModel || proc.closed {
					t.Fatalf("hot reconfiguration replaced native session: %+v", got)
				}
			} else if len(proc.reconfigurations) != 0 {
				t.Fatalf("unexpected reconfiguration: %v", proc.reconfigurations)
			}
		})
	}
}

func TestLaunchSpecModelDoesNotUseLiveModel(t *testing.T) {
	for _, tt := range []struct{ spec, want string }{
		{`{"profile_spec":{"model":" requested ","configured_model":"fallback"}}`, "requested"},
		{`{"profile_spec":{"configured_model":" fallback "}}`, "fallback"},
		{`{"effective_model":"live/model/variant"}`, ""},
		{`null`, ""},
		{`invalid`, ""},
	} {
		if got := launchSpecModel(tt.spec); got != tt.want {
			t.Errorf("launchSpecModel(%s)=%q, want %q", tt.spec, got, tt.want)
		}
	}
}

func TestExecutionCopyLanguagesAndFallback(t *testing.T) {
	for _, language := range []string{"en", "zh-CN", "unknown"} {
		t.Run(language, func(t *testing.T) {
			t.Setenv("AMUX_LANGUAGE", language)
			for _, key := range []string{"acp_agent_type", "acp_model", "acp_reasoning_effort", "turn_idle", "turn_hard_cap", "tool_idle", "repeat_tool", "updated"} {
				if got := executionMessage(key); got == "" || got == key || strings.Contains(got, "{{") {
					t.Fatalf("%s=%q", key, got)
				}
			}
		})
	}
}
