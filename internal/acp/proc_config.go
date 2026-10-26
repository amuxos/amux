package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/amuxos/amux/internal/provider"
	"github.com/amuxos/amux/internal/session"
)

const executionConfigTimeout = 20 * time.Second

type unconfirmedConfigWrite struct{ cause error }

func (e *unconfirmedConfigWrite) Error() string {
	return acpError("config.unconfirmed_write", e.cause).Error()
}
func (e *unconfirmedConfigWrite) Unwrap() error { return e.cause }

func (p *Proc) configDeadline() time.Duration {
	if p.configTimeout > 0 {
		return p.configTimeout
	}
	return executionConfigTimeout
}

type sessionConfigOption struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	CurrentValue string          `json:"currentValue"`
	Options      json.RawMessage `json:"options"`
}

type sessionSetupResult struct {
	SessionID     string                `json:"sessionId"`
	ConfigOptions []sessionConfigOption `json:"configOptions"`
}

func decodeSessionSetup(raw json.RawMessage, fallbackID string) (sessionSetupResult, error) {
	var result sessionSetupResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return result, acpError("session.decode", err)
	}
	if result.SessionID == "" {
		result.SessionID = fallbackID
	}
	if result.SessionID == "" {
		return result, acpError("session.empty_id")
	}
	return result, nil
}

func (p *Proc) beginSessionSetup() {
	p.stateMu.Lock()
	p.stagedCommands = make(map[string][]provider.AvailableCommand)
	p.settingUp.Store(true)
	p.stateMu.Unlock()
}

func (p *Proc) endSessionSetup() {
	p.stateMu.Lock()
	p.settingUp.Store(false)
	p.stagedCommands = nil
	p.stateMu.Unlock()
}

func (p *Proc) stageCommands(line []byte, commands []provider.AvailableCommand) {
	var envelope struct {
		Params struct {
			SessionID string `json:"sessionId"`
		} `json:"params"`
	}
	_ = json.Unmarshal(line, &envelope)
	p.stateMu.Lock()
	if p.stagedCommands != nil {
		p.stagedCommands[envelope.Params.SessionID] = append([]provider.AvailableCommand(nil), commands...)
	}
	p.stateMu.Unlock()
}

func (p *Proc) commitSession(id, model, effort string, options []sessionConfigOption) {
	p.stateMu.Lock()
	previousID := p.sessionID
	p.sessionID, p.model, p.reasoningEffort, p.configOptions = id, model, effort, options
	commands, ok := p.stagedCommands[id]
	if !ok {
		commands, ok = p.stagedCommands[""]
	}
	p.commands = append([]provider.AvailableCommand(nil), commands...)
	p.settingUp.Store(false)
	p.stagedCommands = nil
	p.stateMu.Unlock()
	p.emitAgentSessionID()
	if ok || previousID != "" {
		p.emitCommands(commands)
	}
}

func (p *Proc) createSession(ctx context.Context, model, effort string) error {
	p.beginSessionSetup()
	defer p.endSessionSetup()
	resp, err := p.call(ctx, "session/new", sessionSetupParams(p.cwd, ""))
	if err != nil {
		return err
	}
	result, err := decodeSessionSetup(resp.Result, "")
	if err != nil {
		return err
	}
	options, err := p.configureSession(ctx, result.SessionID, result.ConfigOptions, model, effort)
	if err != nil {
		return err
	}
	p.commitSession(result.SessionID, model, effort, options)
	return nil
}

func (p *Proc) hasCommand(name string) bool {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	for _, command := range p.commands {
		if command.Name == name {
			return true
		}
	}
	return false
}

func (p *Proc) SupportsExecutionReconfigure() bool { return p.executionConfig != nil }

func (p *Proc) desiredConfig(model, effort string) (map[string]string, error) {
	if p.executionConfig == nil {
		if model != "" || effort != "" {
			return nil, ErrExecutionConfigurationUnsupported
		}
		return nil, nil
	}
	return p.executionConfig(model, effort)
}

func (p *Proc) configureSession(ctx context.Context, id string, options []sessionConfigOption, model, effort string) ([]sessionConfigOption, error) {
	desired, err := p.desiredConfig(model, effort)
	if err != nil {
		return nil, err
	}
	configured, _, err := p.applyConfig(ctx, id, options, desired)
	var unconfirmed *unconfirmedConfigWrite
	if errors.As(err, &unconfirmed) {
		_ = p.Close()
	}
	return configured, err
}

// ReconfigureExecution serializes native configuration changes with turn dispatch.
// Metadata advances only after native readback; partial writes are rolled back.
func (p *Proc) ReconfigureExecution(model, reasoningEffort string) error {
	if !p.inputMu.TryLock() {
		return session.ErrTurnSinkActive
	}
	defer p.inputMu.Unlock()
	if p.executionConfig == nil {
		return ErrExecutionConfigurationUnsupported
	}
	desired, err := p.desiredConfig(model, reasoningEffort)
	if err != nil {
		return err
	}
	p.stateMu.Lock()
	id, previous := p.sessionID, p.configOptions
	p.stateMu.Unlock()
	configured, attempted, err := p.applyConfig(context.Background(), id, previous, desired)
	if err != nil {
		var unconfirmed *unconfirmedConfigWrite
		if errors.As(err, &unconfirmed) {
			_ = p.Close()
			return err
		}
		if attempted {
			rollback := make(map[string]string, len(desired))
			for _, option := range previous {
				if _, ok := desired[option.ID]; ok {
					rollback[option.ID] = option.CurrentValue
				}
			}
			// A failure response may omit configOptions or arrive after native mutation.
			// Force writes from the last advertised schema, including the failing key.
			if _, rollbackErr := p.restoreConfig(context.Background(), id, previous, rollback); rollbackErr != nil {
				_ = p.Close()
				return fmt.Errorf("%w; %v", err, acpError("config.rollback_failed", rollbackErr))
			}
		}
		return err
	}
	p.stateMu.Lock()
	p.model, p.reasoningEffort, p.configOptions = model, reasoningEffort, configured
	p.stateMu.Unlock()
	p.emitAgentSessionID()
	return nil
}

func validateConfig(options []sessionConfigOption, desired map[string]string) error {
	remaining := make(map[string]string, len(desired))
	for key, value := range desired {
		remaining[key] = value
	}
	seen := make(map[string]bool)
	for _, option := range options {
		value, needed := remaining[option.ID]
		if seen[option.ID] {
			return acpError("config.duplicate", option.ID)
		}
		seen[option.ID] = true
		if !needed {
			continue
		}
		if option.Type != "select" {
			return acpError("config.not_select", option.ID)
		}
		if !configHasValue(option.Options, value) {
			return acpError("config.unadvertised_value", option.ID, value)
		}
		delete(remaining, option.ID)
	}
	for key := range remaining {
		return acpError("config.unadvertised_key", key)
	}
	return nil
}

func configHasValue(raw json.RawMessage, value string) bool {
	var choices []struct {
		Value   *string         `json:"value"`
		Options json.RawMessage `json:"options"`
	}
	if json.Unmarshal(raw, &choices) != nil {
		return false
	}
	for _, choice := range choices {
		if choice.Value != nil && *choice.Value == value {
			return true
		}
		if len(choice.Options) > 0 && configHasValue(choice.Options, value) {
			return true
		}
	}
	return false
}

func (p *Proc) applyConfig(ctx context.Context, id string, options []sessionConfigOption, desired map[string]string) ([]sessionConfigOption, bool, error) {
	if err := validateConfig(options, desired); err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, p.configDeadline())
	defer cancel()
	attempted := false
	for _, option := range options {
		value, ok := desired[option.ID]
		if !ok {
			continue
		}
		if err := validateConfig(options, desired); err != nil {
			return nil, attempted, err
		}
		if configHasCurrentValue(options, option.ID, value) {
			continue
		}
		attempted = true
		updated, err := p.setConfig(ctx, id, option.ID, value)
		if err != nil {
			return nil, attempted, err
		}
		options = updated
	}
	for key, value := range desired {
		if !configHasCurrentValue(options, key, value) {
			return nil, attempted, acpError("config.readback_mismatch", key, value)
		}
	}
	return options, attempted, nil
}

func (p *Proc) restoreConfig(ctx context.Context, id string, options []sessionConfigOption, desired map[string]string) ([]sessionConfigOption, error) {
	ctx, cancel := context.WithTimeout(ctx, p.configDeadline())
	defer cancel()
	for _, option := range options {
		value, ok := desired[option.ID]
		if !ok {
			continue
		}
		updated, err := p.setConfig(ctx, id, option.ID, value)
		if err != nil {
			return nil, err
		}
		options = updated
	}
	for key, value := range desired {
		if !configHasCurrentValue(options, key, value) {
			return nil, acpError("config.rollback_mismatch", key)
		}
	}
	return options, nil
}

func configHasCurrentValue(options []sessionConfigOption, key, value string) bool {
	for _, option := range options {
		if option.ID == key {
			return option.CurrentValue == value
		}
	}
	return false
}

func (p *Proc) setConfig(ctx context.Context, id, key, value string) ([]sessionConfigOption, error) {
	resp, err := p.call(ctx, "session/set_config_option", map[string]string{"sessionId": id, "configId": key, "value": value})
	if err != nil {
		if resp.ID == 0 {
			return nil, &unconfirmedConfigWrite{cause: err}
		}
		return nil, err
	}
	var result struct {
		ConfigOptions []sessionConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, acpError("config.decode", err)
	}
	if err := validateConfig(result.ConfigOptions, map[string]string{key: value}); err != nil {
		return nil, err
	}
	if !configHasCurrentValue(result.ConfigOptions, key, value) {
		return nil, acpError("config.readback_mismatch", key, value)
	}
	return result.ConfigOptions, nil
}

// The advertised option set may change after native commands or model changes.
// Malformed snapshots fail closed: a later explicit configuration must obtain
// a valid advertised selection instead of reusing stale options.
func (p *Proc) captureConfigUpdate(line []byte) bool {
	var envelope struct {
		Method string `json:"method"`
		Params struct {
			Update struct {
				Kind          string          `json:"sessionUpdate"`
				ConfigOptions json.RawMessage `json:"configOptions"`
			} `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &envelope) != nil || envelope.Method != "session/update" || envelope.Params.Update.Kind != "config_option_update" {
		return false
	}
	var options []sessionConfigOption
	if json.Unmarshal(envelope.Params.Update.ConfigOptions, &options) != nil {
		options = nil
	}
	p.stateMu.Lock()
	p.configOptions = options
	p.stateMu.Unlock()
	return true
}
