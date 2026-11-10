package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentprofile"
	"github.com/amuxos/amux/internal/config"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

type fakeManagementSystemActions struct {
	restartCalls  int
	restartErr    error
	restartCalled chan struct{}
	updateCalls   int
	updateResult  ManagementUpdateInfo
	updateErr     error
	dismissCalls  int
	dismissTarget string
	dismissErr    error
	upgradeCalls  int
	upgradeTarget string
	upgradeErr    error
	upgradeCalled chan struct{}
}

type fakeManagementAgentPackActions struct {
	snapshot              agentprofile.PackCatalogSnapshot
	snapshotAfterMutation *agentprofile.PackCatalogSnapshot
	snapshotErr           error
	snapshotCalls         int
	installCalls          []string
	installErr            error
	syncCalls             []string
	syncErr               error
	removeCalls           []string
	removeErr             error
	stableCalls           int
	stableErr             error
}

type blockingManagementAgentPackActions struct {
	*fakeManagementAgentPackActions
	snapshotStarted chan struct{}
	snapshotRelease chan struct{}
}

type blockingManagementPackMutationActions struct {
	*fakeManagementAgentPackActions
	started chan string
	release chan struct{}
}

type sequencedManagementSnapshot struct {
	name     string
	snapshot agentprofile.PackCatalogSnapshot
	release  <-chan struct{}
}

type sequencedManagementAgentPackActions struct {
	*fakeManagementAgentPackActions
	responses chan sequencedManagementSnapshot
	started   chan string
}

func (f *sequencedManagementAgentPackActions) Snapshot() (agentprofile.PackCatalogSnapshot, error) {
	response := <-f.responses
	f.started <- response.name
	<-response.release
	return response.snapshot, nil
}

func (f *blockingManagementPackMutationActions) SyncIfCurrent(_ context.Context, namespace, _ string) (agentprofile.Pack, error) {
	f.started <- namespace
	<-f.release
	f.syncCalls = append(f.syncCalls, namespace)
	return agentprofile.Pack{Namespace: namespace, CurrentRevision: "revision"}, nil
}

func (f *blockingManagementAgentPackActions) Snapshot() (agentprofile.PackCatalogSnapshot, error) {
	close(f.snapshotStarted)
	<-f.snapshotRelease
	return f.fakeManagementAgentPackActions.Snapshot()
}

func (f *fakeManagementAgentPackActions) Snapshot() (agentprofile.PackCatalogSnapshot, error) {
	f.snapshotCalls++
	return f.snapshot, f.snapshotErr
}

func (f *fakeManagementAgentPackActions) Install(_ context.Context, id string) (agentprofile.Pack, error) {
	f.installCalls = append(f.installCalls, id)
	f.applySnapshotAfterMutation()
	return agentprofile.Pack{Namespace: id, CurrentRevision: "revision"}, f.installErr
}

func (f *fakeManagementAgentPackActions) InstallIfCurrent(ctx context.Context, id, _ string) (agentprofile.Pack, error) {
	return f.Install(ctx, id)
}

func (f *fakeManagementAgentPackActions) Sync(_ context.Context, namespace string) (agentprofile.Pack, error) {
	f.syncCalls = append(f.syncCalls, namespace)
	f.applySnapshotAfterMutation()
	return agentprofile.Pack{Namespace: namespace, CurrentRevision: "revision"}, f.syncErr
}

func (f *fakeManagementAgentPackActions) SyncIfCurrent(ctx context.Context, namespace, _ string) (agentprofile.Pack, error) {
	return f.Sync(ctx, namespace)
}

func (f *fakeManagementAgentPackActions) Remove(namespace string) (agentprofile.RemovePackResult, error) {
	f.removeCalls = append(f.removeCalls, namespace)
	f.applySnapshotAfterMutation()
	return agentprofile.RemovePackResult{Pack: agentprofile.Pack{Namespace: namespace}}, f.removeErr
}

func (f *fakeManagementAgentPackActions) applySnapshotAfterMutation() {
	if f.snapshotAfterMutation == nil {
		return
	}
	f.snapshot = *f.snapshotAfterMutation
	f.snapshotAfterMutation = nil
}

func (f *fakeManagementAgentPackActions) RemoveIfUnreferenced(namespace string, references func() ([]string, error)) (agentprofile.RemovePackResult, []string, error) {
	usedBy, err := references()
	if err != nil || len(usedBy) > 0 {
		return agentprofile.RemovePackResult{}, usedBy, err
	}
	result, err := f.Remove(namespace)
	return result, nil, err
}

func (f *fakeManagementAgentPackActions) RemoveIfCurrentAndUnreferenced(namespace, _ string, references func() ([]string, error)) (agentprofile.RemovePackResult, []string, error) {
	return f.RemoveIfUnreferenced(namespace, references)
}

func (f *fakeManagementAgentPackActions) WithRegistryStable(commit func() error) error {
	f.stableCalls++
	if f.stableErr != nil {
		return f.stableErr
	}
	return commit()
}

func (f *fakeManagementAgentPackActions) WithRegistryStableContext(ctx context.Context, commit func() error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return f.WithRegistryStable(commit)
}

func TestManagementHelpActionRequiresOwner(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	b.managementCardPatch = func(_ context.Context, _ string, _ string) error {
		t.Fatal("non-owner help action must not patch an owner management card")
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "other-open"},
			Context:  &callback.Context{OpenMessageID: "om-help"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey: managementActionHelpOpen,
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled {
		t.Fatal("management help action was not handled")
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "error" {
		t.Fatalf("response = %#v, want owner permission error", resp)
	}
}

func (f *fakeManagementSystemActions) Restart(context.Context) error {
	f.restartCalls++
	if f.restartCalled != nil {
		f.restartCalled <- struct{}{}
	}
	return f.restartErr
}

func (f *fakeManagementSystemActions) CheckUpdate(context.Context) (ManagementUpdateInfo, error) {
	f.updateCalls++
	return f.updateResult, f.updateErr
}

func (f *fakeManagementSystemActions) UpdateSnapshot() ManagementUpdateInfo {
	return f.updateResult
}

func (f *fakeManagementSystemActions) DismissUpdate(version string) error {
	f.dismissCalls++
	f.dismissTarget = version
	if f.dismissErr != nil {
		return f.dismissErr
	}
	f.updateResult.ShouldPrompt = false
	return nil
}

func (f *fakeManagementSystemActions) Upgrade(_ context.Context, expectedVersion string) error {
	f.upgradeCalls++
	f.upgradeTarget = expectedVersion
	if f.upgradeCalled != nil {
		f.upgradeCalled <- struct{}{}
	}
	return f.upgradeErr
}

func TestManagementCardActionDirectlyOpensSettingsEditCard(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, messageID, content string) error {
		patched <- messageID + ":" + content
		return nil
	}
	b.dmInteractive = func(_ context.Context, openID, content string) (string, string, error) {
		t.Fatalf("unexpected fallback DM to %s: %s", openID, content)
		return "", "", nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-settings"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey: managementActionSettingsOpen,
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled {
		t.Fatal("management action was not handled")
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, want success toast", resp)
	}
	if resp.Card != nil {
		t.Fatalf("response card = %#v, want async DM card instead of callback replacement", resp.Card)
	}
	if !strings.Contains(resp.Toast.Content, "助手设置") {
		t.Fatalf("toast = %q, want settings hint", resp.Toast.Content)
	}
	select {
	case card := <-patched:
		for _, want := range []string{
			"om-settings:",
			"修改默认配置",
			`"tag":"form"`,
			`"name":"interaction"`,
			managementActionSettingsSave,
		} {
			if !strings.Contains(card, want) {
				t.Fatalf("patched card = %s, want direct edit field %q", card, want)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async settings edit card")
	}
}

func TestManagementCardActionOpensAgentPackLibrary(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &fakeManagementAgentPackActions{snapshot: agentprofile.PackCatalogSnapshot{Market: []agentprofile.CatalogPackState{{
		CatalogPack: agentprofile.CatalogPack{ID: "expert-agents", Namespace: "expert-agents", DisplayName: "Expert Agents"},
	}}}}
	b.SetManagementAgentPackActions(actions)
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-pack-library"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey: managementActionPackLibraryOpen,
			}},
		},
	})
	if err != nil || !handled {
		t.Fatalf("handleManagementCardAction = %#v, %v, %v", resp, handled, err)
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, want success toast", resp)
	}
	select {
	case card := <-patched:
		if !strings.Contains(card, "专家库") || !strings.Contains(card, "Expert Agents") {
			t.Fatalf("patched card = %s, want expert library", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Pack library card")
	}
	if actions.snapshotCalls != 1 {
		t.Fatalf("Snapshot calls = %d, want 1", actions.snapshotCalls)
	}
}

func TestManagementCardActionOpensAgentPackLibraryWithoutWaitingForSnapshot(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &blockingManagementAgentPackActions{
		fakeManagementAgentPackActions: &fakeManagementAgentPackActions{
			snapshot: agentprofile.PackCatalogSnapshot{Market: []agentprofile.CatalogPackState{{
				CatalogPack: agentprofile.CatalogPack{
					ID: "expert-agents", Namespace: "expert-agents", DisplayName: "Expert Agents",
				},
			}}},
		},
		snapshotStarted: make(chan struct{}),
		snapshotRelease: make(chan struct{}),
	}
	b.SetManagementAgentPackActions(actions)
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	type actionResult struct {
		resp    *callback.CardActionTriggerResponse
		handled bool
		err     error
	}
	result := make(chan actionResult, 1)
	go func() {
		resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
			Event: &callback.CardActionTriggerRequest{
				Operator: &callback.Operator{OpenID: "owner-open"},
				Context:  &callback.Context{OpenMessageID: "om-pack-library"},
				Action: &callback.CallBackAction{Value: map[string]interface{}{
					managementActionKey: managementActionPackLibraryOpen,
				}},
			},
		})
		result <- actionResult{resp: resp, handled: handled, err: err}
	}()

	select {
	case <-actions.snapshotStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Pack snapshot to start")
	}
	select {
	case got := <-result:
		if got.err != nil || !got.handled || got.resp == nil || got.resp.Toast == nil || got.resp.Toast.Type != "success" {
			t.Fatalf("handleManagementCardAction = %#v, %v, %v, want immediate success toast", got.resp, got.handled, got.err)
		}
	case <-time.After(100 * time.Millisecond):
		close(actions.snapshotRelease)
		<-result
		t.Fatal("Pack library callback waited for snapshot and can exceed Feishu's response deadline")
	}

	close(actions.snapshotRelease)
	select {
	case card := <-patched:
		if !strings.Contains(card, "专家库") || !strings.Contains(card, "Expert Agents") {
			t.Fatalf("patched card = %s, want expert library", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Pack library card after snapshot completed")
	}
}

func TestManagementCardActionNavigatesAgentPackLibrarySection(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &fakeManagementAgentPackActions{snapshot: agentprofile.PackCatalogSnapshot{
		Builtins: []agentprofile.BuiltinExpertState{{Ref: "builtin/codex", Name: "builtin-only"}},
		Loaded:   []agentprofile.LoadedPackState{{Namespace: "loaded", DisplayName: "loaded-only"}},
		Market: []agentprofile.CatalogPackState{{
			CatalogPack: agentprofile.CatalogPack{ID: "market", Namespace: "market", DisplayName: "market-only"},
		}},
	}}
	b.SetManagementAgentPackActions(actions)
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-pack-library"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey: managementActionPackLibraryNavigate,
				"section":           "market",
				"page":              "0",
			}},
		},
	})
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("navigate callback = %#v, %v, %v, want immediate success", resp, handled, err)
	}
	select {
	case card := <-patched:
		if !strings.Contains(card, "market-only") {
			t.Fatalf("market section card = %s, want market item", card)
		}
		for _, hidden := range []string{"builtin-only", "loaded-only"} {
			if strings.Contains(card, hidden) {
				t.Fatalf("market section card = %s, should hide %q", card, hidden)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for navigated Pack library card")
	}
}

func TestManagementCardActionOpensFocusedLoadedExpertLibraryManagement(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &fakeManagementAgentPackActions{snapshot: agentprofile.PackCatalogSnapshot{
		Loaded: []agentprofile.LoadedPackState{{
			Namespace:       "expert-agents",
			DisplayName:     "Expert Agents",
			CurrentRevision: "1234567890abcdef",
			ProfileCount:    12,
			Generation:      "generation-1",
		}},
	}}
	b.SetManagementAgentPackActions(actions)
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-pack-library"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:  managementActionPackManage,
				managementSurfaceKey: string(managementSurfaceAmux),
				"namespace":          "expert-agents",
				"section":            string(managementPackLibrarySectionLoaded),
				"page":               "1",
			}},
		},
	})
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("handleManagementCardAction = %#v, %v, %v, want success", resp, handled, err)
	}
	select {
	case card := <-patched:
		for _, want := range []string{"Expert Agents", "12 个专家", "版本 1234567", "检查更新", "移除", "返回专家库", "返回概览"} {
			if !strings.Contains(card, want) {
				t.Fatalf("loaded expert library management card = %s, want %q", card, want)
			}
		}
		if strings.Contains(card, "选择专家") {
			t.Fatalf("loaded expert library management card = %s, should focus on library maintenance", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for loaded expert library management card")
	}
}

func TestManagementCardNavigationKeepsLatestRequestedView(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	actions := &sequencedManagementAgentPackActions{
		fakeManagementAgentPackActions: &fakeManagementAgentPackActions{},
		responses:                      make(chan sequencedManagementSnapshot, 2),
		started:                        make(chan string, 2),
	}
	actions.responses <- sequencedManagementSnapshot{
		name:     "builtins",
		snapshot: agentprofile.PackCatalogSnapshot{Builtins: []agentprofile.BuiltinExpertState{{Ref: "builtin/old", Name: "old-view"}}},
		release:  firstRelease,
	}
	actions.responses <- sequencedManagementSnapshot{
		name: "market",
		snapshot: agentprofile.PackCatalogSnapshot{Market: []agentprofile.CatalogPackState{{
			CatalogPack: agentprofile.CatalogPack{ID: "new", Namespace: "new", DisplayName: "new-view"},
		}}},
		release: secondRelease,
	}
	b.SetManagementAgentPackActions(actions)
	patched := make(chan string, 2)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}
	navigate := func(section string) {
		t.Helper()
		resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
			Event: &callback.CardActionTriggerRequest{
				Operator: &callback.Operator{OpenID: "owner-open"},
				Context:  &callback.Context{OpenMessageID: "om-pack-library"},
				Action: &callback.CallBackAction{Value: map[string]interface{}{
					managementActionKey: managementActionPackLibraryNavigate,
					"section":           section,
					"page":              "0",
				}},
			},
		})
		if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
			t.Fatalf("navigate %s = %#v, %v, %v", section, resp, handled, err)
		}
	}

	navigate("builtins")
	if started := <-actions.started; started != "builtins" {
		t.Fatalf("first snapshot = %q, want builtins", started)
	}
	navigate("market")
	if started := <-actions.started; started != "market" {
		t.Fatalf("second snapshot = %q, want market", started)
	}
	close(secondRelease)
	select {
	case card := <-patched:
		if !strings.Contains(card, "new-view") {
			t.Fatalf("latest navigation card = %s, want market view", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for latest navigation card")
	}
	close(firstRelease)
	select {
	case card := <-patched:
		t.Fatalf("stale navigation overwrote latest view: %s", card)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestManagementCardActionsInstallAndSyncCatalogPack(t *testing.T) {
	for _, tt := range []struct {
		name       string
		action     string
		values     map[string]interface{}
		wantNotice string
		wantCall   func(*fakeManagementAgentPackActions) []string
	}{
		{
			name:       "install",
			action:     managementActionPackInstall,
			values:     map[string]interface{}{"catalog_id": "expert-agents"},
			wantNotice: "已添加 Expert Agents，并已开启自动更新",
			wantCall:   func(actions *fakeManagementAgentPackActions) []string { return actions.installCalls },
		},
		{
			name:       "sync",
			action:     managementActionPackSync,
			values:     map[string]interface{}{"namespace": "expert-agents"},
			wantNotice: "Expert Agents 更新完成",
			wantCall:   func(actions *fakeManagementAgentPackActions) []string { return actions.syncCalls },
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b, _ := newBridgeForInputTest(t)
			b.secrets = &Secrets{OpenId: "owner-open"}
			actions := &fakeManagementAgentPackActions{snapshot: agentprofile.PackCatalogSnapshot{Loaded: []agentprofile.LoadedPackState{{
				Namespace:   "expert-agents",
				DisplayName: "Expert Agents",
				AutoSync:    true,
			}}}}
			b.SetManagementAgentPackActions(actions)
			patched := make(chan string, 4)
			b.managementCardPatch = func(_ context.Context, _ string, content string) error {
				patched <- content
				return nil
			}
			values := map[string]interface{}{managementActionKey: tt.action, "operation_nonce": "nonce-" + tt.name, "expected_generation": "generation-1"}
			for key, value := range tt.values {
				values[key] = value
			}

			resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
				Event: &callback.CardActionTriggerRequest{
					Operator: &callback.Operator{OpenID: "owner-open"},
					Context:  &callback.Context{OpenMessageID: "om-pack-action"},
					Action:   &callback.CallBackAction{Value: values},
				},
			})
			if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
				t.Fatalf("handleManagementCardAction = %#v, %v, %v", resp, handled, err)
			}
			deadline := time.After(2 * time.Second)
			for {
				select {
				case card := <-patched:
					if strings.Contains(card, tt.wantNotice) {
						calls := tt.wantCall(actions)
						if len(calls) != 1 || calls[0] != "expert-agents" {
							t.Fatalf("operation calls = %#v, want expert-agents", calls)
						}
						return
					}
				case <-deadline:
					t.Fatalf("timed out waiting for %q", tt.wantNotice)
				}
			}
		})
	}
}

func TestManagementPackOperationReturnsToOriginatingLibraryView(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	afterInstall := agentprofile.PackCatalogSnapshot{
		Loaded: []agentprofile.LoadedPackState{{Namespace: "installed", DisplayName: "installed-only"}},
		Market: []agentprofile.CatalogPackState{{
			CatalogPack: agentprofile.CatalogPack{ID: "remaining", Namespace: "remaining", DisplayName: "remaining-market"},
		}},
	}
	actions := &fakeManagementAgentPackActions{
		snapshot: agentprofile.PackCatalogSnapshot{Market: []agentprofile.CatalogPackState{{
			CatalogPack: agentprofile.CatalogPack{ID: "install-me", Namespace: "install-me", DisplayName: "install-me"},
		}}},
		snapshotAfterMutation: &afterInstall,
	}
	b.SetManagementAgentPackActions(actions)
	patched := make(chan string, 4)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-pack-action"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:   managementActionPackInstall,
				"catalog_id":          "install-me",
				"operation_nonce":     "nonce-install-view",
				"expected_generation": "generation-1",
				"section":             "market",
				"page":                "0",
			}},
		},
	})
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("install callback = %#v, %v, %v", resp, handled, err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case card := <-patched:
			if !strings.Contains(card, "已添加 install-me") {
				continue
			}
			if !strings.Contains(card, "remaining-market") {
				t.Fatalf("final market card = %s, want remaining market item", card)
			}
			if strings.Contains(card, "installed-only") {
				t.Fatalf("final market card = %s, should not jump to loaded section", card)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for final market card")
		}
	}
}

func TestManagementPackOperationFallsBackToMinimalResultCard(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &fakeManagementAgentPackActions{snapshot: agentprofile.PackCatalogSnapshot{Loaded: []agentprofile.LoadedPackState{{
		Namespace:   "expert-agents",
		DisplayName: "Expert Agents",
	}}}}
	b.SetManagementAgentPackActions(actions)
	fallbackPatched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		switch {
		case strings.Contains(content, "正在添加专家库"):
			return nil
		case strings.Contains(content, "查看专家库") && strings.Contains(content, "已添加 Expert Agents"):
			fallbackPatched <- content
			return nil
		default:
			return errors.New("element exceeds the limit")
		}
	}
	dmSent := make(chan string, 1)
	b.dmInteractive = func(_ context.Context, _ string, content string) (string, string, error) {
		dmSent <- content
		return "om-fallback", "", nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-pack-action"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:   managementActionPackInstall,
				"catalog_id":          "expert-agents",
				"operation_nonce":     "nonce-install-fallback",
				"expected_generation": "generation-1",
			}},
		},
	})
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("install callback = %#v, %v, %v", resp, handled, err)
	}
	select {
	case card := <-fallbackPatched:
		if count := countCardJSONTaggedNodes(t, card); count >= managementPackLibraryMaxElements {
			t.Fatalf("fallback result card elements = %d, want a minimal card", count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for minimal operation result fallback")
	}
	select {
	case content := <-dmSent:
		t.Fatalf("operation result was sent as a new DM instead of replacing the progress card: %s", content)
	default:
	}
}

func TestManagementPackOperationReportsSuccessWhenRefreshSnapshotFails(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &fakeManagementAgentPackActions{
		snapshotErr: errors.New("registry snapshot unavailable"),
	}
	b.SetManagementAgentPackActions(actions)
	patched := make(chan string, 4)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-pack-action"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:   managementActionPackInstall,
				"catalog_id":          "expert-agents",
				"operation_nonce":     "nonce-install-snapshot-failure",
				"expected_generation": "generation-1",
			}},
		},
	})
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("install callback = %#v, %v, %v", resp, handled, err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case card := <-patched:
			if strings.Contains(card, "正在添加专家库") {
				continue
			}
			if !strings.Contains(card, "已添加 expert-agents") || !strings.Contains(card, "查看专家库") {
				t.Fatalf("snapshot failure result card = %s, want truthful minimal success result", card)
			}
			if strings.Contains(card, "本次未做任何更改") {
				t.Fatalf("snapshot failure result card = %s, should not deny a completed mutation", card)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for snapshot-failure result card")
		}
	}
}

func TestManagementCardActionDeduplicatesPackOperationNonce(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &fakeManagementAgentPackActions{snapshot: agentprofile.PackCatalogSnapshot{Loaded: []agentprofile.LoadedPackState{{
		Namespace:   "expert-agents",
		DisplayName: "Expert Agents",
	}}}}
	b.SetManagementAgentPackActions(actions)
	b.managementCardPatch = func(context.Context, string, string) error { return nil }
	event := &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "owner-open"},
		Context:  &callback.Context{OpenMessageID: "om-pack-action"},
		Action: &callback.CallBackAction{Value: map[string]interface{}{
			managementActionKey:   managementActionPackSync,
			"namespace":           "expert-agents",
			"operation_nonce":     "nonce-1",
			"expected_generation": "generation-1",
		}},
	}}

	first, _, err := b.handleManagementCardAction(context.Background(), event)
	if err != nil || first == nil || first.Toast == nil || first.Toast.Type != "success" {
		t.Fatalf("first callback = %#v, %v", first, err)
	}
	// Wait through the operation's synchronized completion state before reading
	// the fake's calls or simulating restart; polling its slice races with Sync.
	deadline := time.Now().Add(2 * time.Second)
	for b.managementPackOperationActive("owner-open", "om-pack-action") {
		if time.Now().After(deadline) {
			t.Fatal("pack sync did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if len(actions.syncCalls) != 1 {
		t.Fatalf("sync calls = %#v, want exactly one", actions.syncCalls)
	}
	// Simulate amuxd restart: the in-memory confirmation map is gone, while the
	// bridge DB and the old Feishu card survive.
	b.maintenanceMu.Lock()
	b.maintenanceConfirmations = nil
	b.maintenanceMu.Unlock()
	second, _, err := b.handleManagementCardAction(context.Background(), event)
	if err != nil || second == nil || second.Toast == nil || second.Toast.Type != "warning" {
		t.Fatalf("duplicate callback after restart = %#v, %v, want warning", second, err)
	}
	if len(actions.syncCalls) != 1 {
		t.Fatalf("sync calls after restart = %#v, want exactly one", actions.syncCalls)
	}
}

func TestManagementCardAllowsOnlyOnePackMutationPerCard(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &blockingManagementPackMutationActions{
		fakeManagementAgentPackActions: &fakeManagementAgentPackActions{snapshot: agentprofile.PackCatalogSnapshot{
			Loaded: []agentprofile.LoadedPackState{
				{Namespace: "one", DisplayName: "one"},
				{Namespace: "two", DisplayName: "two"},
			},
		}},
		started: make(chan string, 2),
		release: make(chan struct{}),
	}
	b.SetManagementAgentPackActions(actions)
	b.managementCardPatch = func(context.Context, string, string) error { return nil }

	event := func(namespace, nonce string) *callback.CardActionTriggerEvent {
		return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-pack-action"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:   managementActionPackSync,
				"namespace":           namespace,
				"operation_nonce":     nonce,
				"expected_generation": "generation-1",
			}},
		}}
	}

	first, _, err := b.handleManagementCardAction(context.Background(), event("one", "nonce-one"))
	if err != nil || first == nil || first.Toast == nil || first.Toast.Type != "success" {
		t.Fatalf("first mutation callback = %#v, %v", first, err)
	}
	select {
	case namespace := <-actions.started:
		if namespace != "one" {
			t.Fatalf("started mutation = %q, want one", namespace)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first mutation")
	}

	second, _, err := b.handleManagementCardAction(context.Background(), event("two", "nonce-two"))
	if err != nil || second == nil || second.Toast == nil || second.Toast.Type != "warning" ||
		!strings.Contains(second.Toast.Content, "已有专家库操作") {
		t.Fatalf("second mutation callback = %#v, %v, want active-operation warning", second, err)
	}
	select {
	case namespace := <-actions.started:
		t.Fatalf("concurrent mutation %q started while first was active", namespace)
	case <-time.After(100 * time.Millisecond):
	}
	close(actions.release)
}

func TestNonCardProfileMutationPathsUseRegistryStableBoundary(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &fakeManagementAgentPackActions{}
	b.SetManagementAgentPackActions(actions)
	b.dmNotice = func(context.Context, string, string) (string, string, error) { return "", "", nil }

	if err := b.cmdSetProfileDefaultAgent(context.Background(), "owner-open", "builtin/codex"); err != nil {
		t.Fatalf("cmdSetProfileDefaultAgent: %v", err)
	}
	if actions.stableCalls != 1 {
		t.Fatalf("stable calls after default Profile command = %d, want 1", actions.stableCalls)
	}
	if err := b.BindBotSession(BotSession{BridgeId: "feishu:other"}, "owner-open"); err == nil {
		t.Fatal("BindBotSession with wrong bridge id succeeded")
	}
	if actions.stableCalls != 2 {
		t.Fatalf("stable calls after BindBotSession = %d, want 2", actions.stableCalls)
	}
}

func TestManagementCardActionBlocksRemovingPackUsedByAnotherBot(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	other, err := b.db.GetBotProfileState("feishu:other-bot", config.FeishuInteractionThread)
	if err != nil {
		t.Fatalf("GetBotProfileState(other): %v", err)
	}
	if _, err := b.db.CommitThreadBotSettings("feishu:other-bot", other.Version, "expert-agents/reviewer"); err != nil {
		t.Fatalf("CommitThreadBotSettings(other): %v", err)
	}
	actions := &fakeManagementAgentPackActions{snapshot: agentprofile.PackCatalogSnapshot{Loaded: []agentprofile.LoadedPackState{{
		Namespace:   "expert-agents",
		DisplayName: "Expert Agents",
	}}}}
	b.SetManagementAgentPackActions(actions)
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-pack-remove"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:   managementActionPackRemove,
				"namespace":           "expert-agents",
				"operation_nonce":     "nonce-remove-blocked",
				"expected_generation": "generation-1",
			}},
		},
	})
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Fatalf("handleManagementCardAction = %#v, %v, %v", resp, handled, err)
	}
	select {
	case card := <-patched:
		if !strings.Contains(card, "1 个飞书助手") || !strings.Contains(card, "请先更换后再移除") {
			t.Fatalf("blocked remove card = %s, want assistant reference guidance", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked remove card")
	}
	if len(actions.removeCalls) != 0 {
		t.Fatalf("Remove calls = %#v, want none while referenced", actions.removeCalls)
	}
}

func TestBlockedPackRemovalDoesNotWaitForCatalogSnapshot(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	other, err := b.db.GetBotProfileState("feishu:other-bot", config.FeishuInteractionThread)
	if err != nil {
		t.Fatalf("GetBotProfileState(other): %v", err)
	}
	if _, err := b.db.CommitThreadBotSettings("feishu:other-bot", other.Version, "expert-agents/reviewer"); err != nil {
		t.Fatalf("CommitThreadBotSettings(other): %v", err)
	}
	actions := &blockingManagementAgentPackActions{
		fakeManagementAgentPackActions: &fakeManagementAgentPackActions{
			snapshot: agentprofile.PackCatalogSnapshot{Loaded: []agentprofile.LoadedPackState{{
				Namespace: "expert-agents", DisplayName: "Expert Agents",
			}}},
		},
		snapshotStarted: make(chan struct{}),
		snapshotRelease: make(chan struct{}),
	}
	b.SetManagementAgentPackActions(actions)
	b.managementCardPatch = func(context.Context, string, string) error { return nil }

	type actionResult struct {
		resp    *callback.CardActionTriggerResponse
		handled bool
		err     error
	}
	result := make(chan actionResult, 1)
	go func() {
		resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
			Event: &callback.CardActionTriggerRequest{
				Operator: &callback.Operator{OpenID: "owner-open"},
				Context:  &callback.Context{OpenMessageID: "om-pack-remove"},
				Action: &callback.CallBackAction{Value: map[string]interface{}{
					managementActionKey:   managementActionPackRemove,
					"namespace":           "expert-agents",
					"operation_nonce":     "nonce-remove-blocked-async",
					"expected_generation": "generation-1",
				}},
			},
		})
		result <- actionResult{resp: resp, handled: handled, err: err}
	}()

	select {
	case <-actions.snapshotStarted:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocked-removal snapshot")
	}
	select {
	case got := <-result:
		if got.err != nil || !got.handled || got.resp == nil || got.resp.Toast == nil || got.resp.Toast.Type != "warning" {
			t.Fatalf("blocked removal callback = %#v, %v, %v", got.resp, got.handled, got.err)
		}
	case <-time.After(100 * time.Millisecond):
		close(actions.snapshotRelease)
		<-result
		t.Fatal("blocked removal callback waited for catalog snapshot")
	}
	close(actions.snapshotRelease)
}

func TestManagementCardActionRemovesUnusedCatalogPack(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	afterRemoval := agentprofile.PackCatalogSnapshot{Market: []agentprofile.CatalogPackState{{
		CatalogPack: agentprofile.CatalogPack{ID: "expert-agents", Namespace: "expert-agents", DisplayName: "expert-agents"},
	}}}
	actions := &fakeManagementAgentPackActions{
		snapshot: agentprofile.PackCatalogSnapshot{Loaded: []agentprofile.LoadedPackState{{
			Namespace: "team", DisplayName: "expert-agents",
		}}},
		snapshotAfterMutation: &afterRemoval,
	}
	b.SetManagementAgentPackActions(actions)
	patched := make(chan string, 4)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-pack-remove"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:   managementActionPackRemove,
				"namespace":           "team",
				"operation_nonce":     "nonce-remove-unused",
				"expected_generation": "generation-1",
			}},
		},
	})
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("handleManagementCardAction = %#v, %v, %v", resp, handled, err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case card := <-patched:
			if strings.Contains(card, "已移除 expert-agents") {
				if len(actions.removeCalls) != 1 || actions.removeCalls[0] != "team" {
					t.Fatalf("Remove calls = %#v, want team", actions.removeCalls)
				}
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for removed Pack card")
		}
	}
}

func TestManagementCardActionOpensSettingsEditCard(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	sent := make(chan string, 1)
	b.dmInteractive = func(_ context.Context, openID, content string) (string, string, error) {
		sent <- openID + ":" + content
		return "msg-1", "", nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey: managementActionSettingsEdit,
				"interaction":       "singleton",
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled {
		t.Fatal("management action was not handled")
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, want success toast", resp)
	}
	select {
	case card := <-sent:
		for _, want := range []string{
			"修改默认配置",
			`"tag":"form"`,
			`"tag":"select_static"`,
			`"name":"interaction"`,
			`"initial_option":"singleton"`,
			`"width":"fill"`,
			`"action_type":"form_submit"`,
			managementActionSettingsSave,
		} {
			if !strings.Contains(card, want) {
				t.Fatalf("async edit card = %s, want %q", card, want)
			}
		}
		if strings.Contains(card, `"name":"governance"`) || strings.Contains(card, "使用范围") {
			t.Fatalf("async edit card = %s, should not expose bot governance", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async settings edit card")
	}
}

func TestManagementCardActionSaveUnchangedInteraction(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	setCurrentBotProfile(t, b, "command-line/custom-profile")
	setCurrentBotRuntime(t, b, "codex")
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, messageID, content string) error {
		patched <- messageID + ":" + content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-settings"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey: managementActionSettingsSave,
			}, FormValue: map[string]interface{}{
				"interaction": "thread",
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled {
		t.Fatal("management action was not handled")
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, want success toast", resp)
	}
	if !strings.Contains(resp.Toast.Content, "已保存") {
		t.Fatalf("toast = %q, want saved hint", resp.Toast.Content)
	}
	select {
	case card := <-patched:
		if !strings.Contains(card, "每个话题独立") || strings.Contains(card, "使用范围") {
			t.Fatalf("patched card = %s, want interaction-only summary", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for saved settings summary")
	}
}

func TestManagementDecomposeInteraction(t *testing.T) {
	interaction, isol := managementDecomposeInteraction(interactionModeThreadIsolated)
	if interaction != config.FeishuInteractionThread || !isol {
		t.Fatalf("decompose isolated = (%q, %v), want (thread, true)", interaction, isol)
	}
	for _, plain := range []string{config.FeishuInteractionThread, config.FeishuInteractionSingleton} {
		interaction, isol = managementDecomposeInteraction(plain)
		if interaction != plain || isol {
			t.Fatalf("decompose %q = (%q, %v), want (%q, false)", plain, interaction, isol, plain)
		}
	}
}

func TestManagementExecutionDiffersBackfillsExplicitRuntime(t *testing.T) {
	state := managementState{Mode: config.FeishuInteractionSingleton, DefaultAgent: "builtin/codex"}
	draft := managementSettingsDraft{
		Interaction: config.FeishuInteractionSingleton, DefaultAgent: "builtin/codex", DefaultRuntimeRef: "codex",
	}
	if !managementExecutionDiffers(state, draft) {
		t.Fatal("legacy empty runtime must be persisted as an explicit execution selection")
	}
}

func countCardJSONTaggedNodes(t *testing.T, card string) int {
	t.Helper()
	var document any
	if err := json.Unmarshal([]byte(card), &document); err != nil {
		t.Fatalf("unmarshal card JSON: %v", err)
	}
	count := 0
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			if tag, ok := typed["tag"].(string); ok && strings.TrimSpace(tag) != "" {
				count++
			}
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(document)
	return count
}

func TestRenderAgentPackStatusCardsUseExpertLibraryLanguage(t *testing.T) {
	for name, card := range map[string]string{
		"installing": renderAgentPackOperationCard(managementActionPackInstall),
		"syncing":    renderAgentPackOperationCard(managementActionPackSync),
		"removing":   renderAgentPackOperationCard(managementActionPackRemove),
		"failure":    renderAgentPackLibraryFailureCard(),
	} {
		if !strings.Contains(card, "专家") && !strings.Contains(card, "更新") {
			t.Fatalf("%s card = %s, want task-oriented expert copy", name, card)
		}
		for _, jargon := range []string{"Agent Pack", "Catalog", "Profile", "manifest", "revision", "registry", "namespace"} {
			if strings.Contains(card, jargon) {
				t.Fatalf("%s card = %s, should hide implementation term %q", name, card, jargon)
			}
		}
	}

	for _, want := range []string{"未能添加 Expert Agents", "专家库没有变化"} {
		if notice := managementPackOperationFailureNotice(managementActionPackInstall, "Expert Agents"); !strings.Contains(managementDefaultText(notice), want) {
			t.Fatalf("install failure notice = %q, want %q", notice, want)
		}
	}
	if notice := managementPackOperationFailureNotice(managementActionPackSync, "Expert Agents"); managementDefaultText(notice) != "Expert Agents 更新失败，当前专家仍可继续使用。" {
		t.Fatalf("sync failure notice = %q", notice)
	}
}

func TestRenderSettingsEditCardKeepsLargeProfileCatalogWithinCardLimit(t *testing.T) {
	profiles := make([]managementProfileSummary, 0, 80)
	for i := 0; i < 80; i++ {
		ref := fmt.Sprintf("expert-agents/profile-%02d", i)
		profiles = append(profiles, managementProfileSummary{
			Ref:         ref,
			Name:        fmt.Sprintf("profile-%02d", i),
			Description: strings.Repeat("用于复杂研发任务的专家说明", 8),
		})
	}
	for _, mode := range []string{"thread", interactionModeThreadIsolated} {
		card := renderSettingsEditCard(
			managementState{Mode: mode, DefaultAgent: profiles[0].Ref, Profiles: profiles, DailyCleanup: "true"},
			managementSettingsDraft{Interaction: mode, DefaultAgent: profiles[0].Ref, DailyCleanup: "true"},
		)
		if len(card) >= maxFeishuCardBytes {
			t.Fatalf("%s settings card bytes = %d, want below Feishu card limit %d", mode, len(card), maxFeishuCardBytes)
		}
	}
}

func TestManagementProfilesKeepsHealthyPackWhenAnotherPackIsBroken(t *testing.T) {
	installBridgeTestProfilePack(t)
	registry, err := agentprofile.LoadPackRegistry(agentprofile.DefaultPackRegistryPath())
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	healthy := registry.Packs["team"]
	healthy.GitURL = "git@example.com:healthy/tools.git"
	registry.Packs["team"] = healthy
	registry.Packs["broken"] = agentprofile.Pack{
		Namespace:        "broken",
		SourceKind:       agentprofile.PackSourceGit,
		GitURL:           "git@example.com:broken/tools.git",
		MaterializedPath: t.TempDir(),
		CurrentRevision:  "broken123",
	}
	if err := agentprofile.SavePackRegistry(agentprofile.DefaultPackRegistryPath(), registry); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	profiles := managementSelectableProfiles(managementProfiles(), "")
	for _, profile := range profiles {
		if profile.Ref == "team/coder" {
			if profile.Description != "Team coding profile" {
				t.Fatalf("team/coder description = %q, want manifest description", profile.Description)
			}
			if profile.LibraryName != "tools" {
				t.Fatalf("team/coder library name = %q, want unavailable collision ignored", profile.LibraryName)
			}
			return
		}
	}
	t.Fatalf("profiles = %#v, want registered team/coder Profile", profiles)
}

func TestManagementProfilesLabelsManagedLocalProfile(t *testing.T) {
	installBridgeTestProfilePack(t)
	registry, err := agentprofile.LoadPackRegistry(agentprofile.DefaultPackRegistryPath())
	if err != nil {
		t.Fatalf("LoadPackRegistry: %v", err)
	}
	pack := registry.Packs["team"]
	delete(registry.Packs, "team")
	pack.Namespace = agentprofile.ManagedLocalPackNamespace
	pack.ManagedBy = agentprofile.ManagedPackOwnerAmux
	pack.DisplayName = "Local experts"
	registry.Packs[agentprofile.ManagedLocalPackNamespace] = pack
	manifestPath := filepath.Join(pack.MaterializedPath, "dist", "agent-pack.manifest.json")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	manifest = []byte(strings.Replace(string(manifest), `"pack_namespace": "team"`, `"pack_namespace": "local"`, 1))
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := agentprofile.SavePackRegistry(agentprofile.DefaultPackRegistryPath(), registry); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}

	for _, profile := range managementSelectableProfiles(managementProfiles(), "") {
		if profile.Ref == "local/coder" {
			if profile.LibraryName != "本地" {
				t.Fatalf("local/coder library = %q, want 本地", profile.LibraryName)
			}
			return
		}
	}
	t.Fatal("managed local profile is missing from the Feishu selector")
}

func TestManagementStatePreservesExactUnavailableCurrentProfile(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	setCurrentBotProfile(t, b, "command-line/custom-profile")

	state := b.managementState()
	if state.DefaultAgent != "command-line/custom-profile" {
		t.Fatalf("default agent = %q, want exact DB value", state.DefaultAgent)
	}
	if len(state.Profiles) < 2 || state.Profiles[0].Ref != "command-line/custom-profile" || !state.Profiles[0].CurrentOnly {
		t.Fatalf("profiles = %#v, want exact current-only projection", state.Profiles)
	}
}

func TestRenderSettingsEditCardExplainsMissingProfileConfiguration(t *testing.T) {
	card := renderSettingsEditCard(
		managementState{Mode: "thread"},
		managementSettingsDraft{Interaction: "thread"},
	)
	if strings.Contains(card, `"name":"default_agent"`) {
		t.Fatalf("settings card = %s, should not render an empty Profile selector", card)
	}
	if !strings.Contains(card, "专家库中暂无可用专家") {
		t.Fatalf("settings card = %s, want empty library hint", card)
	}
}

func TestManagementCardActionAllowsKeepingUnavailableCurrentProfile(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	setCurrentBotProfile(t, b, "command-line/custom-profile")
	setCurrentBotRuntime(t, b, "codex")

	resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(
		managementActionSettingsSave,
		map[string]interface{}{
			"interaction":   "thread",
			"default_agent": "command-line/custom-profile",
		},
	))
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v handled = %v, want current Profile preservation", resp, handled)
	}
	fresh, err := b.db.GetBotProfileState(b.currentBridgeId(), "thread")
	if err != nil {
		t.Fatalf("GetBotProfileState: %v", err)
	}
	if fresh.DefaultAgent != "command-line/custom-profile" {
		t.Fatalf("default agent = %q, want exact current Profile", fresh.DefaultAgent)
	}
}

func settingsCallback(action string, values map[string]interface{}) *callback.CardActionTriggerEvent {
	value := map[string]interface{}{managementActionKey: action}
	form := map[string]interface{}{}
	for key, item := range values {
		if action == managementActionSettingsSave {
			form[key] = item
		} else {
			value[key] = item
		}
	}
	return &callback.CardActionTriggerEvent{Event: &callback.CardActionTriggerRequest{
		Operator: &callback.Operator{OpenID: "owner-open"},
		Context:  &callback.Context{OpenMessageID: "om-settings"},
		Action:   &callback.CallBackAction{Value: value, FormValue: form},
	}}
}

func waitForManagementCard(t *testing.T, cards <-chan string, match func(string) bool) string {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case card := <-cards:
			if match(card) {
				return card
			}
		case <-timeout:
			return ""
		}
	}
}

func TestManagementCardActionRejectsNonOwner(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "member-open"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey: managementActionStatusOpen,
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled {
		t.Fatal("management action was not handled")
	}
	if resp == nil || resp.Toast == nil || resp.Toast.Type != "error" {
		t.Fatalf("response = %#v, want error toast", resp)
	}
}

func TestRenderManagementOverviewCardPromptsCachedUpdate(t *testing.T) {
	card := renderManagementOverviewCard(managementState{
		SystemStatus: managementSystemHealthy,
		Targets:      2,
		Update: ManagementUpdateInfo{
			CurrentVersion:  "1.0.0.1457",
			LatestVersion:   "1.0.0.1458",
			UpdateAvailable: true,
			ShouldPrompt:    true,
		},
	})
	for _, want := range []string{"发现 amux 新版本", "1.0.0.1458", "查看更新", managementActionSystemMaintenance} {
		if !strings.Contains(card, want) {
			t.Fatalf("overview update prompt = %s, want %q", card, want)
		}
	}
}

func TestRenderSystemMaintenanceCardPrioritizesRecoveryWhenAttention(t *testing.T) {
	card := renderSystemMaintenanceCardForSurface(
		managementState{SystemStatus: managementSystemAttention},
		managementSurfaceAmux,
	)
	card = renderManagementCardForSurface(card, managementSurfaceAmux, managementViewSystem)
	for _, want := range []string{"需要关注", "当前没有可用的运行环境", "重新检查", "重启 amux", "返回概览"} {
		if !strings.Contains(card, want) {
			t.Fatalf("attention system card = %s, want %q", card, want)
		}
	}
	if strings.Contains(card, managementActionSystemUpdate) {
		t.Fatalf("attention system card = %s, should defer update checks until recovery", card)
	}
	refresh := findManagementButtonByAction(t, card, managementActionSystemMaintenance)
	restart := findManagementButtonByAction(t, card, managementActionSystemRestartOK)
	if refresh.Type != "primary" || restart.Type != "default" || restart.Confirm == nil {
		t.Fatalf("attention system actions = refresh:%#v restart:%#v, want recovery-first hierarchy", refresh, restart)
	}
}

func TestManagementUptimeText(t *testing.T) {
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.Local)
	for name, tt := range map[string]struct {
		startedAt time.Time
		want      string
	}{
		"unknown":        {want: "未知"},
		"under a minute": {startedAt: now.Add(-30 * time.Second), want: "不到 1 分钟"},
		"minutes":        {startedAt: now.Add(-12 * time.Minute), want: "12 分钟"},
		"hours":          {startedAt: now.Add(-6*time.Hour - 20*time.Minute), want: "6 小时"},
		"days and hours": {startedAt: now.Add(-54 * time.Hour), want: "2 天 6 小时"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := managementDefaultText(managementUptimeText(tt.startedAt, now)); got != tt.want {
				t.Fatalf("managementUptimeText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestManagementRestartRequiresConfirmationBeforeExecution(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &fakeManagementSystemActions{restartCalled: make(chan struct{}, 1)}
	b.SetManagementSystemActions(actions)
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionSystemRestart, nil))
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Fatalf("response = %#v, handled = %v, want warning toast", resp, handled)
	}
	select {
	case card := <-patched:
		for _, want := range []string{"确认重启", managementActionSystemRestartOK} {
			if !strings.Contains(card, want) {
				t.Fatalf("confirmation card = %s, want %q", card, want)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for restart confirmation card")
	}
	if actions.restartCalls != 0 {
		t.Fatalf("restart calls = %d, want 0 before confirmation", actions.restartCalls)
	}
}

func TestManagementRestartConfirmationStartsRestartOnce(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	b.managementRestartDelay = time.Millisecond
	actions := &fakeManagementSystemActions{restartCalled: make(chan struct{}, 1)}
	b.SetManagementSystemActions(actions)
	startedAt := time.Now().Add(-time.Hour)
	b.SetManagementSystemInfo(ManagementSystemInfo{Version: "v1.0.0", StartedAt: startedAt})
	patched := make(chan string, 2)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	confirmation := map[string]interface{}{
		"system_instance":    startedAt.UTC().Format(time.RFC3339Nano),
		"confirmation_nonce": "nonce-1",
	}
	resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionSystemRestartOK, confirmation))
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, handled = %v, want success toast", resp, handled)
	}
	select {
	case card := <-patched:
		if !strings.Contains(card, "正在重启 amux") {
			t.Fatalf("restart card = %s", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for restart processing card")
	}
	select {
	case <-actions.restartCalled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for restart action")
	}
	if actions.restartCalls != 1 {
		t.Fatalf("restart calls = %d, want 1", actions.restartCalls)
	}
	resp, handled, err = b.handleManagementCardAction(context.Background(), settingsCallback(managementActionSystemRestartOK, confirmation))
	if err != nil {
		t.Fatalf("repeated restart: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Fatalf("repeated restart response = %#v, want warning", resp)
	}
	if actions.restartCalls != 1 {
		t.Fatalf("restart calls after repeated confirmation = %d, want 1", actions.restartCalls)
	}
	pending, err := b.db.GetPendingManagementRecovery(b.currentBridgeId())
	if err != nil {
		t.Fatalf("GetPendingManagementRecovery: %v", err)
	}
	if pending == nil || pending.OpenID != "owner-open" || pending.MessageID != "om-settings" {
		t.Fatalf("pending recovery = %#v, want current owner and card", pending)
	}
}

func TestManagementRestartFailureClearsPendingRecovery(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	b.managementRestartDelay = time.Millisecond
	actions := &fakeManagementSystemActions{
		restartErr:    errors.New("restart failed"),
		restartCalled: make(chan struct{}, 1),
	}
	b.SetManagementSystemActions(actions)
	startedAt := time.Now().Add(-time.Hour)
	b.SetManagementSystemInfo(ManagementSystemInfo{Version: "v1.0.0", StartedAt: startedAt})
	patched := make(chan string, 3)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	confirmation := map[string]interface{}{
		managementSurfaceKey: string(managementSurfaceAmux),
		"system_instance":    startedAt.UTC().Format(time.RFC3339Nano),
		"confirmation_nonce": "nonce-failure",
	}
	if _, _, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionSystemRestartOK, confirmation)); err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	select {
	case <-actions.restartCalled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for restart action")
	}
	failureCard := waitForManagementCard(t, patched, func(card string) bool {
		return strings.Contains(card, "操作失败")
	})
	if !strings.Contains(failureCard, `"element_id":"amux_management_back"`) || strings.Contains(failureCard, `"element_id":"amux_navigation"`) {
		t.Fatalf("maintenance failure card = %s, should retain the overview back action", failureCard)
	}
	deadline := time.Now().Add(time.Second)
	for {
		pending, err := b.db.GetPendingManagementRecovery(b.currentBridgeId())
		if err != nil {
			t.Fatalf("GetPendingManagementRecovery: %v", err)
		}
		if pending == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending recovery was not cleared: %#v", pending)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRecoverPendingManagementCardPatchesOriginal(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	if err := b.db.UpsertPendingManagementRecovery(b.currentBridgeId(), "owner-open", "om-restart"); err != nil {
		t.Fatalf("UpsertPendingManagementRecovery: %v", err)
	}
	var patchedMessageID, patchedCard string
	b.managementCardPatch = func(_ context.Context, messageID, content string) error {
		patchedMessageID, patchedCard = messageID, content
		return nil
	}
	b.dmInteractive = func(_ context.Context, openID, content string) (string, string, error) {
		t.Fatalf("unexpected fallback DM to %s: %s", openID, content)
		return "", "", nil
	}

	done, err := b.recoverPendingManagementCardOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("recoverPendingManagementCardOnce: %v", err)
	}
	if !done || patchedMessageID != "om-restart" || !strings.Contains(patchedCard, "amux 已恢复") {
		t.Fatalf("done=%v message=%q card=%s", done, patchedMessageID, patchedCard)
	}
	pending, err := b.db.GetPendingManagementRecovery(b.currentBridgeId())
	if err != nil {
		t.Fatalf("GetPendingManagementRecovery: %v", err)
	}
	if pending != nil {
		t.Fatalf("pending recovery = %#v, want deleted", pending)
	}
}

func TestRecoverPendingLegacyAmux1CardUsesCurrentFocusedLayout(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	if err := b.db.UpsertPendingManagementRecoveryForSurface(
		b.currentBridgeId(),
		"owner-open",
		"om-legacy-amux1",
		managementRecoveryRestart,
		"",
		"amux1",
	); err != nil {
		t.Fatalf("UpsertPendingManagementRecoveryForSurface: %v", err)
	}
	var patchedCard string
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patchedCard = content
		return nil
	}

	done, err := b.recoverPendingManagementCardOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("recoverPendingManagementCardOnce: %v", err)
	}
	if !done || !strings.Contains(patchedCard, `"element_id":"amux_management_back"`) || strings.Contains(patchedCard, `"element_id":"amux_navigation"`) {
		t.Fatalf("done=%v card=%s, want current focused layout", done, patchedCard)
	}
}

func TestRecoverPendingManagementCardFallsBackToDM(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	if err := b.db.UpsertPendingManagementRecovery(b.currentBridgeId(), "owner-open", "om-restart"); err != nil {
		t.Fatalf("UpsertPendingManagementRecovery: %v", err)
	}
	b.managementCardPatch = func(context.Context, string, string) error {
		return errors.New("card expired")
	}
	var sentOpenID, sentCard string
	b.dmInteractive = func(_ context.Context, openID, content string) (string, string, error) {
		sentOpenID, sentCard = openID, content
		return "om-new", "", nil
	}

	done, err := b.recoverPendingManagementCardOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("recoverPendingManagementCardOnce: %v", err)
	}
	if !done || sentOpenID != "owner-open" || !strings.Contains(sentCard, "amux 已恢复") {
		t.Fatalf("done=%v openID=%q card=%s", done, sentOpenID, sentCard)
	}
}

func TestRecoverPendingManagementCardWaitsThenShowsAttention(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	if err := b.db.UpsertPendingManagementRecovery(b.currentBridgeId(), "owner-open", "om-restart"); err != nil {
		t.Fatalf("UpsertPendingManagementRecovery: %v", err)
	}
	var patchedCard string
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patchedCard = content
		return nil
	}

	done, err := b.recoverPendingManagementCardOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("recover before timeout: %v", err)
	}
	if done || patchedCard != "" {
		t.Fatalf("done=%v card=%q, want pending recovery", done, patchedCard)
	}
	done, err = b.recoverPendingManagementCardOnce(context.Background(), true)
	if err != nil {
		t.Fatalf("recover after timeout: %v", err)
	}
	if !done || !strings.Contains(patchedCard, "运行环境仍需关注") {
		t.Fatalf("done=%v card=%s", done, patchedCard)
	}
}

func TestManagementUpdateCheckPatchesResult(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &fakeManagementSystemActions{updateResult: ManagementUpdateInfo{
		CurrentVersion:  "v1.0.0",
		LatestVersion:   "v1.1.0",
		UpdateAvailable: true,
	}}
	b.SetManagementSystemActions(actions)
	b.SetManagementSystemInfo(ManagementSystemInfo{Version: "v1.0.0"})
	patched := make(chan string, 2)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionSystemUpdate, nil))
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, handled = %v, want success toast", resp, handled)
	}
	var cards []string
	for len(cards) < 2 {
		select {
		case card := <-patched:
			cards = append(cards, card)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for update check cards")
		}
	}
	if !strings.Contains(cards[0], "正在检查更新") {
		t.Fatalf("processing card = %s", cards[0])
	}
	for _, want := range []string{"发现可用版本", "v1.0.0", "v1.1.0", "升级到 v1.1.0", managementActionSystemUpgrade, `"confirm":`} {
		if !strings.Contains(cards[1], want) {
			t.Fatalf("result card = %s, want %q", cards[1], want)
		}
	}
	if actions.updateCalls != 1 {
		t.Fatalf("update calls = %d, want 1", actions.updateCalls)
	}
}

func TestManagementUpdateDismissesOnlyRequestedVersion(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	actions := &fakeManagementSystemActions{updateResult: ManagementUpdateInfo{
		CurrentVersion:  "1.0.0.1457",
		LatestVersion:   "1.0.0.1458",
		UpdateAvailable: true,
		ShouldPrompt:    true,
	}}
	b.SetManagementSystemActions(actions)
	b.SetManagementSystemInfo(ManagementSystemInfo{Version: "1.0.0.1457"})
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(
		managementActionSystemUpdateDismiss,
		map[string]interface{}{"version": "1.0.0.1458"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, handled = %v", resp, handled)
	}
	if actions.dismissCalls != 1 || actions.dismissTarget != "1.0.0.1458" {
		t.Fatalf("dismiss calls = %d target = %q", actions.dismissCalls, actions.dismissTarget)
	}
	select {
	case card := <-patched:
		if strings.Contains(card, "发现 amux 新版本") || strings.Contains(card, managementActionSystemUpdateDismiss) {
			t.Fatalf("dismissed maintenance card still prompts: %s", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for dismissed maintenance card")
	}
}

func TestManagementUpgradeConfirmationStartsUpgradeOnce(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	b.managementRestartDelay = time.Millisecond
	actions := &fakeManagementSystemActions{upgradeCalled: make(chan struct{}, 1)}
	b.SetManagementSystemActions(actions)
	startedAt := time.Now().Add(-time.Hour)
	b.SetManagementSystemInfo(ManagementSystemInfo{Version: "v1.0.0", StartedAt: startedAt})
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	confirmation := map[string]interface{}{
		"system_instance":    startedAt.UTC().Format(time.RFC3339Nano),
		"confirmation_nonce": "upgrade-nonce-1",
		"expected_version":   "v1.1.0",
	}
	resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionSystemUpgrade, confirmation))
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, handled = %v, want success toast", resp, handled)
	}
	select {
	case card := <-patched:
		if !strings.Contains(card, "正在升级 amux") || !strings.Contains(card, "v1.1.0") {
			t.Fatalf("upgrade card = %s", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upgrade processing card")
	}
	select {
	case <-actions.upgradeCalled:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for upgrade action")
	}
	if actions.upgradeCalls != 1 || actions.upgradeTarget != "v1.1.0" {
		t.Fatalf("upgrade calls=%d target=%q", actions.upgradeCalls, actions.upgradeTarget)
	}
	pending, err := b.db.GetPendingManagementRecovery(b.currentBridgeId())
	if err != nil {
		t.Fatalf("GetPendingManagementRecovery: %v", err)
	}
	if pending == nil || pending.Operation != managementRecoveryUpgrade || pending.ExpectedVersion != "v1.1.0" {
		t.Fatalf("pending recovery = %#v", pending)
	}

	resp, handled, err = b.handleManagementCardAction(context.Background(), settingsCallback(managementActionSystemUpgrade, confirmation))
	if err != nil {
		t.Fatalf("repeated upgrade: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "warning" {
		t.Fatalf("repeated upgrade response = %#v, want warning", resp)
	}
	if actions.upgradeCalls != 1 {
		t.Fatalf("upgrade calls after repeated confirmation = %d, want 1", actions.upgradeCalls)
	}
}

func TestRecoverPendingUpgradeCardRequiresExpectedVersion(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	b.SetManagementSystemInfo(ManagementSystemInfo{Version: "v1.1.0", StartedAt: time.Now()})
	if err := b.db.UpsertPendingManagementRecoveryForOperation(
		b.currentBridgeId(), "owner-open", "om-upgrade", managementRecoveryUpgrade, "v1.1.0",
	); err != nil {
		t.Fatalf("UpsertPendingManagementRecoveryForOperation: %v", err)
	}
	var patchedCard string
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patchedCard = content
		return nil
	}

	done, err := b.recoverPendingManagementCardOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("recoverPendingManagementCardOnce: %v", err)
	}
	if !done || !strings.Contains(patchedCard, "升级完成") || !strings.Contains(patchedCard, "v1.1.0") {
		t.Fatalf("done=%v card=%s", done, patchedCard)
	}

	if err := b.db.UpsertPendingManagementRecoveryForOperation(
		b.currentBridgeId(), "owner-open", "om-upgrade-2", managementRecoveryUpgrade, "v1.2.0",
	); err != nil {
		t.Fatalf("UpsertPendingManagementRecoveryForOperation: %v", err)
	}
	patchedCard = ""
	done, err = b.recoverPendingManagementCardOnce(context.Background(), false)
	if err != nil {
		t.Fatalf("recover mismatched upgrade: %v", err)
	}
	if !done || !strings.Contains(patchedCard, "更新结果待确认") || !strings.Contains(patchedCard, "v1.2.0") {
		t.Fatalf("done=%v mismatch card=%s", done, patchedCard)
	}
}

func TestManagementSystemMaintenanceNavigationUpdatesSameCard(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	hub.targets = []*pb.Target{{Id: "target-1", Name: "local"}}
	b.SetManagementSystemInfo(ManagementSystemInfo{
		Version:   "v0.1.6",
		StartedAt: time.Now().Add(-time.Hour),
		BotCount:  1,
	})
	patched := make(chan string, 2)
	b.managementCardPatch = func(_ context.Context, messageID, content string) error {
		patched <- messageID + ":" + content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(
		context.Background(),
		settingsCallback(managementActionSystemMaintenance, nil),
	)
	if err != nil {
		t.Fatalf("open maintenance: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("open maintenance response = %#v, handled = %v", resp, handled)
	}
	select {
	case card := <-patched:
		if !strings.HasPrefix(card, "om-settings:") || !strings.Contains(card, "系统状态") {
			t.Fatalf("maintenance patch = %s", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for maintenance card patch")
	}

	resp, handled, err = b.handleManagementCardAction(
		context.Background(),
		settingsCallback(managementActionStatusOpen, nil),
	)
	if err != nil {
		t.Fatalf("return to overview: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("return response = %#v, handled = %v", resp, handled)
	}
	select {
	case card := <-patched:
		if !strings.HasPrefix(card, "om-settings:") || !strings.Contains(card, "助手状态") {
			t.Fatalf("overview patch = %s", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for overview card patch")
	}
}

func managementTextContent(element cardElement) string {
	if element.Text == nil {
		return ""
	}
	return element.Text.Content
}

func managementSummaryTitle(element cardElement) string {
	if element.Tag != "column_set" || len(element.Columns) == 0 || len(element.Columns[0].Elements) == 0 {
		return ""
	}
	title := element.Columns[0].Elements[0]
	if title.Text == nil {
		return ""
	}
	return title.Text.Content
}

func managementElementActions(element cardElement) []string {
	var actions []string
	for _, column := range element.Columns {
		for _, child := range column.Elements {
			if child.Tag != "button" {
				continue
			}
			var value map[string]string
			if json.Unmarshal(child.Value, &value) == nil && value[managementActionKey] != "" {
				actions = append(actions, value[managementActionKey])
			}
		}
	}
	return actions
}

func TestBotToBotCapabilityAppearsInDirectoryWithPermissionCheck(t *testing.T) {
	state := managementState{
		Mode: config.FeishuInteractionThread,
		BotToBot: config.BridgeFeishuBotToBotConfig{
			Enabled:           true,
			MaxTurnsPerThread: 10,
		},
	}
	directory := renderCapabilitiesCard(state)
	for _, want := range []string{
		"共 3 项能力 · 1 项已开启",
		"智能体 @ 智能体", "已开启",
		"检查飞书权限",
		managementActionBotToBotOpen,
	} {
		if !strings.Contains(directory, want) {
			t.Fatalf("capabilities directory = %s, want %q", directory, want)
		}
	}
	detail := renderBotToBotCard(state)
	for _, want := range []string{
		"智能体 @ 智能体",
		"单任务串行",
		"每话题最多", "10 轮",
		"关闭智能体协作",
		managementActionBotToBotSet,
		`"enabled":"false"`,
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("bot-to-bot card = %s, want %q", detail, want)
		}
	}
	for _, unwanted := range []string{"检查飞书权限", managementActionPermissionRecheck} {
		if strings.Contains(detail, unwanted) {
			t.Fatalf("bot-to-bot card = %s, unwanted %q", detail, unwanted)
		}
	}

	state.BotToBot.Enabled = false
	detail = renderBotToBotCard(state)
	for _, want := range []string{"当前状态", "已关闭", "开启智能体协作", managementActionBotToBotSet, `"enabled":"true"`} {
		if !strings.Contains(detail, want) {
			t.Fatalf("disabled bot-to-bot card = %s, want %q", detail, want)
		}
	}

	state.BotToBot.Enabled = true
	state.Mode = config.FeishuInteractionSingleton
	directory = renderCapabilitiesCard(state)
	if !strings.Contains(directory, "切换到每个话题独立后生效") {
		t.Fatalf("singleton bot-to-bot status = %s", directory)
	}
}

func TestManagementCardActionTogglesCurrentBotToBotAndReauditsPermissions(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.bot_to_bot]
enabled = false
max_turns_per_thread = 10
`)
	b, _ := newBridgeForInputTest(t)
	b.profile = "coder"
	b.secrets = &Secrets{OpenId: "owner-open", AppId: "cli_test"}
	b.botToBot = config.GetBridgeFeishuBotToBotForProfile("coder")
	patched := make(chan string, 4)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}
	audited := make(chan bool, 2)
	b.permissionScopeList = func(context.Context) (map[string]int, error) {
		audited <- b.botToBotSnapshot().Enabled
		statuses := make(map[string]int)
		for _, capability := range feishuCapabilitySpecs {
			for _, scope := range capability.TenantScopes {
				statuses[scope] = 1
			}
		}
		return statuses, nil
	}

	for _, step := range []struct {
		enabled  string
		want     bool
		cardText string
	}{
		{enabled: "true", want: true, cardText: "关闭智能体协作"},
		{enabled: "false", want: false, cardText: "开启智能体协作"},
	} {
		type callbackResult struct {
			resp    *callback.CardActionTriggerResponse
			handled bool
			err     error
		}
		var resp *callback.CardActionTriggerResponse
		var handled bool
		var err error
		if step.want {
			b.botToBotSettingsMu.Lock()
			result := make(chan callbackResult, 1)
			go func() {
				gotResp, gotHandled, gotErr := b.handleManagementCardAction(
					context.Background(),
					settingsCallback(managementActionBotToBotSet, map[string]interface{}{"enabled": step.enabled}),
				)
				result <- callbackResult{resp: gotResp, handled: gotHandled, err: gotErr}
			}()
			select {
			case got := <-result:
				resp, handled, err = got.resp, got.handled, got.err
			case <-time.After(200 * time.Millisecond):
				b.botToBotSettingsMu.Unlock()
				t.Fatal("Bot-to-Bot callback blocked on config mutation")
			}
			if b.botToBotSnapshot().Enabled {
				b.botToBotSettingsMu.Unlock()
				t.Fatal("runtime changed before blocked config mutation completed")
			}
			b.botToBotSettingsMu.Unlock()
		} else {
			resp, handled, err = b.handleManagementCardAction(
				context.Background(),
				settingsCallback(managementActionBotToBotSet, map[string]interface{}{"enabled": step.enabled}),
			)
		}
		if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
			t.Fatalf("toggle %s response = %#v, handled = %v, err = %v", step.enabled, resp, handled, err)
		}
		card := waitForManagementCard(t, patched, func(card string) bool {
			return strings.Contains(card, step.cardText)
		})
		if got := b.botToBotSnapshot().Enabled; got != step.want {
			t.Fatalf("runtime enabled after %s = %v, want %v", step.enabled, got, step.want)
		}
		if got := config.GetBridgeFeishuBotToBotForProfile("coder").Enabled; got != step.want {
			t.Fatalf("persisted enabled after %s = %v, want %v", step.enabled, got, step.want)
		}
		if strings.Contains(card, "检查飞书权限") {
			t.Fatalf("toggle card = %s, should not restore detail permission button", card)
		}
		select {
		case got := <-audited:
			if got != step.want {
				t.Fatalf("permission audit enabled state = %v, want %v", got, step.want)
			}
		case <-time.After(time.Second):
			t.Fatalf("toggle %s did not trigger permission audit", step.enabled)
		}
		// Card delivery and permission auditing precede the worker's deferred
		// cleanup. Wait for completion before sending the next settings action.
		waitForBridgeTest(t, time.Second, func() bool {
			b.settingsMu.Lock()
			defer b.settingsMu.Unlock()
			return !b.botToBotSettingsActive
		})
	}
}

func TestSetBotToBotForProfileSerializesPersistenceAndRuntime(t *testing.T) {
	b := &Bridge{
		botCoordinationState: botCoordinationState{botToBot: config.BridgeFeishuBotToBotConfig{MaxTurnsPerThread: 10}},
	}
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{})
	errs := make(chan error, 2)

	go func() {
		errs <- b.setBotToBotForProfileWith(true, func() error {
			close(firstStarted)
			<-firstRelease
			return nil
		})
	}()
	<-firstStarted
	go func() {
		errs <- b.setBotToBotForProfileWith(false, func() error {
			close(secondStarted)
			return nil
		})
	}()
	select {
	case <-secondStarted:
		close(firstRelease)
		t.Fatal("second persistence started before first runtime update completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(firstRelease)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("setBotToBotForProfileWith: %v", err)
		}
	}
	if got := b.botToBotSnapshot().Enabled; got {
		t.Fatalf("final runtime enabled = %v, want second persisted value false", got)
	}
}

func TestManagementCapabilityNavigationUsesFocusedCards(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	patched := make(chan string, 2)
	b.managementCardPatch = func(_ context.Context, messageID, content string) error {
		if messageID != "om-settings" {
			t.Fatalf("message id = %q, want om-settings", messageID)
		}
		patched <- content
		return nil
	}

	for _, step := range []struct {
		action    string
		want      []string
		notWant   []string
		wantToast string
	}{
		{
			action:    managementActionCapabilitiesOpen,
			want:      []string{"接入能力", "返回概览", "返回助手设置", "云文档评论", "已关闭", "打开设置", managementActionDocumentCommentsOpen},
			notWant:   []string{managementActionDocumentCommentsSet},
			wantToast: "已打开接入能力",
		},
		{
			action:    managementActionDocumentCommentsOpen,
			want:      []string{"云文档评论", "返回概览", "返回接入能力", "当前状态", "已关闭", "任务配置", "继承助手默认", "专家", "模型", "如何使用", "开启云文档评论", managementActionDocumentCommentsSet},
			notWant:   []string{"返回助手设置"},
			wantToast: "已打开云文档评论设置",
		},
	} {
		resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(step.action, map[string]interface{}{
			managementSurfaceKey: string(managementSurfaceAmux),
		}))
		if err != nil {
			t.Fatalf("handle %s: %v", step.action, err)
		}
		if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" || resp.Toast.Content != step.wantToast {
			t.Fatalf("handle %s response = %#v, handled = %v", step.action, resp, handled)
		}
		select {
		case card := <-patched:
			if !strings.Contains(card, `"`+managementSurfaceKey+`":"`+string(managementSurfaceAmux)+`"`) {
				t.Fatalf("%s card = %s, want amux surface propagation", step.action, card)
			}
			for _, want := range step.want {
				if !strings.Contains(card, want) {
					t.Fatalf("%s card = %s, want %q", step.action, card, want)
				}
			}
			for _, notWant := range step.notWant {
				if strings.Contains(card, notWant) {
					t.Fatalf("%s card = %s, should hide %q", step.action, card, notWant)
				}
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s card", step.action)
		}
	}
}

func TestManagementCardActionEnablesCurrentBotDocumentComments(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.document_comments]
enabled = false
`)
	b, _ := newBridgeForInputTest(t)
	b.profile = "coder"
	b.secrets = &Secrets{OpenId: "owner-open"}
	subscriptions := 0
	subscriptionStarted := make(chan struct{})
	releaseSubscription := make(chan struct{})
	b.documentSubscribe = func(context.Context) error {
		subscriptions++
		close(subscriptionStarted)
		<-releaseSubscription
		return nil
	}
	patched := make(chan string, 4)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	type callbackResult struct {
		resp    *callback.CardActionTriggerResponse
		handled bool
		err     error
	}
	result := make(chan callbackResult, 1)
	go func() {
		resp, handled, err := b.handleManagementCardAction(
			context.Background(),
			settingsCallback(managementActionDocumentCommentsSet, map[string]interface{}{"enabled": "true"}),
		)
		result <- callbackResult{resp: resp, handled: handled, err: err}
	}()
	var got callbackResult
	select {
	case got = <-result:
	case <-time.After(200 * time.Millisecond):
		close(releaseSubscription)
		t.Fatal("document comment callback blocked on subscription")
	}
	resp, handled, err := got.resp, got.handled, got.err
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, handled = %v, want success", resp, handled)
	}
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !strings.Contains(resp.Toast.Content, "正在开启") {
		t.Fatalf("toast = %q, want immediate processing status", resp.Toast.Content)
	}
	select {
	case <-subscriptionStarted:
	case <-time.After(time.Second):
		t.Fatal("subscription did not start asynchronously")
	}
	if b.documentCommentsActive() {
		t.Fatal("document comments became active before subscription completed")
	}
	close(releaseSubscription)
	card := waitForManagementCard(t, patched, func(card string) bool {
		return strings.Contains(card, "已开启") && strings.Contains(card, "关闭云文档评论")
	})
	if subscriptions != 1 || !b.documentCommentsActive() {
		t.Fatalf("subscriptions = %d active = %v, want 1/true; card=%s", subscriptions, b.documentCommentsActive(), card)
	}
	if got := config.GetBridgeFeishuDocumentCommentsForProfile("coder"); !got.Enabled {
		t.Fatalf("profile config = %+v, want enabled", got)
	}
}

func TestManagementCardActionKeepsDocumentCommentsDisabledWhenSubscriptionFails(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.document_comments]
enabled = false
`)
	b, _ := newBridgeForInputTest(t)
	b.profile = "coder"
	b.secrets = &Secrets{AppId: "cli_test", OpenId: "owner-open"}
	b.documentSubscribe = func(context.Context) error {
		scopes := []string{"docs:event:subscribe"}
		return &feishuPermissionError{
			operation: "subscribe document comments",
			code:      99991672,
			message:   "missing permission",
			scopes:    scopes,
			grantURL:  feishuPermissionGrantURL("cli_test", scopes),
		}
	}
	patched := make(chan string, 4)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(
		context.Background(),
		settingsCallback(managementActionDocumentCommentsSet, map[string]interface{}{"enabled": "true"}),
	)
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" ||
		!strings.Contains(resp.Toast.Content, "正在开启") {
		t.Fatalf("response = %#v, handled = %v, want immediate processing toast", resp, handled)
	}
	card := waitForManagementCard(t, patched, func(card string) bool {
		return strings.Contains(card, "开启失败") && strings.Contains(card, "已关闭") &&
			strings.Contains(card, "申请权限")
	})
	if !strings.Contains(card, "docs:event:subscribe") ||
		!strings.Contains(card, "https://open.feishu.cn/app/cli_test/auth") {
		t.Fatalf("document permission card = %s, want scope and grant URL", card)
	}
	if b.documentCommentsActive() {
		t.Fatalf("document comments active = true after subscription failure; card=%s", card)
	}
	if got := config.GetBridgeFeishuDocumentCommentsForProfile("coder"); got.Enabled {
		t.Fatalf("profile config = %+v, want unchanged disabled state", got)
	}
}

func TestManagementGroupWatchAddDedupesAndRemoveLastDisables(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.group_watch]
enabled = false
sender_types = ["user"]
poll_interval_seconds = 30
lookback_seconds = 120
`)
	b, _ := newBridgeForInputTest(t)
	b.profile = "coder"
	b.secrets = &Secrets{OpenId: "owner-open"}
	b.groupWatchConfig = config.GetBridgeFeishuGroupWatchForProfile("coder")
	patched := make(chan string, 4)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	add := map[string]interface{}{"operation": "add", "chat_id": "oc_qa", "chat_name": "答疑群", "view": "current_group"}
	for range 2 {
		resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionGroupWatchUpdate, add))
		if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
			t.Fatalf("add callback: response=%#v handled=%v err=%v", resp, handled, err)
		}
	}
	got := config.GetBridgeFeishuGroupWatchForProfile("coder")
	if !got.Enabled || fmt.Sprint(got.ChatIDs) != "[oc_qa]" || fmt.Sprint(got.SenderTypes) != "[user app]" || got.PollIntervalSeconds != 15 || got.LookbackSeconds != 60 {
		t.Fatalf("group watch after add = %+v, want enabled default config with one deduped group", got)
	}
	if !b.groupWatchActive() {
		t.Fatal("group watch did not become active immediately after card update")
	}
	card := <-patched
	for _, want := range []string{"答疑群", "已加入", "将本群移出值守"} {
		if !strings.Contains(card, want) {
			t.Fatalf("current group card = %s, want %q", card, want)
		}
	}

	remove := map[string]interface{}{"operation": "remove", "chat_id": "oc_qa", "chat_name": "答疑群", "view": "current_group"}
	resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionGroupWatchUpdate, remove))
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("remove callback: response=%#v handled=%v err=%v", resp, handled, err)
	}
	got = config.GetBridgeFeishuGroupWatchForProfile("coder")
	if got.Enabled || len(got.ChatIDs) != 0 || b.groupWatchActive() {
		t.Fatalf("group watch after removing last group = %+v active=%v, want disabled empty", got, b.groupWatchActive())
	}
}

func TestManagementGroupWatchPauseResumePreservesMultipleGroups(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.profiles.coder.group_watch]
enabled = true
chat_ids = ["oc_alert", "oc_qa"]
sender_types = ["user", "app"]
poll_interval_seconds = 15
lookback_seconds = 60
`)
	b, _ := newBridgeForInputTest(t)
	b.profile = "coder"
	b.secrets = &Secrets{OpenId: "owner-open"}
	b.groupWatchConfig = config.GetBridgeFeishuGroupWatchForProfile("coder")
	b.managementCardPatch = func(context.Context, string, string) error { return nil }

	for _, operation := range []string{"pause", "resume"} {
		resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionGroupWatchUpdate, map[string]interface{}{"operation": operation}))
		if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
			t.Fatalf("%s callback: response=%#v handled=%v err=%v", operation, resp, handled, err)
		}
		got := config.GetBridgeFeishuGroupWatchForProfile("coder")
		wantEnabled := operation == "resume"
		if got.Enabled != wantEnabled || fmt.Sprint(got.ChatIDs) != "[oc_alert oc_qa]" {
			t.Fatalf("%s group watch = %+v, want enabled=%v and preserved groups", operation, got, wantEnabled)
		}
	}
}

func TestManagementGroupWatchOpenShowsResolvedGroupNames(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	b.groupWatchConfig = config.BridgeFeishuGroupWatchConfig{Enabled: true, ChatIDs: []string{"oc_alert", "oc_qa"}}
	b.chatNameLookup = func(_ context.Context, chatID string) (string, error) {
		return map[string]string{"oc_alert": "支付告警群", "oc_qa": "研发答疑群"}[chatID], nil
	}
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionGroupWatchOpen, nil))
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("open callback: response=%#v handled=%v err=%v", resp, handled, err)
	}
	card := <-patched
	for _, want := range []string{"支付告警群", "研发答疑群", "…lert", "…c_qa", "移出值守", `"chat_id":"oc_alert"`, `"view":"group_watch_list"`} {
		if !strings.Contains(card, want) {
			t.Fatalf("group watch card = %s, want %q", card, want)
		}
	}
}

func TestManagementGroupWatchListRemoveStaysOnList(t *testing.T) {
	loadBridgeTestConfig(t, `[bridge.feishu.profiles.coder.group_watch]
enabled = true
chat_ids = ["oc_alert", "oc_qa"]
sender_types = ["user", "app"]
poll_interval_seconds = 15
lookback_seconds = 60
`)
	b, _ := newBridgeForInputTest(t)
	b.profile = "coder"
	b.secrets = &Secrets{OpenId: "owner-open"}
	b.groupWatchConfig = config.GetBridgeFeishuGroupWatchForProfile("coder")
	b.chatNameLookup = func(_ context.Context, chatID string) (string, error) {
		return map[string]string{"oc_alert": "支付告警群", "oc_qa": "研发答疑群"}[chatID], nil
	}
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), settingsCallback(managementActionGroupWatchUpdate, map[string]interface{}{
		"operation": "remove", "chat_id": "oc_alert", "chat_name": "支付告警群", "view": "group_watch_list",
	}))
	if err != nil || !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("remove callback: response=%#v handled=%v err=%v", resp, handled, err)
	}
	card := <-patched
	for _, want := range []string{"群值守", "已开启 · 1 个群", "研发答疑群", "移出值守"} {
		if !strings.Contains(card, want) {
			t.Fatalf("group watch list after remove = %s, want %q", card, want)
		}
	}
	for _, notWant := range []string{"当前群管理", "支付告警群"} {
		if strings.Contains(card, notWant) {
			t.Fatalf("group watch list after remove = %s, should not contain %q", card, notWant)
		}
	}
}

func setCurrentBotProfile(t *testing.T, b *Bridge, profile string) {
	t.Helper()
	state, err := b.db.GetBotProfileState(b.currentBridgeId(), "thread")
	if err != nil {
		t.Fatalf("GetBotProfileState: %v", err)
	}
	if state.DefaultAgent == profile {
		return
	}
	if _, err := b.db.CommitThreadBotSettings(b.currentBridgeId(), state.Version, profile); err != nil {
		t.Fatalf("CommitThreadBotSettings: %v", err)
	}
}

func setCurrentBotRuntime(t *testing.T, b *Bridge, runtimeRef string) {
	t.Helper()
	state, err := b.db.GetBotProfileState(b.currentBridgeId(), "thread")
	if err != nil {
		t.Fatalf("GetBotProfileState: %v", err)
	}
	if _, err := b.db.CommitThreadBotSettingsWithLaunch(b.currentBridgeId(), state.Version,
		state.DefaultAgent, runtimeRef, state.DefaultModelRef, state.DefaultReasoningEffort, state.IsolationRequired); err != nil {
		t.Fatalf("CommitThreadBotSettingsWithLaunch: %v", err)
	}
}

func forceBotProfileThread(t *testing.T, b *Bridge) {
	t.Helper()
	state, err := b.db.GetBotProfileState(b.currentBridgeId(), "thread")
	if err != nil {
		t.Fatalf("GetBotProfileState: %v", err)
	}
	if state.InteractionMode == "thread" {
		return
	}
	if _, err := b.db.CommitThreadBotSettings(b.currentBridgeId(), state.Version, state.DefaultAgent); err != nil {
		t.Fatalf("CommitThreadBotSettings: %v", err)
	}
}

func TestHandleP2PChatEnteredSendsOnboardingOnce(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{}
	var cards []string
	b.dmInteractive = func(_ context.Context, openID, content string) (string, string, error) {
		cards = append(cards, openID+":"+content)
		return "msg-1", "", nil
	}
	openID := "owner-open"
	event := &larkim.P2ChatAccessEventBotP2pChatEnteredV1{
		Event: &larkim.P2ChatAccessEventBotP2pChatEnteredV1Data{
			OperatorId: &larkim.UserId{OpenId: &openID},
		},
	}

	if err := b.handleP2PChatEntered(context.Background(), event); err != nil {
		t.Fatalf("first handleP2PChatEntered: %v", err)
	}
	if err := b.handleP2PChatEntered(context.Background(), event); err != nil {
		t.Fatalf("second handleP2PChatEntered: %v", err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	if !strings.Contains(cards[0], "欢迎使用 amux") {
		t.Fatalf("card = %s, want onboarding content", cards[0])
	}
	if b.secrets == nil || b.secrets.OpenId != openID {
		t.Fatalf("owner open id = %#v, want %q", b.secrets, openID)
	}
}

func TestHandleP2PChatEnteredSkipsAlreadyConfirmedOwner(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	openID := "owner-open"
	b.secrets = &Secrets{OpenId: openID}
	b.dmInteractive = func(_ context.Context, openID, content string) (string, string, error) {
		t.Fatalf("unexpected onboarding card to %s: %s", openID, content)
		return "", "", nil
	}
	event := &larkim.P2ChatAccessEventBotP2pChatEnteredV1{
		Event: &larkim.P2ChatAccessEventBotP2pChatEnteredV1Data{
			OperatorId: &larkim.UserId{OpenId: &openID},
		},
	}
	if err := b.handleP2PChatEntered(context.Background(), event); err != nil {
		t.Fatalf("handleP2PChatEntered: %v", err)
	}
}

func TestIsAmuxManagementCommand(t *testing.T) {
	if !isAmuxManagementCommand("/amux") {
		t.Fatal("/amux must be recognized as the management command")
	}
	for _, text := range []string{"/amux1", "/amux status", "/amux settings", " /amux help ", "/amux onboarding", "/workspace status"} {
		if isAmuxManagementCommand(text) {
			t.Fatalf("isAmuxManagementCommand(%q) = true, want removed", text)
		}
	}
}

func TestAmuxCommandSendsCompactOverviewCard(t *testing.T) {
	loadBridgeTestConfig(t, ``)
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	var sentCard string
	b.dmInteractive = func(_ context.Context, openID, content string) (string, string, error) {
		if openID != "owner-open" {
			t.Fatalf("open id = %q, want owner-open", openID)
		}
		sentCard = content
		return "om-amux", "", nil
	}

	if err := b.handleMessage(context.Background(), feishuMessageEvent("p2p", "oc-private", "om-amux", "owner-open", "/amux")); err != nil {
		t.Fatalf("handleMessage /amux: %v", err)
	}
	for _, want := range []string{
		`"content":"amux"`,
		"当前配置",
		"管理",
		"重新发送 /amux 刷新状态",
		"实验功能",
		"助手设置",
		"专家库",
		"系统状态",
		"使用帮助",
		`"` + managementSurfaceKey + `":"navigation"`,
	} {
		if !strings.Contains(sentCard, want) {
			t.Fatalf("/amux card = %s, want %q", sentCard, want)
		}
	}
	for _, unwanted := range []string{`"element_id":"amux_navigation"`, `"content":"概览"`, `"content":"当前助手"`} {
		if strings.Contains(sentCard, unwanted) {
			t.Fatalf("/amux card = %s, should omit %q", sentCard, unwanted)
		}
	}
	for action, wantCount := range map[string]int{
		managementActionSettingsOpen:      1,
		managementActionPackLibraryOpen:   1,
		managementActionSystemMaintenance: 1,
		managementActionHelpOpen:          1,
	} {
		if got := strings.Count(sentCard, action); got != wantCount {
			t.Fatalf("/amux action %q count = %d, want %d", action, got, wantCount)
		}
	}
	var doc cardDoc
	if err := json.Unmarshal([]byte(sentCard), &doc); err != nil {
		t.Fatalf("unmarshal /amux card: %v", err)
	}
	foundConfig := false
	foundActionRows := 0
	for index, element := range doc.Body.Elements {
		if element.ElementID == "amux_overview_config" {
			foundConfig = true
			if index != 4 || element.FlexMode != "none" || len(element.Columns) != 1 {
				t.Fatalf("/amux config = %#v at %d, want one vertical container after current config heading", element, index)
			}
			column := element.Columns[0]
			if column.BackgroundStyle != "blue-50" || column.Direction != "vertical" || column.VerticalSpacing != "4px" || len(column.Elements) != 7 {
				t.Fatalf("/amux config column = %#v, want seven compact vertical rows", column)
			}
			workspace := column.Elements[1]
			if workspace.Tag != "div" || workspace.Text == nil || workspace.Text.TextSize != "notation" || workspace.Text.TextColor != "grey" || workspace.Icon == nil || workspace.Icon.Token != "info_outlined" || !strings.HasPrefix(workspace.Text.Content, "每个新话题") {
				t.Fatalf("/amux workspace row = %#v, want standalone grey notation with info icon", workspace)
			}
			for rowIndex, label := range map[int]string{0: "处理方式", 2: "默认专家", 3: "运行引擎", 4: "默认模型", 5: "思考强度", 6: "处理过程展示"} {
				row := column.Elements[rowIndex]
				if row.Tag != "column_set" || row.FlexMode != "none" || row.HorizontalSpacing != "8px" || len(row.Columns) != 2 {
					t.Fatalf("/amux config row %d = %#v, want two-column label/value row", rowIndex, row)
				}
				labelElement := row.Columns[0].Elements[0]
				valueElement := row.Columns[1].Elements[0]
				if managementTextContent(labelElement) != label || labelElement.Text.TextSize != "notation" || labelElement.Text.TextColor != "grey" {
					t.Fatalf("/amux config row %d label = %#v, want grey notation %q", rowIndex, labelElement, label)
				}
				if valueElement.Text == nil || valueElement.Text.TextSize != "normal" || valueElement.Text.TextColor != "default" {
					t.Fatalf("/amux config row %d value = %#v, want normal default text", rowIndex, valueElement)
				}
			}
		}
		if strings.HasPrefix(element.ElementID, "amux_overview_actions") {
			row := foundActionRows
			foundActionRows++
			wantActions := []string{managementActionSystemMaintenance, managementActionSettingsOpen, managementActionPackLibraryOpen, managementActionExperimentsOpen, managementActionGuideOpen, managementActionHelpOpen, capabilityAction + "open", managementActionConsoleOpen}
			if index != 7+row || element.FlexMode != "none" || len(element.Columns) != min(3, len(wantActions)-row*3) {
				t.Fatal("overview rows must contain at most three actions")
			}
			for actionIndex, wantAction := range wantActions[row*3 : min(len(wantActions), row*3+3)] {
				button := element.Columns[actionIndex].Elements[0]
				var value map[string]string
				if err := json.Unmarshal(button.Value, &value); err != nil || value[managementActionKey] != wantAction {
					t.Fatalf("/amux action %d = %#v value=%#v err=%v, want %q", actionIndex, button, value, err, wantAction)
				}
			}
		}
	}
	if len(doc.Body.Elements) != 12 || managementTextContent(doc.Body.Elements[3]) != "当前配置" || managementTextContent(doc.Body.Elements[6]) != "管理" {
		t.Fatalf("/amux body = %#v, want status → current config → management hierarchy", doc.Body.Elements)
	}
	if managementTextContent(doc.Body.Elements[10]) != "重新发送 /amux 刷新状态" {
		t.Fatal("refresh hint must precede the language selector")
	}
	if button := findManagementButtonByAction(t, sentCard, managementActionExperimentsOpen); button.Width != "fill" || button.Type != "default" || button.Icon == nil || button.Icon.Color != "grey" {
		t.Fatalf("experiment entry = %#v, want full-width quiet management button with icon", button)
	}
	if !foundConfig || foundActionRows != 3 {
		t.Fatalf("/amux card = %s, found config=%v action rows=%d", sentCard, foundConfig, foundActionRows)
	}
	if b.isPrivateManagementCommand("/amux1") {
		t.Fatal("/amux1 must be removed from owner private management commands")
	}
}

func TestManagementOverviewKeepsOneStatusBlockAndPromotesSystemOnlyWhenAttention(t *testing.T) {
	healthy := renderManagementOverviewCard(managementState{
		Mode:         config.FeishuInteractionThread,
		SystemStatus: managementSystemHealthy,
		Targets:      2,
	})
	for _, want := range []string{
		"运行正常",
		"可用运行环境 2 个 · 可以开始任务",
		`"template":"green"`,
	} {
		if !strings.Contains(healthy, want) {
			t.Fatalf("healthy overview = %s, want %q", healthy, want)
		}
	}
	if got := strings.Count(healthy, `"content":"运行正常"`); got != 1 {
		t.Fatalf("healthy overview status count = %d, want one consolidated status block: %s", got, healthy)
	}
	if system := findManagementButtonByAction(t, healthy, managementActionSystemMaintenance); system.Type != "default" {
		t.Fatalf("healthy system action = %#v, want equal-weight default action", system)
	}
	if settings := findManagementButtonByAction(t, healthy, managementActionSettingsOpen); settings.Type != "primary" || settings.Icon == nil || settings.Icon.Color != "blue" {
		t.Fatalf("healthy settings action = %#v, want the only blue primary action", settings)
	}
	for _, action := range []string{managementActionSystemMaintenance, managementActionPackLibraryOpen, managementActionHelpOpen} {
		if button := findManagementButtonByAction(t, healthy, action); button.Icon == nil || button.Icon.Color != "grey" {
			t.Fatalf("healthy secondary action %q = %#v, want quiet grey icon", action, button)
		}
	}

	attention := renderManagementOverviewCard(managementState{
		Mode:         config.FeishuInteractionThread,
		SystemStatus: managementSystemAttention,
	})
	for _, want := range []string{
		"暂时无法开始新任务",
		"没有可用运行环境 · 请查看「系统状态」",
		`"template":"orange"`,
	} {
		if !strings.Contains(attention, want) {
			t.Fatalf("attention overview = %s, want %q", attention, want)
		}
	}
	if system := findManagementButtonByAction(t, attention, managementActionSystemMaintenance); system.Type != "primary" || system.Icon == nil || system.Icon.Color != "blue" {
		t.Fatalf("attention system action = %#v, want blue primary", system)
	}
	for _, action := range []string{managementActionSettingsOpen, managementActionPackLibraryOpen, managementActionHelpOpen} {
		if button := findManagementButtonByAction(t, attention, action); button.Type != "default" || button.Icon == nil || button.Icon.Color != "grey" {
			t.Fatalf("attention secondary action %q = %#v, want quiet default", action, button)
		}
	}
}

func TestAmuxAssistantUsesStableBackActionAndFocusedEditFlow(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, messageID, content string) error {
		if messageID != "om-amux" {
			t.Fatalf("message id = %q, want om-amux", messageID)
		}
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-amux"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:  managementActionSettingsOpen,
				managementSurfaceKey: string(managementSurfaceAmux),
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, handled = %v", resp, handled)
	}
	select {
	case card := <-patched:
		for _, want := range []string{
			"修改默认配置",
			"管理接入能力",
			"返回概览",
			`"` + managementSurfaceKey + `":"` + string(managementSurfaceAmux) + `"`,
		} {
			if !strings.Contains(card, want) {
				t.Fatalf("settings summary patch = %s, want %q", card, want)
			}
		}
		for _, unwanted := range []string{`"tag":"form"`, `"element_id":"amux_navigation"`, `"content":"amux 状态"`} {
			if strings.Contains(card, unwanted) {
				t.Fatalf("settings summary patch = %s, should omit duplicate action %q", card, unwanted)
			}
		}
		if got := strings.Count(card, managementActionPackLibraryOpen); got != 0 {
			t.Fatalf("settings summary pack navigation count = %d, want no cross-module shortcut", got)
		}
		if got := strings.Count(card, managementActionStatusOpen); got != 1 {
			t.Fatalf("settings summary overview action count = %d, want one", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for /amux settings summary patch")
	}

	resp, handled, err = b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-amux"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:  managementActionSettingsEdit,
				managementSurfaceKey: string(managementSurfaceAmux),
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction edit: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("edit response = %#v, handled = %v", resp, handled)
	}
	select {
	case card := <-patched:
		for _, want := range []string{
			`"tag":"form"`,
			`"` + managementSurfaceKey + `":"` + string(managementSurfaceAmux) + `"`,
		} {
			if !strings.Contains(card, want) {
				t.Fatalf("settings edit patch = %s, want %q", card, want)
			}
		}
		for _, unwanted := range []string{`"element_id":"amux_navigation"`} {
			if strings.Contains(card, unwanted) {
				t.Fatalf("settings edit patch = %s, should omit %q", card, unwanted)
			}
		}
		findManagementButtonByAction(t, card, managementActionStatusOpen)
		findManagementButtonByAction(t, card, managementActionSettingsOpen)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for /amux settings edit patch")
	}

	resp, handled, err = b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-amux"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:  managementActionSettingsSave,
				managementSurfaceKey: string(managementSurfaceAmux),
				"interaction":        "thread",
				"default_agent":      "missing/profile",
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction invalid save: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "error" {
		t.Fatalf("invalid save response = %#v, handled = %v", resp, handled)
	}
	select {
	case card := <-patched:
		if !strings.Contains(card, `"tag":"form"`) || strings.Contains(card, `"element_id":"amux_navigation"`) || !strings.Contains(card, "返回概览") {
			t.Fatalf("settings validation patch = %s, want form with a return action", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for /amux settings validation patch")
	}

	resp, handled, err = b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-amux"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:  managementActionSettingsCancel,
				managementSurfaceKey: string(managementSurfaceAmux),
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction cancel: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("cancel response = %#v, handled = %v", resp, handled)
	}
	// Model discovery can have queued an intermediate editor patch before the
	// cancellation. Wait for its summary instead of treating that older patch
	// as the cancellation's result.
	card := waitForManagementCard(t, patched, func(card string) bool {
		return strings.Contains(card, `"element_id":"amux_settings_task_defaults"`)
	})
	if card == "" || !strings.Contains(card, "返回概览") || strings.Contains(card, `"element_id":"amux_navigation"`) || strings.Contains(card, `"tag":"form"`) {
		t.Fatalf("settings cancel patch = %s, want stable settings summary", card)
	}
}

func TestAmuxHelpViewUsesCompactNaturalEntryGuide(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-amux"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:  managementActionHelpOpen,
				managementSurfaceKey: string(managementSurfaceAmux),
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction help: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("help response = %#v, handled = %v", resp, handled)
	}
	select {
	case card := <-patched:
		for _, want := range []string{
			"返回概览",
			"私聊：直接发送任务",
			"群聊：@智能体 + 任务",
			"话题：继续回复即可补充要求",
			"云文档：在评论中 @智能体",
			"/help：查看当前场景的完整命令",
		} {
			if !strings.Contains(card, want) {
				t.Fatalf("management help card = %s, want %q", card, want)
			}
		}
		for _, unwanted := range []string{`"element_id":"amux_navigation"`, "/run", "/bind", "/queue"} {
			if strings.Contains(card, unwanted) {
				t.Fatalf("management help card = %s, should omit %q", card, unwanted)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for /amux help patch")
	}
}

func TestAmuxSystemStatusUsesStableBackAndSafeActionHierarchy(t *testing.T) {
	card := renderSystemMaintenanceCardForSurface(
		managementState{SystemStatus: managementSystemHealthy},
		managementSurfaceAmux,
	)
	card = renderManagementCardForSurface(card, managementSurfaceAmux, managementViewSystem)
	if !strings.Contains(card, `"element_id":"amux_management_back"`) || !strings.Contains(card, "返回概览") {
		t.Fatalf("system status card = %s, want stable overview back action", card)
	}
	if strings.Contains(card, `"element_id":"amux_navigation"`) || strings.Contains(card, `"content":"返回"`) {
		t.Fatalf("system status card = %s, should omit legacy navigation and duplicate back", card)
	}
	if got := strings.Count(card, managementActionStatusOpen); got != 1 {
		t.Fatalf("system status overview action count = %d, want exactly one", got)
	}
	checkUpdate := findManagementButtonByAction(t, card, managementActionSystemUpdate)
	restart := findManagementButtonByAction(t, card, managementActionSystemRestartOK)
	if checkUpdate.Type != "primary" || restart.Type != "default" || restart.Confirm == nil {
		t.Fatalf("system status actions = update:%#v restart:%#v, want primary update and confirmed secondary restart", checkUpdate, restart)
	}
}

func findManagementButtonByAction(t *testing.T, card, action string) cardElement {
	t.Helper()
	var doc cardDoc
	if err := json.Unmarshal([]byte(card), &doc); err != nil {
		t.Fatalf("unmarshal management card: %v", err)
	}
	var found *cardElement
	var visit func([]cardElement)
	visit = func(elements []cardElement) {
		for i := range elements {
			element := &elements[i]
			if element.Tag == "button" {
				var value map[string]string
				if json.Unmarshal(element.Value, &value) == nil && value[managementActionKey] == action {
					copy := *element
					found = &copy
					return
				}
			}
			visit(element.Actions)
			visit(element.Elements)
			for _, column := range element.Columns {
				visit(column.Elements)
			}
			if found != nil {
				return
			}
		}
	}
	visit(doc.Body.Elements)
	if found == nil {
		t.Fatalf("management card = %s, want action %q", card, action)
	}
	return *found
}

func TestLoadedExpertLibraryKeepsOnlySelectionAndManagementActions(t *testing.T) {
	elements := managementLoadedPackElements(
		agentprofile.LoadedPackState{
			Namespace:       "team",
			DisplayName:     "Data Arch",
			CurrentRevision: "1234567890abcdef",
			ProfileCount:    3,
			Generation:      "generation-1",
		},
		managementPackLibraryView{Section: managementPackLibrarySectionLoaded},
		"operation-nonce",
	)
	raw, err := json.Marshal(elements)
	if err != nil {
		t.Fatalf("marshal loaded expert library elements: %v", err)
	}
	card := string(raw)
	if !strings.Contains(card, `"`+managementActionKey+`":"`+managementActionSettingsEdit+`"`) {
		t.Fatalf("loaded expert library elements = %s, want direct settings edit action", card)
	}
	if !strings.Contains(card, `"`+managementActionKey+`":"agent_pack.manage"`) {
		t.Fatalf("loaded expert library elements = %s, want focused management action", card)
	}
	if strings.Contains(card, `"`+managementActionKey+`":"`+managementActionSettingsOpen+`"`) {
		t.Fatalf("loaded expert library elements = %s, should not route selection through settings summary", card)
	}
	for _, action := range []string{managementActionPackSync, managementActionPackRemove} {
		if strings.Contains(card, `"`+managementActionKey+`":"`+action+`"`) {
			t.Fatalf("loaded expert library elements = %s, should move %q into focused management", card, action)
		}
	}
}

func TestAmuxSettingsFailureKeepsFocusedRecoveryFlow(t *testing.T) {
	card := renderSettingsFailureCardForSurface(
		managementState{
			Mode:           config.FeishuInteractionThread,
			DefaultAgent:   "builtin/codex",
			CanManagePacks: true,
		},
		managementSettingsDraft{
			Interaction:  config.FeishuInteractionThread,
			DefaultAgent: "builtin/codex",
		},
		errors.New("failed"),
		managementSurfaceAmux,
	)
	card = renderFocusedManagementCardForSurface(card, managementSurfaceAmux)
	for _, hidden := range []string{managementActionPackLibraryOpen, `"element_id":"amux_navigation"`} {
		if strings.Contains(card, hidden) {
			t.Fatalf("settings failure card = %s, should omit cross-module action %q", card, hidden)
		}
	}
	if !strings.Contains(card, `"tag":"form"`) {
		t.Fatalf("settings failure card = %s, want focused retry form", card)
	}
	findManagementButtonByAction(t, card, managementActionStatusOpen)
	findManagementButtonByAction(t, card, managementActionSettingsOpen)
}

func TestSettingsFailureReasonRedactsCredentials(t *testing.T) {
	message := managementSettingsFailureMessage(errors.New("provider rejected token=secret-value and Bearer abcdefghijklmnop"))
	if !strings.Contains(managementDefaultText(message), "provider rejected") || !strings.Contains(managementDefaultText(message), "[REDACTED]") {
		t.Fatalf("message = %q, want concrete redacted reason", message)
	}
	for _, secret := range []string{"secret-value", "abcdefghijklmnop"} {
		if strings.Contains(managementDefaultText(message), secret) {
			t.Fatalf("message = %q, leaked %q", message, secret)
		}
	}
}

func TestAmuxLibraryCardIncludesStableBackWithinSafetyBudget(t *testing.T) {
	loaded := make([]agentprofile.LoadedPackState, 0, 40)
	for i := 0; i < 40; i++ {
		loaded = append(loaded, agentprofile.LoadedPackState{
			Namespace:       fmt.Sprintf("loaded-%02d", i),
			DisplayName:     fmt.Sprintf("loaded-%02d", i),
			CurrentRevision: "1234567890abcdef",
			ProfileCount:    17,
			AutoSync:        true,
			Generation:      fmt.Sprintf("generation-%02d", i),
		})
	}
	card := renderAgentPackLibraryCardViewForSurface(
		agentprofile.PackCatalogSnapshot{Loaded: loaded},
		managementPackLibraryView{Section: managementPackLibrarySectionLoaded},
		"",
		managementSurfaceAmux,
	)
	if !strings.Contains(card, `"element_id":"amux_management_back"`) || !strings.Contains(card, "返回概览") {
		t.Fatalf("focused library card = %s, want stable overview back", card)
	}
	if strings.Contains(card, "返回助手设置") || strings.Contains(card, `"element_id":"amux_navigation"`) {
		t.Fatalf("focused library card = %s, should omit duplicate or legacy navigation", card)
	}
	if !strings.Contains(card, "loaded-00") {
		t.Fatalf("navigation library card = %s, should keep paged expert library content", card)
	}
	if got := countCardJSONTaggedNodes(t, card); got > managementPackLibraryMaxElements {
		t.Fatalf("navigation library card elements = %d, want <= %d", got, managementPackLibraryMaxElements)
	}
	if len(card) > managementPackLibraryMaxBytes {
		t.Fatalf("navigation library card bytes = %d, want <= %d", len(card), managementPackLibraryMaxBytes)
	}
}

func TestAmuxLibraryFallbackCardsUseStableOverviewBack(t *testing.T) {
	cards := map[string]string{
		"too_large": renderAgentPackLibraryPageTooLargeCard(managementPackLibraryView{
			Section: managementPackLibrarySectionLoaded,
		}),
		"unavailable": renderAgentPackLibraryFailureCard(),
		"result":      renderAgentPackOperationResultFallbackCard("专家库已更新。", true),
	}
	for name, content := range cards {
		card := renderManagementCardForSurface(content, managementSurfaceAmux, managementViewLibrary)
		if !strings.Contains(card, `"element_id":"amux_management_back"`) || !strings.Contains(card, "返回概览") {
			t.Fatalf("%s focused library card = %s, want stable overview back", name, card)
		}
		if strings.Contains(card, "返回助手设置") || strings.Contains(card, `"element_id":"amux_navigation"`) {
			t.Fatalf("%s focused library card = %s, should omit duplicate or legacy navigation", name, card)
		}
	}
}

func TestRemovedAmuxCommandsDoNotSendCards(t *testing.T) {
	b, hub := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	var sentCards int
	b.dmInteractive = func(_ context.Context, openID, content string) (string, string, error) {
		sentCards++
		return "om-removed-command", "", nil
	}

	for i, command := range []string{
		"/amux1",
		"/amux status",
		"/amux settings",
		"/amux setting",
		"/amux help",
		"/amux h",
		"/amux ?",
		"/amux onboarding",
	} {
		event := feishuMessageEvent("p2p", "oc-private", fmt.Sprintf("om-removed-amux-%d", i), "owner-open", command)
		if err := b.handleMessage(context.Background(), event); err != nil {
			t.Fatalf("handleMessage(%q): %v", command, err)
		}
	}
	if sentCards != 0 {
		t.Fatalf("removed commands sent %d cards, want none", sentCards)
	}
	if len(hub.routes) != 0 {
		t.Fatalf("removed commands created runtime routes: %+v", hub.routes)
	}
}

func TestUnknownAmuxSubcommandReturnsCurrentUsage(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	var notices []string
	b.dmNotice = func(_ context.Context, openID, text string) (string, string, error) {
		notices = append(notices, openID+":"+text)
		return "om-usage", "", nil
	}

	if err := b.handleCommand(context.Background(), "owner-open", "/amux unknown"); err != nil {
		t.Fatalf("handleCommand: %v", err)
	}
	if len(notices) != 1 || notices[0] != "owner-open:用法：/amux" {
		t.Fatalf("notices = %q, want current /amux usage", notices)
	}
}

func TestLegacyAmux1SurfaceCallbacksUseCurrentFocusedLayout(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{OpenId: "owner-open"}
	patched := make(chan string, 1)
	b.managementCardPatch = func(_ context.Context, _, content string) error {
		patched <- content
		return nil
	}

	resp, handled, err := b.handleManagementCardAction(context.Background(), &callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "owner-open"},
			Context:  &callback.Context{OpenMessageID: "om-legacy-amux1"},
			Action: &callback.CallBackAction{Value: map[string]interface{}{
				managementActionKey:  managementActionHelpOpen,
				managementSurfaceKey: "amux1",
			}},
		},
	})
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, handled = %v", resp, handled)
	}
	select {
	case card := <-patched:
		if !strings.Contains(card, `"element_id":"amux_management_back"`) || strings.Contains(card, `"element_id":"amux_navigation"`) {
			t.Fatalf("legacy callback card = %s, want current focused layout", card)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for legacy callback patch")
	}
}
