package sessionprotocol

import (
	"reflect"
	"strings"
	"testing"
)

func TestReplaceRuntimeInstructionPreservesOtherInstructions(t *testing.T) {
	old := Instruction("/old/SKILL.md", "CLI", false)
	current := Instruction("/new/SKILL.md", "MCP", true)
	existing := "USER RULES\n\n" + old + "\n\nMEMORY RULES"
	got := MergeInstruction(existing, current)
	if !strings.Contains(got, "USER RULES") || !strings.Contains(got, "MEMORY RULES") || strings.Contains(got, "/old/") || strings.Count(got, "<amux-runtime>") != 1 {
		t.Fatalf("merged instructions: %s", got)
	}
	if !strings.Contains(got, "Task-owned child") || MergeInstruction(got, current) != got {
		t.Fatal("role lost or repeated configuration duplicated the Runtime instructions")
	}
}

func TestAppendPromptPreservesUserFlagsAndReplacesStaleLaunch(t *testing.T) {
	old := Instruction("/old", "MCP", false)
	current := Instruction("/new", "CLI", false)
	args := []string{"--user-flag", "value", "--append-system-prompt", "PROFILE", "--append-system-prompt=USER\n\n" + old}
	got := AppendPrompt(args, "--append-system-prompt", current)
	want := []string{"--user-flag", "value", "--append-system-prompt", "PROFILE\n\nUSER\n\n" + current}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(AppendPrompt(got, "--append-system-prompt", current), want) {
		t.Fatalf("args=%q", got)
	}
}
