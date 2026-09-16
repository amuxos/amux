package updatecheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
)

const (
	StateSchema         = "amux.update.state.v1"
	defaultInitialDelay = time.Minute
	defaultInterval     = 12 * time.Hour
)

type Comparison string

const (
	ComparisonUnknown Comparison = "unknown"
	ComparisonOlder   Comparison = "older"
	ComparisonSame    Comparison = "same"
	ComparisonNewer   Comparison = "newer"
)

type State struct {
	Schema            string     `json:"schema"`
	CurrentVersion    string     `json:"current_version"`
	LatestVersion     string     `json:"latest_version,omitempty"`
	Comparison        Comparison `json:"comparison"`
	CheckedAt         time.Time  `json:"checked_at,omitempty"`
	DismissedVersion  string     `json:"dismissed_version,omitempty"`
	ReleaseSourceHash string     `json:"release_source_hash"`
}

func (s State) UpdateAvailable() bool {
	return s.Comparison == ComparisonNewer
}

func (s State) ShouldPrompt() bool {
	return s.UpdateAvailable() && s.LatestVersion != s.DismissedVersion
}

type MonitorOptions struct {
	CurrentVersion string
	Source         string
	StatePath      string
	InitialDelay   time.Duration
	Interval       time.Duration
	Now            func() time.Time
	FetchLatest    func(context.Context) (string, error)
}

type Monitor struct {
	refreshMu    sync.Mutex
	mu           sync.RWMutex
	state        State
	statePath    string
	initialDelay time.Duration
	interval     time.Duration
	now          func() time.Time
	fetchLatest  func(context.Context) (string, error)
}

func DefaultStatePath() string {
	return filepath.Join(amuxhome.Dir(), "update-state.json")
}

func NewMonitor(options MonitorOptions) (*Monitor, error) {
	statePath := strings.TrimSpace(options.StatePath)
	if statePath == "" {
		statePath = DefaultStatePath()
	}
	interval := options.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	initialDelay := options.InitialDelay
	if initialDelay <= 0 {
		initialDelay = defaultInitialDelay
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	currentVersion := strings.TrimSpace(options.CurrentVersion)
	sourceHash := releaseSourceHash(options.Source)
	state := State{
		Schema:            StateSchema,
		CurrentVersion:    currentVersion,
		Comparison:        ComparisonUnknown,
		ReleaseSourceHash: sourceHash,
	}
	normalizePersistedState := false
	if persisted, err := ReadState(statePath); err == nil {
		if persisted.ReleaseSourceHash == sourceHash {
			state = persisted
			state.CurrentVersion = currentVersion
			state.Comparison = CompareVersions(currentVersion, state.LatestVersion)
		}
		// Persist both source invalidation and a recomputed current version so
		// CLI readers never observe stale state before the first refresh.
		normalizePersistedState = true
	}
	monitor := &Monitor{
		state:        state,
		statePath:    statePath,
		initialDelay: initialDelay,
		interval:     interval,
		now:          now,
		fetchLatest:  options.FetchLatest,
	}
	if normalizePersistedState {
		if err := writeState(statePath, state); err != nil {
			return monitor, fmt.Errorf("normalize update state: %w", err)
		}
	}
	return monitor, nil
}

func (m *Monitor) Snapshot() State {
	if m == nil {
		return State{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state
}

func (m *Monitor) Refresh(ctx context.Context) (State, error) {
	if m == nil || m.fetchLatest == nil {
		return State{}, fmt.Errorf("update checker is not configured")
	}
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	latestVersion, err := m.fetchLatest(ctx)
	if err != nil {
		return m.Snapshot(), err
	}
	latestVersion = strings.TrimSpace(latestVersion)
	if latestVersion == "" {
		return m.Snapshot(), fmt.Errorf("latest amux version is empty")
	}
	now := m.now().UTC()

	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.state
	next.Schema = StateSchema
	next.LatestVersion = latestVersion
	next.Comparison = CompareVersions(next.CurrentVersion, latestVersion)
	next.CheckedAt = now
	if err := writeState(m.statePath, next); err != nil {
		return m.state, err
	}
	m.state = next
	return next, nil
}

func (m *Monitor) Dismiss(version string) error {
	if m == nil {
		return fmt.Errorf("update checker is not configured")
	}
	version = strings.TrimSpace(version)
	m.mu.Lock()
	defer m.mu.Unlock()
	if version == "" || version != m.state.LatestVersion || !m.state.UpdateAvailable() {
		return fmt.Errorf("amux update %q is not currently available", version)
	}
	next := m.state
	next.DismissedVersion = version
	if err := writeState(m.statePath, next); err != nil {
		return err
	}
	m.state = next
	return nil
}

func (m *Monitor) Run(ctx context.Context) {
	if m == nil {
		return
	}
	timer := time.NewTimer(m.initialDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_, _ = m.Refresh(ctx)
			timer.Reset(jitteredInterval(m.interval))
		}
	}
}

func ReadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, fmt.Errorf("decode update state: %w", err)
	}
	if state.Schema != StateSchema {
		return State{}, fmt.Errorf("unsupported update state schema %q", state.Schema)
	}
	return state, nil
}

func writeState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-state-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func releaseSourceHash(source string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(source)))
	return hex.EncodeToString(sum[:])
}

func jitteredInterval(interval time.Duration) time.Duration {
	if interval <= 0 {
		return defaultInterval
	}
	window := interval / 10
	if window <= 0 {
		return interval
	}
	return interval - window + time.Duration(rand.Int64N(int64(2*window)+1))
}

// CompareVersions compares numeric release versions such as v1.2.3 and
// 1.0.0.1457. Development, dirty, or otherwise ambiguous versions are left
// unknown so an automatic check never advertises a downgrade as an update.
func CompareVersions(current, latest string) Comparison {
	currentParts, currentOK := numericVersionParts(current)
	latestParts, latestOK := numericVersionParts(latest)
	if !currentOK || !latestOK {
		return ComparisonUnknown
	}
	length := max(len(currentParts), len(latestParts))
	for index := 0; index < length; index++ {
		var currentPart, latestPart uint64
		if index < len(currentParts) {
			currentPart = currentParts[index]
		}
		if index < len(latestParts) {
			latestPart = latestParts[index]
		}
		switch {
		case latestPart > currentPart:
			return ComparisonNewer
		case latestPart < currentPart:
			return ComparisonOlder
		}
	}
	return ComparisonSame
}

func numericVersionParts(value string) ([]uint64, bool) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	if value == "" {
		return nil, false
	}
	rawParts := strings.Split(value, ".")
	parts := make([]uint64, len(rawParts))
	for index, raw := range rawParts {
		if raw == "" {
			return nil, false
		}
		part, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return nil, false
		}
		parts[index] = part
	}
	return parts, true
}
