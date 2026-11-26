package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/adapter"
	"github.com/amuxos/amux/internal/memoryprotocol"
	"github.com/amuxos/amux/internal/session"
)

// SessionInstructionsEnv carries session-level out-of-band instructions to a
// Codex-compatible structured broker, which merges them into the thread's
// developer instructions. Claude and pi take them via --append-system-prompt.
const SessionInstructionsEnv = "AMUX_SESSION_INSTRUCTIONS"

// Session instructions (e.g. the Feishu foreground protocol) are owned by the
// submitting client. They are persisted per session so every provider launch
// (fresh, resume, restart, crash recovery) carries them out of band. A running
// launch cannot change its system prompt, so when the persisted value differs
// from what the current launch carries the runtime appends it to the input as
// a hidden fallback until the next launch.
func sessionInstructionsPath(amuxDir, sessionID string) string {
	return filepath.Join(amuxDir, "sessions", sessionID+".instructions")
}

func readSessionInstructions(amuxDir, sessionID string) string {
	if amuxDir == "" || sessionID == "" {
		return ""
	}
	data, err := os.ReadFile(sessionInstructionsPath(amuxDir, sessionID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeSessionInstructions(amuxDir, sessionID, text string) {
	if amuxDir == "" || sessionID == "" {
		return
	}
	text = strings.TrimSpace(text)
	path := sessionInstructionsPath(amuxDir, sessionID)
	if text == "" {
		_ = os.Remove(path)
		return
	}
	if readSessionInstructions(amuxDir, sessionID) == text {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, []byte(text), 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func removeSessionInstructions(amuxDir, sessionID string) {
	if amuxDir != "" && sessionID != "" {
		_ = os.Remove(sessionInstructionsPath(amuxDir, sessionID))
	}
}

func instructionsHash(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

func supportsSessionInstructions(agent string) bool {
	return adapter.ExecutionCapabilitiesFor(agent).SessionInstructions != adapter.SessionInstructionsNone
}

// appliedInstructions records, per session, the instructions hash the
// current provider process was launched with.
type appliedInstructions struct {
	mu sync.Mutex
	m  map[string]appliedLaunch
}

type appliedLaunch struct {
	generation uint64
	hash       string
}

func (a *appliedInstructions) set(sessionID string, generation uint64, hash string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.m == nil {
		a.m = map[string]appliedLaunch{}
	}
	a.m[sessionID] = appliedLaunch{generation: generation, hash: hash}
}

func (a *appliedInstructions) get(sessionID string, generation uint64) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	l, ok := a.m[sessionID]
	if !ok || l.generation != generation {
		return "", false
	}
	return l.hash, true
}

// configureSessionInstructions adds the persisted session instructions to a
// provider launch. It returns the hash actually applied ("" when none).
func (s *server) configureSessionInstructions(snap session.Snapshot, args, env []string) ([]string, []string, string) {
	text := readSessionInstructions(s.amuxDir, snap.ID)
	env = memoryprotocol.SetEnv(env, SessionInstructionsEnv, "")
	mode := adapter.ExecutionCapabilitiesFor(snap.AgentType).SessionInstructions
	if text == "" || mode == adapter.SessionInstructionsNone {
		return args, env, ""
	}
	switch mode {
	case adapter.SessionInstructionsSystemPrompt:
		args = memoryprotocol.AppendPrompt(args, text)
	case adapter.SessionInstructionsEnv:
		env = memoryprotocol.SetEnv(env, SessionInstructionsEnv, text)
	}
	return args, env, instructionsHash(text)
}

// sessionInstructionsDelivery decides how managed instructions reach the
// model for this submit: "system" when the running launch carries them,
// otherwise "inline" with the text to append to the input.
func (s *server) sessionInstructionsDelivery(sess *session.Session, text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	hash, ok := s.launchInstructions.get(sess.Snapshot().ID, sess.ProcessGeneration())
	if ok && hash == instructionsHash(text) {
		return "system", ""
	}
	return "inline", text
}

// appendInstructionsInline appends hidden fallback instructions to the first
// text part of the input (or as a trailing text part).
func appendInstructionsInline(input session.UserInput, text string) session.UserInput {
	if strings.TrimSpace(text) == "" {
		return input
	}
	suffix := "\n\n" + strings.TrimSpace(text)
	out := session.UserInput{Parts: append([]session.InputPart(nil), input.Parts...)}
	for i, part := range out.Parts {
		if part.Image == nil {
			out.Parts[i].Text += suffix
			return out
		}
	}
	out.Parts = append(out.Parts, session.InputPart{Text: suffix})
	return out
}
