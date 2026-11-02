package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/config"
	"github.com/amuxos/amux/internal/principal"
	"github.com/amuxos/amux/internal/provider/codex"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestListNativeModelsUsesRuntimeEnvironmentAndRefreshes(t *testing.T) {
	testNativeModelsRuntime(t, "codex")
}

func testNativeModelsRuntime(t *testing.T, agent string) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	code := "#!" + python + `
import json,os,sys
for line in sys.stdin:
    req=json.loads(line)
    if 'id' not in req: continue
    method=req['method']
    if method=='initialize': result={}
    elif method=='account/read': result={'account':None,'requiresOpenaiAuth':False}
    elif method=='model/list': result={'data':[{'model':os.environ['NATIVE_MODEL_TEST'],'displayName':os.getcwd(),'businessMetadata':{'backend':'future-max'}}]}
    else: sys.exit(4)
    print(json.dumps({'id':req['id'],'result':result}),flush=True)
`
	if err := os.WriteFile(filepath.Join(dir, "codex-team"), []byte(code), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NATIVE_MODEL_TEST", "host-model")
	identity := principal.ExecutionIdentity{IdentityMode: principal.IdentityModeIdentified, EffectivePrincipalID: "11111111-1111-4111-8111-111111111111", OriginActorPrincipalID: "11111111-1111-4111-8111-111111111111", IsolationBackend: principal.BackendPassthrough}
	srv := &server{amuxDir: t.TempDir(), isolationGlobalEnv: []string{"NATIVE_MODEL_TEST=runtime-model"}}
	for _, name := range []string{"runtime-model", "refreshed-model"} {
		srv.isolationGlobalEnv = []string{"NATIVE_MODEL_TEST=" + name}
		result, err := srv.ListNativeModels(context.Background(), &pb.ListNativeModelsRequest{AgentType: agent, AgentExecutable: filepath.Join(dir, "codex-team"), ExecutionIdentity: principal.ToProtoIdentity(identity)})
		if err != nil {
			t.Fatal(err)
		}
		if agent == "custom" && (len(result.Models) != 1 || len(result.Models[0].Variants) != 1 || result.Models[0].Variants[0].BackendVariant != "future-max") {
			t.Fatalf("discovery metadata lost: %+v", result)
		}
		home, _ := os.UserHomeDir()
		home, _ = filepath.EvalSymlinks(home)
		if result.Status != "ready" || len(result.Models) != 1 || result.Models[0].Model != name || result.Models[0].DisplayName != home {
			t.Fatalf("wrong runtime environment: %+v", result)
		}
	}
	identity.IsolationBackend = principal.BackendLinuxDAC
	if _, err := srv.ListNativeModels(context.Background(), &pb.ListNativeModelsRequest{AgentType: agent, AgentExecutable: filepath.Join(dir, "codex-team"), ExecutionIdentity: principal.ToProtoIdentity(identity)}); err == nil {
		t.Fatal("missing isolation backend fell back to service credentials")
	}
}

func TestRegisteredNativeModelsUseRuntimeLauncherAndMetadata(t *testing.T) {
	const child = "AMUX_TEST_NATIVE_DISCOVERY_EXTENSION"
	if os.Getenv(child) != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRegisteredNativeModelsUseRuntimeLauncherAndMetadata$")
		cmd.Env = append(os.Environ(), child+"=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return
	}
	if err := config.RegisterExtension(config.Extension{AgentExecutables: map[string]string{"custom": "Custom"}}); err != nil {
		t.Fatal(err)
	}
	if err := codex.RegisterProvider("custom", codex.ProviderRegistration{DecorateNativeModels: func(models []codex.NativeModel) []codex.NativeModel {
		for i := range models {
			if string(models[i].BusinessMetadata) != `{"backend": "future-max"}` {
				t.Errorf("metadata missing: %s", models[i].BusinessMetadata)
			}
			models[i].Variants = []codex.NativeModelVariant{{DisplayName: "Future Max", BackendVariant: "future-max"}}
		}
		return models
	}}); err != nil {
		t.Fatal(err)
	}
	testNativeModelsRuntime(t, "custom")
}
