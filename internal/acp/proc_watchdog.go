package acp

import (
	"context"
	"time"
)

type watchdogFailure struct{ code string }

func (e *watchdogFailure) Error() string { return acpError("watchdog." + e.code).Error() }

type promptCallResult struct {
	response rpcMessage
	err      error
}

func (p *Proc) startWatchdog() uint64 {
	p.watchdogMu.Lock()
	defer p.watchdogMu.Unlock()
	p.watchdogGeneration++
	generation := p.watchdogGeneration
	p.watchdog = newTurnWatchdog(generation, p.watchdogPolicy, time.Now())
	return generation
}

func (p *Proc) stopWatchdog(generation uint64) {
	p.watchdogMu.Lock()
	if p.watchdog != nil && p.watchdog.generation == generation {
		p.watchdog = nil
	}
	p.watchdogMu.Unlock()
}

func (p *Proc) wakeWatchdog() {
	if p.watchdogWake == nil {
		return
	}
	select {
	case p.watchdogWake <- struct{}{}:
	default:
	}
}

func (p *Proc) observeWatchdogActivity() {
	p.watchdogMu.Lock()
	if p.watchdog != nil {
		p.watchdog.activity = time.Now()
	}
	p.watchdogMu.Unlock()
	p.wakeWatchdog()
}

func (p *Proc) observeWatchdogTool(update toolUpdate) {
	p.watchdogMu.Lock()
	if p.watchdog != nil {
		p.watchdog.observeTool(update, time.Now())
	}
	p.watchdogMu.Unlock()
	p.wakeWatchdog()
}

func (p *Proc) observeWatchdogPermission(id string, pending bool) {
	p.watchdogMu.Lock()
	if p.watchdog != nil {
		if pending {
			p.watchdog.permissions[id] = true
		} else {
			delete(p.watchdog.permissions, id)
		}
		p.watchdog.activity = time.Now()
	}
	p.watchdogMu.Unlock()
	p.wakeWatchdog()
}

func (p *Proc) watchdogCheck(generation uint64) (string, time.Duration) {
	p.watchdogMu.Lock()
	defer p.watchdogMu.Unlock()
	if p.watchdog == nil || p.watchdog.generation != generation {
		return "", time.Hour
	}
	now := time.Now()
	return p.watchdog.violation(now), p.watchdog.nextCheck(now)
}

// One goroutine owns timeout arbitration for one prompt generation. An expired
// generation cannot affect a later prompt, including via a queued wake signal.
func (p *Proc) callPrompt(content []map[string]string) (rpcMessage, error) {
	generation := p.startWatchdog()
	defer p.stopWatchdog(generation)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan promptCallResult, 1)
	go func() {
		response, err := p.call(ctx, "session/prompt", map[string]any{"sessionId": p.SessionID(), "prompt": content})
		result <- promptCallResult{response: response, err: err}
	}()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		// A response already read from the native stream wins over a timer wake.
		select {
		case completed := <-result:
			return completed.response, completed.err
		default:
		}
		reason, delay := p.watchdogCheck(generation)
		if reason != "" {
			select {
			case completed := <-result:
				return completed.response, completed.err
			default:
			}
			p.expirePrompt(result)
			return rpcMessage{}, &watchdogFailure{code: reason}
		}
		resetTimer(timer, delay)
		select {
		case completed := <-result:
			return completed.response, completed.err
		case <-p.watchdogWake:
		case <-timer.C:
		}
	}
}

func (p *Proc) expirePrompt(result <-chan promptCallResult) {
	// ACP updates carry session identity, but no reliable native turn identity.
	// After expiry, suppress all further turn frames and retire this process even
	// if cancellation succeeds, so late updates cannot enter a subsequent turn.
	p.discardTurnFrames.Store(true)
	interruptDone := make(chan struct{})
	go func() { _ = p.Interrupt(); close(interruptDone) }()
	grace := p.watchdogCancelGrace
	if grace <= 0 {
		grace = 2 * time.Second
	}
	timer := time.NewTimer(grace)
	select {
	case <-result:
	case <-p.done:
	case <-timer.C:
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	// Close kills the agent, waits for process exit and drains its reader. The
	// caller emits the sole correlated terminal only after those barriers.
	_ = p.Close()
	<-interruptDone
}
