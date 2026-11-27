package bridge

import (
	"context"
	"net/http"
	"testing"
)

type forbiddenConsoleClient struct{ t *testing.T }

func (c forbiddenConsoleClient) Do(*http.Request) (*http.Response, error) {
	c.t.Fatal("unavailable console automation attempted a network request")
	return nil, nil
}

func TestPublicConsoleUnavailable(t *testing.T) {
	t.Parallel()
	result := AutomateOpenPlatformSetup(context.Background(), "test-app", ConsoleOptions{
		Client: forbiddenConsoleClient{t}, SessionFilePath: t.TempDir(), SessionFallbackPath: t.TempDir(),
		OnQRCode: func(string, string) { t.Fatal("unavailable console requested authentication") },
	})
	if result.OK || result.Reason != "unavailable" || result.SessionFile != "" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
