package codex

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/isolation"
	"github.com/google/uuid"
)

const defaultModeUserInputFeature = "default_mode_request_user_input"

// Capability discovery avoids forcing experimental settings on older hosts.
// The override belongs to this thread, not the user's global configuration.
func (p *Proc) discoverUserInput(ctx context.Context, opts StartOptions, bin string, backend isolation.LaunchBackend) {
	p.defaultModeUserInput = probeUserInput(ctx, opts, bin, backend)
}

// Return the result rather than mutating Proc: Codex can run this during its
// initialize handshake and publish the result only after joining the probe.
func probeUserInput(ctx context.Context, opts StartOptions, bin string, backend isolation.LaunchBackend) bool {
	for _, arg := range opts.ExtraArgs {
		if strings.Contains(arg, defaultModeUserInputFeature) {
			return false
		}
	}
	// The app-server feature list hides under-development features. The native
	// read-only CLI list includes them. Run it under the same identity, HOME,
	// backend and working directory as the agent, with a bounded lifetime.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Helper-backed launches are idempotent by ID. Reusing the agent's ID
	// would attach to its streams and kill it when this probe times out.
	probeContext := opts.LaunchContext
	probeContext.LaunchID = "launch-" + uuid.NewString()
	handle, err := backend.Start(ctx, isolation.LaunchSpec{
		Path: bin, Args: append(appServerArgs(opts.ExtraArgs, "features"), "list"),
		Env: append([]string(nil), opts.Env...), Dir: opts.Cwd,
		Context: probeContext, SetProcessGroup: true,
	})
	if err != nil {
		return false
	}
	defer handle.Close()
	exited := false
	defer func() {
		if exited {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		_ = handle.Kill(cleanup)
		_, _ = handle.Wait(cleanup)
	}()
	_ = handle.Stdin().Close()
	go func() { _, _ = io.Copy(io.Discard, handle.Stderr()) }()
	output := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(io.LimitReader(handle.Stdout(), 128*1024)); output <- data }()
	var data []byte
	select {
	case data = <-output:
	case <-ctx.Done():
		return false
	}
	status, err := handle.Wait(ctx)
	if err != nil {
		return false
	}
	exited = true
	if status.Code != 0 {
		return false
	}
	return supportsDefaultModeUserInput(string(data))
}

func supportsDefaultModeUserInput(output string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != defaultModeUserInputFeature {
			continue
		}
		stage := strings.Join(fields[1:len(fields)-1], " ")
		return stage == "under development" || stage == "experimental" || stage == "beta" || stage == "stable"
	}
	return false
}

func (p *Proc) userInputThreadParams(params map[string]any) {
	if !p.defaultModeUserInput {
		return
	}
	config, _ := params["config"].(map[string]any)
	if config == nil {
		config = make(map[string]any)
	}
	config["features."+defaultModeUserInputFeature] = true
	params["config"] = config
}

// Cancelling any card cancels its native batch. Empty answers express that no
// choice was made; a reserved option label would collide with model content.
func (p *Proc) CancelUserInput(permissionID string) error {
	reqID, qid, ok := parseUserInputPermissionID(permissionID)
	if !ok {
		return fmt.Errorf("%s: unknown user-input request", p.provider())
	}
	p.threadMu.Lock()
	pending := p.userInputs[reqID]
	if pending == nil || pending.permToQ[permissionID] != qid {
		p.threadMu.Unlock()
		return fmt.Errorf("%s: user-input request is no longer pending", p.provider())
	}
	if _, answered := pending.answered[qid]; answered {
		p.threadMu.Unlock()
		return fmt.Errorf("%s: user-input question was already answered", p.provider())
	}
	// Keep the turn lock through the response and resolution emissions. A new
	// turn cannot inherit these resolution events.
	defer p.threadMu.Unlock()
	if err := p.client.Respond(reqID, map[string]any{"answers": map[string]any{}}); err != nil {
		return err
	}
	delete(p.userInputs, reqID)
	p.reconcileWatchdogTimersLocked()
	for _, question := range pending.questions {
		p.emitTurn(agentstream.PermissionResolved{RequestID: userInputPermissionID(reqID, question.id)}, agentstream.ActorUser, "user", "user_input_cancelled", permissionID)
	}
	return nil
}
