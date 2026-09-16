package cli

import "testing"

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
	for _, flag := range []string{"agent", "task", "isolation", "credential-profile", "permission-policy", "workspace-policy", "local-dir", "repo-kind", "repo-remote", "repo-cache-key", "repo-commit", "repo-subdir", "timeout", "trusted-mode-ack", "run-as"} {
		if create.Flags().Lookup(flag) == nil {
			t.Fatalf("create-task lost --%s", flag)
		}
		if update.Flags().Lookup(flag) == nil {
			t.Errorf("update is missing --%s that create-task has", flag)
		}
	}
}
