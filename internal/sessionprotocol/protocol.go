// Package sessionprotocol supplies Profile-independent session/schedule guidance.
package sessionprotocol

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

const InstructionEnv = "AMUX_SESSION_SCHEDULE_CONTEXT"
const SkillName = "amux-session-schedule"

//go:embed amux-session-schedule
var Files embed.FS

func WriteFiles(write func(string, []byte) error) error {
	return fs.WalkDir(Files, SkillName, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := Files.ReadFile(path)
		if err != nil {
			return err
		}
		return write(path, data)
	})
}

func Instruction(path, transport string, child bool) string {
	if child {
		role := "Task-owned child: session/schedule control is restricted. Do not create or control sessions or schedules; return requests to your parent."
		return fmt.Sprintf("<amux-runtime>\nRuntime Skill: %s at %q. Transport: %s. %s This context grants no additional permissions.\n</amux-runtime>", SkillName, path, transport, role)
	}
	role := "Ordinary Session. Use the configured transport for authorized session and schedule operations."
	return fmt.Sprintf("<amux-runtime>\nRuntime Skill: %s at %q (read from $%s if this session resumed or switched models). Transport: %s. %s For schedules: default to isolated subtask (omitting task.agent/model inherits the current session; in Feishu, omitting delivery_target defaults to origin); use session-prompt (reuse-session) only when continuing this conversation; reply immediately after schedule_create without waiting for the first fire, and do not treat an older completed schedule from another thread as the new request. Read the Runtime Skill only when you need detailed session or CLI usage. This context grants no additional permissions.\n</amux-runtime>", SkillName, path, InstructionEnv, transport, role)
}

func FromEnv(env []string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], InstructionEnv+"=") {
			return strings.TrimPrefix(env[i], InstructionEnv+"=")
		}
	}
	return ""
}

// MergeInstruction replaces this Runtime's previous entrypoint while preserving
// the host's effective user/Profile instructions and other Runtime protocols.
func MergeInstruction(existing, instruction string) string {
	const start, end = "<amux-runtime>", "</amux-runtime>"
	for {
		begin := strings.Index(existing, start)
		if begin < 0 {
			break
		}
		finish := strings.Index(existing[begin+len(start):], end)
		if finish < 0 {
			break
		}
		existing = existing[:begin] + existing[begin+len(start)+finish+len(end):]
	}
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return instruction
	}
	return existing + "\n\n" + instruction
}

// AppendPrompt combines repeated append flags before adding the launch-local
// entrypoint, so last-flag-wins hosts retain the user's and Profile's prompts.
func AppendPrompt(args []string, flag, instruction string) []string {
	var out, parts []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == flag && i+1 < len(args) {
			i++
			parts = append(parts, args[i])
		} else if strings.HasPrefix(arg, flag+"=") {
			parts = append(parts, strings.TrimPrefix(arg, flag+"="))
		} else {
			out = append(out, arg)
		}
	}
	return append(out, flag, MergeInstruction(strings.Join(parts, "\n\n"), instruction))
}
