package bridge

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/hubweb"
)

func TestSessionViewerURL(t *testing.T) {
	t.Parallel()
	expiresAt := time.Unix(2000, 0)
	got := sessionViewerURLWithExpiry("https://amux.example.com/base/", "viewer secret", "cloud/1", "feishu a+b", expiresAt)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if gotHost := u.Scheme + "://" + u.Host; gotHost != "https://amux.example.com" {
		t.Fatalf("url host = %q", gotHost)
	}
	if u.RawQuery != "" {
		t.Fatalf("url query = %q, want empty", u.RawQuery)
	}
	if !strings.HasPrefix(u.Path, "/base/s/") {
		t.Fatalf("path = %q, want /base/s/<token>", u.Path)
	}
	token := strings.TrimPrefix(u.Path, "/base/s/")
	if token == "" || token == "viewer secret" {
		t.Fatalf("token = %q, want signed scoped token", token)
	}
	if err := hubweb.VerifySessionToken("viewer secret", token, "cloud/1", "feishu a+b", time.Unix(1000, 0)); err != nil {
		t.Fatalf("signed token did not verify: %v", err)
	}
}

func TestSessionViewerURLEmptyWithoutBaseURL(t *testing.T) {
	t.Parallel()
	if got := sessionViewerURL("", "viewer-secret", "target-1", "feishu-a1"); got != "" {
		t.Fatalf("sessionViewerURL without base = %q, want empty", got)
	}
}
