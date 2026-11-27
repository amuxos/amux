package bridge

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type delayedManagementUpdate struct {
	*fakeManagementSystemActions
	started chan struct{}
	release chan struct{}
	err     error
}

func (a *delayedManagementUpdate) CheckUpdate(ctx context.Context) (ManagementUpdateInfo, error) {
	close(a.started)
	select {
	case <-a.release:
		return ManagementUpdateInfo{CurrentVersion: "v1.0.0", LatestVersion: "v9.9.9", UpdateAvailable: true}, a.err
	case <-ctx.Done():
		return ManagementUpdateInfo{}, ctx.Err()
	}
}

func TestManagementUpdateCheckCannotOverwriteNewerView(t *testing.T) {
	for _, failure := range []bool{false, true} {
		name := "success"
		if failure {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			b, _ := newBridgeForInputTest(t)
			b.secrets = &Secrets{OpenId: "owner-open"}
			a := &delayedManagementUpdate{fakeManagementSystemActions: &fakeManagementSystemActions{}, started: make(chan struct{}), release: make(chan struct{})}
			if failure {
				a.err = errors.New("update unavailable")
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(a.release) }) }
			t.Cleanup(release)
			b.SetManagementSystemActions(a)
			cards := make(chan string, 8)
			b.managementCardPatch = func(_ context.Context, _ string, card string) error { cards <- card; return nil }
			_, _, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionSystemUpdate, nil))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-a.started:
			case <-time.After(2 * time.Second):
				t.Fatal("check did not start")
			}
			if card := <-cards; !strings.Contains(card, "正在检查更新") {
				t.Fatal("expected checking card")
			}
			_, _, err = b.handleManagementCardAction(context.Background(), settingsCallback(managementActionStatusOpen, nil))
			if err != nil {
				t.Fatal(err)
			}
			if card := waitForManagementCard(t, cards, func(s string) bool { return strings.Contains(s, "助手状态") }); card == "" {
				t.Fatal("overview did not arrive")
			}
			release()
			deadline := time.Now().Add(2 * time.Second)
			for {
				b.maintenanceMu.Lock()
				done := b.maintenanceActive == ""
				b.maintenanceMu.Unlock()
				if done {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("check did not finish")
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case <-cards:
				t.Fatal("late update check replaced the newer overview")
			default:
			}
		})
	}
}

func TestManagementCardSuppressesStaleFallback(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	version := b.advanceModelServiceEditor("card")
	b.managementCardPatch = func(context.Context, string, string) error {
		b.advanceModelServiceEditor("card") // Navigation arrives while PATCH is in flight.
		return errors.New("expired card")
	}
	sent := false
	b.dmInteractive = func(context.Context, string, string) (string, string, error) { sent = true; return "new", "", nil }
	b.writeManagementCardFromCallbackIf(context.Background(), "owner", "card", "{}", func() bool { return b.modelServiceEditorCurrent("card", version) })
	if sent {
		t.Fatal("obsolete result was sent as a replacement DM")
	}
}

func TestManagementCardQueuedWriteRejectsOldVersion(t *testing.T) {
	t.Parallel()
	b := &Bridge{}
	writes := 0
	b.managementCardPatch = func(context.Context, string, string) error { writes++; return nil }
	old := b.advanceModelServiceEditor("card")
	unlock := b.lockRoute("management-card\x00card")
	done := make(chan struct{})
	go func() { b.writeManagementCardAtVersion(context.Background(), "owner", "card", "old", old); close(done) }()
	latest := b.advanceModelServiceEditor("card")
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queued writer stuck")
	}
	if writes != 0 {
		t.Fatal("obsolete queued card was delivered")
	}
	b.writeManagementCardAtVersion(context.Background(), "owner", "card", "latest", latest)
	if writes != 1 {
		t.Fatal("current card was dropped")
	}
}

func TestManagementUpdateCheckRepeatedClickKeepsResult(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	a := &delayedManagementUpdate{fakeManagementSystemActions: &fakeManagementSystemActions{}, started: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(a.release) }) }
	t.Cleanup(release)
	b.SetManagementSystemActions(a)
	cards := make(chan string, 8)
	b.managementCardPatch = func(_ context.Context, _ string, card string) error { cards <- card; return nil }
	_, _, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionSystemUpdate, nil))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.started:
	case <-time.After(time.Second):
		t.Fatal("check did not start")
	}
	resp, _, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionSystemUpdate, nil))
	if err != nil || resp == nil || resp.Toast.Type != "warning" {
		t.Fatal("duplicate check was not rejected")
	}
	release()
	if card := waitForManagementCard(t, cards, func(card string) bool { return strings.Contains(card, "v9.9.9") }); card == "" {
		t.Fatal("busy retry invalidated the in-flight result without showing another view")
	}
}
