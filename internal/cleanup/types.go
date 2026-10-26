// Package cleanup implements bounded, reference-aware maintenance of managed data.
package cleanup

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/extensionstate"
)

type Step struct {
	Name       string   `json:"name"`
	Candidates int      `json:"candidates"`
	Deleted    int      `json:"deleted"`
	Skipped    int      `json:"skipped"`
	Bytes      int64    `json:"bytes"`
	Errors     []string `json:"errors,omitempty"`
}

func (s *Step) Error(err error) {
	if err != nil {
		s.Errors = append(s.Errors, err.Error())
	}
}

type Report struct {
	ID         string    `json:"id"`
	Target     string    `json:"target"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	DryRun     bool      `json:"dry_run"`
	Steps      []Step    `json:"steps"`
}

func (r Report) Failed() bool {
	for _, s := range r.Steps {
		if len(s.Errors) > 0 {
			return true
		}
	}
	return false
}

// References contains every persisted Session, including archived and sleeping
// Sessions. An empty snapshot is valid only after a successful authoritative read.
type References struct {
	SessionIDs      []string `json:"session_ids"`
	AgentSessionIDs []string `json:"agent_session_ids"`
	Workspaces      []string `json:"workspaces"`
	ProtectedPaths  []string `json:"protected_paths,omitempty"`
}

// HomePolicy is registered in both the Runtime and its matching helper binary.
// Requests cannot introduce arbitrary privileged filesystem layouts.
type HomePolicy struct {
	Name       string
	RolloutDir string
	TrustFile  string
}

var policies []HomePolicy

func RegisterHomePolicy(p HomePolicy) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if p.Name == "" {
		return fmt.Errorf("cleanup policy name is empty")
	}
	for _, path := range []string{p.RolloutDir, p.TrustFile} {
		if path != "" && (!filepath.IsLocal(path) || filepath.Clean(path) == ".") {
			return fmt.Errorf("cleanup policy path must be HOME-relative")
		}
	}
	for _, old := range policies {
		if old.Name == p.Name {
			return fmt.Errorf("duplicate cleanup policy %q", p.Name)
		}
	}
	policies = append(policies, p)
	return nil
}
func HomePolicies() []HomePolicy { return append([]HomePolicy(nil), policies...) }
func Related(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	return a == b || strings.HasPrefix(a, strings.TrimSuffix(b, string(filepath.Separator))+string(filepath.Separator)) || strings.HasPrefix(b, strings.TrimSuffix(a, string(filepath.Separator))+string(filepath.Separator))
}
