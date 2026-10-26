package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestScheduleTaskFlagsCarryPortableExecutionSelection(t *testing.T) {
	for _, language := range []string{"en", "zh-CN", "zz"} {
		t.Run(language, func(t *testing.T) {
			t.Setenv("AMUX_LANGUAGE", language)
			var flags scheduleTaskFlags
			cmd := &cobra.Command{}
			registerScheduleTaskFlags(cmd, &flags)
			if err := cmd.Flags().Parse([]string{"--agent", "team/reviewer", "--task", "review",
				"--runtime", "claude", "--model", "review-model", "--reasoning-effort", "high"}); err != nil {
				t.Fatal(err)
			}
			spec := flags.clientopSpec().ToTaskSpec()
			if spec.Agent != "team/reviewer" || spec.Runtime != "claude" || spec.Model != "review-model" || spec.ReasoningEffort != "high" {
				t.Fatalf("scheduled execution selection was lost: %#v", spec)
			}
			for flag, markers := range map[string][2]string{
				"runtime":          {"omitted uses", "省略时使用"},
				"model":            {"pass the current session model by default", "默认应传入当前会话模型"},
				"reasoning-effort": {"pass the current session effort by default", "默认应传入当前会话思考强度"},
			} {
				usage := cmd.Flags().Lookup(flag).Usage
				want := markers[0]
				if language == "zh-CN" {
					want = markers[1]
				}
				if !strings.Contains(usage, want) {
					t.Fatalf("untranslated --%s help: %s", flag, usage)
				}
			}
		})
	}
}

func TestScheduleUpdateSharesTaskFlagsWithCreateTask(t *testing.T) {
	update := clientopCoverageFindSubcommandOrNil(newScheduleCmd(), "update")
	create := clientopCoverageFindSubcommandOrNil(newScheduleCmd(), "create-task")
	if update == nil || create == nil {
		t.Fatal("missing update or create-task command")
	}
	for _, flag := range []string{"name", "spec"} {
		if update.Flags().Lookup(flag) == nil {
			t.Errorf("schedule update has no --%s flag", flag)
		}
	}
	// Every task-spec flag on create-task must also be on update, so a field a
	// scheduled task can carry is settable through both. (update adds --spec for
	// the time rule and --name for the label, which create-task spells
	// differently; those two are not part of this set.)
	for _, flag := range []string{"agent", "runtime", "model", "reasoning-effort", "task", "isolation", "credential-profile", "permission-policy", "workspace-policy", "local-dir", "repo-kind", "repo-remote", "repo-cache-key", "repo-commit", "repo-subdir", "timeout", "trusted-mode-ack", "run-as"} {
		if create.Flags().Lookup(flag) == nil {
			t.Fatalf("create-task lost --%s", flag)
		}
		if update.Flags().Lookup(flag) == nil {
			t.Errorf("update is missing --%s that create-task has", flag)
		}
	}
}
