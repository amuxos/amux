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

func TestPublicUpdateDefaultsToPublicManifest(t *testing.T) {
	t.Setenv("AMUX_RELEASE_URL", "")
	t.Setenv("AMUX_SCM_LATEST_URL", "")
	if got := LatestURL(); got != "https://github.com/amuxos/amux-releases/releases/latest/download/latest.json" {
		t.Fatalf("unexpected public source = %q", got)
	}
	client := &http.Client{Transport: forbiddenTransport{t}}
	if _, err := LatestVersion(context.Background(), client, "", "linux", "amd64"); err == nil {
		t.Fatal("LatestVersion accepted missing source")
	}
	t.Setenv("AMUX_SCM_LATEST_URL", "https://releases.example/amux.tar.gz")
	if got := LatestURL(); got != "https://releases.example/amux.tar.gz" {
		t.Fatalf("explicit source = %q", got)
	}
}

func TestPublicUpdateGenericOverrideTakesPrecedence(t *testing.T) {
	t.Setenv("AMUX_SCM_LATEST_URL", "https://archive.example/latest.tar.gz")
	t.Setenv("AMUX_RELEASE_URL", "https://release.example/latest.json")
	if got := LatestURL(); got != "https://release.example/latest.json" {
		t.Fatalf("source = %q", got)
	}
}
