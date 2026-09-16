package bridge

import (
	"net/http"
	"time"
)

// httpDoer is the subset of *http.Client used here; tests inject a fake.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ConsoleResult is the outcome of legacy console configuration.
type ConsoleResult struct {
	OK                bool
	SessionFile       string
	SessionSource     string // amux_cache | qr_login | distribution-specific fallback
	ScopeCount        int
	SkippedScopeCount int
	ScopeWarning      string
	VersionID         string
	Reason            string // set when !OK
	Message           string // set when !OK
}

// ConsoleOptions configures the legacy console fallback.
type ConsoleOptions struct {
	Client                 httpDoer
	Manifest               ScopeManifest
	SessionFilePath        string
	SessionFallbackPath    string
	DisableSessionFallback bool
	PollInterval           time.Duration
	MaxWait                time.Duration
	OnQRCode               func(qrText, qrPayload string)
	OnStatus               func(msg string)

	// Test overrides. Empty values fall back to production origins.
	AccountsOrigin string
	AskOrigin      string
	ConsoleOrigin  string
}
