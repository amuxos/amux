package agentprofile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSkillsPackReadsDiskDescription(t *testing.T) {
	pack := t.TempDir()
	skillDir := filepath.Join(pack, "skills", "code-review")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: code-review\ndescription: Review code changes\n---\n\nUse this skill.\n"), 0600); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	profile := Profile{
		Source:   SourcePack,
		PackPath: pack,
		Skills:   []string{"code-review", "missing"},
	}
	skills := profile.ResolveSkills()
	if len(skills) != 2 {
		t.Fatalf("ResolveSkills() = %+v, want two entries", skills)
	}
	if skills[0].Name != "code-review" || skills[0].Description != "Review code changes" {
		t.Fatalf("code-review = %+v, want description from SKILL.md", skills[0])
	}
	if skills[1].Name != "missing" || skills[1].Description != "" {
		t.Fatalf("missing = %+v, want empty description (no skill dir)", skills[1])
	}
}

func TestResolveSkillsNoSkillsReturnsNil(t *testing.T) {
	if got := (Profile{}).ResolveSkills(); got != nil {
		t.Fatalf("ResolveSkills() = %+v, want nil", got)
	}
}
