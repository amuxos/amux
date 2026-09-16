package updatecheck

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLatestVersionReadsCurrentPlatformArchive(t *testing.T) {
	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	tw := tar.NewWriter(gz)
	manifest := []byte(`{"version":"v1.2.3","assets":{"darwin_arm64":{},"linux_amd64":{}}}`)
	if err := tw.WriteHeader(&tar.Header{Name: "latest.json", Mode: 0600, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-gzip")
		_, _ = w.Write(payload.Bytes())
	}))
	defer server.Close()

	version, err := LatestVersion(context.Background(), server.Client(), server.URL, "linux", "amd64")
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if version != "v1.2.3" {
		t.Fatalf("version = %q, want v1.2.3", version)
	}
}

func TestLatestVersionUsesArtifactVersionHeaderWithoutReadingArchive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Artifact-Version", "1.0.0.134")
		w.Header().Set("Content-Type", "application/x-gzip")
		_, _ = w.Write([]byte("body must not be parsed"))
	}))
	defer server.Close()

	version, err := LatestVersion(context.Background(), server.Client(), server.URL, "linux", "amd64")
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if version != "1.0.0.134" {
		t.Fatalf("version = %q, want 1.0.0.134", version)
	}
}

func TestLatestVersionRejectsMissingPlatform(t *testing.T) {
	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	tw := tar.NewWriter(gz)
	manifest := []byte(`{"version":"v1.2.3","assets":{"darwin_arm64":{}}}`)
	if err := tw.WriteHeader(&tar.Header{Name: "latest.json", Mode: 0600, Size: int64(len(manifest))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(manifest); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload.Bytes())
	}))
	defer server.Close()

	if _, err := LatestVersion(context.Background(), server.Client(), server.URL, "linux", "amd64"); err == nil {
		t.Fatal("LatestVersion missing platform = nil, want error")
	}
}
