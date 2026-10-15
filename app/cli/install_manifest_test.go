package cli

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManifestUpdateVerifiesArtifactBeforeExtraction(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			payload := writeFakeInstallSource(t, "new")
			dir := t.TempDir()
			archive := filepath.Join(dir, "release.tar.gz")
			writeTarGz(t, archive, payload, installBinaryNames)
			data, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			if corrupt {
				digest[0] ^= 1
			}
			manifest, err := json.Marshal(map[string]any{
				"version": "v1.2.3", "assets": map[string]any{runtime.GOOS + "_" + runtime.GOARCH: map[string]string{
					"url": "release.tar.gz", "sha256": fmt.Sprintf("%x", digest),
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			manifestPath := filepath.Join(dir, "latest.json")
			if err := os.WriteFile(manifestPath, manifest, 0644); err != nil {
				t.Fatal(err)
			}
			source, version, cleanup, err := prepareSCMUpdateSource("file://" + manifestPath)
			if corrupt {
				if err == nil || !strings.Contains(err.Error(), "checksum mismatch") || cleanup != nil {
					t.Fatalf("corrupt artifact accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if version != "v1.2.3" {
				t.Fatalf("version = %q", version)
			}
			got, err := os.ReadFile(filepath.Join(source, "amux"))
			if err != nil || string(got) != "amux-new" {
				t.Fatalf("payload = %q, %v", got, err)
			}
		})
	}
}

func TestReleaseUpdateRollsBackBinariesAndSidecarTogether(t *testing.T) {
	for _, failSDK := range []bool{false, true} {
		t.Run(fmt.Sprint(failSDK), func(t *testing.T) {
			t.Setenv("AMUX_HOME", t.TempDir())
			source := writeFakeInstallSource(t, "new")
			binDir := writeFakeInstallSource(t, "old")
			for _, root := range []string{source, defaultAmuxDir()} {
				for _, name := range []string{"dist/index.js", "package.json", "node_modules/@anthropic-ai/claude-agent-sdk/index.js"} {
					filename := filepath.Join(root, "sidecars/claude-sdk", name)
					if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filename, []byte(root), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := replaceInstallBinaries(source, binDir, func(from, to string) error {
				if failSDK && strings.Contains(from, ".amux-stage-") && strings.HasSuffix(to, "claude-sdk") {
					return fmt.Errorf("injected activation failure")
				}
				return os.Rename(from, to)
			})
			if (err != nil) != failSDK {
				t.Fatalf("update error: %v", err)
			}
			wantBin, wantSDK := "amux-new", source
			if failSDK {
				wantBin, wantSDK = "amux-old", defaultAmuxDir()
			}
			binary, _ := os.ReadFile(filepath.Join(binDir, "amux"))
			sdk, _ := os.ReadFile(filepath.Join(defaultAmuxDir(), "sidecars/claude-sdk/dist/index.js"))
			if string(binary) != wantBin || string(sdk) != wantSDK {
				t.Fatalf("partial update: binary=%q sdk=%q", binary, sdk)
			}
			if !failSDK && runtime.GOOS != "windows" {
				// The source sidecar entry point is 0644 like tsc output; the
				// installed copy must still be executable.
				info, err := os.Stat(filepath.Join(defaultAmuxDir(), "sidecars/claude-sdk/dist/index.js"))
				if err != nil || info.Mode().Perm()&0o111 == 0 {
					t.Fatalf("installed sidecar entry point not executable: %v, %v", info, err)
				}
			}
		})
	}
}
