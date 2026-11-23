// Package modelservice connects distribution model services to existing AMUX
// principal authentication. It does not implement a second credential system.
package modelservice

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/amuxos/amux/internal/extensionstate"
)

type Model struct {
	Ref   string `json:"ref"`
	Label string `json:"label"`
}

type State struct {
	Status     string  `json:"status"` // signed_in, signed_out, pending, expired, setup_required, installing
	LoginURL   string  `json:"login_url,omitempty"`
	Notice     string  `json:"notice,omitempty"`
	Models     []Model `json:"models,omitempty"`
	CanInstall bool    `json:"can_install,omitempty"`
}

// ProxyError is a safe, classified failure returned by a distribution model
// service before it has written an HTTP response. Message is safe to expose to
// the local agent; underlying provider and credential details remain private.
type ProxyError struct {
	Status  int
	Message string
}

func (e *ProxyError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func NewProxyError(status int, message string) error {
	return &ProxyError{Status: status, Message: message}
}

func ProxyErrorStatus(err error) (int, bool) {
	var proxyErr *ProxyError
	if !errors.As(err, &proxyErr) || proxyErr == nil {
		return 0, false
	}
	return proxyErr.Status, true
}

// WriteProxyError relays only the safe classified payload, never an enclosing
// error's diagnostic text. Call it only before the response has started.
func WriteProxyError(w http.ResponseWriter, err error) bool {
	var proxyErr *ProxyError
	if !errors.As(err, &proxyErr) || proxyErr == nil {
		return false
	}
	code, message := proxyErr.Status, proxyErr.Message
	if code < 400 || code > 599 || strings.TrimSpace(message) == "" {
		code, message = http.StatusServiceUnavailable, errorMessage("unavailable")
	}
	gatewayError(w, code, message)
	return true
}

type Service struct {
	ID       string
	Name     string
	Runtimes []string
	// AllowedPaths optionally declares exact HTTP paths accepted by Hub
	// ProxyModel in addition to the standard /v1/* endpoints, enabling
	// transparent passthrough gateways.
	AllowedPaths []string
	// AllowLegacyHost permits ProxyModel calls from non-isolated legacy-host
	// launches where no per-session actor token is minted (invalid actor tokens
	// are still rejected). Standard managed services leave this false.
	AllowLegacyHost bool
	// Query receives a trusted principal, never a principal supplied in a card.
	// Actions are status, login, refresh and an explicit install opt-in for
	// services that return CanInstall. Challenges remain service-private.
	Query func(context.Context, string, string) (State, error)
	Proxy func(context.Context, string, http.ResponseWriter, *http.Request) error
}

var registry = struct {
	sync.RWMutex
	services map[string]Service
}{services: map[string]Service{}}

func Register(s Service) error {
	release, err := extensionstate.BeginRegistration()
	if err != nil {
		return err
	}
	defer release()
	if s.ID == "" || s.Name == "" || s.Query == nil || s.Proxy == nil {
		return fmt.Errorf("incomplete model service")
	}
	registry.Lock()
	defer registry.Unlock()
	if _, ok := registry.services[s.ID]; ok {
		return fmt.Errorf("model service %q already registered", s.ID)
	}
	s.Runtimes = append([]string(nil), s.Runtimes...)
	s.AllowedPaths = append([]string(nil), s.AllowedPaths...)
	registry.services[s.ID] = s
	return nil
}

func Get(id string) (Service, bool) {
	registry.RLock()
	defer registry.RUnlock()
	s, ok := registry.services[id]
	if ok {
		s.Runtimes = append([]string(nil), s.Runtimes...)
		s.AllowedPaths = append([]string(nil), s.AllowedPaths...)
	}
	return s, ok
}

func List() []Service {
	registry.RLock()
	defer registry.RUnlock()
	out := make([]Service, 0, len(registry.services))
	for _, s := range registry.services {
		s.Runtimes = append([]string(nil), s.Runtimes...)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
