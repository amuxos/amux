package agentprofile

import (
	"strings"
	"testing"
	"time"
)

func TestManagedAuthoringReceiptBindsPreviewAndRejectsTampering(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	signer, err := NewManagedAuthoringReceiptSigner([]byte("01234567890123456789012345678901"), time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	want := ManagedAuthoringReceiptClaims{
		SessionID: "session-1", PreviewTurnID: "turn-1", Operation: ManagedAuthoringUpdate,
		TargetRef: "local/reviewer", BaseRevision: strings.Repeat("1", 40),
		PatchDigest: strings.Repeat("2", 64), Tree: strings.Repeat("3", 40),
		EvaluationProfileRef: "local-eval-0123456789abcdef-dev/reviewer",
	}
	receipt, err := signer.Issue(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := signer.Verify(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != want.SessionID || got.PreviewTurnID != want.PreviewTurnID || got.Tree != want.Tree || got.EvaluationProfileRef != want.EvaluationProfileRef || got.ExpiresAt != now.Add(time.Hour).Unix() {
		t.Fatalf("claims = %+v", got)
	}
	tampered := receipt[:len(receipt)-1] + "A"
	if _, err := signer.Verify(tampered); err == nil {
		t.Fatal("tampered receipt verified")
	}
	now = now.Add(2 * time.Hour)
	if _, err := signer.Verify(receipt); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired receipt error = %v", err)
	}
}
