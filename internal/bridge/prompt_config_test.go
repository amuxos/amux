package bridge

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/amuxos/amux/internal/bridge/prompt"
	"github.com/amuxos/amux/internal/config"
	"github.com/spf13/viper"
)

func TestNewPromptRendererFromConfigLoadsTemplateFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	templatePath := filepath.Join(home, ".amux", "prompts", "feishu.md")
	if err := os.MkdirAll(filepath.Dir(templatePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(templatePath, []byte("CONFIG {{.UserRequest}}"), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".amux", "config.toml")
	if err := os.WriteFile(configPath, []byte("[bridge.feishu.prompt]\nshared_thread_enabled = true\ntopic_root_enabled = false\nshared_thread_template_file = \"~/.amux/prompts/feishu.md\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	viper.Reset()
	if err := config.Load(configPath); err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(viper.Reset)

	renderer := newPromptRendererFromConfig()
	got, err := renderer.RenderFeishuSharedThread(prompt.SharedThreadInput{UserRequest: "hello"})
	if err != nil {
		t.Fatalf("RenderFeishuSharedThread: %v", err)
	}

	if got != "CONFIG hello" {
		t.Fatalf("rendered prompt = %q, want config template", got)
	}
	if renderer.TopicRootEnabled() {
		t.Fatal("TopicRootEnabled = true, want configured false")
	}
}
