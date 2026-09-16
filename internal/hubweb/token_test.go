package hubweb

import (
	"strings"
	"testing"
	"time"
)

func TestSessionTokenScopesTargetAndSession(t *testing.T) {
	expiresAt := time.Unix(2000, 0)
	token := SignSessionToken("viewer-secret", "target-1", "feishu-a1", expiresAt)
	if token == "" {
		t.Fatal("SignSessionToken returned empty token")
	}
	if strings.Contains(token, "viewer-secret") || strings.Contains(token, "feishu-a1") {
		t.Fatalf("token should not expose secret or session name: %q", token)
	}
	if err := VerifySessionToken("viewer-secret", token, "target-1", "feishu-a1", time.Unix(1000, 0)); err != nil {
		t.Fatalf("VerifySessionToken matching scope = %v", err)
	}
	if err := VerifySessionToken("viewer-secret", token, "target-1", "other", time.Unix(1000, 0)); err == nil {
		t.Fatal("VerifySessionToken with other session = nil, want error")
	}
	if err := VerifySessionToken("viewer-secret", token, "other", "feishu-a1", time.Unix(1000, 0)); err == nil {
		t.Fatal("VerifySessionToken with other target = nil, want error")
	}
	if err := VerifySessionToken("viewer-secret", token, "target-1", "feishu-a1", time.Unix(3000, 0)); err == nil {
		t.Fatal("VerifySessionToken after expiry = nil, want error")
	}
}
