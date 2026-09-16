package agentprofile

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/amuxos/amux/internal/amuxhome"
	"github.com/amuxos/amux/internal/distribution"
	"golang.org/x/mod/semver"
)

const (
	ManagedContractMinimumVersion = "0.5.0"
	// ManagedContractRuntimeVersion remains as a source-compatible alias while
	// callers migrate to resolved runtime versions.
	ManagedContractRuntimeVersion = ManagedContractMinimumVersion
	managedContractLauncherName   = "agent-pack-contract"
	managedContractMetadataName   = "runtime.json"
	managedContractMaxRelease     = 128 << 20
	managedContractMaxManifest    = 1 << 20
	managedContractMaxAsset       = 64 << 20
	managedContractMaxBinary      = 64 << 20
)

type ManagedContractRuntime struct {
	Version    string
	Root       string
	Executable string
}

type managedContractRuntimeMetadata struct {
	Version      string `json:"version"`
	SourceURL    string `json:"source_url"`
	Platform     string `json:"platform"`
	BinarySHA256 string `json:"binary_sha256"`
	InstalledAt  string `json:"installed_at"`
}

type managedContractReleaseManifest struct {
	Version string                                 `json:"version"`
	Assets  map[string]managedContractReleaseAsset `json:"assets"`
}

type managedContractReleaseAsset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type managedContractRelease struct {
	Version string
	Binary  []byte
}

type managedContractCommandRunner func(ctx context.Context, command string, args, env []string) ([]byte, error)
type managedContractDownloader func(ctx context.Context, sourceURL string) ([]byte, error)

type managedContractRuntimeInstaller struct {
	run      managedContractCommandRunner
	rename   func(string, string) error
	download managedContractDownloader
	goos     string
	goarch   string
}

func DefaultManagedContractRuntimeRoot() string {
	root := filepath.Join(amuxhome.Dir(), "libexec", "agent-pack-contract")
	if absolute, err := filepath.Abs(root); err == nil {
		return absolute
	}
	return filepath.Clean(root)
}

func DefaultManagedContractExecutablePath() string {
	return filepath.Join(DefaultManagedContractRuntimeRoot(), managedContractLauncherName)
}

func EnsureDefaultManagedContractRuntime(ctx context.Context) (ManagedContractRuntime, error) {
	return (managedContractRuntimeInstaller{}).ensure(ctx, DefaultManagedContractRuntimeRoot())
}

func RemoveDefaultManagedContractRuntime() error {
	return os.RemoveAll(DefaultManagedContractRuntimeRoot())
}

func (i managedContractRuntimeInstaller) ensure(ctx context.Context, root string) (ManagedContractRuntime, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "." || root == string(filepath.Separator) {
		return ManagedContractRuntime{}, fmt.Errorf("invalid managed Contract runtime root %q", root)
	}
	platform := i.platform()
	sourceURL := managedContractSCMURL()
	if sourceURL == "" {
		return ManagedContractRuntime{}, fmt.Errorf("Agent Pack Contract release source is not configured; set AGENT_PACK_CONTRACT_SCM_URL to an approved release archive")
	}
	releaseBytes, err := i.downloader()(ctx, sourceURL)
	if err != nil {
		return ManagedContractRuntime{}, fmt.Errorf("download managed Agent Pack Contract from %s: %w", sourceURL, err)
	}
	release, err := unpackManagedContractRelease(releaseBytes, platform)
	if err != nil {
		return ManagedContractRuntime{}, fmt.Errorf("unpack managed Agent Pack Contract: %w", err)
	}
	contractRuntime := ManagedContractRuntime{
		Version:    release.Version,
		Root:       root,
		Executable: filepath.Join(root, managedContractLauncherName),
	}
	if managedContractRuntimeMatches(contractRuntime, platform) {
		if err := i.verifyBinaryVersion(ctx, contractRuntime.Executable, release.Version); err == nil {
			return contractRuntime, nil
		}
	}

	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return ManagedContractRuntime{}, fmt.Errorf("create managed Contract runtime parent: %w", err)
	}
	stage, err := os.MkdirTemp(parent, ".agent-pack-contract-stage-")
	if err != nil {
		return ManagedContractRuntime{}, fmt.Errorf("create managed Contract runtime stage: %w", err)
	}
	defer os.RemoveAll(stage)

	stageExecutable := filepath.Join(stage, managedContractLauncherName)
	if err := os.WriteFile(stageExecutable, release.Binary, 0o700); err != nil {
		return ManagedContractRuntime{}, fmt.Errorf("write managed Agent Pack Contract binary: %w", err)
	}
	if err := i.verifyBinaryVersion(ctx, stageExecutable, release.Version); err != nil {
		return ManagedContractRuntime{}, err
	}
	if err := writeManagedContractRuntimeMetadata(stage, sourceURL, platform, release.Version); err != nil {
		return ManagedContractRuntime{}, err
	}
	if err := i.activate(stage, root, release.Version); err != nil {
		return ManagedContractRuntime{}, err
	}
	return contractRuntime, nil
}

func (i managedContractRuntimeInstaller) platform() string {
	goos := strings.TrimSpace(i.goos)
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := strings.TrimSpace(i.goarch)
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos + "_" + goarch
}

func (i managedContractRuntimeInstaller) commandRunner() managedContractCommandRunner {
	if i.run != nil {
		return i.run
	}
	return func(ctx context.Context, command string, args, env []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, command, args...)
		cmd.Env = env
		return cmd.CombinedOutput()
	}
}

func (i managedContractRuntimeInstaller) downloader() managedContractDownloader {
	if i.download != nil {
		return i.download
	}
	return downloadManagedContractRelease
}

func (i managedContractRuntimeInstaller) verifyBinaryVersion(ctx context.Context, executable, expected string) error {
	out, err := i.commandRunner()(ctx, executable, []string{"--version"}, os.Environ())
	if err != nil {
		return commandOutputError("verify managed Agent Pack Contract binary", err, out)
	}
	actualRaw := strings.TrimSpace(string(out))
	actual, versionErr := normalizeManagedContractVersion(actualRaw)
	if versionErr != nil || actual != expected {
		return fmt.Errorf("managed Agent Pack Contract binary version = %q, want %q", actualRaw, expected)
	}
	return nil
}

func downloadManagedContractRelease(ctx context.Context, sourceURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return nil, fmt.Errorf("unexpected HTTP status %s", response.Status)
	}
	return readManagedContractBytes(response.Body, managedContractMaxRelease, "SCM release")
}

func managedContractSCMURL() string {
	if value := strings.TrimSpace(os.Getenv("AGENT_PACK_CONTRACT_SCM_URL")); value != "" {
		return value
	}
	return distribution.ContractReleaseURL
}

func unpackManagedContractRelease(release []byte, platform string) (managedContractRelease, error) {
	manifestBytes, err := managedContractTarEntry(release, "latest.json", managedContractMaxManifest)
	if err != nil {
		return managedContractRelease{}, err
	}
	var manifest managedContractReleaseManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return managedContractRelease{}, fmt.Errorf("decode latest.json: %w", err)
	}
	resolvedVersion, err := normalizeManagedContractVersion(manifest.Version)
	if err != nil {
		return managedContractRelease{}, fmt.Errorf("release version %q: %w", manifest.Version, err)
	}
	asset, ok := manifest.Assets[platform]
	if !ok {
		available := make([]string, 0, len(manifest.Assets))
		for key := range manifest.Assets {
			available = append(available, key)
		}
		sort.Strings(available)
		return managedContractRelease{}, fmt.Errorf("release has no asset for platform %s; available: %s", platform, strings.Join(available, ", "))
	}
	assetName, err := cleanManagedContractAssetName(asset.URL)
	if err != nil {
		return managedContractRelease{}, err
	}
	assetBytes, err := managedContractTarEntry(release, assetName, managedContractMaxAsset)
	if err != nil {
		return managedContractRelease{}, err
	}
	if err := verifyManagedContractDigest(assetBytes, asset.SHA256); err != nil {
		return managedContractRelease{}, fmt.Errorf("verify %s: %w", assetName, err)
	}
	binary, err := managedContractBinaryFromAsset(assetBytes, assetName)
	if err != nil {
		return managedContractRelease{}, fmt.Errorf("extract %s: %w", assetName, err)
	}
	return managedContractRelease{Version: resolvedVersion, Binary: binary}, nil
}

func normalizeManagedContractVersion(value string) (string, error) {
	value = strings.TrimSpace(value)
	normalized := value
	if !strings.HasPrefix(normalized, "v") {
		normalized = "v" + normalized
	}
	if !semver.IsValid(normalized) || semver.Prerelease(normalized) != "" {
		return "", fmt.Errorf("must be a stable semantic version")
	}
	minimum := "v" + ManagedContractMinimumVersion
	if semver.Compare(normalized, minimum) < 0 {
		return "", fmt.Errorf("below minimum supported version %s", ManagedContractMinimumVersion)
	}
	return strings.TrimPrefix(normalized, "v"), nil
}

func cleanManagedContractAssetName(value string) (string, error) {
	value = strings.TrimSpace(value)
	cleaned, err := cleanManagedContractArchiveName(value)
	if err != nil || cleaned != value || path.Base(cleaned) != cleaned || !strings.HasSuffix(cleaned, ".tar.gz") {
		return "", fmt.Errorf("invalid release asset path %q", value)
	}
	return cleaned, nil
}

func verifyManagedContractDigest(data []byte, expected string) error {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if len(expected) != sha256.Size*2 {
		return fmt.Errorf("invalid SHA-256 %q", expected)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("invalid SHA-256 %q", expected)
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != expected {
		return fmt.Errorf("SHA-256 mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

func managedContractBinaryFromAsset(asset []byte, assetName string) ([]byte, error) {
	packageDir := strings.TrimSuffix(assetName, ".tar.gz")
	binaryEntry := path.Join(packageDir, managedContractLauncherName)
	return managedContractTarEntry(asset, binaryEntry, managedContractMaxBinary)
}

func managedContractTarEntry(archive []byte, target string, maxBytes int64) ([]byte, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("open gzip archive: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read tar archive: %w", err)
		}
		name, err := cleanManagedContractArchiveName(header.Name)
		if err != nil {
			return nil, err
		}
		if name != target {
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("archive entry %s is not a regular file", target)
		}
		return readManagedContractBytes(tarReader, maxBytes, target)
	}
	return nil, fmt.Errorf("archive does not contain %s", target)
}

func cleanManagedContractArchiveName(name string) (string, error) {
	if strings.ContainsRune(name, '\\') || path.IsAbs(name) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	cleaned := path.Clean(name)
	for strings.HasPrefix(cleaned, "./") {
		cleaned = strings.TrimPrefix(cleaned, "./")
	}
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return cleaned, nil
}

func readManagedContractBytes(reader io.Reader, maxBytes int64, label string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return data, nil
}

func (i managedContractRuntimeInstaller) activate(stage, root, version string) error {
	rename := i.rename
	if rename == nil {
		rename = os.Rename
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create managed Contract runtime root: %w", err)
	}
	releases := filepath.Join(root, ".releases")
	if err := os.MkdirAll(releases, 0o700); err != nil {
		return fmt.Errorf("create managed Contract runtime releases: %w", err)
	}
	launcher := filepath.Join(root, managedContractLauncherName)
	launcherReady, err := managedContractStableLauncherReady(launcher)
	if err != nil {
		return err
	}

	releaseName := fmt.Sprintf("%s-%d", version, time.Now().UnixNano())
	release := filepath.Join(releases, releaseName)
	if err := rename(stage, release); err != nil {
		return fmt.Errorf("stage managed Contract runtime release: %w", err)
	}
	activated := false
	defer func() {
		if !activated {
			_ = os.RemoveAll(release)
		}
	}()

	currentTemp := filepath.Join(root, fmt.Sprintf(".current-%d", time.Now().UnixNano()))
	defer os.Remove(currentTemp)
	if err := os.Symlink(filepath.Join(".releases", releaseName), currentTemp); err != nil {
		return fmt.Errorf("prepare managed Contract runtime switch: %w", err)
	}
	launcherTemp := ""
	if !launcherReady {
		launcherTemp = filepath.Join(root, fmt.Sprintf(".launcher-%d", time.Now().UnixNano()))
		defer os.Remove(launcherTemp)
		if err := os.Symlink(filepath.Join("current", managedContractLauncherName), launcherTemp); err != nil {
			return fmt.Errorf("prepare managed Contract stable binary: %w", err)
		}
	}
	if err := rename(currentTemp, filepath.Join(root, "current")); err != nil {
		return fmt.Errorf("activate managed Contract runtime release: %w", err)
	}
	activated = true
	if launcherTemp != "" {
		if err := rename(launcherTemp, launcher); err != nil {
			return fmt.Errorf("activate managed Contract stable binary: %w", err)
		}
	}
	return nil
}

func managedContractStableLauncherReady(binaryPath string) (bool, error) {
	info, err := os.Lstat(binaryPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect managed Contract stable binary: %w", err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("managed Contract stable binary is a directory: %s", binaryPath)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return false, nil
	}
	target, err := os.Readlink(binaryPath)
	if err != nil {
		return false, fmt.Errorf("read managed Contract stable binary: %w", err)
	}
	return target == filepath.Join("current", managedContractLauncherName), nil
}

func writeManagedContractRuntimeMetadata(root, sourceURL, platform, version string) error {
	hash, err := fileSHA256(filepath.Join(root, managedContractLauncherName))
	if err != nil {
		return err
	}
	metadata := managedContractRuntimeMetadata{
		Version:      version,
		SourceURL:    sourceURL,
		Platform:     platform,
		BinarySHA256: hash,
		InstalledAt:  time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(root, managedContractMetadataName), data, 0o600); err != nil {
		return fmt.Errorf("write managed Contract runtime metadata: %w", err)
	}
	return nil
}

func managedContractRuntimeMatches(contractRuntime ManagedContractRuntime, platform string) bool {
	stable, err := managedContractStableLauncherReady(contractRuntime.Executable)
	if err != nil || !stable {
		return false
	}
	payloadRoot := filepath.Join(contractRuntime.Root, "current")
	data, err := os.ReadFile(filepath.Join(payloadRoot, managedContractMetadataName))
	if err != nil {
		return false
	}
	var metadata managedContractRuntimeMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return false
	}
	if metadata.Version != contractRuntime.Version || metadata.SourceURL == "" || metadata.Platform != platform {
		return false
	}
	binaryPath := filepath.Join(payloadRoot, managedContractLauncherName)
	hash, err := fileSHA256(binaryPath)
	if err != nil || hash != metadata.BinarySHA256 {
		return false
	}
	info, err := os.Stat(contractRuntime.Executable)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func fileSHA256(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func commandOutputError(action string, err error, output []byte) error {
	message := strings.TrimSpace(string(output))
	if message == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w\n%s", action, err, message)
}
