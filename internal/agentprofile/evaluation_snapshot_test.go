package agentprofile

import (
	"github.com/amuxos/amux/internal/task"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveTaskSpecSnapshotsLocalSkillFiles(t *testing.T) {
	packRoot := t.TempDir()
	writePackContent(t, packRoot, "instructions/reviewer/AGENTS.md", "review carefully\n")
	writePackContent(t, packRoot, "skills/custom/demo/SKILL.md", "local skill\n")
	writeManifest(t, packRoot, `{
  "schema_version": "v2",
  "pack_namespace": "local",
  "profiles": {
    "reviewer": {
      "namespace": "local",
      "instructions": [
        "instructions/reviewer/AGENTS.md"
      ],
      "skills": [
        "demo"
      ],
      "subagents": [],
      "extends": null
    }
  },
  "namespaces": {
    "local": {
      "profiles": [
        "reviewer"
      ]
    }
  }
}`)
	registryPath := filepath.Join(t.TempDir(), "registry.json")
	if err := SavePackRegistry(registryPath, PackRegistry{Packs: map[string]Pack{
		ManagedLocalPackNamespace: {
			Namespace: ManagedLocalPackNamespace, SourceKind: PackSourceGit,
			ManagedBy: ManagedPackOwnerAmux, MaterializedPath: packRoot,
			CurrentRevision: strings.Repeat("a", 40),
		},
	}}); err != nil {
		t.Fatalf("SavePackRegistry: %v", err)
	}
	spec := task.Spec{Runtime: "codex", Agent: "local/reviewer", Task: "review"}
	if err := ResolveTaskSpec(&spec, registryPath); err != nil {
		t.Fatalf("ResolveTaskSpec: %v", err)
	}
	files := spec.ResolvedProfile.GetSnapshotSkillFiles()
	if len(files) != 1 || files[0].GetSkill() != "demo" || files[0].GetPath() != "SKILL.md" || string(files[0].GetData()) != "local skill\n" {
		t.Fatalf("snapshot skill files = %+v", files)
	}
}

func TestLocalTaskSnapshotCarriesSkillFilesAcrossHosts(t *testing.T) {
	packRoot := t.TempDir()
	skillRoot := filepath.Join(packRoot, "skills", "custom", "demo")
	if err := os.MkdirAll(filepath.Join(skillRoot, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "SKILL.md"), []byte("candidate skill\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillRoot, "scripts", "run.sh"), []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	profile, err := attachSnapshotSkillFiles(Profile{
		Namespace: "local", Name: "reviewer", Type: "codex", Source: SourceLocal,
		PackPath: packRoot, Skills: []string{"demo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.SnapshotSkillFiles) != 2 {
		t.Fatalf("snapshot files = %+v", profile.SnapshotSkillFiles)
	}
	workspace := t.TempDir()
	if _, err := MaterializeIntoSandbox(ToPBProfileSpec(profile), &task.Sandbox{Workspace: workspace}, nil, t.TempDir()); err != nil {
		t.Fatalf("MaterializeIntoSandbox: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, ".agents", "skills", "demo", "SKILL.md"))
	if err != nil || string(data) != "candidate skill\n" {
		t.Fatalf("projected SKILL.md = %q, err=%v", data, err)
	}
	info, err := os.Stat(filepath.Join(workspace, ".agents", "skills", "demo", "scripts", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0100 == 0 {
		t.Fatalf("projected script mode = %v", info.Mode())
	}
}

func TestSnapshotSkillProjectionRejectsTraversal(t *testing.T) {
	_, err := projectSnapshotSkillFiles(Profile{
		Namespace: "local", Name: "reviewer", Type: "codex", Skills: []string{"demo"},
		SnapshotSkillFiles: []ProfileSkillFile{{Skill: "demo", Path: "../escape", Data: []byte("bad")}},
	}, t.TempDir(), ".agents/skills")
	if err == nil {
		t.Fatal("snapshot traversal was accepted")
	}
}
