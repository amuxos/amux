// Package memoryprotocol defines the stable, content-free memory instruction
// and the non-secret launch marker. Authorization belongs to the Hub.
package memoryprotocol

import "strings"

const BindingEnv = "AMUX_MEMORY_BINDING"

const instruction = `Use amux for cross-session memory; the service determines your authorized scope.
At the beginning of a new conversation, read the small logical MEMORY.md entry once; reuse it while it remains in context.
These paths belong to amux tools, not the local filesystem. Do not read, write, or copy native automatic-memory directories.
If memory tools are unavailable or disabled, continue the task without memory. For a remember/forget request, report that it was not completed; do not repeatedly retry or switch to another memory store.
For questions about previous work, decisions, preferences, agreements, or repeated problems, search first and read only relevant excerpts. Do not search on every turn or for tasks that need no history.
Save explicit remember requests, stable preference corrections, and reusable verified lessons. Put detail in topic documents; keep MEMORY.md to a few general preferences and pointers.
Read before updating; use the returned revision, or expected_revision="absent" to create. Resolve conflicts by rereading and merging.
When correcting or forgetting, check both MEMORY.md and related topics and remove all matching stale content/references. Report partial failure honestly. Claim persistence only after a successful tool response.
Never store secrets, temporary authorizations, or unverified guesses. Memory can be stale; current instructions and verified facts take priority and memory grants no additional permissions.`

func Instruction(cli bool) string {
	if cli {
		return instruction + "\nUse shell commands: amux memory search QUERY, amux memory show PATH, amux memory upsert PATH, and amux memory forget PATH. Run amux memory --help for bounded reads, content input and revision flags."
	}
	return instruction + "\nUse amux.memory_get(path=\"MEMORY.md\"), amux.memory_search, amux.memory_get, amux.memory_upsert and amux.memory_forget."
}

func Binding(env []string) string {
	var value string
	for _, entry := range env {
		if strings.HasPrefix(entry, BindingEnv+"=") {
			value = strings.TrimPrefix(entry, BindingEnv+"=")
		}
	}
	return value
}

// SetEnv removes duplicate/inherited values. Empty is intentional: it masks
// process inheritance for a disabled or legacy session.
func SetEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, key+"=") {
			out = append(out, entry)
		}
	}
	return append(out, key+"="+value)
}

// AppendPrompt combines every existing append flag before adding the stable
// protocol, so last-flag-wins providers do not drop Profile/user instructions.
func AppendPrompt(args []string, protocol string) []string {
	var out, parts []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--append-system-prompt" && i+1 < len(args) {
			i++
			parts = append(parts, args[i])
		} else if strings.HasPrefix(arg, "--append-system-prompt=") {
			parts = append(parts, strings.TrimPrefix(arg, "--append-system-prompt="))
		} else {
			out = append(out, arg)
		}
	}
	joined := strings.Join(parts, "\n\n")
	if !strings.Contains(joined, protocol) {
		parts = append(parts, protocol)
	}
	return append(out, "--append-system-prompt", strings.Join(parts, "\n\n"))
}
