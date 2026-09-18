package memoryprotocol

import (
	"reflect"
	"strings"
	"testing"
)

func TestPromptPreservesAllExistingAppendInstructions(t *testing.T) {
	got := AppendPrompt([]string{"--model", "test", "--append-system-prompt", "profile instructions", "--append-system-prompt=user instructions"}, Instruction(false))
	if len(got) != 4 || got[0] != "--model" || got[2] != "--append-system-prompt" {
		t.Fatalf("unexpected arguments: %v", got)
	}
	if !strings.HasPrefix(got[3], "profile instructions\n\nuser instructions\n\n") || strings.Count(got[3], Instruction(false)) != 1 {
		t.Fatalf("lost existing instructions: %s", got[3])
	}
	if next := AppendPrompt(got, Instruction(false)); !reflect.DeepEqual(got, next) {
		t.Fatalf("relaunch duplicated memory protocol: %v", next)
	}
}

func TestBindingOverrideMasksInheritance(t *testing.T) {
	got := SetEnv([]string{BindingEnv + "=principal:other", "PATH=/bin", BindingEnv + "=global"}, BindingEnv, "")
	if !reflect.DeepEqual(got, []string{"PATH=/bin", BindingEnv + "="}) || Binding(got) != "" {
		t.Fatalf("inherited binding survived: %v", got)
	}
}
