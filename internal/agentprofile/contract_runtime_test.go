package agentprofile

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultManagedContractRuntimePaths(t *testing.T) {
	amuxHome := filepath.Join(t.TempDir(), "amux-home")
	t.Setenv("AMUX_HOME", amuxHome)

	wantRoot := filepath.Join(amuxHome, "libexec", "agent-pack-contract")
	if got := DefaultManagedContractRuntimeRoot(); got != wantRoot {
		t.Fatalf("runtime root = %q, want %q", got, wantRoot)
	}
	if got, want := DefaultManagedContractExecutablePath(), filepath.Join(wantRoot, "agent-pack-contract"); got != want {
		t.Fatalf("runtime executable = %q, want %q", got, want)
	}
}

func TestDefaultManagedContractRuntimePathIsAbsolute(t *testing.T) {
	t.Setenv("AMUX_HOME", "relative-amux-home")

	if got := DefaultManagedContractExecutablePath(); !filepath.IsAbs(got) {
		t.Fatalf("runtime executable should be absolute, got %q", got)
	}
}

func TestManagedContractSCMURLOverride(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://example.test/contract.tar.gz")
	if got := managedContractSCMURL(); got != "https://example.test/contract.tar.gz" {
		t.Fatalf("SCM URL = %q", got)
	}
}

func TestManagedContractRuntimeInstallerInstallsLatestCompatiblePatch(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	const latestVersion = "0.5.7"
	root := filepath.Join(t.TempDir(), "agent-pack-contract")
	release := testManagedContractRelease(t, "testos", "testarch", latestVersion, []byte("go-binary"), "")
	installer := managedContractRuntimeInstaller{
		goos:   "testos",
		goarch: "testarch",
		download: func(context.Context, string) ([]byte, error) {
			return release, nil
		},
		run: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
			return []byte(latestVersion + "\n"), nil
		},
	}

	installed, err := installer.ensure(context.Background(), root)
	if err != nil {
		t.Fatalf("ensure latest compatible runtime: %v", err)
	}
	if installed.Version != latestVersion {
		t.Fatalf("runtime version = %q, want latest compatible %q", installed.Version, latestVersion)
	}
	metadataBytes, err := os.ReadFile(filepath.Join(root, "current", managedContractMetadataName))
	if err != nil {
		t.Fatal(err)
	}
	var metadata managedContractRuntimeMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Version != latestVersion {
		t.Fatalf("runtime metadata version = %q, want %q", metadata.Version, latestVersion)
	}
}

func TestManagedContractRuntimeInstallerInstallsAndReusesMatchingLatestRuntime(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	root := filepath.Join(t.TempDir(), "agent-pack-contract")
	release := testManagedContractRelease(t, "testos", "testarch", ManagedContractRuntimeVersion, []byte("go-binary"), "")
	var calls [][]string
	downloads := 0
	installer := managedContractRuntimeInstaller{
		goos:   "testos",
		goarch: "testarch",
		download: func(_ context.Context, sourceURL string) ([]byte, error) {
			downloads++
			if sourceURL != managedContractSCMURL() {
				t.Fatalf("download URL = %q", sourceURL)
			}
			return release, nil
		},
		run: func(_ context.Context, command string, args, _ []string) ([]byte, error) {
			calls = append(calls, append([]string{command}, args...))
			return []byte(ManagedContractRuntimeVersion + "\n"), nil
		},
	}

	installed, err := installer.ensure(context.Background(), root)
	if err != nil {
		t.Fatalf("ensure runtime: %v", err)
	}
	if installed.Version != ManagedContractRuntimeVersion {
		t.Fatalf("runtime version = %q, want %q", installed.Version, ManagedContractRuntimeVersion)
	}
	if installed.Executable != filepath.Join(root, managedContractLauncherName) {
		t.Fatalf("runtime executable = %q", installed.Executable)
	}
	if info, err := os.Stat(installed.Executable); err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("binary should be executable: info=%v err=%v", info, err)
	}
	if downloads != 1 {
		t.Fatalf("downloads = %d, want 1", downloads)
	}
	if len(calls) != 1 || len(calls[0]) != 2 || calls[0][1] != "--version" {
		t.Fatalf("install verification calls = %#v", calls)
	}

	metadataBytes, err := os.ReadFile(filepath.Join(root, "current", managedContractMetadataName))
	if err != nil {
		t.Fatal(err)
	}
	var metadata managedContractRuntimeMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Version != ManagedContractRuntimeVersion || metadata.SourceURL != managedContractSCMURL() || metadata.BinarySHA256 == "" {
		t.Fatalf("runtime metadata = %#v", metadata)
	}

	again, err := installer.ensure(context.Background(), root)
	if err != nil {
		t.Fatalf("ensure matching runtime: %v", err)
	}
	if again != installed {
		t.Fatalf("matching runtime = %#v, want %#v", again, installed)
	}
	if downloads != 2 {
		t.Fatalf("matching runtime latest checks = %d, want 2", downloads)
	}
	if len(calls) != 2 || calls[1][0] != installed.Executable || calls[1][1] != "--version" {
		t.Fatalf("matching runtime health calls = %#v", calls)
	}
	releases, err := os.ReadDir(filepath.Join(root, ".releases"))
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 {
		t.Fatalf("matching latest should reuse the active release, got %d releases", len(releases))
	}
}

func TestManagedContractRuntimeInstallerUpgradesToNewCompatibleLatest(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	root := filepath.Join(t.TempDir(), "runtime")
	currentVersion := ManagedContractMinimumVersion
	release := testManagedContractRelease(t, "testos", "testarch", currentVersion, []byte("go-binary-0.5.0"), "")
	installer := managedContractRuntimeInstaller{
		goos:   "testos",
		goarch: "testarch",
		download: func(context.Context, string) ([]byte, error) {
			return release, nil
		},
		run: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
			return []byte(currentVersion + "\n"), nil
		},
	}

	installed, err := installer.ensure(context.Background(), root)
	if err != nil {
		t.Fatalf("install minimum compatible runtime: %v", err)
	}
	if installed.Version != ManagedContractMinimumVersion {
		t.Fatalf("initial runtime version = %q", installed.Version)
	}

	currentVersion = "0.5.1"
	release = testManagedContractRelease(t, "testos", "testarch", currentVersion, []byte("go-binary-0.5.1"), "")
	upgraded, err := installer.ensure(context.Background(), root)
	if err != nil {
		t.Fatalf("upgrade to compatible latest: %v", err)
	}
	if upgraded.Version != currentVersion {
		t.Fatalf("upgraded runtime version = %q, want %q", upgraded.Version, currentVersion)
	}
	metadataBytes, err := os.ReadFile(filepath.Join(root, "current", managedContractMetadataName))
	if err != nil {
		t.Fatal(err)
	}
	var metadata managedContractRuntimeMetadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Version != currentVersion {
		t.Fatalf("active metadata version = %q, want %q", metadata.Version, currentVersion)
	}
}

func TestManagedContractRuntimeInstallerInstallsFutureBackwardCompatibleLatest(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	const futureVersion = "1.2.3"
	release := testManagedContractRelease(t, "testos", "testarch", futureVersion, []byte("go-binary"), "")
	installer := testManagedContractInstaller("testos", "testarch", release)
	installer.run = func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
		return []byte(futureVersion + "\n"), nil
	}

	installed, err := installer.ensure(context.Background(), filepath.Join(t.TempDir(), "runtime"))
	if err != nil {
		t.Fatalf("install future backward-compatible latest: %v", err)
	}
	if installed.Version != futureVersion {
		t.Fatalf("runtime version = %q, want %q", installed.Version, futureVersion)
	}
}

func TestManagedContractRuntimeInstallerRejectsLatestBelowMinimum(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	release := testManagedContractRelease(t, "testos", "testarch", "0.4.9", []byte("go-binary"), "")
	installer := testManagedContractInstaller("testos", "testarch", release)

	_, err := installer.ensure(context.Background(), filepath.Join(t.TempDir(), "runtime"))
	if err == nil || !strings.Contains(err.Error(), "release version") || !strings.Contains(err.Error(), ManagedContractMinimumVersion) {
		t.Fatalf("below-minimum latest error = %v", err)
	}
}

func TestManagedContractRuntimeInstallerRejectsUnsupportedPlatform(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	release := testManagedContractRelease(t, "linux", "amd64", ManagedContractRuntimeVersion, []byte("go-binary"), "")
	installer := testManagedContractInstaller("plan9", "mips", release)

	_, err := installer.ensure(context.Background(), filepath.Join(t.TempDir(), "runtime"))
	if err == nil || !strings.Contains(err.Error(), "plan9_mips") || !strings.Contains(err.Error(), "linux_amd64") {
		t.Fatalf("unsupported platform error = %v", err)
	}
}

func TestManagedContractRuntimeInstallerRejectsAssetChecksumMismatch(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	release := testManagedContractRelease(t, "testos", "testarch", ManagedContractRuntimeVersion, []byte("go-binary"), strings.Repeat("0", sha256.Size*2))
	installer := testManagedContractInstaller("testos", "testarch", release)

	_, err := installer.ensure(context.Background(), filepath.Join(t.TempDir(), "runtime"))
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("checksum error = %v", err)
	}
}

func TestManagedContractRuntimeInstallerRejectsBinaryVersionMismatch(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	release := testManagedContractRelease(t, "testos", "testarch", ManagedContractRuntimeVersion, []byte("go-binary"), "")
	installer := testManagedContractInstaller("testos", "testarch", release)
	installer.run = func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
		return []byte("0.4.2\n"), nil
	}

	_, err := installer.ensure(context.Background(), filepath.Join(t.TempDir(), "runtime"))
	if err == nil || !strings.Contains(err.Error(), "binary version") || !strings.Contains(err.Error(), "0.4.2") {
		t.Fatalf("binary version error = %v", err)
	}
}

func TestManagedContractRuntimeInstallerDownloadFailureKeepsPreviousRuntime(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	root := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "previous-runtime")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	downloadErr := errors.New("SCM unavailable")
	installer := managedContractRuntimeInstaller{
		download: func(context.Context, string) ([]byte, error) { return nil, downloadErr },
	}

	_, err := installer.ensure(context.Background(), root)
	if !errors.Is(err, downloadErr) {
		t.Fatalf("ensure error = %v, want SCM failure", err)
	}
	if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "keep" {
		t.Fatalf("previous runtime changed: data=%q err=%v", data, readErr)
	}
}

func TestManagedContractRuntimeInstallerActivationFailureKeepsPreviousRuntime(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	root := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "previous-runtime")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	release := testManagedContractRelease(t, "testos", "testarch", ManagedContractRuntimeVersion, []byte("go-binary"), "")
	activationErr := errors.New("activation failed")
	installer := testManagedContractInstaller("testos", "testarch", release)
	installer.rename = func(oldName, newName string) error {
		if strings.Contains(filepath.Base(oldName), ".current-") && newName == filepath.Join(root, "current") {
			return activationErr
		}
		return os.Rename(oldName, newName)
	}

	_, err := installer.ensure(context.Background(), root)
	if !errors.Is(err, activationErr) {
		t.Fatalf("ensure error = %v, want activation failure", err)
	}
	if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "keep" {
		t.Fatalf("previous runtime changed: data=%q err=%v", data, readErr)
	}
}

func TestManagedContractRuntimeReplacementKeepsBinaryVisibleAndRepairsFailedHealthCheck(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	root := filepath.Join(t.TempDir(), "runtime")
	release := testManagedContractRelease(t, "testos", "testarch", ManagedContractRuntimeVersion, []byte("go-binary"), "")
	installed := false
	failStableHealth := false
	downloads := 0
	observedAtomicSwitch := false
	installer := testManagedContractInstaller("testos", "testarch", release)
	installer.download = func(context.Context, string) ([]byte, error) {
		downloads++
		return release, nil
	}
	installer.run = func(_ context.Context, command string, _ []string, _ []string) ([]byte, error) {
		if command == filepath.Join(root, managedContractLauncherName) && failStableHealth {
			return []byte("broken binary"), errors.New("health check failed")
		}
		return []byte(ManagedContractRuntimeVersion + "\n"), nil
	}
	installer.rename = func(oldName, newName string) error {
		if installed && newName == filepath.Join(root, "current") {
			if _, err := os.Stat(filepath.Join(root, managedContractLauncherName)); err != nil {
				t.Fatalf("stable binary missing before switch: %v", err)
			}
			if err := os.Rename(oldName, newName); err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(root, managedContractLauncherName)); err != nil {
				t.Fatalf("stable binary missing after switch: %v", err)
			}
			observedAtomicSwitch = true
			return nil
		}
		return os.Rename(oldName, newName)
	}

	if _, err := installer.ensure(context.Background(), root); err != nil {
		t.Fatalf("initial ensure: %v", err)
	}
	installed = true
	failStableHealth = true
	if _, err := installer.ensure(context.Background(), root); err != nil {
		t.Fatalf("repair unhealthy runtime: %v", err)
	}
	if downloads != 2 {
		t.Fatalf("downloads = %d, want reinstall after failed health check", downloads)
	}
	if !observedAtomicSwitch {
		t.Fatal("replacement did not atomically switch current release")
	}
}

func TestManagedContractGoBinaryDoesNotNeedPythonOnPATH(t *testing.T) {
	t.Setenv("AGENT_PACK_CONTRACT_SCM_URL", "https://contract.example/runtime.tar.gz")
	root := filepath.Join(t.TempDir(), "runtime")
	output := filepath.Join(t.TempDir(), "output")
	script := "#!/bin/sh\n" +
		"if [ \"${1:-}\" = \"--version\" ]; then printf '%s\\n' '" + ManagedContractRuntimeVersion + "'; exit 0; fi\n" +
		"printf '%s\\n' \"$*\" > \"$CONTRACT_TEST_OUTPUT\"\n"
	release := testManagedContractRelease(t, runtime.GOOS, runtime.GOARCH, ManagedContractRuntimeVersion, []byte(script), "")
	installer := managedContractRuntimeInstaller{
		download: func(context.Context, string) ([]byte, error) { return release, nil },
	}

	installed, err := installer.ensure(context.Background(), root)
	if err != nil {
		t.Fatalf("ensure runtime: %v", err)
	}
	cmd := exec.Command(installed.Executable, "check", "--repo-root", "/tmp/repo")
	cmd.Env = []string{"PATH=/path-without-python", "CONTRACT_TEST_OUTPUT=" + output}
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run Go Contract binary with restricted PATH: %v\n%s", err, combined)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(data)); got != "check --repo-root /tmp/repo" {
		t.Fatalf("binary arguments = %q", got)
	}
}

func testManagedContractInstaller(goos, goarch string, release []byte) managedContractRuntimeInstaller {
	return managedContractRuntimeInstaller{
		goos:   goos,
		goarch: goarch,
		download: func(context.Context, string) ([]byte, error) {
			return release, nil
		},
		run: func(_ context.Context, _ string, _ []string, _ []string) ([]byte, error) {
			return []byte(ManagedContractRuntimeVersion + "\n"), nil
		},
	}
}

func testManagedContractRelease(t *testing.T, goos, goarch, version string, binary []byte, digestOverride string) []byte {
	t.Helper()
	assetName := fmt.Sprintf("agent-pack-contract_%s_%s_%s.tar.gz", version, goos, goarch)
	inner := testTarGzip(t, map[string]testTarFile{
		fmt.Sprintf("agent-pack-contract_%s_%s_%s/agent-pack-contract", version, goos, goarch): {
			mode: 0o755,
			data: binary,
		},
	})
	digest := sha256.Sum256(inner)
	digestText := hex.EncodeToString(digest[:])
	if digestOverride != "" {
		digestText = digestOverride
	}
	manifest, err := json.Marshal(map[string]any{
		"version": version,
		"assets": map[string]any{
			goos + "_" + goarch: map[string]string{
				"url":    assetName,
				"sha256": digestText,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return testTarGzip(t, map[string]testTarFile{
		"latest.json": {mode: 0o644, data: manifest},
		assetName:     {mode: 0o644, data: inner},
	})
}

type testTarFile struct {
	mode int64
	data []byte
}

func testTarGzip(t *testing.T, files map[string]testTarFile) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, file := range files {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: file.mode, Size: int64(len(file.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
