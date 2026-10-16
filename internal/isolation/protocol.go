package isolation

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	HelperProtocolVersion = 1
	MaxHelperFrameBytes   = 4 << 20
	MaxLaunchVectorBytes  = 2 << 20
)

type HelperOperation string

const (
	OpHandshake       HelperOperation = "handshake"
	OpEnsurePrincipal HelperOperation = "ensure_principal"
	OpPrepareRun      HelperOperation = "prepare_run"
	OpSessionFile     HelperOperation = "session_file"
	OpMaterializeRun  HelperOperation = "materialize_run"
	OpStart           HelperOperation = "start"
	OpDescribe        HelperOperation = "describe"
	OpWait            HelperOperation = "wait"
	OpSignal          HelperOperation = "signal"
	OpKill            HelperOperation = "kill"
	OpCloseHandle     HelperOperation = "close_handle"
	OpRemoveRun       HelperOperation = "remove_run"
	OpRemoveWorkspace HelperOperation = "remove_workspace"
	OpReclaimHome     HelperOperation = "reclaim_home"
	OpKillPrincipal   HelperOperation = "kill_principal"
	OpDrain           HelperOperation = "drain"
	OpUpgradeGuard    HelperOperation = "upgrade_guard"
	OpListAccounts    HelperOperation = "list_accounts"
	OpRemoveAccount   HelperOperation = "remove_account"
)

type AccountMapping struct {
	PrincipalID string    `json:"principal_id"`
	Account     string    `json:"account"`
	UID         int       `json:"uid"`
	GID         int       `json:"gid"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	LastUsedAt  time.Time `json:"last_used_at,omitempty"`
}

type PrincipalPaths struct {
	Root      string `json:"root"`
	Home      string `json:"home"`
	Workspace string `json:"workspace"`
	Run       string `json:"run,omitempty"`
}

type LaunchDescription struct {
	LaunchID   string         `json:"launch_id"`
	Principal  string         `json:"principal_id,omitempty"`
	Account    string         `json:"account,omitempty"`
	UID        int            `json:"uid,omitempty"`
	PID        int            `json:"pid,omitempty"`
	PIDStart   uint64         `json:"pid_start,omitempty"`
	State      string         `json:"state"`
	StartedAt  time.Time      `json:"started_at,omitempty"`
	ExitStatus *ExitStatus    `json:"exit_status,omitempty"`
	Paths      PrincipalPaths `json:"paths,omitempty"`
	Attached   bool           `json:"attached,omitempty"`
}

type HelperRequest struct {
	FileAction      string          `json:"file_action,omitempty"`
	FileName        string          `json:"file_name,omitempty"`
	ProtocolVersion int             `json:"protocol_version"`
	RequestID       string          `json:"request_id"`
	Operation       HelperOperation `json:"operation"`
	LaunchID        string          `json:"launch_id,omitempty"`
	ExecutionID     string          `json:"execution_id,omitempty"`
	Mapping         *AccountMapping `json:"mapping,omitempty"`
	Spec            *LaunchSpec     `json:"spec,omitempty"`
	Signal          string          `json:"signal,omitempty"`
	HomeCaches      []HomeCacheSpec `json:"home_caches,omitempty"`
	Force           bool            `json:"force,omitempty"`
}

// HomeCacheSpec is one reclaimable provider cache inside a principal HOME. Path
// gates the decision by size; when it exceeds MaxBytes (or MaxBytes is 0) every
// entry in Remove is deleted. All paths are HOME-relative.
type HomeCacheSpec struct {
	Path     string   `json:"path"`
	MaxBytes int64    `json:"max_bytes,omitempty"`
	Remove   []string `json:"remove"`
}

type HelperResponse struct {
	ProtocolVersion int                `json:"protocol_version"`
	RequestID       string             `json:"request_id"`
	LaunchID        string             `json:"launch_id,omitempty"`
	OK              bool               `json:"ok"`
	ErrorCode       string             `json:"error_code,omitempty"`
	Error           string             `json:"error,omitempty"`
	HelperVersion   string             `json:"helper_version,omitempty"`
	Description     *LaunchDescription `json:"description,omitempty"`
	Mapping         *AccountMapping    `json:"mapping,omitempty"`
	Paths           *PrincipalPaths    `json:"paths,omitempty"`
	Accounts        []AccountMapping   `json:"accounts,omitempty"`
	Draining        bool               `json:"draining,omitempty"`
	Hardening       map[string]bool    `json:"hardening,omitempty"`
	DegradedReasons []string           `json:"degraded_reasons,omitempty"`
	ActiveLaunches  int                `json:"active_launches,omitempty"`
}

func validateLaunchSpec(spec *LaunchSpec) error {
	if spec == nil {
		return errors.New("launch spec is required")
	}
	if strings.TrimSpace(spec.Path) == "" {
		return errors.New("launch path is required")
	}
	total := len(spec.Path) + len(spec.Dir)
	values := append(append([]string{}, spec.Args...), spec.Env...)
	values = append(values, spec.Path, spec.Dir)
	for _, value := range values {
		if strings.IndexByte(value, 0) >= 0 {
			return errors.New("launch vector contains NUL")
		}
		total += len(value)
	}
	if total > MaxLaunchVectorBytes {
		return fmt.Errorf("launch vector exceeds %d bytes", MaxLaunchVectorBytes)
	}
	return nil
}

func encodeHelperFrame(value any) ([]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxHelperFrameBytes {
		return nil, fmt.Errorf("helper frame exceeds %d bytes", MaxHelperFrameBytes)
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(payload)))
	copy(frame[4:], payload)
	return frame, nil
}

func decodeHelperFrame(frame []byte, value any) error {
	if len(frame) < 4 {
		return io.ErrUnexpectedEOF
	}
	size := int(binary.BigEndian.Uint32(frame[:4]))
	if size < 0 || size > MaxHelperFrameBytes {
		return fmt.Errorf("invalid helper frame size %d", size)
	}
	if len(frame)-4 != size {
		return io.ErrUnexpectedEOF
	}
	return json.Unmarshal(frame[4:], value)
}
