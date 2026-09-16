package bridge

import (
	"net/url"
	"strings"
	"time"

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
