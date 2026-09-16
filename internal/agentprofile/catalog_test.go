package agentprofile

import (
	"strings"
	"testing"
)

func TestCatalogDefaultsNameAndNamespaceFromRepository(t *testing.T) {
	packs, err := normalizeCatalogPacks([]CatalogPack{{
		ID:     "one",
		GitURL: "git@example.com:org/review-tools.git/",
	}})
	if err != nil {
		t.Fatalf("normalizeCatalogPacks: %v", err)
	}
	if got := packs[0]; got.Namespace != "review-tools" || got.DisplayName != "review-tools" {
		t.Fatalf("normalized pack = %+v, want repository-derived defaults", got)
	}
}

func TestCatalogAllowsSameRepositoryNameWithExplicitNamespaces(t *testing.T) {
	packs, err := normalizeCatalogPacks([]CatalogPack{
		{ID: "one", Namespace: "org-one-tools", GitURL: "git@example.com:org-one/tools.git"},
		{ID: "two", Namespace: "org-two-tools", GitURL: "git@example.com:org-two/tools.git"},
	})
	if err != nil {
		t.Fatalf("normalizeCatalogPacks: %v", err)
	}
	if packs[0].DisplayName != "tools" || packs[1].DisplayName != "tools" {
		t.Fatalf("display names = %+v, want shared repository basename", packs)
	}
}

func TestCatalogRejectsSameRepositoryNameWithoutExplicitNamespace(t *testing.T) {
	_, err := normalizeCatalogPacks([]CatalogPack{
		{ID: "one", GitURL: "git@example.com:org-one/tools.git"},
		{ID: "two", GitURL: "git@example.com:org-two/tools.git"},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate catalog namespace "tools"`) {
		t.Fatalf("normalizeCatalogPacks error = %v, want explicit namespace requirement", err)
	}
}

func TestCatalogRejectsDuplicateGitURL(t *testing.T) {
	_, err := normalizeCatalogPacks([]CatalogPack{
		{ID: "one", Namespace: "one", GitURL: "git@example.com:org/tools.git"},
		{ID: "two", Namespace: "two", GitURL: "git@example.com:org/tools.git/"},
	})
	if err == nil {
		t.Fatal("normalizeCatalogPacks accepted duplicate Git URL")
	}
}
