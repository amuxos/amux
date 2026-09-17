package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"

	"github.com/amuxos/amux/internal/config"
)

func TestListFeishuPermissionStatusesUsesOfficialTenantScopeAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
		case "/open-apis/application/v6/scopes":
			if got := r.Header.Get("Authorization"); got != "Bearer t-test" {
				t.Fatalf("scope authorization = %q", got)
			}
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","data":{"scopes":[`+
				`{"scope_name":"im:message.group_msg","grant_status":1,"scope_type":"tenant"},`+
				`{"scope_name":"docs:event:subscribe","grant_status":2,"scope_type":"tenant"},`+
				`{"scope_name":"user.phone:readonly","grant_status":1,"scope_type":"user"}`+
				`]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	b := &Bridge{client: lark.NewClient(
		"cli_test123", "secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithOAuthBaseUrl(server.URL),
	)}
	statuses, err := b.listFeishuPermissionStatuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if statuses["im:message.group_msg"] != 1 || statuses["docs:event:subscribe"] != 2 {
		t.Fatalf("tenant statuses = %v", statuses)
	}
	if _, ok := statuses["user.phone:readonly"]; ok {
		t.Fatalf("user scope leaked into tenant statuses: %v", statuses)
	}
}

func TestActiveFeishuTenantScopesFollowEnabledCapabilities(t *testing.T) {
	b := &Bridge{
		groupWatchState: groupWatchState{groupWatchConfig: config.BridgeFeishuGroupWatchConfig{
			Enabled: true,
			ChatIDs: []string{"oc_watch"},
		}},
		documentCommentController: documentCommentController{documentCommentsEnabled: true},
		botCoordinationState:      botCoordinationState{botToBot: config.BridgeFeishuBotToBotConfig{Enabled: true}},
	}
	want := normalizeFeishuScopes([]string{
		"application:app_slash_command:read",
		"application:app_slash_command:write",
		"application:bot.basic_info:read",
		"cardkit:card:write",
		"docs:document.comment:create",
		"docs:document.comment:read",
		"docs:event:subscribe",
		"docx:document:readonly",
		"im:chat.members:read",
		"im:chat:read",
		"im:message.group_at_msg.include_bot:readonly",
		"im:message.group_at_msg:readonly",
		"im:message.group_msg",
		"im:message.p2p_msg:readonly",
		"im:message:readonly",
		"im:message.reactions:write_only",
		"im:message:send_as_bot",
	})
	if got := testActiveFeishuTenantScopes(b); !reflect.DeepEqual(got, want) {
		t.Fatalf("active scopes = %v, want %v", got, want)
	}

	b.botToBot.Enabled = false
	b.groupWatchConfig.Enabled = false
	b.documentCommentsEnabled = false
	got := testActiveFeishuTenantScopes(b)
	for _, optional := range []string{
		"application:bot.basic_info:read",
		"im:chat.members:read",
		"im:message.group_at_msg.include_bot:readonly",
		"im:message.group_msg",
		"docs:event:subscribe",
	} {
		if slices.Contains(got, optional) {
			t.Fatalf("disabled capability scope %q remains active: %v", optional, got)
		}
	}
}

func TestOnlyExecutionFeishuCapabilitiesAreInactiveInSingletonMode(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.db.Close() })
	if _, err := db.GetBotProfileState("bridge-singleton", config.FeishuInteractionSingleton); err != nil {
		t.Fatal(err)
	}
	b := &Bridge{
		db:       db,
		bridgeID: "bridge-singleton",
		groupWatchState: groupWatchState{groupWatchConfig: config.BridgeFeishuGroupWatchConfig{
			Enabled: true,
			ChatIDs: []string{"oc_watch"},
		}},
		botCoordinationState: botCoordinationState{botToBot: config.BridgeFeishuBotToBotConfig{Enabled: true}},
	}
	got := testActiveFeishuTenantScopes(b)
	for _, active := range []string{"application:bot.basic_info:read", "im:chat.members:read", "im:message.group_at_msg.include_bot:readonly"} {
		if !slices.Contains(got, active) {
			t.Fatalf("singleton mode dropped Bot catalog scope %q: %v", active, got)
		}
	}
	for _, inactive := range []string{"im:message.group_msg"} {
		if slices.Contains(got, inactive) {
			t.Fatalf("singleton mode retained execution-only scope %q: %v", inactive, got)
		}
	}
}

func TestPermissionAuditConsolidatesAndPersistsNotificationDedupe(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.db.Close() })
	if _, err := db.GetBotProfileState("bridge-test", config.FeishuInteractionThread); err != nil {
		t.Fatal(err)
	}

	statuses := make(map[string]int)
	prototype := &Bridge{
		db:       db,
		bridgeID: "bridge-test",
		secrets:  &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
		groupWatchState: groupWatchState{groupWatchConfig: config.BridgeFeishuGroupWatchConfig{
			Enabled: true,
			ChatIDs: []string{"oc_watch"},
		}},
		botCoordinationState: botCoordinationState{botToBot: config.BridgeFeishuBotToBotConfig{Enabled: true}},
	}
	for _, scope := range testActiveFeishuTenantScopes(prototype) {
		statuses[scope] = 1
	}
	statuses["im:message.group_msg"] = 2
	statuses["im:message.group_at_msg.include_bot:readonly"] = 2

	notifications := 0
	var notificationCards []string
	newBridge := func() *Bridge {
		b := &Bridge{
			db:       db,
			bridgeID: "bridge-test",
			secrets:  &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
			groupWatchState: groupWatchState{groupWatchConfig: config.BridgeFeishuGroupWatchConfig{
				Enabled: true,
				ChatIDs: []string{"oc_watch"},
			}},
			botCoordinationState: botCoordinationState{botToBot: config.BridgeFeishuBotToBotConfig{Enabled: true}},
		}
		b.permissionScopeList = func(context.Context) (map[string]int, error) {
			copy := make(map[string]int, len(statuses))
			for scope, status := range statuses {
				copy[scope] = status
			}
			return copy, nil
		}
		b.dmInteractive = func(_ context.Context, openID, content string) (string, string, error) {
			notifications++
			notificationCards = append(notificationCards, content)
			if openID != "ou_owner" || !strings.Contains(content, "重新检测") {
				t.Fatalf("permission notification = openID %q card %s", openID, content)
			}
			return "om_audit", "", nil
		}
		return b
	}

	first := newBridge()
	if err := first.auditFeishuPermissions(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if notifications != 1 {
		t.Fatalf("notifications = %d, want 1", notifications)
	}
	if !strings.Contains(notificationCards[0], "智能体协作") || !strings.Contains(notificationCards[0], "群值守") {
		t.Fatalf("first notification did not consolidate affected capabilities: %s", notificationCards[0])
	}
	if issue := first.capabilityPermissionIssue(capabilityFeishuIntegration); issue == nil || len(issue.Scopes) != 2 {
		t.Fatalf("integration issue = %#v", issue)
	}

	// A fresh Bridge process sharing the same DB must not notify the same set.
	second := newBridge()
	if err := second.auditFeishuPermissions(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if notifications != 1 {
		t.Fatalf("notifications after restart = %d, want 1", notifications)
	}

	for scope := range statuses {
		statuses[scope] = 1
	}
	if err := second.auditFeishuPermissions(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	statuses["im:message.group_msg"] = 2
	third := newBridge()
	if err := third.auditFeishuPermissions(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if notifications != 2 {
		t.Fatalf("notifications after recovery and regression = %d, want 2", notifications)
	}
	if !strings.Contains(notificationCards[1], "群值守") || strings.Contains(notificationCards[1], "智能体协作") {
		t.Fatalf("regression notification = %s", notificationCards[1])
	}
}

func TestCapabilitiesCardShowsPermissionHealthAndRecheck(t *testing.T) {
	card := renderCapabilitiesCard(managementState{
		PermissionAudit: &capabilityPermissionIssue{
			Capability: capabilityFeishuIntegration,
			Name:       "飞书接入",
			Scopes:     []string{"im:message.group_at_msg.include_bot:readonly"},
			Affected:   []string{"智能体协作"},
			GrantURL:   "https://open.feishu.cn/app/cli_test123/auth?q=scope",
		},
	})
	for _, want := range []string{
		"智能体协作",
		"im:message.group_at_msg.include_bot:readonly",
		"申请权限",
		"开发者后台",
		"检查飞书权限",
		managementActionPermissionApply,
		managementActionPermissionRecheck,
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("capabilities card missing %q: %s", want, card)
		}
	}
}

func TestPermissionAuditFailureKeepsLastKnownIssuesAndReportsUnknownHealth(t *testing.T) {
	b := &Bridge{
		secrets:              &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
		botCoordinationState: botCoordinationState{botToBot: config.BridgeFeishuBotToBotConfig{Enabled: true}},
		accessScopePort: accessScopePort{permissionScopeList: func(context.Context) (map[string]int, error) {
			return nil, errors.New("scope API unavailable")
		}},
	}
	previous := capabilityPermissionIssue{
		Capability: capabilityFeishuIntegration,
		Name:       "飞书接入",
		Scopes:     []string{"im:message.group_at_msg.include_bot:readonly"},
		Affected:   []string{"智能体协作"},
	}
	b.recordCapabilityPermissionIssue(previous)
	if err := b.auditFeishuPermissions(context.Background(), false); err == nil {
		t.Fatal("permission audit unexpectedly succeeded")
	}
	if issue := b.capabilityPermissionIssue(capabilityFeishuIntegration); issue == nil || !sameCapabilityPermissionIssue(*issue, previous) {
		t.Fatalf("last known issue was rewritten: %#v", issue)
	}
	checked, failed := b.feishuPermissionAuditHealth()
	if checked || !failed {
		t.Fatalf("audit health checked/failed = %v/%v, want false/true", checked, failed)
	}
	card := renderCapabilitiesCard(managementState{PermissionAuditFailed: true})
	if !strings.Contains(card, "暂时无法检测") || !strings.Contains(card, managementActionPermissionRecheck) {
		t.Fatalf("unknown permission health card = %s", card)
	}
}

func TestDocumentCommentSourceRecoversAfterPermissionRecheck(t *testing.T) {
	subscriptions := 0
	b := &Bridge{
		documentCommentController: documentCommentController{documentCommentsConfigured: true},
		documentCommentAPI: documentCommentAPI{documentSubscribe: func(context.Context) error {
			subscriptions++
			return nil
		}},
	}
	if b.documentCommentsActive() {
		t.Fatal("document comments unexpectedly active before recovery")
	}
	if err := b.recoverDocumentCommentSource(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if subscriptions != 1 || !b.documentCommentsActive() {
		t.Fatalf("document comment recovery subscriptions/active = %d/%v", subscriptions, b.documentCommentsActive())
	}
	if err := b.recoverDocumentCommentSource(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if subscriptions != 1 {
		t.Fatalf("idempotent recovery subscriptions = %d, want 1", subscriptions)
	}
}

func TestPermissionNotificationClaimIsAtomicAcrossBridgeDBConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bridge.db")
	firstDB, err := openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstDB.db.Close() })
	secondDB, err := openBridgeDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondDB.db.Close() })

	claimed, err := firstDB.shouldNotifyFeishuPermissionAudit("bridge-claim", "scope-a")
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	claimed, err = secondDB.shouldNotifyFeishuPermissionAudit("bridge-claim", "scope-a")
	if err != nil || claimed {
		t.Fatalf("overlapping claim = %v, %v, want false/nil", claimed, err)
	}
	if err := firstDB.releaseFeishuPermissionAuditClaim("bridge-claim", "scope-a"); err != nil {
		t.Fatal(err)
	}
	claimed, err = secondDB.shouldNotifyFeishuPermissionAudit("bridge-claim", "scope-a")
	if err != nil || !claimed {
		t.Fatalf("claim after release = %v, %v", claimed, err)
	}
}

func TestPermissionAuditRetriesAfterOwnerBecomesAvailable(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.db.Close() })
	b := &Bridge{
		db:       db,
		bridgeID: "bridge-owner-retry",
		secrets:  &Secrets{AppId: "cli_test123"},
		accessScopePort: accessScopePort{permissionScopeList: func(context.Context) (map[string]int, error) {
			return map[string]int{}, nil
		}},
	}
	if err := b.auditFeishuPermissions(context.Background(), true); err == nil {
		t.Fatal("owner-less permission notification unexpectedly succeeded")
	}
	notified := 0
	b.secrets.OpenId = "ou_owner"
	b.dmInteractive = func(context.Context, string, string) (string, string, error) {
		notified++
		return "om_permission", "", nil
	}
	if err := b.auditFeishuPermissions(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if notified != 1 {
		t.Fatalf("notifications after owner capture = %d, want 1", notified)
	}
}

func TestAggregatedNotificationMarksCapabilityIssueNotified(t *testing.T) {
	b := &Bridge{
		secrets: &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
		groupWatchState: groupWatchState{groupWatchConfig: config.BridgeFeishuGroupWatchConfig{
			Enabled: true,
			ChatIDs: []string{"oc_watch"},
		}},
	}
	scopeLists := 0
	b.permissionScopeList = func(context.Context) (map[string]int, error) {
		scopeLists++
		return map[string]int{}, nil
	}
	b.dmInteractive = func(context.Context, string, string) (string, string, error) {
		return "om_permission", "", nil
	}
	permissionErr := feishuAPIResponseError(
		"lark list messages", "cli_test123",
		larkcore.CodeError{Code: 230027, Msg: "Lack of necessary permissions"},
		"im:message.group_msg",
	)
	b.observeCapabilityPermissionError(context.Background(), capabilityGroupWatch, "群值守", permissionErr, true)
	b.observeCapabilityPermissionError(context.Background(), capabilityGroupWatch, "群值守", permissionErr, true)
	memberErr := feishuAPIResponseErrorForRequiredScopes(
		"lark list chat Bots", "cli_test123",
		larkcore.CodeError{Code: 99991672, Msg: "Access denied"},
		"im:chat.members:read",
	)
	b.observeCapabilityPermissionError(context.Background(), capabilityGroupWatch, "群值守", memberErr, true)
	if scopeLists != 1 {
		t.Fatalf("scope catalog calls for repeated operational error = %d, want 1", scopeLists)
	}
	issue := b.capabilityPermissionIssue(capabilityGroupWatch)
	if issue == nil || !slices.Contains(issue.Scopes, "im:message.group_msg") || !slices.Contains(issue.Scopes, "im:chat.members:read") {
		t.Fatalf("repeated subset error lost the aggregated issue: %+v", issue)
	}
	b.resolveCapabilityPermissionScopes(capabilityGroupWatch, "im:chat.members:read")
	b.observeCapabilityPermissionError(context.Background(), capabilityGroupWatch, "群值守", memberErr, true)
	if scopeLists != 2 {
		t.Fatalf("scope catalog calls after a resolved permission fails again = %d, want 2", scopeLists)
	}
}

func TestPermissionRecheckSingleflightAndNavigationInvalidation(t *testing.T) {
	b := &Bridge{}
	version, ok := b.beginFeishuPermissionRecheck("om-card")
	if !ok {
		t.Fatal("first permission recheck was not accepted")
	}
	if _, ok := b.beginFeishuPermissionRecheck("om-card"); ok {
		t.Fatal("duplicate permission recheck was accepted")
	}
	if !b.feishuPermissionRecheckCurrent("om-card", version) {
		t.Fatal("active permission recheck was not current")
	}
	b.invalidateFeishuPermissionRecheck("om-card")
	if b.feishuPermissionRecheckCurrent("om-card", version) {
		t.Fatal("navigated-away permission recheck remained current")
	}
	patches := 0
	b.managementCardPatch = func(context.Context, string, string) error {
		patches++
		return nil
	}
	b.writeManagementCardFromCallbackIf(
		context.Background(), "ou_owner", "om-card", `{}`,
		func() bool { return b.feishuPermissionRecheckCurrent("om-card", version) },
	)
	if patches != 0 {
		t.Fatalf("stale permission recheck patches = %d, want 0", patches)
	}
	if _, ok := b.beginFeishuPermissionRecheck("om-card"); !ok {
		t.Fatal("permission recheck could not restart after invalidation")
	}
}

func TestApplyFeishuCapabilityScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
		case r.URL.Path == "/open-apis/application/v6/scopes/apply" && r.Method == http.MethodPost:
			if got := r.Header.Get("Authorization"); got != "Bearer t-test" {
				t.Fatalf("apply authorization = %q", got)
			}
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"success","data":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	b := &Bridge{client: lark.NewClient(
		"cli_test123", "secret",
		lark.WithOpenBaseUrl(server.URL),
		lark.WithOAuthBaseUrl(server.URL),
	)}
	if err := b.applyFeishuCapabilityScopes(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestApplyFeishuCapabilityScopesErrorNotice(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{
		{212001, "高敏权限"},
		{212002, "没有可继续申请"},
		{212003, "上限"},
		{212004, "已申请过"},
		{99999, "申请失败"},
	} {
		err := fmt.Errorf("apply Feishu scopes: %w", larkcore.CodeError{Code: tc.code, Msg: "boom"})
		if notice := feishuScopeApplyErrorNotice(err); !strings.Contains(notice, tc.want) {
			t.Fatalf("code %d notice = %q, want containing %q", tc.code, notice, tc.want)
		}
	}
	if notice := feishuScopeApplyErrorNotice(errors.New("network down")); !strings.Contains(notice, "申请失败") {
		t.Fatalf("plain error notice = %q", notice)
	}
}

func TestPermissionApplySingleflight(t *testing.T) {
	b := &Bridge{}
	version, ok := b.beginFeishuPermissionApply("om-card")
	if !ok {
		t.Fatal("first permission apply was not accepted")
	}
	if _, ok := b.beginFeishuPermissionApply("om-card"); ok {
		t.Fatal("duplicate permission apply was accepted")
	}
	if !b.feishuPermissionApplyCurrent("om-card", version) {
		t.Fatal("active permission apply was not current")
	}
	b.finishFeishuPermissionApply("om-card", version)
	if _, ok := b.beginFeishuPermissionApply("om-card"); !ok {
		t.Fatal("permission apply could not restart after completion")
	}
}

func TestPermissionApplyActionAppliesAndPatchesCard(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
		case r.URL.Path == "/open-apis/application/v6/scopes/apply" && r.Method == http.MethodPost:
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"success","data":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	b := &Bridge{
		secrets: &Secrets{AppId: "cli_test123", OpenId: "owner-open"},
		client: lark.NewClient(
			"cli_test123", "secret",
			lark.WithOpenBaseUrl(server.URL),
			lark.WithOAuthBaseUrl(server.URL),
		),
	}
	b.permissionScopeList = func(context.Context) (map[string]int, error) {
		statuses := make(map[string]int)
		for _, scope := range testActiveFeishuTenantScopes(b) {
			statuses[scope] = 1
		}
		return statuses, nil
	}
	patched := make(chan string, 4)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}
	resp, handled, err := b.handleManagementCardAction(
		context.Background(),
		settingsCallback(managementActionPermissionApply, nil),
	)
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" ||
		!strings.Contains(resp.Toast.Content, "正在向管理员申请权限") {
		t.Fatalf("response = %#v, handled = %v", resp, handled)
	}
	card := waitForManagementCard(t, patched, func(card string) bool {
		return strings.Contains(card, "已向管理员发起权限申请")
	})
	if !strings.Contains(card, "当前未发现缺失") {
		t.Fatalf("permission apply card = %s", card)
	}
}

func TestFeishuRegistrationGrantURL(t *testing.T) {
	orig := feishuRegistrationBeginFn
	defer func() { feishuRegistrationBeginFn = orig }()
	feishuRegistrationBeginFn = func(_ context.Context, domain string) (*feishuRegistrationBeginResponse, error) {
		if domain != "https://accounts.feishu.cn" {
			t.Fatalf("registration domain = %q", domain)
		}
		return &feishuRegistrationBeginResponse{
			DeviceCode:              "dc-test",
			VerificationURIComplete: "https://open.feishu.cn/page/launcher?user_code=ABCD-EFGH",
		}, nil
	}
	b := &Bridge{secrets: &Secrets{AppId: "cli_test123", OpenId: "ou_owner"}}
	u, err := b.feishuRegistrationGrantURL(context.Background(), []string{"docs:event:subscribe"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"https://open.feishu.cn/page/launcher",
		"user_code=ABCD-EFGH",
		"from=sdk",
		"source=amux",
		"clientID=cli_test123",
		"addons=",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("grant URL %q missing %q", u, want)
		}
	}
}

func TestGrantAddonsForScopesCollectsCapabilityDeclarations(t *testing.T) {
	addons := grantAddonsForScopes([]string{"im:message.group_msg", "docs:event:subscribe"})
	for _, scope := range []string{
		"im:message.group_msg",
		"docs:event:subscribe",
		"docs:document.comment:create",
		"docs:document.comment:read",
		"docx:document:readonly",
	} {
		if !slices.Contains(addons.Scopes.Tenant, scope) {
			t.Fatalf("addons scopes missing %q: %v", scope, addons.Scopes.Tenant)
		}
	}
	if !slices.Contains(addons.Events.Items.Tenant, "drive.notice.comment_add_v1") {
		t.Fatalf("addons events missing drive.notice.comment_add_v1: %v", addons.Events.Items.Tenant)
	}
	if len(addons.Callbacks.Items) != 0 {
		t.Fatalf("group_watch/document_comments should not add callbacks: %v", addons.Callbacks.Items)
	}
}

func TestPermissionGrantGuideCard(t *testing.T) {
	card := renderPermissionGrantGuideCard(
		capabilityPermissionIssue{
			Capability: capabilityFeishuIntegration,
			Name:       "飞书接入",
			Scopes:     []string{"docs:event:subscribe"},
			GrantURL:   "https://open.feishu.cn/app/cli_test123/auth?q=scope",
		},
		"这些权限尚未在开发者后台勾选",
		"https://open.feishu.cn/page/launcher?user_code=ABCD-EFGH",
	)
	for _, want := range []string{
		"申请确认页",
		"https://open.feishu.cn/page/launcher?user_code=ABCD-EFGH",
		"开发者后台",
		"重新检测",
		managementActionPermissionRecheck,
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("grant guide card missing %q: %s", want, card)
		}
	}
}

func TestPermissionApplyFailedGuidesToConfirmPage(t *testing.T) {
	orig := feishuRegistrationBeginFn
	defer func() { feishuRegistrationBeginFn = orig }()
	feishuRegistrationBeginFn = func(_ context.Context, _ string) (*feishuRegistrationBeginResponse, error) {
		return &feishuRegistrationBeginResponse{
			DeviceCode:              "dc-test",
			VerificationURIComplete: "https://open.feishu.cn/page/launcher?user_code=ABCD-EFGH",
		}, nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
		case r.URL.Path == "/open-apis/application/v6/scopes/apply" && r.Method == http.MethodPost:
			_, _ = fmt.Fprint(w, `{"code":212002,"msg":"unauthorized scopes were empty","data":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	b := &Bridge{
		secrets: &Secrets{AppId: "cli_test123", OpenId: "owner-open"},
		client: lark.NewClient(
			"cli_test123", "secret",
			lark.WithOpenBaseUrl(server.URL),
			lark.WithOAuthBaseUrl(server.URL),
		),
	}
	b.recordCapabilityPermissionIssue(capabilityPermissionIssue{
		Capability: capabilityFeishuIntegration,
		Name:       "飞书接入",
		Scopes:     []string{"docs:event:subscribe"},
		GrantURL:   "https://open.feishu.cn/app/cli_test123/auth?q=scope",
	})
	patched := make(chan string, 4)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}
	resp, handled, err := b.handleManagementCardAction(
		context.Background(),
		settingsCallback(managementActionPermissionApply, nil),
	)
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil || resp.Toast == nil || resp.Toast.Type != "success" {
		t.Fatalf("response = %#v, handled = %v", resp, handled)
	}
	card := waitForManagementCard(t, patched, func(card string) bool {
		return strings.Contains(card, "申请确认页")
	})
	for _, want := range []string{
		"没有可继续申请",
		"page/launcher",
		"user_code=ABCD-EFGH",
		"clientID=cli_test123",
		managementActionPermissionRecheck,
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("grant guide card missing %q: %s", want, card)
		}
	}
}

func TestPermissionApplyEmptyUnauthorizedRechecksGrantedScopes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"t-test","expire":7200}`)
		case r.URL.Path == "/open-apis/application/v6/scopes/apply" && r.Method == http.MethodPost:
			_, _ = fmt.Fprint(w, `{"code":212002,"msg":"unauthorized scopes were empty","data":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	b := &Bridge{
		secrets: &Secrets{AppId: "cli_test123", OpenId: "owner-open"},
		client: lark.NewClient(
			"cli_test123", "secret",
			lark.WithOpenBaseUrl(server.URL),
			lark.WithOAuthBaseUrl(server.URL),
		),
	}
	b.permissionScopeList = func(context.Context) (map[string]int, error) {
		statuses := make(map[string]int)
		for _, scope := range testActiveFeishuTenantScopes(b) {
			statuses[scope] = 1
		}
		return statuses, nil
	}
	b.recordCapabilityPermissionIssue(capabilityPermissionIssue{
		Capability: capabilityFeishuIntegration,
		Name:       "飞书接入",
		Scopes:     []string{"im:message:readonly"},
	})
	patched := make(chan string, 4)
	b.managementCardPatch = func(_ context.Context, _ string, content string) error {
		patched <- content
		return nil
	}
	resp, handled, err := b.handleManagementCardAction(
		context.Background(),
		settingsCallback(managementActionPermissionApply, nil),
	)
	if err != nil {
		t.Fatalf("handleManagementCardAction: %v", err)
	}
	if !handled || resp == nil {
		t.Fatalf("response = %#v, handled = %v", resp, handled)
	}
	card := waitForManagementCard(t, patched, func(card string) bool {
		return strings.Contains(card, "权限均已满足")
	})
	if strings.Contains(card, "申请确认页") || strings.Contains(card, "仍有权限需要补齐") {
		t.Fatalf("granted scopes still rendered missing guidance: %s", card)
	}
}

func TestPermissionApplyRecheckRecoversDocumentComments(t *testing.T) {
	subscriptions := 0
	b := &Bridge{
		secrets:                   &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
		documentCommentController: documentCommentController{documentCommentsConfigured: true},
		documentCommentAPI: documentCommentAPI{documentSubscribe: func(context.Context) error {
			subscriptions++
			return nil
		}},
	}
	statuses := make(map[string]int)
	b.permissionScopeList = func(context.Context) (map[string]int, error) {
		return statuses, nil
	}
	// While the catalog reports no grants, the audit keeps the capability issue.
	if err := b.auditFeishuPermissions(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if b.capabilityPermissionIssue(capabilityDocumentComments) == nil {
		t.Fatal("empty scope catalog did not leave a document comment issue")
	}
	// Once the apply takes effect, the recheck path clears the issue and
	// re-establishes the document comment subscription.
	for _, scope := range testActiveFeishuTenantScopes(b) {
		statuses[scope] = 1
	}
	if err := b.auditFeishuPermissions(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if b.capabilityPermissionIssue(capabilityDocumentComments) != nil {
		t.Fatal("granted catalog kept a document comment issue")
	}
	if err := b.recoverDocumentCommentSource(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if subscriptions != 1 || !b.documentCommentsActive() {
		t.Fatalf("document comment recovery after apply subscriptions/active = %d/%v", subscriptions, b.documentCommentsActive())
	}
}

func TestOperationalPermissionScopesResolveIndependently(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.db.Close() })
	b := &Bridge{
		db:                        db,
		bridgeID:                  "bridge-document-scopes",
		secrets:                   &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
		documentCommentController: documentCommentController{documentCommentsConfigured: true},
	}
	b.permissionScopeList = func(context.Context) (map[string]int, error) {
		statuses := make(map[string]int)
		for _, scope := range testActiveFeishuTenantScopes(b) {
			statuses[scope] = 1
		}
		return statuses, nil
	}
	observed := capabilityPermissionIssue{
		Capability: capabilityDocumentComments,
		Name:       "云文档评论",
		Scopes: []string{
			"docs:event:subscribe",
			"docs:document.comment:read",
			"docs:document.comment:create",
		},
	}
	if err := b.auditFeishuPermissionsWithObservedIssue(context.Background(), false, &observed); err != nil {
		t.Fatal(err)
	}
	b.resolveCapabilityPermissionScopes(capabilityDocumentComments, "docs:event:subscribe")
	issue := b.capabilityPermissionIssue(capabilityDocumentComments)
	if issue == nil || slices.Contains(issue.Scopes, "docs:event:subscribe") ||
		!slices.Contains(issue.Scopes, "docs:document.comment:read") ||
		!slices.Contains(issue.Scopes, "docs:document.comment:create") {
		t.Fatalf("issue after subscription recovery = %#v", issue)
	}

	// Verify the scope-granular remainder survives a fresh process and a
	// catalog that still reports everything granted.
	restarted := &Bridge{
		db:                        db,
		bridgeID:                  "bridge-document-scopes",
		secrets:                   &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
		documentCommentController: documentCommentController{documentCommentsConfigured: true},
		accessScopePort:           accessScopePort{permissionScopeList: b.permissionScopeList},
	}
	if err := restarted.auditFeishuPermissions(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	issue = restarted.capabilityPermissionIssue(capabilityDocumentComments)
	if issue == nil || len(issue.Scopes) != 2 {
		t.Fatalf("persisted scope-granular issue = %#v", issue)
	}
	restarted.resolveCapabilityPermissionScopes(
		capabilityDocumentComments,
		"docs:document.comment:read",
		"docs:document.comment:create",
	)
	if issue := restarted.capabilityPermissionIssue(capabilityDocumentComments); issue != nil {
		t.Fatalf("all operational scopes resolved but issue remains: %#v", issue)
	}
}

func TestFeishuPermissionGrantURLUsesCurrentAppAndTenantScopes(t *testing.T) {
	raw := feishuPermissionGrantURL("cli_test123", []string{
		"im:message.group_msg",
		"docs:event:subscribe",
		"im:message.group_msg",
	})
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("Parse grant URL: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "open.feishu.cn" || parsed.Path != "/app/cli_test123/auth" {
		t.Fatalf("grant URL = %q, want official app permission page", raw)
	}
	if got := parsed.Query().Get("q"); got != "docs:event:subscribe,im:message.group_msg" {
		t.Fatalf("grant URL scopes = %q", got)
	}
	if parsed.Query().Get("op_from") != "openapi" || parsed.Query().Get("token_type") != "tenant" {
		t.Fatalf("grant URL query = %v", parsed.Query())
	}
	if got := feishuPermissionGrantURL("https://evil.example", []string{"im:message.group_msg"}); got != "" {
		t.Fatalf("unsafe app ID produced grant URL %q", got)
	}
}

func TestFeishuAPIResponseErrorClassifiesGroupWatchPermissionCode(t *testing.T) {
	err := feishuAPIResponseError(
		"lark list messages",
		"cli_test123",
		larkcore.CodeError{Code: 230027, Msg: "Lack of necessary permissions"},
		"im:message.group_msg",
	)
	var permissionErr *feishuPermissionError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("error = %T %v, want feishuPermissionError", err, err)
	}
	if got := strings.Join(permissionErr.scopes, ","); got != "im:message.group_msg" {
		t.Fatalf("permission scopes = %q", got)
	}
	if !strings.Contains(permissionErr.grantURL, "/app/cli_test123/auth") {
		t.Fatalf("grant URL = %q", permissionErr.grantURL)
	}
}

func TestFeishuAPIResponseErrorForRequiredScopesIgnoresAlternatives(t *testing.T) {
	codeError := larkcore.CodeError{Code: 99991672, Msg: "one of these scopes is required"}
	codeError.Err = &struct {
		LogID                string                                   `json:"log_id,omitempty"`
		Troubleshooter       string                                   `json:"troubleshooter,omitempty"`
		Details              []*larkcore.CodeErrorDetail              `json:"details,omitempty"`
		PermissionViolations []*larkcore.CodeErrorPermissionViolation `json:"permission_violations,omitempty"`
		FieldViolations      []*larkcore.CodeErrorFieldViolation      `json:"field_violations,omitempty"`
		Helps                []*larkcore.CodeErrorHelp                `json:"helps,omitempty"`
	}{
		PermissionViolations: []*larkcore.CodeErrorPermissionViolation{
			{Subject: "im:message"},
			{Subject: "im:message.history:readonly"},
			{Subject: "im:message:readonly"},
		},
	}
	err := feishuAPIResponseErrorForRequiredScopes(
		"lark get message image", "cli_test123", codeError, "im:message:readonly",
	)
	var permissionErr *feishuPermissionError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("error = %T %v, want feishuPermissionError", err, err)
	}
	if got := strings.Join(permissionErr.scopes, ","); got != "im:message:readonly" {
		t.Fatalf("permission scopes = %q, want selected least-privilege scope", got)
	}
}

func TestPermissionAuditDropsStaleAlternativeOperationalScopes(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.db.Close() })
	b := &Bridge{
		db:       db,
		bridgeID: "bridge-test",
		secrets:  &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
	}
	statuses := make(map[string]int)
	for _, scope := range testActiveFeishuTenantScopes(b) {
		statuses[scope] = 1
	}
	b.permissionScopeList = func(context.Context) (map[string]int, error) { return statuses, nil }
	if err := db.upsertOperationalPermissionIssue("bridge-test", capabilityPermissionIssue{
		Capability: capabilityCoreMessaging,
		Name:       "图片消息",
		Scopes:     []string{"im:message", "im:message.history:readonly"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.auditFeishuPermissions(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if issue := b.capabilityPermissionIssue(capabilityFeishuIntegration); issue != nil {
		t.Fatalf("stale alternative scopes remain as missing: %#v", issue)
	}
	persisted, err := db.operationalPermissionIssues("bridge-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 0 {
		t.Fatalf("persisted stale alternatives = %#v", persisted)
	}
}

func TestPermissionAuditPreservesObservedScopesForUndeclaredCapability(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.db.Close() })
	b := &Bridge{
		db:       db,
		bridgeID: "bridge-test",
		secrets:  &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
	}
	b.permissionScopeList = func(context.Context) (map[string]int, error) {
		return map[string]int{"custom:scope": 1}, nil
	}
	observed := capabilityPermissionIssue{
		Capability: "custom_runtime_capability",
		Name:       "动态能力",
		Scopes:     []string{"custom:scope"},
	}
	if err := b.auditFeishuPermissionsWithObservedIssue(context.Background(), false, &observed); err != nil {
		t.Fatal(err)
	}
	issue := b.capabilityPermissionIssue("custom_runtime_capability")
	if issue == nil || strings.Join(issue.Scopes, ",") != "custom:scope" {
		t.Fatalf("observed dynamic capability issue = %#v, want operational scope preserved", issue)
	}
	persisted, err := db.operationalPermissionIssues("bridge-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 1 || strings.Join(persisted[0].Scopes, ",") != "custom:scope" {
		t.Fatalf("persisted dynamic capability issue = %#v", persisted)
	}
}

func TestCapabilityPermissionNotificationIsDeduplicatedUntilRecovery(t *testing.T) {
	b := &Bridge{secrets: &Secrets{AppId: "cli_test123", OpenId: "ou_owner"}}
	calls := 0
	var card string
	b.dmInteractive = func(_ context.Context, openID, content string) (string, string, error) {
		if openID != "ou_owner" {
			t.Fatalf("notification open ID = %q", openID)
		}
		calls++
		card = content
		return "om_notice", "", nil
	}
	err := feishuAPIResponseError(
		"lark list messages",
		b.secrets.AppId,
		larkcore.CodeError{Code: 230027, Msg: "Lack of necessary permissions"},
		"im:message.group_msg",
	)

	b.observeCapabilityPermissionError(context.Background(), capabilityGroupWatch, "群值守", err, true)
	b.observeCapabilityPermissionError(context.Background(), capabilityGroupWatch, "群值守", err, true)
	if calls != 1 {
		t.Fatalf("permission notifications = %d, want 1", calls)
	}
	if !strings.Contains(card, "im:message.group_msg") || !strings.Contains(card, "申请权限") ||
		!strings.Contains(card, "https://open.feishu.cn/app/cli_test123/auth") {
		t.Fatalf("permission card = %s", card)
	}
	if issue := b.capabilityPermissionIssue(capabilityGroupWatch); issue == nil {
		t.Fatal("group watch permission issue was not recorded")
	}

	b.clearCapabilityPermissionIssue(capabilityGroupWatch)
	b.observeCapabilityPermissionError(context.Background(), capabilityGroupWatch, "群值守", err, true)
	if calls != 2 {
		t.Fatalf("permission notifications after recovery = %d, want 2", calls)
	}
}

func TestCapabilityPermissionNotificationFallsBackToTextAndStillDeduplicates(t *testing.T) {
	b := &Bridge{secrets: &Secrets{AppId: "cli_test123", OpenId: "ou_owner"}}
	interactiveCalls := 0
	textCalls := 0
	b.dmInteractive = func(context.Context, string, string) (string, string, error) {
		interactiveCalls++
		return "", "", errors.New("card unavailable")
	}
	b.dmNotice = func(_ context.Context, openID, text string) (string, string, error) {
		textCalls++
		if openID != "ou_owner" || !strings.Contains(text, "申请权限：https://open.feishu.cn/app/cli_test123/auth") {
			t.Fatalf("fallback notification = openID %q text %q", openID, text)
		}
		return "om_text", "", nil
	}
	err := feishuAPIResponseError(
		"lark list messages",
		b.secrets.AppId,
		larkcore.CodeError{Code: 230027, Msg: "Lack of necessary permissions"},
		"im:message.group_msg",
	)

	b.observeCapabilityPermissionError(context.Background(), capabilityGroupWatch, "群值守", err, true)
	b.observeCapabilityPermissionError(context.Background(), capabilityGroupWatch, "群值守", err, true)
	if interactiveCalls != 1 || textCalls != 1 {
		t.Fatalf("notification calls interactive/text = %d/%d, want 1/1", interactiveCalls, textCalls)
	}
}

func TestCapabilityPermissionFallbackDedupePersistsWhenAuditAPIIsUnavailable(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.db.Close() })
	calls := 0
	newBridge := func() *Bridge {
		b := &Bridge{
			db:       db,
			bridgeID: "bridge-fallback",
			secrets:  &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
		}
		b.dmInteractive = func(context.Context, string, string) (string, string, error) {
			calls++
			return "om_permission", "", nil
		}
		return b
	}
	permissionErr := feishuAPIResponseError(
		"lark list messages",
		"cli_test123",
		larkcore.CodeError{Code: 230027, Msg: "Lack of necessary permissions"},
		"im:message.group_msg",
	)
	newBridge().observeCapabilityPermissionError(
		context.Background(), capabilityGroupWatch, "群值守", permissionErr, true,
	)
	newBridge().observeCapabilityPermissionError(
		context.Background(), capabilityGroupWatch, "群值守", permissionErr, true,
	)
	if calls != 1 {
		t.Fatalf("fallback notifications across restart = %d, want 1", calls)
	}
}

func TestObservedPermissionErrorOverridesLaggingGrantedScopeCatalog(t *testing.T) {
	db, err := openBridgeDB(filepath.Join(t.TempDir(), "bridge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.db.Close() })
	if _, err := db.GetBotProfileState("bridge-observed", config.FeishuInteractionThread); err != nil {
		t.Fatal(err)
	}
	notifications := 0
	newBridge := func() *Bridge {
		b := &Bridge{
			db:       db,
			bridgeID: "bridge-observed",
			secrets:  &Secrets{AppId: "cli_test123", OpenId: "ou_owner"},
			groupWatchState: groupWatchState{groupWatchConfig: config.BridgeFeishuGroupWatchConfig{
				Enabled: true,
				ChatIDs: []string{"oc_watch"},
			}},
		}
		b.permissionScopeList = func(context.Context) (map[string]int, error) {
			statuses := make(map[string]int)
			for _, scope := range testActiveFeishuTenantScopes(b) {
				statuses[scope] = 1
			}
			return statuses, nil
		}
		b.dmInteractive = func(_ context.Context, _ string, _ string) (string, string, error) {
			notifications++
			return "om_permission", "", nil
		}
		return b
	}
	permissionErr := feishuAPIResponseError(
		"lark list messages",
		"cli_test123",
		larkcore.CodeError{Code: 230027, Msg: "Lack of necessary permissions"},
		"im:message.group_msg",
	)
	first := newBridge()
	first.observeCapabilityPermissionError(
		context.Background(), capabilityGroupWatch, "群值守", permissionErr, true,
	)
	if notifications != 1 {
		t.Fatalf("observed permission notifications = %d, want 1", notifications)
	}
	if issue := first.capabilityPermissionIssue(capabilityGroupWatch); issue == nil || len(issue.Scopes) != 1 {
		t.Fatalf("observed capability issue = %#v", issue)
	}

	// A fresh process must preserve stronger operational evidence even while
	// the catalog still reports the scope granted, without notifying twice.
	second := newBridge()
	if err := second.auditFeishuPermissions(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if notifications != 1 {
		t.Fatalf("notifications after lagging catalog restart = %d, want 1", notifications)
	}
	if issue := second.capabilityPermissionIssue(capabilityGroupWatch); issue == nil {
		t.Fatal("persisted operational issue was cleared by the granted catalog")
	}

	second.resolveCapabilityPermissionScopes(capabilityGroupWatch, "im:message.group_msg")
	if err := second.auditFeishuPermissions(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if issue := second.capabilityPermissionIssue(capabilityFeishuIntegration); issue != nil {
		t.Fatalf("resolved operational issue remains after real API success: %#v", issue)
	}
}

// testActiveFeishuTenantScopes mirrors the deleted activeFeishuTenantScopes
// aggregation so capability tests can still assert the effective tenant scope
// set for a given bridge configuration.
func testActiveFeishuTenantScopes(b *Bridge) []string {
	var scopes []string
	for _, capability := range b.activeFeishuCapabilitySpecs() {
		scopes = append(scopes, capability.TenantScopes...)
	}
	return normalizeFeishuScopes(scopes)
}
