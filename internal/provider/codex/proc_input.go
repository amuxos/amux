package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	providerjsonrpc "github.com/amuxos/amux/internal/provider/jsonrpc"
	"github.com/amuxos/amux/internal/session"
)

func (p *Proc) turnInput(text string) []map[string]string {
	input := []map[string]string{{"type": "text", "text": text}}
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "$") {
		return input
	}
	name := strings.TrimPrefix(fields[0], "$")
	p.skillsMu.Lock()
	path := p.skillPaths[name]
	p.skillsMu.Unlock()
	if path == "" {
		return input
	}
	return append(input, map[string]string{"type": "skill", "name": name, "path": path})
}
func (p *Proc) discoverInputCapabilities(parent context.Context, effectiveModel string) {
	if p.discoverDistributionInputCapabilities() {
		return
	}
	if p.provider() != "codex" {
		return
	}
	ctx, cancel := context.WithTimeout(parent, skillListTimeout)
	defer cancel()
	resp, err := p.client.Call(ctx, "model/list", map[string]any{"limit": 100, "includeHidden": false})
	if err != nil {
		return
	}
	var result struct {
		Data []struct {
			ID              string   `json:"id"`
			Model           string   `json:"model"`
			IsDefault       bool     `json:"isDefault"`
			InputModalities []string `json:"inputModalities"`
		} `json:"data"`
	}
	if json.Unmarshal(resp.Result, &result) != nil {
		return
	}
	want := strings.TrimSpace(effectiveModel)
	for _, model := range result.Data {
		matched := want != "" && (strings.EqualFold(want, strings.TrimSpace(model.ID)) || strings.EqualFold(want, strings.TrimSpace(model.Model)))
		if want == "" {
			matched = model.IsDefault
		}
		if !matched {
			continue
		}
		p.inputModalities = append([]string(nil), model.InputModalities...)
		p.inputCapabilitySource = "codex:model/list"
		return
	}
}
func (p *Proc) InputCapabilities() ([]string, string) {
	return append([]string(nil), p.inputModalities...), p.inputCapabilitySource
}

func (p *Proc) DispatchTurn(ctx context.Context, req session.TurnDispatchRequest, sink func(agentstream.ProducerEmission) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.eventSinks.BeginTurnFor(req.TurnToken, sink); err != nil {
		return err
	}
	var err error
	if req.Control != "" {
		err = p.Control(req.Control)
	} else {
		err = p.SubmitInput(req.Input)
	}
	if err != nil {
		p.eventSinks.AbortTurn()
	}
	return err
}

func (p *Proc) SubmitInput(input session.UserInput) error {
	p.inputMu.Lock()
	defer p.inputMu.Unlock()
	parts, stagingDir, err := p.stageTurnInput(input)
	if err != nil {
		return err
	}
	p.setInputStagingDir(stagingDir)
	if err := p.sendTurnInput(parts); err != nil {
		p.cleanupInputStagingDir(stagingDir)
		return err
	}
	return nil
}
func (p *Proc) stageTurnInput(input session.UserInput) ([]map[string]any, string, error) {
	var parts []map[string]any
	var stagingDir string
	cleanup := func() {
		if stagingDir != "" {
			_ = os.RemoveAll(stagingDir)
		}
	}
	for _, part := range input.Parts {
		if part.Image == nil {
			if part.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": part.Text})
			}
			continue
		}
		if stagingDir == "" {
			var err error
			stagingDir, err = os.MkdirTemp(p.inputTempDir, "amux-session-images-")
			if err != nil {
				cleanup()
				return nil, "", fmt.Errorf("stage image input: %w", err)
			}
			if err := os.Chmod(stagingDir, 0o700); err != nil {
				cleanup()
				return nil, "", fmt.Errorf("secure image staging: %w", err)
			}
		}
		ext := ".img"
		switch part.Image.MIMEType {
		case "image/jpeg":
			ext = ".jpg"
		case "image/png":
			ext = ".png"
		case "image/webp":
			ext = ".webp"
		}
		path := filepath.Join(stagingDir, fmt.Sprintf("image-%02d%s", len(parts)+1, ext))
		if err := os.WriteFile(path, part.Image.Data, 0o600); err != nil {
			cleanup()
			return nil, "", fmt.Errorf("stage image input: %w", err)
		}
		parts = append(parts, map[string]any{"type": "localImage", "path": path})
	}
	turnParts := p.turnInput(input.Text())
	for _, part := range turnParts[1:] {
		converted := make(map[string]any, len(part))
		for key, value := range part {
			converted[key] = value
		}
		parts = append(parts, converted)
	}
	if len(parts) == 0 {
		cleanup()
		return nil, "", errors.New("empty user input")
	}
	return parts, stagingDir, nil
}
func (p *Proc) setInputStagingDir(dir string) {
	p.cleanupInputStaging()
	p.stagingMu.Lock()
	p.stagingDir = dir
	p.stagingMu.Unlock()
}
func (p *Proc) cleanupInputStaging() {
	p.stagingMu.Lock()
	dir := p.stagingDir
	p.stagingDir = ""
	p.stagingMu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}
func (p *Proc) cleanupInputStagingDir(dir string) {
	p.stagingMu.Lock()
	if p.stagingDir == dir {
		p.stagingDir = ""
	}
	p.stagingMu.Unlock()
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}
func environmentValue(env []string, name string) string {
	prefix := name + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}
func (p *Proc) sendTurnInput(input []map[string]any) error {
	p.goalControlMu.Lock()
	defer p.goalControlMu.Unlock()
	tid := p.snapshotThreadID()
	if tid == "" {
		return fmt.Errorf("%s: thread not started", p.provider())
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnStartTimeout)
	defer cancel()
	// Stop the native scheduler before reserving a client turn/start attempt.
	// Otherwise an automatic turn can be mistaken for this submission's
	// provisional turn. Session retains the grant and resumes after input.
	p.threadMu.Lock()
	goalActive := p.goalSupported && p.goal != nil && p.goal.Status == "active"
	p.threadMu.Unlock()
	if goalActive {
		if _, err := p.callGoal(ctx, "thread/goal/set", map[string]any{"threadId": tid, "status": "paused"}); err != nil {
			return err
		}
		if err := p.Interrupt(); err != nil {
			return err
		}
	}
	attempt := p.beginTurnStartAttempt("turn/start", false)
	p.usageMu.Lock()
	p.turnStartAt = time.Now()
	p.firstTokenAt = time.Time{}
	p.genActiveAt = time.Time{}
	p.genElapsedNs = 0
	p.usageMu.Unlock()
	params := map[string]any{"threadId": tid, "input": input}
	if p.turnModel != "" {
		params["model"] = p.turnModel
	}
	if p.turnEffort != "" {
		params["effort"] = p.turnEffort
	}
	resp, err := p.client.Call(ctx, "turn/start", params)
	if err != nil {
		return p.finishTurnStartError(attempt, err)
	}
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if uerr := json.Unmarshal(resp.Result, &result); uerr != nil {
		return p.finishTurnStartResponse(attempt, "", fmt.Errorf("decode turn/start response: %w", uerr))
	}
	return p.finishTurnStartResponse(attempt, strings.TrimSpace(result.Turn.ID), nil)
}

// Arm the turn watchdog from the turn/start response, which carries the
// initial turn object, so a dropped turn/started notification — the exact
// defect class this watchdog exists for — cannot leave the turn
// unwatched. turn/started re-arms and reconciles when it does arrive.

// SteerTurn appends user input to the in-flight Codex turn via the app-server
// turn/steer method. The amux turn named by TurnToken must still be the one in
// flight, and expectedTurnId makes the server reject the request when the
// native turn already finished or was replaced, so the runtime can queue
// instead.
func (p *Proc) SteerTurn(ctx context.Context, req session.TurnSteerRequest) error {
	if req.Input.ImageCount() > 0 {
		return fmt.Errorf("%w: image input cannot be steered", session.ErrSteerUnavailable)
	}
	text := strings.TrimSpace(req.Input.Text())
	if text == "" {
		return fmt.Errorf("%w: empty steer input", session.ErrSteerUnavailable)
	}
	tid, turn := p.steerTarget()
	// Reading the native id on both sides of the amux turn check pins it to
	// that turn: a turn that ended or was replaced in between changes it.
	active := p.eventSinks.TurnActiveFor(req.TurnToken)
	if _, again := p.steerTarget(); !active || again != turn {
		return fmt.Errorf("%w: %s turn %q is not in flight", session.ErrSteerUnavailable, p.provider(), req.TurnToken)
	}
	if tid == "" || turn == "" {
		return fmt.Errorf("%w: %s turn id is not known yet", session.ErrSteerUnavailable, p.provider())
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := p.client.Call(callCtx, "turn/steer", map[string]any{
		"threadId":       tid,
		"expectedTurnId": turn,
		"input":          []map[string]any{{"type": "text", "text": text}},
	}); err != nil {
		var rpcErr *providerjsonrpc.Error
		return session.SteerCallError(err, errors.As(err, &rpcErr))
	}
	return nil
}

func (p *Proc) steerTarget() (threadID, turnID string) {
	p.threadMu.Lock()
	defer p.threadMu.Unlock()
	return p.threadID, p.turnID
}
