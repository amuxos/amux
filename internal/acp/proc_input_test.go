package acp

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
	"strings"
	"testing"
	"time"

	"github.com/amuxos/amux/internal/agentstream"
	"github.com/amuxos/amux/internal/session"
)

func TestACPInputCapabilitiesRequireExplicitImageSupport(t *testing.T) {
	for _, tc := range []struct {
		name, result string
		image        bool
		malformed    bool
	}{
		{"enabled", `{"agentCapabilities":{"promptCapabilities":{"image":true}}}`, true, false},
		{"disabled", `{"agentCapabilities":{"promptCapabilities":{"image":false}}}`, false, false},
		{"missing", `{}`, false, false},
		{"missing image", `{"agentCapabilities":{"promptCapabilities":{"audio":true}}}`, false, false},
		{"null image", `{"agentCapabilities":{"promptCapabilities":{"image":null}}}`, false, false},
		{"malformed image", `{"agentCapabilities":{"promptCapabilities":{"image":"true"}}}`, false, true},
		{"malformed prompt capabilities", `{"agentCapabilities":{"promptCapabilities":true}}`, false, true},
		{"malformed agent capabilities", `{"agentCapabilities":true}`, false, true},
		{"partial decode", `{"agentCapabilities":{"promptCapabilities":{"image":true},"loadSession":"invalid"}}`, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proc, capture := startACPInputTest(t, tc.result)
			modalities, source := proc.InputCapabilities()
			want := []string{"text"}
			if tc.image {
				want = append(want, "image")
			}
			wantSource := "acp:initialize"
			if tc.malformed {
				wantSource = "acp:default"
			}
			if !reflect.DeepEqual(modalities, want) || source != wantSource {
				t.Fatalf("capabilities = %v, %q; want %v, %q", modalities, source, want, wantSource)
			}
			modalities[0] = "mutated"
			if got, _ := proc.InputCapabilities(); !reflect.DeepEqual(got, want) {
				t.Fatalf("caller changed capabilities: %v", got)
			}
			terminals := 0
			err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
				TurnToken: "image-turn", Input: session.UserInput{Parts: []session.InputPart{{Image: acpTestImage(t)}}},
			}, func(ev agentstream.ProducerEmission) bool {
				if ev.Terminal != nil {
					terminals++
				}
				return false
			})
			if tc.image {
				if err != nil || terminals != 1 || len(capturedPrompt(t, capture)) != 1 {
					t.Fatalf("image dispatch err=%v terminals=%d", err, terminals)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "does not support image") || terminals != 0 || capturedPrompt(t, capture) != nil {
				t.Fatalf("unsupported dispatch err=%v terminals=%d", err, terminals)
			}
			// Rejection must not reserve the turn sink or damage text input.
			if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
				TurnToken: "text-turn", Input: session.TextUserInput("hello"),
			}, func(agentstream.ProducerEmission) bool { return false }); err != nil {
				t.Fatalf("text after rejected image: %v", err)
			}
		})
	}
}

func TestACPMultipartPromptPreservesContentOrder(t *testing.T) {
	for _, imageOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "mixed", true: "image-only"}[imageOnly], func(t *testing.T) {
			proc, capture := startACPInputTest(t, `{"agentCapabilities":{"promptCapabilities":{"image":true}}}`)
			img := acpTestImage(t)
			input := session.UserInput{Parts: []session.InputPart{{Image: img}}}
			imageBlock := map[string]string{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(img.Data)}
			want := []map[string]string{imageBlock}
			if !imageOnly {
				input.Parts = []session.InputPart{{Text: "before\n"}, {Image: img}, {Text: " between "}, {Image: img}, {Text: "after"}}
				want = []map[string]string{{"type": "text", "text": "before\n"}, imageBlock, {"type": "text", "text": " between "}, imageBlock, {"type": "text", "text": "after"}}
			}
			if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{TurnToken: "turn", Input: input}, func(agentstream.ProducerEmission) bool { return false }); err != nil {
				t.Fatal(err)
			}
			if got := capturedPrompt(t, capture); !reflect.DeepEqual(got, want) {
				t.Fatalf("prompt = %+v, want %+v", got, want)
			}
		})
	}
}

func TestACPImageValidationRejectsBeforeNativePrompt(t *testing.T) {
	proc, capture := startACPInputTest(t, `{"agentCapabilities":{"promptCapabilities":{"image":true}}}`)
	img := acpTestImage(t)
	img.MIMEType = "image/jpeg"
	if err := proc.DispatchTurn(context.Background(), session.TurnDispatchRequest{
		TurnToken: "invalid", Input: session.UserInput{Parts: []session.InputPart{{Image: img}}},
	}, func(agentstream.ProducerEmission) bool { t.Error("invalid image emitted an event"); return false }); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("invalid MIME error = %v", err)
	}
	if got := capturedPrompt(t, capture); got != nil {
		t.Fatalf("invalid image reached native prompt: %+v", got)
	}
}

func startACPInputTest(t *testing.T, result string) (*Proc, string) {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "requests.jsonl")
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeACPServer")
	cmd.Env = append(os.Environ(), "AMUX_FAKE_ACP=1", "AMUX_FAKE_ACP_INITIALIZE_RESULT="+result, "AMUX_FAKE_ACP_CAPTURE="+capture)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	proc, _, err := StartWithOptions(ctx, cmd, StartOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("start ACP: %v", err)
	}
	t.Cleanup(func() { _ = proc.Close() })
	return proc, capture
}

func acpTestImage(t *testing.T) *session.ImageInput {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return &session.ImageInput{Data: data.Bytes(), MIMEType: "image/png", DisplayName: "private-display-name.png"}
}

func capturedPrompt(t *testing.T, path string) []map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
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
			return request.Params.Prompt
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return nil
}
