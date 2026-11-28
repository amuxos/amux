package bridge

import (
	"net/url"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/hubweb"
)

const sessionViewerTokenTTL = 7 * 24 * time.Hour

func sessionViewerURL(baseURL, token, targetID, sessionName string) string {
	return sessionViewerURLWithExpiry(baseURL, token, targetID, sessionName, time.Now().Add(sessionViewerTokenTTL))
}

func sessionViewerURLWithExpiry(baseURL, token, targetID, sessionName string, expiresAt time.Time) string {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" || strings.TrimSpace(sessionName) == "" {
		return ""
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	signedToken := hubweb.SignSessionToken(token, targetID, sessionName, expiresAt)
	if signedToken == "" {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/s/" + url.PathEscape(signedToken)
	u.RawQuery = ""
	return u.String()
}

// SetSessionViewer configures the optional read-only session output links used
// in Feishu cards. Leave baseURL empty to omit the link.
func (b *Bridge) SetSessionViewer(baseURL, token string) {
	if b == nil {
		return
	}
	b.sessionViewerBaseURL = strings.TrimSpace(baseURL)
	b.sessionViewerToken = strings.TrimSpace(token)
}

func (b *Bridge) sessionViewerURL(targetID, sessionName string) string {
	if b == nil {
		return ""
	}
	return sessionViewerURL(b.sessionViewerBaseURL, b.sessionViewerToken, targetID, sessionName)
}

// SetTurnProcessViewer sets the current connection information for the optional
// Turn-only viewer. It is wired before the Bridge starts.
func (b *Bridge) SetTurnProcessViewer(settings func() (baseURL, token string)) {
	b.turnProcessViewer = settings
}

func (b *Bridge) currentTurnProcessMode() string {
	mode, _ := b.turnProcessMode.Load().(string)
	return config.NormalizeFeishuTurnProcessMode(mode)
}

func (b *Bridge) turnProcessViewerSettings() (baseURL, token string) {
	if b.currentTurnProcessMode() != config.FeishuTurnProcessSidebar || b.turnProcessViewer == nil {
		return "", ""
	}
	return b.turnProcessViewer()
}

func (b *Bridge) turnProcessURL(targetID, sessionName, turnID, thinkingProcess string, now time.Time) string {
	baseURL, signingKey := b.turnProcessViewerSettings()
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	// Keep the card payload stable across streaming updates within the hour.
	expires := now.Truncate(time.Hour).Add(sessionViewerTokenTTL)
	token := hubweb.SignTurnProcessToken(
		signingKey, targetID, sessionName, turnID,
		thinkingProcess == config.FeishuThinkingProcessHidden,
		thinkingProcess == config.FeishuThinkingProcessExpanded,
		expires,
	)
	if token == "" {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/t/" + token
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	u.ForceQuery = false
	return u.String()
}
