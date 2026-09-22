package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/schedule"
	"github.com/amuxos/amux/internal/task"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/spf13/cobra"
)

func completionValues(candidates []string) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		v, _, _ := strings.Cut(c, "\t")
		out = append(out, v)
	}
	return out
}

func TestScheduleRunModeCompletions(t *testing.T) {
	got := completionValues(scheduleRunModeCompletions(""))
	want := []string{"reuse-session", "new-session-per-run"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scheduleRunModeCompletions(empty) = %v, want %v", got, want)
	}

	got = completionValues(scheduleRunModeCompletions("new"))
	want = []string{"new-session-per-run"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scheduleRunModeCompletions(new) = %v, want %v", got, want)
	}
}

func TestScheduleSpecCompletions(t *testing.T) {
	got := completionValues(scheduleSpecCompletions(""))
	want := []string{"every", "daily", "at", "cron"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scheduleSpecCompletions(empty) = %v, want %v", got, want)
	}

	got = completionValues(scheduleSpecCompletions("cr"))
	want = []string{"cron"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scheduleSpecCompletions(cr) = %v, want %v", got, want)
	}
}

func TestSessionRefCompletions(t *testing.T) {
	targets := []targetSessions{
		{target: targetRef{Name: "devbox"}, sessions: []*sessionEntry{{Session: pb.Session{Name: "c2"}}}},
		{target: targetRef{Name: "local"}, sessions: []*sessionEntry{{Session: pb.Session{Name: "c1"}}, {Session: pb.Session{Name: "z"}}}},
	}

	got := sessionRefCompletions(targets, "")
	want := []string{"devbox/c2", "local/c1", "local/z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionRefCompletions(empty) = %v, want %v", got, want)
	}

	got = sessionRefCompletions(targets, "local/c")
	want = []string{"local/c1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sessionRefCompletions(prefix) = %v, want %v", got, want)
	}
}

func TestScheduleIDCompletions(t *testing.T) {
	items := []*pb.ScheduleItem{
		{Id: 12, Name: "hourly", TargetName: "local", SessionName: "c1", Spec: "every 1h", Enabled: true},
		{Id: 3, Name: "daily", TargetName: "dev", SessionName: "c2", Spec: "daily 09:00", Enabled: false},
	}

	got := completionValues(scheduleIDCompletions(items, "", scheduleAny))
	want := []string{"3", "12"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scheduleIDCompletions(any) = %v, want %v", got, want)
	}

	got = completionValues(scheduleIDCompletions(items, "", scheduleEnabledOnly))
	want = []string{"12"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scheduleIDCompletions(enabled) = %v, want %v", got, want)
	}

	got = completionValues(scheduleIDCompletions(items, "", scheduleDisabledOnly))
	want = []string{"3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scheduleIDCompletions(disabled) = %v, want %v", got, want)
	}

	got = completionValues(scheduleIDCompletions(items, "1", scheduleAny))
	want = []string{"12"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scheduleIDCompletions(prefix) = %v, want %v", got, want)
	}
}

func TestTargetCompletions(t *testing.T) {
	targets := []targetSessions{
		{target: targetRef{ID: "id-b", Name: "beta"}},
		{target: targetRef{ID: "id-a", Name: "alpha"}},
		{target: targetRef{ID: "alpha", Name: "alpha"}},
	}
	got := completionValues(targetCompletions(targets, ""))
	want := []string{"alpha", "beta", "id-a", "id-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targetCompletions(empty) = %v, want %v", got, want)
	}
	got = completionValues(targetCompletions(targets, "id-"))
	want = []string{"id-a", "id-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targetCompletions(prefix) = %v, want %v", got, want)
	}
}

func TestConfigCompletions(t *testing.T) {
	got := configKeyCompletions("bridge.feishu.prompt")
	want := []string{
		"bridge.feishu.prompt.shared_thread_enabled",
		"bridge.feishu.prompt.topic_root_enabled",
		"bridge.feishu.prompt.shared_thread_template_file",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("configKeyCompletions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.document_comments")
	want = []string{"bridge.feishu.document_comments.enabled"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("document comment config completions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.group_watch")
	want = []string{
		"bridge.feishu.group_watch.enabled",
		"bridge.feishu.group_watch.chat_ids",
		"bridge.feishu.group_watch.sender_types",
		"bridge.feishu.group_watch.chat_sender_types.<chat_id>",
		"bridge.feishu.group_watch.poll_interval_seconds",
		"bridge.feishu.group_watch.lookback_seconds",
		"bridge.feishu.group_watch.dedup.enabled",
		"bridge.feishu.group_watch.dedup.window_seconds",
		"bridge.feishu.group_watch.max_active_sessions_per_chat",
		"bridge.feishu.group_watch.idle_timeout_seconds",
		"bridge.feishu.group_watch.thinking_process",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("group_watch config completions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.bot_to_bot")
	want = []string{
		"bridge.feishu.bot_to_bot.enabled",
		"bridge.feishu.bot_to_bot.max_turns_per_thread",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bot to bot config completions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.workspace_defaults")
	want = []string{
		"bridge.feishu.workspace_defaults.agent",
		"bridge.feishu.workspace_defaults.target",
		"bridge.feishu.workspace_defaults.cwd",
		"bridge.feishu.workspace_defaults.model",
		"bridge.feishu.workspace_defaults.args",
		"bridge.feishu.workspace_defaults.idle_timeout_seconds",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace defaults config completions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.profiles.")
	want = []string{
		"bridge.feishu.profiles.<profile>.workspace_defaults.agent",
		"bridge.feishu.profiles.<profile>.workspace_defaults.target",
		"bridge.feishu.profiles.<profile>.workspace_defaults.cwd",
		"bridge.feishu.profiles.<profile>.workspace_defaults.model",
		"bridge.feishu.profiles.<profile>.workspace_defaults.args",
		"bridge.feishu.profiles.<profile>.workspace_defaults.idle_timeout_seconds",
		"bridge.feishu.profiles.<profile>.interaction.mode",
		"bridge.feishu.profiles.<profile>.presentation.thinking_process",
		"bridge.feishu.profiles.<profile>.presentation.completion_notification_after_seconds",
		"bridge.feishu.profiles.<profile>.bot_e2e.sender_open_ids",
		"bridge.feishu.profiles.<profile>.bot_e2e.sender_roles",
		"bridge.feishu.profiles.<profile>.document_comments.enabled",
		"bridge.feishu.profiles.<profile>.document_comments.agent_profile_ref",
		"bridge.feishu.profiles.<profile>.document_comments.runtime_ref",
		"bridge.feishu.profiles.<profile>.document_comments.model_ref",
		"bridge.feishu.profiles.<profile>.document_comments.reasoning_effort",
		"bridge.feishu.profiles.<profile>.bot_to_bot.enabled",
		"bridge.feishu.profiles.<profile>.bot_to_bot.max_turns_per_thread",
		"bridge.feishu.profiles.<profile>.group_watch.enabled",
		"bridge.feishu.profiles.<profile>.group_watch.chat_ids",
		"bridge.feishu.profiles.<profile>.group_watch.sender_types",
		"bridge.feishu.profiles.<profile>.group_watch.chat_sender_types.<chat_id>",
		"bridge.feishu.profiles.<profile>.group_watch.poll_interval_seconds",
		"bridge.feishu.profiles.<profile>.group_watch.lookback_seconds",
		"bridge.feishu.profiles.<profile>.group_watch.dedup.enabled",
		"bridge.feishu.profiles.<profile>.group_watch.dedup.window_seconds",
		"bridge.feishu.profiles.<profile>.group_watch.max_active_sessions_per_chat",
		"bridge.feishu.profiles.<profile>.group_watch.idle_timeout_seconds",
		"bridge.feishu.profiles.<profile>.group_watch.agent_profile_ref",
		"bridge.feishu.profiles.<profile>.group_watch.runtime_ref",
		"bridge.feishu.profiles.<profile>.group_watch.model_ref",
		"bridge.feishu.profiles.<profile>.group_watch.reasoning_effort",
		"bridge.feishu.profiles.<profile>.group_watch.thinking_process",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("profile workspace defaults config completions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.profiles.coder.bot_e2e.")
	want = []string{
		"bridge.feishu.profiles.coder.bot_e2e.sender_open_ids",
		"bridge.feishu.profiles.coder.bot_e2e.sender_roles",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete profile bot E2E config completions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.profiles.coder.workspace_defaults.")
	want = []string{
		"bridge.feishu.profiles.coder.workspace_defaults.agent",
		"bridge.feishu.profiles.coder.workspace_defaults.target",
		"bridge.feishu.profiles.coder.workspace_defaults.cwd",
		"bridge.feishu.profiles.coder.workspace_defaults.model",
		"bridge.feishu.profiles.coder.workspace_defaults.args",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete profile workspace defaults config completions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.profiles.coder.presentation.")
	want = []string{
		"bridge.feishu.profiles.coder.presentation.thinking_process",
		"bridge.feishu.profiles.coder.presentation.completion_notification_after_seconds",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete profile presentation config completions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.profiles.coder.document_comments.")
	want = []string{
		"bridge.feishu.profiles.coder.document_comments.enabled",
		"bridge.feishu.profiles.coder.document_comments.agent_profile_ref",
		"bridge.feishu.profiles.coder.document_comments.runtime_ref",
		"bridge.feishu.profiles.coder.document_comments.model_ref",
		"bridge.feishu.profiles.coder.document_comments.reasoning_effort",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete profile document comments config completions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.profiles.coder.group_watch.")
	want = []string{
		"bridge.feishu.profiles.coder.group_watch.enabled",
		"bridge.feishu.profiles.coder.group_watch.chat_ids",
		"bridge.feishu.profiles.coder.group_watch.sender_types",
		"bridge.feishu.profiles.coder.group_watch.chat_sender_types.",
		"bridge.feishu.profiles.coder.group_watch.poll_interval_seconds",
		"bridge.feishu.profiles.coder.group_watch.lookback_seconds",
		"bridge.feishu.profiles.coder.group_watch.dedup.enabled",
		"bridge.feishu.profiles.coder.group_watch.dedup.window_seconds",
		"bridge.feishu.profiles.coder.group_watch.max_active_sessions_per_chat",
		"bridge.feishu.profiles.coder.group_watch.idle_timeout_seconds",
		"bridge.feishu.profiles.coder.group_watch.agent_profile_ref",
		"bridge.feishu.profiles.coder.group_watch.runtime_ref",
		"bridge.feishu.profiles.coder.group_watch.model_ref",
		"bridge.feishu.profiles.coder.group_watch.reasoning_effort",
		"bridge.feishu.profiles.coder.group_watch.thinking_process",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete profile group_watch config completions = %v, want %v", got, want)
	}
	got = configKeyCompletions("bridge.feishu.profiles.coder.bot_to_bot.")
	want = []string{
		"bridge.feishu.profiles.coder.bot_to_bot.enabled",
		"bridge.feishu.profiles.coder.bot_to_bot.max_turns_per_thread",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete profile bot_to_bot config completions = %v, want %v", got, want)
	}
	vals, directive := completeConfigValueForKey("runtime.auto_start", "t")
	if directive != cobra.ShellCompDirectiveNoFileComp || !reflect.DeepEqual(vals, []string{"true"}) {
		t.Fatalf("bool config completion = %v %v, want [true] NoFileComp", vals, directive)
	}
	vals, directive = completeConfigValueForKey("bridge.feishu.prompt.topic_root_enabled", "f")
	if directive != cobra.ShellCompDirectiveNoFileComp || !reflect.DeepEqual(vals, []string{"false"}) {
		t.Fatalf("topic root bool config completion = %v %v, want [false] NoFileComp", vals, directive)
	}
	vals, directive = completeConfigValueForKey("bridge.feishu.document_comments.enabled", "t")
	if directive != cobra.ShellCompDirectiveNoFileComp || !reflect.DeepEqual(vals, []string{"true"}) {
		t.Fatalf("document comment bool config completion = %v %v, want [true] NoFileComp", vals, directive)
	}
	vals, directive = completeConfigValueForKey("bridge.feishu.profiles.coder.document_comments.enabled", "f")
	if directive != cobra.ShellCompDirectiveNoFileComp || !reflect.DeepEqual(vals, []string{"false"}) {
		t.Fatalf("profile document comment bool config completion = %v %v, want [false] NoFileComp", vals, directive)
	}
	vals, directive = completeConfigValueForKey("bridge.feishu.prompt.shared_thread_template_file", "")
	if vals != nil || directive != cobra.ShellCompDirectiveDefault {
		t.Fatalf("template file completion = %v %v, want nil Default", vals, directive)
	}
	vals, directive = completeConfigValueForKey("bridge.feishu.profiles.coder.workspace_defaults.cwd", "")
	if vals != nil || directive != cobra.ShellCompDirectiveDefault {
		t.Fatalf("profile cwd completion = %v %v, want nil Default", vals, directive)
	}
	vals, directive = completeConfigValueForKey("bridge.feishu.profiles.coder.interaction.mode", "")
	if directive != cobra.ShellCompDirectiveNoFileComp || !reflect.DeepEqual(vals, []string{"thread", "singleton"}) {
		t.Fatalf("interaction mode completion = %v %v, want [thread singleton] NoFileComp", vals, directive)
	}
	vals, directive = completeConfigValueForKey("bridge.feishu.profiles.coder.presentation.thinking_process", "c")
	if directive != cobra.ShellCompDirectiveNoFileComp || !reflect.DeepEqual(vals, []string{"collapsed"}) {
		t.Fatalf("thinking process completion = %v %v, want [collapsed] NoFileComp", vals, directive)
	}
}

func TestQueueIDCompletions(t *testing.T) {
	items := []*pb.QueueItem{
		{Id: "b2", Source: "lark", Text: "ignored"},
		{Id: "a1", Text: "this is a long queued prompt"},
	}
	got := completionValues(queueIDCompletions(items, ""))
	want := []string{"a1", "b2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queueIDCompletions(empty) = %v, want %v", got, want)
	}
	got = completionValues(queueIDCompletions(items, "b"))
	want = []string{"b2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queueIDCompletions(prefix) = %v, want %v", got, want)
	}
}

func TestScheduleCreateTaskPermissionPolicyFlagDefaultsToInherited(t *testing.T) {
	cmd := newScheduleCreateTaskCmd()
	flag := cmd.Flags().Lookup("permission-policy")
	if flag == nil {
		t.Fatal("permission-policy flag missing")
	}
	if flag.DefValue != "" {
		t.Fatalf("permission-policy default = %q, want empty for inherited task/profile defaults", flag.DefValue)
	}
	if got := flag.Usage; !strings.Contains(got, "default, policy-allow, or deny-all") {
		t.Fatalf("permission-policy usage = %q, want default/policy-allow/deny-all help", got)
	}
}

func TestScheduleListTargetPreservesSessionPromptDisplay(t *testing.T) {
	for _, tt := range []struct {
		name string
		item *pb.ScheduleItem
		want string
	}{
		{
			name: "target name uses target slash session",
			item: &pb.ScheduleItem{TargetId: "rt-1", TargetName: "devbox", SessionName: "work"},
			want: "devbox/work",
		},
		{
			name: "target id does not fallback for session prompt",
			item: &pb.ScheduleItem{TargetId: "rt-1", SessionName: "work"},
			want: "work",
		},
		{
			name: "missing session displays dash",
			item: &pb.ScheduleItem{TargetId: "rt-1"},
			want: "—",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := scheduleListTarget(tt.item); got != tt.want {
				t.Fatalf("scheduleListTarget() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScheduleListTargetUsesTaskAgentForSubtasks(t *testing.T) {
	specJSON, err := json.Marshal(task.Spec{Agent: "builtin/codex", Task: "review"})
	if err != nil {
		t.Fatal(err)
	}
	item := &pb.ScheduleItem{ScheduleType: schedule.ScheduleTypeSubtask, TargetId: "rt-1", SessionName: "work", TaskSpecJson: specJSON}
	if got, want := scheduleListTarget(item), "task:builtin/codex"; got != want {
		t.Fatalf("scheduleListTarget(subtask) = %q, want %q", got, want)
	}
}

func TestScheduledTaskAgent(t *testing.T) {
	valid, err := json.Marshal(task.Spec{Agent: "builtin/codex", Task: "review"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		json []byte
		want string
	}{
		{name: "valid", json: valid, want: "builtin/codex"},
		{name: "empty", json: nil, want: "—"},
		{name: "invalid", json: []byte(`{`), want: "—"},
		{name: "missing agent", json: []byte(`{"task":"review"}`), want: "—"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := scheduledTaskAgent(tt.json); got != tt.want {
				t.Fatalf("scheduledTaskAgent() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScheduleTaskCompletions(t *testing.T) {
	if got, want := completionValues(schedulePermissionPolicyCompletions("")), []string{"default", "policy-allow", "deny-all"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("permission completions = %v, want %v", got, want)
	}
	if got, want := completionValues(scheduleIsolationCompletions("p")), []string{"process", "private-home"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("isolation completions = %v, want %v", got, want)
	}
	if got, want := completionValues(scheduleWorkspacePolicyCompletions("git")), []string{"git-worktree"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("workspace completions = %v, want %v", got, want)
	}
}

func TestScheduleCompletionWiring(t *testing.T) {
	create := newScheduleCreateCmd()
	if create.ValidArgsFunction == nil {
		t.Fatal("create should have ValidArgsFunction")
	}
	got, directive := create.ValidArgsFunction(create, []string{"local/c1"}, "cr")
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive = %v, want NoFileComp", directive)
	}
	if values := completionValues(got); !reflect.DeepEqual(values, []string{"cron"}) {
		t.Fatalf("create spec completion = %v, want [cron]", values)
	}

	createTask := newScheduleCreateTaskCmd()
	if createTask.ValidArgsFunction == nil {
		t.Fatal("create-task should have ValidArgsFunction")
	}
	for _, flag := range []string{"name", "agent", "task", "isolation", "credential-profile", "permission-policy", "workspace-policy", "local-dir", "repo-kind", "repo-remote", "repo-cache-key", "repo-commit", "repo-subdir", "timeout", "trusted-mode-ack"} {
		if _, ok := createTask.GetFlagCompletionFunc(flag); !ok {
			t.Fatalf("create-task --%s should have flag completion", flag)
		}
	}

	if newScheduleRunsCmd().ValidArgsFunction == nil {
		t.Fatal("runs should have ValidArgsFunction")
	}
	if newScheduleRemoveCommand("remove <id>").ValidArgsFunction == nil {
		t.Fatal("remove should have ValidArgsFunction")
	}
	if newScheduleToggleCmd(true).ValidArgsFunction == nil {
		t.Fatal("enable should have ValidArgsFunction")
	}
	if newScheduleToggleCmd(false).ValidArgsFunction == nil {
		t.Fatal("disable should have ValidArgsFunction")
	}
	if newScheduleListCmd().ValidArgsFunction == nil {
		t.Fatal("list should have ValidArgsFunction")
	}
	if _, ok := create.GetFlagCompletionFunc("run-mode"); !ok {
		t.Fatal("create --run-mode should have flag completion")
	}
}

func TestScheduleCanonicalAndCompatCommandSurface(t *testing.T) {
	root := newRootCmd()
	for _, tt := range []struct {
		name    string
		aliases []string
	}{
		{name: "remove"},
	} {
		found, _, err := root.Find([]string{"schedule", tt.name})
		if err != nil || found.Name() != tt.name {
			t.Fatalf("find schedule %s: found=%v err=%v", tt.name, found, err)
		}
		if !reflect.DeepEqual(found.Aliases, tt.aliases) {
			t.Fatalf("schedule %s aliases = %v, want %v", tt.name, found.Aliases, tt.aliases)
		}
		if err := found.Args(found, []string{"12"}); err != nil {
			t.Fatalf("schedule %s rejected one id: %v", tt.name, err)
		}
		for _, args := range [][]string{nil, {"12", "13"}} {
			if err := found.Args(found, args); err == nil {
				t.Fatalf("schedule %s accepted invalid args %v", tt.name, args)
			}
		}
		if found.ValidArgsFunction == nil {
			t.Fatalf("schedule %s should retain id completion", tt.name)
		}
	}
	if found, _, err := root.Find([]string{"schedule", "create-task"}); err != nil || found.Name() != "create-task" {
		t.Fatalf("schedule create-task changed: found=%v err=%v", found, err)
	}

	var help bytes.Buffer
	root.SetOut(&help)
	root.SetErr(&help)
	root.SetArgs([]string{"schedule", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("schedule --help: %v", err)
	}
	for _, want := range []string{"  remove", "  create-task"} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("schedule help missing %q:\n%s", want, help.String())
		}
	}
	if strings.Contains(help.String(), "  rm") {
		t.Fatalf("schedule help exposed hidden rm alias:\n%s", help.String())
	}

	root = newRootCmd()
	var completion bytes.Buffer
	root.SetOut(&completion)
	root.SetErr(&completion)
	root.SetArgs([]string{"__complete", "schedule", ""})
	if err := root.Execute(); err != nil {
		t.Fatalf("complete schedule: %v", err)
	}
	for _, want := range []string{"remove\t", "create-task\t"} {
		if !strings.Contains(completion.String(), want) {
			t.Fatalf("schedule completion missing %q:\n%s", want, completion.String())
		}
	}
	if strings.Contains(completion.String(), "rm\t") {
		t.Fatalf("schedule completion exposed hidden rm alias:\n%s", completion.String())
	}
	for _, path := range [][]string{{"schedule", "rm"}, {"schedule", "delete"}} {
		found, _, err := root.Find(path)
		if err == nil && found != nil && found.Name() == path[len(path)-1] {
			t.Fatalf("%v should not be registered: found=%v", path, found)
		}
	}
}

// TestScheduleCreateTaskDefaultIsolationProcess: `amux schedule create-task`
// must not pre-fill --isolation. Leaving it empty lets the control plane / runner
// resolve the effective default to process (aligned with `amux task submit` and
// MCP subtask, see cli-product-model v0.13/v0.35). A non-empty default would
// silently force private-home HOME isolation onto every scheduled subtask.
func TestScheduleCreateTaskDefaultIsolationProcess(t *testing.T) {
	c := newScheduleCreateTaskCmd()
	flag := c.Flags().Lookup("isolation")
	if flag == nil {
		t.Fatal("create-task should have --isolation flag")
	}
	if flag.DefValue != "" {
		t.Fatalf("--isolation default = %q; want empty so effective default resolves to process", flag.DefValue)
	}
	if got := task.DefaultIsolation(task.IsolationMode(flag.DefValue)); got != task.IsolationProcess {
		t.Fatalf("effective default isolation = %q, want process", got)
	}
}

func TestGeneralCompletionWiring(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{
		{"session", "run"},
		{"session", "list"},
		{"session", "attach"},
		{"session", "send"},
		{"session", "interrupt"},
		{"session", "stop"},
		{"session", "queue"},
		{"session", "queue", "clear"},
		{"session", "queue", "cancel"},
		{"system", "config", "path"},
		{"system", "config", "show"},
		{"system", "config", "get"},
		{"system", "config", "set"},
		{"system", "config", "unset"},
		{"system", "start"},
		{"system", "restart"},
		{"system", "status"},
		{"system", "logs"},
		{"system", "stop"},
		{"system", "doctor"},
		{"bridge", "feishu", "setup"},
		{"bridge", "feishu", "status"},
		{"bridge", "feishu", "test"},
		{"bridge", "feishu", "disable"},
		{"bridge", "feishu", "bindings"},
	} {
		cmd, _, err := root.Find(path)
		if err != nil {
			t.Fatalf("find %v: %v", path, err)
		}
		if cmd.ValidArgsFunction == nil {
			t.Fatalf("%v should have ValidArgsFunction", path)
		}
	}

	run, _, _ := root.Find([]string{"session", "run"})
	for _, flag := range []string{"name", "env", "on", "cwd"} {
		if _, ok := run.GetFlagCompletionFunc(flag); !ok {
			t.Fatalf("run --%s should have flag completion", flag)
		}
	}
	update, _, _ := root.Find([]string{"update"})
	for _, flag := range []string{"source", "bin-dir", "shell-profile"} {
		if _, ok := update.GetFlagCompletionFunc(flag); !ok {
			t.Fatalf("update --%s should have flag completion", flag)
		}
	}
}
