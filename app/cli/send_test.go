package cli

import "testing"

func TestSendCommandRequiresSessionAndText(t *testing.T) {
	cmd := newSendCmd()
	cmd.SetArgs([]string{"work"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute() error = nil, want usage error")
	}
}
