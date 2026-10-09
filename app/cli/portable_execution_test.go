package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/amuxos/amux/internal/task"
	"github.com/spf13/cobra"
)

func TestPortableExecutionSchemasExposeNativeArguments(t *testing.T) {
	for _, name := range []string{"amux.task_submit", "amux.task_run", "amux.instance_spawn", "amux.schedule_create", "amux.schedule_update"} {
		t.Run(name, func(t *testing.T) {
			var schema map[string]any
			if err := json.Unmarshal(findMCPTool(t, amuxTools(), name).InputSchema, &schema); err != nil {
				t.Fatal(err)
			}
			props := schema["properties"].(map[string]any)
			if strings.Contains(name, "schedule_") {
				props = props["task"].(map[string]any)["properties"].(map[string]any)
			}
			value, ok := props["extra_args"].(map[string]any)
			if !ok || value["type"] != "array" || value["items"].(map[string]any)["type"] != "string" {
				t.Fatalf("native arguments unavailable: %v", props)
			}
			description, _ := value["description"].(string)
			if !strings.Contains(description, "permission") || !strings.Contains(description, "权限") {
				t.Fatalf("missing bilingual permission guidance: %s", description)
			}
		})
	}
}

func TestMCPPortableExecutionPreservesNativeArguments(t *testing.T) {
	for _, spawn := range []bool{false, true} {
		t.Run(map[bool]string{false: "task", true: "instance"}[spawn], func(t *testing.T) {
			capture := withTaskClient(t)
			request := task.Spec{Agent: "team/reviewer", Runtime: "claude", Model: "review-model", Task: "review", ExtraArgs: []string{"--permission-mode", "plan", "--add-dir", "/tmp/review space"}}
			data, _ := json.Marshal(request)
			h := &mcpHandler{}
			if spawn {
				result, err := h.spawnAgent(context.Background(), data)
				if err != nil || result.IsError {
					t.Fatalf("spawn: %v, %+v", err, result)
				}
			} else {
				result, err := h.submit(context.Background(), data, false)
				if err != nil || result.IsError {
					t.Fatalf("submit: %v, %+v", err, result)
				}
			}
			var got task.Spec
			if err := json.Unmarshal(capture.specJSON, &got); err != nil {
				t.Fatal(err)
			}
			if got.Runtime != request.Runtime || got.Model != request.Model || !reflect.DeepEqual(got.ExtraArgs, request.ExtraArgs) {
				t.Fatalf("execution changed: %+v", got)
			}
		})
	}
}

func TestScheduledTaskArgumentsSurviveCLIAndWireConversion(t *testing.T) {
	var flags scheduleTaskFlags
	cmd := &cobra.Command{}
	registerScheduleTaskFlags(cmd, &flags)
	if err := cmd.ParseFlags([]string{"--agent", "team/reviewer", "--runtime", "claude", "--task", "review", "--arg=--permission-mode", "--arg=plan", "--arg=--add-dir", "--arg=/tmp/review space"}); err != nil {
		t.Fatal(err)
	}
	spec := flags.clientopSpec()
	if err := spec.Validate(); err != nil {
		t.Fatal(err)
	}
	got := spec.ToTaskSpec()
	want := []string{"--permission-mode", "plan", "--add-dir", "/tmp/review space"}
	if !reflect.DeepEqual(got.ExtraArgs, want) {
		t.Fatalf("args = %#v", got.ExtraArgs)
	}
	got.Model = "review-model"
	view := taskSpecMCPView(got)
	if view["runtime"] != "claude" || view["model"] != "review-model" || !reflect.DeepEqual(view["extra_args"], want) {
		t.Fatalf("schedule readback lost execution settings: %#v", view)
	}
	spec.ExtraArgs[0] = "changed"
	if !reflect.DeepEqual(got.ExtraArgs, want) {
		t.Fatal("wire arguments alias caller input")
	}
}

func TestAgentRenderPortableProfileRequiresRuntimeAndRetainsAssets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AMUX_HOME", "")
	repo := initTestProfilePackRepo(t)
	runAgentPackCmd(t, "add", "team", repo)
	output := filepath.Join(t.TempDir(), "rendered")
	cmd := newAgentCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"render", "team/coder", "--out", output})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--runtime") {
		t.Fatalf("missing runtime: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("failed render wrote output: %v", err)
	}
	for _, language := range []string{"en", "zh-CN", "unknown"} {
		t.Setenv("AMUX_LANGUAGE", language)
		invalid := newAgentCmd()
		invalid.SetOut(&bytes.Buffer{})
		invalid.SetErr(&bytes.Buffer{})
		invalid.SetArgs([]string{"render", "team/coder", "--runtime", "nonexistent", "--out", output})
		err := invalid.Execute()
		if err == nil || !strings.Contains(err.Error(), "unknown runtime") || !strings.Contains(err.Error(), "未知 Runtime") {
			t.Fatalf("invalid runtime error in %s: %v", language, err)
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("invalid runtime wrote output: %v", err)
		}
	}
	cmd = newAgentCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"render", "team/coder", "--runtime", "claude", "--model", "sonnet", "--out", output})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", ".claude/skills/amux-dev/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	content, _ := os.ReadFile(filepath.Join(output, "AGENTS.md"))
	if !strings.Contains(string(content), "You are the team coder.") || !strings.Contains(string(content), "sonnet") {
		t.Fatalf("incorrect projected instructions: %s", content)
	}
}
