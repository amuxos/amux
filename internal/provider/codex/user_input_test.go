package codex

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
)

func TestUserInputDiscoveryAppliedToStartResumeAndClear(t *testing.T) {
	for _, resume := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{true: "resume", false: "start"}[resume], map[bool]string{true: "explicit", false: "managed"}[explicit]}, "/"), func(t *testing.T) {
				script := `#!/usr/bin/env python3
import json, sys
if sys.argv[-2:] == ["features", "list"]:
    print("default_mode_request_user_input  under development  false")
    sys.exit(0)
for line in sys.stdin:
    msg=json.loads(line)
    method=msg.get("method")
    if method == "initialize":
        result={"userAgent":"host"}
    elif method in ["thread/start", "thread/resume"]:
        cfg=msg["params"].get("config",{})
        expected=not any("default_mode_request_user_input" in arg for arg in sys.argv[1:])
        assert cfg.get("features.default_mode_request_user_input",False)==expected,cfg
        result={"thread":{"id":"t1"}}
    else:
        result={}
    if "id" in msg: print(json.dumps({"id":msg["id"],"result":result}),flush=True)
`
				opts := compatibleStartOptions(t, writeFakeCodex(t, script))
				if resume {
					opts.ResumeSessionID = "existing"
				}
				if explicit {
					opts.ExtraArgs = []string{"-c", "features.default_mode_request_user_input=false"}
				}
				proc, _, err := Start(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				defer proc.Close()
				if proc.defaultModeUserInput == explicit {
					t.Fatalf("managed feature=%v", proc.defaultModeUserInput)
				}
				if err := proc.clearThread(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestDefaultModeUserInputCapabilityParsing(t *testing.T) {
	for _, tc := range []struct {
		output string
		want   bool
	}{
		{"default_mode_request_user_input\tunder development\tfalse", true},
		{"default_mode_request_user_input stable true", true},
		{"default_mode_request_user_input removed false", false},
		{"another_feature stable true", false},
		{"", false},
	} {
		if got := supportsDefaultModeUserInput(tc.output); got != tc.want {
			t.Errorf("%q = %v", tc.output, got)
		}
	}
}

func TestUserInputValidationAndBatchCancellation(t *testing.T) {
	script := strings.ReplaceAll(strings.ReplaceAll(fakeCodexRequestUserInputScript, "9001", "0"), `assert got_answers["q1"]["answers"] == ["A"], got_answers`, `assert got_answers == {}, got_answers`)
	script = strings.ReplaceAll(script, `        assert got_answers["q2"]["answers"] == ["Y"], got_answers`, "")
	proc, _, err := Start(context.Background(), compatibleStartOptions(t, writeFakeCodex(t, script)))
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Close()
	events := make(chan testEmission, 32)
	proc.SetEventSink(captureEventSink(events))
	go func() { _, _ = proc.Write([]byte("run\r")) }()
	if ids := waitForPermissions(t, events, 2, 3*time.Second); len(ids) != 2 {
		t.Fatalf("ids=%v", ids)
	}
	for _, response := range []struct {
		id      string
		answers []string
	}{{"0:q1", []string{"A", "B"}}, {"0:q1", []string{"unknown"}}, {"0:other", []string{"A"}}} {
		if err := proc.RespondPermissionMulti(response.id, response.answers); err == nil {
			t.Fatalf("invalid response accepted: %+v", response)
		}
	}
	if err := proc.RespondPermission("0:q1", "A"); err != nil {
		t.Fatal(err)
	}
	if err := proc.RespondPermission("0:q1", "B"); err == nil {
		t.Fatal("duplicate answer accepted")
	}
	if err := proc.CancelUserInput("0:q1"); err == nil {
		t.Fatal("answered card cancelled the batch")
	}
	if err := proc.CancelUserInput("0:q2"); err != nil {
		t.Fatal(err)
	}
	if !waitForOutcome(t, events, agentstream.OutcomeSuccess, 3*time.Second) {
		t.Fatal("batch cancellation did not complete")
	}
	if err := proc.RespondPermission("0:q2", "Y"); err == nil {
		t.Fatal("cancelled question accepted answer")
	}
}

func TestUserInputMalformedBatchFailsWithoutPendingCards(t *testing.T) {
	for _, tc := range []struct{ name, old, replacement string }{
		{"duplicate questions", `"id":"q2"`, `"id":"q1"`},
		{"empty question", `"question":"选哪个？"`, `"question":""`},
		{"duplicate choices", `"label":"B"`, `"label":"A"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script := strings.Replace(fakeCodexRequestUserInputScript, tc.old, tc.replacement, 1)
			script = strings.Replace(script, `    elif mid == 9001 and "result" in msg:`, `    elif mid == 9001 and "error" in msg:
        assert msg["error"]["code"] == -32602, msg
        print(json.dumps({"method":"turn/completed","params":{"threadId":thread_id,"turn":{"id":turn_id}}}),flush=True)
        print(json.dumps({"id":pending_turn_id,"result":{"turn":{"id":turn_id}}}),flush=True)
    elif mid == 9001 and "result" in msg:`, 1)
			proc, _, err := Start(context.Background(), compatibleStartOptions(t, writeFakeCodex(t, script)))
			if err != nil {
				t.Fatal(err)
			}
			defer proc.Close()
			events := make(chan testEmission, 32)
			proc.SetEventSink(captureEventSink(events))
			go func() { _, _ = proc.Write([]byte("run\r")) }()
			deadline := time.After(3 * time.Second)
			for {
				select {
				case ev := <-events:
					if ev.Kind == agentstream.KindPermissionRequested {
						t.Fatal("malformed request exposed as an approval")
					}
					if ev.Terminal != nil {
						return
					}
				case <-deadline:
					t.Fatal("malformed native tool call remained blocked")
				}
			}
		})
	}
}
