package bridge

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/rs/zerolog/log"
)

// capabilityPermissionAuditor owns observed Feishu capability-permission issues
// and the audit health flag.
type capabilityPermissionAuditor struct {
	capabilityPermissionMu sync.Mutex
	capabilityPermissions  map[string]capabilityPermissionState
	permissionAuditChecked bool
	permissionAuditFailed  bool
	permissionAuditMu      sync.Mutex
	permissionAuditOnce    sync.Once
}

const (
	capabilityGroupWatch        = "group_watch"
	capabilityDocumentComments  = "document_comments"
	capabilityFeishuIntegration = "feishu_integration"

	feishuRegistrationEndpoint = "/oauth/v1/app/registration"
)

var (
	feishuAppIDPattern = regexp.MustCompile(`^cli_[A-Za-z0-9]+$`)
	feishuScopePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
)

// feishuRegistrationBeginFn is overridable in tests to avoid the network.
var feishuRegistrationBeginFn = func(ctx context.Context, domain string) (*feishuRegistrationBeginResponse, error) {
	form := url.Values{
		"action":            []string{"begin"},
		"archetype":         []string{"PersonalAgent"},
		"auth_method":       []string{"client_secret"},
		"request_user_info": []string{"open_id"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(domain, "/")+feishuRegistrationEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	out := &feishuRegistrationBeginResponse{}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, fmt.Errorf("decode registration begin response: %w", err)
	}
	if strings.TrimSpace(out.VerificationURIComplete) == "" || strings.TrimSpace(out.DeviceCode) == "" {
		return nil, fmt.Errorf("registration begin returned empty verification uri or device code")
	}
	return out, nil
}

type feishuRegistrationBeginResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// feishuRegistrationAddons mirrors the official app-registration Addons
// payload: scopes, events and callbacks pre-filled into the confirmation
// page.
type feishuRegistrationAddons struct {
	Preset    *bool                       `json:"preset,omitempty"`
	Scopes    feishuRegistrationScopes    `json:"scopes,omitempty"`
	Events    feishuRegistrationEvents    `json:"events,omitempty"`
	Callbacks feishuRegistrationCallbacks `json:"callbacks,omitempty"`
}

type feishuRegistrationScopes struct {
	Tenant []string `json:"tenant,omitempty"`
	User   []string `json:"user,omitempty"`
}

type feishuRegistrationEvents struct {
	Items feishuRegistrationEventItems `json:"items,omitempty"`
}

type feishuRegistrationEventItems struct {
	Tenant []string `json:"tenant,omitempty"`
	User   []string `json:"user,omitempty"`
}

type feishuRegistrationCallbacks struct {
	Items []string `json:"items,omitempty"`
}

func grantAddonsForScopes(scopes []string) feishuRegistrationAddons {
	missing := normalizeFeishuScopes(scopes)
	preset := false
	addons := feishuRegistrationAddons{Preset: &preset}
	var tenantScopes, tenantEvents, callbacks []string
	for _, spec := range feishuCapabilitySpecs {
		matched := false
		for _, scope := range spec.TenantScopes {
			if slices.Contains(missing, scope) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		tenantScopes = append(tenantScopes, spec.TenantScopes...)
		tenantEvents = append(tenantEvents, spec.TenantEvents...)
		callbacks = append(callbacks, spec.Callbacks...)
	}
	addons.Scopes.Tenant = normalizeFeishuScopes(tenantScopes)
	addons.Events.Items.Tenant = normalizeFeishuCapabilityStrings(tenantEvents)
	addons.Callbacks.Items = normalizeFeishuCapabilityStrings(callbacks)
	return addons
}

func encodeFeishuRegistrationAddons(addons feishuRegistrationAddons) (string, error) {
	payload := make(map[string]interface{})
	if addons.Preset != nil {
		payload["preset"] = *addons.Preset
	}
	if len(addons.Scopes.Tenant) > 0 || len(addons.Scopes.User) > 0 {
		scopes := make(map[string]interface{})
		if len(addons.Scopes.Tenant) > 0 {
			scopes["tenant"] = addons.Scopes.Tenant
		}
		if len(addons.Scopes.User) > 0 {
			scopes["user"] = addons.Scopes.User
		}
		payload["scopes"] = scopes
	}
	if len(addons.Events.Items.Tenant) > 0 || len(addons.Events.Items.User) > 0 {
		events := make(map[string]interface{})
		items := make(map[string]interface{})
		if len(addons.Events.Items.Tenant) > 0 {
			items["tenant"] = addons.Events.Items.Tenant
		}
		if len(addons.Events.Items.User) > 0 {
			items["user"] = addons.Events.Items.User
		}
		events["items"] = items
		payload["events"] = events
	}
	if len(addons.Callbacks.Items) > 0 {
		payload["callbacks"] = map[string]interface{}{"items": addons.Callbacks.Items}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal registration addons: %w", err)
	}
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(body); err != nil {
		_ = writer.Close()
		return "", fmt.Errorf("gzip registration addons: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("gzip registration addons close: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// feishuRegistrationGrantURL builds the official app-registration
// confirmation page URL that pre-fills the missing tenant scopes (plus the
// events/callbacks of the capabilities they belong to) for the current app.
// Opening the URL and confirming moves those scopes into the pending-publish
// state, which scopes/apply cannot do for scopes that were never requested in
// the developer console.
func (b *Bridge) feishuRegistrationGrantURL(ctx context.Context, scopes []string) (string, error) {
	if b == nil || b.secrets == nil {
		return "", fmt.Errorf("missing bridge secrets")
	}
	appID := strings.TrimSpace(b.secrets.AppId)
	if !feishuAppIDPattern.MatchString(appID) {
		return "", fmt.Errorf("invalid app id %q", appID)
	}
	scopes = normalizeFeishuScopes(scopes)
	if len(scopes) == 0 {
		return "", fmt.Errorf("no scopes to grant")
	}
	beginResp, err := feishuRegistrationBeginFn(ctx, "https://accounts.feishu.cn")
	if err != nil {
		return "", fmt.Errorf("begin feishu registration: %w", err)
	}
	encoded, err := encodeFeishuRegistrationAddons(grantAddonsForScopes(scopes))
	if err != nil {
		return "", fmt.Errorf("encode feishu registration addons: %w", err)
	}
	parsed, err := url.Parse(strings.TrimSpace(beginResp.VerificationURIComplete))
	if err != nil {
		return "", fmt.Errorf("parse registration verification uri: %w", err)
	}
	query := parsed.Query()
	query.Set("from", "sdk")
	query.Set("tp", "sdk")
	query.Set("source", "amux")
	query.Set("addons", encoded)
	query.Set("clientID", appID)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

type capabilityPermissionIssue struct {
	Capability string
	Name       string
	Scopes     []string
	Affected   []string
	GrantURL   string
}

type capabilityPermissionState struct {
	Issue    capabilityPermissionIssue
	Notified bool
}

type feishuPermissionError struct {
	operation string
	code      int
	message   string
	scopes    []string
	grantURL  string
}

func (e *feishuPermissionError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: code=%d msg=%s", e.operation, e.code, e.message)
}

func feishuAPIResponseError(
	operation, appID string,
	codeError larkcore.CodeError,
	fallbackScopes ...string,
) error {
	scopes := make([]string, 0)
	if codeError.Err != nil {
		for _, violation := range codeError.Err.PermissionViolations {
			if violation != nil {
				scopes = append(scopes, violation.Subject)
			}
		}
	}
	scopes = normalizeFeishuScopes(scopes)
	if len(scopes) == 0 && isFeishuPermissionCode(codeError.Code) {
		scopes = normalizeFeishuScopes(fallbackScopes)
	}
	if len(scopes) == 0 {
		return fmt.Errorf("%s: code=%d msg=%s", operation, codeError.Code, codeError.Msg)
	}
	return &feishuPermissionError{
		operation: strings.TrimSpace(operation),
		code:      codeError.Code,
		message:   strings.TrimSpace(codeError.Msg),
		scopes:    scopes,
		grantURL:  feishuPermissionGrantURL(appID, scopes),
	}
}

// feishuAPIResponseErrorForRequiredScopes records the scopes selected by amux
// for an operation. Feishu may report alternative scopes where any one is
// sufficient; persisting all alternatives would incorrectly require all of
// them after the selected least-privilege scope is granted.
func feishuAPIResponseErrorForRequiredScopes(
	operation, appID string,
	codeError larkcore.CodeError,
	requiredScopes ...string,
) error {
	if !isFeishuPermissionCode(codeError.Code) {
		return fmt.Errorf("%s: code=%d msg=%s", operation, codeError.Code, codeError.Msg)
	}
	scopes := normalizeFeishuScopes(requiredScopes)
	if len(scopes) == 0 {
		return feishuAPIResponseError(operation, appID, codeError)
	}
	return &feishuPermissionError{
		operation: strings.TrimSpace(operation),
		code:      codeError.Code,
		message:   strings.TrimSpace(codeError.Msg),
		scopes:    scopes,
		grantURL:  feishuPermissionGrantURL(appID, scopes),
	}
}

func isFeishuPermissionCode(code int) bool {
	switch code {
	case 230027, 99991672:
		return true
	default:
		return false
	}
}

func normalizeFeishuScopes(scopes []string) []string {
	seen := make(map[string]bool, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" || !feishuScopePattern.MatchString(scope) || seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

func feishuPermissionGrantURL(appID string, scopes []string) string {
	appID = strings.TrimSpace(appID)
	scopes = normalizeFeishuScopes(scopes)
	if !feishuAppIDPattern.MatchString(appID) || len(scopes) == 0 {
		return ""
	}
	query := url.Values{}
	query.Set("q", strings.Join(scopes, ","))
	query.Set("op_from", "openapi")
	query.Set("token_type", "tenant")
	return (&url.URL{
		Scheme:   "https",
		Host:     "open.feishu.cn",
		Path:     "/app/" + appID + "/auth",
		RawQuery: query.Encode(),
	}).String()
}

func capabilityPermissionIssueFromError(err error, capability, name string) (capabilityPermissionIssue, bool) {
	var permissionErr *feishuPermissionError
	if !errors.As(err, &permissionErr) || permissionErr == nil || len(permissionErr.scopes) == 0 {
		return capabilityPermissionIssue{}, false
	}
	return capabilityPermissionIssue{
		Capability: strings.TrimSpace(capability),
		Name:       strings.TrimSpace(name),
		Scopes:     append([]string(nil), permissionErr.scopes...),
		GrantURL:   strings.TrimSpace(permissionErr.grantURL),
	}, true
}

func sameCapabilityPermissionIssue(left, right capabilityPermissionIssue) bool {
	return left.Capability == right.Capability &&
		left.Name == right.Name &&
		left.GrantURL == right.GrantURL &&
		strings.Join(left.Scopes, "\x00") == strings.Join(right.Scopes, "\x00") &&
		strings.Join(left.Affected, "\x00") == strings.Join(right.Affected, "\x00")
}

func (c *capabilityPermissionAuditor) recordCapabilityPermissionIssue(issue capabilityPermissionIssue) bool {
	if c == nil || issue.Capability == "" || len(issue.Scopes) == 0 {
		return false
	}
	c.capabilityPermissionMu.Lock()
	defer c.capabilityPermissionMu.Unlock()
	if c.capabilityPermissions == nil {
		c.capabilityPermissions = make(map[string]capabilityPermissionState)
	}
	current, ok := c.capabilityPermissions[issue.Capability]
	if ok && sameCapabilityPermissionIssue(current.Issue, issue) {
		return !current.Notified
	}
	c.capabilityPermissions[issue.Capability] = capabilityPermissionState{Issue: issue}
	return true
}

func (c *capabilityPermissionAuditor) markCapabilityPermissionNotified(issue capabilityPermissionIssue) {
	if c == nil {
		return
	}
	c.capabilityPermissionMu.Lock()
	defer c.capabilityPermissionMu.Unlock()
	current, ok := c.capabilityPermissions[issue.Capability]
	if !ok || !sameCapabilityPermissionIssue(current.Issue, issue) {
		return
	}
	current.Notified = true
	c.capabilityPermissions[issue.Capability] = current
}

// An operational error may name only one scope from an already-notified audit.
// Keep the broader issue and its notification state until a scope is resolved;
// replacing it with each API's subset would repeatedly re-audit and notify.
func (c *capabilityPermissionAuditor) capabilityPermissionIssueAlreadyNotified(issue capabilityPermissionIssue) bool {
	if c == nil {
		return false
	}
	c.capabilityPermissionMu.Lock()
	defer c.capabilityPermissionMu.Unlock()
	current, ok := c.capabilityPermissions[issue.Capability]
	if !ok || !current.Notified {
		return false
	}
	for _, scope := range issue.Scopes {
		if !slices.Contains(current.Issue.Scopes, scope) {
			return false
		}
	}
	return true
}

func (c *capabilityPermissionAuditor) clearCapabilityPermissionIssue(capability string) {
	if c == nil {
		return
	}
	c.capabilityPermissionMu.Lock()
	delete(c.capabilityPermissions, strings.TrimSpace(capability))
	c.capabilityPermissionMu.Unlock()
}

func (b *Bridge) resolveCapabilityPermissionScopes(capability string, resolvedScopes ...string) {
	if b == nil {
		return
	}
	capability = strings.TrimSpace(capability)
	resolvedScopes = normalizeFeishuScopes(resolvedScopes)
	if capability == "" || len(resolvedScopes) == 0 {
		return
	}
	if b.db != nil {
		if err := b.db.resolveOperationalPermissionScopes(b.currentBridgeId(), capability, resolvedScopes); err != nil {
			log.Warn().Err(err).Str("capability", capability).Msg("bridge: resolve operational permission issue")
			return
		}
	}
	resolved := make(map[string]bool, len(resolvedScopes))
	for _, scope := range resolvedScopes {
		resolved[scope] = true
	}
	b.capabilityPermissionMu.Lock()
	if state, ok := b.capabilityPermissions[capability]; ok {
		remaining := make([]string, 0, len(state.Issue.Scopes))
		for _, scope := range state.Issue.Scopes {
			if !resolved[scope] {
				remaining = append(remaining, scope)
			}
		}
		state.Issue.Scopes = normalizeFeishuScopes(remaining)
		state.Issue.GrantURL = feishuPermissionGrantURL(b.secretsAppID(), state.Issue.Scopes)
		if len(state.Issue.Scopes) == 0 {
			delete(b.capabilityPermissions, capability)
		} else {
			b.capabilityPermissions[capability] = state
		}
	}
	b.capabilityPermissionMu.Unlock()
	b.refreshFeishuIntegrationPermissionIssue()
}

func (b *Bridge) refreshFeishuIntegrationPermissionIssue() {
	if b == nil {
		return
	}
	b.capabilityPermissionMu.Lock()
	var scopes []string
	var affected []string
	for capability, state := range b.capabilityPermissions {
		if capability == capabilityFeishuIntegration || len(state.Issue.Scopes) == 0 {
			continue
		}
		scopes = append(scopes, state.Issue.Scopes...)
		name := strings.TrimSpace(state.Issue.Name)
		if name != "" {
			affected = append(affected, name)
		}
	}
	scopes = normalizeFeishuScopes(scopes)
	sort.Strings(affected)
	if len(scopes) == 0 {
		delete(b.capabilityPermissions, capabilityFeishuIntegration)
	} else {
		issue := capabilityPermissionIssue{
			Capability: capabilityFeishuIntegration,
			Name:       "飞书接入",
			Scopes:     scopes,
			Affected:   affected,
			GrantURL:   feishuPermissionGrantURL(b.secretsAppID(), scopes),
		}
		current := b.capabilityPermissions[capabilityFeishuIntegration]
		if !sameCapabilityPermissionIssue(current.Issue, issue) {
			current = capabilityPermissionState{Issue: issue}
		}
		b.capabilityPermissions[capabilityFeishuIntegration] = current
	}
	b.capabilityPermissionMu.Unlock()
	if len(scopes) == 0 && b.db != nil {
		if err := b.db.clearFeishuPermissionAudit(b.currentBridgeId()); err != nil {
			log.Warn().Err(err).Msg("bridge: clear resolved permission notification state")
		}
	}
}

func (c *capabilityPermissionAuditor) capabilityPermissionIssue(capability string) *capabilityPermissionIssue {
	if c == nil {
		return nil
	}
	c.capabilityPermissionMu.Lock()
	defer c.capabilityPermissionMu.Unlock()
	state, ok := c.capabilityPermissions[strings.TrimSpace(capability)]
	if !ok {
		return nil
	}
	issue := state.Issue
	issue.Scopes = append([]string(nil), issue.Scopes...)
	issue.Affected = append([]string(nil), issue.Affected...)
	return &issue
}

func (b *Bridge) observeCapabilityPermissionError(
	ctx context.Context,
	capability, name string,
	err error,
	notifyOwner bool,
) {
	issue, ok := capabilityPermissionIssueFromError(err, capability, name)
	if !ok || b.capabilityPermissionIssueAlreadyNotified(issue) || !b.recordCapabilityPermissionIssue(issue) || !notifyOwner {
		return
	}
	// Prefer the authoritative permission list so one missing capability can be
	// consolidated with every other active integration issue. If the audit API
	// itself is unavailable, retain the existing immediate single-capability
	// fallback rather than swallowing the actionable error.
	if auditErr := b.auditFeishuPermissionsWithObservedIssue(ctx, true, &issue); auditErr == nil {
		return
	}
	fingerprint := strings.Join(normalizeFeishuScopes(issue.Scopes), "\x00")
	if b.db != nil {
		shouldNotify, stateErr := b.db.shouldNotifyFeishuPermissionAudit(b.currentBridgeId(), fingerprint)
		if stateErr != nil {
			log.Warn().Err(stateErr).Str("capability", capability).
				Msg("bridge: prepare fallback permission notification")
		} else if !shouldNotify {
			b.markCapabilityPermissionNotified(issue)
			return
		}
	}
	if notifyErr := b.notifyCapabilityPermissionIssue(ctx, issue); notifyErr != nil {
		if b.db != nil {
			_ = b.db.releaseFeishuPermissionAuditClaim(b.currentBridgeId(), fingerprint)
		}
		log.Warn().Err(notifyErr).Str("capability", capability).
			Msg("bridge: notify capability permission issue")
		return
	}
	b.markCapabilityPermissionNotified(issue)
	if b.db != nil {
		if stateErr := b.db.markFeishuPermissionAuditNotified(b.currentBridgeId(), fingerprint); stateErr != nil {
			log.Warn().Err(stateErr).Str("capability", capability).
				Msg("bridge: persist fallback permission notification")
		}
	}
}

func (b *Bridge) notifyCapabilityPermissionIssue(ctx context.Context, issue capabilityPermissionIssue) error {
	if b == nil || b.secrets == nil {
		return fmt.Errorf("missing bridge secrets")
	}
	ownerOpenID := strings.TrimSpace(b.secrets.OpenId)
	if ownerOpenID == "" {
		return fmt.Errorf("missing bridge owner")
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	card := renderCapabilityPermissionCard(issue)
	err := b.sendManagementCardToDM(notifyCtx, ownerOpenID, card)
	if err == nil {
		return nil
	}
	text := managementCapabilityPermissionNotice(issue)
	if issue.GrantURL != "" {
		text = managementConcat(text, "\n", managementCopy("management.permission.fallback.link", issue.GrantURL))
	}
	fallbackText := b.managementFallbackText(ownerOpenID, text)
	fallbackCtx, fallbackCancel := context.WithTimeout(ctx, 10*time.Second)
	defer fallbackCancel()
	var fallbackErr error
	if b.dmNotice != nil {
		_, _, fallbackErr = b.dmNotice(fallbackCtx, ownerOpenID, fallbackText)
	} else {
		_, _, fallbackErr = b.sendDM(fallbackCtx, ownerOpenID, fallbackText)
	}
	if fallbackErr == nil {
		return nil
	}
	return errors.Join(err, fallbackErr)
}

func capabilityPermissionNotice(issue capabilityPermissionIssue) string {
	if issue.Capability == capabilityFeishuIntegration {
		affected := ""
		if len(issue.Affected) > 0 {
			affected = "受影响能力：" + strings.Join(issue.Affected, "、") + "。"
		}
		return fmt.Sprintf("部分飞书接入能力受限：%s当前智能体缺少飞书应用权限 %s。申请并开通后，请发布应用版本。",
			affected, strings.Join(issue.Scopes, "、"))
	}
	name := strings.TrimSpace(issue.Name)
	if name == "" {
		name = "接入能力"
	}
	affected := ""
	if len(issue.Affected) > 0 {
		affected = "受影响能力：" + strings.Join(issue.Affected, "、") + "。"
	}
	return fmt.Sprintf("%s暂不可用：%s当前智能体缺少飞书应用权限 %s。申请并开通后，请发布应用版本。",
		name, affected, strings.Join(issue.Scopes, "、"))
}

func renderCapabilityPermissionCard(issue capabilityPermissionIssue) string {
	elements := []cardElement{
		managementTextElement(managementCapabilityPermissionNotice(issue), "normal", "orange", managementIconPtr("warning_outlined", "orange")),
		managementHintElement(managementCopy("management.permission.request.hint")),
	}
	var buttons []managementButton
	if len(issue.Scopes) > 0 {
		buttons = append(buttons, managementButton{
			Text:    managementCopy("management.request.permissions"),
			Type:    "primary",
			Action:  managementActionPermissionApply,
			Name:    "amux.capabilities.permissions.apply",
			Confirm: managementConfirm(managementCopy("management.request.these.permissions.from.the.organization.administrator"), managementCopy("management.the.app.will.request.permissions.from.the.tenant.administrator")),
		})
	}
	if issue.GrantURL != "" {
		buttons = append(buttons, managementButton{Text: managementCopy("management.developer.console"), Type: "default", URL: issue.GrantURL})
	}
	if issue.Capability == capabilityFeishuIntegration {
		buttons = append(buttons, managementButton{Text: managementCopy("management.recheck"), Type: "default", Action: managementActionPermissionRecheck})
	}
	return marshalNativeManagementCard(managementCardView{
		Title:    managementCopy("management.permission.title", managementCapabilityName(issue)),
		Summary:  managementCapabilityPermissionNotice(issue),
		Theme:    "orange",
		Icon:     managementIcon("warning_outlined", "orange"),
		Elements: elements,
		Buttons:  buttons,
	})
}

// renderPermissionGrantGuideCard shows the result of a failed one-click
// apply together with the official registration confirmation page URL that
// pre-fills the missing scopes, so the owner can move them into the
// pending-publish state in one click.
func renderPermissionGrantGuideCard(issue capabilityPermissionIssue, notice any, confirmURL string) string {
	elements := []cardElement{
		managementTextElement(notice, "normal", "orange", managementIconPtr("warning_outlined", "orange")),
		managementHintElement(managementCopy("management.permission.guide.hint")),
	}
	var buttons []managementButton
	if strings.TrimSpace(confirmURL) != "" {
		buttons = append(buttons, managementButton{Text: managementCopy("management.permission.confirm.page"), Type: "primary", URL: strings.TrimSpace(confirmURL)})
	}
	if issue.GrantURL != "" {
		buttons = append(buttons, managementButton{Text: managementCopy("management.developer.console"), Type: "default", URL: issue.GrantURL})
	}
	buttons = append(buttons, managementButton{Text: managementCopy("management.recheck"), Type: "default", Action: managementActionPermissionRecheck})
	return marshalNativeManagementCard(managementCardView{
		Title:    managementCopy("management.permission.required"),
		Summary:  managementCapabilityPermissionNotice(issue),
		Theme:    "orange",
		Icon:     managementIcon("warning_outlined", "orange"),
		Elements: elements,
		Buttons:  buttons,
	})
}

func (b *Bridge) startFeishuPermissionAudit(ctx context.Context) {
	if b == nil {
		return
	}
	b.permissionAuditOnce.Do(func() {
		b.triggerFeishuPermissionAudit(ctx)
	})
}

func (b *Bridge) triggerFeishuPermissionAudit(ctx context.Context) {
	if b == nil {
		return
	}
	go func() {
		auditCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := b.auditFeishuPermissions(auditCtx, true)
		cancel()
		if err != nil {
			log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: Feishu permission self-check unavailable")
			return
		}
		if b.capabilityPermissionIssue(capabilityDocumentComments) == nil {
			recoverCtx, recoverCancel := context.WithTimeout(ctx, 10*time.Second)
			defer recoverCancel()
			if err := b.recoverDocumentCommentSource(recoverCtx, true); err != nil {
				log.Warn().Err(err).Str("profile", b.ProfileName()).Msg("bridge: document comment permission recovery unavailable")
			}
		}
	}()
}

func (b *Bridge) applyFeishuCapabilityScopes(ctx context.Context) error {
	if b == nil || b.client == nil {
		return fmt.Errorf("nil lark client")
	}
	resp, err := b.client.Application.Scope.Apply(ctx)
	if err != nil {
		return fmt.Errorf("apply Feishu scopes: %w", err)
	}
	if resp == nil || !resp.Success() {
		if resp == nil {
			return fmt.Errorf("apply Feishu scopes: empty response")
		}
		return fmt.Errorf("apply Feishu scopes: %w", larkcore.CodeError{Code: resp.Code, Msg: resp.Msg})
	}
	return nil
}

func feishuScopeApplyErrorNotice(err error) string {
	var codeErr larkcore.CodeError
	if errors.As(err, &codeErr) && codeErr.Code != 0 {
		switch codeErr.Code {
		case 212001:
			return "剩余缺失权限为高敏权限，无法直接申请；请在开发者后台手动申请。"
		case 212002:
			return "当前应用版本没有可继续申请的权限，正在重新检测实际授权状态。若仍有缺失，请通过「申请确认页」补充并发布应用版本。"
		case 212003:
			return "权限申请次数已达上限，请改用开发者后台申请。"
		case 212004:
			return "这些权限已申请过，等待管理员审核即可。"
		}
	}
	return "权限申请失败，请稍后重试，或通过开发者后台手动申请。"
}

func isFeishuScopeApplyCode(err error, code int) bool {
	var codeErr larkcore.CodeError
	return errors.As(err, &codeErr) && codeErr.Code == code
}

func (b *Bridge) listFeishuPermissionStatuses(ctx context.Context) (map[string]int, error) {
	if b == nil {
		return nil, fmt.Errorf("nil bridge")
	}
	if b.permissionScopeList != nil {
		return b.permissionScopeList(ctx)
	}
	if b.client == nil {
		return nil, fmt.Errorf("nil lark client")
	}
	resp, err := b.client.Application.Scope.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list Feishu application scopes: %w", err)
	}
	if resp == nil || !resp.Success() {
		if resp == nil {
			return nil, fmt.Errorf("list Feishu application scopes: empty response")
		}
		return nil, fmt.Errorf("list Feishu application scopes: code=%d msg=%s", resp.Code, resp.Msg)
	}
	statuses := make(map[string]int)
	if resp.Data == nil {
		return statuses, nil
	}
	for _, scope := range resp.Data.Scopes {
		if scope == nil || scope.ScopeName == nil || scope.GrantStatus == nil {
			continue
		}
		if scope.ScopeType != nil && strings.TrimSpace(*scope.ScopeType) != "" && strings.TrimSpace(*scope.ScopeType) != "tenant" {
			continue
		}
		name := strings.TrimSpace(*scope.ScopeName)
		if name != "" {
			statuses[name] = *scope.GrantStatus
		}
	}
	return statuses, nil
}

func (b *Bridge) auditFeishuPermissions(ctx context.Context, notifyOwner bool) error {
	return b.auditFeishuPermissionsWithObservedIssue(ctx, notifyOwner, nil)
}

func (b *Bridge) auditFeishuPermissionsWithObservedIssue(
	ctx context.Context,
	notifyOwner bool,
	observed *capabilityPermissionIssue,
) error {
	if b == nil {
		return fmt.Errorf("nil bridge")
	}
	b.permissionAuditMu.Lock()
	defer b.permissionAuditMu.Unlock()

	operationalIssues := make(map[string]capabilityPermissionIssue)
	if b.db != nil {
		persisted, loadErr := b.db.operationalPermissionIssues(b.currentBridgeId())
		if loadErr != nil {
			return fmt.Errorf("load operational permission issues: %w", loadErr)
		}
		for _, issue := range persisted {
			operationalIssues[issue.Capability] = issue
		}
	}
	if observed != nil && strings.TrimSpace(observed.Capability) != "" && len(observed.Scopes) > 0 {
		copy := *observed
		copy.Capability = strings.TrimSpace(copy.Capability)
		copy.Name = strings.TrimSpace(copy.Name)
		copy.Scopes = normalizeFeishuScopes(copy.Scopes)
		if current, ok := operationalIssues[copy.Capability]; ok {
			copy.Scopes = normalizeFeishuScopes(append(copy.Scopes, current.Scopes...))
			if copy.Name == "" {
				copy.Name = current.Name
			}
		}
		operationalIssues[copy.Capability] = copy
		if b.db != nil {
			if persistErr := b.db.upsertOperationalPermissionIssue(b.currentBridgeId(), copy); persistErr != nil {
				return fmt.Errorf("persist operational permission issue: %w", persistErr)
			}
		}
	}
	statuses, err := b.listFeishuPermissionStatuses(ctx)
	if err != nil {
		b.setFeishuPermissionAuditHealth(false)
		return err
	}
	b.setFeishuPermissionAuditHealth(true)
	activeCapabilities := b.activeFeishuCapabilitySpecs()
	activeIDs := make(map[string]bool, len(activeCapabilities))
	activeScopes := make(map[string]map[string]bool, len(activeCapabilities))
	for _, capability := range activeCapabilities {
		activeIDs[capability.ID] = true
		allowed := make(map[string]bool, len(capability.TenantScopes))
		for _, scope := range capability.TenantScopes {
			allowed[scope] = true
		}
		activeScopes[capability.ID] = allowed
	}
	if observed != nil && strings.TrimSpace(observed.Capability) != "" && len(observed.Scopes) > 0 {
		observedCapability := strings.TrimSpace(observed.Capability)
		if !activeIDs[observedCapability] {
			activeCapabilities = append(activeCapabilities, feishuCapabilitySpec{
				ID:   observedCapability,
				Name: strings.TrimSpace(observed.Name),
			})
			activeIDs[observedCapability] = true
		}
	}
	for _, capability := range feishuCapabilitySpecs {
		if !activeIDs[capability.ID] {
			b.clearCapabilityPermissionIssue(capability.ID)
		}
	}
	missingByCapability := make(map[string][]string)
	capabilityNames := make(map[string]string)
	for _, capability := range activeCapabilities {
		capabilityNames[capability.ID] = capability.Name
		for _, scope := range capability.TenantScopes {
			if statuses[scope] != 1 {
				missingByCapability[capability.ID] = append(missingByCapability[capability.ID], scope)
			}
		}
	}
	// A concrete OpenAPI permission error is stronger and more current evidence
	// than the application scope catalog, which can lag a version publish. Keep
	// persisted operational failures missing until that capability's real API
	// call succeeds and explicitly resolves the issue.
	for capability, issue := range operationalIssues {
		if !activeIDs[capability] {
			continue
		}
		allowed, declared := activeScopes[capability]
		if !declared {
			missingByCapability[capability] = append(missingByCapability[capability], issue.Scopes...)
			if strings.TrimSpace(issue.Name) != "" {
				capabilityNames[capability] = strings.TrimSpace(issue.Name)
			}
			continue
		}
		kept := make([]string, 0, len(issue.Scopes))
		removed := make([]string, 0, len(issue.Scopes))
		for _, scope := range issue.Scopes {
			if allowed[scope] {
				kept = append(kept, scope)
			} else {
				removed = append(removed, scope)
			}
		}
		if len(removed) > 0 && b.db != nil {
			if resolveErr := b.db.resolveOperationalPermissionScopes(b.currentBridgeId(), capability, removed); resolveErr != nil {
				return fmt.Errorf("discard stale operational permission alternatives: %w", resolveErr)
			}
		}
		missingByCapability[capability] = append(missingByCapability[capability], kept...)
		if strings.TrimSpace(issue.Name) != "" {
			capabilityNames[capability] = strings.TrimSpace(issue.Name)
		}
	}

	missingScopes := make([]string, 0)
	affected := make([]string, 0)
	capabilityIssues := make([]capabilityPermissionIssue, 0)
	for _, capability := range activeCapabilities {
		scopes := normalizeFeishuScopes(missingByCapability[capability.ID])
		if len(scopes) == 0 {
			b.clearCapabilityPermissionIssue(capability.ID)
			continue
		}
		name := capabilityNames[capability.ID]
		affected = append(affected, name)
		missingScopes = append(missingScopes, scopes...)
		issue := capabilityPermissionIssue{
			Capability: capability.ID,
			Name:       name,
			Scopes:     scopes,
			GrantURL:   feishuPermissionGrantURL(b.secretsAppID(), scopes),
		}
		b.recordCapabilityPermissionIssue(issue)
		capabilityIssues = append(capabilityIssues, issue)
	}
	missingScopes = normalizeFeishuScopes(missingScopes)
	sort.Strings(affected)
	if len(missingScopes) == 0 {
		b.clearCapabilityPermissionIssue(capabilityFeishuIntegration)
		if b.db != nil {
			if err := b.db.clearFeishuPermissionAudit(b.currentBridgeId()); err != nil {
				return fmt.Errorf("clear permission audit: %w", err)
			}
		}
		return nil
	}

	issue := capabilityPermissionIssue{
		Capability: capabilityFeishuIntegration,
		Name:       "飞书接入",
		Scopes:     missingScopes,
		Affected:   affected,
		GrantURL:   feishuPermissionGrantURL(b.secretsAppID(), missingScopes),
	}
	b.recordCapabilityPermissionIssue(issue)
	if !notifyOwner {
		return nil
	}
	fingerprint := strings.Join(missingScopes, "\x00")
	shouldNotify := true
	if b.db != nil {
		shouldNotify, err = b.db.shouldNotifyFeishuPermissionAudit(b.currentBridgeId(), fingerprint)
		if err != nil {
			return fmt.Errorf("prepare permission audit notification: %w", err)
		}
	}
	if !shouldNotify {
		b.markCapabilityPermissionNotified(issue)
		for _, capabilityIssue := range capabilityIssues {
			b.markCapabilityPermissionNotified(capabilityIssue)
		}
		return nil
	}
	if err := b.notifyCapabilityPermissionIssue(ctx, issue); err != nil {
		if b.db != nil {
			_ = b.db.releaseFeishuPermissionAuditClaim(b.currentBridgeId(), fingerprint)
		}
		return err
	}
	b.markCapabilityPermissionNotified(issue)
	for _, capabilityIssue := range capabilityIssues {
		b.markCapabilityPermissionNotified(capabilityIssue)
	}
	if b.db != nil {
		if err := b.db.markFeishuPermissionAuditNotified(b.currentBridgeId(), fingerprint); err != nil {
			return fmt.Errorf("mark permission audit notified: %w", err)
		}
	}
	return nil
}

func (c *capabilityPermissionAuditor) setFeishuPermissionAuditHealth(success bool) {
	if c == nil {
		return
	}
	c.capabilityPermissionMu.Lock()
	if success {
		c.permissionAuditChecked = true
		c.permissionAuditFailed = false
	} else {
		c.permissionAuditFailed = true
	}
	c.capabilityPermissionMu.Unlock()
}

func (c *capabilityPermissionAuditor) feishuPermissionAuditHealth() (checked, failed bool) {
	if c == nil {
		return false, false
	}
	c.capabilityPermissionMu.Lock()
	defer c.capabilityPermissionMu.Unlock()
	return c.permissionAuditChecked, c.permissionAuditFailed
}

func (b *Bridge) secretsAppID() string {
	if b == nil || b.secrets == nil {
		return ""
	}
	return b.secrets.AppId
}
