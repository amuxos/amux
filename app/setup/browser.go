package setup

import (
	"fmt"
	"os/exec"
	"runtime"
)

// OpenURL opens url in the default browser. Best-effort; errors are returned
// but callers may continue showing a terminal QR.
func OpenURL(url string) error {
	url = trimURL(url)
	if url == "" {
		return fmt.Errorf("empty authorization URL")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func trimURL(url string) string {
	for len(url) > 0 && (url[0] == ' ' || url[0] == '\t' || url[0] == '\n') {
		url = url[1:]
	}
	for len(url) > 0 && (url[len(url)-1] == ' ' || url[len(url)-1] == '\t' || url[len(url)-1] == '\n') {
		url = url[:len(url)-1]
	}
	return url
}
