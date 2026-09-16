package bridge

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"sync"

	"github.com/amuxos/amux/internal/agentui"
	"github.com/amuxos/amux/internal/extensionstate"
)

// Extension supplies distribution-specific Bridge behavior. Install it before
// invoking Bridge operations. Nil hooks retain the public defaults.
type Extension struct {
	ActivityPossiblyQueued func(string, *agentui.ActivityView) bool
	ProviderErrorNotice    func(string) string
	ReplyCardAgent         func(string) bool
	AgentTypeName          func(string) string
	CoordinationProfile    func(string) bool
	MigrateDefaults        func(*sql.Tx) error
	ConsoleSetup           func(context.Context, string, ConsoleOptions) ConsoleResult
}

type extensionRegistry struct {
	mu         sync.Mutex
	extension  Extension
	registered bool
	used       bool
}

var bridgeExtensions extensionRegistry

// RegisterExtension installs the process's Bridge customization exactly once.
func RegisterExtension(extension Extension) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	return bridgeExtensions.register(extension)
}

func (r *extensionRegistry) register(extension Extension) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.registered {
		return fmt.Errorf("bridge extension already registered")
	}
	if r.used {
		return fmt.Errorf("bridge extension registration must precede first use")
	}
	r.extension = extension
	r.registered = true
	return nil
}

func (r *extensionRegistry) snapshot() Extension {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.used = true
	return r.extension
}

// RenderQRCode shares the official provisioning renderer with extensions.
func RenderQRCode(writer io.Writer, payload string) { renderQRCode(writer, payload) }
