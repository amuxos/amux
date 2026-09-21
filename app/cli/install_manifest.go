package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// A manifest resolves one version and one immutable platform URL before any
// installed files are changed. Archive distributions keep their existing path.
func prepareManifestUpdateSource(manifestPath, sourceURL, tempDir string) (string, string, error) {
	info, err := os.Stat(manifestPath)
	if err != nil {
		return "", "", err
	}
	if info.Size() > 1<<20 {
		return "", "", fmt.Errorf("release manifest is too large")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", "", err
	}
	var manifest struct {
		Version string `json:"version"`
		Assets  map[string]struct {
			URL    string `json:"url"`
			SHA256 string `json:"sha256"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", "", fmt.Errorf("decode release manifest: %w", err)
	}
	asset, ok := manifest.Assets[runtime.GOOS+"_"+runtime.GOARCH]
	if !ok || strings.TrimSpace(manifest.Version) == "" {
		return "", "", fmt.Errorf("release manifest has no version or current platform asset")
	}
	digest, err := hex.DecodeString(asset.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return "", "", fmt.Errorf("release asset requires a SHA256 checksum")
	}
	assetURL, err := url.Parse(asset.URL)
	if err != nil || asset.URL == "" {
		return "", "", fmt.Errorf("invalid release asset URL")
	}
	base, err := url.Parse(sourceURL)
	if err != nil {
		return "", "", err
	}
	if !assetURL.IsAbs() {
		assetURL = base.ResolveReference(assetURL)
	}
	if assetURL.Scheme != "https" && !(base.Scheme == "file" && assetURL.Scheme == "file") {
		return "", "", fmt.Errorf("release assets require HTTPS (or file for local manifests)")
	}
	archive := filepath.Join(tempDir, "platform.tar.gz")
	if err := downloadToFile(assetURL.String(), archive); err != nil {
		return "", "", err
	}
	f, err := os.Open(archive)
	if err != nil {
		return "", "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, f)
	closeErr := f.Close()
	if copyErr != nil {
		return "", "", copyErr
	}
	if closeErr != nil {
		return "", "", closeErr
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), asset.SHA256) {
		return "", "", fmt.Errorf("release checksum mismatch; installation unchanged")
	}
	extracted := filepath.Join(tempDir, "release")
	if err := os.MkdirAll(extracted, 0755); err != nil {
		return "", "", err
	}
	if err := extractTarGz(archive, extracted); err != nil {
		return "", "", err
	}
	sourceDir, err := findInstallSourceRoot(extracted)
	if err != nil {
		return "", "", err
	}
	if err := validateInstallSource(sourceDir); err != nil {
		return "", "", err
	}
	return sourceDir, manifest.Version, nil
}
