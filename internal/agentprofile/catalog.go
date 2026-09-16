package agentprofile

import (
	"encoding/json"
	"fmt"
	"strings"
)

const packCatalogSchemaVersion = "v1"

// CatalogPack is an amux-maintained, discoverable Agent Pack source.
// It contains source metadata only; the Pack manifest remains authoritative.
type CatalogPack struct {
	ID          string `json:"id"`
	Namespace   string `json:"namespace,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description"`
	GitURL      string `json:"git_url"`
	Ref         string `json:"ref"`
}

type packCatalogDocument struct {
	SchemaVersion string        `json:"schema_version"`
	Packs         []CatalogPack `json:"packs"`
}

var catalogPacks = mustLoadCatalogPacks()

// CatalogPacks returns isolated copies of the Pack sources maintained by amux.
func CatalogPacks() []CatalogPack {
	return append([]CatalogPack(nil), catalogPacks...)
}

func mustLoadCatalogPacks() []CatalogPack {
	packs, err := loadCatalogPacks(embeddedPackCatalog)
	if err != nil {
		panic(fmt.Errorf("load embedded Agent Pack catalog: %w", err))
	}
	return packs
}

func loadCatalogPacks(data []byte) ([]CatalogPack, error) {
	var doc packCatalogDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if strings.TrimSpace(doc.SchemaVersion) != packCatalogSchemaVersion {
		return nil, fmt.Errorf("unsupported schema_version %q", doc.SchemaVersion)
	}
	return normalizeCatalogPacks(doc.Packs)
}

func normalizeCatalogPacks(packs []CatalogPack) ([]CatalogPack, error) {
	packs = append([]CatalogPack(nil), packs...)
	seenIDs := make(map[string]struct{}, len(packs))
	seenNamespaces := make(map[string]struct{}, len(packs))
	seenGitURLs := make(map[string]struct{}, len(packs))
	for i := range packs {
		pack := &packs[i]
		pack.ID = strings.TrimSpace(pack.ID)
		pack.Namespace = strings.TrimSpace(pack.Namespace)
		pack.DisplayName = strings.TrimSpace(pack.DisplayName)
		pack.Description = strings.TrimSpace(pack.Description)
		pack.GitURL = NormalizePackGitURL(pack.GitURL)
		pack.Ref = strings.TrimSpace(pack.Ref)
		if pack.ID == "" {
			return nil, fmt.Errorf("packs[%d].id is required", i)
		}
		if _, exists := seenIDs[pack.ID]; exists {
			return nil, fmt.Errorf("duplicate catalog id %q", pack.ID)
		}
		if pack.GitURL == "" {
			return nil, fmt.Errorf("packs[%d].git_url is required", i)
		}
		if _, exists := seenGitURLs[pack.GitURL]; exists {
			return nil, fmt.Errorf("duplicate catalog Git URL %q", pack.GitURL)
		}
		repositoryName := PackRepositoryName(pack.GitURL)
		if pack.Namespace == "" {
			pack.Namespace = repositoryName
		}
		if err := validatePackNamespace(pack.Namespace); err != nil {
			return nil, fmt.Errorf("packs[%d].namespace: %w", i, err)
		}
		if _, exists := seenNamespaces[pack.Namespace]; exists {
			return nil, fmt.Errorf("duplicate catalog namespace %q", pack.Namespace)
		}
		if pack.DisplayName == "" {
			pack.DisplayName = repositoryName
		}
		if pack.DisplayName == "" {
			pack.DisplayName = pack.Namespace
		}
		seenIDs[pack.ID] = struct{}{}
		seenNamespaces[pack.Namespace] = struct{}{}
		seenGitURLs[pack.GitURL] = struct{}{}
	}
	return packs, nil
}
