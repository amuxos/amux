// Package consoleentry validates credential-free browser-facing console origins.
package consoleentry

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

var ErrInvalidURL = errors.New("invalid console public URL")

// ValidateURL permits bare HTTP(S) origins, never credentials or URL secrets.
// HTTP is supported for temporary trusted-network access, not made confidential
// by its authentication. Public deployments should terminate HTTPS.
func ValidateURL(raw string) (string, error) {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\t\\") {
		return "", ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return "", ErrInvalidURL
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", ErrInvalidURL
	}
	if strings.TrimSpace(u.Hostname()) == "" || u.Hostname() == "0.0.0.0" || u.Hostname() == "::" || strings.ContainsAny(u.Host, " \x00") {
		return "", ErrInvalidURL
	}
	if u.Port() != "" {
		n, err := strconv.Atoi(u.Port())
		if err != nil || n < 1 || n > 65535 {
			return "", ErrInvalidURL
		}
	}
	u.Path = ""
	return u.String(), nil
}
func LocalURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}
