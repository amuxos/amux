package task

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/i18n"
)

func TestRunnerPermissionResponseFailureDoesNotWaitForTaskDeadline(t *testing.T) {
	for _, policy := range []PermissionPolicy{PolicyAllow, PolicyDenyAll, PolicyEscalateToMain} {
		t.Run(string(policy), func(t *testing.T) {
			ctrl := &fakeController{onCreate: func(c *fakeChild) {
				c.respondErr = errors.New("provider disconnected")
				c.onSubmit = func(c *fakeChild, _ string) { c.emit(permissionEvent("approval-42", "Bash", "pwd")) }
			}}
			r := newTestRunner(t, ctrl, time.Minute)
			run, err := r.Submit(Spec{Agent: "claude", Task: "review", PermissionPolicy: policy}, "", "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if policy == PolicyEscalateToMain {
				deadline := time.Now().Add(2 * time.Second)
				for {
					current, _ := r.Get(run.ID)
					if current.Status == StatusWaitingPermission {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("no permission escalation")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if err := r.RespondPermission(run.ID, PermissionResponse{PermissionId: "approval-42", Allow: true}); err != nil {
					t.Fatal(err)
				}
			}
			final := waitTerminal(t, r, run.ID)
			if final.Status != StatusFailed || !strings.Contains(final.Error, "approval-42") || !strings.Contains(final.Error, "provider disconnected") || !strings.Contains(final.Error, "回复失败") {
				t.Fatalf("expected immediate approval failure, got %+v", final)
			}
			if ctrl.last().permissionPolicy != policy {
				t.Fatal("child did not receive Task permission policy")
			}
		})
	}
}

func TestRunnerPolicyAllowHandlesThreeOutstandingApprovals(t *testing.T) {
	var done sync.Once
	ctrl := &fakeController{onCreate: func(c *fakeChild) {
		c.onSubmit = func(c *fakeChild, _ string) {
			for _, id := range []string{"skill", "agents", "cli"} {
				c.emit(permissionEvent(id, "Bash", "pwd"))
			}
		}
		c.onRespond = func(c *fakeChild, _, _ string) {
			if len(c.responses()) == 3 {
				done.Do(func() {
					c.emit(agentText(`{"amux_task_result":{"status":"ok","summary":"all approved"}}`))
					c.emit(turnDone("end_turn"))
				})
			}
		}
	}}
	r := newTestRunner(t, ctrl, time.Minute)
	run, err := r.Submit(Spec{Agent: "claude", Task: "probe", PermissionPolicy: PolicyAllow}, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if final := waitTerminal(t, r, run.ID); final.Status != StatusSucceeded {
		t.Fatalf("final Task = %+v", final)
	}
	responses := ctrl.last().responses()
	if len(responses) != 3 {
		t.Fatalf("responses = %v", responses)
	}
	for _, response := range responses {
		if response.optID != "allow" {
			t.Fatalf("unexpected response: %+v", response)
		}
	}
}

func TestPermissionResponseErrorLanguagesAndFallback(t *testing.T) {
	key := "permission_response_failed"
	en := taskCatalog.Format(i18n.English, key, "p1", "disconnected")
	zh := taskCatalog.Format(i18n.Chinese, key, "p1", "disconnected")
	if en == key || zh == key || en == zh || taskCatalog.Format(i18n.Locale("unknown"), key, "p1", "disconnected") != en || taskPermissionResponseError("p1", errors.New("disconnected")).Error() != en+"\n"+zh {
		t.Fatalf("invalid translated error: %q / %q", en, zh)
	}
}
