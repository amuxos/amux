package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/amuxos/amux/internal/task"
	"github.com/amuxos/amux/internal/webconsole"
)

const (
	consoleCredentialEnvLimit  = 64 << 10
	consoleCredentialFileLimit = 512 << 10
)

// consoleCredentialStore is the host credential store the operator console
// manages. Values travel only browser → store; every response carries names.
func consoleCredentialStore() task.FileCredentials {
	return task.FileCredentials{Dir: filepath.Join(defaultAmuxDir(), "secrets")}
}

func consoleCredentialView(meta task.CredentialProfileMetadata) map[string]any {
	return map[string]any{"profile": meta.Profile, "kind": credentialKindOrDefault(meta.Kind), "env_names": nonNilStrings(meta.EnvNames), "file_names": nonNilStrings(meta.FileNames)}
}

func (b *consoleBackend) consoleExecuteCredential(_ context.Context, action string, data json.RawMessage) (any, error) {
	var request struct {
		Profile string `json:"profile"`
		Name    string `json:"name"`
		Value   string `json:"value"`
		Confirm string `json:"confirm"`
	}
	if err := consoleDecode(data, &request); err != nil {
		return nil, err
	}
	request.Profile = strings.TrimSpace(request.Profile)
	request.Name = strings.TrimSpace(request.Name)
	if !configProfileNamePattern.MatchString(request.Profile) {
		return nil, consoleInvalid("invalid_field", "profile")
	}
	switch action {
	case "set_env", "unset_env":
		if !credentialEnvNamePattern.MatchString(request.Name) {
			return nil, consoleInvalid("invalid_field", "name")
		}
	case "set_file", "unset_file":
		if !task.ValidCredentialFileName(request.Name) {
			return nil, consoleInvalid("invalid_field", "name")
		}
	case "delete":
		if request.Confirm != request.Profile {
			return nil, consoleInvalid("confirmation_required", "confirm")
		}
	}
	switch action {
	case "set_env":
		if request.Value == "" || len(request.Value) > consoleCredentialEnvLimit || strings.ContainsAny(request.Value, "\x00\r\n") {
			return nil, consoleInvalid("invalid_field", "value")
		}
	case "set_file":
		if request.Value == "" || len(request.Value) > consoleCredentialFileLimit || strings.ContainsRune(request.Value, 0) {
			return nil, consoleInvalid("invalid_field", "value")
		}
	}
	store := consoleCredentialStore()
	b.localMu.Lock()
	defer b.localMu.Unlock()
	var (
		meta task.CredentialProfileMetadata
		err  error
	)
	switch action {
	case "set_env":
		meta, err = store.PutEnv(request.Profile, request.Name, request.Value)
	case "unset_env":
		meta, err = store.UnsetEnv(request.Profile, request.Name)
	case "set_file":
		meta, err = store.SetFile(request.Profile, request.Name, request.Value)
	case "unset_file":
		meta, err = store.UnsetFile(request.Profile, request.Name)
	case "delete":
		if err = store.DeleteProfile(request.Profile); err == nil {
			return map[string]any{"profile": request.Profile, "status": "deleted"}, nil
		}
	default:
		return nil, consoleInvalid("unsupported_action", "")
	}
	switch {
	case err == nil:
		return map[string]any{"profile": consoleCredentialView(meta), "status": "saved"}, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, consoleMissing()
	case errors.Is(err, task.ErrNotCredentialProfile):
		return nil, &consoleResourceError{code: "credential_profile_conflict", field: "profile", cause: webconsole.ErrConflict}
	}
	return nil, err
}
