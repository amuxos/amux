package agentprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/amuxos/amux/internal/amuxhome"
)

const managedAuthoringStateSchema = "managed-authoring-state.v1"

type ManagedAuthoringStatus string

const (
	ManagedAuthoringStatusReady    ManagedAuthoringStatus = "ready"
	ManagedAuthoringStatusDegraded ManagedAuthoringStatus = "degraded"
)

type ManagedAuthoringState struct {
	Schema                     string                 `json:"schema"`
	AgentPackContractStatus    ManagedAuthoringStatus `json:"agent_pack_contract_status"`
	LocalExpertAuthoringStatus ManagedAuthoringStatus `json:"local_expert_authoring_status"`
}

func ReadyManagedAuthoringState() ManagedAuthoringState {
	return ManagedAuthoringState{
		Schema:                     managedAuthoringStateSchema,
		AgentPackContractStatus:    ManagedAuthoringStatusReady,
		LocalExpertAuthoringStatus: ManagedAuthoringStatusReady,
	}
}

func DefaultManagedAuthoringStatePath() string {
	return filepath.Join(amuxhome.Dir(), "managed-agent-packs", "authoring-state.json")
}

func SaveDefaultManagedAuthoringState(state ManagedAuthoringState) error {
	if state.Schema == "" {
		state.Schema = managedAuthoringStateSchema
	}
	if err := validateManagedAuthoringState(state); err != nil {
		return err
	}
	path := DefaultManagedAuthoringStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create managed authoring state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".authoring-state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("activate managed authoring state: %w", err)
	}
	return nil
}

func LoadDefaultManagedAuthoringState() (ManagedAuthoringState, error) {
	data, err := os.ReadFile(DefaultManagedAuthoringStatePath())
	if os.IsNotExist(err) {
		return ReadyManagedAuthoringState(), nil
	}
	if err != nil {
		return ManagedAuthoringState{}, err
	}
	var state ManagedAuthoringState
	if err := json.Unmarshal(data, &state); err != nil {
		return ManagedAuthoringState{}, fmt.Errorf("parse managed authoring state: %w", err)
	}
	if err := validateManagedAuthoringState(state); err != nil {
		return ManagedAuthoringState{}, err
	}
	return state, nil
}

func RequireDefaultManagedAuthoringReady() error {
	state, err := LoadDefaultManagedAuthoringState()
	if err != nil {
		return fmt.Errorf("inspect local expert authoring state: %w", err)
	}
	if state.AgentPackContractStatus != ManagedAuthoringStatusReady || state.LocalExpertAuthoringStatus != ManagedAuthoringStatusReady {
		return fmt.Errorf("local expert authoring is degraded; run `amux update` to repair the managed Agent Pack Contract runtime and local library")
	}
	return nil
}

func validateManagedAuthoringState(state ManagedAuthoringState) error {
	if state.Schema != managedAuthoringStateSchema {
		return fmt.Errorf("unsupported managed authoring state schema %q", state.Schema)
	}
	for name, status := range map[string]ManagedAuthoringStatus{
		"agent_pack_contract_status":    state.AgentPackContractStatus,
		"local_expert_authoring_status": state.LocalExpertAuthoringStatus,
	} {
		if status != ManagedAuthoringStatusReady && status != ManagedAuthoringStatusDegraded {
			return fmt.Errorf("invalid %s %q", name, status)
		}
	}
	if state.AgentPackContractStatus == ManagedAuthoringStatusDegraded && state.LocalExpertAuthoringStatus == ManagedAuthoringStatusReady {
		return fmt.Errorf("local expert authoring cannot be ready while the managed Agent Pack Contract runtime is degraded")
	}
	return nil
}
