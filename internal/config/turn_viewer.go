package config

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

const (
	FeishuTurnProcessModeKey = "bridge.feishu.experimental.turn_process_mode"
	FeishuTurnProcessInline  = "inline"
	FeishuTurnProcessBubble  = "bubble"
	FeishuTurnProcessSidebar = "sidebar"
	TurnViewerAddrKey        = "hub.turn_viewer.addr"
	TurnViewerBaseURLKey     = "hub.turn_viewer.base_url"
	DefaultTurnViewerPort    = "7893"
)

type TurnViewerConfig struct {
	Addr    string
	BaseURL string
}

func GetTurnViewer() TurnViewerConfig {
	return TurnViewerConfig{
		Addr:    strings.TrimSpace(viper.GetString(TurnViewerAddrKey)),
		BaseURL: strings.TrimSpace(viper.GetString(TurnViewerBaseURLKey)),
	}
}

func GetFeishuTurnProcessMode() string {
	return NormalizeFeishuTurnProcessMode(viper.GetString(FeishuTurnProcessModeKey))
}

func NormalizeFeishuTurnProcessMode(value string) string {
	if ValidFeishuTurnProcessMode(value) {
		return value
	}
	return FeishuTurnProcessInline
}

func ValidFeishuTurnProcessMode(value string) bool {
	switch value {
	case FeishuTurnProcessInline, FeishuTurnProcessBubble, FeishuTurnProcessSidebar:
		return true
	default:
		return false
	}
}

func ValidTurnViewerAddr(addr string) bool {
	if addr == "" {
		return true
	}
	_, port, err := net.SplitHostPort(addr)
	n, portErr := strconv.Atoi(port)
	return err == nil && portErr == nil && n > 0 && n <= 65535
}

func ValidTurnViewerBaseURL(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
		u.Hostname() != "" && u.User == nil && !u.ForceQuery && u.RawQuery == "" && u.Fragment == ""
}
