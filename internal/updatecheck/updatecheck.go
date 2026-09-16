// Package updatecheck reads the version advertised by the configured amux SCM
// release archive without installing it.
package updatecheck

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/amuxos/amux/internal/distribution"
)

func DefaultLatestURL() string { return distribution.LatestReleaseURL }

const (
	artifactVersionHeader      = "X-Artifact-Version"
	maxCompressedMetadataBytes = 8 << 20
	maxManifestBytes           = 1 << 20
	maxArchiveHeaders          = 128
)

type latestManifest struct {
	Version string                     `json:"version"`
	Assets  map[string]json.RawMessage `json:"assets"`
}

// LatestURL returns the release source shared by CLI update and Feishu checks.
func LatestURL() string {
	if value := strings.TrimSpace(os.Getenv("AMUX_SCM_LATEST_URL")); value != "" {
		return value
	}
	return DefaultLatestURL()
}

// LatestVersion reads the SCM artifact version header when available, then
// falls back to scanning latest.json for custom release sources. It does not
// extract or install any binary.
func LatestVersion(ctx context.Context, client *http.Client, rawURL, goos, goarch string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("SCM latest URL is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download latest release metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", fmt.Errorf("download latest release metadata: HTTP %s", resp.Status)
	}
	if version := strings.TrimSpace(resp.Header.Get(artifactVersionHeader)); version != "" {
		return version, nil
	}

	gz, err := gzip.NewReader(io.LimitReader(resp.Body, maxCompressedMetadataBytes))
	if err != nil {
		return "", fmt.Errorf("read latest release archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for headers := 0; headers < maxArchiveHeaders; headers++ {
		header, nextErr := tr.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return "", fmt.Errorf("scan latest release archive: %w", nextErr)
		}
		if strings.TrimPrefix(header.Name, "./") != "latest.json" {
			continue
		}
		if header.Size < 0 || header.Size > maxManifestBytes {
			return "", fmt.Errorf("latest release manifest is too large")
		}
		var manifest latestManifest
		if err := json.NewDecoder(io.LimitReader(tr, maxManifestBytes)).Decode(&manifest); err != nil {
			return "", fmt.Errorf("decode latest release manifest: %w", err)
		}
		version := strings.TrimSpace(manifest.Version)
		if version == "" {
			return "", fmt.Errorf("latest release manifest has no version")
		}
		platform := strings.TrimSpace(goos) + "_" + strings.TrimSpace(goarch)
		if _, ok := manifest.Assets[platform]; !ok {
			return "", fmt.Errorf("no amux archive for platform %s", platform)
		}
		return version, nil
	}
	return "", fmt.Errorf("latest release manifest not found")
}
