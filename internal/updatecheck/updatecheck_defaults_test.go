package updatecheck

import (
	"context"
	"net/http"
	"testing"
)

type forbiddenTransport struct{ t *testing.T }

func (r forbiddenTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Fatal("attempted a request without an explicit release source")
	return nil, nil
}

func TestPublicUpdateRequiresExplicitSource(t *testing.T) {
	t.Setenv("AMUX_SCM_LATEST_URL", "")
	if got := LatestURL(); got != "" {
		t.Fatalf("default source = %q, want no source", got)
	}
	client := &http.Client{Transport: forbiddenTransport{t}}
	if _, err := LatestVersion(context.Background(), client, LatestURL(), "linux", "amd64"); err == nil {
		t.Fatal("LatestVersion accepted missing source")
	}
	t.Setenv("AMUX_SCM_LATEST_URL", "https://releases.example/amux.tar.gz")
	if got := LatestURL(); got != "https://releases.example/amux.tar.gz" {
		t.Fatalf("explicit source = %q", got)
	}
}
