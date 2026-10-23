package mux

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

func TestACPBrokerImageCapabilitiesSurviveReconnect(t *testing.T) {
	// A short socket path is required on macOS; the normal TempDir may exceed
	// its Unix-domain socket limit.
	dir, err := os.MkdirTemp("/tmp", "acp-image-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock, pidFile := filepath.Join(dir, "broker.sock"), filepath.Join(dir, "broker.pid")
	capture := filepath.Join(dir, "requests.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPBrokerServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP_BROKER=1", "AMUX_FAKE_ACP_IMAGES=1", "AMUX_FAKE_ACP_CAPTURE="+capture)
	brokerDone := make(chan error, 1)
	go func() { brokerDone <- RunACPBrokerWithOptions(sock, pidFile, ACPBrokerOptions{Cwd: dir, Cmd: cmd}) }()
	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			if pid > 0 {
				break
			}
		}
		select {
		case err := <-brokerDone:
			t.Fatalf("broker exited during startup: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	if pid == 0 {
		t.Fatal("broker did not publish its PID")
	}
	first, firstDone, err := ConnectACP(sock, pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	assertACPImageCapabilities(t, first)
	// Runtime connection loss must not shut down the broker or erase its
	// initialize negotiation. Reconnect before admitting any prompt.
	_ = first.conn.Close()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first connection did not close")
	}
	second, _, err := ConnectACP(sock, pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	assertACPImageCapabilities(t, second)
	sess := &session.Session{ID: "image-session", TimelineEpoch: "image-epoch"}
	sess.SetProc(second)
	if snap := sess.Snapshot(); !reflect.DeepEqual(snap.InputModalities, []string{"text", "image"}) || snap.InputCapabilitySource != "acp:initialize" {
		t.Fatalf("Session lost negotiated modalities: %v, %q", snap.InputModalities, snap.InputCapabilitySource)
	}

	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	terminals := make(chan agentstream.Terminal, 2)
	if err := second.DispatchTurn(context.Background(), session.TurnDispatchRequest{
		TurnToken: "image-turn",
		Input: session.UserInput{Parts: []session.InputPart{
			{Text: "before"}, {Image: &session.ImageInput{Data: data.Bytes(), MIMEType: "image/png"}}, {Text: "after"},
		}},
	}, func(ev agentstream.ProducerEmission) bool {
		if ev.Terminal != nil {
			terminals <- *ev.Terminal
		}
		return false
	}); err != nil {
		t.Fatalf("image dispatch: %v", err)
	}
	select {
	case terminal := <-terminals:
		if terminal.Outcome != agentstream.OutcomeSuccess {
			t.Fatalf("image terminal = %+v", terminal)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("image turn did not finish")
	}
	select {
	case err := <-brokerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("broker did not finish after native process exit")
	}
	requests, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var prompt []map[string]string
	scanner := bufio.NewScanner(bytes.NewReader(requests))
	for scanner.Scan() {
		var request struct {
			Method string `json:"method"`
			Params struct {
				Prompt []map[string]string `json:"prompt"`
			} `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatal(err)
		}
		if request.Method == "session/prompt" {
			if prompt != nil {
				t.Fatal("reconnect submitted the image more than once")
			}
			prompt = request.Params.Prompt
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	want := []map[string]string{
		{"type": "text", "text": "before"},
		{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(data.Bytes())},
		{"type": "text", "text": "after"},
	}
	if !reflect.DeepEqual(prompt, want) {
		t.Fatalf("native image prompt = %+v, want %+v", prompt, want)
	}
}

func assertACPImageCapabilities(t *testing.T, proc *ACPBrokerProc) {
	t.Helper()
	modalities, source := proc.InputCapabilities()
	if !reflect.DeepEqual(modalities, []string{"text", "image"}) || source != "acp:initialize" {
		t.Fatalf("broker capabilities = %v, %q", modalities, source)
	}
	modalities[1] = "mutated"
	if got, _ := proc.InputCapabilities(); !reflect.DeepEqual(got, []string{"text", "image"}) {
		t.Fatalf("caller changed broker capabilities: %v", got)
	}
}
