package command

import "testing"

func TestBareGoalCommand(t *testing.T) {
	for _, text := range []string{"/goal fix it", "my goal is clear", "goalkeeper", "goals fix it", ""} {
		if _, ok, _ := ParseGoal(text); ok {
			t.Errorf("recognized prose/slash %q", text)
		}
	}
	for _, test := range []struct{ text, action, objective string }{
		{"goal", "get", ""}, {"GOAL pause", "pause", ""}, {"goal resume", "resume", ""}, {"goal clear", "clear", ""},
		{"goal fix\n  the tests", "create", "fix\n  the tests"}, {"goal set new objective", "set", "new objective"},
	} {
		got, recognized, err := ParseGoal(test.text)
		if !recognized || err != nil || got.Action != test.action || got.Objective != test.objective {
			t.Errorf("ParseGoal(%q)=%+v,%v,%v", test.text, got, recognized, err)
		}
		for _, scope := range []Scope{ScopePrivate, ScopeGroup, ScopeFreshTopic, ScopeActiveTopic} {
			if Classify(scope, test.text) != CommandGoal {
				t.Errorf("scope %s missed %q", scope, test.text)
			}
		}
	}
	for _, text := range []string{"goal set", "goal budget", "goal budget 0", "goal budget -1", "goal budget 9223372036854775808", "goal budget 1 2", "goal pause extra"} {
		if _, recognized, err := ParseGoal(text); !recognized || err == nil {
			t.Errorf("invalid command fell through: %q", text)
		}
	}
	got, _, err := ParseGoal("goal budget 40000")
	if err != nil || got.TokenBudget == nil || *got.TokenBudget != 40000 {
		t.Fatalf("budget=%+v,%v", got, err)
	}
	for _, entry := range ManagedNativeSlashCommands() {
		if entry.Command == "goal" {
			t.Fatal("bare goal advertised as slash command")
		}
	}
}
