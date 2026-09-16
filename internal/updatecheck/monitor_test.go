package updatecheck

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompareVersionsOnlyAdvertisesNewerNumericRelease(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    Comparison
	}{
		{name: "internal four part release", current: "1.0.0.1457", latest: "1.0.0.1458", want: ComparisonNewer},
		{name: "v-prefixed release", current: "v1.2.3", latest: "v1.3.0", want: ComparisonNewer},
		{name: "same release", current: "1.0.0.1458", latest: "1.0.0.1458", want: ComparisonSame},
		{name: "installed release is newer", current: "1.0.0.1459", latest: "1.0.0.1458", want: ComparisonOlder},
		{name: "development build", current: "dev", latest: "1.0.0.1458", want: ComparisonUnknown},
		{name: "dirty build", current: "1.0.0.1457-dirty", latest: "1.0.0.1458", want: ComparisonUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CompareVersions(tt.current, tt.latest); got != tt.want {
				t.Fatalf("CompareVersions(%q, %q) = %q, want %q", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestMonitorRunChecksAfterInitialDelayAndStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checked := make(chan struct{})
	var once sync.Once
	monitor := newTestMonitor(t, MonitorOptions{
		CurrentVersion: "1.0.0.1457",
		Source:         "https://example.test/latest",
		StatePath:      filepath.Join(t.TempDir(), "update-state.json"),
		InitialDelay:   5 * time.Millisecond,
		Interval:       time.Hour,
		FetchLatest: func(context.Context) (string, error) {
			once.Do(func() { close(checked) })
			return "1.0.0.1458", nil
		},
	})
	done := make(chan struct{})
	go func() {
		monitor.Run(ctx)
		close(done)
	}()
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("monitor did not perform its delayed check")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop with its context")
	}
}

func TestMonitorSerializesConcurrentRefreshesSoOlderResponseCannotWin(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondFinished := make(chan struct{})
	var calls atomic.Int32
	monitor := newTestMonitor(t, MonitorOptions{
		CurrentVersion: "1.0.0.1457",
		Source:         "https://example.test/latest",
		StatePath:      filepath.Join(t.TempDir(), "update-state.json"),
		FetchLatest: func(context.Context) (string, error) {
			switch calls.Add(1) {
			case 1:
				close(firstStarted)
				<-releaseFirst
				return "1.0.0.1458", nil
			default:
				return "1.0.0.1459", nil
			}
		},
	})

	firstDone := make(chan struct{})
	go func() {
		_, _ = monitor.Refresh(context.Background())
		close(firstDone)
	}()
	<-firstStarted
	go func() {
		_, _ = monitor.Refresh(context.Background())
		close(secondFinished)
	}()

	select {
	case <-secondFinished:
		// Without serialization the newer response completes while the first
		// request is blocked and is then overwritten by its stale result.
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	<-firstDone
	<-secondFinished

	if got := monitor.Snapshot().LatestVersion; got != "1.0.0.1459" {
		t.Fatalf("latest version = %q, want newest serialized response", got)
	}
}

func TestMonitorRefreshPersistsAvailableUpdateAndDismissal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-state.json")
	now := time.Date(2026, 8, 10, 8, 0, 0, 0, time.UTC)
	latestVersion := "1.0.0.1458"
	monitor := newTestMonitor(t, MonitorOptions{
		CurrentVersion: "1.0.0.1457",
		Source:         "https://example.test/latest",
		StatePath:      path,
		Interval:       12 * time.Hour,
		Now:            func() time.Time { return now },
		FetchLatest: func(context.Context) (string, error) {
			return latestVersion, nil
		},
	})

	state, err := monitor.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.UpdateAvailable() || !state.ShouldPrompt() {
		t.Fatalf("refreshed state = %#v, want promptable update", state)
	}
	if state.CheckedAt != now {
		t.Fatalf("checked at = %s", state.CheckedAt)
	}

	persisted, err := ReadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.LatestVersion != "1.0.0.1458" || !persisted.UpdateAvailable() {
		t.Fatalf("persisted state = %#v", persisted)
	}

	if err := monitor.Dismiss("1.0.0.1458"); err != nil {
		t.Fatal(err)
	}
	persisted, err = ReadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ShouldPrompt() || persisted.DismissedVersion != "1.0.0.1458" {
		t.Fatalf("dismissed state = %#v", persisted)
	}

	latestVersion = "1.0.0.1459"
	state, err = monitor.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.ShouldPrompt() {
		t.Fatalf("new release should prompt after previous dismissal: %#v", state)
	}
}

func TestMonitorDoesNotReuseStateFromAnotherReleaseSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-state.json")
	first := newTestMonitor(t, MonitorOptions{
		CurrentVersion: "1.0.0.1457",
		Source:         "https://example.test/first",
		StatePath:      path,
		FetchLatest: func(context.Context) (string, error) {
			return "1.0.0.1458", nil
		},
	})
	if _, err := first.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := newTestMonitor(t, MonitorOptions{
		CurrentVersion: "1.0.0.1457",
		Source:         "https://example.test/second",
		StatePath:      path,
	})
	if state := second.Snapshot(); state.LatestVersion != "" || state.UpdateAvailable() {
		t.Fatalf("state from another source was reused: %#v", state)
	}
	persisted, err := ReadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ReleaseSourceHash == first.Snapshot().ReleaseSourceHash || persisted.LatestVersion != "" {
		t.Fatalf("state from another source remained on disk: %#v", persisted)
	}
}

func TestNewMonitorPersistsRecomputedCurrentVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-state.json")
	source := "https://example.test/latest"
	first := newTestMonitor(t, MonitorOptions{
		CurrentVersion: "1.0.0.1457",
		Source:         source,
		StatePath:      path,
		FetchLatest: func(context.Context) (string, error) {
			return "1.0.0.1458", nil
		},
	})
	if _, err := first.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := newTestMonitor(t, MonitorOptions{
		CurrentVersion: "1.0.0.1458",
		Source:         source,
		StatePath:      path,
	})
	if state := second.Snapshot(); state.Comparison != ComparisonSame {
		t.Fatalf("recomputed state = %#v", state)
	}
	persisted, err := ReadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.CurrentVersion != "1.0.0.1458" || persisted.Comparison != ComparisonSame {
		t.Fatalf("persisted recomputed state = %#v", persisted)
	}
}

func newTestMonitor(t *testing.T, options MonitorOptions) *Monitor {
	t.Helper()
	monitor, err := NewMonitor(options)
	if err != nil {
		t.Fatal(err)
	}
	return monitor
}
