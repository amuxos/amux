package acp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestInterruptCancelsPendingAndLatePermissions(t *testing.T) {
	stdin := &writeCloserBuffer{}
	p := &Proc{
		stdin:       stdin,
		sessionID:   "permission-session",
		permissions: map[string]int64{"permission-session:101": 101, "permission-session:102": 102},
	}
	var permissionEvents int
	p.SetEventSink(func(emission testProducerEmission) bool {
		if _, ok := emission.Turn.(agentstream.PermissionRequested); ok {
			permissionEvents++
		}
		return false
	})
	if err := p.Interrupt(); err != nil {
		t.Fatal(err)
	}
	if err := p.RespondPermission("permission-session:101", "allow"); err == nil {
		t.Fatal("cancelled permission accepted a late selection")
	}
	if !p.handleRequest(rpcMessage{ID: 103, Method: "session/request_permission", Params: json.RawMessage(`{}`)}) {
		t.Fatal("late permission request was not handled")
	}
	if permissionEvents != 0 || len(p.permissions) != 0 {
		t.Fatalf("cancelled turn retained permissions: events=%d pending=%v", permissionEvents, p.permissions)
	}
	scanner := bufio.NewScanner(bytes.NewReader(stdin.Bytes()))
	responses := make(map[int64]string)
	cancelNotifications := 0
	for scanner.Scan() {
		var msg rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Method == "session/cancel" {
			cancelNotifications++
			if len(responses) != 2 {
				t.Fatalf("session/cancel preceded pending permission responses: %v", responses)
			}
			continue
		}
		var result struct {
			Outcome struct {
				Outcome string `json:"outcome"`
			} `json:"outcome"`
		}
		if err := json.Unmarshal(msg.Result, &result); err != nil {
			t.Fatal(err)
		}
		if _, duplicate := responses[msg.ID]; duplicate {
			t.Fatalf("duplicate permission response for %d", msg.ID)
		}
		responses[msg.ID] = result.Outcome.Outcome
	}
	if cancelNotifications != 1 || len(responses) != 3 {
		t.Fatalf("notifications=%d permission responses=%v", cancelNotifications, responses)
	}
	for _, id := range []int64{101, 102, 103} {
		if responses[id] != "cancelled" {
			t.Fatalf("permission %d response = %q, want cancelled", id, responses[id])
		}
	}
}

func TestNewPromptResetsPermissionCancellation(t *testing.T) {
	proc := startUsageServer(t, []usageServerTurn{{Result: json.RawMessage(`{"stopReason":"end_turn"}`)}})
	if err := proc.Interrupt(); err != nil {
		t.Fatal(err)
	}
	dispatchUsageTurn(t, proc)
	var permissionEvents int
	proc.SetEventSink(func(emission testProducerEmission) bool {
		if _, ok := emission.Turn.(agentstream.PermissionRequested); ok {
			permissionEvents++
		}
		return false
	})
	if !proc.handleRequest(rpcMessage{ID: 105, Method: "session/request_permission", Params: json.RawMessage(`{}`)}) {
		t.Fatal("permission request was not handled")
	}
	if permissionEvents != 1 {
		t.Fatalf("permission events = %d, want one for the new turn", permissionEvents)
	}
	if err := proc.RespondPermission("usage-session:105", "allow"); err != nil {
		t.Fatalf("new turn permission response: %v", err)
	}
}
