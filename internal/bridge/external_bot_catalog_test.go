package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
)

func TestExternalBotCatalogPermissionFailureUsesCatalogScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
		case "/open-apis/im/v1/chats/oc-shared":
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"name":"devbox"}}`)
		case "/open-apis/im/v1/chats/oc-shared/members/list":
			_, _ = fmt.Fprint(w, `{"code":99991672,"msg":"missing permission"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	b := &Bridge{
		client:  lark.NewClient("cli-amux", "secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL)),
		secrets: &Secrets{AppId: "cli-amux", AppSecret: "secret"},
	}
	_, err := b.fetchExternalBotCatalog(context.Background(), "oc-shared")
	var permissionErr *feishuPermissionError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("error = %T %v, want feishuPermissionError", err, err)
	}
	if len(permissionErr.scopes) != 1 || permissionErr.scopes[0] != externalBotCatalogScope {
		t.Fatalf("permission scopes = %v", permissionErr.scopes)
	}
}

func TestExternalBotCatalogRefreshesPagedBotsWithDisplayNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
		case "/open-apis/im/v1/chats/oc-shared":
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"name":"devbox"}}`)
		case "/open-apis/im/v1/chats/oc-shared/members/list":
			if got := r.URL.Query().Get("member_types"); got != "bot" {
				t.Errorf("member_types = %q", got)
			}
			if got := r.URL.Query().Get("member_id_type"); got != "open_id" {
				t.Errorf("member_id_type = %q", got)
			}
			if r.URL.Query().Get("page_token") == "next" {
				_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"bots":[{"member_id":"ou-helper","app_id":"cli-helper","name":"Helper"}],"has_more":false,"page_token":"","truncations":[]}}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"bots":[{"member_id":"ou-amux","app_id":"cli-amux","name":"amux"},{"member_id":"ou-docman","app_id":"cli-docman","name":"docman 智能体"}],"has_more":true,"page_token":"next","truncations":[]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	b, _ := newBridgeForInputTest(t)
	b.secrets = &Secrets{AppId: "cli-amux", AppSecret: "secret", BotOpenId: "ou-amux"}
	b.client = lark.NewClient("cli-amux", "secret", lark.WithOpenBaseUrl(server.URL), lark.WithOAuthBaseUrl(server.URL))
	b.botToBot.Enabled = true
	if err := b.db.SetWorkspaceMode(b.currentBridgeId(), "oc-shared", WorkspaceModeOwnerOnly); err != nil {
		t.Fatal(err)
	}
	if err := b.refreshKnownExternalBotCatalog(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	peers, err := b.db.ListExternalBotPeers(b.currentBridgeId())
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("peers = %+v, want docman and helper without current app", peers)
	}
	got := map[string]externalBotPeer{}
	for _, peer := range peers {
		got[peer.BotOpenID] = peer
	}
	if got["ou-docman"].BotDisplayName != "docman 智能体" || got["ou-docman"].ChatDisplayName != "devbox" || got["ou-docman"].BotAppID != "cli-docman" {
		t.Fatalf("docman peer = %+v", got["ou-docman"])
	}
	if _, ok := got["ou-amux"]; ok {
		t.Fatalf("current app leaked into external catalog: %+v", peers)
	}
}

func TestExternalBotCatalogPartialSnapshotPreservesUnknownAndCompleteSnapshotRemovesIt(t *testing.T) {
	b, _ := newBridgeForInputTest(t)
	b.botToBot.Enabled = true
	chatID := "oc-shared"
	stale := externalBotPeer{
		CollaboratorID: externalBotCollaboratorID(b.currentBridgeId(), chatID, "ou-stale"),
		BridgeID:       b.currentBridgeId(), ChatID: chatID, BotOpenID: "ou-stale", BotDisplayName: "old bot",
	}
	if err := b.db.UpsertExternalBotPeer(stale); err != nil {
		t.Fatal(err)
	}
	b.externalBotCatalogFetch = func(context.Context, string) (externalBotCatalogSnapshot, error) {
		return externalBotCatalogSnapshot{
			ChatDisplayName: "devbox",
			Members:         []externalBotCatalogMember{{BotOpenID: "ou-docman", BotAppID: "cli-docman", DisplayName: "docman"}},
			Complete:        false,
		}, nil
	}
	if err := b.refreshExternalBotCatalogChat(context.Background(), chatID, true); err != nil {
		t.Fatal(err)
	}
	peers, _ := b.db.ListExternalBotPeers(b.currentBridgeId())
	if len(peers) != 2 {
		t.Fatalf("partial peers = %+v, want stale peer preserved", peers)
	}
	b.externalBotCatalogFetch = func(context.Context, string) (externalBotCatalogSnapshot, error) {
		return externalBotCatalogSnapshot{
			ChatDisplayName: "devbox",
			Members:         []externalBotCatalogMember{{BotOpenID: "ou-docman", BotAppID: "cli-docman", DisplayName: "docman"}},
			Complete:        true,
		}, nil
	}
	if err := b.refreshExternalBotCatalogChat(context.Background(), chatID, true); err != nil {
		t.Fatal(err)
	}
	peers, _ = b.db.ListExternalBotPeers(b.currentBridgeId())
	if len(peers) != 1 || peers[0].BotOpenID != "ou-docman" {
		t.Fatalf("complete peers = %+v, want only current docman", peers)
	}
}
