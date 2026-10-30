package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/principal"
	pb "github.com/amuxos/amux/internal/transport/pbgen"
)

func TestListNativeModelsUsesRuntimeEnvironmentAndRefreshes(t *testing.T) {
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
    elif method=='account/read': result={'account':{'type':'apiKey'},'requiresOpenaiAuth':True}
    elif method=='model/list': result={'data':[{'model':os.environ['NATIVE_MODEL_TEST'],'displayName':os.getcwd()}]}
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
		result, err := srv.ListNativeModels(context.Background(), &pb.ListNativeModelsRequest{AgentType: "codex", AgentExecutable: filepath.Join(dir, "codex-team"), ExecutionIdentity: principal.ToProtoIdentity(identity)})
		if err != nil {
			t.Fatal(err)
		}
		home, _ := os.UserHomeDir()
		home, _ = filepath.EvalSymlinks(home)
		if result.Status != "ready" || len(result.Models) != 1 || result.Models[0].Model != name || result.Models[0].DisplayName != home {
			t.Fatalf("wrong runtime environment: %+v", result)
		}
	}
	identity.IsolationBackend = principal.BackendLinuxDAC
	if _, err := srv.ListNativeModels(context.Background(), &pb.ListNativeModelsRequest{AgentType: "codex", AgentExecutable: filepath.Join(dir, "codex-team"), ExecutionIdentity: principal.ToProtoIdentity(identity)}); err == nil {
		t.Fatal("missing isolation backend fell back to service credentials")
	}
}
