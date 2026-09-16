package credentialref

import (
	"strings"
	"testing"
)

func TestParseEnvRefCredential(t *testing.T) {
	ref, ok, err := ParseEnvRef("credential:github:GITHUB_TOKEN")
	if err != nil {
		t.Fatalf("ParseEnvRef: %v", err)
	}
	if !ok {
		t.Fatal("ParseEnvRef ok=false, want true")
	}
	if ref.Profile != "github" || ref.Env != "GITHUB_TOKEN" || ref.Raw != "credential:github:GITHUB_TOKEN" {
		t.Fatalf("ref = %+v, want github/GITHUB_TOKEN with raw value", ref)
	}
}

func TestParseEnvRefIgnoresOtherRefs(t *testing.T) {
	ref, ok, err := ParseEnvRef("secret:github-token")
	if err != nil {
		t.Fatalf("ParseEnvRef: %v", err)
	}
	if ok || ref != (EnvRef{}) {
		t.Fatalf("ParseEnvRef = %+v/%v, want ignored", ref, ok)
	}
}

func TestParseEnvRefRejectsMalformedCredentialRefs(t *testing.T) {
	for _, value := range []string{
		"credential",
		"credential:",
		"credential:github",
		"credential:github:",
		"credential::GITHUB_TOKEN",
		"credential:git/hub:GITHUB_TOKEN",
		"credential:github:1TOKEN",
		"credential:github:GITHUB-TOKEN",
		"credential:github:GITHUB_TOKEN:extra",
	} {
		t.Run(value, func(t *testing.T) {
			if _, _, err := ParseEnvRef(value); err == nil {
				t.Fatalf("ParseEnvRef(%q) succeeded, want error", value)
			}
		})
	}
}

func TestParseEnvRefsReturnsCredentialRefsInOrder(t *testing.T) {
	refs, err := ParseEnvRefs([]string{
		"secret:legacy",
		"credential:github:GITHUB_TOKEN",
		"credential:github:GITHUB_APP_ID",
	})
	if err != nil {
		t.Fatalf("ParseEnvRefs: %v", err)
	}
	if len(refs) != 2 || refs[0].Env != "GITHUB_TOKEN" || refs[1].Env != "GITHUB_APP_ID" {
		t.Fatalf("refs = %+v, want two github refs in order", refs)
	}
}

func TestParseEnvRefsReportsTheBadValue(t *testing.T) {
	_, err := ParseEnvRefs([]string{"credential:github:BAD-NAME"})
	if err == nil || !strings.Contains(err.Error(), "credential:github:BAD-NAME") {
		t.Fatalf("error = %v, want bad env ref value", err)
	}
}
