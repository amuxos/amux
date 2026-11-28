package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/hubweb"
)

type managedTurnViewer struct {
	ctx     context.Context
	backend hubweb.Backend
	config  config.TurnViewerConfig
	hubIP   string
	keyPath string

	mu     sync.Mutex
	active *turnViewerInstance
}

type turnViewerInstance struct {
	server   *http.Server
	listener net.Listener
	cancel   context.CancelFunc
	baseURL  string
	token    string
}

func (v *managedTurnViewer) settings() (baseURL, token string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.active == nil {
		return "", ""
	}
	return v.active.baseURL, v.active.token
}

func (v *managedTurnViewer) setEnabled(enabled bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if (v.active != nil) == enabled {
		return nil
	}
	if !enabled {
		active := v.active
		v.active = nil
		active.cancel()
		_ = active.server.Close()
		_ = active.listener.Close()
		return nil
	}
	if err := v.ctx.Err(); err != nil {
		return err
	}
	addr, baseURL, err := turnViewerAddresses(v.config, v.hubIP)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	token, err := loadTurnViewerKey(v.keyPath)
	if err != nil {
		_ = listener.Close()
		return err
	}
	ctx, cancel := context.WithCancel(v.ctx)
	instance := &turnViewerInstance{
		baseURL: baseURL, token: token, listener: listener, cancel: cancel,
		server: &http.Server{
			Handler:           hubweb.New(v.backend, token, version).TurnProcessHandler(),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       time.Minute,
			BaseContext:       func(net.Listener) context.Context { return ctx },
		},
	}
	v.active = instance
	go func() {
		if err := instance.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "hub: turn viewer: %v\n", err)
		}
		cancel()
		v.mu.Lock()
		if v.active == instance {
			v.active = nil
		}
		v.mu.Unlock()
	}()
	return nil
}

func turnViewerAddresses(settings config.TurnViewerConfig, hubIP string) (addr, baseURL string, err error) {
	if !config.ValidTurnViewerAddr(settings.Addr) || !config.ValidTurnViewerBaseURL(settings.BaseURL) {
		return "", "", errors.New("invalid hub.turn_viewer configuration")
	}
	if ip := net.ParseIP(hubIP); ip == nil || !ip.IsPrivate() {
		hubIP = "127.0.0.1"
	}
	addr = settings.Addr
	if addr == "" {
		addr = net.JoinHostPort(hubIP, config.DefaultTurnViewerPort)
	}
	if settings.BaseURL != "" {
		return addr, settings.BaseURL, nil
	}
	host, port, _ := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); host == "" || ip.IsUnspecified() {
		host = hubIP
	}
	return addr, "http://" + net.JoinHostPort(host, port), nil
}

func loadTurnViewerKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		key, err := hex.DecodeString(string(data))
		if err != nil || len(key) != 32 {
			return "", errors.New("invalid Turn viewer signing key")
		}
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	value := hex.EncodeToString(key[:])
	f, err := os.CreateTemp(dir, ".turn-viewer-key-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(value)
	if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
		return "", err
	}
	if err := os.Link(f.Name(), path); os.IsExist(err) {
		return loadTurnViewerKey(path)
	} else if err != nil {
		return "", err
	}
	return value, nil
}
