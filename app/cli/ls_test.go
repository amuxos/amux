package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/viper"
)

func TestSessionListResolvesLocalRuntimeAlias(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("runtime.id", "runtime-laptop")
	viper.Set("runtime.name", "my-laptop")
	targets := []targetSessions{
		{
			target: targetRef{ID: "runtime-laptop", Name: "my-laptop"},
			sessions: []*sessionEntry{
				{Session: pb.Session{Id: "local-main", Name: "default-main", AgentProfile: "builtin/codex"}},
				{Session: pb.Session{Id: "local-child", Name: "child", OwnerTaskId: "task-1"}},
			},
		},
		{
			target:   targetRef{ID: "runtime-remote", Name: "remote"},
			sessions: []*sessionEntry{{Session: pb.Session{Id: "remote-main", Name: "default-main"}}},
		},
	}
	for _, ref := range []string{"local", "my-laptop", "runtime-laptop"} {
		t.Run(ref, func(t *testing.T) {
			list := newLsCmdWithResolver(func() (*sessionResolver, error) {
				return resolverForTests(targets), nil
			})
			list.Use = "list [target]"
			cmd := newRootCmd()
			existing, _, err := cmd.Find([]string{"session", "list"})
			if err != nil {
				t.Fatal(err)
			}
			sessionCmd := existing.Parent()
			sessionCmd.RemoveCommand(existing)
			sessionCmd.AddCommand(list)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"session", "list", ref, "--json"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("session list %s: %v", ref, err)
			}
			var got struct {
				Sessions []sessionJSONView `json:"sessions"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("decode session list: %v", err)
			}
			if len(got.Sessions) != 1 || got.Sessions[0].ID != "local-main" || got.Sessions[0].TargetName != "my-laptop" {
				t.Fatalf("session list %s = %+v, want only local user Session", ref, got.Sessions)
			}
		})
	}
}

func TestShouldShowSessionForLsHidesAllTaskChildren(t *testing.T) {
	tests := []struct {
		name string
		s    *sessionEntry
		opts lsOptions
		want bool
	}{
		{
			name: "archived user session remains visible",
			s:    &sessionEntry{Session: pb.Session{Name: "work", State: "archived"}},
			want: true,
		},
		{
			name: "running task child hidden by default",
			s:    &sessionEntry{Session: pb.Session{Name: "subtask", State: "running", OwnerTaskId: "task-1"}},
			want: false,
		},
		{
			name: "sleeping task child hidden by default",
			s:    &sessionEntry{Session: pb.Session{Name: "subtask", State: "sleeping", OwnerTaskId: "task-1"}},
			want: false,
		},
		{
			name: "archived task child hidden by default",
			s:    &sessionEntry{Session: pb.Session{Name: "subtask", State: "archived", OwnerTaskId: "task-1"}},
			want: false,
		},
		{
			name: "all includes running task child",
			s:    &sessionEntry{Session: pb.Session{Name: "subtask", State: "running", OwnerTaskId: "task-1"}},
			opts: lsOptions{All: true},
			want: true,
		},
		{
			name: "include tasks includes running task child",
			s:    &sessionEntry{Session: pb.Session{Name: "subtask", State: "running", OwnerTaskId: "task-1"}},
			opts: lsOptions{IncludeTasks: true},
			want: true,
		},
		{
			name: "nil hidden",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldShowSessionForLs(tt.s, tt.opts); got != tt.want {
				t.Fatalf("shouldShowSessionForLs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatSessionListJSONIsStableAndKeepsRawValues(t *testing.T) {
	sessions := []*resolvedSession{
		{
			Target: targetRef{ID: "runtime-1", Name: "local"},
			Session: &sessionEntry{Session: pb.Session{
				Id:                 "session-1",
				Name:               "work",
				AgentType:          "codex",
				AgentProfile:       "team/coder@abc123",
				State:              "running",
				Pid:                42,
				CreatedAt:          1000,
				UpdatedAt:          1001,
				IdleTimeoutSeconds: 120,
				LastActivityAt:     1002,
				AutoSleepEligible:  true,
				Cwd:                "/repo",
			}},
		},
	}

	var buf bytes.Buffer
	if err := formatSessionList(&buf, sessions, true); err != nil {
		t.Fatalf("formatSessionList JSON: %v", err)
	}
	var got struct {
		Schema   string `json:"schema"`
		Sessions []struct {
			ID       string `json:"id"`
			TargetID string `json:"target_id"`
			PID      int32  `json:"pid"`
			Created  int64  `json:"created_at"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON %q: %v", buf.String(), err)
	}
	if got.Schema != "amux.session.list.v1" {
		t.Fatalf("schema = %q", got.Schema)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "session-1" || got.Sessions[0].TargetID != "runtime-1" || got.Sessions[0].PID != 42 || got.Sessions[0].Created != 1000 {
		t.Fatalf("sessions = %#v", got.Sessions)
	}
}

func TestFormatSessionListAlwaysShowsTarget(t *testing.T) {
	sessions := []*resolvedSession{
		{
			Target: targetRef{Name: "local"},
			Session: &sessionEntry{Session: pb.Session{
				Name:               "work",
				AgentType:          "codex",
				AgentProfile:       "team/coder@abc123",
				State:              "running",
				Pid:                42,
				CreatedAt:          1000,
				IdleTimeoutSeconds: 120,
				LastActivityAt:     1001,
				LastSleepReason:    "idle_timeout",
				AutoSleepEligible:  true,
			}},
		},
	}

	var buf bytes.Buffer
	if err := formatSessionList(&buf, sessions, false); err != nil {
		t.Fatalf("formatSessionList: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"NAME", "TYPE", "PROFILE", "STATE", "PID", "TARGET", "IDLE_TIMEOUT", "LAST_ACTIVITY", "SLEEP_REASON", "AUTO_SLEEP", "work", "team/coder@abc123", "local", "120s", "idle_timeout", "yes"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}
