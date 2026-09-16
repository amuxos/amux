package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const credentialTestPrincipalID = "47068569-50e1-409b-b86a-7a75ddc0561a"

func TestCredentialSetupRejectsMalformedPrincipalBeforeReadingSecret(t *testing.T) {
	t.Setenv("AMUX_HOME", t.TempDir())
	cmd := newCredentialCommand(agentCredentialCommandPath)
	cmd.SetIn(strings.NewReader("must-not-be-read\n"))
	cmd.SetArgs([]string{"setup", "openai-compat", "GLM_API_KEY", "--principal", "not-a-uuid"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "principal must be a UUID") {
		t.Fatalf("credential setup error = %v, want UUID validation", err)
	}
}

func TestCredentialSetupEnrollsPrincipalAndCopiesLegacyValue(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	fake := &credentialCommandHub{}
	pb.RegisterAmuxdServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	amuxHome := t.TempDir()
	t.Setenv("AMUX_HOME", amuxHome)
	t.Setenv("AMUX_HUB_ADDR", listener.Addr().String())
	t.Setenv("AMUX_ACTOR_TOKEN", "")
	legacyDir := filepath.Join(amuxHome, "secrets")
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "openai-compat.json")
	if err := os.WriteFile(legacyPath, []byte(`{"kind":"long-lived","env":{"GLM_API_KEY":"legacy-secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	cmd := newCredentialCommand(agentCredentialCommandPath)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"setup", "openai-compat", "GLM_API_KEY", "--principal", credentialTestPrincipalID, "--from-legacy-host"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("credential setup: %v", err)
	}
	if fake.enrollmentPrincipal != credentialTestPrincipalID || fake.enrollmentRotate {
		t.Fatalf("enrollment = principal %q rotate=%v", fake.enrollmentPrincipal, fake.enrollmentRotate)
	}
	if fake.actorToken != "enrolled-token" || fake.value != "legacy-secret" {
		t.Fatalf("principal write = actor token %q value %q", fake.actorToken, fake.value)
	}
	if fake.expectedPrincipalID != credentialTestPrincipalID {
		t.Fatalf("expected principal = %q, want %s", fake.expectedPrincipalID, credentialTestPrincipalID)
	}
	if got := out.String(); !strings.Contains(got, "credential ready for Principal "+credentialTestPrincipalID) || !strings.Contains(got, "create the session again") {
		t.Fatalf("setup output = %q", got)
	}
	if strings.Contains(out.String(), "legacy-secret") || strings.Contains(errOut.String(), "legacy-secret") {
		t.Fatalf("credential setup leaked secret: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	if data, err := os.ReadFile(filepath.Join(legacyDir, "principal-enrollment.token")); err != nil || strings.TrimSpace(string(data)) != "enrolled-token" {
		t.Fatalf("enrollment token = %q err=%v", data, err)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy credential was removed: %v", err)
	}
}

func TestCredentialSetupOffersLegacyValueInInteractiveTerminal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	fake := &credentialCommandHub{}
	pb.RegisterAmuxdServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	amuxHome := t.TempDir()
	t.Setenv("AMUX_HOME", amuxHome)
	t.Setenv("AMUX_HUB_ADDR", listener.Addr().String())
	t.Setenv("AMUX_ACTOR_TOKEN", "")
	legacyDir := filepath.Join(amuxHome, "secrets")
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "openai-compat.json"), []byte(`{"kind":"long-lived","env":{"GLM_API_KEY":"legacy-secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}

	oldInputFD := credentialInputFD
	oldIsTerminal := credentialIsTerminal
	oldReadPassword := credentialReadPassword
	t.Cleanup(func() {
		credentialInputFD = oldInputFD
		credentialIsTerminal = oldIsTerminal
		credentialReadPassword = oldReadPassword
	})
	credentialInputFD = func(io.Reader) (int, bool) { return 99, true }
	credentialIsTerminal = func(fd int) bool { return fd == 99 }
	credentialReadPassword = func(int) ([]byte, error) {
		return nil, errors.New("secret prompt must not run when legacy reuse is accepted")
	}

	var out, errOut bytes.Buffer
	cmd := newCredentialCommand(agentCredentialCommandPath)
	cmd.SetIn(strings.NewReader("\n"))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"setup", "openai-compat", "GLM_API_KEY", "--principal", credentialTestPrincipalID})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("credential setup: %v", err)
	}
	if fake.value != "legacy-secret" {
		t.Fatalf("principal value = %q, want copied legacy value", fake.value)
	}
	wantPrompt := "Reuse existing legacy-host GLM_API_KEY for Principal " + credentialTestPrincipalID + "? [Y/n]: "
	if !strings.Contains(errOut.String(), wantPrompt) {
		t.Fatalf("stderr = %q, want prompt %q", errOut.String(), wantPrompt)
	}
	if strings.Contains(out.String(), "legacy-secret") || strings.Contains(errOut.String(), "legacy-secret") {
		t.Fatalf("credential setup leaked secret: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestCredentialSetShowAndList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	set := newCredentialCommand(agentCredentialCommandPath)
	set.SetIn(strings.NewReader("ak-secret\n"))
	set.SetArgs([]string{"set", "provider", "PROVIDER_AK"})
	if err := set.Execute(); err != nil {
		t.Fatalf("credential set: %v", err)
	}

	path := filepath.Join(home, ".amux", "secrets", "provider.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("credential file mode = %o, want 0600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credential file: %v", err)
	}
	var payload struct {
		Kind string            `json:"kind"`
		Env  map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("parse credential json: %v\n%s", err, data)
	}
	if payload.Kind != "long-lived" || payload.Env["PROVIDER_AK"] != "ak-secret" {
		t.Fatalf("payload = %+v, want long-lived PROVIDER_AK", payload)
	}

	var showOut bytes.Buffer
	show := newCredentialCommand(agentCredentialCommandPath)
	show.SetOut(&showOut)
	show.SetArgs([]string{"show", "provider"})
	if err := show.Execute(); err != nil {
		t.Fatalf("credential show: %v", err)
	}
	showText := showOut.String()
	for _, want := range []string{"profile: provider", "kind: long-lived", "env: PROVIDER_AK"} {
		if !strings.Contains(showText, want) {
			t.Fatalf("show output missing %q:\n%s", want, showText)
		}
	}
	if strings.Contains(showText, "ak-secret") {
		t.Fatalf("show output leaked secret:\n%s", showText)
	}

	var listOut bytes.Buffer
	list := newCredentialCommand(agentCredentialCommandPath)
	list.SetOut(&listOut)
	list.SetArgs([]string{"ls"})
	if err := list.Execute(); err != nil {
		t.Fatalf("credential ls: %v", err)
	}
	wantList := "PROFILE   KIND        ENV\nprovider  long-lived  PROVIDER_AK\n"
	if listOut.String() != wantList {
		t.Fatalf("list output = %q, want %q", listOut.String(), wantList)
	}
}

func TestCredentialSetPromptsForInteractiveSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oldInputFD := credentialInputFD
	oldIsTerminal := credentialIsTerminal
	oldReadPassword := credentialReadPassword
	t.Cleanup(func() {
		credentialInputFD = oldInputFD
		credentialIsTerminal = oldIsTerminal
		credentialReadPassword = oldReadPassword
	})
	credentialInputFD = func(r io.Reader) (int, bool) { return 99, true }
	credentialIsTerminal = func(fd int) bool { return fd == 99 }
	credentialReadPassword = func(fd int) ([]byte, error) {
		if fd != 99 {
			t.Fatalf("ReadPassword fd = %d, want 99", fd)
		}
		return []byte("example-secret"), nil
	}

	var out, errOut bytes.Buffer
	cmd := newCredentialCommand(agentCredentialCommandPath)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"set", "example-provider", "EXAMPLE_API_KEY"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("credential set: %v", err)
	}
	if got := errOut.String(); !strings.HasPrefix(got, "Enter value for EXAMPLE_API_KEY: \n") || !strings.Contains(got, "identified Feishu sessions cannot use this credential") {
		t.Fatalf("stderr = %q, want hidden prompt followed by legacy-host warning", got)
	}
	if strings.Contains(out.String(), "example-secret") || strings.Contains(errOut.String(), "example-secret") {
		t.Fatalf("credential set leaked secret: stdout=%q stderr=%q", out.String(), errOut.String())
	}
	data, err := os.ReadFile(filepath.Join(home, ".amux", "secrets", "example-provider.json"))
	if err != nil {
		t.Fatalf("read credential file: %v", err)
	}
	var payload struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("parse credential json: %v\n%s", err, data)
	}
	if got := payload.Env["EXAMPLE_API_KEY"]; got != "example-secret" {
		t.Fatalf("EXAMPLE_API_KEY = %q, want stored secret", got)
	}
}

func TestCredentialSetReportsLegacyHostScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var out, errOut bytes.Buffer
	cmd := newCredentialCommand(agentCredentialCommandPath)
	cmd.SetIn(strings.NewReader("example-secret\n"))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"set", "example-provider", "EXAMPLE_API_KEY"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("credential set: %v", err)
	}

	wantPath := filepath.Join(home, ".amux", "secrets", "example-provider.json")
	if !strings.Contains(out.String(), "scope: legacy-host ("+wantPath+")") {
		t.Fatalf("stdout = %q, want explicit legacy-host scope", out.String())
	}
	for _, want := range []string{
		"identified Feishu sessions cannot use this credential",
		"amux agent credential setup example-provider EXAMPLE_API_KEY --principal <principal-id> --from-legacy-host",
		"exact Principal ID shown by the Feishu error",
	} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("stderr = %q, want %q", errOut.String(), want)
		}
	}
	if strings.Contains(out.String(), "example-secret") || strings.Contains(errOut.String(), "example-secret") {
		t.Fatalf("credential set leaked secret: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestCredentialSetHelpUsesInvocationPath(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		path    string
		notPath string
	}{
		{name: "nested canonical", args: []string{"agent", "credential", "set", "--help"}, path: "amux agent credential", notPath: "amux credential"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newRootCmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tt.args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("%v: %v", tt.args, err)
			}
			text := out.String()
			for _, want := range []string{
				tt.path + " set openai-compat OPENAI_API_KEY",
				`printf '%s\n' "$OPENAI_API_KEY" | ` + tt.path + " set openai-compat OPENAI_API_KEY",
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("help output missing %q:\n%s", want, text)
				}
			}
			if strings.Contains(text, tt.notPath+" set") {
				t.Fatalf("help output contains wrong invocation path %q:\n%s", tt.notPath, text)
			}
		})
	}
}

func TestCredentialSetRejectsUnsafeNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, args := range [][]string{
		{"set", "../provider", "PROVIDER_AK"},
		{"set", "provider", "bad-name"},
	} {
		cmd := newCredentialCommand(agentCredentialCommandPath)
		cmd.SetIn(strings.NewReader("ak-secret\n"))
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("credential %v succeeded, want validation error", args)
		}
	}
}

func TestCredentialCommandsUseAuthenticatedPrincipalStore(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	fake := &credentialCommandHub{}
	pb.RegisterAmuxdServer(server, fake)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	amuxHome := t.TempDir()
	t.Setenv("AMUX_HOME", amuxHome)
	t.Setenv("AMUX_HUB_ADDR", listener.Addr().String())
	t.Setenv("AMUX_ACTOR_TOKEN", "actor-token")

	set := newCredentialCommand(agentCredentialCommandPath)
	var setOut, setErr bytes.Buffer
	set.SetIn(strings.NewReader("principal-secret\n"))
	set.SetOut(&setOut)
	set.SetErr(&setErr)
	set.SetArgs([]string{"set", "openai-compat", "GLM_API_KEY"})
	if err := set.Execute(); err != nil {
		t.Fatalf("credential set: %v", err)
	}
	if !strings.Contains(setOut.String(), "scope: current Principal") {
		t.Fatalf("set output = %q, want current Principal scope", setOut.String())
	}
	if setErr.Len() != 0 {
		t.Fatalf("set stderr = %q, want no legacy-host warning", setErr.String())
	}
	if fake.actorToken != "actor-token" {
		t.Fatalf("actor token = %q, want actor-token", fake.actorToken)
	}
	if fake.value != "principal-secret" {
		t.Fatalf("set value = %q, want principal-secret", fake.value)
	}
	if _, err := os.Stat(filepath.Join(amuxHome, "secrets", "openai-compat.json")); !os.IsNotExist(err) {
		t.Fatalf("credential command wrote legacy global file: %v", err)
	}

	var showOut bytes.Buffer
	show := newCredentialCommand(agentCredentialCommandPath)
	show.SetOut(&showOut)
	show.SetArgs([]string{"show", "openai-compat"})
	if err := show.Execute(); err != nil {
		t.Fatalf("credential show: %v", err)
	}
	if got := showOut.String(); !strings.Contains(got, "env: GLM_API_KEY") || strings.Contains(got, "principal-secret") {
		t.Fatalf("show output = %q", got)
	}

	var listOut bytes.Buffer
	list := newCredentialCommand(agentCredentialCommandPath)
	list.SetOut(&listOut)
	list.SetArgs([]string{"list"})
	if err := list.Execute(); err != nil {
		t.Fatalf("credential list: %v", err)
	}
	if !strings.Contains(listOut.String(), "openai-compat  long-lived  GLM_API_KEY") {
		t.Fatalf("list output = %q", listOut.String())
	}
}

type credentialCommandHub struct {
	pb.UnimplementedAmuxdServer
	actorToken          string
	value               string
	expectedPrincipalID string
	enrollmentPrincipal string
	enrollmentRotate    bool
}

func (h *credentialCommandHub) CreatePrincipalEnrollment(_ context.Context, req *pb.CreatePrincipalEnrollmentRequest) (*pb.CreatePrincipalEnrollmentResponse, error) {
	h.enrollmentPrincipal = req.PrincipalId
	h.enrollmentRotate = req.Rotate
	return &pb.CreatePrincipalEnrollmentResponse{CredentialId: "enrollment-123", Token: "enrolled-token"}, nil
}

func (h *credentialCommandHub) SetCredential(ctx context.Context, req *pb.SetCredentialRequest) (*pb.CredentialProfileMetadata, error) {
	h.captureActorToken(ctx)
	h.value = req.Value
	h.expectedPrincipalID = req.ExpectedPrincipalId
	return h.metadata(), nil
}

func (h *credentialCommandHub) GetCredential(ctx context.Context, _ *pb.GetCredentialRequest) (*pb.CredentialProfileMetadata, error) {
	h.captureActorToken(ctx)
	return h.metadata(), nil
}

func (h *credentialCommandHub) ListCredentials(ctx context.Context, _ *pb.ListCredentialsRequest) (*pb.ListCredentialsResponse, error) {
	h.captureActorToken(ctx)
	return &pb.ListCredentialsResponse{Profiles: []*pb.CredentialProfileMetadata{h.metadata()}}, nil
}

func (h *credentialCommandHub) metadata() *pb.CredentialProfileMetadata {
	return &pb.CredentialProfileMetadata{
		Profile: "openai-compat", Kind: "long-lived", EnvNames: []string{"GLM_API_KEY"}, Path: "principal-scoped",
	}
}

func (h *credentialCommandHub) captureActorToken(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("x-amux-actor-token")
	if len(values) > 0 {
		h.actorToken = values[0]
	}
}
