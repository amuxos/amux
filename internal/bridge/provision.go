package bridge

// Feishu bot provisioning: create and configure the 企业自建应用 that backs a
// bridge bot without hand-clicking the Open Platform console.
//
// The default path is the official SDK registration flow:
//
//   - create the app via the Feishu device-code flow
//     (scene/registration.RegisterApp, archetype=PersonalAgent);
//   - pre-fill the exact scopes, events and callbacks declared below through
//     registration Addons with the minimal base template;
//   - one QR confirmation yields app_id + app_secret and the scanning user's
//     open_id.
//
// The legacy console automation in provision_console*.go is retained only as
// an explicit fallback. It reverse-engineers internal console endpoints and is
// never part of the default provisioning path.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/larksuite/oapi-sdk-go/v3/scene/registration"
	"github.com/mdp/qrterminal/v3"
)

// ScopeManifest is the legacy console fallback projection of FeishuAppManifest.
type ScopeManifest struct {
	Tenant []string
	User   []string
}

// FeishuAppManifest is the single source of truth for the Open Platform
// capabilities provisioned for an amux bridge app.
type FeishuAppManifest struct {
	Preset       bool
	TenantScopes []string
	UserScopes   []string
	TenantEvents []string
	UserEvents   []string
	Callbacks    []string
}

// RequiredFeishuAppManifest returns only capabilities backed by current bridge
// behavior. In particular it excludes message-read/reaction product events,
// reaction read and document-body write access. Message read access is required
// to download image resources attached to accepted user messages.
// The dispatcher separately acknowledges unsolicited echoes of reactions
// written by the app; that transport sink is not a subscribed product
// capability.
func RequiredFeishuAppManifest() FeishuAppManifest {
	return allFeishuCapabilityManifest()
}

// RequiredScopeManifest returns the scope-only projection consumed by the
// explicit legacy console fallback.
func RequiredScopeManifest() ScopeManifest {
	manifest := RequiredFeishuAppManifest()
	return ScopeManifest{
		Tenant: append([]string(nil), manifest.TenantScopes...),
		User:   append([]string(nil), manifest.UserScopes...),
	}
}

func registrationAddonsForProvisioning(skipAddons bool) *registration.AppAddons {
	manifest := RequiredFeishuAppManifest()
	preset := manifest.Preset
	addons := &registration.AppAddons{Preset: &preset}
	if skipAddons {
		return addons
	}
	addons.Scopes = registration.AppAddonsScopes{
		Tenant: append([]string(nil), manifest.TenantScopes...),
		User:   append([]string(nil), manifest.UserScopes...),
	}
	addons.Events = registration.AppAddonsEvents{
		Items: registration.AppAddonsEventItems{
			Tenant: append([]string(nil), manifest.TenantEvents...),
			User:   append([]string(nil), manifest.UserEvents...),
		},
	}
	addons.Callbacks = registration.AppAddonsCallbacks{
		Items: append([]string(nil), manifest.Callbacks...),
	}
	return addons
}

// ProvisionResult is the outcome of official registration.
type ProvisionResult struct {
	AppID       string
	AppSecret   string
	OwnerOpenID string // open_id of the user who scanned to create the app
	TenantBrand string // "feishu" or "lark"
}

// ProvisionOptions configures official registration.
type ProvisionOptions struct {
	// QRWriter receives the rendered terminal QR code and surrounding prompts.
	// Defaults to os.Stderr when nil (set by the caller).
	QRWriter io.Writer
	// OnStatus, when set, receives human-readable polling status updates
	// ("等待扫码" / "已扫码，等待确认" / domain switch).
	OnStatus func(msg string)
	// Domain / LarkDomain override the accounts endpoints (tests only).
	Domain     string
	LarkDomain string
	// SkipAddons creates a minimal-base app without the bridge manifest. The
	// caller must print the manual configuration checklist.
	SkipAddons bool
	// AppID, when set, asks the registration confirmation page to start from
	// the app already bound to the current profile. The operator may still
	// choose another existing app or create a new one on that page.
	AppID string
}

// ProvisionFeishuApp runs the Feishu device-code flow to select an existing or
// create a new PersonalAgent app. It renders a QR code the operator scans in
// the Feishu app, then returns the selected app's credentials and the scanning
// user's open_id.
//
// The scanning user is, by construction, the operator running setup, so callers
// may use OwnerOpenID to seed the bridge owner.
func ProvisionFeishuApp(ctx context.Context, opts ProvisionOptions) (*ProvisionResult, error) {
	qrOut := opts.QRWriter
	if qrOut == nil {
		return nil, fmt.Errorf("ProvisionFeishuApp: QRWriter is required")
	}

	regOpts := &registration.Options{
		Source:     "amux",
		Domain:     opts.Domain,
		LarkDomain: opts.LarkDomain,
		Addons:     registrationAddonsForProvisioning(opts.SkipAddons),
		AppID:      strings.TrimSpace(opts.AppID),
		OnQRCode: func(info *registration.QRCodeInfo) {
			fmt.Fprintln(qrOut)
			fmt.Fprintln(qrOut, "请用飞书 App 扫码选择已有应用或创建智能体应用：")
			fmt.Fprintln(qrOut)
			renderQRCode(qrOut, info.URL)
			fmt.Fprintln(qrOut)
			if info.ExpireIn > 0 {
				fmt.Fprintf(qrOut, "二维码约 %d 秒内有效。也可在浏览器打开：\n  %s\n\n", info.ExpireIn, info.URL)
			} else {
				fmt.Fprintf(qrOut, "也可在浏览器打开：\n  %s\n\n", info.URL)
			}
		},
		OnStatusChange: func(info *registration.StatusChangeInfo) {
			if opts.OnStatus == nil || info == nil {
				return
			}
			switch info.Status {
			case registration.StatusPolling:
				opts.OnStatus("等待飞书扫码确认…")
			case registration.StatusSlowDown:
				opts.OnStatus("轮询放缓，请稍候…")
			case registration.StatusDomainSwitched:
				opts.OnStatus("检测到 Lark 租户，切换认证域…")
			}
		},
	}

	result, err := registerAppFn(ctx, regOpts)
	if err != nil {
		return nil, classifyRegisterAppError(err)
	}
	if result == nil || strings.TrimSpace(result.ClientID) == "" || strings.TrimSpace(result.ClientSecret) == "" {
		return nil, fmt.Errorf("设备码流程未返回应用凭证")
	}

	out := &ProvisionResult{
		AppID:     strings.TrimSpace(result.ClientID),
		AppSecret: strings.TrimSpace(result.ClientSecret),
	}
	if result.UserInfo != nil {
		out.OwnerOpenID = strings.TrimSpace(result.UserInfo.OpenID)
		out.TenantBrand = strings.TrimSpace(result.UserInfo.TenantBrand)
	}
	return out, nil
}

var registerAppFn = registration.RegisterApp

// renderQRCode writes a scannable terminal QR for the given URL.
func renderQRCode(w io.Writer, url string) {
	qrterminal.GenerateWithConfig(url, qrterminal.Config{
		Level:          qrterminal.L,
		Writer:         w,
		HalfBlocks:     true,
		QuietZone:      1,
		BlackChar:      qrterminal.BLACK_BLACK,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		WhiteChar:      qrterminal.WHITE_WHITE,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
	})
}

// classifyRegisterAppError normalizes device-flow errors into user-facing
// Chinese messages while preserving the underlying error for logs.
func classifyRegisterAppError(err error) error {
	switch e := err.(type) {
	case *registration.AccessDeniedError:
		return fmt.Errorf("扫码授权被拒绝: %w", err)
	case *registration.ExpiredError:
		return fmt.Errorf("二维码已过期，请重试: %w", err)
	case *registration.RegisterAppError:
		return fmt.Errorf("配置应用失败(%s): %s", e.Code, e.Description)
	default:
		return fmt.Errorf("配置应用失败: %w", err)
	}
}
